package runtimehost

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

func (e *Engine) MonitorOnce(ctx context.Context) error {
	active, err := e.Store.Active(ctx)
	if err != nil {
		return err
	}
	sample := e.Probe(ctx, active)
	payload := map[string]any{"externalId": e.Store.Config.external(active.ID), "releaseId": active.ReleaseID, "binding": active.Binding, "health": sample.Health, "statusCode": sample.StatusCode, "latencyMs": sample.LatencyMS, "checkedAt": sample.CheckedAt}
	body, _ := json.Marshal(payload)
	if _, err = e.Store.db.ExecContext(ctx, `INSERT INTO observations(deployment_id,body) VALUES(?,?)`, active.ID, string(body)); err != nil {
		return err
	}
	// Retain the latest 10,000 samples even during a long control-plane outage.
	// Delivery reports only the latest current-activation sample, never replays
	// historical samples as fresh checks. AUTOINCREMENT survives this pruning.
	if _, err = e.Store.db.ExecContext(ctx, `DELETE FROM observations WHERE sequence <= (SELECT COALESCE(MAX(sequence),0)-10000 FROM observations)`); err != nil {
		return err
	}
	if e.Store.Config.PlatformURL == "" {
		return nil
	}
	return e.DeliverPending(ctx)
}
func (e *Engine) DeliverPending(ctx context.Context) error {
	c := e.Store.Config
	u, err := url.Parse(c.PlatformURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1"))) {
		return fmt.Errorf("monitor requires HTTPS control plane or explicit loopback development URL")
	}
	if c.WorkerTokenFile == "" {
		return fmt.Errorf("monitor requires a trusted publishing worker token file")
	}
	file, err := os.Open(c.WorkerTokenFile)
	if err != nil {
		return fmt.Errorf("monitor worker credential is unavailable")
	}
	token, err := io.ReadAll(io.LimitReader(file, 8193))
	file.Close()
	if err != nil || len(token) > 8192 || strings.TrimSpace(string(token)) == "" {
		return fmt.Errorf("invalid monitor credential")
	}
	active, err := e.Store.Active(ctx)
	if err != nil {
		return err
	}
	var sequence int64
	var data string
	err = e.Store.db.QueryRowContext(ctx, `SELECT sequence,body FROM observations WHERE deployment_id=? AND delivered=0 ORDER BY sequence DESC LIMIT 1`, active.ID).Scan(&sequence, &data)
	if err != nil {
		return err
	}
	var payload map[string]any
	if err = json.Unmarshal([]byte(data), &payload); err != nil {
		return err
	}
	payload["sequence"] = sequence
	body, _ := json.Marshal(payload)
	request, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(c.PlatformURL, "/")+"/api/v1/commands/runtime.report", bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	request.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("control plane runtime report unavailable; latest observation retained")
	}
	response.Body.Close()
	if response.StatusCode != 200 {
		return fmt.Errorf("control plane declined runtime report (HTTP %d); latest observation retained", response.StatusCode)
	}
	_, err = e.Store.db.ExecContext(ctx, `UPDATE observations SET delivered=1 WHERE sequence<=?`, sequence)
	return err
}

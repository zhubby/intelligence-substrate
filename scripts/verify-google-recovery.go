//go:build ignore

// Local TLS/OAuth/SQLite and real CLI proof. Synthetic credentials and bytes;
// no Google account, valid AAB, production API override or publication evidence.
package main

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/intelligence-substrate/platform/internal/publisher"
)

type localGoogleTransport struct {
	base   http.RoundTripper
	origin *url.URL
}

func (tr localGoogleTransport) RoundTrip(q *http.Request) (*http.Response, error) {
	if q.URL.Scheme != "https" || (q.URL.Host != "oauth2.googleapis.com" && q.URL.Host != "androidpublisher.googleapis.com") {
		return nil, fmt.Errorf("contract refused unexpected external destination")
	}
	local := q.Clone(q.Context())
	u := *q.URL
	u.Scheme, u.Host = tr.origin.Scheme, tr.origin.Host
	local.URL, local.Host = &u, tr.origin.Host
	response, err := tr.base.RoundTrip(local)
	if err == nil && strings.HasSuffix(q.URL.Path, ":commit") {
		response.Body.Close()
		response.StatusCode = 503
		response.Body = io.NopCloser(strings.NewReader(`{"error":"local-secret-provider-detail"}`))
	}
	return response, err
}

func main() {
	if err := verifyGoogleRecovery(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func verifyGoogleRecovery() (resultErr error) {
	programFlag := flag.String("publisher", "", "actual publisher executable")
	rootFlag := flag.String("state", "", "new private proof directory; refuses reuse")
	flag.Parse()
	if *programFlag == "" || *rootFlag == "" {
		return fmt.Errorf("-publisher and a new -state directory required")
	}
	program, err := filepath.Abs(*programFlag)
	if err != nil {
		return err
	}
	root, err := filepath.Abs(*rootFlag)
	if err != nil {
		return err
	}
	if err = os.Mkdir(root, 0700); err != nil {
		return err
	}
	report := map[string]any{"kind": "google-play-recovery-local-contract", "liveGoogle": false, "validAAB": false, "realDevice": false, "overallP3Complete": false, "startedAt": time.Now().UTC()}
	writeJSON := func(name string, value any) error {
		body, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(root, name), append(body, '\n'), 0600)
	}
	defer func() {
		report["passed"], report["finishedAt"] = resultErr == nil, time.Now().UTC()
		if resultErr != nil {
			report["error"] = resultErr.Error()
		}
		_ = writeJSON("result.json", report)
		fmt.Println("Evidence:", filepath.Join(root, "result.json"))
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	artifact := []byte("synthetic-contract-bytes; this file is deliberately not an Android App Bundle\n")
	digest := sha256.Sum256(artifact)
	r := publisher.Request{ProtocolVersion: "1", Action: "publish", OperationKey: strings.Repeat("a", 64), ProjectID: "project_contract", GoalID: "goal_contract", ReleaseID: "release_contract", Target: "android-google-play", Version: "1.0.0", ArtifactID: "artifact_contract", ArtifactPath: filepath.Join(root, "synthetic-artifact.bin"), ArtifactDigest: hex.EncodeToString(digest[:]), ArtifactSize: int64(len(artifact)), Commit: strings.Repeat("1", 40), Binding: strings.Repeat("b", 64)}
	if err = os.WriteFile(r.ArtifactPath, artifact, 0600); err != nil {
		return err
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return err
	}
	keyBytes, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	credentials := filepath.Join(root, "synthetic-service-account.json")
	if err = writeJSON("synthetic-service-account.json", map[string]string{"type": "service_account", "client_email": "contract@example.invalid", "private_key_id": "local-contract-key", "private_key": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyBytes})), "token_uri": "https://oauth2.googleapis.com/token"}); err != nil {
		return err
	}
	cfg := publisher.Config{StateDir: filepath.Join(root, "state"), Profiles: []publisher.Profile{{ProjectID: r.ProjectID, GoalID: r.GoalID, Target: r.Target, Driver: "google-play", AppID: "com.example.contract", Track: "production", CredentialsFile: credentials}}}
	if err = writeJSON("config.json", cfg); err != nil {
		return err
	}
	var mu sync.Mutex
	edits, deletes, uploads, tracks, commits, authentications := 0, 0, 0, 0, 0, 0
	status := "draft"
	var calls []map[string]any
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if q.TLS == nil || q.TLS.Version < 0x0303 {
			http.Error(w, "TLS required", 400)
			return
		}
		calls = append(calls, map[string]any{"method": q.Method, "path": q.URL.RequestURI(), "tlsVersion": q.TLS.Version})
		w.Header().Set("Content-Type", "application/json")
		if q.URL.Path == "/token" {
			if q.Method != "POST" || q.ParseForm() != nil || q.Form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:jwt-bearer" {
				http.Error(w, "invalid OAuth grant", 400)
				return
			}
			parts := strings.Split(q.Form.Get("assertion"), ".")
			if len(parts) != 3 {
				http.Error(w, "invalid assertion", 400)
				return
			}
			header, hErr := base64.RawURLEncoding.DecodeString(parts[0])
			claims, cErr := base64.RawURLEncoding.DecodeString(parts[1])
			signature, sErr := base64.RawURLEncoding.DecodeString(parts[2])
			var h, c map[string]any
			message := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
			if hErr != nil || cErr != nil || sErr != nil || json.Unmarshal(header, &h) != nil || json.Unmarshal(claims, &c) != nil || rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, message[:], signature) != nil || h["alg"] != "RS256" || h["kid"] != "local-contract-key" || c["iss"] != "contract@example.invalid" || c["aud"] != "https://oauth2.googleapis.com/token" || c["scope"] != "https://www.googleapis.com/auth/androidpublisher" {
				http.Error(w, "invalid signed claims", 400)
				return
			}
			issued, iOK := c["iat"].(float64)
			expiry, eOK := c["exp"].(float64)
			now := float64(time.Now().Unix())
			if !iOK || !eOK || issued > now+60 || issued < now-120 || expiry <= now || expiry-issued > 3600 {
				http.Error(w, "invalid token lifetime", 400)
				return
			}
			authentications++
			_, _ = io.WriteString(w, `{"access_token":"synthetic-private-token","token_type":"Bearer","expires_in":600}`)
			return
		}
		if q.Header.Get("Authorization") != "Bearer synthetic-private-token" {
			http.Error(w, "missing bearer", 401)
			return
		}
		base := "/androidpublisher/v3/applications/com.example.contract/edits"
		respond := func(name string, value any) {
			body, _ := json.Marshal(value)
			_ = os.WriteFile(filepath.Join(root, name), body, 0600)
			_, _ = w.Write(body)
		}
		switch {
		case q.Method == "POST" && q.URL.Path == base:
			edits++
			_ = json.NewEncoder(w).Encode(map[string]string{"id": fmt.Sprintf("edit-%d", edits)})
		case q.Method == "POST" && q.URL.Path == "/upload"+base+"/edit-1/bundles":
			body, readErr := io.ReadAll(q.Body)
			if readErr != nil || !bytes.Equal(body, artifact) || q.ContentLength != r.ArtifactSize || q.URL.RawQuery != "uploadType=media" {
				http.Error(w, "wrong approved bytes", 400)
				return
			}
			uploads++
			_ = json.NewEncoder(w).Encode(map[string]any{"versionCode": 42, "sha256": r.ArtifactDigest})
		case q.Method == "PUT" && q.URL.Path == base+"/edit-1/tracks/production":
			var body struct {
				Track    string `json:"track"`
				Releases []struct {
					Name         string   `json:"name"`
					VersionCodes []string `json:"versionCodes"`
					Status       string   `json:"status"`
				} `json:"releases"`
			}
			if json.NewDecoder(q.Body).Decode(&body) != nil || body.Track != "production" || len(body.Releases) != 1 || body.Releases[0].Name != "1.0.0" || len(body.Releases[0].VersionCodes) != 1 || body.Releases[0].VersionCodes[0] != "42" || body.Releases[0].Status != "completed" {
				http.Error(w, "wrong track payload", 400)
				return
			}
			tracks++
			_, _ = io.WriteString(w, `{"track":"production"}`)
		case q.Method == "POST" && q.URL.Path == base+"/edit-1:commit" && q.URL.RawQuery == "changesNotSentForReview=false" && uploads == 1 && tracks == 1:
			commits++
			_, _ = io.WriteString(w, `{"id":"edit-1"}`)
		case q.Method == "GET" && strings.HasSuffix(q.URL.Path, "/bundles"):
			respond("bundles-response.json", map[string]any{"kind": "androidpublisher#bundlesListResponse", "bundles": []any{map[string]any{"versionCode": 42, "sha256": r.ArtifactDigest}}})
		case q.Method == "GET" && strings.HasSuffix(q.URL.Path, "/tracks/production"):
			respond("track-"+status+"-response.json", map[string]any{"track": "production", "releases": []any{map[string]any{"name": "1.0.0", "versionCodes": []string{"42"}, "status": status}}})
		case q.Method == "DELETE" && strings.HasPrefix(q.URL.Path, base+"/edit-") && q.URL.Path != base+"/edit-1":
			deletes++
			w.WriteHeader(204)
		default:
			http.Error(w, "unapproved contract action", 400)
		}
	}))
	defer server.Close()
	e, err := publisher.Open(cfg)
	if err != nil {
		return err
	}
	defer e.Close()
	origin, _ := url.Parse(server.URL)
	localClient := &http.Client{Transport: localGoogleTransport{base: server.Client().Transport, origin: origin}, Timeout: 45 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	e.HTTP = localClient
	if _, err = e.Execute(ctx, r); err == nil || strings.Contains(err.Error(), "local-secret-provider-detail") {
		return fmt.Errorf("accepted commit response was not lost safely")
	}
	before, err := e.Inspect(ctx, r.OperationKey)
	if err != nil || before.State != "pending" || before.Receipt.VersionCode != "42" || before.Receipt.EditID != "edit-1" {
		return fmt.Errorf("accepted commit did not retain its exact pending receipt")
	}
	if err = writeJSON("before.json", before); err != nil {
		return err
	}
	// Stop the sole writer before retaining an exact pre-recovery DB image.
	// A separate reader can then recompute both snapshot digests, including the
	// original raw JSON and timestamp, without trusting the Engine's digest.
	if err = e.Close(); err != nil {
		return err
	}
	pendingDatabase, err := os.ReadFile(filepath.Join(cfg.StateDir, "publisher.db"))
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(root, "pending-publisher.db"), pendingDatabase, 0600); err != nil {
		return err
	}
	e, err = publisher.Open(cfg)
	if err != nil {
		return err
	}
	defer e.Close()
	e.HTTP = localClient
	in := publisher.GoogleReconciliationRequest{Request: r, JournalDigest: before.JournalDigest, ConfirmStoppedOperationKey: r.OperationKey, Reason: "已停止准确 publisher 并排除同应用其他写入；本机合成契约核对"}
	if err = writeJSON("recovery.json", in); err != nil {
		return err
	}
	if _, err = e.ReconcileGoogle(ctx, in); err == nil {
		return fmt.Errorf("draft release incorrectly unlocked journal")
	}
	unchanged, err := e.Inspect(ctx, r.OperationKey)
	if err != nil || unchanged.JournalDigest != before.JournalDigest {
		return fmt.Errorf("negative channel proof changed pending journal")
	}
	mu.Lock()
	status = "completed"
	mu.Unlock()
	op, err := e.ReconcileGoogle(ctx, in)
	if err != nil || op.State != "complete" || op.Result == nil || op.Result.State != "awaiting_review" || op.Result.ExternalID != "com.example.contract:42" || len(op.Receipt.GoogleReconciliations) != 1 {
		return fmt.Errorf("recovery failed or overstated channel availability: %v", err)
	}
	if err = writeJSON("operation.json", op); err != nil {
		return err
	}
	if err = e.Close(); err != nil {
		return err
	}
	if err = os.Remove(credentials); err != nil {
		return err
	}
	reopened, err := publisher.Open(cfg)
	if err != nil {
		return err
	}
	again, err := reopened.ReconcileGoogle(ctx, in)
	if err != nil || again.JournalDigest != op.JournalDigest {
		reopened.Close()
		return fmt.Errorf("credential-free reopen did not replay recovery")
	}
	result, err := reopened.Execute(ctx, r)
	closeErr := reopened.Close()
	if err != nil || closeErr != nil || result.State != "awaiting_review" {
		return fmt.Errorf("ordinary publication failed to replay recovered result")
	}
	input, _ := json.Marshal(in)
	commands := []map[string]any{}
	run := func(name string, args []string, body []byte, allowed bool) ([]byte, error) {
		cmd := exec.CommandContext(ctx, "rtk", append([]string{"proxy", program, "-config", filepath.Join(root, "config.json")}, args...)...)
		cmd.Stdin = bytes.NewReader(body)
		var out, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &stderr
		callErr := cmd.Run()
		_ = os.WriteFile(filepath.Join(root, name+".stdout"), out.Bytes(), 0600)
		_ = os.WriteFile(filepath.Join(root, name+".stderr"), stderr.Bytes(), 0600)
		code := 0
		if callErr != nil {
			code = -1
			if exit, ok := callErr.(*exec.ExitError); ok {
				code = exit.ExitCode()
			}
		}
		commands = append(commands, map[string]any{"name": name, "exitCode": code, "successExpected": allowed})
		report["commands"] = commands
		if (callErr == nil) != allowed || ctx.Err() != nil {
			return nil, fmt.Errorf("%s unexpected CLI exit %d", name, code)
		}
		return out.Bytes(), nil
	}
	for _, mode := range []string{"-status-only", "-continue-only", "-reconcile-apple", "-reconcile-store", "-renew-feeds", "-inspect", "-init-key"} {
		args := []string{"-reconcile-google", mode}
		if mode == "-inspect" || mode == "-init-key" {
			args = append(args, "not-executed")
		}
		if _, err = run("deny-mode"+mode, args, input, false); err != nil {
			return err
		}
	}
	for _, scenario := range []string{"unknown", "nested-unknown", "duplicate", "trailing", "oversized", "null", "binding", "external", "snapshot", "reason", "confirmation", "status", "continue"} {
		changed := in
		switch scenario {
		case "binding":
			changed.Request.Binding = strings.Repeat("f", 64)
		case "external":
			changed.Request.ExternalID = "com.example.contract:43"
		case "snapshot":
			changed.JournalDigest = op.JournalDigest
		case "reason":
			changed.Reason = "another assertion"
		case "confirmation":
			changed.ConfirmStoppedOperationKey = ""
		case "status", "continue":
			changed.Request.Action = scenario
		}
		body, _ := json.Marshal(changed)
		switch scenario {
		case "unknown":
			body = append([]byte(`{"editId":"other",`), body[1:]...)
		case "nested-unknown":
			body = []byte(`{"request":{"editId":"other"}}`)
		case "duplicate":
			body = append([]byte(`{"reason":"other",`), body[1:]...)
		case "trailing":
			body = append(body, []byte("\n{}")...)
		case "oversized":
			body = append(body, bytes.Repeat([]byte(" "), 65536)...)
		case "null":
			body = []byte("null")
		}
		if _, err = run("deny-"+scenario, []string{"-reconcile-google"}, body, false); err != nil {
			return err
		}
	}
	for name, args := range map[string][]string{"inspect": {"-inspect", r.OperationKey}, "replay": {"-reconcile-google"}} {
		body, err := run(name, args, input, true)
		if err != nil {
			return err
		}
		var got publisher.Operation
		if json.Unmarshal(body, &got) != nil || got.JournalDigest != op.JournalDigest {
			return fmt.Errorf("%s changed recovered snapshot", name)
		}
	}
	r.Action = "reconcile-google"
	body, _ := json.Marshal(r)
	if _, err = run("deny-worker-protocol", nil, body, false); err != nil {
		return err
	}
	mu.Lock()
	defer mu.Unlock()
	if authentications != 3 || edits != 3 || deletes != 2 || uploads != 1 || tracks != 1 || commits != 1 {
		return fmt.Errorf("unexpected external side effects or query edit leak")
	}
	if err = writeJSON("api-calls.json", calls); err != nil {
		return err
	}
	report["counts"] = map[string]int{"verifiedJWTAuthentications": authentications, "edits": edits, "deletedQueryEdits": deletes, "uploads": uploads, "trackWrites": tracks, "commits": commits}
	report["checks"] = []string{"actual TLS and RS256 OAuth assertion verification", "accepted commit response lost", "draft proof refused without journal changes", "exact bundle and single-release track proof", "query edits deleted before recovery persisted", "credential-free process restart replay", "real CLI inspection and recovery replay", "strict input and authority modes refused", "normal publication never acquires recovery action"}
	return nil
}

package publisher

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

// Only the test executable accepts a loopback origin and a synthetic token.
// The production CLI has neither override.
type googleQueryChildInput struct {
	Config      Config
	Request     Request
	Origin      string
	Certificate []byte
	ReadyFile   string
	Mode        string
}

func TestGoogleQueryGuardChildProcess(t *testing.T) {
	if os.Getenv("IS_GOOGLE_QUERY_CHILD") != "1" {
		return
	}
	var in googleQueryChildInput
	if err := json.NewDecoder(os.Stdin).Decode(&in); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(in.Origin)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || net.ParseIP(u.Hostname()) == nil || !net.ParseIP(u.Hostname()).IsLoopback() {
		t.Fatal("test helper requires an exact loopback HTTPS origin")
	}
	cert, err := x509.ParseCertificate(in.Certificate)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	e, err := Open(in.Config)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	e.apiBase = in.Origin
	e.HTTP = &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}}
	e.tokenSource = func(ctx context.Context, _ Profile) (oauth2.TokenSource, error) {
		if in.Mode == "reserved" {
			if err := os.WriteFile(in.ReadyFile, []byte("reserved-before-auth"), 0600); err != nil {
				return nil, err
			}
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "local-write-guard-token"}), nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err = e.Execute(ctx, in.Request); err != nil {
		t.Fatal(err)
	}
	t.Fatal("parent must terminate this helper while its persisted query is active")
}

func TestGoogleQueryGuardProcessDeathAndReplayEvidence(t *testing.T) {
	root := os.Getenv("IS_GOOGLE_QUERY_PROOF_DIR")
	if root == "" {
		root = t.TempDir()
	} else if !filepath.IsAbs(root) {
		t.Fatal("proof directory must be absolute")
	} else if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal("proof directory must not already exist", err)
	}
	write := func(name string, value any) {
		t.Helper()
		body, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(root, name), append(body, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
	cfg, approved, e, upstream := googleWriteFixture(t)
	defer func() { e.Close() }()
	result, err := e.Execute(context.Background(), approved)
	if err != nil {
		t.Fatal(err)
	}
	retainArtifact := func(request Request, name string) Request {
		t.Helper()
		body, err := os.ReadFile(request.ArtifactPath)
		if err != nil {
			t.Fatal(err)
		}
		request.ArtifactPath = filepath.Join(root, name)
		if err = os.WriteFile(request.ArtifactPath, body, 0600); err != nil {
			t.Fatal(err)
		}
		return request
	}
	approved = retainArtifact(approved, "original-synthetic-artifact.bin")
	r := approved
	r.Action, r.ExternalID, r.ArtifactPath = "status", result.ExternalID, ""
	next := nextGoogleRequest(t, r)
	next.Action, next.ExternalID = "publish", ""
	next = retainArtifact(next, "next-synthetic-artifact.bin")
	write("config.json", cfg)
	write("publish-request.json", approved)
	write("status-request.json", r)
	write("next-request.json", next)
	original, err := e.Inspect(context.Background(), r.OperationKey)
	if err != nil {
		t.Fatal(err)
	}
	write("original.json", original)
	snapshot := func(name string) {
		t.Helper()
		if _, err := e.db.Exec("VACUUM INTO ?", filepath.Join(root, name+"-publisher.db")); err != nil {
			t.Fatal("consistent SQLite snapshot failed", err)
		}
		op, err := e.Inspect(context.Background(), r.OperationKey)
		if err != nil || op.JournalDigest != original.JournalDigest {
			t.Fatal("query lifecycle changed the publication journal", op, err)
		}
		write(name+".json", op)
	}
	snapshot("before")
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	createdReady := make(chan struct{}, 1)
	upstream.mu.Lock()
	upstream.hook = func(q *http.Request) {
		if q.Method == "GET" {
			createdReady <- struct{}{}
			<-q.Context().Done()
		}
	}
	upstream.mu.Unlock()
	processes := []map[string]any{}
	stopChild := func(mode string) {
		t.Helper()
		executable, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		readyFile := filepath.Join(root, mode+"-child-ready")
		in := googleQueryChildInput{Config: cfg, Request: r, Origin: e.apiBase, Certificate: upstream.certificate, ReadyFile: readyFile, Mode: mode}
		body, err := json.Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.CommandContext(ctx, executable, "-test.run=^TestGoogleQueryGuardChildProcess$", "-test.count=1")
		for _, entry := range os.Environ() {
			if !strings.HasPrefix(entry, "IS_") {
				cmd.Env = append(cmd.Env, entry)
			}
		}
		cmd.Env = append(cmd.Env, "IS_GOOGLE_QUERY_CHILD=1")
		cmd.Stdin = strings.NewReader(string(body))
		log, err := os.OpenFile(filepath.Join(root, mode+"-child.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		defer log.Close()
		cmd.Stdout, cmd.Stderr = log, log
		if err = cmd.Start(); err != nil {
			t.Fatal(err)
		}
		waited := false
		defer func() {
			if !waited {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
			}
		}()
		if mode == "created" {
			select {
			case <-createdReady:
			case <-ctx.Done():
				t.Fatal("child never reached the read after its query checkpoint")
			}
		} else {
			ticker := time.NewTicker(10 * time.Millisecond)
			defer ticker.Stop()
			for {
				if body, err := os.ReadFile(readyFile); err == nil && string(body) == "reserved-before-auth" {
					break
				}
				select {
				case <-ticker.C:
				case <-ctx.Done():
					t.Fatal("child never reserved its query before authentication")
				}
			}
		}
		op, err := e.Inspect(context.Background(), r.OperationKey)
		if err != nil || op.GoogleQuery == nil || op.GoogleQuery.State != mode || !op.GoogleQuery.WindowHeld {
			t.Fatal("child lacks its durable query checkpoint", op, err)
		}
		if err = cmd.Process.Kill(); err != nil {
			t.Fatal(err)
		}
		err = cmd.Wait()
		waited = true
		if err == nil || cmd.ProcessState.Success() || cmd.ProcessState.Exited() {
			t.Fatal("helper was not terminated by a signal", err, cmd.ProcessState)
		}
		processes = append(processes, map[string]any{"stateBeforeKill": mode, "pid": cmd.Process.Pid, "processState": cmd.ProcessState.String(), "signalTerminated": true})
		base, client, tokens := e.apiBase, e.HTTP, e.tokenSource
		if err = e.Close(); err != nil {
			t.Fatal(err)
		}
		e, err = Open(cfg)
		if err != nil {
			t.Fatal(err)
		}
		e.apiBase, e.HTTP, e.tokenSource = base, client, tokens
	}
	stopChild("created")
	snapshot("stopped")
	in := stoppedGoogleQueryInput(t, e, r)
	write("cleanup.json", in)
	upstream.mu.Lock()
	upstream.hook = nil
	before := len(upstream.requests)
	upstream.mu.Unlock()
	for _, request := range []Request{r, next} {
		if _, err = e.Execute(context.Background(), request); err == nil {
			t.Fatal("process death released its uncertain edit window")
		}
	}
	upstream.mu.Lock()
	if len(upstream.requests) != before {
		t.Error("restarted contention reached the provider", upstream.requests)
	}
	upstream.mu.Unlock()
	cleaned, err := e.ReconcileGoogleQuery(context.Background(), in)
	if err != nil || cleaned.State != "cleaned" || cleaned.WindowHeld || cleaned.CleanupSource != "delete-confirmed" || cleaned.Reconciliation == nil {
		t.Fatal("exact stopped query could not delete only its own edit", cleaned, err)
	}
	snapshot("cleaned")
	ready, resume := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-resume:
		default:
			close(resume)
		}
	}()
	upstream.mu.Lock()
	upstream.hook = func(q *http.Request) {
		if q.Method == "GET" {
			close(ready)
			<-resume
		}
	}
	upstream.mu.Unlock()
	done := make(chan error, 1)
	go func() { _, err := e.Execute(ctx, r); done <- err }()
	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatal("next query never reached its read")
	}
	newQuery, err := e.Inspect(context.Background(), r.OperationKey)
	if err != nil || newQuery.GoogleQuery == nil || newQuery.GoogleQuery.QueryID == in.QueryID || !newQuery.GoogleQuery.WindowHeld {
		t.Fatal("new query did not receive its own window", newQuery, err)
	}
	upstream.mu.Lock()
	before = len(upstream.requests)
	upstream.mu.Unlock()
	again, err := e.ReconcileGoogleQuery(context.Background(), in)
	current, inspectErr := e.InspectGoogleQuery(context.Background(), newQuery.GoogleQuery.QueryID)
	if err != nil || inspectErr != nil || again.QueryDigest != cleaned.QueryDigest || current.QueryDigest != newQuery.GoogleQuery.QueryDigest || !current.WindowHeld {
		t.Fatal("old cleanup replay touched a newer active query", again, current, err, inspectErr)
	}
	upstream.mu.Lock()
	if len(upstream.requests) != before {
		t.Error("closed cleanup replay contacted the provider")
	}
	upstream.mu.Unlock()
	snapshot("replay-live")
	close(resume)
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	upstream.mu.Lock()
	upstream.hook = nil
	upstream.mu.Unlock()
	snapshot("query-finished")
	stopChild("reserved")
	snapshot("reserved")
	reserved := stoppedGoogleQueryInput(t, e, r)
	write("reserved-cleanup.json", reserved)
	q, err := e.ReconcileGoogleQuery(context.Background(), reserved)
	if err != nil || q.State != "cleaned" || q.WindowHeld || q.CleanupSource != "not-started" {
		t.Fatal("known pre-edit process death could not release its reservation", q, err)
	}
	snapshot("reserved-cleaned")
	if result, err := e.Execute(context.Background(), next); err != nil || result.ExternalID != "com.example.feedback:42" {
		t.Fatal("confirmed cleanup did not allow a distinct approved publication", result, err)
	}
	snapshot("published")
	e.HTTP.Transport = googleQueryFaultTransport{base: e.HTTP.Transport, lost: "create"}
	if _, err = e.Execute(context.Background(), r); err == nil {
		t.Fatal("lost edit creation returned a successful observation")
	}
	unknown := stoppedGoogleQueryInput(t, e, r)
	write("unknown-cleanup.json", unknown)
	if _, err = e.ReconcileGoogleQuery(context.Background(), unknown); err == nil {
		t.Fatal("unidentified created edit was force-unlocked")
	}
	snapshot("unknown")
	write("child-processes.json", processes)
	upstream.mu.Lock()
	write("api-calls.json", upstream.requests)
	if len(upstream.requests) != 15 {
		t.Error("unexpected provider calls across process death, cleanup and replay", fmt.Sprint(upstream.requests))
	}
	upstream.mu.Unlock()
	write("result.json", map[string]any{"passed": !t.Failed(), "signalTerminatedChildren": len(processes), "liveGoogle": false, "validAAB": false, "realDevice": false, "overallP3Complete": false})
}

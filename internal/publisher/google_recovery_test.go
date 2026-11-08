package publisher

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

// This TLS provider accepts the real upload/track/commit requests before the
// client loses the commit response. The artifact is synthetic, not an AAB.
type googleRecoveryFixture struct {
	mu                     sync.Mutex
	calls                  []string
	edits, deletes, writes int
	accepted               bool
	bundles, track         map[string]any
	failPath, rawPath      string
	hook                   func(*http.Request)
}

type googleRecoveryTransport struct {
	base http.RoundTripper
	lost bool
}

func (tr googleRecoveryTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	response, err := tr.base.RoundTrip(r)
	if err == nil && tr.lost && strings.HasSuffix(r.URL.Path, ":commit") {
		response.Body.Close()
		response.StatusCode = 503
		response.Body = io.NopCloser(strings.NewReader(`{"error":"private-provider-detail"}`))
	}
	return response, err
}

func googleLostCommit(t *testing.T) (Config, Request, *Engine, *googleRecoveryFixture, GoogleReconciliationRequest) {
	t.Helper()
	cfg, r := publisherFixture(t)
	cfg.Profiles = []Profile{{ProjectID: r.ProjectID, GoalID: r.GoalID, Target: "android-google-play", Driver: "google-play", AppID: "com.example.feedback", Track: "production", CredentialsFile: filepath.Join(cfg.StateDir, "absent-service-account.json")}}
	r.Target = "android-google-play"
	s := &googleRecoveryFixture{
		bundles: map[string]any{"kind": "androidpublisher#bundlesListResponse", "bundles": []any{map[string]any{"versionCode": 42, "sha1": "not-used", "sha256": r.ArtifactDigest}}},
		track:   map[string]any{"track": "production", "releases": []any{map[string]any{"name": "1.0.0", "versionCodes": []any{"42"}, "status": "completed"}}},
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		if q.TLS == nil || q.Header.Get("Authorization") != "Bearer local-contract-token" {
			t.Error("missing TLS or authentication")
			w.WriteHeader(401)
			return
		}
		s.mu.Lock()
		s.calls = append(s.calls, q.Method+" "+q.URL.RequestURI())
		hook, fail, raw := s.hook, s.failPath, s.rawPath
		s.mu.Unlock()
		if hook != nil {
			hook(q)
		}
		if strings.HasSuffix(q.URL.Path, fail) && fail != "" {
			http.Error(w, "private-provider-detail", 502)
			return
		}
		if strings.HasSuffix(q.URL.Path, raw) && raw != "" {
			_, _ = io.WriteString(w, `{"private":"private-provider-detail"} {}`)
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		base := "/androidpublisher/v3/applications/com.example.feedback/edits"
		w.Header().Set("Content-Type", "application/json")
		switch {
		case q.Method == "POST" && q.URL.Path == base:
			s.edits++
			_ = json.NewEncoder(w).Encode(map[string]string{"id": fmt.Sprintf("edit-%d", s.edits), "expiryTimeSeconds": "9999999999"})
		case q.Method == "POST" && q.URL.Path == "/upload"+base+"/edit-1/bundles":
			body, err := io.ReadAll(q.Body)
			approved, readErr := os.ReadFile(r.ArtifactPath)
			if err != nil || readErr != nil || string(body) != string(approved) || q.ContentLength != r.ArtifactSize || q.URL.RawQuery != "uploadType=media" {
				t.Error("upload did not use exact approved bytes")
			}
			s.writes++
			_ = json.NewEncoder(w).Encode(map[string]any{"versionCode": 42, "sha256": r.ArtifactDigest})
		case q.Method == "PUT" && q.URL.Path == base+"/edit-1/tracks/production":
			var track map[string]any
			if err := json.NewDecoder(q.Body).Decode(&track); err != nil {
				t.Error(err)
			}
			expected, _ := json.Marshal(s.track)
			actual, _ := json.Marshal(track)
			if string(actual) != string(expected) {
				t.Error("track write differs from approved release")
			}
			s.writes++
			_ = json.NewEncoder(w).Encode(track)
		case q.Method == "POST" && q.URL.Path == base+"/edit-1:commit":
			if s.writes != 2 || q.URL.RawQuery != "changesNotSentForReview=false" {
				t.Error("commit did not follow upload and track selection")
			}
			s.writes++
			s.accepted = true
			_, _ = io.WriteString(w, `{"id":"edit-1"}`)
		case q.Method == "GET" && strings.HasPrefix(q.URL.Path, base+"/edit-") && strings.HasSuffix(q.URL.Path, "/bundles"):
			_ = json.NewEncoder(w).Encode(s.bundles)
		case q.Method == "GET" && strings.HasPrefix(q.URL.Path, base+"/edit-") && strings.HasSuffix(q.URL.Path, "/tracks/production"):
			_ = json.NewEncoder(w).Encode(s.track)
		case q.Method == "DELETE" && strings.HasPrefix(q.URL.Path, base+"/edit-") && q.URL.Path != base+"/edit-1":
			s.deletes++
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected channel action: %s %s", q.Method, q.URL.RequestURI())
			http.NotFound(w, q)
		}
	}))
	t.Cleanup(server.Close)
	e, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	e.apiBase = server.URL
	e.HTTP = server.Client()
	e.HTTP.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	e.HTTP.Transport = googleRecoveryTransport{base: e.HTTP.Transport, lost: true}
	e.tokenSource = func(context.Context, Profile) (oauth2.TokenSource, error) {
		return oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "local-contract-token"}), nil
	}
	if _, err = e.Execute(context.Background(), r); err == nil || strings.Contains(err.Error(), "private-provider-detail") {
		t.Fatal("accepted commit did not lose its response safely", err)
	}
	op, err := e.Inspect(context.Background(), r.OperationKey)
	if err != nil || op.State != "pending" || op.Receipt.EditID != "edit-1" || op.Receipt.VersionCode != "42" {
		t.Fatal(op, err)
	}
	if _, err = e.Execute(context.Background(), r); err == nil {
		t.Fatal("uncertain publish replayed")
	}
	return cfg, r, e, s, GoogleReconciliationRequest{Request: r, JournalDigest: op.JournalDigest, ConfirmStoppedOperationKey: r.OperationKey, Reason: "Administrator stopped the original publisher; exclusive reconciliation window"}
}

func TestGoogleRecoveryAcceptedCommitIsConservativeDurableAndNeverResubmitted(t *testing.T) {
	for _, status := range []string{"completed", "halted"} {
		t.Run(status, func(t *testing.T) {
			cfg, r, e, s, in := googleLostCommit(t)
			s.track["releases"].([]any)[0].(map[string]any)["status"] = status
			op, err := e.ReconcileGoogle(context.Background(), in)
			want := "awaiting_review"
			if status == "halted" {
				want = "halted"
			}
			if err != nil || op.State != "complete" || op.Result == nil || op.Result.State != want || op.Result.ExternalID != "com.example.feedback:42" || op.Result.ContinueRequired || op.Receipt.EditID != "edit-1" || op.JournalDigest == in.JournalDigest {
				t.Fatal("recovery lost identity or overstated publication", op, err)
			}
			if len(op.Receipt.GoogleReconciliations) != 1 {
				t.Fatal("missing recovery provenance")
			}
			s.mu.Lock()
			if !s.accepted || s.writes != 3 || s.edits != 2 || s.deletes != 1 {
				t.Error("recovery repeated writes or leaked its query edit", s.calls)
			}
			calls := len(s.calls)
			s.mu.Unlock()
			if err = e.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := Open(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			// apiBase and credentials are deliberately unavailable on reopen.
			again, err := reopened.ReconcileGoogle(context.Background(), in)
			if err != nil || again.JournalDigest != op.JournalDigest {
				t.Fatal("recovery did not survive process restart", again, err)
			}
			result, err := reopened.Execute(context.Background(), r)
			if err != nil || result.State != want || result.ExternalID != "com.example.feedback:42" {
				t.Fatal("ordinary publication did not replay the recovered result", result, err)
			}
			changed := in
			changed.Reason = "Different administrator assertion"
			if _, err = reopened.ReconcileGoogle(context.Background(), changed); err == nil {
				t.Fatal("changed reason borrowed prior recovery")
			}
			if _, err = reopened.Execute(context.Background(), Request{Action: "reconcile-google"}); err == nil {
				t.Fatal("ordinary Worker protocol acquired recovery authority")
			}
			s.mu.Lock()
			defer s.mu.Unlock()
			if len(s.calls) != calls {
				t.Fatal("replay called provider")
			}
		})
	}
}

func TestGoogleRecoveryRejectsIncompleteOrForeignChannelProof(t *testing.T) {
	for _, scenario := range []string{"missing bundle", "duplicate bundle", "bundle hash", "bundle version", "track", "missing release", "release name", "release version", "multiple versions", "duplicate release", "same name foreign version", "draft", "inProgress", "unknown status", "API bundles", "API track", "trailing bundles", "trailing track", "cleanup failure"} {
		t.Run(scenario, func(t *testing.T) {
			_, r, e, s, in := googleLostCommit(t)
			bundle := s.bundles["bundles"].([]any)[0].(map[string]any)
			release := s.track["releases"].([]any)[0].(map[string]any)
			switch scenario {
			case "missing bundle":
				s.bundles["bundles"] = []any{}
			case "duplicate bundle":
				s.bundles["bundles"] = []any{bundle, bundle}
			case "bundle hash":
				bundle["sha256"] = strings.Repeat("0", 64)
			case "bundle version":
				bundle["versionCode"] = 43
			case "track":
				s.track["track"] = "internal"
			case "missing release":
				s.track["releases"] = []any{}
			case "release name":
				release["name"] = "1.0.1"
			case "release version":
				release["versionCodes"] = []any{"43"}
			case "multiple versions":
				release["versionCodes"] = []any{"42", "43"}
			case "duplicate release":
				s.track["releases"] = []any{release, release}
			case "same name foreign version":
				s.track["releases"] = []any{release, map[string]any{"name": "1.0.0", "versionCodes": []any{"43"}, "status": "completed"}}
			case "draft", "inProgress":
				release["status"] = scenario
			case "unknown status":
				release["status"] = "future-state"
			case "API bundles":
				s.failPath = "/bundles"
			case "API track":
				s.failPath = "/tracks/production"
			case "trailing bundles":
				s.rawPath = "/bundles"
			case "trailing track":
				s.rawPath = "/tracks/production"
			case "cleanup failure":
				s.failPath = "/edit-2"
			}
			if _, err := e.ReconcileGoogle(context.Background(), in); err == nil || strings.Contains(err.Error(), "private-provider-detail") {
				t.Fatal("incomplete proof accepted or sensitive provider detail leaked", err)
			}
			after, err := e.Inspect(context.Background(), r.OperationKey)
			if err != nil || after.JournalDigest != in.JournalDigest || after.State != "pending" {
				t.Fatal("failed proof changed exact pending snapshot", after, err)
			}
			s.mu.Lock()
			defer s.mu.Unlock()
			if s.writes != 3 || (scenario != "cleanup failure" && s.deletes != 1) {
				t.Fatal("failed recovery repeated release writes or leaked edit", s.calls)
			}
		})
	}
}

func TestGoogleRecoveryRejectsAuthorityAndLocalByteChangesBeforeNetwork(t *testing.T) {
	for _, scenario := range []string{"snapshot", "confirmation", "reason", "long reason", "binding", "digest", "size", "commit", "version", "destination", "track", "driver", "external", "status", "continue", "artifact", "symlink", "no edit", "no version", "leading zero", "negative version", "overflow version", "complete"} {
		t.Run(scenario, func(t *testing.T) {
			_, r, e, s, in := googleLostCommit(t)
			op, err := e.Inspect(context.Background(), r.OperationKey)
			if err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "snapshot":
				in.JournalDigest = strings.Repeat("0", 64)
			case "confirmation":
				in.ConfirmStoppedOperationKey = ""
			case "reason":
				in.Reason = " "
			case "long reason":
				in.Reason = strings.Repeat("r", 4097)
			case "binding":
				in.Request.Binding = strings.Repeat("f", 64)
			case "digest":
				in.Request.ArtifactDigest = strings.Repeat("f", 64)
			case "size":
				in.Request.ArtifactSize++
			case "commit":
				in.Request.Commit = strings.Repeat("2", 40)
			case "version":
				in.Request.Version = "2.0.0"
			case "destination":
				e.Config.Profiles[0].AppID = "com.example.other"
			case "track":
				e.Config.Profiles[0].Track = "internal"
			case "driver":
				e.Config.Profiles[0].Driver = "hosted"
			case "external":
				in.Request.ExternalID = "com.example.feedback:43"
			case "status", "continue":
				in.Request.Action = scenario
			case "artifact":
				if err = os.WriteFile(r.ArtifactPath, []byte("tampered"), 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				link := r.ArtifactPath + ".link"
				if err = os.Symlink(r.ArtifactPath, link); err != nil {
					t.Fatal(err)
				}
				in.Request.ArtifactPath = link
			case "no edit":
				op.Receipt.EditID = ""
			case "no version":
				op.Receipt.VersionCode = ""
			case "leading zero":
				op.Receipt.VersionCode = "042"
			case "negative version":
				op.Receipt.VersionCode = "-42"
			case "overflow version":
				op.Receipt.VersionCode = "99999999999999999999999"
			case "complete":
				if _, err = e.db.Exec("UPDATE operations SET state='complete' WHERE operation_key=?", r.OperationKey); err != nil {
					t.Fatal(err)
				}
			}
			switch scenario {
			case "no edit", "no version", "leading zero", "negative version", "overflow version":
				if err = e.checkpoint(context.Background(), r.OperationKey, op.Receipt); err != nil {
					t.Fatal(err)
				}
			}
			before, err := e.Inspect(context.Background(), r.OperationKey)
			if err != nil {
				t.Fatal(err)
			}
			if scenario != "snapshot" {
				in.JournalDigest = before.JournalDigest
			}
			s.mu.Lock()
			calls := len(s.calls)
			s.mu.Unlock()
			if _, err = e.ReconcileGoogle(context.Background(), in); err == nil {
				t.Fatal("changed authority or local bytes accepted")
			}
			after, err := e.Inspect(context.Background(), r.OperationKey)
			if err != nil || after.JournalDigest != before.JournalDigest {
				t.Fatal("rejected input changed journal", err)
			}
			s.mu.Lock()
			defer s.mu.Unlock()
			if len(s.calls) != calls {
				t.Fatal("invalid authority reached channel", s.calls)
			}
		})
	}
}

func TestGoogleRecoveryConcurrentConnectionsCannotOverwrite(t *testing.T) {
	cfg, r, e, s, in := googleLostCommit(t)
	second, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	second.apiBase, second.HTTP, second.tokenSource = e.apiBase, e.HTTP, e.tokenSource
	ready, release := make(chan struct{}, 1), make(chan struct{})
	s.hook = func(q *http.Request) {
		if q.Method == "GET" && strings.HasSuffix(q.URL.Path, "/edit-2/tracks/production") {
			ready <- struct{}{}
			<-release
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	first := make(chan error, 1)
	go func() { _, err := e.ReconcileGoogle(ctx, in); first <- err }()
	select {
	case <-ready:
	case <-ctx.Done():
		close(release)
		t.Fatal("first recovery never entered its query")
	}
	_, secondErr := second.ReconcileGoogle(ctx, in)
	close(release)
	firstErr := <-first
	op, err := e.Inspect(context.Background(), r.OperationKey)
	if err != nil || firstErr != nil || secondErr == nil || op.State != "complete" || len(op.Receipt.GoogleReconciliations) != 1 {
		t.Fatal("competing recovery entered the active query window", firstErr, secondErr, op, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writes != 3 || s.edits != 2 || s.deletes != 1 {
		t.Fatal("competing recovery repeated publication or leaked edits", s.calls)
	}
}

func TestGoogleRecoverySnapshotChangeAndSQLiteFailureAreAtomic(t *testing.T) {
	for _, scenario := range []string{"changed timestamp", "changed receipt", "changed result", "changed request hash", "SQL failure"} {
		t.Run(scenario, func(t *testing.T) {
			_, r, e, s, in := googleLostCommit(t)
			s.hook = func(q *http.Request) {
				if q.Method != "GET" || !strings.HasSuffix(q.URL.Path, "/tracks/production") {
					return
				}
				query := map[string]string{
					"changed timestamp":    "UPDATE operations SET updated_at='concurrent' WHERE operation_key=?",
					"changed receipt":      `UPDATE operations SET receipt='{"editId":"edit-1","versionCode":"43"}' WHERE operation_key=?`,
					"changed result":       `UPDATE operations SET result='{"externalId":"com.example.feedback:42","state":"processing"}' WHERE operation_key=?`,
					"changed request hash": "UPDATE operations SET request_hash='changed' WHERE operation_key=?",
				}[scenario]
				if scenario == "SQL failure" {
					query = `CREATE TRIGGER block_google_recovery BEFORE UPDATE ON operations WHEN NEW.state='complete' BEGIN SELECT RAISE(ABORT,'forced recovery failure'); END`
					_, err := e.db.Exec(query)
					if err != nil {
						t.Error(err)
					}
				} else if _, err := e.db.Exec(query, r.OperationKey); err != nil {
					t.Error(err)
				}
			}
			if _, err := e.ReconcileGoogle(context.Background(), in); err == nil {
				t.Fatal("concurrent snapshot or failed SQL was overwritten")
			}
			after, err := e.Inspect(context.Background(), r.OperationKey)
			if err != nil || after.State != "pending" || len(after.Receipt.GoogleReconciliations) != 0 {
				t.Fatal("partial recovery survived failed transaction", after, err)
			}
			if scenario == "SQL failure" && after.JournalDigest != in.JournalDigest {
				t.Fatal("SQL failure changed original snapshot")
			}
		})
	}
}

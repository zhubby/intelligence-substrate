package publisher

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// The transport loses an otherwise successful write response, after the TLS
// fixture has accepted the actual request. It never substitutes the engine or DB.
type appleRecoveryTransport struct {
	base  http.RoundTripper
	after func(*http.Request, *http.Response) (*http.Response, error)
}

func (t appleRecoveryTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	res, err := t.base.RoundTrip(r)
	if err != nil || t.after == nil {
		return res, err
	}
	return t.after(r, res)
}

func appleLostResponse(t *testing.T, phase string) (Config, Request, *Engine, *appleFixtureState, AppleReconciliationRequest) {
	t.Helper()
	config, r, e, state, _ := appleFixture(t)
	t.Cleanup(func() { e.Close() })
	path := "/v1/buildUploadFiles/file1"
	if phase == "review" {
		path = "/v1/reviewSubmissions/submission1"
	}
	e.HTTP.Transport = appleRecoveryTransport{base: e.HTTP.Transport, after: func(q *http.Request, res *http.Response) (*http.Response, error) {
		if q.Method == "PATCH" && q.URL.Path == path {
			res.Body.Close()
			res.StatusCode = 503
			res.Body = io.NopCloser(strings.NewReader(`{"errors":[]}`))
		}
		return res, nil
	}}
	result, err := e.Execute(context.Background(), r)
	if phase == "review" {
		if err != nil {
			t.Fatal(err)
		}
		state.mu.Lock()
		state.uploadState = "COMPLETE"
		state.mu.Unlock()
		r.Action, r.ExternalID = "continue", result.ExternalID
		_, err = e.Execute(context.Background(), r)
	}
	if err == nil {
		t.Fatal("fixture did not lose an accepted response")
	}
	op, err := e.Inspect(context.Background(), r.OperationKey)
	if err != nil || op.State != "pending" || len(op.JournalDigest) != 64 {
		t.Fatal(op, err)
	}
	wantStage := "file_commit_intent"
	if phase == "review" {
		wantStage = "review_submit_intent"
	}
	if op.Receipt.Apple.Stage != wantStage {
		t.Fatal(op.Receipt.Apple)
	}
	state.mu.Lock()
	state.uploadState = "COMPLETE"
	state.mu.Unlock()
	in := AppleReconciliationRequest{Request: r, JournalDigest: op.JournalDigest, ConfirmStoppedOperationKey: r.OperationKey, Reason: "Operator stopped the exact publisher and verified no outstanding writes"}
	return config, r, e, state, in
}

func TestAppleRecoveryAcceptedWritesAreReadOnlyAndDurable(t *testing.T) {
	for _, phase := range []string{"upload", "review"} {
		t.Run(phase, func(t *testing.T) {
			config, r, e, state, in := appleLostResponse(t, phase)
			ctx := context.Background()
			state.mu.Lock()
			before := state.writes
			state.mu.Unlock()
			op, err := e.ReconcileApple(ctx, in)
			wantStage, wantState := "processing", "processing"
			if phase == "review" {
				wantStage, wantState = "submitted", "awaiting_review"
			}
			if err != nil || op.State != "complete" || op.Result == nil || op.Result.State != wantState || op.Receipt.Apple.Stage != wantStage || op.JournalDigest == in.JournalDigest {
				t.Fatal(op, err)
			}
			if len(op.Receipt.Apple.Reconciliations) != 1 {
				t.Fatal("recovery provenance missing")
			}
			// A different process reopens real SQLite and replays without credentials
			// or network. This is a local observation, not another publication.
			e.Close()
			reopened, err := Open(config)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			reopened.HTTP.Transport = appleRecoveryTransport{after: func(*http.Request, *http.Response) (*http.Response, error) {
				t.Fatal("idempotent recovery called Apple")
				return nil, nil
			}}
			if err = os.Remove(config.Profiles[0].CredentialsFile); err != nil {
				t.Fatal(err)
			}
			again, err := reopened.ReconcileApple(ctx, in)
			if err != nil || again.JournalDigest != op.JournalDigest {
				t.Fatal(again, err)
			}
			if evidenceRoot := os.Getenv("IS_APPLE_RECOVERY_EVIDENCE_DIR"); evidenceRoot != "" {
				if err = reopened.Close(); err != nil {
					t.Fatal(err)
				}
				// Preserve an actual recovered contract journal for a later process
				// to exercise CLI parsing/inspection/replay without any Apple access.
				dir := filepath.Join(evidenceRoot, phase)
				if err = os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
				stateDir := filepath.Join(dir, "state")
				if err = os.Mkdir(stateDir, 0700); err != nil {
					t.Fatal(err)
				}
				database, err := os.ReadFile(filepath.Join(config.StateDir, "publisher.db"))
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(filepath.Join(stateDir, "publisher.db"), database, 0600); err != nil {
					t.Fatal(err)
				}
				config.StateDir = stateDir
				for name, value := range map[string]any{"config.json": config, "recovery.json": in, "operation.json": op} {
					body, err := json.MarshalIndent(value, "", "  ")
					if err != nil {
						t.Fatal(err)
					}
					if err = os.WriteFile(filepath.Join(dir, name), body, 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			if _, err = reopened.Execute(ctx, Request{Action: "reconcile"}); err == nil {
				t.Fatal("normal publication accepted recovery")
			}
			state.mu.Lock()
			defer state.mu.Unlock()
			if state.writes != before {
				t.Fatal("recovery replayed an Apple write", r.OperationKey)
			}
		})
	}
}

func TestAppleRecoveryDoesNotUnlockIncompleteOrForeignProof(t *testing.T) {
	rows := []struct {
		name, path string
		mutate     func(map[string]any)
	}{
		{"file digest", "/v1/buildUploads/upload1/buildUploadFiles", func(m map[string]any) {
			m["data"].([]any)[0].(map[string]any)["attributes"].(map[string]any)["sourceFileChecksums"] = map[string]any{"file": map[string]any{"algorithm": "MD5", "hash": strings.Repeat("0", 32)}}
		}},
		{"foreign file", "/v1/buildUploads/upload1/buildUploadFiles", func(m map[string]any) { m["data"].([]any)[0].(map[string]any)["id"] = "file-other" }},
		{"partial file list", "/v1/buildUploads/upload1/buildUploadFiles", func(m map[string]any) { m["links"] = map[string]any{"next": "https://outside.invalid/page"} }},
		{"processing upload", "/v1/buildUploads/upload1", func(m map[string]any) {
			m["data"].(map[string]any)["attributes"].(map[string]any)["state"] = map[string]any{"state": "PROCESSING"}
		}},
		{"different build", "/v1/builds/build1", func(m map[string]any) { m["data"].(map[string]any)["attributes"].(map[string]any)["version"] = "43" }},
		{"draft review", "/v1/reviewSubmissions/submission1", func(m map[string]any) {
			m["data"].(map[string]any)["attributes"].(map[string]any)["state"] = "READY_FOR_REVIEW"
		}},
		{"missing submitted date", "/v1/reviewSubmissions/submission1", func(m map[string]any) {
			delete(m["data"].(map[string]any)["attributes"].(map[string]any), "submittedDate")
		}},
		{"foreign review app", "/v1/reviewSubmissions/submission1", func(m map[string]any) {
			m["data"].(map[string]any)["relationships"].(map[string]any)["app"] = appleRelationshipFixture("apps", "2222222222")
		}},
		{"foreign review version", "/v1/reviewSubmissions/submission1", func(m map[string]any) {
			m["data"].(map[string]any)["relationships"].(map[string]any)["appStoreVersionForReview"] = appleRelationshipFixture("appStoreVersions", "version-other")
		}},
		{"foreign item", "/v1/reviewSubmissions/submission1/items", func(m map[string]any) { m["data"].([]any)[0].(map[string]any)["id"] = "item-other" }},
		{"foreign item version", "/v1/reviewSubmissions/submission1/items", func(m map[string]any) {
			m["data"].([]any)[0].(map[string]any)["relationships"].(map[string]any)["appStoreVersion"] = appleRelationshipFixture("appStoreVersions", "version-other")
		}},
		{"removed review item", "/v1/reviewSubmissions/submission1/items", func(m map[string]any) {
			m["data"].([]any)[0].(map[string]any)["attributes"].(map[string]any)["state"] = "REMOVED"
		}},
		{"draft review item", "/v1/reviewSubmissions/submission1/items", func(m map[string]any) {
			m["data"].([]any)[0].(map[string]any)["attributes"].(map[string]any)["state"] = "READY_FOR_REVIEW"
		}},
		{"unknown version state", "/v1/appStoreVersions/version1", func(m map[string]any) {
			m["data"].(map[string]any)["attributes"].(map[string]any)["appStoreState"] = "FUTURE_STATE"
		}},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			_, r, e, state, in := appleLostResponse(t, "review")
			before, _ := e.Inspect(context.Background(), r.OperationKey)
			e.HTTP.Transport = appleRecoveryTransport{base: e.HTTP.Transport, after: func(q *http.Request, res *http.Response) (*http.Response, error) {
				if q.URL.Path == row.path && q.Method == "GET" {
					var m map[string]any
					if err := json.NewDecoder(res.Body).Decode(&m); err != nil {
						t.Fatal(err)
					}
					res.Body.Close()
					row.mutate(m)
					b, _ := json.Marshal(m)
					res.Body = io.NopCloser(bytes.NewReader(b))
					res.ContentLength = int64(len(b))
				}
				return res, nil
			}}
			state.mu.Lock()
			writes := state.writes
			state.mu.Unlock()
			if _, err := e.ReconcileApple(context.Background(), in); err == nil {
				t.Fatal("incomplete or foreign evidence unlocked publisher")
			}
			after, _ := e.Inspect(context.Background(), r.OperationKey)
			if before.JournalDigest != after.JournalDigest || after.State != "pending" {
				t.Fatal("failed proof changed journal")
			}
			state.mu.Lock()
			defer state.mu.Unlock()
			if state.writes != writes {
				t.Fatal("failed recovery wrote to Apple")
			}
		})
	}
}

func TestAppleRecoveryRejectsChangedAuthorityBeforeNetwork(t *testing.T) {
	for _, scenario := range []string{"snapshot", "confirmation", "reason", "binding", "external", "destination", "stage", "artifact", "complete"} {
		t.Run(scenario, func(t *testing.T) {
			_, r, e, _, in := appleLostResponse(t, "upload")
			switch scenario {
			case "snapshot":
				in.JournalDigest = strings.Repeat("a", 64)
			case "confirmation":
				in.ConfirmStoppedOperationKey = strings.Repeat("0", 64)
			case "reason":
				in.Reason = " "
			case "binding":
				in.Request.Binding = strings.Repeat("f", 64)
			case "external":
				in.Request.ExternalID = "apple:1234567890:upload-other"
			case "destination":
				e.Config.Profiles[0].AppID = "2222222222"
			case "stage":
				op, _ := e.Inspect(context.Background(), r.OperationKey)
				op.Receipt.Apple.Stage = "file_create_intent"
				if err := e.checkpoint(context.Background(), r.OperationKey, op.Receipt); err != nil {
					t.Fatal(err)
				}
				op, _ = e.Inspect(context.Background(), r.OperationKey)
				in.JournalDigest = op.JournalDigest
			case "artifact":
				if err := os.WriteFile(r.ArtifactPath, []byte("tampered"), 0600); err != nil {
					t.Fatal(err)
				}
			case "complete":
				if _, err := e.db.Exec("UPDATE operations SET state='complete' WHERE operation_key=?", r.OperationKey); err != nil {
					t.Fatal(err)
				}
				op, _ := e.Inspect(context.Background(), r.OperationKey)
				in.JournalDigest = op.JournalDigest
			}
			before, _ := e.Inspect(context.Background(), r.OperationKey)
			e.HTTP.Transport = appleRecoveryTransport{base: e.HTTP.Transport, after: func(*http.Request, *http.Response) (*http.Response, error) {
				t.Error("invalid recovery reached provider")
				return nil, io.ErrUnexpectedEOF
			}}
			if _, err := e.ReconcileApple(context.Background(), in); err == nil {
				t.Fatal("invalid authority accepted")
			}
			after, _ := e.Inspect(context.Background(), r.OperationKey)
			if before.JournalDigest != after.JournalDigest {
				t.Fatal("invalid authority changed journal")
			}
		})
	}
}

func TestAppleRecoveryConcurrentSnapshotsCannotOverwrite(t *testing.T) {
	config, r, e, state, in := appleLostResponse(t, "upload")
	second, err := Open(config)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	second.appleBase = e.appleBase
	second.HTTP = e.HTTP
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, engine := range []*Engine{e, second} {
		wg.Add(1)
		go func(engine *Engine) {
			defer wg.Done()
			_, err := engine.ReconcileApple(context.Background(), in)
			results <- err
		}(engine)
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		}
	}
	if success < 1 {
		t.Fatal("no reconciliation completed")
	}
	op, err := e.Inspect(context.Background(), r.OperationKey)
	if err != nil || len(op.Receipt.Apple.Reconciliations) != 1 {
		t.Fatal("recovery recorded more than once", op, err)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.writes != 5 {
		t.Fatal("recovery mutated external upload", state.writes)
	}
}

func TestAppleRecoveryRejectsSnapshotChangedDuringObservation(t *testing.T) {
	_, r, e, _, in := appleLostResponse(t, "upload")
	e.HTTP.Transport = appleRecoveryTransport{base: e.HTTP.Transport, after: func(q *http.Request, res *http.Response) (*http.Response, error) {
		if q.Method == "GET" && q.URL.Path == "/v1/buildUploads/upload1/buildUploadFiles" {
			if _, err := e.db.Exec("UPDATE operations SET updated_at='changed-concurrently' WHERE operation_key=?", r.OperationKey); err != nil {
				t.Fatal(err)
			}
		}
		return res, nil
	}}
	if _, err := e.ReconcileApple(context.Background(), in); err == nil {
		t.Fatal("recovery overwrote a concurrent journal update")
	}
	op, err := e.Inspect(context.Background(), r.OperationKey)
	if err != nil || op.State != "pending" || len(op.Receipt.Apple.Reconciliations) != 0 {
		t.Fatal(op, err)
	}
}

func TestAppleRecoveryOldReplayCannotRegressLaterReview(t *testing.T) {
	_, r, e, _, in := appleLostResponse(t, "upload")
	op, err := e.ReconcileApple(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	r.Action, r.ExternalID = "continue", op.Result.ExternalID
	if result, err := e.Execute(context.Background(), r); err != nil || result.State != "awaiting_review" {
		t.Fatal(result, err)
	}
	before, _ := e.Inspect(context.Background(), r.OperationKey)
	if _, err := e.ReconcileApple(context.Background(), in); err == nil {
		t.Fatal("old recovery replay regressed later review")
	}
	after, _ := e.Inspect(context.Background(), r.OperationKey)
	if before.JournalDigest != after.JournalDigest || after.Result.State != "awaiting_review" {
		t.Fatal(after)
	}
}

func TestAppleRecoveryKeepsObservedChannelStatesDistinct(t *testing.T) {
	for _, row := range []struct{ upstream, want string }{
		{"IN_REVIEW", "awaiting_review"},
		{"PENDING_DEVELOPER_RELEASE", "awaiting_release"},
		{"READY_FOR_SALE", "published"},
		{"METADATA_REJECTED", "rejected"},
		{"REMOVED_FROM_SALE", "halted"},
	} {
		t.Run(row.upstream, func(t *testing.T) {
			_, _, e, state, in := appleLostResponse(t, "review")
			state.mu.Lock()
			state.storeState = row.upstream
			state.mu.Unlock()
			op, err := e.ReconcileApple(context.Background(), in)
			if err != nil || op.Result == nil || op.Result.State != row.want || op.Result.ContinueRequired {
				t.Fatal(op, err)
			}
		})
	}
}

func TestAppleRecoveryAfterActualLocalCompletionWriteFails(t *testing.T) {
	for _, phase := range []string{"upload", "review"} {
		t.Run(phase, func(t *testing.T) {
			_, r, e, state, _ := appleFixture(t)
			defer e.Close()
			ctx := context.Background()
			if phase == "review" {
				result, err := e.Execute(ctx, r)
				if err != nil {
					t.Fatal(err)
				}
				r.Action, r.ExternalID = "continue", result.ExternalID
			}
			if _, err := e.db.Exec(`CREATE TRIGGER fail_completion BEFORE UPDATE OF state ON operations WHEN NEW.state='complete' BEGIN SELECT RAISE(ABORT,'injected local completion failure'); END`); err != nil {
				t.Fatal(err)
			}
			state.mu.Lock()
			state.uploadState = "COMPLETE"
			state.mu.Unlock()
			if _, err := e.Execute(ctx, r); err == nil {
				t.Fatal("local completion failure was not retained")
			}
			op, err := e.Inspect(ctx, r.OperationKey)
			stage, wantState := "processing", "processing"
			if phase == "review" {
				stage, wantState = "submitted", "awaiting_review"
			}
			if err != nil || op.State != "pending" || op.Receipt.Apple.Stage != stage {
				t.Fatal(op, err)
			}
			if _, err = e.db.Exec("DROP TRIGGER fail_completion"); err != nil {
				t.Fatal(err)
			}
			state.mu.Lock()
			writes := state.writes
			state.mu.Unlock()
			in := AppleReconciliationRequest{Request: r, JournalDigest: op.JournalDigest, ConfirmStoppedOperationKey: r.OperationKey, Reason: "Synchronous original operation stopped after an actual local SQL failure"}
			recovered, err := e.ReconcileApple(ctx, in)
			if err != nil || recovered.State != "complete" || recovered.Result.State != wantState || recovered.Receipt.Apple.Reconciliations[0].FromStage != stage {
				t.Fatal(recovered, err)
			}
			state.mu.Lock()
			defer state.mu.Unlock()
			if state.writes != writes {
				t.Fatal("local completion recovery wrote to Apple")
			}
		})
	}
}

func TestAppleRecoveryLocalFailureLeavesReceiptAndResultAtomic(t *testing.T) {
	_, r, e, _, in := appleLostResponse(t, "review")
	if _, err := e.db.Exec(`CREATE TRIGGER fail_recovery BEFORE UPDATE OF state ON operations WHEN NEW.state='complete' BEGIN SELECT RAISE(ABORT,'injected atomic recovery failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ReconcileApple(context.Background(), in); err == nil {
		t.Fatal("failed recovery SQL reported success")
	}
	op, err := e.Inspect(context.Background(), r.OperationKey)
	if err != nil || op.JournalDigest != in.JournalDigest || op.State != "pending" || len(op.Receipt.Apple.Reconciliations) != 0 || op.Result == nil || op.Result.State != "processing" {
		t.Fatal("SQL failure left a partial recovery", op, err)
	}
}

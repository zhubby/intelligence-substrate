package publisher

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// Each case catches a missing positive-proof or current-ownership check. The
// rejected recovery must retain the pending operation and issue no writes.
func TestAppleScreenshotCommitRecoveryRejectsIncompleteOrChangedProof(t *testing.T) {
	for _, scenario := range []string{"incomplete-images", "uncommitted", "failed-image", "changed-image-size", "text-drift", "old-placement-drift", "catalog-drift", "manifest-drift", "png-drift", "parent-drift", "app-lock-lost", "version-lost", "successor-lost", "wrong-journal", "missing-stop", "unsupported-stage", "sql-abort", "parent-drift-during-observation"} {
		t.Run(scenario, func(t *testing.T) {
			count := 1
			if scenario == "incomplete-images" {
				count = 2
			}
			r, e, base, material, review, state, in := lostScreenshotCommit(t, count, 1)
			defer e.Close()
			ctx := context.Background()
			mustSQL := func(query string, args ...any) {
				t.Helper()
				if _, err := e.db.Exec(query, args...); err != nil {
					t.Fatal(err)
				}
			}
			state.mu.Lock()
			switch scenario {
			case "uncommitted":
				state.images["replacement1"]["state"] = "AWAITING_UPLOAD"
			case "failed-image":
				state.images["replacement1"]["state"] = "FAILED"
			case "changed-image-size":
				state.images["replacement1"]["fileSize"] = 7
			case "old-placement-drift":
				state.placements = nil
			}
			state.mu.Unlock()
			switch scenario {
			case "text-drift":
				material.mu.Lock()
				material.versionText["description"] = "Unapproved external text"
				material.mu.Unlock()
			case "catalog-drift":
				e.HTTP.Transport = appleRecoveryTransport{base: e.HTTP.Transport, after: func(q *http.Request, response *http.Response) (*http.Response, error) {
					if q.Method == "GET" && q.URL.Path == "/v1/appAssetLibraryRefData" {
						response.Body.Close()
						response.Body = io.NopCloser(strings.NewReader(`{"data":[]}`))
					}
					return response, nil
				}}
			case "manifest-drift":
				p := e.Config.Profiles[0].MetadataFile
				body, err := os.ReadFile(p)
				if err != nil || os.WriteFile(p, append(body, '\n'), 0600) != nil {
					t.Fatal("could not inject manifest drift", err)
				}
			case "png-drift":
				body, err := os.ReadFile(e.Config.Profiles[0].MetadataFile)
				var m appleMaterialManifest
				if err != nil || json.Unmarshal(body, &m) != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(m.Locales[0].Screenshots[0].Images[0].Path, []byte("different PNG bytes"), 0600); err != nil {
					t.Fatal(err)
				}
			case "parent-drift":
				mustSQL("UPDATE operations SET updated_at='changed-parent' WHERE operation_key=?", r.SourceOperationKey)
			case "app-lock-lost":
				mustSQL("DELETE FROM apple_destinations WHERE operation_key=?", r.OperationKey)
			case "version-lost":
				mustSQL("DELETE FROM apple_versions")
			case "successor-lost":
				mustSQL("DELETE FROM apple_material_revisions WHERE operation_key=?", r.OperationKey)
			case "wrong-journal":
				in.JournalDigest = strings.Repeat("f", 64)
			case "missing-stop":
				in.ConfirmStoppedOperationKey = ""
			case "unsupported-stage":
				op, err := e.Inspect(ctx, r.OperationKey)
				if err != nil {
					t.Fatal(err)
				}
				op.Receipt.Apple.Stage = "image_bytes_upload_intent"
				mustSQL("UPDATE operations SET receipt=? WHERE operation_key=?", mustAppleReceiptJSON(op.Receipt), r.OperationKey)
				fresh, err := e.Inspect(ctx, r.OperationKey)
				if err != nil {
					t.Fatal(err)
				}
				in.JournalDigest = fresh.JournalDigest
			case "sql-abort":
				mustSQL("CREATE TRIGGER refuse_media_recovery BEFORE UPDATE OF state ON operations WHEN NEW.state='complete' BEGIN SELECT RAISE(ABORT,'injected media recovery failure'); END")
			case "parent-drift-during-observation":
				var once sync.Once
				e.HTTP.Transport = appleRecoveryTransport{base: e.HTTP.Transport, after: func(q *http.Request, response *http.Response) (*http.Response, error) {
					if q.Method == "GET" && q.URL.Path == "/v1/appAssetLibraries/library1/images" && q.URL.Query().Get("filter[id]") == "replacement1" {
						once.Do(func() {
							mustSQL("UPDATE operations SET updated_at='changed-after-parent-check' WHERE operation_key=?", r.SourceOperationKey)
						})
					}
					return response, nil
				}}
			}
			before, err := e.Inspect(ctx, r.OperationKey)
			if err != nil {
				t.Fatal(err)
			}
			writes, reviewWrites, packageWrites := len(state.writes), len(review.writes), base.writes
			if _, err = e.ReconcileApple(ctx, in); err == nil {
				t.Fatal("incomplete or changed proof was accepted", scenario)
			}
			after, err := e.Inspect(ctx, r.OperationKey)
			if err != nil || after.JournalDigest != before.JournalDigest || after.State != "pending" || len(after.Receipt.Apple.Reconciliations) != 0 {
				t.Fatal("failed recovery changed the pending journal", after, err)
			}
			if len(state.writes) != writes || len(review.writes) != reviewWrites || base.writes != packageWrites || review.resolved {
				t.Fatal("failed recovery mutated the provider")
			}
			screenshotProof(t, "media-recovery-denied-"+scenario, e, r, state, review, base)
		})
	}
}

func TestAppleScreenshotCommitRecoveryReadyImagesRetainProtectedContinuation(t *testing.T) {
	r, e, base, _, review, state, in := lostScreenshotCommit(t, 1, 1)
	defer e.Close()
	state.mu.Lock()
	state.ready = true
	state.mu.Unlock()
	writes := len(state.writes)
	op, err := e.ReconcileApple(context.Background(), in)
	if err != nil || op.Result == nil || op.Result.State != "processing" || op.Result.ContinueRequired || op.Receipt.Apple.Stage != "material_images_processing" {
		t.Fatal("ready media was not restored as a separately gated wait", op, err)
	}
	if len(state.writes) != writes || len(review.writes) != 0 || review.resolved || len(state.placements) != 1 {
		t.Fatal("ready recovery replaced display or resubmitted without continuation")
	}
	screenshotProof(t, "media-recovery-ready-wait", e, r, state, review, base)
}

func TestAppleScreenshotCommitRecoveryConcurrentEnginesRecordOneTransition(t *testing.T) {
	r, e, base, _, review, state, in := lostScreenshotCommit(t, 1, 1)
	defer e.Close()
	two, err := Open(e.Config)
	if err != nil {
		t.Fatal(err)
	}
	defer two.Close()
	two.appleBase = e.appleBase
	arrived, release := make(chan struct{}, 2), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	client := *e.HTTP
	client.Transport = appleRecoveryTransport{base: e.HTTP.Transport, after: func(q *http.Request, response *http.Response) (*http.Response, error) {
		if q.Method == "GET" && q.URL.Path == "/v1/appAssetLibraries/library1/images" && q.URL.Query().Get("filter[id]") == "replacement1" {
			arrived <- struct{}{}
			<-release
		}
		return response, nil
	}}
	e.HTTP, two.HTTP = &client, &client
	results := make(chan error, 2)
	writes := len(state.writes)
	for _, engine := range []*Engine{e, two} {
		go func(engine *Engine) {
			_, err := engine.ReconcileApple(context.Background(), in)
			results <- err
		}(engine)
	}
	for range 2 {
		select {
		case <-arrived:
		case err := <-results:
			t.Fatal("reconciliation failed before the shared CAS", err)
		case <-time.After(10 * time.Second):
			t.Fatal("reconciliations did not reach the barrier")
		}
	}
	once.Do(func() { close(release) })
	successes := 0
	for range 2 {
		if err := <-results; err == nil {
			successes++
		}
	}
	op, err := e.Inspect(context.Background(), r.OperationKey)
	if err != nil || successes != 1 || op.State != "complete" || op.Result.State != "processing" || len(op.Receipt.Apple.Reconciliations) != 1 {
		t.Fatal("two engines did not make one atomic recovery transition", successes, op, err)
	}
	if len(state.writes) != writes || len(review.writes) != 0 {
		t.Fatal("concurrent recovery wrote to Apple")
	}
	screenshotProof(t, "media-recovery-concurrent", e, r, state, review, base)
}

func TestAppleScreenshotCommitRecoveryRecoversLostLocalCompletion(t *testing.T) {
	r, e, base, _, review, state, in := lostScreenshotCommit(t, 1, 1)
	defer e.Close()
	ctx := context.Background()
	op, err := e.Inspect(ctx, r.OperationKey)
	if err != nil {
		t.Fatal(err)
	}
	// This is the real final checkpoint used immediately before appleFinish.
	if err = e.appleCheckpoint(ctx, r, op.Receipt.Apple, "material_images_processing"); err != nil {
		t.Fatal(err)
	}
	pending, err := e.Inspect(ctx, r.OperationKey)
	if err != nil {
		t.Fatal(err)
	}
	in.JournalDigest = pending.JournalDigest
	writes := len(state.writes)
	recovered, err := e.ReconcileApple(ctx, in)
	if err != nil || recovered.State != "complete" || recovered.Result.State != "processing" || recovered.Result.ContinueRequired || len(state.writes) != writes || len(review.writes) != 0 {
		t.Fatal("confirmed final checkpoint could not restore its durable wait", recovered, err)
	}
	screenshotProof(t, "media-recovery-local-completion", e, r, state, review, base)
}

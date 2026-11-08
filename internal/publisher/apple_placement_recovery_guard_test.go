package publisher

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Removing complete-display, exact-source or ownership validation permits one
// of these literal invalid observations to finish the pending journal.
func TestApplePlacementRecoveryRejectsIncompleteDisplayAndChangedAuthority(t *testing.T) {
	for _, scenario := range []string{"missing-placement-id", "duplicate-placement-id", "wrong-order", "wrong-placement-image", "extra-placement", "missing-placement", "image-not-ready", "review-item-drift", "review-already-resolved", "material-drift", "source-drift", "source-during-observation", "app-lock-lost", "version-lost", "successor-lost", "unsupported-stage", "sql-abort"} {
		t.Run(scenario, func(t *testing.T) {
			r, e, base, _, review, state, in := lostScreenshotPlacement(t, "order")
			defer e.Close()
			ctx := context.Background()
			op, err := e.Inspect(ctx, r.OperationKey)
			if err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "missing-placement-id":
				op.Receipt.Apple.Material.Locales[0].Screenshots[0].Images[1].PlacementID = ""
			case "duplicate-placement-id":
				images := op.Receipt.Apple.Material.Locales[0].Screenshots[0].Images
				images[1].PlacementID = images[0].PlacementID
			case "material-drift":
				op.Receipt.Apple.Material.Locales[0].VersionText.Description = "Unapproved material journal"
			case "unsupported-stage":
				op.Receipt.Apple.Stage = "image_placement_intent"
			}
			placementMustSQL(t, e, "UPDATE operations SET receipt=? WHERE operation_key=?", mustAppleReceiptJSON(op.Receipt), r.OperationKey)
			state.mu.Lock()
			switch scenario {
			case "wrong-order":
				state.placements[0], state.placements[1] = state.placements[1], state.placements[0]
			case "wrong-placement-image":
				state.placements[0]["relationships"] = map[string]any{"image": appleRelationshipFixture("appAssetLibraryImages", "image1")}
			case "extra-placement":
				state.placements = append(state.placements, state.placements[0])
			case "missing-placement":
				state.placements = state.placements[:1]
			case "image-not-ready":
				state.ready = false
				state.images["replacement2"]["state"] = "UPLOAD_COMPLETE"
			}
			state.mu.Unlock()
			switch scenario {
			case "review-item-drift":
				review.wrongItem = true
			case "review-already-resolved":
				review.resolved = true
			case "source-drift":
				placementMustSQL(t, e, "UPDATE operations SET updated_at='changed-source' WHERE operation_key=?", r.SourceOperationKey)
			case "source-during-observation":
				var once sync.Once
				e.HTTP.Transport = appleRecoveryTransport{base: e.HTTP.Transport, after: func(q *http.Request, response *http.Response) (*http.Response, error) {
					if q.Method == "GET" && q.URL.Path == "/v1/appAssetLibraries/library1/images" && q.URL.Query().Get("filter[id]") == "replacement2" {
						once.Do(func() {
							placementMustSQL(t, e, "UPDATE operations SET updated_at='changed-late-source' WHERE operation_key=?", r.SourceOperationKey)
						})
					}
					return response, nil
				}}
			case "app-lock-lost":
				placementMustSQL(t, e, "DELETE FROM apple_destinations WHERE operation_key=?", r.OperationKey)
			case "version-lost":
				placementMustSQL(t, e, "DELETE FROM apple_versions")
			case "successor-lost":
				placementMustSQL(t, e, "DELETE FROM apple_material_revisions WHERE operation_key=?", r.OperationKey)
			case "sql-abort":
				placementMustSQL(t, e, "CREATE TRIGGER refuse_placement_recovery BEFORE UPDATE OF state ON operations WHEN NEW.state='complete' BEGIN SELECT RAISE(ABORT,'injected recovery failure'); END")
			}
			before, err := e.Inspect(ctx, r.OperationKey)
			if err != nil {
				t.Fatal(err)
			}
			in.JournalDigest = before.JournalDigest
			writes, reviews, packages := len(state.writes), len(review.writes), base.writes
			if _, err = e.ReconcileApple(ctx, in); err == nil {
				t.Fatal("incomplete display or changed ownership was accepted", scenario)
			}
			after, err := e.Inspect(ctx, r.OperationKey)
			if err != nil || after.JournalDigest != before.JournalDigest || after.State != "pending" || len(after.Receipt.Apple.Reconciliations) != 0 {
				t.Fatal("failed recovery changed the pending operation", after, err)
			}
			if len(state.writes) != writes || len(review.writes) != reviews || base.writes != packages {
				t.Fatal("negative read reconciliation issued a write")
			}
			screenshotProof(t, "placement-recovery-denied-"+scenario, e, r, state, review, base)
		})
	}
}

// A recovered flag cannot authorize writes after the exact display, review,
// source or reservations change. Both state and write counts are checked.
func TestApplePlacementRecoveryContinuationRechecksCurrentEvidenceAndAuthority(t *testing.T) {
	for _, scenario := range []string{"display-order", "text", "resolved-item-regressed", "source", "source-during-continuation", "app-lock", "version-lock", "successor-lock", "sql-abort"} {
		t.Run(scenario, func(t *testing.T) {
			r, e, base, material, review, state, in := lostScreenshotPlacement(t, "resolve")
			defer e.Close()
			ctx := context.Background()
			before, err := e.ReconcileApple(ctx, in)
			if err != nil {
				t.Fatal("could not prepare confirmed resolution", err)
			}
			switch scenario {
			case "display-order":
				state.placements[0], state.placements[1] = state.placements[1], state.placements[0]
			case "text":
				material.versionText["description"] = "Unapproved late text"
			case "resolved-item-regressed":
				review.resolved = false
			case "source":
				placementMustSQL(t, e, "UPDATE operations SET updated_at='changed-source' WHERE operation_key=?", r.SourceOperationKey)
			case "source-during-continuation":
				var once sync.Once
				e.HTTP.Transport = appleRecoveryTransport{base: e.HTTP.Transport, after: func(q *http.Request, response *http.Response) (*http.Response, error) {
					if q.Method == "GET" && q.URL.Path == "/v1/appAssetLibraries/library1/images" && q.URL.Query().Get("filter[id]") == "replacement2" {
						once.Do(func() {
							placementMustSQL(t, e, "UPDATE operations SET updated_at='changed-late-source' WHERE operation_key=?", r.SourceOperationKey)
						})
					}
					return response, nil
				}}
			case "app-lock":
				placementMustSQL(t, e, "DELETE FROM apple_destinations WHERE operation_key=?", r.OperationKey)
			case "version-lock":
				placementMustSQL(t, e, "DELETE FROM apple_versions")
			case "successor-lock":
				placementMustSQL(t, e, "DELETE FROM apple_material_revisions WHERE operation_key=?", r.OperationKey)
			case "sql-abort":
				placementMustSQL(t, e, "CREATE TRIGGER refuse_placement_continue BEFORE UPDATE OF state ON operations WHEN NEW.state='pending' BEGIN SELECT RAISE(ABORT,'injected continuation failure'); END")
			}
			writes, reviews, packages := len(state.writes), len(review.writes), base.writes
			r.Action = "continue"
			if _, err = e.Execute(ctx, r); err == nil {
				t.Fatal("stale recovery proof allowed continuation", scenario)
			}
			after, err := e.Inspect(ctx, r.OperationKey)
			if err != nil || after.JournalDigest != before.JournalDigest || after.State != "complete" || len(state.writes) != writes || len(review.writes) != reviews || base.writes != packages {
				t.Fatal("rejected continuation changed confirmed state or provider", after, err)
			}
			screenshotProof(t, "placement-recovery-stale-"+scenario, e, r, state, review, base)
		})
	}
}

// Two independent Engines sharing SQLite must claim one local transition and
// one remaining submission, even when their complete remote observations race.
func TestApplePlacementRecoveryConcurrentEnginesClaimOneRemainingAction(t *testing.T) {
	for _, action := range []string{"recover", "continue"} {
		t.Run(action, func(t *testing.T) {
			phase := "order"
			if action == "continue" {
				phase = "resolve"
			}
			r, one, base, _, review, state, in := lostScreenshotPlacement(t, phase)
			defer one.Close()
			ctx := context.Background()
			if action == "continue" {
				if _, err := one.ReconcileApple(ctx, in); err != nil {
					t.Fatal(err)
				}
			}
			two, err := Open(one.Config)
			if err != nil {
				t.Fatal(err)
			}
			defer two.Close()
			two.appleBase = one.appleBase
			arrived, release := make(chan struct{}, 2), make(chan struct{})
			var once sync.Once
			defer once.Do(func() { close(release) })
			var arrivals atomic.Int32
			client := *one.HTTP
			client.Transport = appleRecoveryTransport{base: one.HTTP.Transport, after: func(q *http.Request, response *http.Response) (*http.Response, error) {
				if q.Method == "GET" && q.URL.Path == "/v1/appAssetLibraries/library1/images" && q.URL.Query().Get("filter[id]") == "replacement2" && arrivals.Add(1) <= 2 {
					arrived <- struct{}{}
					<-release
				}
				return response, nil
			}}
			one.HTTP, two.HTTP = &client, &client
			results := make(chan error, 2)
			writes, reviews, packages := len(state.writes), len(review.writes), base.writes
			for _, engine := range []*Engine{one, two} {
				go func(engine *Engine) {
					var err error
					if action == "recover" {
						_, err = engine.ReconcileApple(ctx, in)
					} else {
						request := r
						request.Action = "continue"
						_, err = engine.Execute(ctx, request)
					}
					results <- err
				}(engine)
			}
			for range 2 {
				select {
				case <-arrived:
				case err := <-results:
					t.Fatal("engine returned before the shared proof barrier", err)
				case <-time.After(10 * time.Second):
					t.Fatal("engines did not both observe the exact complete display")
				}
			}
			once.Do(func() { close(release) })
			successes := 0
			for range 2 {
				if err := <-results; err == nil {
					successes++
				}
			}
			op, err := one.Inspect(ctx, r.OperationKey)
			wantReviews := reviews
			if action == "continue" {
				wantReviews++
			}
			if err != nil || successes != 1 || op.State != "complete" || len(op.Receipt.Apple.Reconciliations) != 1 || len(state.writes) != writes || len(review.writes) != wantReviews || base.writes != packages {
				t.Fatal("racing engines duplicated or lost the remaining action", successes, op, err, review.writes)
			}
			screenshotProof(t, "placement-recovery-concurrent-"+action, one, r, state, review, base)
		})
	}
}

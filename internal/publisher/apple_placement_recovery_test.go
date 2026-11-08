package publisher

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The provider accepts the actual ordered placements or resolution before its
// response is lost. The fixture never changes the product's journal afterward.
func lostScreenshotPlacement(t *testing.T, phase string) (Request, *Engine, *appleFixtureState, *appleMaterialsFixtureState, *materialReviewFixture, *screenshotRevisionFixture, AppleReconciliationRequest) {
	t.Helper()
	source, e, base, material, review, state := screenshotFixture(t)
	r := screenshotRequest(t, e, state, source, 1, 2)
	material.expectedDescription = "Reviewed correction 1"
	if _, err := e.Execute(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	state.mu.Lock()
	state.ready = true
	state.mu.Unlock()
	if phase == "resolve-not-accepted" {
		review.failWrite = "PATCH /v1/reviewSubmissionItems/item1"
	}
	transport := e.HTTP.Transport
	ordered := false
	e.HTTP.Transport = appleRecoveryTransport{base: transport, after: func(q *http.Request, response *http.Response) (*http.Response, error) {
		lose := false
		if q.Method == "POST" && q.URL.Path == "/v1/appAssetLibraryPlacementOrderingRequests" {
			ordered = true
			lose = phase == "order"
		}
		if ordered && q.Method == "GET" && q.URL.Path == "/v1/reviewSubmissions/submission1/items" && phase == "before-resolve" {
			lose = true
		}
		if q.Method == "PATCH" && q.URL.Path == "/v1/reviewSubmissionItems/item1" && phase == "resolve" {
			lose = true
		}
		if lose {
			response.Body.Close()
			response.StatusCode = http.StatusServiceUnavailable
			response.Body = io.NopCloser(strings.NewReader(`{"errors":[]}`))
		}
		return response, nil
	}}
	r.Action = "continue"
	if _, err := e.Execute(context.Background(), r); err == nil {
		t.Fatal("fixture did not stop the actual continuation at the requested uncertainty", phase)
	}
	e.HTTP.Transport = transport
	review.failWrite = ""
	op, err := e.Inspect(context.Background(), r.OperationKey)
	stages := map[string]string{"order": "image_order_intent", "before-resolve": "materials_placed", "resolve": "material_resolve_intent", "resolve-not-accepted": "material_resolve_intent"}
	if err != nil || op.State != "pending" || op.Receipt.Apple.Stage != stages[phase] || len(state.placements) != 2 {
		t.Fatal("fixture lacks the exact known complete placement stage", op, err)
	}
	r.ArtifactPath = filepath.Join(t.TempDir(), "original-approved.ipa")
	if err = os.WriteFile(r.ArtifactPath, base.uploaded, 0600); err != nil {
		t.Fatal(err)
	}
	in := AppleReconciliationRequest{Request: r, JournalDigest: op.JournalDigest, ConfirmStoppedOperationKey: r.OperationKey,
		Reason: "Administrator stopped the exact synthetic operation and verified no request remains in flight"}
	return r, e, base, material, review, state, in
}

// Missing routing or resubmitting a confirmed resolve fails this real TLS/SQL
// flow. A returned processing flag alone cannot satisfy the write assertions.
func TestApplePlacementRecoveryPreservesDisplayAndContinuesOnlyRemainingReview(t *testing.T) {
	for _, tc := range []struct {
		phase, stage string
		reviewWrites int
	}{
		{"order", "material_placement_confirmed", 2},
		{"before-resolve", "material_placement_confirmed", 2},
		{"resolve", "material_resolution_confirmed", 1},
		{"resolve-not-accepted", "material_placement_confirmed", 2},
	} {
		t.Run(tc.phase, func(t *testing.T) {
			r, e, base, _, review, state, in := lostScreenshotPlacement(t, tc.phase)
			defer func() { _ = e.Close() }()
			ctx := context.Background()
			writes, reviews, packages := len(state.writes), len(review.writes), base.writes
			screenshotProof(t, "placement-recovery-"+tc.phase+"-pending", e, r, state, review, base)
			if _, err := e.Execute(ctx, r); err == nil {
				t.Fatal("ordinary retry repeated an uncertain write")
			}
			recovered, err := e.ReconcileApple(ctx, in)
			if err != nil || recovered.State != "complete" || recovered.Result == nil || recovered.Result.State != "processing" || recovered.Result.ContinueRequired || recovered.Receipt.Apple.Stage != tc.stage {
				t.Fatal("complete display did not recover into its exact protected wait", recovered, err)
			}
			if len(state.writes) != writes || len(review.writes) != reviews || base.writes != packages || len(state.placements) != 2 {
				t.Fatal("reconciliation changed display, package or review")
			}
			var locks int
			if err = e.db.QueryRow("SELECT count(*) FROM apple_destinations WHERE operation_key=?", r.OperationKey).Scan(&locks); err != nil || locks != 1 {
				t.Fatal("confirmed display released its app reservation", locks, err)
			}
			if len(recovered.Receipt.Apple.Reconciliations) != 1 || recovered.Receipt.Apple.Reconciliations[0].JournalDigest != in.JournalDigest {
				t.Fatal("exact recovery provenance was lost")
			}
			screenshotProof(t, "placement-recovery-"+tc.phase+"-recovered", e, r, state, review, base)
			config, api, client := e.Config, e.appleBase, e.HTTP
			if err = e.Close(); err != nil {
				t.Fatal(err)
			}
			e, err = Open(config)
			if err != nil {
				t.Fatal(err)
			}
			e.appleBase, e.HTTP = api, client
			body, err := os.ReadFile(r.ArtifactPath)
			if err != nil || os.Remove(r.ArtifactPath) != nil {
				t.Fatal(err)
			}
			observations := 0
			e.HTTP.Transport = appleRecoveryTransport{base: e.HTTP.Transport, after: func(q *http.Request, response *http.Response) (*http.Response, error) {
				observations++
				return response, nil
			}}
			replay, err := e.ReconcileApple(ctx, in)
			if err != nil || replay.JournalDigest != recovered.JournalDigest || observations != 0 {
				t.Fatal("restart did not return the exact local recovery without artifact/API", replay, err, observations)
			}
			if err = os.WriteFile(r.ArtifactPath, body, 0600); err != nil {
				t.Fatal(err)
			}
			r.Action = "status"
			if got, err := e.Execute(ctx, r); err != nil || got.State != "processing" || !got.ContinueRequired {
				t.Fatal("status did not reobserve exact confirmed media/review", got, err)
			}
			r.Action = "continue"
			if got, err := e.Execute(ctx, r); err != nil || got.State != "awaiting_review" {
				t.Fatal("protected continuation did not finish the remaining review", got, err)
			}
			if len(state.writes) != writes || base.writes != packages || len(review.writes) != reviews+tc.reviewWrites {
				t.Fatal("continuation repeated known display/package/resolve writes", state.writes, review.writes)
			}
			if _, err = e.ReconcileApple(ctx, in); err == nil {
				t.Fatal("old recovery input regressed the later submitted operation")
			}
			final, err := e.Inspect(ctx, r.OperationKey)
			if err != nil || final.Receipt.Apple.Stage != "submitted" || len(final.Receipt.Apple.Reconciliations) != 1 {
				t.Fatal(final, err)
			}
			screenshotProof(t, "placement-recovery-"+tc.phase+"-continued", e, r, state, review, base)
		})
	}
}

func placementMustSQL(t *testing.T, e *Engine, query string, args ...any) {
	t.Helper()
	if _, err := e.db.Exec(query, args...); err != nil {
		t.Fatal("fixture SQL:", err)
	}
}

// A crash after the durable continuation claim but before its first write must
// not make a positively confirmed display permanently unrecoverable.
func TestApplePlacementRecoveryRestoresAClaimedContinuationBeforeFirstWrite(t *testing.T) {
	for _, phase := range []string{"order", "resolve"} {
		t.Run(phase, func(t *testing.T) {
			r, e, base, _, review, state, in := lostScreenshotPlacement(t, phase)
			defer e.Close()
			ctx := context.Background()
			first, err := e.ReconcileApple(ctx, in)
			if err != nil {
				t.Fatal(err)
			}
			transport := e.HTTP.Transport
			e.HTTP.Transport = appleRecoveryTransport{base: transport, after: func(q *http.Request, response *http.Response) (*http.Response, error) {
				var localState string
				if err := e.db.QueryRow("SELECT state FROM operations WHERE operation_key=?", r.OperationKey).Scan(&localState); err != nil {
					t.Fatal(err)
				}
				if q.Method == "GET" && localState == "pending" {
					response.Body.Close()
					response.StatusCode = http.StatusServiceUnavailable
					response.Body = io.NopCloser(strings.NewReader(`{"errors":[]}`))
				}
				return response, nil
			}}
			writes, reviews, packages := len(state.writes), len(review.writes), base.writes
			r.Action = "continue"
			if _, err = e.Execute(ctx, r); err == nil {
				t.Fatal("fixture did not fail after the durable continuation claim")
			}
			e.HTTP.Transport = transport
			pending, err := e.Inspect(ctx, r.OperationKey)
			if err != nil || pending.State != "pending" || pending.Receipt.Apple.Stage != first.Receipt.Apple.Stage || pending.JournalDigest == first.JournalDigest || len(state.writes) != writes || len(review.writes) != reviews {
				t.Fatal("fixture did not retain the original confirmed checkpoint", pending, err)
			}
			screenshotProof(t, "placement-recovery-reclaimed-"+phase+"-pending", e, r, state, review, base)
			in.JournalDigest = pending.JournalDigest
			in.Reason = "Stopped the subsequent claimed continuation before its first external write and rechecked all exact evidence"
			recovered, err := e.ReconcileApple(ctx, in)
			if err != nil || recovered.State != "complete" || recovered.Result.State != "processing" || recovered.Result.ContinueRequired || len(recovered.Receipt.Apple.Reconciliations) != 2 {
				t.Fatal("claimed confirmed checkpoint could not restore its protected wait", recovered, err)
			}
			screenshotProof(t, "placement-recovery-reclaimed-"+phase+"-recovered", e, r, state, review, base)
			if got, err := e.Execute(ctx, r); err != nil || got.State != "awaiting_review" {
				t.Fatal("reclaimed continuation did not reach review", got, err)
			}
			remaining := 2
			if phase == "resolve" {
				remaining = 1
			}
			if len(state.writes) != writes || len(review.writes) != reviews+remaining || base.writes != packages {
				t.Fatal("reclaimed continuation repeated a confirmed external write")
			}
			screenshotProof(t, "placement-recovery-reclaimed-"+phase+"-continued", e, r, state, review, base)
		})
	}
}

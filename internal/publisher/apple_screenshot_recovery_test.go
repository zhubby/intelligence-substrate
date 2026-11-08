package publisher

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The real TLS provider commits the exact uploaded bytes before its accepted
// response is lost. Ordinary retries must remain fenced; only a stopped,
// administrator-bound reconciliation may finish the local processing wait.
func lostScreenshotCommit(t *testing.T, count, lost int) (Request, *Engine, *appleFixtureState, *appleMaterialsFixtureState, *materialReviewFixture, *screenshotRevisionFixture, AppleReconciliationRequest) {
	t.Helper()
	source, e, base, material, review, state := screenshotFixture(t)
	r := screenshotRequest(t, e, state, source, 1, count)
	material.expectedDescription = "Reviewed correction 1"
	r.ArtifactPath = filepath.Join(t.TempDir(), "approved-original.ipa")
	if err := os.WriteFile(r.ArtifactPath, base.uploaded, 0600); err != nil {
		t.Fatal(err)
	}
	e.HTTP.Transport = appleRecoveryTransport{base: e.HTTP.Transport, after: func(q *http.Request, response *http.Response) (*http.Response, error) {
		if q.Method == "PATCH" && q.URL.Path == fmt.Sprintf("/v1/appAssetLibraryImages/replacement%d", lost) {
			response.Body.Close()
			response.StatusCode = http.StatusServiceUnavailable
			response.Body = io.NopCloser(strings.NewReader(`{"errors":[]}`))
		}
		return response, nil
	}}
	if _, err := e.Execute(context.Background(), r); err == nil {
		t.Fatal("fixture did not lose an accepted image commit response")
	}
	op, err := e.Inspect(context.Background(), r.OperationKey)
	if err != nil || op.State != "pending" || op.Receipt.Apple.Stage != "image_commit_intent" {
		t.Fatal(op, err)
	}
	in := AppleReconciliationRequest{Request: r, JournalDigest: op.JournalDigest, ConfirmStoppedOperationKey: r.OperationKey,
		Reason: "Administrator stopped the exact synthetic publisher process and confirmed all outstanding writes ended"}
	return r, e, base, material, review, state, in
}

func TestAppleScreenshotCommitRecoveryRestoresWaitWithoutRemoteWrites(t *testing.T) {
	for _, count := range []int{1, 2} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			r, e, base, _, review, state, in := lostScreenshotCommit(t, count, count)
			defer func() { _ = e.Close() }()
			ctx := context.Background()
			beforeImages, beforeReview, beforePackage := len(state.writes), len(review.writes), base.writes
			screenshotProof(t, fmt.Sprintf("media-recovery-%d-pending", count), e, r, state, review, base)
			if _, err := e.Execute(ctx, r); err == nil {
				t.Fatal("ordinary retry replayed an uncertain commit")
			}
			recovered, err := e.ReconcileApple(ctx, in)
			if err != nil || recovered.State != "complete" || recovered.Result == nil || recovered.Result.State != "processing" || recovered.Result.ContinueRequired || recovered.Receipt.Apple.Stage != "material_images_processing" {
				t.Fatal("accepted final image commit did not recover into a non-continuing processing wait", recovered, err)
			}
			if len(state.writes) != beforeImages || len(review.writes) != beforeReview || base.writes != beforePackage || len(state.placements) != 1 || state.placements[0]["id"] != "placement1" || review.resolved {
				t.Fatal("read-only recovery changed original display, package or review")
			}
			var locks int
			if err = e.db.QueryRow("SELECT count(*) FROM apple_destinations WHERE operation_key=?", r.OperationKey).Scan(&locks); err != nil || locks != 1 {
				t.Fatal("processing recovery released its app reservation", locks, err)
			}
			provenance := recovered.Receipt.Apple.Reconciliations
			if len(provenance) != 1 || provenance[0].JournalDigest != in.JournalDigest || provenance[0].FromStage != "image_commit_intent" || provenance[0].ToStage != "material_images_processing" || provenance[0].ResultState != "processing" {
				t.Fatal("recovery provenance does not describe the accepted stopped operation", provenance)
			}
			screenshotProof(t, fmt.Sprintf("media-recovery-%d-recovered", count), e, r, state, review, base)
			config, api, client := e.Config, e.appleBase, e.HTTP
			if err = e.Close(); err != nil {
				t.Fatal(err)
			}
			e, err = Open(config)
			if err != nil {
				t.Fatal(err)
			}
			e.appleBase, e.HTTP = api, client
			// Exact replay must work even when the original local artifact is
			// temporarily unavailable, without contacting Apple again.
			body, err := os.ReadFile(r.ArtifactPath)
			if err != nil || os.Remove(r.ArtifactPath) != nil {
				t.Fatal(err)
			}
			observations := 0
			e.HTTP.Transport = appleRecoveryTransport{base: e.HTTP.Transport, after: func(q *http.Request, response *http.Response) (*http.Response, error) {
				observations++
				return response, nil
			}}
			replayed, err := e.ReconcileApple(ctx, in)
			if err != nil || replayed.JournalDigest != recovered.JournalDigest || observations != 0 {
				t.Fatal("exact restarted recovery was not locally idempotent", replayed, err, observations)
			}
			if err = os.WriteFile(r.ArtifactPath, body, 0600); err != nil {
				t.Fatal(err)
			}
			r.Action = "status"
			if got, err := e.Execute(ctx, r); err != nil || got.ContinueRequired || got.State != "processing" {
				t.Fatal("unprocessed images escaped their durable wait", got, err)
			}
			state.mu.Lock()
			state.ready = true
			state.mu.Unlock()
			if got, err := e.Execute(ctx, r); err != nil || !got.ContinueRequired {
				t.Fatal("read-only status did not observe the exact now-ready images", got, err)
			}
			r.Action = "continue"
			if got, err := e.Execute(ctx, r); err != nil || got.State != "awaiting_review" {
				t.Fatal("ordinary approved continuation failed after recovery", got, err)
			}
			if base.writes != beforePackage || len(review.writes) != beforeReview+2 || len(state.placements) != count {
				t.Fatal("continuation reuploaded package or duplicated review/placements")
			}
			if _, err = e.ReconcileApple(ctx, in); err == nil {
				t.Fatal("old recovery input regressed a later submitted operation")
			}
			screenshotProof(t, fmt.Sprintf("media-recovery-%d-continued", count), e, r, state, review, base)
		})
	}
}

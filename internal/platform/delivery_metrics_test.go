package platform

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func metricJSON(t *testing.T, s *Service, a Actor, project, goal string, rev int) M {
	t.Helper()
	r, err := s.DeliveryMetrics(context.Background(), a, project, goal, rev)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var m M
	if err = json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestDeliveryMetricsMissingValuesAreNotZeroOutcomes(t *testing.T) {
	s, a, p := fixture(t)
	g := goal(t, s, a, p)
	r := metricJSON(t, s, a, p.ID, g.ID, 0)
	for _, key := range []string{"meanPreviewSeconds", "firstPassRate", "humanSeconds", "publishExecutionFailureRate", "totalCostPerSuccessfulDeliveryCents"} {
		if value, exists := r[key]; !exists || value != nil {
			t.Fatal("missing data turned into zero or disappeared", key, r)
		}
	}
	if N(r, "requirements") != 1 || N(r, "acceptanceSamples") != 0 || B(r, "manualBaselineAvailable") || len(Strings(r["gaps"])) < 3 {
		t.Fatal("no-data coverage hidden", r)
	}
}

// Literal timestamps establish a 120s preview latency from requirement revision1.
// Later pass cannot overwrite first failure; new requirements start unobserved.
func TestDeliveryMetricsFirstFailureAndHistoricalVersionIsolation(t *testing.T) {
	f := newMetricFixture(t)
	preview := must(t, f.s, f.owner, "measurements.record", f.input("preview", "preview"))
	failed := must(t, f.s, f.owner, "measurements.record", f.input("business_acceptance", "review-1"))
	pass := f.input("business_acceptance", "review-2")
	pass["result"] = "passed"
	must(t, f.s, f.owner, "measurements.record", pass)
	if err := f.s.Store.Update(context.Background(), func(tx *Tx) error {
		baseline, err := measurementBaseline(tx, f.goal, 1)
		if err != nil {
			return err
		}
		baseline.CreatedAt = "2026-01-01T00:00:00Z"
		if err = tx.Put(baseline); err != nil {
			return err
		}
		preview.CreatedAt = "2026-01-01T00:02:00Z"
		return tx.Put(preview)
	}); err != nil {
		t.Fatal(err)
	}
	r := metricJSON(t, f.s, f.owner, f.project.ID, f.goal.ID, 0)
	if N(r, "meanPreviewSeconds") != 120 || N(r, "previewSamples") != 1 || N(r, "acceptanceSamples") != 1 || N(r, "firstPassCount") != 0 || r["firstPassRate"] != float64(0) {
		t.Fatal("incorrect first observation or denominator", r)
	}
	rows := Items(r["versions"])
	if S(Obj(rows[0]), "firstAcceptanceId") != failed.ID {
		t.Fatal("later pass replaced first failed result", rows)
	}
	must(t, f.s, f.owner, "goals.revise", M{"id": f.goal.ID, "reason": "A different requirement baseline"})
	current := metricJSON(t, f.s, f.owner, f.project.ID, f.goal.ID, 0)
	if current["meanPreviewSeconds"] != nil || current["firstPassRate"] != nil {
		t.Fatal("historical facts covered new baseline", current)
	}
	history := metricJSON(t, f.s, f.owner, f.project.ID, f.goal.ID, 1)
	if N(history, "meanPreviewSeconds") != 120 || N(history, "acceptanceSamples") != 1 {
		t.Fatal("revision erased historical observation", history)
	}
	if err := os.WriteFile(f.s.ArtifactPath(S(f.proof.Data, "digest")), []byte("tampered-report"), 0600); err != nil {
		t.Fatal(err)
	}
	broken := metricJSON(t, f.s, f.owner, f.project.ID, f.goal.ID, 1)
	if broken["meanPreviewSeconds"] != nil || broken["firstPassRate"] != nil {
		t.Fatal("unreadable proof remained counted", broken)
	}
	if len(Items(broken["observations"])) != 3 {
		t.Fatal("broken facts were silently erased")
	}
}

func TestDeliveryMetricsProjectCohortAndPublishExecutionDenominator(t *testing.T) {
	f := newMetricFixture(t)
	pass := f.input("business_acceptance", "pass")
	pass["result"] = "passed"
	must(t, f.s, f.owner, "measurements.record", pass)
	goal(t, f.s, f.owner, f.project) // unobserved, must not lower first-pass rate
	if err := f.s.Store.Update(context.Background(), func(tx *Tx) error {
		for i, entry := range []struct{ kind, status string }{{"publish", "failed"}, {"publish", "succeeded"}, {"publish_continue", "succeeded"}, {"publish_status", "failed"}, {"publish", "running"}, {"publish", "expired"}} {
			task := resource("task", entry.kind, f.project.ID, f.goal.ID, M{"kind": entry.kind, "requirementRevision": 1})
			if err := tx.Put(task); err != nil {
				return err
			}
			run := resource("run", "run", f.project.ID, f.goal.ID, M{"taskId": task.ID, "requirementRevision": 1, "result": M{"leaseToken": "metric-hidden-token", "private": i}})
			run.Status = entry.status
			if err := tx.Put(run); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	r := metricJSON(t, f.s, f.owner, f.project.ID, "", 0)
	if N(r, "requirements") != 2 || N(r, "acceptanceSamples") != 1 || N(r, "firstPassRate") != 1 || N(r, "publishExecutions") != 3 || N(r, "failedPublishExecutions") != 1 {
		t.Fatal("unobserved outcome or poll changed denominators", r)
	}
	if N(r, "publishExecutionFailureRate") < .333 || N(r, "publishExecutionFailureRate") > .334 {
		t.Fatal("wrong execution failure rate", r)
	}
	data, _ := json.Marshal(r)
	if strings.Contains(string(data), "metric-hidden-token") || strings.Contains(string(data), "leaseToken") {
		t.Fatal("execution secrets leaked in read model")
	}
}

func TestDeliveryMetricsChannelWaitIsObservedNotReconstructed(t *testing.T) {
	f := newMetricFixture(t)
	stored, _ := f.s.Store.Get(f.release.ID)
	if len(Items(Obj(Items(stored.Data["targets"])[0])["observedHistory"])) != 1 {
		t.Fatal("new release lacks observation origin")
	}
	var wait float64
	var complete bool
	if err := f.s.Store.Update(context.Background(), func(tx *Tx) error {
		r := f.release
		r.Data["targets"] = []any{M{"target": "web", "status": "ready", "observedHistory": []any{M{"from": "", "to": "ready", "at": "2026-01-01T00:00:00Z"}}}}
		for _, state := range []string{"processing", "awaiting_review", "published"} {
			if err := f.s.setTarget(tx, &r, "web", state, "synthetic-external", "observed", f.owner.ID); err != nil {
				return err
			}
		}
		target := Obj(Items(r.Data["targets"])[0])
		history := Items(target["observedHistory"])
		for i, at := range []string{"2026-01-01T00:00:00Z", "2026-01-01T00:00:10Z", "2026-01-01T00:00:20Z", "2026-01-01T00:00:50Z"} {
			Obj(history[i])["at"] = at
		}
		wait, complete = observedChannelWait(target, time.Date(2026, 1, 1, 0, 2, 0, 0, time.UTC))
		return tx.Put(r)
	}); err != nil {
		t.Fatal(err)
	}
	if wait != 40 || !complete {
		t.Fatal("wait did not follow actual transitions", wait, complete)
	}
	legacy := M{"status": "awaiting_review", "updatedAt": "2020-01-01T00:00:00Z"}
	if seconds, full := observedChannelWait(legacy, time.Now()); seconds != 0 || full {
		t.Fatal("fabricated legacy waiting time", seconds, full)
	}
}

func TestDeliveryMetricsEffortAndProductionDefectAreExplicitHumanFacts(t *testing.T) {
	f := newMetricFixture(t)
	origin := time.Now().Add(-2 * time.Hour).UTC()
	if err := f.s.Store.Update(context.Background(), func(tx *Tx) error {
		baseline, err := measurementBaseline(tx, f.goal, 1)
		if err != nil {
			return err
		}
		baseline.CreatedAt = origin.Format(time.RFC3339Nano)
		return tx.Put(baseline)
	}); err != nil {
		t.Fatal(err)
	}
	in := f.input("human_effort", "owner-review-time")
	in["startedAt"], in["endedAt"] = origin.Add(time.Minute).Format(time.RFC3339Nano), origin.Add(31*time.Minute).Format(time.RFC3339Nano)
	must(t, f.s, f.owner, "measurements.record", in)
	officer := Actor{ID: "metric-release-officer", Kind: "human", Admin: true}
	must(t, f.s, officer, "releases.approve", M{"id": f.release.ID})
	must(t, f.s, officer, "releases.transition", M{"id": f.release.ID, "target": "web", "state": "published", "externalId": "synthetic-local-publication", "proofArtifactId": f.proof.ID})
	defect := must(t, f.s, f.qa, "measurements.record", f.input("production_defect", "customer-case-1"))
	r := metricJSON(t, f.s, f.owner, f.project.ID, f.goal.ID, 0)
	if N(r, "humanSeconds") != 1800 || N(r, "effortSamples") != 1 || N(r, "reportedProductionDefects") != 1 || r["firstPassRate"] != nil || r["totalCostPerSuccessfulDeliveryCents"] != nil {
		t.Fatal("manual observation became unproved business success", r)
	}
	must(t, f.s, f.owner, "goals.revise", M{"id": f.goal.ID, "reason": "Next product version"})
	late := f.input("production_defect", "late-customer-case")
	old := must(t, f.s, f.qa, "measurements.record", late)
	if revision(old) != 1 || old.GoalID != defect.GoalID {
		t.Fatal("late defect moved to new requirement")
	}
	current := metricJSON(t, f.s, f.owner, f.project.ID, f.goal.ID, 0)
	history := metricJSON(t, f.s, f.owner, f.project.ID, f.goal.ID, 1)
	if N(current, "reportedProductionDefects") != 0 || N(history, "reportedProductionDefects") != 2 {
		t.Fatal("defect version scope lost", current, history)
	}
}

func TestDeliveryMetricsFirstAttemptUsesTimeNotTimestampTextOrder(t *testing.T) {
	f := newMetricFixture(t)
	first := must(t, f.s, f.owner, "measurements.record", f.input("business_acceptance", "first"))
	in := f.input("business_acceptance", "second")
	in["result"] = "passed"
	second := must(t, f.s, f.owner, "measurements.record", in)
	if err := f.s.Store.Update(context.Background(), func(tx *Tx) error {
		first.CreatedAt = "2026-11-06T17:00:00Z"
		second.CreatedAt = "2026-11-06T17:00:00.1Z"
		if err := tx.Put(first); err != nil {
			return err
		}
		return tx.Put(second)
	}); err != nil {
		t.Fatal(err)
	}
	r := metricJSON(t, f.s, f.owner, f.project.ID, f.goal.ID, 0)
	if N(r, "firstPassCount") != 0 || r["firstPassRate"] != float64(0) {
		t.Fatal("fractional timestamp sorted before an earlier whole second", r)
	}
}

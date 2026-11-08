package platform

import (
	"context"
	"testing"
	"time"
)

func processingChannel(t *testing.T) (*Service, Actor, Actor, Resource, Resource) {
	t.Helper()
	s, officer, worker, release := publishTargetFixture(t, "ios-appstore")
	must(t, s, officer, "releases.dispatch", M{"id": release.ID, "target": "ios-appstore", "workerId": worker.WorkerID, "key": "upload"})
	run := must(t, s, worker, "tasks.claim", M{})
	must(t, s, worker, "tasks.complete", M{"id": run.ID, "leaseToken": S(run.Data, "leaseToken"), "exitCode": 0, "result": M{"externalId": "apple:123:upload1", "state": "processing"}})
	release, _ = s.Store.Get(release.ID)
	return s, officer, worker, release, run
}

func TestProcessingWaitAndApprovedContinuation(t *testing.T) {
	s, officer, worker, release, initial := processingChannel(t)
	if idle := must(t, s, worker, "tasks.claim", M{}); idle.Kind == "run" {
		t.Fatal("processing wait held a lease")
	}
	must(t, s, officer, "releases.refresh", M{"id": release.ID, "target": "ios-appstore", "key": "processed"})
	query := must(t, s, worker, "tasks.claim", M{})
	must(t, s, worker, "tasks.complete", M{"id": query.ID, "leaseToken": S(query.Data, "leaseToken"), "exitCode": 0, "result": M{"externalId": "apple:123:upload1", "state": "processing", "continueRequired": true, "upstreamState": "COMPLETE"}})
	continuation := must(t, s, worker, "tasks.claim", M{})
	task := Obj(continuation.Data["task"])
	if S(task, "kind") != "publish_continue" || S(task, "operationKey") != S(Obj(initial.Data["task"]), "operationKey") || S(task, "binding") != S(release.Data, "binding") || S(task, "externalId") != "apple:123:upload1" {
		t.Fatalf("continuation lost original approval: %+v", task)
	}
	if _, err := s.Command(context.Background(), worker, "tasks.complete", M{"id": continuation.ID, "leaseToken": S(continuation.Data, "leaseToken"), "exitCode": 0, "result": M{"externalId": "other", "state": "awaiting_review"}}); err == nil {
		t.Fatal("continuation replaced uploaded build")
	}
	must(t, s, worker, "tasks.complete", M{"id": continuation.ID, "leaseToken": S(continuation.Data, "leaseToken"), "exitCode": 0, "result": M{"externalId": "apple:123:upload1", "state": "awaiting_review"}})
	current, _ := s.Store.Get(release.ID)
	if S(releaseTarget(current, "ios-appstore"), "status") != "awaiting_review" || current.Status == "delivered" {
		t.Fatal("accepted review incorrectly verified delivery")
	}
}

func TestContinuationLeaseExpiryNeverResubmits(t *testing.T) {
	s, officer, worker, release, _ := processingChannel(t)
	must(t, s, officer, "releases.refresh", M{"id": release.ID, "target": "ios-appstore", "key": "ready"})
	query := must(t, s, worker, "tasks.claim", M{})
	must(t, s, worker, "tasks.complete", M{"id": query.ID, "leaseToken": S(query.Data, "leaseToken"), "exitCode": 0, "result": M{"externalId": "apple:123:upload1", "state": "processing", "continueRequired": true}})
	run := must(t, s, worker, "tasks.claim", M{})
	run.Data["leaseUntil"] = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
	if err := s.Store.Update(context.Background(), func(tx *Tx) error { return tx.Put(run) }); err != nil {
		t.Fatal(err)
	}
	if idle := must(t, s, worker, "tasks.claim", M{}); idle.Kind == "run" {
		t.Fatal("uncertain review was resubmitted")
	}
	current, _ := s.Store.Get(release.ID)
	if S(releaseTarget(current, "ios-appstore"), "status") != "reconciliation_required" {
		t.Fatal("missing reconciliation after uncertain continuation")
	}
	if _, err := s.Command(context.Background(), officer, "tasks.retry", M{"id": S(run.Data, "taskId")}); err == nil {
		t.Fatal("generic retry bypassed reconciliation")
	}
}

func TestContinuationRequiresCurrentApprovalAndPlan(t *testing.T) {
	for _, change := range []string{"requirement", "approval", "plan", "budget"} {
		t.Run(change, func(t *testing.T) {
			s, officer, worker, release, _ := processingChannel(t)
			must(t, s, officer, "releases.refresh", M{"id": release.ID, "target": "ios-appstore", "key": "ready"})
			run := must(t, s, worker, "tasks.claim", M{})
			must(t, s, worker, "tasks.complete", M{"id": run.ID, "leaseToken": S(run.Data, "leaseToken"), "exitCode": 0, "result": M{"externalId": "apple:123:upload1", "state": "processing", "continueRequired": true}})
			if change == "budget" {
				must(t, s, officer, "goals.revise", M{"id": release.GoalID, "budget": 100, "reason": "Increase requires a new approved baseline"})
			}
			if err := s.Store.Update(context.Background(), func(tx *Tx) error {
				if change == "budget" {
					return nil
				}
				if change == "approval" {
					r, e := tx.Get(release.ID)
					if e != nil {
						return e
					}
					r.Data["approvedBinding"] = "revoked"
					return tx.Put(r)
				}
				g, e := tx.Get(release.GoalID)
				if e != nil {
					return e
				}
				if change == "requirement" {
					g.Revision++
				} else {
					g.Data["activePlanId"] = "other"
				}
				return tx.Put(g)
			}); err != nil {
				t.Fatal(err)
			}
			if idle := must(t, s, worker, "tasks.claim", M{}); idle.Kind == "run" {
				t.Fatal("stale or revoked approval allowed review mutation")
			}
		})
	}
}

func TestContinuationBudgetPauseCanResumeBeforeExternalExecution(t *testing.T) {
	s, officer, worker, release, _ := processingChannel(t)
	must(t, s, officer, "releases.refresh", M{"id": release.ID, "target": "ios-appstore", "key": "ready-budget"})
	query := must(t, s, worker, "tasks.claim", M{})
	must(t, s, worker, "tasks.complete", M{"id": query.ID, "leaseToken": S(query.Data, "leaseToken"), "exitCode": 0, "result": M{"externalId": "apple:123:upload1", "state": "processing", "continueRequired": true}})
	goal, _ := s.Store.Get(release.GoalID)
	reservation := resource("run", "Synthetic concurrent reservation", release.ProjectID, release.GoalID, M{"workerId": "another-synthetic-worker", "reservedCents": N(goal.Data, "budgetCents") + 1, "leaseUntil": time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)})
	reservation.Status = "running"
	if err := s.Store.Update(context.Background(), func(tx *Tx) error { return tx.Put(reservation) }); err != nil {
		t.Fatal(err)
	}
	if idle := must(t, s, worker, "tasks.claim", M{}); idle.Kind == "run" {
		t.Fatal("continuation ignored reserved budget")
	}
	reservation.Status = "succeeded"
	if err := s.Store.Update(context.Background(), func(tx *Tx) error { return tx.Put(reservation) }); err != nil {
		t.Fatal(err)
	}
	if run := must(t, s, worker, "tasks.claim", M{}); run.Kind != "run" || S(Obj(run.Data["task"]), "kind") != "publish_continue" {
		t.Fatal("budget pause permanently orphaned a continuation before it executed")
	}
}

func TestContinuationCompletionCannotOverrideRevokedApproval(t *testing.T) {
	s, officer, worker, release, _ := processingChannel(t)
	must(t, s, officer, "releases.refresh", M{"id": release.ID, "target": "ios-appstore", "key": "ready-revoke"})
	query := must(t, s, worker, "tasks.claim", M{})
	must(t, s, worker, "tasks.complete", M{"id": query.ID, "leaseToken": S(query.Data, "leaseToken"), "exitCode": 0, "result": M{"externalId": "apple:123:upload1", "state": "processing", "continueRequired": true}})
	run := must(t, s, worker, "tasks.claim", M{})
	if err := s.Store.Update(context.Background(), func(tx *Tx) error {
		r, e := tx.Get(release.ID)
		if e != nil {
			return e
		}
		r.Data["approvedBinding"] = "revoked"
		return tx.Put(r)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Command(context.Background(), worker, "tasks.complete", M{"id": run.ID, "leaseToken": S(run.Data, "leaseToken"), "exitCode": 0, "result": M{"externalId": "apple:123:upload1", "state": "awaiting_review"}}); err == nil {
		t.Fatal("revoked approval was overridden by old continuation completion")
	}
}

func TestChannelApprovalAndPublicAvailabilityStayDistinct(t *testing.T) {
	s, officer, worker, release := awaitingChannel(t)
	for i, state := range []string{"awaiting_release", "published"} {
		must(t, s, officer, "releases.refresh", M{"id": release.ID, "target": "web", "key": state})
		run := must(t, s, worker, "tasks.claim", M{})
		must(t, s, worker, "tasks.complete", M{"id": run.ID, "leaseToken": S(run.Data, "leaseToken"), "exitCode": 0, "result": M{"externalId": "channel-version-123", "state": state}})
		current, _ := s.Store.Get(release.ID)
		target := releaseTarget(current, "web")
		if S(target, "status") != state || current.Status == "delivered" {
			t.Fatalf("state %d became independent delivery: %+v", i, target)
		}
	}
}

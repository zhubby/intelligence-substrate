package platform

import (
	"context"
	"testing"
	"time"
)

func awaitingChannel(t *testing.T) (*Service, Actor, Actor, Resource) {
	t.Helper()
	s, officer, worker, release := publishFixture(t)
	must(t, s, officer, "releases.dispatch", M{"id": release.ID, "target": "web", "workerId": worker.WorkerID, "key": "publish"})
	run := must(t, s, worker, "tasks.claim", M{})
	must(t, s, worker, "tasks.complete", M{"id": run.ID, "leaseToken": S(run.Data, "leaseToken"), "exitCode": 0, "result": M{"externalId": "channel-version-123", "state": "awaiting_review"}})
	release, _ = s.Store.Get(release.ID)
	return s, officer, worker, release
}

func TestChannelQueriesReleaseResourcesAndNeverVerifyDelivery(t *testing.T) {
	s, officer, worker, release := awaitingChannel(t)
	target := Obj(Items(release.Data["targets"])[0])
	if S(target, "nextCheckAt") == "" || S(target, "publisherWorkerId") != worker.WorkerID {
		t.Fatal("accepted review did not persist its next query and assigned publisher")
	}
	if idle := must(t, s, worker, "tasks.claim", M{}); idle.Kind == "run" {
		t.Fatal("waiting for review held an execution lease or queried immediately")
	}
	request := M{"id": release.ID, "target": "web", "key": "manual-query"}
	query := must(t, s, officer, "releases.refresh", request)
	if S(query.Data, "kind") != "publish_status" {
		t.Fatal("refresh created a mutating publication")
	}
	if replay := must(t, s, officer, "releases.refresh", request); replay.ID != query.ID {
		t.Fatal("duplicate refresh created another query")
	}
	if _, err := s.Command(context.Background(), Actor{ID: "agent", Kind: "agent", Admin: true}, "releases.refresh", request); err == nil {
		t.Fatal("agent acquired production querying authority")
	}
	run := must(t, s, worker, "tasks.claim", M{})
	complete := M{"id": run.ID, "leaseToken": S(run.Data, "leaseToken"), "exitCode": 0, "result": M{"externalId": "other-version", "state": "published"}}
	if _, err := s.Command(context.Background(), worker, "tasks.complete", complete); err == nil {
		t.Fatal("query overwrote a different channel version")
	}
	complete["result"] = M{"externalId": "channel-version-123", "state": "verified"}
	if _, err := s.Command(context.Background(), worker, "tasks.complete", complete); err == nil {
		t.Fatal("publisher query substituted for independent publication verification")
	}
	complete["result"] = M{"externalId": "channel-version-123", "state": "published", "upstreamState": "AVAILABLE"}
	must(t, s, worker, "tasks.complete", complete)
	must(t, s, worker, "tasks.complete", complete)
	current, _ := s.Store.Get(release.ID)
	target = Obj(Items(current.Data["targets"])[0])
	if S(target, "status") != "published" || S(target, "lastCheckedAt") == "" || S(target, "upstreamState") != "AVAILABLE" || current.Status == "delivered" {
		t.Fatalf("query did not preserve truthful publication state: %+v", current)
	}
}

func TestChannelScheduledQueryFailureDoesNotResubmitOrEraseState(t *testing.T) {
	s, _, worker, release := awaitingChannel(t)
	if err := s.Store.Update(context.Background(), func(tx *Tx) error {
		current, e := tx.Get(release.ID)
		if e != nil {
			return e
		}
		Obj(Items(current.Data["targets"])[0])["nextCheckAt"] = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
		return tx.Put(current)
	}); err != nil {
		t.Fatal(err)
	}
	run := must(t, s, worker, "tasks.claim", M{})
	if run.Kind != "run" || S(Obj(run.Data["task"]), "kind") != "publish_status" {
		t.Fatalf("due review query was not resumed: %+v", run)
	}
	must(t, s, worker, "tasks.complete", M{"id": run.ID, "leaseToken": S(run.Data, "leaseToken"), "exitCode": 1, "result": M{"reason": "Channel temporarily unavailable"}})
	current, _ := s.Store.Get(release.ID)
	target := Obj(Items(current.Data["targets"])[0])
	if S(target, "status") != "awaiting_review" || S(target, "externalId") != "channel-version-123" || S(target, "queryError") == "" || S(target, "nextCheckAt") <= now() {
		t.Fatalf("query failure erased known state or lost bounded rescheduling: %+v", target)
	}
	if idle := must(t, s, worker, "tasks.claim", M{}); idle.Kind == "run" {
		t.Fatal("query failure triggered an immediate retry or new publication")
	}
}

func TestHumanChannelDecisionCancelsQueryLease(t *testing.T) {
	s, officer, worker, release := awaitingChannel(t)
	must(t, s, officer, "releases.refresh", M{"id": release.ID, "target": "web", "key": "check"})
	run := must(t, s, worker, "tasks.claim", M{})
	must(t, s, officer, "releases.transition", M{"id": release.ID, "target": "web", "state": "halted", "reason": "Operator stopped this distribution"})
	current, err := s.Store.Get(run.ID)
	if err != nil || current.Status != "cancelled" {
		t.Fatalf("human decision left an old status lease active: %+v %v", current, err)
	}
	if _, err = s.Command(context.Background(), worker, "tasks.complete", M{"id": run.ID, "leaseToken": S(run.Data, "leaseToken"), "exitCode": 0, "result": M{"externalId": "channel-version-123", "state": "published"}}); err == nil {
		t.Fatal("old query overwrote human halt")
	}
	if idle := must(t, s, worker, "tasks.claim", M{}); idle.Kind == "run" {
		t.Fatal("cancelled query reacquired a lease")
	}
}

func TestExhaustedChannelQueryDoesNotPoisonWorkerOrLoseWaitingState(t *testing.T) {
	s, officer, worker, release := awaitingChannel(t)
	query := must(t, s, officer, "releases.refresh", M{"id": release.ID, "target": "web", "key": "single-attempt"})
	run := must(t, s, worker, "tasks.claim", M{})
	query.Data["maxAttempts"] = 1
	query.Data["attempts"] = 1
	run.Data["leaseUntil"] = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
	if err := s.Store.Update(context.Background(), func(tx *Tx) error {
		if err := tx.Put(query); err != nil {
			return err
		}
		return tx.Put(run)
	}); err != nil {
		t.Fatal(err)
	}
	if idle := must(t, s, worker, "tasks.claim", M{}); idle.Kind == "run" {
		t.Fatal("exhausted query immediately reacquired resources")
	}
	current, _ := s.Store.Get(release.ID)
	target := Obj(Items(current.Data["targets"])[0])
	if S(target, "status") != "awaiting_review" || S(target, "queryError") == "" {
		t.Fatalf("lease exhaustion changed known publication state: %+v", target)
	}
}

func TestInconclusiveChannelObservationPreservesKnownStateAndTimestamp(t *testing.T) {
	for _, known := range []string{"awaiting_review", "published"} {
		t.Run(known, func(t *testing.T) {
			s, officer, worker, release := awaitingChannel(t)
			if known == "published" {
				artifacts, _ := s.Store.List(release.ProjectID, "artifact", release.GoalID)
				var proofID string
				for _, a := range artifacts {
					if S(a.Data, "type") == "report" {
						proofID = a.ID
					}
				}
				must(t, s, officer, "releases.transition", M{"id": release.ID, "target": "web", "state": "published", "externalId": "channel-version-123", "proofArtifactId": proofID})
			}
			before, _ := s.Store.Get(release.ID)
			timestamp := S(Obj(Items(before.Data["targets"])[0]), "updatedAt")
			must(t, s, officer, "releases.refresh", M{"id": release.ID, "target": "web", "key": "observe"})
			run := must(t, s, worker, "tasks.claim", M{})
			must(t, s, worker, "tasks.complete", M{"id": run.ID, "leaseToken": S(run.Data, "leaseToken"), "exitCode": 0, "result": M{"externalId": "channel-version-123", "state": "unchanged", "upstreamState": "completed", "reason": "Track API does not prove public availability"}})
			current, _ := s.Store.Get(release.ID)
			target := Obj(Items(current.Data["targets"])[0])
			if S(target, "status") != known || S(target, "updatedAt") != timestamp || S(target, "lastCheckedAt") == "" {
				t.Fatalf("inconclusive query overrode or re-dated a publication: %+v", target)
			}
		})
	}
}

func TestReadOnlyChannelQueriesDoNotRequireMoreModelBudget(t *testing.T) {
	s, officer, worker, release := awaitingChannel(t)
	if err := s.Store.Update(context.Background(), func(tx *Tx) error {
		goal, e := tx.Get(release.GoalID)
		if e != nil {
			return e
		}
		goal.Data["spentCents"] = N(goal.Data, "budgetCents") + 1
		return tx.Put(goal)
	}); err != nil {
		t.Fatal(err)
	}
	must(t, s, officer, "releases.refresh", M{"id": release.ID, "target": "web", "key": "observe-without-model"})
	run := must(t, s, worker, "tasks.claim", M{})
	if run.Kind != "run" || S(Obj(run.Data["task"]), "kind") != "publish_status" {
		t.Fatal("model spend prevented observing an already submitted external operation")
	}
}

package platform

import (
	"context"
	"testing"
	"time"
)

func TestWorkerLeaseCannotMutateAnotherGoalOrRepository(t *testing.T) {
	s, a, p := fixture(t)
	g := goal(t, s, a, p)
	other := goal(t, s, a, p)
	must(t, s, a, "goals.approve", M{"id": g.ID})
	must(t, s, a, "goals.approve", M{"id": other.ID})
	repo := must(t, s, a, "repositories.create", M{"projectId": p.ID, "title": "Assigned repository"})
	outside := must(t, s, a, "repositories.create", M{"projectId": p.ID, "title": "Other repository"})
	plan := must(t, s, a, "plans.propose", M{"goalId": g.ID, "summary": "Bound implementation", "tasks": []any{M{"id": "code", "title": "Implement", "kind": "code", "role": "developer", "runtime": "linux", "repositoryId": repo.ID}}})
	must(t, s, a, "plans.approve", M{"id": plan.ID})
	w := must(t, s, a, "workers.register", M{"projectId": p.ID, "title": "Developer", "runtime": "linux", "roles": []any{"developer"}, "capabilities": []any{"code"}})
	actor := Actor{ID: "worker:" + w.ID, Kind: "worker", ProjectID: p.ID, WorkerID: w.ID, Roles: []string{"developer"}}
	run := must(t, s, actor, "tasks.claim", M{})
	for command, input := range map[string]M{
		"plans.propose":       {"goalId": other.ID, "summary": "Unauthorized scope", "tasks": []any{M{"id": "code", "title": "Implement", "kind": "code", "role": "developer", "runtime": "linux"}}},
		"repositories.commit": {"id": outside.ID, "branch": "feature", "message": "Unauthorized repository", "files": M{"app.txt": "other"}},
	} {
		input["runId"] = run.ID
		input["leaseToken"] = S(run.Data, "leaseToken")
		if _, e := s.Command(context.Background(), actor, command, input); e == nil {
			t.Fatalf("valid lease escaped assigned scope via %s", command)
		}
	}
	if _, e := s.Command(context.Background(), actor, "tasks.complete", M{"id": run.ID, "leaseToken": S(run.Data, "leaseToken"), "exitCode": 0, "result": M{"summary": "Claimed complete without source"}}); e == nil {
		t.Fatal("code completed without a real commit")
	}
}

func TestKnowledgeRevisionRetainsPreviousContentAndSource(t *testing.T) {
	s, a, p := fixture(t)
	old := must(t, s, a, "knowledge.create", M{"projectId": p.ID, "title": "API contract", "content": "v1 behavior", "source": "customer interview"})
	next := must(t, s, a, "knowledge.revise", M{"id": old.ID, "content": "v2 behavior", "reason": "Clarified acceptance"})
	stored, _ := s.Store.Get(old.ID)
	if next.ID == old.ID || next.Revision != 2 || S(next.Data, "previousId") != old.ID || S(next.Data, "source") != "customer interview" || stored.Status != "superseded" || S(stored.Data, "content") != "v1 behavior" {
		t.Fatal("knowledge history was overwritten")
	}
	if _, e := s.Command(context.Background(), a, "knowledge.revise", M{"id": old.ID, "content": "fork", "reason": "Stale writer"}); e == nil {
		t.Fatal("stale knowledge version accepted")
	}
}

func TestQualityWorkerCannotAlsoProduceCode(t *testing.T) {
	s, a, p := fixture(t)
	if _, e := s.Command(context.Background(), a, "workers.register", M{"projectId": p.ID, "title": "Mixed trust", "runtime": "linux", "roles": []any{"developer", "quality"}, "capabilities": []any{"code", "test"}, "trustedQuality": true}); e == nil {
		t.Fatal("quality trust allowed a code producer on the same identity")
	}
}

func TestTaskBudgetsCannotBeNegative(t *testing.T) {
	s, a, p := fixture(t)
	g := goal(t, s, a, p)
	must(t, s, a, "goals.approve", M{"id": g.ID})
	if _, e := s.Command(context.Background(), a, "plans.propose", M{"goalId": g.ID, "summary": "Bad budget", "tasks": []any{M{"id": "code", "title": "Code", "role": "developer", "runtime": "linux", "kind": "code", "budgetCents": -100}}}); e == nil {
		t.Fatal("negative task budget bypassed budget checks")
	}
}

func TestMessageDeadLetterPersistsOnEmptyPoll(t *testing.T) {
	s, a, p := fixture(t)
	message := must(t, s, a, "messages.send", M{"projectId": p.ID, "recipient": a.ID, "type": "request", "key": "msg", "payload": M{"work": "review"}})
	if e := s.Store.Update(context.Background(), func(tx *Tx) error {
		message.Status = "leased"
		message.Data["attempts"] = 3
		message.Data["leaseUntil"] = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
		return tx.Put(message)
	}); e != nil {
		t.Fatal(e)
	}
	must(t, s, a, "messages.claim", M{"projectId": p.ID})
	stored, _ := s.Store.Get(message.ID)
	if stored.Status != "blocked" {
		t.Fatal("empty polling rolled back dead-letter transition")
	}
}

func TestDisabledWorkerCannotClaim(t *testing.T) {
	s, a, p := fixture(t)
	w := must(t, s, a, "workers.register", M{"projectId": p.ID, "title": "Runner", "runtime": "linux", "roles": []any{"developer"}, "capabilities": []any{"code"}})
	w.Status = "disabled"
	if e := s.Store.Update(context.Background(), func(tx *Tx) error { return tx.Put(w) }); e != nil {
		t.Fatal(e)
	}
	worker := Actor{ID: "worker:" + w.ID, Kind: "worker", WorkerID: w.ID, ProjectID: p.ID, Roles: []string{"developer"}}
	if _, e := s.Command(context.Background(), worker, "tasks.claim", M{}); e == nil {
		t.Fatal("disabled worker revived itself")
	}
}

package platform

import (
	"context"
	"testing"
)

func TestAdaptiveSplitKeepsApprovedEnvelope(t *testing.T) {
	s, a, p := fixture(t)
	g := goal(t, s, a, p)
	must(t, s, a, "goals.approve", M{"id": g.ID})
	plan := must(t, s, a, "plans.propose", M{"goalId": g.ID, "summary": "Build portal", "tasks": []any{M{"id": "code", "title": "Implementation", "kind": "code", "role": "developer", "runtime": "linux", "budgetCents": 1000}, M{"id": "build", "title": "Build", "kind": "build", "role": "developer", "runtime": "linux", "dependsOn": []any{"code"}}}})
	must(t, s, a, "plans.approve", M{"id": plan.ID})
	tasks, _ := s.Store.List(p.ID, "task", g.ID)
	var parent Resource
	for _, task := range tasks {
		if S(task.Data, "logicalId") == "code" {
			parent = task
		}
	}
	must(t, s, a, "tasks.split", M{"id": parent.ID, "reason": "Independent UI and API work", "impact": "Same scope and budget; review both changes before build", "children": []any{M{"title": "UI", "instructions": "Implement the approved UI"}, M{"title": "API", "instructions": "Implement the approved API"}}})
	tasks, _ = s.Store.List(p.ID, "task", g.ID)
	sum := 0.0
	children := 0
	for _, task := range tasks {
		if S(task.Data, "parentId") == parent.ID {
			children++
			sum += N(task.Data, "budgetCents")
			if S(task.Data, "role") != "developer" || S(task.Data, "runtime") != "linux" {
				t.Fatal("split increased authority")
			}
			task.Status = "succeeded"
			if e := s.Store.Update(context.Background(), func(tx *Tx) error { return tx.Put(task) }); e != nil {
				t.Fatal(e)
			}
		}
	}
	if children != 2 || sum != 1000 {
		t.Fatal("split changed total budget")
	}
	if e := s.Store.Update(context.Background(), func(tx *Tx) error { return s.aggregateChildren(tx, p.ID) }); e != nil {
		t.Fatal(e)
	}
	parent, _ = s.Store.Get(parent.ID)
	if parent.Status != "succeeded" {
		t.Fatal("parent completed before or failed to aggregate all children")
	}
}

func TestCancelSplitParentRevokesChildrenAndActiveLease(t *testing.T) {
	s, a, p := fixture(t)
	g := goal(t, s, a, p)
	must(t, s, a, "goals.approve", M{"id": g.ID})
	plan := must(t, s, a, "plans.propose", M{"goalId": g.ID, "summary": "Implement portal", "tasks": []any{M{"id": "code", "title": "Implement", "kind": "code", "role": "developer", "runtime": "linux"}}})
	must(t, s, a, "plans.approve", M{"id": plan.ID})
	tasks, _ := s.Store.List(p.ID, "task", g.ID)
	parent := tasks[0]
	must(t, s, a, "tasks.split", M{"id": parent.ID, "reason": "Independent work", "impact": "Same scope", "children": []any{M{"title": "UI", "instructions": "Implement approved UI"}, M{"title": "API", "instructions": "Implement approved API"}}})
	w := must(t, s, a, "workers.register", M{"projectId": p.ID, "title": "Developer", "runtime": "linux", "roles": []any{"developer"}, "capabilities": []any{"code"}})
	actor := Actor{ID: "worker:" + w.ID, Kind: "worker", ProjectID: p.ID, WorkerID: w.ID, Roles: []string{"developer"}}
	run := must(t, s, actor, "tasks.claim", M{})
	must(t, s, a, "tasks.cancel", M{"id": parent.ID})
	tasks, _ = s.Store.List(p.ID, "task", g.ID)
	for _, task := range tasks {
		if task.Status != "cancelled" {
			t.Fatalf("child remained executable: %+v", task)
		}
	}
	if _, e := s.Command(context.Background(), actor, "tasks.progress", M{"id": run.ID, "leaseToken": S(run.Data, "leaseToken"), "message": "late heartbeat"}); e == nil {
		t.Fatal("cancelled child kept its lease")
	}
}

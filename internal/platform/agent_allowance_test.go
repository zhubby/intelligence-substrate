package platform

import (
	"context"
	"strings"
	"testing"
	"time"
)

// A fresh allowance on retry is the bug: the approved task owns 100 cents and
// 600 active seconds in total, regardless of how many nodes run it.
func seedAgentUsage(t *testing.T, f *messageFixture, status string, spent float64, calls int, chargeStatus string, seconds int) {
	t.Helper()
	end := time.Now().Add(-time.Second).UTC()
	start := end.Add(-time.Duration(seconds) * time.Second)
	if e := f.s.Store.Update(context.Background(), func(tx *Tx) error {
		run, e := tx.Get(f.run.ID)
		if e != nil {
			return e
		}
		run.CreatedAt = start.Format(time.RFC3339Nano)
		run.Status = status
		run.Data["spentCents"] = spent
		run.Data["completedAt"] = end.Format(time.RFC3339Nano)
		run.Data["leaseUntil"] = end.Format(time.RFC3339Nano)
		run.Data["deadline"] = start.Add(600 * time.Second).Format(time.RFC3339Nano)
		if status != "running" {
			delete(run.Data, "leaseToken")
		}
		task, e := tx.Get(S(run.Data, "taskId"))
		if e != nil {
			return e
		}
		task.Status = "failed"
		if status == "running" {
			task.Status = "running"
		}
		goal, e := tx.Get(f.g.ID)
		if e != nil {
			return e
		}
		goal.Data["spentCents"] = spent
		for _, r := range []Resource{run, task, goal} {
			if e = tx.Put(r); e != nil {
				return e
			}
		}
		for i := 0; i < calls; i++ {
			charge := resource("charge", "Deterministic request", f.p.ID, f.g.ID, M{"runId": run.ID, "reservedCents": 1, "chargedCents": 0})
			charge.Status = "settled"
			if i == 0 {
				charge.Data["chargedCents"], charge.Status = spent, chargeStatus
			}
			if e = tx.Put(charge); e != nil {
				return e
			}
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
}

func newAllowanceFixture(t *testing.T) *messageFixture {
	s, a, project := fixture(t)
	return newMessageFixtureOn(t, s, a, project, M{"id": "analysis", "budgetCents": 100, "timeoutSeconds": 600})
}

func TestAgentRetryRetainsCumulativeMoneyTimeAndCalls(t *testing.T) {
	for _, status := range []string{"failed", "running"} {
		t.Run(status, func(t *testing.T) {
			f := newAllowanceFixture(t)
			seedAgentUsage(t, f, status, 40, 7, "settled", 120)
			if status == "failed" {
				must(t, f.s, f.a, "tasks.retry", M{"id": S(f.run.Data, "taskId")})
			}
			replacement := f.register(t, "Replacement Agent")
			next := must(t, f.s, replacement, "tasks.claim", M{})
			if next.Kind != "run" || N(next.Data, "reservedCents") != 60 {
				t.Fatal("retry reset the task's 100-cent budget instead of reserving the remaining 60", next)
			}
			deadline, e := time.Parse(time.RFC3339Nano, S(next.Data, "deadline"))
			if e != nil {
				t.Fatal(e)
			}
			if remaining := time.Until(deadline); remaining > 480*time.Second || remaining < 478*time.Second {
				t.Fatal("retry reset or lost the remaining 480 active seconds", remaining)
			}
			if N(Obj(next.Data["agentAllowance"]), "usedModelCalls") != 7 {
				t.Fatal("replacement lost the seven already-reserved model requests", next)
			}
			original, _ := f.s.Store.Get(f.run.ID)
			if _, ok := original.Data["leaseToken"]; ok {
				t.Fatal("replacement revived the original lease")
			}
		})
	}
}

func TestAgentRetryRejectsUnresolvedAndExhaustedUsage(t *testing.T) {
	for _, mode := range []string{"reserved", "uncertain", "budget", "time", "calls"} {
		t.Run(mode, func(t *testing.T) {
			f := newAllowanceFixture(t)
			spent, calls, status, seconds := float64(40), 2, "settled", 120
			switch mode {
			case "reserved", "uncertain":
				status = mode
			case "budget":
				spent = 100
			case "time":
				seconds = 600
			case "calls":
				calls = 24
			}
			seedAgentUsage(t, f, "failed", spent, calls, status, seconds)
			if _, e := f.s.Command(context.Background(), f.a, "tasks.retry", M{"id": S(f.run.Data, "taskId")}); e == nil {
				t.Fatal("ordinary retry expanded an exhausted/unreconciled task", mode)
			}
			task, _ := f.s.Store.Get(S(f.run.Data, "taskId"))
			if task.Status != "failed" || N(task.Data, "attempts") != 1 {
				t.Fatal("denied retry mutated task", task)
			}
		})
	}
}

func TestAgentExpiredClaimBlocksBeforeReservingANewRun(t *testing.T) {
	for _, mode := range []string{"reserved", "uncertain", "budget", "time", "calls"} {
		t.Run(mode, func(t *testing.T) {
			f := newAllowanceFixture(t)
			spent, calls, status, seconds := float64(40), 2, "settled", 120
			switch mode {
			case "reserved", "uncertain":
				status = mode
			case "budget":
				spent = 100
			case "time":
				seconds = 600
			case "calls":
				calls = 24
			}
			seedAgentUsage(t, f, "running", spent, calls, status, seconds)
			next := must(t, f.s, f.register(t, "Replacement"), "tasks.claim", M{})
			task, _ := f.s.Store.Get(S(f.run.Data, "taskId"))
			runs, _ := f.s.Store.List(f.p.ID, "run", f.g.ID)
			if next.Kind == "run" || task.Status != "blocked" || len(runs) != 1 || N(task.Data, "attempts") != 1 || strings.TrimSpace(S(task.Data, "reason")) == "" {
				t.Fatal("expired task was not fenced before another reservation", mode, next, task, len(runs))
			}
		})
	}
}

func TestAgentReconciliationRestoresOnlyRemainingAllowance(t *testing.T) {
	f := newAllowanceFixture(t)
	seedAgentUsage(t, f, "running", 40, 7, "uncertain", 120)
	replacement := f.register(t, "Replacement")
	if next := must(t, f.s, replacement, "tasks.claim", M{}); next.Kind == "run" {
		t.Fatal("unreconciled task was claimed")
	}
	charges, _ := f.s.Store.List(f.p.ID, "charge", f.g.ID)
	for _, charge := range charges {
		if charge.Status == "uncertain" {
			must(t, f.s, f.a, "charges.reconcile", M{"id": charge.ID, "chargedCents": 25, "reason": "Synthetic provider statement confirms 25 cents"})
		}
	}
	must(t, f.s, f.a, "tasks.retry", M{"id": S(f.run.Data, "taskId")})
	next := must(t, f.s, replacement, "tasks.claim", M{})
	if next.Kind != "run" || N(next.Data, "reservedCents") != 75 || N(Obj(next.Data["agentAllowance"]), "usedModelCalls") != 7 {
		t.Fatal("reconciliation reset money or refunded consumed calls", next)
	}
}

func TestAgentRetryPreservesLegacyStepFloorAcrossFailures(t *testing.T) {
	f := newAllowanceFixture(t)
	seedAgentUsage(t, f, "failed", 1, 1, "settled", 1)
	if e := f.s.Store.Update(context.Background(), func(tx *Tx) error {
		run, e := tx.Get(f.run.ID)
		if e != nil {
			return e
		}
		run.Data["agentAllowance"] = M{"usedModelCalls": 20}
		return tx.Put(run)
	}); e != nil {
		t.Fatal(e)
	}
	must(t, f.s, f.a, "tasks.retry", M{"id": S(f.run.Data, "taskId")})
	next := must(t, f.s, f.worker, "tasks.claim", M{})
	if N(Obj(next.Data["agentAllowance"]), "usedModelCalls") != 21 {
		t.Fatal("legacy floor was discarded", next.Data["agentAllowance"])
	}
	if e := f.s.Store.Update(context.Background(), func(tx *Tx) error {
		charge := resource("charge", "One more actual request", f.p.ID, f.g.ID, M{"runId": next.ID, "reservedCents": 1, "chargedCents": 0})
		charge.Status = "settled"
		return tx.Put(charge)
	}); e != nil {
		t.Fatal(e)
	}
	must(t, f.s, f.worker, "tasks.complete", M{"id": next.ID, "leaseToken": S(next.Data, "leaseToken"), "exitCode": 1, "result": M{}})
	must(t, f.s, f.a, "tasks.retry", M{"id": S(f.run.Data, "taskId")})
	third := must(t, f.s, f.worker, "tasks.claim", M{})
	if N(Obj(third.Data["agentAllowance"]), "usedModelCalls") != 22 {
		t.Fatal("second retry refunded a call from the legacy floor", third.Data["agentAllowance"])
	}
}

func TestAgentAllowanceIgnoresOtherTasksUnknownCharges(t *testing.T) {
	f := newAllowanceFixture(t)
	seedAgentUsage(t, f, "failed", 40, 7, "settled", 120)
	if e := f.s.Store.Update(context.Background(), func(tx *Tx) error {
		run := resource("run", "Unrelated task", f.p.ID, f.g.ID, M{"taskId": "unrelated-task"})
		run.Status = "failed"
		if e := tx.Put(run); e != nil {
			return e
		}
		charge := resource("charge", "Unrelated reservation", f.p.ID, f.g.ID, M{"runId": run.ID, "reservedCents": 1})
		charge.Status = "reserved"
		return tx.Put(charge)
	}); e != nil {
		t.Fatal(e)
	}
	must(t, f.s, f.a, "tasks.retry", M{"id": S(f.run.Data, "taskId")})
	next := must(t, f.s, f.worker, "tasks.claim", M{})
	if next.Kind != "run" || N(next.Data, "reservedCents") != 60 {
		t.Fatal("other task's unknown charge contaminated this task")
	}
}

func TestAgentCodeAndDesignRetryRetainsAllowanceAndSource(t *testing.T) {
	for _, kind := range []string{"code", "design"} {
		t.Run(kind, func(t *testing.T) {
			s, owner, project := fixture(t)
			f, _, _ := checkpointFixtureOn(t, s, owner, project, kind)
			original := Obj(f.run.Data["task"])
			seedAgentUsage(t, f, "failed", 40, 7, "settled", 120)
			must(t, s, owner, "tasks.retry", M{"id": S(f.run.Data, "taskId")})
			next := must(t, s, f.worker, "tasks.claim", M{})
			if next.Kind != "run" || N(next.Data, "reservedCents") != 60 || N(Obj(next.Data["agentAllowance"]), "usedModelCalls") != 7 {
				t.Fatal("workspace retry reset allowance")
			}
			bound := Obj(next.Data["task"])
			if S(bound, "repositoryId") != S(original, "repositoryId") || S(bound, "commit") != S(original, "commit") || Hash(bound["sourceBinding"]) != Hash(original["sourceBinding"]) {
				t.Fatal("retry changed approved source input")
			}
		})
	}
}

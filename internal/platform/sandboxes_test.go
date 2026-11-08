package platform

import (
	"context"
	"strings"
	"testing"
	"time"
)

func sandboxInput(f *messageFixture) M {
	return M{"runId": f.run.ID, "leaseToken": S(f.run.Data, "leaseToken"), "key": "command-1", "command": []any{"/bin/true"}, "backend": M{"context": "enterprise", "namespace": "ax-system", "atespace": "project-one", "image": "runner@sha256:" + strings.Repeat("a", 64)}}
}

func expireSandboxRun(t *testing.T, f *messageFixture) {
	t.Helper()
	if e := f.s.Store.Update(context.Background(), func(tx *Tx) error {
		run, e := tx.Get(f.run.ID)
		if e != nil {
			return e
		}
		run.Data["leaseUntil"] = time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)
		return tx.Put(run)
	}); e != nil {
		t.Fatal(e)
	}
}

func TestSandboxPrepareReplayCannotCreateAnotherIdentity(t *testing.T) {
	f := newAllowanceFixture(t)
	input := sandboxInput(f)
	first := must(t, f.s, f.worker, "sandboxes.prepare", input)
	second := must(t, f.s, f.worker, "sandboxes.prepare", input)
	if first.ID != second.ID || B(first.Data, "replayed") || !B(second.Data, "replayed") {
		t.Fatal("prepare replay acquired a new execution", first, second)
	}
	input["command"] = []any{"/bin/false"}
	if _, e := f.s.Command(context.Background(), f.worker, "sandboxes.prepare", input); e == nil {
		t.Fatal("same key accepted a different command")
	}
	input["key"] = "another-command"
	if _, e := f.s.Command(context.Background(), f.worker, "sandboxes.prepare", input); e == nil {
		t.Fatal("second command started while the first guest was unknown")
	}
	all, _ := f.s.Store.List(f.p.ID, "sandbox", f.g.ID)
	if len(all) != 1 {
		t.Fatal("replay created multiple guests", len(all))
	}
}

func TestSandboxReconcileRequiresDisabledWorkerAndInactiveLease(t *testing.T) {
	f := newAllowanceFixture(t)
	remote := must(t, f.s, f.worker, "sandboxes.prepare", sandboxInput(f))
	input := M{"id": remote.ID, "name": remote.Data["name"], "backendDigest": remote.Data["backendDigest"], "key": "verified-stopped", "observation": "Operator isolated the original node and independently verified Task, Workspace and Actor absent"}
	if _, e := f.s.Command(context.Background(), f.a, "sandboxes.reconcile", input); e == nil {
		t.Fatal("live worker could later create the sandbox")
	}
	must(t, f.s, f.a, "workers.disable", M{"id": f.worker.WorkerID})
	if _, e := f.s.Command(context.Background(), f.a, "sandboxes.reconcile", input); e == nil {
		t.Fatal("active lease was treated as proof of stopped process")
	}
	expireSandboxRun(t, f)
	wrong := copyM(input)
	wrong["name"] = "unrelated-remote-task"
	if _, e := f.s.Command(context.Background(), f.a, "sandboxes.reconcile", wrong); e == nil {
		t.Fatal("reconciled another remote resource")
	}
	closed := must(t, f.s, f.a, "sandboxes.reconcile", input)
	if closed.Status != "stopped" || S(closed.Data, "reconciledBy") != f.a.ID {
		t.Fatal("decision lost attribution", closed)
	}
	must(t, f.s, f.a, "sandboxes.reconcile", input)
	if _, e := f.s.Command(context.Background(), f.worker, "sandboxes.finish", M{"id": remote.ID, "leaseToken": f.run.Data["leaseToken"], "name": remote.Data["name"], "backendDigest": remote.Data["backendDigest"], "stopped": true, "observation": "late worker"}); e == nil {
		t.Fatal("disabled original worker could finish")
	}
}

func TestSandboxUnknownCleanupFencesRetryAndOtherGoalRemainsAvailable(t *testing.T) {
	f := newAllowanceFixture(t)
	remote := must(t, f.s, f.worker, "sandboxes.prepare", sandboxInput(f))
	input := M{"id": remote.ID, "leaseToken": f.run.Data["leaseToken"], "name": remote.Data["name"], "backendDigest": remote.Data["backendDigest"], "stopped": false, "observation": "AX Task deletion denied; exact guest may still run"}
	if got := must(t, f.s, f.worker, "sandboxes.finish", input); got.Status != "unresolved" {
		t.Fatal(got.Status)
	}
	must(t, f.s, f.worker, "tasks.complete", M{"id": f.run.ID, "leaseToken": f.run.Data["leaseToken"], "exitCode": 1, "result": M{"blocked": true}})
	if _, e := f.s.Command(context.Background(), f.a, "tasks.retry", M{"id": S(f.run.Data, "taskId")}); e == nil {
		t.Fatal("retry bypassed remote termination")
	}
	other := newMessageFixtureOn(t, f.s, f.a, f.p, M{"id": "unrelated", "budgetCents": 20})
	if other.run.Kind != "run" || other.run.GoalID == f.g.ID {
		t.Fatal("unrelated goal blocked by an old guest")
	}
}

func TestSandboxPrepareRejectsUntrustedInputs(t *testing.T) {
	for _, mode := range []string{"human", "agent", "lease", "capability", "runtime", "plan", "command", "backend", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			f := newAllowanceFixture(t)
			input := sandboxInput(f)
			actor := f.worker
			switch mode {
			case "human":
				actor = f.a
			case "agent":
				actor = Actor{ID: "agent-test", Kind: "agent", ProjectID: f.p.ID, Roles: []string{"developer"}}
			case "lease":
				input["leaseToken"] = "forged"
			case "command":
				input["command"] = []any{"/bin/true", 42}
			case "backend":
				Obj(input["backend"])["atespace"] = "../../other"
			case "unknown":
				input["secret"] = "not accepted"
			default:
				if e := f.s.Store.Update(context.Background(), func(tx *Tx) error {
					if mode == "plan" {
						g, e := tx.Get(f.g.ID)
						if e != nil {
							return e
						}
						g.Data["activePlanId"] = "replaced"
						return tx.Put(g)
					}
					w, e := tx.Get(f.worker.WorkerID)
					if e != nil {
						return e
					}
					if mode == "runtime" {
						w.Data["runtime"] = "darwin"
					} else {
						w.Data["capabilities"] = []any{"build"}
					}
					return tx.Put(w)
				}); e != nil {
					t.Fatal(e)
				}
			}
			if _, e := f.s.Command(context.Background(), actor, "sandboxes.prepare", input); e == nil {
				t.Fatal("invalid execution admitted", mode)
			}
			all, _ := f.s.Store.List(f.p.ID, "sandbox", f.g.ID)
			if len(all) != 0 {
				t.Fatal("denied request left ownership", len(all))
			}
		})
	}
}

func TestSandboxDisabledOrDeadlineExceededSiblingFencesNewWork(t *testing.T) {
	for _, mode := range []string{"disabled", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			s, owner, project := fixture(t)
			f := newMessageFixtureOn(t, s, owner, project, M{"id": "first"}, M{"id": "second"})
			must(t, f.s, f.worker, "sandboxes.prepare", sandboxInput(f))
			if mode == "disabled" {
				must(t, f.s, f.a, "workers.disable", M{"id": f.worker.WorkerID})
			} else {
				if e := f.s.Store.Update(context.Background(), func(tx *Tx) error {
					run, e := tx.Get(f.run.ID)
					if e != nil {
						return e
					}
					run.Data["deadline"] = time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)
					return tx.Put(run)
				}); e != nil {
					t.Fatal(e)
				}
			}
			if got := must(t, f.s, f.register(t, "New node"), "tasks.claim", M{}); got.Kind == "run" {
				t.Fatal("parallel work bypassed an unsafe old guest", mode)
			}
		})
	}
}

func TestSandboxFixedCommandCannotChangeAtPrepare(t *testing.T) {
	s, owner, project := fixture(t)
	f := newMessageFixtureOn(t, s, owner, project, M{"id": "fixed", "command": []any{"/bin/true"}})
	input := sandboxInput(f)
	input["command"] = []any{"/bin/false"}
	if _, e := s.Command(context.Background(), f.worker, "sandboxes.prepare", input); e == nil {
		t.Fatal("fixed task command was replaced at the execution boundary")
	}
}

func TestSandboxExpiredRunFencesReplacement(t *testing.T) {
	f := newAllowanceFixture(t)
	seedAgentUsage(t, f, "running", 0, 0, "settled", 5)
	if e := f.s.Store.Update(context.Background(), func(tx *Tx) error {
		remote := resource("sandbox", "AX command", f.p.ID, f.g.ID, M{"runId": f.run.ID, "taskId": S(f.run.Data, "taskId"), "workerId": f.worker.WorkerID})
		remote.Status = "prepared"
		return tx.Put(remote)
	}); e != nil {
		t.Fatal(e)
	}
	next := must(t, f.s, f.register(t, "Replacement"), "tasks.claim", M{})
	runs, _ := f.s.Store.List(f.p.ID, "run", f.g.ID)
	if next.Kind == "run" || len(runs) != 1 {
		t.Fatal("unknown AX guest allowed a replacement execution", next.Kind, len(runs))
	}
}

func TestSandboxLifecycleFencesResultUntilExactCleanup(t *testing.T) {
	f := newAllowanceFixture(t)
	input := sandboxInput(f)
	remote := must(t, f.s, f.worker, "sandboxes.prepare", input)
	if remote.Kind != "sandbox" || remote.Status != "prepared" || !strings.HasPrefix(S(remote.Data, "name"), "is-sandbox-") {
		t.Fatal("missing durable remote ownership", remote)
	}
	if _, e := f.s.Command(context.Background(), f.worker, "tasks.complete", M{"id": f.run.ID, "leaseToken": f.run.Data["leaseToken"], "exitCode": 0, "result": M{"summary": "Premature success"}}); e == nil {
		t.Fatal("command completion bypassed unknown remote state")
	}
	finish := M{"id": remote.ID, "leaseToken": f.run.Data["leaseToken"], "name": remote.Data["name"], "backendDigest": remote.Data["backendDigest"], "stopped": true, "observation": "Both exact resources absent from successful AX listings"}
	closed := must(t, f.s, f.worker, "sandboxes.finish", finish)
	if closed.Status != "stopped" {
		t.Fatal("cleanup not recorded", closed.Status)
	}
	must(t, f.s, f.worker, "sandboxes.finish", finish)
	changed := copyM(finish)
	changed["stopped"] = false
	if _, e := f.s.Command(context.Background(), f.worker, "sandboxes.finish", changed); e == nil {
		t.Fatal("conflicting cleanup receipt replaced the first result")
	}
	must(t, f.s, f.worker, "tasks.complete", M{"id": f.run.ID, "leaseToken": f.run.Data["leaseToken"], "exitCode": 0, "result": M{"summary": "Actual command with confirmed cleanup"}})
}

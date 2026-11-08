package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

// Real Git is moved before the SQL trigger fails. This catches recovery paths
// which only repair fabricated database fixtures or re-run the source command.
func interruptedSource(t *testing.T, f *sourceFixture, committed bool) (Resource, Resource, M, []byte) {
	t.Helper()
	run := f.claim(t)
	if run.Kind != "run" {
		t.Fatal("source did not claim", run)
	}
	b := Obj(Obj(run.Data["task"])["sourceBinding"])
	in := M{"key": run.ID, "runId": run.ID, "leaseToken": S(run.Data, "leaseToken"), "branch": S(b, "targetBranch"), "baseCommit": S(b, "commit"), "expectedHead": S(b, "expectedHead"), "message": "Persist interrupted application"}
	body := sourceTar(t, map[string][]byte{"app.txt": []byte("candidate\n")}, "")
	if !committed {
		if _, e := f.s.Store.DB.Exec(`CREATE TRIGGER fail_recovery_finalize BEFORE UPDATE ON resources WHEN NEW.kind='source_commit' AND NEW.status='committed' BEGIN SELECT RAISE(ABORT,'injected recovery interruption'); END`); e != nil {
			t.Fatal(e)
		}
	}
	_, e := f.s.CommitSnapshot(context.Background(), f.worker, f.repo.ID, in, bytes.NewReader(body))
	if !committed {
		if e == nil {
			t.Fatal("SQL interruption did not fire")
		}
		if _, e = f.s.Store.DB.Exec(`DROP TRIGGER fail_recovery_finalize`); e != nil {
			t.Fatal(e)
		}
	} else if e != nil {
		t.Fatal(e)
	}
	ops, e := f.s.Store.List(f.p.ID, "source_commit", f.g.ID)
	if e != nil || len(ops) != 1 {
		t.Fatal("candidate was lost", e, ops)
	}
	head, e := f.s.Git(f.repo, "rev-parse", "refs/heads/"+S(in, "branch"))
	if e != nil || head != S(ops[0].Data, "commit") {
		t.Fatal("fault missed actual Git mutation", head, e)
	}
	return run, ops[0], in, body
}

func recoveryInput(op Resource) M {
	return M{"id": op.ID, "key": "human-decision", "commit": S(op.Data, "commit"), "observedHead": S(op.Data, "commit"), "decision": "confirm", "reason": "已核对候选源码与原批准工作项，继续独立构建验证"}
}
func expireSourceRun(t *testing.T, f *sourceFixture, run Resource) {
	t.Helper()
	run.Data["leaseUntil"] = "2000-01-01T00:00:00Z"
	f.put(t, run)
}

func TestSourceRecoveryConfirmsInterruptedSourceAndPinsDownstream(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(map[bool]string{false: "prepared", true: "committed"}[committed], func(t *testing.T) {
			f := newSourceFixture(t, M{"id": "code", "branch": "feature"}, M{"id": "build", "kind": "build", "dependsOn": []any{"code"}})
			run, op, original, body := interruptedSource(t, f, committed)
			expireSourceRun(t, f, run)
			// A later valid descendant must be retained, while the recovered result
			// remains pinned to the original candidate, not this branch tip.
			next := f.commit(t, "feature", S(op.Data, "commit"))
			in := recoveryInput(op)
			in["observedHead"] = next
			decision := must(t, f.s, f.a, "sources.reconcile", in)
			if decision.Kind != "source_recovery" || decision.Status != "confirmed" {
				t.Fatal("missing explicit human decision", decision)
			}
			recovered, _ := f.s.Store.Get(run.ID)
			if recovered.Status != "recovered" || S(recovered.Data, "sourceRecoveryId") != decision.ID || S(recovered.Data, "leaseToken") != "" {
				t.Fatal("run did not retain explicit recovered state", recovered)
			}
			if _, invented := recovered.Data["exitCode"]; invented {
				t.Fatal("manual recovery fabricated process success")
			}
			if S(recovered.Data, "completionDigest") != "" || S(recovered.Data, "workerId") != f.worker.WorkerID {
				t.Fatal("manual recovery impersonated worker")
			}
			confirmed, _ := f.s.Store.Get(op.ID)
			change, _ := f.s.Store.Get(S(op.Data, "changeId"))
			if confirmed.Status != "committed" || change.Status != "committed" || S(change.Data, "authorId") != f.worker.ID {
				t.Fatal("source confirmation lost original producer")
			}
			head, _ := f.s.Git(f.repo, "rev-parse", "refs/heads/feature")
			if head != next {
				t.Fatal("recovery rewrote the branch")
			}
			if _, e := f.s.CommitSnapshot(context.Background(), f.worker, f.repo.ID, original, bytes.NewReader(body)); e == nil {
				t.Fatal("old lease submitted after human recovery")
			}
			if _, e := f.s.Command(context.Background(), f.worker, "tasks.complete", M{"id": run.ID, "leaseToken": S(original, "leaseToken"), "exitCode": 0, "result": recovered.Data["result"]}); e == nil {
				t.Fatal("old worker completed a recovered run")
			}
			build := f.claim(t)
			assertSource(t, build, f.repo.ID, S(op.Data, "commit"), recovered)
			evidence, _ := f.s.Store.List(f.p.ID, "evidence", f.g.ID)
			if len(evidence) != 0 {
				t.Fatal("source acceptance invented quality evidence")
			}
			again := must(t, f.s, f.a, "sources.reconcile", in)
			if again.ID != decision.ID {
				t.Fatal("replay duplicated recovery")
			}
			bad := copyM(in)
			bad["reason"] = "changed reason"
			if _, e := f.s.Command(context.Background(), f.a, "sources.reconcile", bad); e == nil {
				t.Fatal("idempotency key accepted changed decision")
			}
		})
	}
}

func TestSourceRecoveryBlocksRetryWithoutConsumingAnotherAttempt(t *testing.T) {
	f := newSourceFixture(t, M{"id": "code", "branch": "feature"})
	run, op, _, _ := interruptedSource(t, f, false)
	expireSourceRun(t, f, run)
	if next := f.claim(t); next.Kind == "run" {
		t.Fatal("unresolved candidate ran again")
	}
	task := f.task(t, "code")
	if task.Status != "blocked" || !Has(Strings(task.Data["sourceRecoveryIds"]), op.ID) || N(task.Data, "attempts") != 1 {
		t.Fatal("interrupted source not visibly blocked", task)
	}
	if _, e := f.s.Command(context.Background(), f.a, "tasks.retry", M{"id": task.ID}); e == nil {
		t.Fatal("generic retry bypassed source recovery")
	}
	decision := must(t, f.s, f.a, "sources.reconcile", recoveryInput(op))
	if S(decision.Data, "runId") != run.ID {
		t.Fatal("decision lost run provenance")
	}
}

func TestSourceRecoveryRejectsActiveLeasesAndUnauthorizedDecisions(t *testing.T) {
	f := newSourceFixture(t, M{"id": "code", "branch": "feature"})
	run, op, _, _ := interruptedSource(t, f, false)
	in := recoveryInput(op)
	if _, e := f.s.Command(context.Background(), f.a, "sources.reconcile", in); e == nil {
		t.Fatal("human replaced an active source lease")
	}
	expireSourceRun(t, f, run)
	for _, actor := range []Actor{f.worker, {ID: "developer", Kind: "human", ProjectID: f.p.ID, Roles: []string{"developer"}}, {ID: "viewer", Kind: "human", ProjectID: f.p.ID, Roles: []string{"viewer"}}, {ID: "other-owner", Kind: "human", ProjectID: "foreign-project", Roles: []string{"owner"}}} {
		if _, e := f.s.Command(context.Background(), actor, "sources.reconcile", in); e == nil {
			t.Fatal("unauthorized recovery", actor)
		}
	}
	for _, mutation := range []M{{"commit": strings.Repeat("a", 40)}, {"observedHead": strings.Repeat("a", 40)}, {"reason": ""}, {"decision": "retry"}, {"leaseToken": "forged"}, {"key": ""}, {"observedHead": 42}} {
		bad := copyM(in)
		for key, value := range mutation {
			bad[key] = value
		}
		if _, e := f.s.Command(context.Background(), f.a, "sources.reconcile", bad); e == nil {
			t.Fatal("invalid recovery accepted", mutation)
		}
	}
	worker, _ := f.s.Store.Get(f.worker.WorkerID)
	worker.Status = "disabled"
	f.put(t, worker)
	must(t, f.s, f.a, "sources.reconcile", in)
	worker, _ = f.s.Store.Get(worker.ID)
	if worker.Status != "disabled" {
		t.Fatal("recovery re-enabled untrusted execution node")
	}
}

func TestSourceRecoveryAbandonsWithoutChangingGitOrRevivingStaleWork(t *testing.T) {
	f := newSourceFixture(t, M{"id": "code", "branch": "feature"})
	run, op, _, _ := interruptedSource(t, f, false)
	expireSourceRun(t, f, run)
	must(t, f.s, f.a, "goals.revise", M{"id": f.g.ID, "reason": "new approved scope needed", "outcome": "changed outcome"})
	if _, e := f.s.Command(context.Background(), f.a, "sources.reconcile", recoveryInput(op)); e == nil {
		t.Fatal("obsolete requirement was confirmed")
	}
	in := recoveryInput(op)
	in["decision"] = "abandon"
	decision := must(t, f.s, f.a, "sources.reconcile", in)
	if decision.Status != "abandoned" {
		t.Fatal("candidate was not closed explicitly")
	}
	task := f.task(t, "code")
	if task.Status != "cancelled" {
		t.Fatal("abandonment revived obsolete task", task)
	}
	head, _ := f.s.Git(f.repo, "rev-parse", "refs/heads/feature")
	retained, _ := f.s.Git(f.repo, "rev-parse", "refs/intelligence/source/"+op.ID)
	if head != S(op.Data, "commit") || retained != head {
		t.Fatal("abandonment discarded source or changed branch")
	}
}

func TestSourceRecoveryConcurrentDecisionsAreAtomic(t *testing.T) {
	f := newSourceFixture(t, M{"id": "code", "branch": "feature"})
	run, op, _, _ := interruptedSource(t, f, false)
	expireSourceRun(t, f, run)
	if _, e := f.s.Store.DB.Exec(`CREATE TRIGGER fail_recovery_decision BEFORE INSERT ON resources WHEN NEW.kind='source_recovery' BEGIN SELECT RAISE(ABORT,'injected decision failure'); END`); e != nil {
		t.Fatal(e)
	}
	if _, e := f.s.Command(context.Background(), f.a, "sources.reconcile", recoveryInput(op)); e == nil {
		t.Fatal("decision SQL fault did not fire")
	}
	still, _ := f.s.Store.Get(op.ID)
	if still.Status != "prepared" || f.task(t, "code").Status != "running" {
		t.Fatal("failed transaction partially confirmed source")
	}
	if _, e := f.s.Store.DB.Exec(`DROP TRIGGER fail_recovery_decision`); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	failures := make(chan error, 2)
	for _, key := range []string{"decision-a", "decision-b"} {
		in := recoveryInput(op)
		in["key"] = key
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := f.s.Command(context.Background(), f.a, "sources.reconcile", in)
			failures <- e
		}()
	}
	wg.Wait()
	close(failures)
	success := 0
	for e := range failures {
		if e == nil {
			success++
		}
	}
	decisions, _ := f.s.Store.List(f.p.ID, "source_recovery", f.g.ID)
	if success != 1 || len(decisions) != 1 {
		t.Fatal("competing decisions did not select exactly one", success, len(decisions))
	}
}

func TestSourceRecoveryInspectsLiveStateWithoutLeakingLease(t *testing.T) {
	f := newSourceFixture(t, M{"id": "code", "branch": "feature"})
	run, op, _, _ := interruptedSource(t, f, false)
	view, e := f.s.InspectSourceOperation(context.Background(), f.a, op.ID)
	if e != nil || B(view, "canConfirm") || B(view, "canAbandon") || S(view, "observedHead") != S(op.Data, "commit") {
		t.Fatal("active source inspection was misleading", view, e)
	}
	if strings.Contains(string(mustJSON(t, view)), S(run.Data, "leaseToken")) {
		t.Fatal("inspection exposed old lease")
	}
	expireSourceRun(t, f, run)
	view, e = f.s.InspectSourceOperation(context.Background(), f.a, op.ID)
	if e != nil || !B(view, "canConfirm") || !B(view, "canAbandon") {
		t.Fatal("expired source not inspectable", view, e)
	}
	viewer := Actor{ID: "viewer", Kind: "human", ProjectID: f.p.ID, Roles: []string{"viewer"}}
	view, e = f.s.InspectSourceOperation(context.Background(), viewer, op.ID)
	if e != nil || B(view, "canConfirm") || B(view, "canAbandon") {
		t.Fatal("read-only member received decision capability", view, e)
	}
	if _, e = f.s.InspectSourceOperation(context.Background(), Actor{ID: "outsider", Kind: "human"}, op.ID); e == nil {
		t.Fatal("foreign candidate was exposed")
	}
}

func TestSourceRecoveryRejectsUnlandedOrChangedCandidates(t *testing.T) {
	for _, mutation := range []string{"unrelated-head", "deleted-branch", "changed-private-ref", "changed-binding", "other-live-run", "missing-worker", "superseded-plan"} {
		t.Run(mutation, func(t *testing.T) {
			f := newSourceFixture(t, M{"id": "code", "branch": "feature"})
			run, op, _, _ := interruptedSource(t, f, false)
			expireSourceRun(t, f, run)
			in := recoveryInput(op)
			switch mutation {
			case "unrelated-head":
				if _, e := f.s.Git(f.repo, "update-ref", "refs/heads/feature", S(f.repo.Data, "head")); e != nil {
					t.Fatal(e)
				}
				in["observedHead"] = S(f.repo.Data, "head")
			case "deleted-branch":
				if _, e := f.s.Git(f.repo, "update-ref", "-d", "refs/heads/feature"); e != nil {
					t.Fatal(e)
				}
				in["observedHead"] = ""
			case "changed-private-ref":
				if _, e := f.s.Git(f.repo, "update-ref", "refs/intelligence/source/"+op.ID, S(f.repo.Data, "head")); e != nil {
					t.Fatal(e)
				}
			case "changed-binding":
				task := f.task(t, "code")
				task.Data["commit"] = strings.Repeat("a", 40)
				f.put(t, task)
			case "other-live-run":
				other := run
				other.ID = NewID("run")
				other.Data = copyM(run.Data)
				other.Data["leaseUntil"] = "2999-01-01T00:00:00Z"
				f.put(t, other)
			case "missing-worker":
				if _, e := f.s.Store.DB.Exec(`DELETE FROM resources WHERE id=$1`, f.worker.WorkerID); e != nil {
					t.Fatal(e)
				}
			case "superseded-plan":
				plan := must(t, f.s, f.a, "plans.propose", M{"goalId": f.g.ID, "summary": "Changed plan", "tasks": []any{M{"id": "new", "title": "New scope", "kind": "analysis", "role": "analysis", "runtime": "linux"}}})
				must(t, f.s, f.a, "plans.approve", M{"id": plan.ID})
			}
			if _, e := f.s.Command(context.Background(), f.a, "sources.reconcile", in); e == nil {
				t.Fatal("invalid source was confirmed", mutation)
			}
			candidate, _ := f.s.Store.Get(op.ID)
			if candidate.Status != "prepared" || S(candidate.Data, "sourceRecoveryId") != "" {
				t.Fatal("rejection mutated original candidate")
			}
		})
	}
}

func TestSourceRecoveryPreservesFailedProcessAndBlocksTimeoutRetry(t *testing.T) {
	for _, mode := range []string{"failed", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			f := newSourceFixture(t, M{"id": "code", "branch": "feature"})
			run, op, _, _ := interruptedSource(t, f, true)
			input := M{"id": run.ID, "leaseToken": S(run.Data, "leaseToken"), "exitCode": 17, "result": M{"reason": "runner interrupted"}, "log": "original process did not complete"}
			if mode == "failed" {
				must(t, f.s, f.worker, "tasks.complete", input)
			} else {
				run.Data["deadline"] = "2000-01-01T00:00:00Z"
				f.put(t, run)
				must(t, f.s, f.worker, "tasks.progress", M{"id": run.ID, "leaseToken": S(run.Data, "leaseToken"), "message": "timeout"})
			}
			if task := f.task(t, "code"); task.Status != "blocked" || !Has(Strings(task.Data["sourceRecoveryIds"]), op.ID) {
				t.Fatal("source-bearing failed run was not blocked", task)
			}
			decision := must(t, f.s, f.a, "sources.reconcile", recoveryInput(op))
			recovered, _ := f.s.Store.Get(run.ID)
			if mode == "failed" {
				if N(recovered.Data, "exitCode") != 17 || S(recovered.Data, "log") != "original process did not complete" || N(Obj(decision.Data["priorRunState"]), "exitCode") != 17 {
					t.Fatal("manual confirmation erased actual process failure")
				}
				if _, e := f.s.Command(context.Background(), f.worker, "tasks.complete", input); e == nil {
					t.Fatal("old completion replay replaced recovered state")
				}
			}
		})
	}
}

func TestSourceRecoveryIntegrationPreservesDecisionsAndRejectsForgedProvenance(t *testing.T) {
	f, _, _ := integrationFixture(t, true)
	run, _ := prepareIntegration(t, f)
	run = must(t, f.s, f.worker, "sources.resolve", M{"runId": run.ID, "leaseToken": S(run.Data, "leaseToken"), "resolutions": []any{M{"path": "shared.txt", "deleted": true}}})
	in := integrationSnapshotInput(run)
	op, e := f.s.CommitSnapshot(context.Background(), f.worker, f.repo.ID, in, bytes.NewReader(sourceTar(t, map[string][]byte{"left.txt": []byte("left\n"), "right.txt": []byte("right\n")}, "")))
	if e != nil {
		t.Fatal(e)
	}
	expireSourceRun(t, f, run)
	decision := must(t, f.s, f.a, "sources.reconcile", recoveryInput(op))
	recovered, _ := f.s.Store.Get(run.ID)
	task := f.task(t, "join")
	if e = f.s.Store.Update(context.Background(), func(tx *Tx) error {
		return f.s.validateTaskResult(tx, f.worker, recovered, task, Obj(recovered.Data["result"]))
	}); e != nil {
		t.Fatal("real recovered integration failed provenance", e)
	}
	decision.Data["commit"] = strings.Repeat("a", 40)
	f.put(t, decision)
	if e = f.s.Store.Update(context.Background(), func(tx *Tx) error {
		return f.s.validateTaskResult(tx, f.worker, recovered, task, Obj(recovered.Data["result"]))
	}); e == nil {
		t.Fatal("forged recovered provenance was accepted")
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, e := json.Marshal(v)
	if e != nil {
		t.Fatal(e)
	}
	return b
}

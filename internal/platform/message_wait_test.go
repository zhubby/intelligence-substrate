package platform

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

func waitRequest(t *testing.T, f *messageFixture) (Resource, M) {
	t.Helper()
	request := must(t, f.s, f.worker, "messages.send", M{"projectId": f.p.ID, "goalId": f.g.ID, "runId": f.run.ID, "leaseToken": S(f.run.Data, "leaseToken"), "recipient": f.a.ID, "type": "request", "key": "wait-request-" + f.run.ID, "payload": M{"question": "Which approved timestamp convention applies?"}})
	return request, M{"id": f.run.ID, "leaseToken": S(f.run.Data, "leaseToken"), "requestMessageId": request.ID, "reason": "Waiting for the interface collaborator", "summary": "Read the approved scope; timestamp convention is still unknown.", "timeoutSeconds": 600, "steps": 3, "source": M{"repositoryId": "", "commit": ""}}
}

func waitReply(t *testing.T, f *messageFixture, request Resource, key string) Resource {
	t.Helper()
	return must(t, f.s, f.a, "messages.send", M{"projectId": f.p.ID, "goalId": f.g.ID, "recipient": f.worker.ID, "recipientTaskId": S(f.run.Data, "taskId"), "type": "result", "key": key, "causationId": request.ID, "payload": M{"answer": "Use UTC as specified in the approved API."}})
}

func TestMessageWaitReleasesLeaseAndResumesOnReplacementWorker(t *testing.T) {
	f := newMessageFixture(t)
	request, input := waitRequest(t, f)
	wait := must(t, f.s, f.worker, "tasks.wait", input)
	old, _ := f.s.Store.Get(f.run.ID)
	task, _ := f.s.Store.Get(S(f.run.Data, "taskId"))
	if wait.Kind != "message_wait" || wait.Status != "waiting" || old.Status != "suspended" || S(old.Data, "leaseToken") != "" || task.Status != "waiting_message" {
		t.Fatal("suspension did not release execution", wait, old.Status, task.Status)
	}
	if next := must(t, f.s, f.worker, "tasks.claim", M{}); next.Kind == "run" {
		t.Fatal("waiting task kept the worker occupied or was reclaimed")
	}
	must(t, f.s, f.a, "workers.disable", M{"id": f.worker.WorkerID})
	reply := waitReply(t, f, request, "human-reply")
	stored, _ := f.s.Store.Get(wait.ID)
	task, _ = f.s.Store.Get(task.ID)
	if stored.Status != "ready" || S(stored.Data, "replyMessageId") != reply.ID || task.Status != "pending" {
		t.Fatal("exact reply did not wake task", stored, task.Status)
	}
	other := f.register(t, "Replacement analyst")
	next := must(t, f.s, other, "tasks.claim", M{})
	if next.Kind != "run" || S(next.Data, "taskId") != task.ID || N(Obj(next.Data["task"]), "attempts") != 1 || N(Obj(next.Data["task"]), "messageStepsUsed") != 3 {
		t.Fatal("continuation used failure retry or lost step bound", next)
	}
	resume := Obj(next.Data["messageContinuation"])
	if S(resume, "waitId") != wait.ID || S(resume, "summary") != S(input, "summary") || S(resume, "requestMessageId") != request.ID {
		t.Fatal("fresh inference lost its exact wait context", resume)
	}
	inbox := must(t, f.s, other, "messages.receive", M{"runId": next.ID, "leaseToken": S(next.Data, "leaseToken")})
	items := Items(inbox.Data["items"])
	if len(items) != 1 || S(Obj(Obj(Obj(items[0])["data"])["message"]), "id") != reply.ID {
		t.Fatal("replacement did not inherit exact durable reply", inbox)
	}
	if _, e := f.s.Command(context.Background(), f.worker, "tasks.complete", M{"id": old.ID, "leaseToken": S(input, "leaseToken"), "exitCode": 0, "result": M{}}); e == nil {
		t.Fatal("suspended lease could still complete")
	}
	b, _ := json.Marshal(stored)
	if strings.Contains(string(b), "leaseToken") || strings.Contains(string(b), S(input, "leaseToken")) {
		t.Fatal("wait exposed execution authority")
	}
}

func TestMessageWaitEarlyReplyAndLostResponseReplay(t *testing.T) {
	f := newMessageFixture(t)
	request, input := waitRequest(t, f)
	reply := waitReply(t, f, request, "early")
	// The original run may already have read the reply before it chooses wait.
	f.receive(t)
	first := must(t, f.s, f.worker, "tasks.wait", input)
	if first.Status != "ready" || S(first.Data, "replyMessageId") != reply.ID {
		t.Fatal("reply-before-wait was lost", first)
	}
	next := must(t, f.s, f.worker, "tasks.claim", M{})
	if next.Kind != "run" {
		t.Fatal("early reply was not runnable")
	}
	replay := must(t, f.s, f.worker, "tasks.wait", input)
	if replay.ID != first.ID || replay.Status != "resumed" {
		t.Fatal("lost-response replay created another suspension", replay)
	}
	changed := copyM(input)
	changed["summary"] = "A different continuation"
	if _, e := f.s.Command(context.Background(), f.worker, "tasks.wait", changed); e == nil {
		t.Fatal("conflicting replay accepted")
	}
	waits, _ := f.s.Store.List(f.p.ID, "message_wait", f.g.ID)
	receipts, _ := f.s.Store.List(f.p.ID, "message_receipt", f.g.ID)
	if len(waits) != 1 || len(receipts) != 1 {
		t.Fatal("idempotency duplicated durable state", len(waits), len(receipts))
	}
}

func TestMessageWaitRejectsUnauthorizedOrUnboundedSuspension(t *testing.T) {
	for _, mode := range []string{"human", "foreign-worker", "wrong-lease", "expired-deadline", "fixed-command", "code", "foreign-request", "progress-request", "self-task", "missing-source", "bad-source", "fraction-timeout", "short-timeout", "long-timeout", "zero-steps", "last-step", "oversized-summary", "unsettled-call", "wait-limit"} {
		t.Run(mode, func(t *testing.T) {
			f := newMessageFixture(t)
			request, in := waitRequest(t, f)
			actor := f.worker
			task, _ := f.s.Store.Get(S(f.run.Data, "taskId"))
			switch mode {
			case "human":
				actor = f.a
			case "foreign-worker":
				actor = f.register(t, "Foreign run owner")
			case "wrong-lease":
				in["leaseToken"] = "wrong"
			case "expired-deadline":
				f.run.Data["deadline"] = time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)
			case "fixed-command":
				task.Data["command"] = []string{"true"}
			case "code":
				task.Data["kind"] = "code"
			case "foreign-request":
				request.Data["senderRunId"] = "another-run"
			case "progress-request":
				request.Data["type"] = "progress"
			case "self-task":
				request.Data["recipientTaskId"] = task.ID
			case "missing-source":
				delete(in, "source")
			case "bad-source":
				in["source"] = M{"repositoryId": "missing", "commit": strings.Repeat("a", 40)}
			case "fraction-timeout":
				in["timeoutSeconds"] = 61.5
			case "short-timeout":
				in["timeoutSeconds"] = 59
			case "long-timeout":
				in["timeoutSeconds"] = 86401
			case "zero-steps":
				in["steps"] = 0
			case "last-step":
				in["steps"] = 24
			case "oversized-summary":
				in["summary"] = strings.Repeat("x", 8001)
			case "unsettled-call":
				charge := resource("charge", "Running provider request", f.p.ID, f.g.ID, M{"runId": f.run.ID, "reservedCents": 1})
				charge.Status = "reserved"
				if e := f.s.Store.Update(context.Background(), func(tx *Tx) error { return tx.Put(charge) }); e != nil {
					t.Fatal(e)
				}
			case "wait-limit":
				task.Data["messageWaitCount"] = 3
			}
			if e := f.s.Store.Update(context.Background(), func(tx *Tx) error {
				for _, item := range []Resource{request, task, f.run} {
					if e := tx.Put(item); e != nil {
						return e
					}
				}
				return nil
			}); e != nil {
				t.Fatal(e)
			}
			if _, e := f.s.Command(context.Background(), actor, "tasks.wait", in); e == nil {
				t.Fatal("invalid suspension accepted", mode)
			}
			waits, _ := f.s.Store.List(f.p.ID, "message_wait", f.g.ID)
			if len(waits) != 0 {
				t.Fatal("rejected suspension persisted a wait")
			}
		})
	}
}

func TestMessageWaitWrongReplyCannotWake(t *testing.T) {
	for _, mode := range []string{"progress", "cancel", "request", "wrong-cause", "wrong-sender", "wrong-task", "wrong-recipient"} {
		t.Run(mode, func(t *testing.T) {
			f := newMessageFixture(t)
			request, input := waitRequest(t, f)
			wait := must(t, f.s, f.worker, "tasks.wait", input)
			in := M{"projectId": f.p.ID, "goalId": f.g.ID, "recipient": f.worker.ID, "recipientTaskId": S(f.run.Data, "taskId"), "type": "result", "key": "wrong", "causationId": request.ID, "payload": M{"answer": "Untrusted"}}
			actor := f.a
			switch mode {
			case "progress", "cancel", "request":
				in["type"] = mode
			case "wrong-cause":
				delete(in, "causationId")
			case "wrong-sender":
				actor = Actor{ID: "other-person", Kind: "human", Admin: true}
			case "wrong-task":
				in["recipientTaskId"] = "another-task"
			case "wrong-recipient":
				in["recipient"] = f.a.ID
			}
			_, _ = f.s.Command(context.Background(), actor, "messages.send", in)
			stored, _ := f.s.Store.Get(wait.ID)
			task, _ := f.s.Store.Get(S(f.run.Data, "taskId"))
			if stored.Status != "waiting" || task.Status != "waiting_message" {
				t.Fatal("unrelated message woke task", mode)
			}
		})
	}
}

func TestMessageWaitInvalidationAndLateReply(t *testing.T) {
	for _, mode := range []string{"timeout", "cancel", "requirement", "plan", "recipe"} {
		t.Run(mode, func(t *testing.T) {
			f := newMessageFixture(t)
			request, input := waitRequest(t, f)
			wait := must(t, f.s, f.worker, "tasks.wait", input)
			switch mode {
			case "timeout":
				wait.Data["expiresAt"] = time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)
				if e := f.s.Store.Update(context.Background(), func(tx *Tx) error { return tx.Put(wait) }); e != nil {
					t.Fatal(e)
				}
			case "cancel":
				must(t, f.s, f.a, "tasks.cancel", M{"id": S(f.run.Data, "taskId")})
			case "requirement":
				must(t, f.s, f.a, "goals.revise", M{"id": f.g.ID, "reason": "Changed scope", "outcome": "A new scope"})
			case "plan":
				plan := must(t, f.s, f.a, "plans.propose", M{"goalId": f.g.ID, "summary": "Replacement", "tasks": []any{M{"id": "new", "title": "New analysis", "kind": "analysis", "role": "developer", "runtime": "linux"}}})
				must(t, f.s, f.a, "plans.approve", M{"id": plan.ID})
			case "recipe":
				task, _ := f.s.Store.Get(S(f.run.Data, "taskId"))
				task.Data["instructions"] = "Different authority"
				if e := f.s.Store.Update(context.Background(), func(tx *Tx) error { return tx.Put(task) }); e != nil {
					t.Fatal(e)
				}
			}
			// A normal worker poll also performs durable wait maintenance.
			must(t, f.s, f.worker, "tasks.claim", M{})
			_, _ = f.s.Command(context.Background(), f.a, "messages.send", M{"projectId": f.p.ID, "goalId": f.g.ID, "recipient": f.worker.ID, "recipientTaskId": S(f.run.Data, "taskId"), "type": "result", "key": "late", "causationId": request.ID, "payload": M{"answer": "Too late"}})
			stored, _ := f.s.Store.Get(wait.ID)
			if Has([]string{"waiting", "ready", "resumed"}, stored.Status) {
				t.Fatal("invalid wait revived", mode, stored)
			}
		})
	}
}

func TestMessageWaitReplyTransactionRollbackAndConcurrentReplay(t *testing.T) {
	f := newMessageFixture(t)
	request, input := waitRequest(t, f)
	wait := must(t, f.s, f.worker, "tasks.wait", input)
	if _, e := f.s.Store.DB.Exec(`CREATE TRIGGER reject_wait_receipt BEFORE INSERT ON resources WHEN NEW.kind='message_receipt' BEGIN SELECT RAISE(ABORT,'injected wait failure'); END`); e != nil {
		t.Fatal(e)
	}
	in := M{"projectId": f.p.ID, "goalId": f.g.ID, "recipient": f.worker.ID, "recipientTaskId": S(f.run.Data, "taskId"), "type": "result", "key": "concurrent", "causationId": request.ID, "payload": M{"answer": "UTC"}}
	if _, e := f.s.Command(context.Background(), f.a, "messages.send", in); e == nil {
		t.Fatal("receipt failure was ignored")
	}
	stored, _ := f.s.Store.Get(wait.ID)
	if stored.Status != "waiting" {
		t.Fatal("partial wake survived rollback")
	}
	if _, e := f.s.Store.DB.Exec(`DROP TRIGGER reject_wait_receipt`); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := f.s.Command(context.Background(), f.a, "messages.send", in)
			errs <- e
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	receipts, _ := f.s.Store.List(f.p.ID, "message_receipt", f.g.ID)
	if len(receipts) != 1 {
		t.Fatal("concurrent replies duplicated receipt", len(receipts))
	}
}

func TestMessageWaitContinuationKeepsOriginalBudgetTimeAndStepLimits(t *testing.T) {
	for _, mode := range []string{"remaining", "spent", "time", "reserved", "recipe-after-reply"} {
		t.Run(mode, func(t *testing.T) {
			f := newMessageFixture(t)
			request, input := waitRequest(t, f)
			wait := must(t, f.s, f.worker, "tasks.wait", input)
			run, _ := f.s.Store.Get(f.run.ID)
			start := time.Now().Add(-20 * time.Second)
			run.CreatedAt = start.UTC().Format(time.RFC3339Nano)
			run.Data["suspendedAt"] = start.Add(10 * time.Second).UTC().Format(time.RFC3339Nano)
			run.Data["spentCents"] = 40
			if mode == "spent" {
				run.Data["spentCents"] = 100
			}
			if mode == "time" {
				run.CreatedAt = time.Now().Add(-1000 * time.Second).UTC().Format(time.RFC3339Nano)
			}
			if e := f.s.Store.Update(context.Background(), func(tx *Tx) error { return tx.Put(run) }); e != nil {
				t.Fatal(e)
			}
			waitReply(t, f, request, "resume-with-bounds")
			if mode == "reserved" {
				charge := resource("charge", "Unsettled provider", f.p.ID, f.g.ID, M{"runId": run.ID, "reservedCents": 1})
				charge.Status = "reserved"
				if e := f.s.Store.Update(context.Background(), func(tx *Tx) error { return tx.Put(charge) }); e != nil {
					t.Fatal(e)
				}
			}
			if mode == "recipe-after-reply" {
				task, _ := f.s.Store.Get(S(run.Data, "taskId"))
				task.Data["budgetCents"] = 200
				if e := f.s.Store.Update(context.Background(), func(tx *Tx) error { return tx.Put(task) }); e != nil {
					t.Fatal(e)
				}
			}
			next := must(t, f.s, f.worker, "tasks.claim", M{})
			if mode != "remaining" {
				if next.Kind == "run" {
					t.Fatal("continuation bypassed original limit", mode, next)
				}
				return
			}
			if next.Kind != "run" || N(next.Data, "reservedCents") != 60 {
				t.Fatal("task budget reset by suspension", next)
			}
			deadline, e := time.Parse(time.RFC3339Nano, S(next.Data, "deadline"))
			if e != nil || time.Until(deadline) > 891*time.Second || time.Until(deadline) < 888*time.Second {
				t.Fatal("active time reset or wait time counted as execution", e, time.Until(deadline))
			}
			if S(Obj(next.Data["messageContinuation"]), "waitId") != wait.ID {
				t.Fatal("missing continuation")
			}
		})
	}
}

func TestMessageWaitCountCannotBeResetByContinuationOrPlanInput(t *testing.T) {
	f := newMessageFixture(t)
	for i := 0; i < 3; i++ {
		request, input := waitRequest(t, f)
		input["steps"] = 3 + i
		must(t, f.s, f.worker, "tasks.wait", input)
		waitReply(t, f, request, NewID("reply"))
		f.run = must(t, f.s, f.worker, "tasks.claim", M{})
		if f.run.Kind != "run" {
			t.Fatal("legal continuation not claimed", i)
		}
	}
	_, input := waitRequest(t, f)
	input["steps"] = 6
	if _, e := f.s.Command(context.Background(), f.worker, "tasks.wait", input); e == nil {
		t.Fatal("fourth wait allowed")
	}
	for _, key := range []string{"messageWaitId", "messageWaitCount", "messageStepsUsed", "messageContinuation"} {
		_, e := validateTasks([]any{M{"id": "forged", "title": "Forged task", "role": "analysis", "runtime": "linux", "kind": "analysis", key: 0}})
		if e == nil {
			t.Fatal("plan accepted protected continuation field", key)
		}
	}
}

func TestMessageWaitCapacityPreservesReplyWithoutWaking(t *testing.T) {
	f := newMessageFixture(t)
	for i := 0; i < 32; i++ {
		f.send(t, NewID("preexisting"), f.worker.ID, M{"text": "Existing bounded task context"})
	}
	f.receive(t)
	request, input := waitRequest(t, f)
	wait := must(t, f.s, f.worker, "tasks.wait", input)
	reply := waitReply(t, f, request, "cannot-fit")
	stored, _ := f.s.Store.Get(wait.ID)
	task, _ := f.s.Store.Get(S(f.run.Data, "taskId"))
	message, _ := f.s.Store.Get(reply.ID)
	if stored.Status != "blocked" || task.Status != "blocked" || message.Status != "pending" {
		t.Fatal("capacity loss or unbounded wake", stored.Status, task.Status, message.Status)
	}
	receipts, _ := f.s.Store.List(f.p.ID, "message_receipt", f.g.ID)
	if len(receipts) != 32 {
		t.Fatal("capacity exceeded", len(receipts))
	}
}

func TestMessageWaitFrozenReadSourceSurvivesRepositoryChanges(t *testing.T) {
	f := newMessageFixture(t)
	repo := must(t, f.s, f.a, "repositories.create", M{"projectId": f.p.ID, "title": "Analysis source"})
	repo = must(t, f.s, f.a, "repositories.commit", M{"id": repo.ID, "branch": "feature", "message": "Original context", "files": M{"contract.txt": "UTC"}})
	commit := S(repo.Data, "lastCommit")
	request, input := waitRequest(t, f)
	input["source"] = M{"repositoryId": repo.ID, "commit": commit}
	must(t, f.s, f.worker, "tasks.wait", input)
	must(t, f.s, f.a, "repositories.commit", M{"id": repo.ID, "branch": "feature", "expectedHead": commit, "message": "Later source", "files": M{"contract.txt": "Unrelated newer context"}})
	waitReply(t, f, request, "source-pinned")
	next := must(t, f.s, f.worker, "tasks.claim", M{})
	if S(Obj(Obj(next.Data["messageContinuation"])["source"]), "commit") != commit {
		t.Fatal("continuation borrowed current branch head")
	}
}

func TestMessageWaitExhaustedMessageDeliveryCannotWake(t *testing.T) {
	f := newMessageFixture(t)
	request, input := waitRequest(t, f)
	reply := waitReply(t, f, request, "exhausted-delivery")
	reply.Data["attempts"] = 3
	if e := f.s.Store.Update(context.Background(), func(tx *Tx) error { return tx.Put(reply) }); e != nil {
		t.Fatal(e)
	}
	wait := must(t, f.s, f.worker, "tasks.wait", input)
	if wait.Status != "blocked" {
		t.Fatal("exhausted inbox delivery bypassed retry limit", wait.Status)
	}
	stored, _ := f.s.Store.Get(reply.ID)
	if stored.Status != "pending" {
		t.Fatal("exhausted reply was silently acknowledged", stored.Status)
	}
}

func TestMessageWaitDeclaredSourceCannotBeDiscarded(t *testing.T) {
	f := newMessageFixture(t)
	repo := must(t, f.s, f.a, "repositories.create", M{"projectId": f.p.ID, "title": "Declared analysis source"})
	commit, e := f.s.Git(repo, "rev-parse", "refs/heads/main")
	if e != nil {
		t.Fatal(e)
	}
	task, _ := f.s.Store.Get(S(f.run.Data, "taskId"))
	task.Data["repositoryId"], task.Data["commit"] = repo.ID, commit
	f.run.Data["task"] = copyM(task.Data)
	if e = f.s.Store.Update(context.Background(), func(tx *Tx) error {
		if e := tx.Put(task); e != nil {
			return e
		}
		return tx.Put(f.run)
	}); e != nil {
		t.Fatal(e)
	}
	_, input := waitRequest(t, f)
	if _, e = f.s.Command(context.Background(), f.worker, "tasks.wait", input); e == nil {
		t.Fatal("wait discarded the source declared in approved work")
	}
}

func TestMessageWaitWorkerReplyMustComeFromRequestedTask(t *testing.T) {
	s, a, project := fixture(t)
	f := newMessageFixtureOn(t, s, a, project, M{"id": "one", "role": "developer"}, M{"id": "two", "role": "developer"}, M{"id": "expected", "role": "architect"})
	peer := f.register(t, "Busy collaborator")
	peerRun := must(t, s, peer, "tasks.claim", M{})
	tasks, _ := s.Store.List(project.ID, "task", f.g.ID)
	target := ""
	for _, task := range tasks {
		if S(task.Data, "logicalId") == "expected" {
			target = task.ID
		}
	}
	request := must(t, s, f.worker, "messages.send", M{"projectId": project.ID, "goalId": f.g.ID, "runId": f.run.ID, "leaseToken": S(f.run.Data, "leaseToken"), "recipient": peer.ID, "recipientTaskId": target, "type": "review", "key": "expected-peer-task", "payload": M{"question": "Review the approved convention"}})
	_, input := waitRequest(t, f)
	input["requestMessageId"] = request.ID
	wait := must(t, s, f.worker, "tasks.wait", input)
	must(t, s, peer, "messages.send", M{"projectId": project.ID, "goalId": f.g.ID, "runId": peerRun.ID, "leaseToken": S(peerRun.Data, "leaseToken"), "recipient": f.worker.ID, "recipientTaskId": S(f.run.Data, "taskId"), "type": "result", "key": "wrong-peer-task", "causationId": request.ID, "payload": M{"answer": "Same identity, different work"}})
	stored, _ := s.Store.Get(wait.ID)
	if stored.Status != "waiting" {
		t.Fatal("another run from the same Worker woke the wait")
	}
}

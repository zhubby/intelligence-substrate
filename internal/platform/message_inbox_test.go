package platform

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

type messageFixture struct {
	s               *Service
	a, worker       Actor
	p, g, plan, run Resource
}

func newMessageFixture(t *testing.T) *messageFixture {
	t.Helper()
	s, a, project := fixture(t)
	return newMessageFixtureOn(t, s, a, project)
}

func newMessageFixtureOn(t *testing.T, s *Service, a Actor, project Resource, recipes ...M) *messageFixture {
	t.Helper()
	f := &messageFixture{s: s, a: a, p: project}
	f.g = goal(t, s, a, project)
	must(t, s, a, "goals.approve", M{"id": f.g.ID})
	if len(recipes) == 0 {
		recipes = []M{{"id": "analysis"}}
	}
	tasks := []any{}
	for _, recipe := range recipes {
		data := M{"title": "Clarify interfaces", "kind": "analysis", "role": "developer", "runtime": "linux", "budgetCents": 100}
		for key, value := range recipe {
			data[key] = value
		}
		tasks = append(tasks, data)
	}
	f.plan = must(t, s, a, "plans.propose", M{"goalId": f.g.ID, "summary": "Discuss the approved requirements", "tasks": tasks})
	must(t, s, a, "plans.approve", M{"id": f.plan.ID})
	f.worker = f.register(t, "Primary collaborator")
	f.run = must(t, s, f.worker, "tasks.claim", M{})
	if f.run.Kind != "run" {
		t.Fatal("analysis was not claimed")
	}
	return f
}

func (f *messageFixture) register(t *testing.T, title string) Actor {
	t.Helper()
	w := must(t, f.s, f.a, "workers.register", M{"projectId": f.p.ID, "title": title, "roles": []any{"developer"}, "runtime": "linux", "capabilities": []any{"analysis"}})
	return Actor{ID: "worker:" + w.ID, Kind: "worker", WorkerID: w.ID, ProjectID: f.p.ID, Roles: []string{"developer"}}
}

func (f *messageFixture) send(t *testing.T, key, recipient string, payload M) Resource {
	t.Helper()
	return must(t, f.s, f.a, "messages.send", M{"projectId": f.p.ID, "goalId": f.g.ID, "recipient": recipient, "type": "clarification", "key": key, "payload": payload})
}

func (f *messageFixture) receive(t *testing.T) Resource {
	t.Helper()
	return must(t, f.s, f.worker, "messages.receive", M{"runId": f.run.ID, "leaseToken": S(f.run.Data, "leaseToken")})
}

func TestMessageInboxPersistsDeliveryAndReplaysOnlyItsTask(t *testing.T) {
	f := newMessageFixture(t)
	message := f.send(t, "question", f.worker.ID, M{"question": "Use UTC for all timestamps?"})
	inbox := f.receive(t)
	items := Items(inbox.Data["items"])
	if inbox.Kind != "message-inbox" || len(items) != 1 || B(inbox.Data, "deferred") {
		t.Fatalf("missing inbox %+v", inbox)
	}
	receipt := Obj(items[0])
	data := Obj(receipt["data"])
	snapshot := Obj(data["message"])
	if S(data, "taskId") != S(f.run.Data, "taskId") || S(data, "firstRunId") != f.run.ID || S(data, "planId") != f.plan.ID || N(data, "requirementRevision") != 1 || S(snapshot, "id") != message.ID || S(Obj(snapshot["payload"]), "question") != "Use UTC for all timestamps?" {
		t.Fatal("wrong task/producer/content", receipt)
	}
	b, _ := json.Marshal(inbox)
	if strings.Contains(string(b), "leaseToken") || strings.Contains(string(b), S(f.run.Data, "leaseToken")) {
		t.Fatal("delivery exposed lease credentials")
	}
	stored, _ := f.s.Store.Get(message.ID)
	if stored.Status != "delivered" {
		t.Fatal("delivery did not commit with receipt")
	}
	audits, _ := f.s.Store.Audits(f.p.ID)
	count := len(audits)
	if len(Items(f.receive(t).Data["items"])) != 1 {
		t.Fatal("inbox history vanished")
	}
	audits, _ = f.s.Store.Audits(f.p.ID)
	if len(audits) != count {
		t.Fatal("unchanged inbox poll appended audit")
	}
	receipts, _ := f.s.Store.List(f.p.ID, "message_receipt", f.g.ID)
	if len(receipts) != 1 {
		t.Fatal("duplicate receipt")
	}
	must(t, f.s, f.worker, "tasks.complete", M{"id": f.run.ID, "leaseToken": S(f.run.Data, "leaseToken"), "exitCode": 1, "result": M{}})
	must(t, f.s, f.a, "tasks.retry", M{"id": S(f.run.Data, "taskId")})
	f.worker = f.register(t, "Replacement collaborator")
	f.run = must(t, f.s, f.worker, "tasks.claim", M{})
	history := Items(f.receive(t).Data["items"])
	if len(history) != 1 || S(Obj(history[0]), "id") != S(receipt, "id") {
		t.Fatal("replacement execution lost durable context", history)
	}
}

func TestMessageInboxRejectsUnboundWorkerOperations(t *testing.T) {
	for _, mode := range []string{"missing-lease", "wrong-lease", "expired", "disabled", "cancelled", "wrong-goal", "wrong-plan", "human-receive", "legacy-claim", "legacy-ack"} {
		t.Run(mode, func(t *testing.T) {
			f := newMessageFixture(t)
			message := f.send(t, "incoming", f.worker.ID, M{"question": "Approved scope?"})
			input := M{"runId": f.run.ID, "leaseToken": S(f.run.Data, "leaseToken"), "projectId": f.p.ID, "goalId": f.g.ID, "recipient": f.a.ID, "type": "result", "key": "reply", "payload": M{"answer": "yes"}}
			actor := f.worker
			if mode == "missing-lease" {
				delete(input, "leaseToken")
			}
			if mode == "wrong-lease" {
				input["leaseToken"] = "foreign"
			}
			if mode == "expired" {
				f.run.Data["leaseUntil"] = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
			}
			if mode == "cancelled" {
				f.run.Status = "cancelled"
			}
			if mode == "wrong-goal" {
				input["goalId"] = goal(t, f.s, f.a, f.p).ID
			}
			if mode == "wrong-plan" {
				f.run.Data["planId"] = "unapproved-plan"
			}
			if mode == "disabled" {
				must(t, f.s, f.a, "workers.disable", M{"id": f.worker.WorkerID})
			}
			if e := f.s.Store.Update(context.Background(), func(tx *Tx) error { return tx.Put(f.run) }); e != nil {
				t.Fatal(e)
			}
			command := "messages.send"
			if mode == "human-receive" {
				actor = f.a
				command = "messages.receive"
			}
			if mode == "legacy-claim" {
				command = "messages.claim"
			}
			if mode == "legacy-ack" {
				command = "messages.ack"
				input["id"] = message.ID
			}
			if _, e := f.s.Command(context.Background(), actor, command, input); e == nil {
				t.Fatal("unbound message operation succeeded", mode)
			}
		})
	}
}

func TestMessageSendValidatesRecipientCausalityAndPayload(t *testing.T) {
	for _, mode := range []string{"unknown-recipient", "other-project-worker", "disabled-worker", "other-goal-cause", "unknown-cause", "array-payload", "oversized", "nonempty-goal-required"} {
		t.Run(mode, func(t *testing.T) {
			f := newMessageFixture(t)
			in := M{"projectId": f.p.ID, "goalId": f.g.ID, "recipient": f.worker.ID, "type": "request", "key": "invalid", "payload": M{"question": "Interface?"}}
			switch mode {
			case "unknown-recipient":
				in["recipient"] = "unregistered-agent"
			case "other-project-worker":
				project := must(t, f.s, f.a, "projects.create", M{"title": "Another project"})
				w := must(t, f.s, f.a, "workers.register", M{"projectId": project.ID, "title": "Foreign", "runtime": "linux", "roles": []any{"developer"}, "capabilities": []any{"analysis"}})
				in["recipient"] = "worker:" + w.ID
			case "disabled-worker":
				must(t, f.s, f.a, "workers.disable", M{"id": f.worker.WorkerID})
			case "other-goal-cause":
				g := goal(t, f.s, f.a, f.p)
				cause := must(t, f.s, f.a, "messages.send", M{"projectId": f.p.ID, "goalId": g.ID, "recipient": f.a.ID, "type": "request", "key": "other", "payload": M{"q": "Other goal"}})
				in["causationId"] = cause.ID
			case "unknown-cause":
				in["causationId"] = "message_missing"
			case "array-payload":
				in["payload"] = []any{"not an object"}
			case "oversized":
				in["payload"] = M{"text": strings.Repeat("x", 8193)}
			case "nonempty-goal-required":
				delete(in, "goalId")
				in["runId"] = f.run.ID
				in["leaseToken"] = S(f.run.Data, "leaseToken")
			}
			actor := f.a
			if mode == "nonempty-goal-required" {
				actor = f.worker
			}
			if _, e := f.s.Command(context.Background(), actor, "messages.send", in); e == nil {
				t.Fatal("invalid collaboration accepted", mode)
			}
		})
	}
}

func TestMessageRecipientIdentityLookupFailsClosed(t *testing.T) {
	for _, mode := range []string{"no-auth-row", "active", "disabled", "invalid-json", "query-error"} {
		t.Run(mode, func(t *testing.T) {
			s, owner, project := fixture(t)
			registerMembershipIdentity(t, s, "recipient", false, false)
			must(t, s, owner, "members.grant", M{"projectId": project.ID, "identityId": "recipient", "name": "Recipient", "roles": []any{"developer"}})
			if mode == "query-error" {
				if _, e := s.Store.DB.Exec(`ALTER TABLE identities RENAME COLUMN body TO unavailable_body`); e != nil {
					t.Fatal(e)
				}
			} else if mode == "no-auth-row" {
				if _, e := s.Store.DB.Exec(`DELETE FROM identities WHERE id='recipient'`); e != nil {
					t.Fatal(e)
				}
			} else {
				body := `{"disabled":false}`
				if mode == "disabled" {
					body = `{"disabled":true}`
				} else if mode == "invalid-json" {
					body = `{`
				}
				if _, e := s.Store.DB.Exec(`UPDATE identities SET body=$1 WHERE id='recipient'`, body); e != nil {
					t.Fatal(e)
				}
			}
			_, e := s.Command(context.Background(), owner, "messages.send", M{"projectId": project.ID, "recipient": "recipient", "type": "request", "key": "identity-check", "payload": M{"text": "Project collaborator only"}})
			allowed := mode == "no-auth-row" || mode == "active"
			if (e == nil) != allowed {
				t.Fatalf("identity lookup %s: allowed=%t, error=%v", mode, allowed, e)
			}
			messages, listError := s.Store.List(project.ID, "message", "")
			if listError != nil || (!allowed && len(messages) != 0) {
				t.Fatal("denied delivery persisted a message", listError, messages)
			}
		})
	}
}

func TestMessageInboxFiltersGoalRevisionAndOtherTasks(t *testing.T) {
	f := newMessageFixture(t)
	old := f.send(t, "old", f.worker.ID, M{"text": "Old requirement"})
	if e := f.s.Store.Update(context.Background(), func(tx *Tx) error { old.Data["requirementRevision"] = 0; return tx.Put(old) }); e != nil {
		t.Fatal(e)
	}
	g := goal(t, f.s, f.a, f.p)
	foreign := resource("message", "Legacy queued message", f.p.ID, g.ID, M{"recipient": f.worker.ID, "recipientTaskId": S(f.run.Data, "taskId"), "requirementRevision": 1, "payload": M{"text": "Other target"}})
	foreign.Status = "pending"
	if e := f.s.Store.Update(context.Background(), func(tx *Tx) error { return tx.Put(foreign) }); e != nil {
		t.Fatal(e)
	}
	f.send(t, "project-wide", f.worker.ID, M{"text": "Current"})
	inbox := f.receive(t)
	if len(Items(inbox.Data["items"])) != 1 {
		t.Fatal("cross-goal or old revision entered task", inbox)
	}
	old, _ = f.s.Store.Get(old.ID)
	if old.Status != "pending" {
		t.Fatal("old revision was silently acknowledged")
	}
}

func TestMessageInboxConcurrentReceiptAndAtomicRollback(t *testing.T) {
	f := newMessageFixture(t)
	message := f.send(t, "one", f.worker.ID, M{"text": "Exactly one context record"})
	if _, e := f.s.Store.DB.Exec(`CREATE TRIGGER reject_receipt BEFORE INSERT ON resources WHEN NEW.kind='message_receipt' BEGIN SELECT RAISE(ABORT,'injected inbox failure'); END`); e != nil {
		t.Fatal(e)
	}
	if _, e := f.s.Command(context.Background(), f.worker, "messages.receive", M{"runId": f.run.ID, "leaseToken": S(f.run.Data, "leaseToken")}); e == nil {
		t.Fatal("injected failure ignored")
	}
	stored, _ := f.s.Store.Get(message.ID)
	if stored.Status != "pending" {
		t.Fatal("failed transaction acknowledged message")
	}
	if _, e := f.s.Store.DB.Exec(`DROP TRIGGER reject_receipt`); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := f.s.Command(context.Background(), f.worker, "messages.receive", M{"runId": f.run.ID, "leaseToken": S(f.run.Data, "leaseToken")})
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
		t.Fatal("concurrent delivery duplicated context", len(receipts))
	}
}

func TestMessageInboxCapacityDefersWithoutAcknowledging(t *testing.T) {
	for _, mode := range []string{"count", "bytes"} {
		t.Run(mode, func(t *testing.T) {
			f := newMessageFixture(t)
			for i := 0; i < 33; i++ {
				payload := M{"text": "small"}
				if mode == "bytes" {
					payload["text"] = strings.Repeat("x", 7800)
				}
				f.send(t, NewID("message"), f.worker.ID, payload)
			}
			inbox := f.receive(t)
			items := Items(inbox.Data["items"])
			b, _ := json.Marshal(items)
			if !B(inbox.Data, "deferred") || len(items) > 32 || len(items) == 0 || len(b) > 64<<10 {
				t.Fatal("capacity not bounded", len(items), len(b), inbox.Data["deferred"])
			}
			messages, _ := f.s.Store.List(f.p.ID, "message", f.g.ID)
			delivered := 0
			for _, m := range messages {
				if m.Status == "delivered" {
					delivered++
				}
			}
			if delivered != len(items) || delivered == len(messages) {
				t.Fatal("deferred messages were acknowledged")
			}
		})
	}
}

func TestMessageSendExactReplaySurvivesRecipientCompletion(t *testing.T) {
	f := newMessageFixture(t)
	in := M{"projectId": f.p.ID, "goalId": f.g.ID, "recipient": f.worker.ID, "recipientTaskId": S(f.run.Data, "taskId"), "type": "request", "key": "stable", "payload": M{"text": "Preserve the original request"}}
	first := must(t, f.s, f.a, "messages.send", in)
	f.receive(t)
	must(t, f.s, f.worker, "tasks.complete", M{"id": f.run.ID, "leaseToken": S(f.run.Data, "leaseToken"), "exitCode": 0, "result": M{"summary": "Done"}})
	replayed := must(t, f.s, f.a, "messages.send", in)
	if replayed.ID != first.ID || replayed.Status != "delivered" {
		t.Fatal("lost-response retry did not return original message")
	}
}

func TestMessageInboxRequiresParsedLeaseAndUnexpiredDeadline(t *testing.T) {
	for _, mode := range []string{"invalid-lease", "expired-deadline", "invalid-deadline"} {
		t.Run(mode, func(t *testing.T) {
			f := newMessageFixture(t)
			switch mode {
			case "invalid-lease":
				f.run.Data["leaseUntil"] = "zz-invalid"
			case "expired-deadline":
				f.run.Data["deadline"] = time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)
			case "invalid-deadline":
				f.run.Data["deadline"] = "invalid"
			}
			if e := f.s.Store.Update(context.Background(), func(tx *Tx) error { return tx.Put(f.run) }); e != nil {
				t.Fatal(e)
			}
			if _, e := f.s.Command(context.Background(), f.worker, "messages.receive", M{"runId": f.run.ID, "leaseToken": S(f.run.Data, "leaseToken")}); e == nil {
				t.Fatal("invalid execution window accepted", mode)
			}
		})
	}
}

func TestMessageInboxPinsExplicitTargetForNextTask(t *testing.T) {
	s, a, project := fixture(t)
	f := newMessageFixtureOn(t, s, a, project, M{"id": "one"}, M{"id": "two"})
	tasks, e := s.Store.List(project.ID, "task", f.g.ID)
	if e != nil {
		t.Fatal(e)
	}
	secondTask := ""
	for _, task := range tasks {
		if task.ID != S(f.run.Data, "taskId") {
			secondTask = task.ID
		}
	}
	if secondTask == "" {
		t.Fatal("missing second task")
	}
	in := M{"projectId": project.ID, "goalId": f.g.ID, "recipient": f.worker.ID, "type": "request", "key": "target", "payload": M{"text": "Only the second task"}}
	in["recipientTaskId"] = secondTask
	must(t, s, a, "messages.send", in)
	if len(Items(f.receive(t).Data["items"])) != 0 {
		t.Fatal("different task consumed the message")
	}
	must(t, s, f.worker, "tasks.complete", M{"id": f.run.ID, "leaseToken": S(f.run.Data, "leaseToken"), "exitCode": 0, "result": M{"summary": "First task done"}})
	f.run = must(t, s, f.worker, "tasks.claim", M{})
	if f.run.Kind != "run" || S(f.run.Data, "taskId") != secondTask {
		t.Fatal("next task was not claimed")
	}
	if len(Items(f.receive(t).Data["items"])) != 1 {
		t.Fatal("explicit recipient task missed its message")
	}
}

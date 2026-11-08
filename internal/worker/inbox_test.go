package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/intelligence-substrate/platform/internal/httpapi"
	p "github.com/intelligence-substrate/platform/internal/platform"
)

type inboxHarness struct {
	svc           *p.Service
	h             *httpapi.Handler
	server        *httptest.Server
	owner         p.Actor
	project, goal p.Resource
}

func newInboxHarness(t *testing.T, parallel bool) *inboxHarness {
	t.Helper()
	store, e := p.Open(filepath.Join(t.TempDir(), "inbox.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { store.Close() })
	svc := p.NewService(store)
	h := httpapi.New(svc, false)
	if e = h.Auth.Bootstrap("owner@inbox.test", "inbox-owner-password-2026"); e != nil {
		t.Fatal(e)
	}
	user, e := h.Auth.IdentityByEmail("owner@inbox.test")
	if e != nil {
		t.Fatal(e)
	}
	f := &inboxHarness{svc: svc, h: h, owner: p.Actor{ID: user.ID, Kind: "human", Admin: true}}
	f.project = f.command(t, f.owner, "projects.create", p.M{"title": "Actual Worker collaboration"})
	f.goal = f.command(t, f.owner, "goals.create", p.M{"projectId": f.project.ID, "title": "Resolve the time contract", "outcome": "Document an approved UTC interface", "targets": []any{"web"}, "criteria": []any{p.M{"id": "utc", "text": "All timestamps use UTC"}}, "budget": 100})
	f.command(t, f.owner, "goals.approve", p.M{"id": f.goal.ID})
	tasks := []any{p.M{"id": "left", "title": "Clarify the contract", "kind": "analysis", "role": "developer", "runtime": runtime.GOOS, "budgetCents": 100}}
	if parallel {
		tasks = append(tasks, p.M{"id": "right", "title": "Review the contract", "kind": "analysis", "role": "architect", "runtime": runtime.GOOS, "budgetCents": 100})
	}
	plan := f.command(t, f.owner, "plans.propose", p.M{"goalId": f.goal.ID, "summary": "Collaborate without changing the baseline", "tasks": tasks})
	f.command(t, f.owner, "plans.approve", p.M{"id": plan.ID})
	f.server = httptest.NewServer(h)
	t.Cleanup(f.server.Close)
	return f
}

func (f *inboxHarness) command(t *testing.T, a p.Actor, name string, in p.M) p.Resource {
	t.Helper()
	r, e := f.svc.Command(context.Background(), a, name, in)
	if e != nil {
		t.Fatalf("%s: %v", name, e)
	}
	return r
}

func (f *inboxHarness) runner(t *testing.T, role string) (*Runner, p.Actor) {
	t.Helper()
	w := f.command(t, f.owner, "workers.register", p.M{"projectId": f.project.ID, "title": role + " collaborator", "roles": []any{role}, "runtime": runtime.GOOS, "capabilities": []any{"analysis"}})
	token, actor, e := f.h.Auth.IssueToken(context.Background(), f.owner, p.M{"projectId": f.project.ID, "workerId": w.ID})
	if e != nil {
		t.Fatal(e)
	}
	client, e := NewClient(f.server.URL, token, true)
	if e != nil {
		t.Fatal(e)
	}
	return &Runner{Client: client, Config: Config{Driver: "native", AllowNative: true, StateDir: t.TempDir()}}, actor
}

func (f *inboxHarness) task(t *testing.T, logical string) p.Resource {
	t.Helper()
	tasks, e := f.svc.Store.List(f.project.ID, "task", f.goal.ID)
	if e != nil {
		t.Fatal(e)
	}
	for _, task := range tasks {
		if p.S(task.Data, "logicalId") == logical {
			return task
		}
	}
	t.Fatal("missing task", logical)
	return p.Resource{}
}

func actionResponse(action p.M) p.M {
	body, _ := json.Marshal(action)
	return p.M{"choices": []any{p.M{"message": p.M{"content": string(body)}}}}
}

func TestWorkersExchangeMessagesDuringActualAgentLoops(t *testing.T) {
	f := newInboxHarness(t, true)
	left, leftActor := f.runner(t, "developer")
	right, rightActor := f.runner(t, "architect")
	leftTask, rightTask := f.task(t, "left"), f.task(t, "right")
	initial := f.command(t, f.owner, "messages.send", p.M{"projectId": f.project.ID, "goalId": f.goal.ID, "recipient": leftActor.ID, "recipientTaskId": leftTask.ID, "type": "request", "key": "initial", "payload": p.M{"instruction": "Document UTC; ask the architect to confirm it."}})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	startRight, rightDone := make(chan struct{}), make(chan error, 1)
	go func() {
		select {
		case <-startRight:
			worked, e := right.RunOnce(ctx)
			if e == nil && !worked {
				e = fmt.Errorf("right worker did not claim work")
			}
			rightDone <- e
		case <-ctx.Done():
			rightDone <- ctx.Err()
		}
	}()
	var leftCalls, rightCalls atomic.Int32
	f.h.ModelCall = func(ctx context.Context, a p.Actor, in p.M) (p.M, error) {
		body, _ := json.Marshal(in["messages"])
		text := string(body)
		if a.ID == leftActor.ID {
			switch leftCalls.Add(1) {
			case 1:
				if !strings.Contains(text, "Document UTC; ask the architect to confirm it.") {
					return nil, fmt.Errorf("first model request did not receive actual incoming context")
				}
				return actionResponse(p.M{"action": "message", "recipient": rightActor.ID, "recipientTaskId": rightTask.ID, "type": "clarification", "payload": p.M{"question": "Please confirm UTC for the interface."}, "causationId": initial.ID}), nil
			case 2:
				close(startRight)
				select {
				case e := <-rightDone:
					if e != nil {
						return nil, e
					}
				case <-ctx.Done():
					return nil, ctx.Err()
				}
				return actionResponse(p.M{"action": "read", "path": "not-present.txt"}), nil
			case 3:
				if !strings.Contains(text, "Confirmed UTC; messages do not approve a release.") || strings.Count(text, "Document UTC; ask the architect to confirm it.") != 1 {
					return nil, fmt.Errorf("new reply missing or original context duplicated")
				}
				return actionResponse(p.M{"action": "finish", "summary": "The actual architect reply confirms UTC. Independent quality and release approval are still required."}), nil
			}
		}
		if a.ID == rightActor.ID {
			switch rightCalls.Add(1) {
			case 1:
				if !strings.Contains(text, "Please confirm UTC for the interface.") {
					return nil, fmt.Errorf("second agent never received the first agent request")
				}
				messages, e := f.svc.Store.List(f.project.ID, "message", f.goal.ID)
				if e != nil {
					return nil, e
				}
				cause := ""
				for _, m := range messages {
					if p.S(m.Data, "sender") == leftActor.ID {
						cause = m.ID
					}
				}
				if cause == "" {
					return nil, fmt.Errorf("no durable request to reply to")
				}
				return actionResponse(p.M{"action": "message", "recipient": leftActor.ID, "recipientTaskId": leftTask.ID, "type": "result", "causationId": cause, "payload": p.M{"answer": "Confirmed UTC; messages do not approve a release."}}), nil
			case 2:
				return actionResponse(p.M{"action": "finish", "summary": "Confirmed the UTC interface contract."}), nil
			}
		}
		return nil, fmt.Errorf("unexpected model execution")
	}
	worked, e := left.RunOnce(ctx)
	if e != nil || !worked {
		t.Fatalf("left worker: worked=%v error=%v", worked, e)
	}
	if leftCalls.Load() != 3 || rightCalls.Load() != 2 {
		t.Fatal("actual reciprocal model loop did not run", leftCalls.Load(), rightCalls.Load())
	}
	for _, logical := range []string{"left", "right"} {
		task := f.task(t, logical)
		if task.Status != "succeeded" {
			t.Fatal("collaboration task failed", logical, task)
		}
	}
	messages, _ := f.svc.Store.List(f.project.ID, "message", f.goal.ID)
	receipts, _ := f.svc.Store.List(f.project.ID, "message_receipt", f.goal.ID)
	if len(messages) != 3 || len(receipts) != 3 {
		t.Fatal("missing real messages/receipts", len(messages), len(receipts))
	}
	for _, m := range messages {
		if m.Status != "delivered" || p.N(m.Data, "requirementRevision") != 1 {
			t.Fatal("unbound or undelivered message", m)
		}
	}
	knowledge, _ := f.svc.Store.List(f.project.ID, "knowledge", "")
	if len(knowledge) != 2 {
		t.Fatal("actual agent conclusions were not stored")
	}
	evidence, _ := f.svc.Store.List(f.project.ID, "evidence", f.goal.ID)
	if len(evidence) != 0 {
		t.Fatal("collaboration manufactured quality evidence")
	}
}

func TestWorkerRetryRestoresInboxAfterModelFailure(t *testing.T) {
	f := newInboxHarness(t, false)
	runner, actor := f.runner(t, "developer")
	task := f.task(t, "left")
	f.command(t, f.owner, "messages.send", p.M{"projectId": f.project.ID, "goalId": f.goal.ID, "recipient": actor.ID, "recipientTaskId": task.ID, "type": "request", "key": "recover", "payload": p.M{"text": "Keep this UTC requirement after a provider failure."}})
	var calls atomic.Int32
	f.h.ModelCall = func(_ context.Context, _ p.Actor, in p.M) (p.M, error) {
		body, _ := json.Marshal(in["messages"])
		if !strings.Contains(string(body), "Keep this UTC requirement after a provider failure.") {
			return nil, fmt.Errorf("durable inbox was not restored")
		}
		if calls.Add(1) == 1 {
			return nil, &p.Fault{Code: "model_failed", Message: "simulated upstream outage", Status: 502}
		}
		return actionResponse(p.M{"action": "finish", "summary": "Restored the original UTC requirement after the failed model request."}), nil
	}
	if worked, e := runner.RunOnce(context.Background()); e != nil || !worked {
		t.Fatal("failed execution was not recorded", worked, e)
	}
	if f.task(t, "left").Status != "blocked" {
		t.Fatal("provider failure was reported as completed")
	}
	receipts, _ := f.svc.Store.List(f.project.ID, "message_receipt", f.goal.ID)
	if len(receipts) != 1 {
		t.Fatal("provider failure lost received context")
	}
	f.command(t, f.owner, "tasks.retry", p.M{"id": task.ID})
	replacement, _ := f.runner(t, "developer")
	if worked, e := replacement.RunOnce(context.Background()); e != nil || !worked {
		t.Fatal("replacement worker failed", worked, e)
	}
	if f.task(t, "left").Status != "succeeded" || calls.Load() != 2 {
		t.Fatal("task did not recover with original context", calls.Load())
	}
	replayed, _ := f.svc.Store.List(f.project.ID, "message_receipt", f.goal.ID)
	if len(replayed) != 1 || replayed[0].ID != receipts[0].ID {
		t.Fatal("recovery created a second delivery record")
	}
}

func TestWorkerDefersOversizedInboxBeforeCallingModel(t *testing.T) {
	f := newInboxHarness(t, false)
	runner, actor := f.runner(t, "developer")
	task := f.task(t, "left")
	for i := 0; i < 33; i++ {
		f.command(t, f.owner, "messages.send", p.M{"projectId": f.project.ID, "goalId": f.goal.ID, "recipient": actor.ID, "recipientTaskId": task.ID, "type": "request", "key": fmt.Sprintf("message-%d", i), "payload": p.M{"text": "Explicitly preserve all pending requests"}})
	}
	var calls atomic.Int32
	f.h.ModelCall = func(context.Context, p.Actor, p.M) (p.M, error) {
		calls.Add(1)
		return actionResponse(p.M{"action": "finish", "summary": "Must not run"}), nil
	}
	if worked, e := runner.RunOnce(context.Background()); e != nil || !worked {
		t.Fatal("capacity block was not recorded", worked, e)
	}
	if calls.Load() != 0 || f.task(t, "left").Status != "blocked" {
		t.Fatal("oversized inbox was silently truncated or spent model budget")
	}
	messages, _ := f.svc.Store.List(f.project.ID, "message", f.goal.ID)
	pending := 0
	for _, message := range messages {
		if message.Status == "pending" {
			pending++
		}
	}
	if pending != 1 {
		t.Fatal("capacity deferred request was acknowledged", pending)
	}
}

func TestWorkerReceivesReplyBeforeAcceptingModelFinish(t *testing.T) {
	f := newInboxHarness(t, false)
	runner, actor := f.runner(t, "developer")
	var calls atomic.Int32
	f.h.ModelCall = func(ctx context.Context, _ p.Actor, in p.M) (p.M, error) {
		if calls.Add(1) == 1 {
			_, e := f.svc.Command(ctx, f.owner, "messages.send", p.M{"projectId": f.project.ID, "goalId": f.goal.ID, "recipient": actor.ID, "type": "result", "key": "during-final-response", "payload": p.M{"answer": "Late reply: confirm UTC before finishing."}})
			if e != nil {
				return nil, e
			}
			return actionResponse(p.M{"action": "finish", "summary": "Premature conclusion before the late reply."}), nil
		}
		body, _ := json.Marshal(in["messages"])
		if !strings.Contains(string(body), "Late reply: confirm UTC before finishing.") {
			return nil, fmt.Errorf("finish race lost the late reply")
		}
		return actionResponse(p.M{"action": "finish", "summary": "Final conclusion includes the actual late UTC reply."}), nil
	}
	if worked, e := runner.RunOnce(context.Background()); e != nil || !worked {
		t.Fatal("finish race execution failed", worked, e)
	}
	if calls.Load() != 2 {
		t.Fatal("model finish bypassed newly arrived collaboration", calls.Load())
	}
	knowledge, _ := f.svc.Store.List(f.project.ID, "knowledge", "")
	if len(knowledge) != 1 || !strings.Contains(p.S(knowledge[0].Data, "content"), "actual late UTC reply") {
		t.Fatal("premature conclusion was published as task output")
	}
}

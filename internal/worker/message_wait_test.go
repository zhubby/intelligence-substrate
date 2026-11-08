package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/intelligence-substrate/platform/internal/integrations"
	p "github.com/intelligence-substrate/platform/internal/platform"
)

func TestMessageWaitWorkerSuspendsAndResumesFreshInference(t *testing.T) {
	f := newInboxHarness(t, true)
	left, actor := f.runner(t, "developer")
	right, rightActor := f.runner(t, "architect")
	leftTask, rightTask := f.task(t, "left"), f.task(t, "right")
	calls := map[string]int{}
	f.h.ModelCall = func(_ context.Context, a p.Actor, in p.M) (p.M, error) {
		calls[a.ID]++
		messages, _ := json.Marshal(in["messages"])
		if a.ID == actor.ID {
			switch calls[a.ID] {
			case 1:
				return actionResponse(p.M{"action": "message", "recipient": rightActor.ID, "recipientTaskId": rightTask.ID, "type": "clarification", "payload": p.M{"question": "Confirm UTC before writing the specification."}}), nil
			case 2:
				all, _ := f.svc.Store.List(f.project.ID, "message", f.goal.ID)
				return actionResponse(p.M{"action": "wait", "requestMessageId": all[0].ID, "reason": "The architect must answer the existing scope question.", "summary": "Read the baseline; awaiting the architect's UTC confirmation.", "timeoutSeconds": 600}), nil
			}
		}
		if a.ID == rightActor.ID {
			switch calls[a.ID] {
			case 1:
				if !strings.Contains(string(messages), "Confirm UTC before writing") {
					return nil, fmt.Errorf("responder lost request")
				}
				all, _ := f.svc.Store.List(f.project.ID, "message", f.goal.ID)
				return actionResponse(p.M{"action": "message", "recipient": actor.ID, "recipientTaskId": leftTask.ID, "type": "result", "causationId": all[0].ID, "payload": p.M{"answer": "UTC confirmed within the approved scope."}}), nil
			case 2:
				return actionResponse(p.M{"action": "finish", "summary": "Confirmed the UTC convention."}), nil
			}
		}
		if a.ID != actor.ID && a.ID != rightActor.ID {
			if len(p.Items(in["messages"])) != 3 || !strings.Contains(string(messages), "UTC confirmed within") || !strings.Contains(string(messages), "untrustedAnalysisContinuation") || strings.Contains(string(messages), "Tool observation:") {
				return nil, fmt.Errorf("continuation did not use a fresh bounded context: %s", messages)
			}
			return actionResponse(p.M{"action": "finish", "summary": "Documented UTC using the durable architect reply. No release approval was granted."}), nil
		}
		return nil, fmt.Errorf("unexpected inference after wait: %s %d", a.ID, calls[a.ID])
	}
	if worked, e := left.RunOnce(context.Background()); e != nil || !worked {
		t.Fatal(worked, e)
	}
	if f.task(t, "left").Status != "waiting_message" || calls[actor.ID] != 2 {
		t.Fatal("worker did not suspend after two actual calls", f.task(t, "left"), calls)
	}
	entries, e := os.ReadDir(left.Config.StateDir)
	if e != nil || len(entries) != 0 {
		t.Fatal("acknowledged wait journal was not cleared", e, entries)
	}
	if worked, e := left.RunOnce(context.Background()); e != nil || worked {
		t.Fatal("waiting worker kept its execution", worked, e)
	}
	if worked, e := right.RunOnce(context.Background()); e != nil || !worked {
		t.Fatal(worked, e)
	}
	if f.task(t, "left").Status != "pending" {
		t.Fatal("reply did not automatically wake task")
	}
	f.command(t, f.owner, "workers.disable", p.M{"id": actor.WorkerID})
	replacement, _ := f.runner(t, "developer")
	if worked, e := replacement.RunOnce(context.Background()); e != nil || !worked {
		t.Fatal(worked, e)
	}
	if task := f.task(t, "left"); task.Status != "succeeded" || p.N(task.Data, "attempts") != 1 || p.N(task.Data, "messageStepsUsed") != 2 {
		t.Fatal("suspension consumed retry or failed to finish", task)
	}
	runs, _ := f.svc.Store.List(f.project.ID, "run", f.goal.ID)
	if len(runs) != 3 {
		t.Fatal("duplicate or missing runs", len(runs))
	}
	waits, _ := f.svc.Store.List(f.project.ID, "message_wait", f.goal.ID)
	if len(waits) != 1 || waits[0].Status != "resumed" {
		t.Fatal("missing durable wait", waits)
	}
	evidence, _ := f.svc.Store.List(f.project.ID, "evidence", f.goal.ID)
	if len(evidence) != 0 {
		t.Fatal("analysis coordination manufactured quality evidence")
	}
}

func TestMessageWaitWorkerJournalsLostAckAndDoesNotResendInference(t *testing.T) {
	f := newInboxHarness(t, false)
	runner, actor := f.runner(t, "developer")
	calls := 0
	f.h.ModelCall = func(_ context.Context, _ p.Actor, _ p.M) (p.M, error) {
		calls++
		if calls == 1 {
			return actionResponse(p.M{"action": "message", "recipient": f.owner.ID, "type": "request", "payload": p.M{"question": "Please confirm UTC."}}), nil
		}
		all, _ := f.svc.Store.List(f.project.ID, "message", f.goal.ID)
		return actionResponse(p.M{"action": "wait", "requestMessageId": all[0].ID, "reason": "Need an exact answer", "summary": "Original context preserved without a conversation transcript.", "timeoutSeconds": 600}), nil
	}
	lose := true
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if lose && strings.HasSuffix(r.URL.Path, "/commands/tasks.wait") {
			response := httptest.NewRecorder()
			f.h.ServeHTTP(response, r)
			if response.Code != 200 {
				t.Errorf("wait failed before lost ack: %s", response.Body.String())
			}
			w.WriteHeader(503)
			return
		}
		f.h.ServeHTTP(w, r)
	}))
	defer proxy.Close()
	runner.Client.URL = proxy.URL
	if worked, e := runner.RunOnce(context.Background()); e == nil || !worked {
		t.Fatal("lost response not preserved", worked, e)
	}
	files, _ := filepath.Glob(filepath.Join(runner.Config.StateDir, "run_*.json"))
	if len(files) != 1 {
		t.Fatal("wait journal missing", files)
	}
	body, _ := os.ReadFile(files[0])
	var journal p.M
	_ = json.Unmarshal(body, &journal)
	if p.S(journal, "operation") != "wait" {
		t.Fatal("journal would replay wait as task success", journal)
	}
	f.command(t, f.owner, "messages.send", p.M{"projectId": f.project.ID, "goalId": f.goal.ID, "recipient": actor.ID, "recipientTaskId": f.task(t, "left").ID, "type": "result", "key": "after-lost-response", "causationId": p.S(p.Obj(journal["result"]), "requestMessageId"), "payload": p.M{"answer": "UTC"}})
	lose = false
	if e := runner.flush(context.Background()); e != nil {
		t.Fatal(e)
	}
	if calls != 2 {
		t.Fatal("journal replay resent model inference", calls)
	}
	waits, _ := f.svc.Store.List(f.project.ID, "message_wait", f.goal.ID)
	if len(waits) != 1 || waits[0].Status != "ready" {
		t.Fatal("lost ack duplicated or reset wait", waits)
	}
}

func TestMessageWaitWorkerRejectsSuspensionAfterCommand(t *testing.T) {
	f := newInboxHarness(t, false)
	runner, _ := f.runner(t, "developer")
	calls := 0
	f.h.ModelCall = func(_ context.Context, _ p.Actor, in p.M) (p.M, error) {
		calls++
		switch calls {
		case 1:
			return actionResponse(p.M{"action": "run", "command": []string{"/bin/sh", "-c", "printf actual-tool > marker.txt"}}), nil
		case 2:
			return actionResponse(p.M{"action": "wait", "requestMessageId": "missing", "reason": "Cannot lose actual tool state", "summary": "Must not suspend", "timeoutSeconds": 600}), nil
		case 3:
			body, _ := json.Marshal(in["messages"])
			if !strings.Contains(string(body), "cannot wait after executing a command") {
				return nil, fmt.Errorf("unsafe wait was not explained")
			}
			return actionResponse(p.M{"action": "finish", "summary": "Actual command completed; no suspension or command replay occurred."}), nil
		}
		return nil, fmt.Errorf("unexpected call")
	}
	if worked, e := runner.RunOnce(context.Background()); e != nil || !worked {
		t.Fatal(worked, e)
	}
	waits, _ := f.svc.Store.List(f.project.ID, "message_wait", f.goal.ID)
	if len(waits) != 0 || calls != 3 || f.task(t, "left").Status != "succeeded" {
		t.Fatal("unsafe wait lost tool state", waits, calls)
	}
}

func TestMessageWaitActualLocalProviderRetainsCumulativeBilling(t *testing.T) {
	f := newInboxHarness(t, true)
	left, leftActor := f.runner(t, "developer")
	right, rightActor := f.runner(t, "architect")
	leftTask, rightTask := f.task(t, "left"), f.task(t, "right")
	calls, leftCalls, rightCalls := 0, 0, 0
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in p.M
		if e := json.NewDecoder(r.Body).Decode(&in); e != nil {
			t.Error(e)
			w.WriteHeader(400)
			return
		}
		if r.URL.Path != "/v1/chat/completions" || p.S(in, "model") != "local-wait-fixture" {
			t.Error("unexpected provider request")
			w.WriteHeader(400)
			return
		}
		calls++
		messages := p.Items(in["messages"])
		var initial p.M
		if e := json.Unmarshal([]byte(p.S(p.Obj(messages[1]), "content")), &initial); e != nil {
			t.Error(e)
			w.WriteHeader(400)
			return
		}
		var action p.M
		all, _ := f.svc.Store.List(f.project.ID, "message", f.goal.ID)
		if p.S(p.Obj(initial["task"]), "logicalId") == "left" {
			leftCalls++
			switch leftCalls {
			case 1:
				action = p.M{"action": "message", "recipient": rightActor.ID, "recipientTaskId": rightTask.ID, "type": "request", "payload": p.M{"question": "Confirm UTC using the actual provider protocol."}}
			case 2:
				action = p.M{"action": "wait", "requestMessageId": all[0].ID, "reason": "Need exact contract reply", "summary": "Original request awaits UTC confirmation.", "timeoutSeconds": 600}
			case 3:
				if len(p.Obj(initial["untrustedAnalysisContinuation"])) == 0 {
					t.Error("actual provider lost fresh continuation")
				}
				action = p.M{"action": "finish", "summary": "Documented UTC from the exact durable reply."}
			}
		} else {
			rightCalls++
			if rightCalls == 1 {
				action = p.M{"action": "message", "recipient": leftActor.ID, "recipientTaskId": leftTask.ID, "type": "result", "causationId": all[0].ID, "payload": p.M{"answer": "UTC confirmed by the local deterministic collaborator."}}
			} else {
				action = p.M{"action": "finish", "summary": "Confirmed UTC."}
			}
		}
		out := actionResponse(action)
		out["id"] = "local-wait-response"
		out["model"] = "local-wait-fixture"
		out["usage"] = p.M{"prompt_tokens": 10, "completion_tokens": 30}
		p.Obj(p.Items(out["choices"])[0])["finish_reason"] = "stop"
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	}))
	defer provider.Close()
	model := &integrations.Model{Service: f.svc, BaseURL: provider.URL + "/v1", Name: "local-wait-fixture", InputUSD: 1, OutputUSD: 1, Client: provider.Client()}
	f.h.ModelCall = model.Chat
	for _, runner := range []*Runner{left, right, left} {
		if worked, e := runner.RunOnce(context.Background()); e != nil || !worked {
			t.Fatal(worked, e)
		}
	}
	if calls != 5 || leftCalls != 3 || rightCalls != 2 || f.task(t, "left").Status != "succeeded" {
		t.Fatal("actual protocol collaboration failed", calls, leftCalls, rightCalls)
	}
	charges, _ := f.svc.Store.List(f.project.ID, "charge", f.goal.ID)
	if len(charges) != 5 {
		t.Fatal("model fees lost or duplicated", len(charges))
	}
	for _, charge := range charges {
		if charge.Status != "settled" || p.N(charge.Data, "chargedCents") != 1 {
			t.Fatal("actual usage not reconciled", charge)
		}
	}
	runs, _ := f.svc.Store.List(f.project.ID, "run", f.goal.ID)
	found := false
	for _, run := range runs {
		if len(p.Obj(run.Data["messageContinuation"])) > 0 {
			found = true
			if p.N(run.Data, "reservedCents") != 98 || p.N(run.Data, "spentCents") != 1 {
				t.Fatal("continuation reset its billing envelope", run)
			}
		}
	}
	if !found {
		t.Fatal("actual provider never resumed")
	}
	g, _ := f.svc.Store.Get(f.goal.ID)
	if p.N(g.Data, "spentCents") != 5 {
		t.Fatal("goal billing mismatch", g.Data)
	}
}

func TestMessageWaitMalformedModelActionKeepsExecutionRecoverable(t *testing.T) {
	for _, mode := range []string{"unknown-request", "missing-summary", "invalid-timeout"} {
		t.Run(mode, func(t *testing.T) {
			f := newInboxHarness(t, false)
			runner, _ := f.runner(t, "developer")
			calls := 0
			f.h.ModelCall = func(_ context.Context, _ p.Actor, in p.M) (p.M, error) {
				calls++
				if calls == 1 {
					return actionResponse(p.M{"action": "message", "recipient": f.owner.ID, "type": "request", "payload": p.M{"question": "UTC?"}}), nil
				}
				if calls == 2 {
					all, _ := f.svc.Store.List(f.project.ID, "message", f.goal.ID)
					action := p.M{"action": "wait", "requestMessageId": all[0].ID, "reason": "Need an exact reply", "summary": "No tools were executed.", "timeoutSeconds": 600}
					switch mode {
					case "unknown-request":
						action["requestMessageId"] = "another-run-request"
					case "missing-summary":
						delete(action, "summary")
					case "invalid-timeout":
						action["timeoutSeconds"] = 1
					}
					return actionResponse(action), nil
				}
				body, _ := json.Marshal(in["messages"])
				if !strings.Contains(string(body), "invalid wait action") {
					return nil, fmt.Errorf("model did not receive correctable validation failure")
				}
				return actionResponse(p.M{"action": "finish", "summary": "Preserved the unknown contract as an open question, without inventing an answer."}), nil
			}
			if worked, e := runner.RunOnce(context.Background()); e != nil || !worked {
				t.Fatal("malformed action stranded the journal", worked, e)
			}
			if f.task(t, "left").Status != "succeeded" || calls != 3 {
				t.Fatal("model could not correct its wait action", calls)
			}
			waits, _ := f.svc.Store.List(f.project.ID, "message_wait", f.goal.ID)
			if len(waits) != 0 {
				t.Fatal("invalid wait persisted")
			}
		})
	}
}

func TestMessageWaitWorkerReadsFrozenSourceAfterRepositoryChanges(t *testing.T) {
	for _, mode := range []string{"pinned", "empty-source"} {
		t.Run(mode, func(t *testing.T) {
			f := newInboxHarness(t, false)
			runner, actor := f.runner(t, "developer")
			var repo p.Resource
			commit := ""
			if mode == "pinned" {
				repo = f.command(t, f.owner, "repositories.create", p.M{"projectId": f.project.ID, "title": "Read source"})
				repo = f.command(t, f.owner, "repositories.commit", p.M{"id": repo.ID, "branch": "feature", "message": "Original context", "files": p.M{"contract.txt": "original-frozen-UTC"}})
				commit = p.S(repo.Data, "lastCommit")
				plan := f.command(t, f.owner, "plans.propose", p.M{"goalId": f.goal.ID, "summary": "Read a fixed source", "tasks": []any{p.M{"id": "read-fixed", "title": "Read fixed contract", "kind": "analysis", "role": "developer", "runtime": runtime.GOOS, "repositoryId": repo.ID, "commit": commit, "budgetCents": 100}}})
				f.command(t, f.owner, "plans.approve", p.M{"id": plan.ID})
			}
			calls := 0
			f.h.ModelCall = func(_ context.Context, _ p.Actor, in p.M) (p.M, error) {
				calls++
				var initial p.M
				_ = json.Unmarshal([]byte(p.S(p.Obj(p.Items(in["messages"])[1]), "content")), &initial)
				if calls == 1 {
					return actionResponse(p.M{"action": "message", "recipient": f.owner.ID, "type": "request", "payload": p.M{"question": "Confirm this frozen context?"}}), nil
				}
				if calls == 2 {
					all, _ := f.svc.Store.List(f.project.ID, "message", f.goal.ID)
					return actionResponse(p.M{"action": "wait", "requestMessageId": all[0].ID, "reason": "Need context confirmation", "summary": "Read source remains fixed.", "timeoutSeconds": 600}), nil
				}
				if mode == "empty-source" {
					if len(p.Items(initial["files"])) != 0 || p.S(initial, "commit") != "" {
						return nil, fmt.Errorf("continuation adopted a newly created repository")
					}
				} else if calls == 3 {
					if p.S(initial, "commit") != commit {
						return nil, fmt.Errorf("continuation switched source commit")
					}
					return actionResponse(p.M{"action": "read", "path": "contract.txt"}), nil
				} else {
					b, _ := json.Marshal(in["messages"])
					if !strings.Contains(string(b), "Tool observation:\\noriginal-frozen-UTC") || strings.Contains(string(b), "later-mutated-source") {
						return nil, fmt.Errorf("actual file bytes were not frozen")
					}
				}
				return actionResponse(p.M{"action": "finish", "summary": "Analyzed only the original fixed source."}), nil
			}
			if worked, e := runner.RunOnce(context.Background()); e != nil || !worked {
				t.Fatal(worked, e)
			}
			waits, _ := f.svc.Store.List(f.project.ID, "message_wait", f.goal.ID)
			if len(waits) != 1 || waits[0].Status != "waiting" {
				t.Fatal("missing suspension", waits)
			}
			if mode == "empty-source" {
				repo = f.command(t, f.owner, "repositories.create", p.M{"projectId": f.project.ID, "title": "Later repository"})
			}
			f.command(t, f.owner, "repositories.commit", p.M{"id": repo.ID, "branch": "feature", "expectedHead": commit, "message": "Later context", "files": p.M{"contract.txt": "later-mutated-source"}})
			f.command(t, f.owner, "messages.send", p.M{"projectId": f.project.ID, "goalId": f.goal.ID, "recipient": actor.ID, "recipientTaskId": p.S(waits[0].Data, "taskId"), "type": "result", "key": "frozen-reply", "causationId": p.S(waits[0].Data, "requestMessageId"), "payload": p.M{"answer": "Continue with the original scope."}})
			if worked, e := runner.RunOnce(context.Background()); e != nil || !worked {
				t.Fatal(worked, e)
			}
			task, _ := f.svc.Store.Get(p.S(waits[0].Data, "taskId"))
			if task.Status != "succeeded" {
				runs, _ := f.svc.Store.List(f.project.ID, "run", f.goal.ID)
				t.Fatal("frozen file read failed", task, runs)
			}
		})
	}
}

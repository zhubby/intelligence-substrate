package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/intelligence-substrate/platform/internal/httpapi"
	"github.com/intelligence-substrate/platform/internal/integrations"
	p "github.com/intelligence-substrate/platform/internal/platform"
)

func TestWorkerUsesRemainingModelCallsFromAdmission(t *testing.T) {
	f := newInboxHarness(t, false)
	runner, actor := f.runner(t, "developer")
	run := f.command(t, actor, "tasks.claim", p.M{})
	// Exercise a versioned control-plane admission snapshot at the Worker
	// boundary; inference admission has separate real gateway tests.
	run.Data["agentAllowance"] = p.M{"usedModelCalls": 23}
	calls := 0
	f.h.ModelCall = func(_ context.Context, _ p.Actor, _ p.M) (p.M, error) {
		calls++
		return actionResponse(p.M{"action": "read", "path": "absent.txt"}), nil
	}
	resources, e := f.svc.Store.List(f.project.ID, "", "")
	if e != nil {
		t.Fatal(e)
	}
	_, exit, e := runner.agent(context.Background(), run, resources, nil, t.TempDir(), "", "", "", "", io.Discard)
	if e == nil || exit == 0 || calls != 1 {
		t.Fatal("replacement Worker reset the model loop instead of consuming one remaining call", exit, e, calls)
	}
}

func TestWorkerReplacementSharesActualModelAllowanceAndInbox(t *testing.T) {
	for _, mode := range []string{"finish", "exhausted"} {
		t.Run(mode, func(t *testing.T) {
			f := newInboxHarness(t, false)
			first, actor := f.runner(t, "developer")
			task := f.task(t, "left")
			f.command(t, f.owner, "messages.send", p.M{"projectId": f.project.ID, "goalId": f.goal.ID, "recipient": actor.ID, "recipientTaskId": task.ID, "type": "request", "key": "original-request", "payload": p.M{"question": "Preserve this UTC decision across replacement nodes."}})
			var calls atomic.Int32
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				var input p.M
				if e := json.NewDecoder(req.Body).Decode(&input); e != nil {
					t.Error(e)
					w.WriteHeader(400)
					return
				}
				call := calls.Add(1)
				body, _ := json.Marshal(input["messages"])
				if !strings.Contains(string(body), "Preserve this UTC decision") {
					t.Error("lost durable context")
				}
				if call == 24 && (len(p.Items(input["messages"])) != 3 || strings.Contains(string(body), "Tool observation:")) {
					t.Error("replacement replayed the old model session")
				}
				action := p.M{"action": "read", "path": "absent.txt"}
				if call == 23 {
					action = p.M{"action": "blocked", "reason": "Synthetic provider asks for an operator retry"}
				}
				if call == 24 && mode == "finish" {
					action = p.M{"action": "finish", "summary": "UTC decision retained on a replacement node."}
				}
				content, _ := json.Marshal(action)
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(p.M{"id": fmt.Sprintf("local-%d", call), "choices": []any{p.M{"finish_reason": "stop", "message": p.M{"content": string(content)}}}, "usage": p.M{"prompt_tokens": 1, "completion_tokens": 1}})
			}))
			defer provider.Close()
			model := &integrations.Model{Service: f.svc, BaseURL: provider.URL, Name: "local-deterministic", InputUSD: 1, OutputUSD: 1, Client: provider.Client()}
			f.h.ModelCall = model.Chat
			if worked, e := first.RunOnce(context.Background()); e != nil || !worked {
				t.Fatal(worked, e)
			}
			if calls.Load() != 23 || f.task(t, "left").Status != "blocked" {
				t.Fatal("first run did not use 23 actual gateway calls", calls.Load())
			}
			f.command(t, f.owner, "workers.disable", p.M{"id": actor.WorkerID})
			f.command(t, f.owner, "tasks.retry", p.M{"id": task.ID})
			replacement, _ := f.runner(t, "developer")
			if worked, e := replacement.RunOnce(context.Background()); e != nil || !worked {
				t.Fatal(worked, e)
			}
			final := f.task(t, "left")
			want := "succeeded"
			if mode == "exhausted" {
				want = "blocked"
			}
			runs, _ := f.svc.Store.List(f.project.ID, "run", f.goal.ID)
			charges, _ := f.svc.Store.List(f.project.ID, "charge", f.goal.ID)
			receipts, _ := f.svc.Store.List(f.project.ID, "message_receipt", f.goal.ID)
			if final.Status != want || calls.Load() != 24 || len(charges) != 24 || len(runs) != 2 || len(receipts) != 1 {
				t.Fatal("replacement did not share cumulative allowance", final.Status, calls.Load(), len(charges), len(runs), len(receipts))
			}
			for _, run := range runs {
				if p.S(run.Data, "workerId") != actor.WorkerID && (p.N(run.Data, "reservedCents") != 77 || p.N(p.Obj(run.Data["agentAllowance"]), "usedModelCalls") != 23) {
					t.Fatal("replacement reset budget or steps")
				}
			}
			for _, charge := range charges {
				if charge.Status != "settled" || p.N(charge.Data, "chargedCents") != 1 {
					t.Fatal("local gateway costs not settled", charge.Status)
				}
			}
			if mode == "exhausted" {
				if _, e := f.svc.Command(context.Background(), f.owner, "tasks.retry", p.M{"id": task.ID}); e == nil {
					t.Fatal("24-call exhaustion was retried")
				}
			}
			root := t.TempDir()
			if evidence := os.Getenv("IS_AGENT_ALLOWANCE_EVIDENCE_DIR"); evidence != "" {
				root = filepath.Join(evidence, "worker-"+mode)
				if e := os.Mkdir(root, 0700); e != nil {
					t.Fatal(e)
				}
			}
			archive := filepath.Join(root, "backup.tar.gz")
			if e := f.svc.Backup(context.Background(), archive); e != nil {
				t.Fatal(e)
			}
			destination := filepath.Join(root, "restored")
			if e := p.Restore(context.Background(), archive, destination, ""); e != nil {
				t.Fatal(e)
			}
			store, e := p.Open(filepath.Join(destination, "platform.db"))
			if e != nil {
				t.Fatal(e)
			}
			defer store.Close()
			svc := &p.Service{Store: store, Root: destination}
			if e = svc.VerifyIntegrity(context.Background()); e != nil {
				t.Fatal(e)
			}
			oldRequest := httptest.NewRequest("GET", "/api/v1/projects", nil)
			oldRequest.Header.Set("Authorization", "Bearer "+first.Client.Token)
			denied := httptest.NewRecorder()
			httpapi.New(svc, false).ServeHTTP(denied, oldRequest)
			if denied.Code != http.StatusUnauthorized {
				t.Fatal("backup restored a Worker credential", denied.Code)
			}
			facts := p.M{"passed": true, "mode": mode, "projectId": f.project.ID, "goalId": f.goal.ID, "taskId": task.ID, "providerRequests": calls.Load(), "charges": len(charges), "runs": len(runs), "receipts": len(receipts), "status": want, "archive": archive, "restoredDatabase": filepath.Join(destination, "platform.db"), "realModel": false}
			body, _ := json.MarshalIndent(facts, "", "  ")
			if e = os.WriteFile(filepath.Join(root, "proof.json"), append(body, '\n'), 0600); e != nil {
				t.Fatal(e)
			}
		})
	}
}

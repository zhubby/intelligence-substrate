package integrations

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	p "github.com/intelligence-substrate/platform/internal/platform"
)

func agentModelFixture(t *testing.T) (*Model, p.Actor, p.Actor, p.Resource, p.Resource, *atomic.Int32) {
	t.Helper()
	m, owner, goal, _ := modelFixture(t, true)
	command := func(a p.Actor, name string, in p.M) p.Resource {
		t.Helper()
		r, e := m.Service.Command(context.Background(), a, name, in)
		if e != nil {
			t.Fatal(name, e)
		}
		return r
	}
	plan := command(owner, "plans.propose", p.M{"goalId": goal.ID, "summary": "Analyze the approved goal", "tasks": []any{p.M{"id": "work", "title": "Analysis", "kind": "analysis", "role": "developer", "runtime": "linux", "budgetCents": 100, "timeoutSeconds": 600}}})
	command(owner, "plans.approve", p.M{"id": plan.ID})
	w := command(owner, "workers.register", p.M{"projectId": goal.ProjectID, "title": "Agent", "roles": []any{"developer"}, "runtime": "linux", "capabilities": []any{"analysis"}})
	actor := p.Actor{ID: "worker:" + w.ID, Kind: "worker", WorkerID: w.ID, ProjectID: goal.ProjectID, Roles: []string{"developer"}}
	run := command(actor, "tasks.claim", p.M{})
	calls := new(atomic.Int32)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(p.M{"choices": []any{p.M{"finish_reason": "stop", "message": p.M{"content": `{"action":"finish","summary":"Local deterministic result"}`}}}, "usage": p.M{"prompt_tokens": 1, "completion_tokens": 1}})
	}))
	t.Cleanup(server.Close)
	m.BaseURL, m.Client = server.URL, server.Client()
	return m, owner, actor, goal, run, calls
}

func agentChatInput(goal, run p.Resource) p.M {
	return p.M{"goalId": goal.ID, "runId": run.ID, "leaseToken": p.S(run.Data, "leaseToken"), "messages": []any{p.M{"role": "user", "content": "Analyze the approved requirement"}}, "max_tokens": 16}
}

func seedAgentModelCalls(t *testing.T, m *Model, goal, run p.Resource, count int, status string) {
	t.Helper()
	if e := m.Service.Store.Update(context.Background(), func(tx *p.Tx) error {
		for i := 0; i < count; i++ {
			now := time.Now().UTC().Format(time.RFC3339Nano)
			charge := p.Resource{ID: p.NewID("charge"), Kind: "charge", ProjectID: goal.ProjectID, GoalID: goal.ID, Title: "Historical deterministic call", Status: "settled", Revision: 1, CreatedAt: now, UpdatedAt: now, Data: p.M{"runId": run.ID, "reservedCents": 1, "chargedCents": 0}}
			if i == 0 {
				charge.Status = status
			}
			if e := tx.Put(charge); e != nil {
				return e
			}
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
}

func TestAgentModelAdmissionCannotBypassTaskLimits(t *testing.T) {
	for _, mode := range []string{"calls", "reserved", "uncertain", "reconciled-upward", "deadline", "plan", "capability"} {
		t.Run(mode, func(t *testing.T) {
			m, owner, actor, goal, run, calls := agentModelFixture(t)
			count, status := 1, "settled"
			switch mode {
			case "calls":
				count = 24
			case "reserved", "uncertain":
				status = mode
			}
			seedAgentModelCalls(t, m, goal, run, count, status)
			if e := m.Service.Store.Update(context.Background(), func(tx *p.Tx) error {
				r, e := tx.Get(run.ID)
				if e != nil {
					return e
				}
				if mode == "deadline" {
					r.Data["deadline"] = time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)
				}
				if mode == "reconciled-upward" {
					// A previous run's statement changes after this run was claimed.
					prior := r
					prior.ID, prior.Status, prior.Data = p.NewID("run"), "failed", p.M{"taskId": p.S(r.Data, "taskId"), "spentCents": 100, "completedAt": r.CreatedAt, "deadline": p.S(r.Data, "deadline"), "leaseUntil": r.CreatedAt}
					if e = tx.Put(prior); e != nil {
						return e
					}
				}
				if mode == "plan" {
					plan, e := tx.Get(p.S(run.Data, "planId"))
					if e != nil {
						return e
					}
					plan.Status = "superseded"
					if e = tx.Put(plan); e != nil {
						return e
					}
				}
				if mode == "capability" {
					worker, e := tx.Get(actor.WorkerID)
					if e != nil {
						return e
					}
					worker.Data["capabilities"] = []any{"build"}
					if e = tx.Put(worker); e != nil {
						return e
					}
				}
				return tx.Put(r)
			}); e != nil {
				t.Fatal(e)
			}
			if _, e := m.Chat(context.Background(), actor, agentChatInput(goal, run)); e == nil {
				t.Fatal("direct model request bypassed task admission", mode)
			}
			charges, _ := m.Service.Store.List(goal.ProjectID, "charge", goal.ID)
			if calls.Load() != 0 || len(charges) != count {
				t.Fatal("denied request reached provider or created a charge", mode, calls.Load(), len(charges))
			}
			_ = owner
		})
	}
}

func TestAgentModelConcurrentLastCallReservesOnce(t *testing.T) {
	m, _, actor, goal, run, calls := agentModelFixture(t)
	seedAgentModelCalls(t, m, goal, run, 23, "settled")
	start, results := make(chan struct{}), make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, e := m.Chat(context.Background(), actor, agentChatInput(goal, run))
			results <- e
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	passed, rejected := 0, 0
	for e := range results {
		if e == nil {
			passed++
		} else {
			rejected++
		}
	}
	charges, _ := m.Service.Store.List(goal.ProjectID, "charge", goal.ID)
	if passed != 1 || rejected != 1 || calls.Load() != 1 || len(charges) != 24 {
		t.Fatal("concurrent requests exceeded 24 total calls", passed, rejected, calls.Load(), len(charges))
	}
}

func TestPostgresAgentModelConcurrentLastCall(t *testing.T) {
	dsn := os.Getenv("IS_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("set IS_TEST_POSTGRES_URL to an isolated PostgreSQL database")
	}
	admin, e := p.Open(dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer admin.Close()
	schema := p.NewID("agent_model_test")
	if _, e = admin.DB.Exec(`CREATE SCHEMA ` + schema); e != nil {
		t.Fatal(e)
	}
	defer admin.DB.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
	u, e := url.Parse(dsn)
	if e != nil {
		t.Fatal(e)
	}
	query := u.Query()
	query.Set("search_path", schema)
	u.RawQuery = query.Encode()
	root := t.TempDir()
	if evidence := os.Getenv("IS_AGENT_ALLOWANCE_EVIDENCE_DIR"); evidence != "" {
		root = filepath.Join(evidence, "postgres-model")
		if e = os.Mkdir(root, 0700); e != nil {
			t.Fatal(e)
		}
	}
	m, _, actor, goal, run, calls := agentModelFixture(t)
	seedAgentModelCalls(t, m, goal, run, 23, "settled")
	input := filepath.Join(root, "input.tar.gz")
	if e = m.Service.Backup(context.Background(), input); e != nil {
		t.Fatal(e)
	}
	if e = p.Restore(context.Background(), input, filepath.Join(root, "imported"), u.String()); e != nil {
		t.Fatal(e)
	}
	first, e := p.Open(u.String())
	if e != nil {
		t.Fatal(e)
	}
	defer first.Close()
	second, e := p.Open(u.String())
	if e != nil {
		t.Fatal(e)
	}
	defer second.Close()
	m.Service = &p.Service{Store: first, Root: filepath.Join(root, "imported")}
	other := *m
	other.Service = &p.Service{Store: second, Root: m.Service.Root}
	start, results := make(chan struct{}), make(chan error, 2)
	var wg sync.WaitGroup
	for _, model := range []*Model{m, &other} {
		wg.Add(1)
		go func(model *Model) {
			defer wg.Done()
			<-start
			_, e := model.Chat(context.Background(), actor, agentChatInput(goal, run))
			results <- e
		}(model)
	}
	close(start)
	wg.Wait()
	close(results)
	successes, rejected := 0, 0
	for e := range results {
		if e == nil {
			successes++
		} else {
			rejected++
		}
	}
	charges, _ := first.List(goal.ProjectID, "charge", goal.ID)
	if successes != 1 || rejected != 1 || len(charges) != 24 || calls.Load() != 1 {
		t.Fatal("independent PostgreSQL gateways exceeded last-call authority", successes, rejected, len(charges), calls.Load())
	}
	archive := filepath.Join(root, "backup.tar.gz")
	if e = m.Service.Backup(context.Background(), archive); e != nil {
		t.Fatal(e)
	}
	destination := filepath.Join(root, "restored")
	if e = p.Restore(context.Background(), archive, destination, ""); e != nil {
		t.Fatal(e)
	}
	restored, e := p.Open(filepath.Join(destination, "platform.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer restored.Close()
	copyModel := *m
	copyModel.Service = &p.Service{Store: restored, Root: destination}
	if _, e = copyModel.Chat(context.Background(), actor, agentChatInput(goal, run)); e == nil {
		t.Fatal("portable restore refunded model requests")
	}
	if calls.Load() != 1 {
		t.Fatal("restored exhausted task contacted provider")
	}
	facts := p.M{"passed": true, "projectId": goal.ProjectID, "goalId": goal.ID, "runId": run.ID, "charges": 24, "providerRequests": calls.Load(), "successfulCalls": successes, "rejectedCalls": rejected, "archive": archive, "restoredDatabase": filepath.Join(destination, "platform.db"), "realModel": false}
	body, _ := json.MarshalIndent(facts, "", "  ")
	if e = os.WriteFile(filepath.Join(root, "proof.json"), append(body, '\n'), 0600); e != nil {
		t.Fatal(e)
	}
}

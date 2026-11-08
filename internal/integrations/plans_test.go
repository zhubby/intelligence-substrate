package integrations

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	p "github.com/intelligence-substrate/platform/internal/platform"
)

const planReply = `{"summary":"Implement the approved portal","risk":"Independent verification required","tasks":[{"id":"analysis","title":"Analyze the approved portal","kind":"analysis","role":"analysis","runtime":"linux"}]}`

func planInput(g p.Resource, key string) p.M {
	return p.M{"goalId": g.ID, "expectedRequirementRevision": g.Revision, "key": key}
}

func TestPlanGenerationSuccessReplayReturnsOriginalEvenWhenStale(t *testing.T) {
	m, a, g, _ := modelFixture(t, true)
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		requests, _ := m.Service.Store.List(g.ProjectID, "plan_generation", g.ID)
		charges, _ := m.Service.Store.List(g.ProjectID, "charge", g.ID)
		if len(requests) != 1 || requests[0].Status != "running" || len(charges) != 1 || charges[0].Status != "reserved" || p.S(charges[0].Data, "planRequestId") != requests[0].ID {
			t.Error("network preceded durable intent/reservation", requests, charges)
		}
		discoveryReply(w, planReply)
	}))
	defer provider.Close()
	m.BaseURL, m.Client = provider.URL, provider.Client()
	in := planInput(g, "durable")
	plan, err := m.GeneratePlan(context.Background(), a, in)
	if err != nil || plan.Status != "proposed" || p.S(plan.Data, "generationId") == "" || p.S(plan.Data, "chargeId") == "" {
		t.Fatal(plan, err)
	}
	discoveryCommand(t, m, a, "goals.revise", p.M{"id": g.ID, "reason": "New requirement revision"})
	replay, err := m.GeneratePlan(context.Background(), a, in)
	if err != nil || replay.ID != plan.ID || replay.Status != "stale" || calls.Load() != 1 {
		t.Fatal(replay, err, calls.Load())
	}
}

func TestPlanGenerationInvalidOrFailedResponseRetainsCostAndNeverResends(t *testing.T) {
	for _, mode := range []string{"provider", "json", "null", "authority", "empty", "cycle", "risk", "truncated", "usage"} {
		t.Run(mode, func(t *testing.T) {
			m, a, g, _ := modelFixture(t, true)
			var calls atomic.Int32
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				switch mode {
				case "provider":
					w.WriteHeader(503)
				case "json":
					discoveryReply(w, "invalid JSON")
				case "null":
					discoveryReply(w, "null")
				case "authority":
					discoveryReply(w, strings.TrimSuffix(planReply, "}")+`,"goalId":"other-goal","approved":true}`)
				case "empty":
					discoveryReply(w, `{"summary":"No tasks","tasks":[]}`)
				case "cycle":
					discoveryReply(w, `{"summary":"Cyclic plan","tasks":[{"id":"a","title":"Analyze","kind":"analysis","role":"analysis","runtime":"linux","dependsOn":["a"]}]}`)
				case "risk":
					discoveryReply(w, strings.Replace(planReply, `"risk":"Independent verification required"`, `"risk":42`, 1))
				case "truncated", "usage":
					finish, usage := "length", p.M{"prompt_tokens": 10, "completion_tokens": 30}
					if mode == "usage" {
						finish, usage = "stop", p.M{}
					}
					_ = json.NewEncoder(w).Encode(p.M{"choices": []any{p.M{"finish_reason": finish, "message": p.M{"content": planReply}}}, "usage": usage})
				}
			}))
			defer provider.Close()
			m.BaseURL, m.Client = provider.URL, provider.Client()
			in := planInput(g, "failed")
			_, err := m.GeneratePlan(context.Background(), a, in)
			expected := 422
			if mode == "provider" || mode == "truncated" || mode == "usage" {
				expected = 502
			}
			discoveryFault(t, err, expected)
			_, err = m.GeneratePlan(context.Background(), a, in)
			discoveryFault(t, err, 409)
			requests, _ := m.Service.Store.List(g.ProjectID, "plan_generation", g.ID)
			plans, _ := m.Service.Store.List(g.ProjectID, "plan", g.ID)
			charges, _ := m.Service.Store.List(g.ProjectID, "charge", g.ID)
			if len(requests) != 1 || requests[0].Status != "failed" || len(plans) != 0 || len(charges) != 1 || p.S(requests[0].Data, "chargeId") != charges[0].ID || p.N(charges[0].Data, "chargedCents") < 1 || calls.Load() != 1 {
				t.Fatal(requests, plans, charges, calls.Load())
			}
		})
	}
}

func TestPlanGenerationRechecksContextPolicyAndCurrentMembershipAfterInference(t *testing.T) {
	for _, mode := range []string{"knowledge", "revision", "approval", "policy", "active-plan", "revoked"} {
		t.Run(mode, func(t *testing.T) {
			m, admin, g, _ := modelFixture(t, true)
			a := admin
			member := p.Resource{ID: p.NewID("member"), Kind: "member", ProjectID: g.ProjectID, Status: "active", Revision: 1, Data: p.M{"identityId": "planner-person", "roles": []string{"architect"}}}
			if mode == "revoked" {
				a = p.Actor{ID: "planner-person", Kind: "human"}
				if err := m.Service.Store.Update(context.Background(), func(tx *p.Tx) error { return tx.Put(member) }); err != nil {
					t.Fatal(err)
				}
			}
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch mode {
				case "knowledge":
					discoveryCommand(t, m, admin, "knowledge.create", p.M{"projectId": g.ProjectID, "title": "New decision", "content": "Updated business constraints", "source": "Human review"})
				case "revision":
					discoveryCommand(t, m, admin, "goals.revise", p.M{"id": g.ID, "reason": "New baseline"})
				case "approval":
					discoveryCommand(t, m, admin, "goals.approve", p.M{"id": g.ID})
				case "policy":
					discoveryCommand(t, m, admin, "projects.policy", p.M{"id": g.ProjectID, "allowExternalModels": false})
				case "active-plan":
					var content p.M
					_ = json.Unmarshal([]byte(planReply), &content)
					content["goalId"] = g.ID
					plan := discoveryCommand(t, m, admin, "plans.propose", content)
					discoveryCommand(t, m, admin, "plans.approve", p.M{"id": plan.ID})
				case "revoked":
					member.Status = "revoked"
					if err := m.Service.Store.Update(context.Background(), func(tx *p.Tx) error { return tx.Put(member) }); err != nil {
						t.Error(err)
					}
				}
				discoveryReply(w, planReply)
			}))
			defer provider.Close()
			m.BaseURL, m.Client = provider.URL, provider.Client()
			_, err := m.GeneratePlan(context.Background(), a, planInput(g, "context"))
			expected, plansExpected := 409, 0
			if mode == "revoked" {
				expected = 403
			}
			if mode == "active-plan" {
				plansExpected = 1
			}
			discoveryFault(t, err, expected)
			requests, _ := m.Service.Store.List(g.ProjectID, "plan_generation", g.ID)
			charges, _ := m.Service.Store.List(g.ProjectID, "charge", g.ID)
			plans, _ := m.Service.Store.List(g.ProjectID, "plan", g.ID)
			if len(requests) != 1 || requests[0].Status != "failed" || len(charges) != 1 || charges[0].Status != "settled" || len(plans) != plansExpected {
				t.Fatal(requests, charges, plans)
			}
		})
	}
}

func TestPlanGenerationConcurrentReplayMakesOneProviderAttempt(t *testing.T) {
	m, a, g, _ := modelFixture(t, true)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		close(entered)
		<-release
		discoveryReply(w, planReply)
	}))
	defer provider.Close()
	defer unblock()
	m.BaseURL, m.Client = provider.URL, provider.Client()
	in := planInput(g, "concurrent")
	done := make(chan error, 1)
	go func() { _, err := m.GeneratePlan(context.Background(), a, in); done <- err }()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("provider did not receive request")
	}
	_, err := m.GeneratePlan(context.Background(), a, in)
	discoveryFault(t, err, 409)
	unblock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("generation did not finish")
	}
	if calls.Load() != 1 {
		t.Fatal("replay reserved a second inference", calls.Load())
	}
}

func TestPlanGenerationAdmissionAndAuditFailureDoNotLeakProposals(t *testing.T) {
	for _, mode := range []string{"policy", "budget", "worker", "start-audit", "complete-audit"} {
		t.Run(mode, func(t *testing.T) {
			m, a, g, _ := modelFixture(t, true)
			var calls atomic.Int32
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); discoveryReply(w, planReply) }))
			defer provider.Close()
			m.BaseURL, m.Client = provider.URL, provider.Client()
			switch mode {
			case "policy":
				discoveryCommand(t, m, a, "projects.policy", p.M{"id": g.ProjectID, "allowExternalModels": false})
			case "budget":
				m.InputUSD, m.OutputUSD = 50000, 50000
			case "worker":
				a.Kind = "worker"
			case "start-audit", "complete-audit":
				stage := strings.TrimSuffix(mode, "-audit")
				if _, err := m.Service.Store.DB.Exec(`CREATE TRIGGER fail_plan_generation BEFORE INSERT ON audit WHEN json_extract(NEW.body,'$.action')='plans.generate.` + stage + `' BEGIN SELECT RAISE(ABORT,'synthetic audit failure'); END`); err != nil {
					t.Fatal(err)
				}
			}
			in := planInput(g, "denied-or-audit")
			if _, err := m.GeneratePlan(context.Background(), a, in); err == nil {
				t.Fatal("unauthorized/unaudited generation succeeded")
			}
			plans, _ := m.Service.Store.List(g.ProjectID, "plan", g.ID)
			charges, _ := m.Service.Store.List(g.ProjectID, "charge", g.ID)
			if len(plans) != 0 {
				t.Fatal("proposal escaped failed transaction", plans)
			}
			if mode == "complete-audit" {
				if calls.Load() != 1 || len(charges) != 1 || charges[0].Status != "settled" {
					t.Fatal(calls.Load(), charges)
				}
				_, err := m.GeneratePlan(context.Background(), a, in)
				discoveryFault(t, err, 409)
			} else if calls.Load() != 0 || len(charges) != 0 {
				t.Fatal("blocked inference reached provider", calls.Load(), charges)
			}
		})
	}
}

func TestPlanGenerationAdmissionRejectsChangedContextAndExistingCharge(t *testing.T) {
	for _, mode := range []string{"knowledge", "reserved-charge"} {
		t.Run(mode, func(t *testing.T) {
			m, a, g, calls := modelFixture(t, true)
			request, started, err := m.Service.BeginPlanGeneration(context.Background(), a, planInput(g, "admission"))
			if err != nil || !started {
				t.Fatal(request, err)
			}
			if mode == "knowledge" {
				discoveryCommand(t, m, a, "knowledge.create", p.M{"projectId": g.ProjectID, "title": "Changed before network", "content": "Updated input", "source": "Human"})
			} else if err := m.Service.Store.Update(context.Background(), func(tx *p.Tx) error {
				return tx.Put(p.Resource{ID: p.NewID("charge"), Kind: "charge", ProjectID: g.ProjectID, GoalID: g.ID, Status: "reserved", Revision: 1, Data: p.M{"planRequestId": request.ID, "actorId": a.ID, "reservedCents": 1}})
			}); err != nil {
				t.Fatal(err)
			}
			_, err = m.chat(context.Background(), a, p.M{"goalId": g.ID, "messages": []any{p.M{"role": "user", "content": "plan"}}}, request.ID)
			discoveryFault(t, err, 409)
			if *calls != 0 {
				t.Fatal("admission rejection reached provider")
			}
			charges, _ := m.Service.Store.List(g.ProjectID, "charge", g.ID)
			want := 0
			if mode == "reserved-charge" {
				want = 1
			}
			if len(charges) != want {
				t.Fatal("admission created another reservation", charges)
			}
		})
	}
}

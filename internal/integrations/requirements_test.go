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

const requirementsReply = `{"outcome":"Customers book and manage their own appointments","brief":"Customers select a service and time; only the owner cancels. Payments are excluded.","criteria":[{"text":"A customer cannot cancel another customer's booking","method":"automated"}]}`

func TestRequirementDraftGenerationIsPrivateDurableAndBilledOnce(t *testing.T) {
	m, a, g, in := discoveryFixture(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body p.M
		_ = json.NewDecoder(r.Body).Decode(&body)
		if !strings.Contains(p.S(p.Obj(p.Items(body["messages"])[1]), "content"), "Build a portal") {
			t.Error("missing business goal")
		}
		discoveryReply(w, requirementsReply)
	}))
	defer server.Close()
	m.BaseURL, m.Client = server.URL, server.Client()
	draft, err := m.GenerateRequirements(context.Background(), a, in)
	if err != nil || draft.Status != "proposed" || p.S(draft.Data, "origin") != "model" {
		t.Fatal(draft, err)
	}
	requests, _ := m.Service.Store.List(g.ProjectID, "requirement_generation", g.ID)
	charges, _ := m.Service.Store.List(g.ProjectID, "charge", g.ID)
	if len(requests) != 1 || requests[0].Status != "succeeded" || p.S(requests[0].Data, "resultId") != draft.ID || len(charges) != 1 || p.S(charges[0].Data, "requirementRequestId") != requests[0].ID || p.S(draft.Data, "chargeId") != charges[0].ID {
		t.Fatal(requests, charges, draft)
	}
	if replay, err := m.GenerateRequirements(context.Background(), a, in); err != nil || replay.ID != draft.ID || calls.Load() != 1 {
		t.Fatal(replay, err, calls.Load())
	}
	_, err = m.Chat(context.Background(), a, p.M{"goalId": g.ID, "requirementRequestId": requests[0].ID, "allowDraft": true, "messages": []any{p.M{"role": "user", "content": "bypass baseline"}}})
	discoveryFault(t, err, 409)
	current, _ := m.Service.Store.Get(g.ID)
	if current.Revision != g.Revision || p.N(current.Data, "approvedRevision") != 0 || calls.Load() != 1 {
		t.Fatal(current, calls.Load())
	}
}

func TestRequirementDraftGenerationFailureRetainsCostAndNeverResends(t *testing.T) {
	for _, mode := range []string{"provider", "json", "extra", "criteria", "duplicate", "truncated"} {
		t.Run(mode, func(t *testing.T) {
			m, a, g, in := discoveryFixture(t)
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				switch mode {
				case "provider":
					w.WriteHeader(503)
				case "json":
					discoveryReply(w, "invalid JSON")
				case "extra":
					discoveryReply(w, strings.TrimSuffix(requirementsReply, "}")+`,"approve":true}`)
				case "criteria":
					discoveryReply(w, `{"outcome":"Goal","brief":"Scope","criteria":[]}`)
				case "duplicate":
					discoveryReply(w, `{"outcome":"Goal","brief":"Scope","criteria":[{"text":"same","method":"automated"},{"text":" SAME ","method":"manual"}]}`)
				case "truncated":
					_ = json.NewEncoder(w).Encode(p.M{"choices": []any{p.M{"finish_reason": "length", "message": p.M{"content": requirementsReply}}}, "usage": p.M{"prompt_tokens": 10, "completion_tokens": 30}})
				}
			}))
			defer server.Close()
			m.BaseURL, m.Client = server.URL, server.Client()
			_, err := m.GenerateRequirements(context.Background(), a, in)
			expected := 422
			if mode == "provider" || mode == "truncated" {
				expected = 502
			}
			discoveryFault(t, err, expected)
			_, err = m.GenerateRequirements(context.Background(), a, in)
			discoveryFault(t, err, 409)
			requests, _ := m.Service.Store.List(g.ProjectID, "requirement_generation", g.ID)
			charges, _ := m.Service.Store.List(g.ProjectID, "charge", g.ID)
			drafts, _ := m.Service.Store.List(g.ProjectID, "requirement_proposal", g.ID)
			if len(requests) != 1 || requests[0].Status != "failed" || len(charges) != 1 || len(drafts) != 0 || calls.Load() != 1 {
				t.Fatal(requests, charges, drafts, calls.Load())
			}
		})
	}
}

func TestRequirementDraftGenerationRechecksHumanContextAfterProvider(t *testing.T) {
	for _, mode := range []string{"revision", "question", "knowledge", "approval", "revoked"} {
		t.Run(mode, func(t *testing.T) {
			m, admin, g, in := discoveryFixture(t)
			a := admin
			member := p.Resource{ID: p.NewID("member"), Kind: "member", ProjectID: g.ProjectID, Status: "active", Revision: 1, Data: p.M{"identityId": "product-person", "roles": []string{"product"}}}
			if mode == "revoked" {
				a = p.Actor{ID: "product-person", Kind: "human"}
				if err := m.Service.Store.Update(context.Background(), func(tx *p.Tx) error { return tx.Put(member) }); err != nil {
					t.Fatal(err)
				}
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch mode {
				case "revision":
					discoveryCommand(t, m, admin, "goals.revise", p.M{"id": g.ID, "reason": "Change scope during generation"})
				case "question":
					discoveryCommand(t, m, admin, "clarifications.propose", p.M{"goalId": g.ID, "expectedRequirementRevision": g.Revision, "key": "new", "questions": []any{p.M{"question": "Who pays?"}}})
				case "knowledge":
					discoveryCommand(t, m, admin, "knowledge.create", p.M{"projectId": g.ProjectID, "title": "New decision", "content": "Updated policy", "source": "Business"})
				case "approval":
					discoveryCommand(t, m, admin, "goals.approve", p.M{"id": g.ID})
				case "revoked":
					member.Status = "revoked"
					if err := m.Service.Store.Update(context.Background(), func(tx *p.Tx) error { return tx.Put(member) }); err != nil {
						t.Error(err)
					}
				}
				discoveryReply(w, requirementsReply)
			}))
			defer server.Close()
			m.BaseURL, m.Client = server.URL, server.Client()
			_, err := m.GenerateRequirements(context.Background(), a, in)
			expected := 409
			if mode == "revoked" {
				expected = 403
			}
			discoveryFault(t, err, expected)
			drafts, _ := m.Service.Store.List(g.ProjectID, "requirement_proposal", g.ID)
			charges, _ := m.Service.Store.List(g.ProjectID, "charge", g.ID)
			requests, _ := m.Service.Store.List(g.ProjectID, "requirement_generation", g.ID)
			if len(drafts) != 0 || len(charges) != 1 || charges[0].Status != "settled" || len(requests) != 1 || requests[0].Status != "failed" {
				t.Fatal(drafts, charges, requests)
			}
		})
	}
}

func TestRequirementDraftGenerationPolicyBudgetAndOpenQuestionsBlockNetwork(t *testing.T) {
	for _, mode := range []string{"policy", "budget", "agent", "question", "running"} {
		t.Run(mode, func(t *testing.T) {
			m, a, g, in := discoveryFixture(t)
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); discoveryReply(w, requirementsReply) }))
			defer server.Close()
			m.BaseURL, m.Client = server.URL, server.Client()
			expected := 403
			switch mode {
			case "policy":
				discoveryCommand(t, m, a, "projects.policy", p.M{"id": g.ProjectID, "allowExternalModels": false})
			case "budget":
				m.InputUSD, m.OutputUSD, expected = 50000, 50000, 409
			case "agent":
				a.Kind = "agent"
			case "question":
				discoveryCommand(t, m, a, "clarifications.propose", p.M{"goalId": g.ID, "expectedRequirementRevision": g.Revision, "key": "new", "questions": []any{p.M{"question": "Who pays?"}}})
				expected = 409
			case "running":
				_, started, err := m.Service.BeginRequirementGeneration(context.Background(), a, in)
				if err != nil || !started {
					t.Fatal(started, err)
				}
				expected = 409
			}
			_, err := m.GenerateRequirements(context.Background(), a, in)
			discoveryFault(t, err, expected)
			if calls.Load() != 0 {
				t.Fatal("unauthorized or duplicate model call")
			}
		})
	}
}

func TestRequirementDraftPlanningReadsHumanEditedBriefAndAdoptedAnswers(t *testing.T) {
	m, a, g, in := discoveryFixture(t)
	q := discoveryCommand(t, m, a, "clarifications.propose", p.M{"goalId": g.ID, "expectedRequirementRevision": g.Revision, "key": "ask", "questions": []any{p.M{"question": "Who may cancel?"}}})
	discoveryCommand(t, m, a, "clarifications.answer", p.M{"id": q.ID, "expectedRequirementRevision": g.Revision, "key": "answer", "questionId": p.Obj(p.Items(q.Data["questions"])[0])["id"], "answer": "HUMAN-OWNER-ONLY"})
	var observed string
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			discoveryReply(w, requirementsReply)
			return
		}
		var body p.M
		_ = json.NewDecoder(r.Body).Decode(&body)
		b, _ := json.Marshal(body["messages"])
		observed = string(b)
		discoveryReply(w, `{"summary":"Implement adopted requirements","tasks":[{"id":"analysis","title":"Inspect adopted scope","kind":"analysis","role":"analysis","runtime":"linux"}]}`)
	}))
	defer server.Close()
	m.BaseURL, m.Client = server.URL, server.Client()
	draft, err := m.GenerateRequirements(context.Background(), a, in)
	if err != nil {
		t.Fatal(err)
	}
	content := p.Obj(draft.Data["content"])
	content["brief"] = "HUMAN-REVIEWED-BRIEF: Payments excluded."
	for _, v := range p.Items(content["criteria"]) {
		delete(p.Obj(v), "id")
	}
	current := discoveryCommand(t, m, a, "requirements.apply", p.M{"id": draft.ID, "expectedRequirementRevision": g.Revision, "key": "adopt", "reason": "Reviewed scope", "content": content})
	discoveryCommand(t, m, a, "goals.approve", p.M{"id": g.ID, "expectedRequirementRevision": current.Revision})
	if _, err := m.GeneratePlan(context.Background(), a, planInput(current, "adopted-brief")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(observed, "HUMAN-REVIEWED-BRIEF") || !strings.Contains(observed, "HUMAN-OWNER-ONLY") {
		t.Fatal("planner missed human adoption", observed)
	}
}

func TestRequirementDraftGenerationConcurrentReplayUsesOneProviderAttempt(t *testing.T) {
	m, a, _, in := discoveryFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		close(entered)
		<-release
		discoveryReply(w, requirementsReply)
	}))
	defer server.Close()
	defer unblock()
	m.BaseURL, m.Client = server.URL, server.Client()
	done := make(chan error, 1)
	go func() { _, err := m.GenerateRequirements(context.Background(), a, in); done <- err }()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("provider did not receive request")
	}
	_, err := m.GenerateRequirements(context.Background(), a, in)
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
		t.Fatal("request replay reached provider", calls.Load())
	}
}

func TestRequirementDraftGenerationAuditFailuresDoNotLeakDraftOrRepeatCost(t *testing.T) {
	for _, stage := range []string{"start", "complete"} {
		t.Run(stage, func(t *testing.T) {
			m, a, g, in := discoveryFixture(t)
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); discoveryReply(w, requirementsReply) }))
			defer server.Close()
			m.BaseURL, m.Client = server.URL, server.Client()
			if _, err := m.Service.Store.DB.Exec(`CREATE TRIGGER fail_requirement_generation BEFORE INSERT ON audit WHEN json_extract(NEW.body,'$.action')='requirements.generate.` + stage + `' BEGIN SELECT RAISE(ABORT,'synthetic audit failure'); END`); err != nil {
				t.Fatal(err)
			}
			if _, err := m.GenerateRequirements(context.Background(), a, in); err == nil {
				t.Fatal("unaudited generation succeeded")
			}
			requests, _ := m.Service.Store.List(g.ProjectID, "requirement_generation", g.ID)
			drafts, _ := m.Service.Store.List(g.ProjectID, "requirement_proposal", g.ID)
			charges, _ := m.Service.Store.List(g.ProjectID, "charge", g.ID)
			if len(drafts) != 0 {
				t.Fatal("draft escaped failed completion audit", drafts)
			}
			if stage == "start" {
				if len(requests) != 0 || len(charges) != 0 || calls.Load() != 0 {
					t.Fatal(requests, charges, calls.Load())
				}
			} else {
				if len(requests) != 1 || requests[0].Status != "failed" || len(charges) != 1 || charges[0].Status != "settled" || calls.Load() != 1 {
					t.Fatal(requests, charges, calls.Load())
				}
				_, err := m.GenerateRequirements(context.Background(), a, in)
				discoveryFault(t, err, 409)
				if calls.Load() != 1 {
					t.Fatal("retried failed completion")
				}
			}
		})
	}
}

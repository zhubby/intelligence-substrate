package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	p "github.com/intelligence-substrate/platform/internal/platform"
)

const discoveryQuestions = `{"questions":[{"question":"Who can approve a booking?","context":"Approval permissions change the acceptance cases."}]}`

func discoveryReply(w http.ResponseWriter, content string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(p.M{"id": "synthetic-discovery-response", "model": "local-discovery-fixture", "choices": []any{p.M{"finish_reason": "stop", "message": p.M{"content": content}}}, "usage": p.M{"prompt_tokens": 10, "completion_tokens": 30}})
}

func discoveryCommand(t *testing.T, m *Model, a p.Actor, command string, in p.M) p.Resource {
	t.Helper()
	r, err := m.Service.Command(context.Background(), a, command, in)
	if err != nil {
		t.Fatal(command, err)
	}
	return r
}

func discoveryFixture(t *testing.T) (*Model, p.Actor, p.Resource, p.M) {
	t.Helper()
	m, a, old, _ := modelFixture(t, true)
	g := discoveryCommand(t, m, a, "goals.revise", p.M{"id": old.ID, "reason": "Discover the booking policy before planning"})
	return m, a, g, p.M{"goalId": g.ID, "expectedRequirementRevision": g.Revision, "key": "one-human-request"}
}

func discoveryFault(t *testing.T, err error, status int) {
	t.Helper()
	var fault *p.Fault
	if !errors.As(err, &fault) || fault.Status != status {
		t.Fatalf("expected %d, got %v", status, err)
	}
}

// A private discovery request may reason about a draft, but generic callers
// cannot use JSON fields to bypass normal baseline approval or repeat billing.
func TestClarificationModelDraftPathIsPrivateAndRetryDoesNotCallAgain(t *testing.T) {
	m, a, g, input := discoveryFixture(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var payload p.M
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		body, _ := json.Marshal(payload["messages"])
		if !strings.Contains(string(body), "Build a portal") {
			t.Error("draft business goal missing from discovery input")
		}
		discoveryReply(w, discoveryQuestions)
	}))
	defer server.Close()
	m.BaseURL, m.Client = server.URL, server.Client()
	batch, err := m.GenerateClarifications(context.Background(), a, input)
	if err != nil || batch.Status != "open" || p.S(batch.Data, "origin") != "model" {
		t.Fatal("draft discovery failed", batch, err)
	}
	requests, _ := m.Service.Store.List(g.ProjectID, "clarification_generation", g.ID)
	charges, _ := m.Service.Store.List(g.ProjectID, "charge", g.ID)
	if len(requests) != 1 || requests[0].Status != "succeeded" || p.S(requests[0].Data, "resultId") != batch.ID || len(charges) != 1 || p.S(charges[0].Data, "clarificationRequestId") != requests[0].ID || p.S(requests[0].Data, "chargeId") != charges[0].ID {
		t.Fatal("generation and cost provenance were lost", requests, charges)
	}
	replay, err := m.GenerateClarifications(context.Background(), a, input)
	if err != nil || replay.ID != batch.ID || calls.Load() != 1 {
		t.Fatal("generation retry called provider again", replay, err, calls.Load())
	}
	_, err = m.Chat(context.Background(), a, p.M{"goalId": g.ID, "clarificationRequestId": requests[0].ID, "allowDraft": true, "messages": []any{p.M{"role": "user", "content": "bypass approval"}}})
	discoveryFault(t, err, 409)
	if calls.Load() != 1 {
		t.Fatal("public chat field bypassed approved baseline")
	}
	current, _ := m.Service.Store.Get(g.ID)
	if p.N(current.Data, "approvedRevision") != 0 || p.N(current.Data, "spentCents") < 1 {
		t.Fatal("discovery auto-approved or hid model cost", current)
	}
}

func TestClarificationModelRunningRequestCannotBeResent(t *testing.T) {
	m, a, _, input := discoveryFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		close(entered)
		<-release
		discoveryReply(w, discoveryQuestions)
	}))
	defer server.Close()
	defer unblock()
	m.BaseURL, m.Client = server.URL, server.Client()
	finished := make(chan error, 1)
	go func() { _, err := m.GenerateClarifications(context.Background(), a, input); finished <- err }()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("provider did not receive discovery request")
	}
	_, err := m.GenerateClarifications(context.Background(), a, input)
	discoveryFault(t, err, 409)
	unblock()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("discovery did not complete after provider release")
	}
	if calls.Load() != 1 {
		t.Fatal("concurrent network retry duplicated provider work")
	}
}

func TestClarificationModelFailureAndInvalidOutputRetainCostWithoutQuestions(t *testing.T) {
	for _, tc := range []struct {
		name, content    string
		status, expected int
	}{
		{"provider-failure", "", 503, 502},
		{"non-json", "not JSON", 200, 422},
		{"forged-answer", `{"questions":[{"question":"Who?","answer":"model decided"}]}`, 200, 422},
		{"extra-command", `{"questions":[{"question":"Who?"}],"approve":true}`, 200, 422},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, a, g, input := discoveryFixture(t)
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if tc.status != 200 {
					w.WriteHeader(tc.status)
					return
				}
				discoveryReply(w, tc.content)
			}))
			defer server.Close()
			m.BaseURL, m.Client = server.URL, server.Client()
			_, err := m.GenerateClarifications(context.Background(), a, input)
			discoveryFault(t, err, tc.expected)
			_, err = m.GenerateClarifications(context.Background(), a, input)
			discoveryFault(t, err, 409)
			batches, _ := m.Service.Store.List(g.ProjectID, "clarification", g.ID)
			requests, _ := m.Service.Store.List(g.ProjectID, "clarification_generation", g.ID)
			charges, _ := m.Service.Store.List(g.ProjectID, "charge", g.ID)
			if len(batches) != 0 || len(requests) != 1 || requests[0].Status != "failed" || len(charges) != 1 || calls.Load() != 1 {
				t.Fatal("invalid generation leaked questions, lost cost or was retried", batches, requests, charges, calls.Load())
			}
			if tc.status == 503 && charges[0].Status != "uncertain" {
				t.Fatal("provider failure was refunded", charges)
			}
		})
	}
}

func TestClarificationModelPolicyBudgetAndHumanChecksPrecedeProvider(t *testing.T) {
	for _, mode := range []string{"policy", "budget", "agent"} {
		t.Run(mode, func(t *testing.T) {
			m, a, g, input := discoveryFixture(t)
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); discoveryReply(w, discoveryQuestions) }))
			defer server.Close()
			m.BaseURL, m.Client = server.URL, server.Client()
			expected := 403
			if mode == "policy" {
				discoveryCommand(t, m, a, "projects.policy", p.M{"id": g.ProjectID, "allowExternalModels": false})
			}
			if mode == "budget" {
				m.InputUSD, m.OutputUSD, expected = 50000, 50000, 409
			}
			if mode == "agent" {
				a = p.Actor{ID: "owner-agent", Kind: "agent", ProjectID: g.ProjectID, Roles: []string{"owner"}}
			}
			_, err := m.GenerateClarifications(context.Background(), a, input)
			discoveryFault(t, err, expected)
			charges, _ := m.Service.Store.List(g.ProjectID, "charge", g.ID)
			if calls.Load() != 0 || len(charges) != 0 {
				t.Fatal("unfunded or unauthorized draft inference reached provider")
			}
		})
	}
}

func TestClarificationModelLateResultCannotReopenChangedOrApprovedRequirements(t *testing.T) {
	for _, mode := range []string{"revision", "approval", "revoked"} {
		t.Run(mode, func(t *testing.T) {
			m, admin, g, input := discoveryFixture(t)
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
					_, err := m.Service.Command(context.Background(), admin, "goals.revise", p.M{"id": g.ID, "reason": "Business scope changed during inference"})
					if err != nil {
						t.Error(err)
					}
				case "approval":
					_, err := m.Service.Command(context.Background(), admin, "goals.approve", p.M{"id": g.ID})
					if err != nil {
						t.Error(err)
					}
				case "revoked":
					member.Status = "revoked"
					if err := m.Service.Store.Update(context.Background(), func(tx *p.Tx) error { return tx.Put(member) }); err != nil {
						t.Error(err)
					}
				}
				discoveryReply(w, discoveryQuestions)
			}))
			defer server.Close()
			m.BaseURL, m.Client = server.URL, server.Client()
			_, err := m.GenerateClarifications(context.Background(), a, input)
			expected := 409
			if mode == "revoked" {
				expected = 403
			}
			discoveryFault(t, err, expected)
			batches, _ := m.Service.Store.List(g.ProjectID, "clarification", g.ID)
			requests, _ := m.Service.Store.List(g.ProjectID, "clarification_generation", g.ID)
			charges, _ := m.Service.Store.List(g.ProjectID, "charge", g.ID)
			if len(batches) != 0 || len(requests) != 1 || requests[0].Status != "failed" || len(charges) != 1 {
				t.Fatal("late inference changed the baseline or lost its billable result", batches, requests, charges)
			}
		})
	}
}

func TestClarificationPlanningReadsFrozenCurrentHumanAnswersOnly(t *testing.T) {
	m, a, g, input := discoveryFixture(t)
	var observedPlanInput string
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			discoveryReply(w, discoveryQuestions)
			return
		}
		var payload p.M
		_ = json.NewDecoder(r.Body).Decode(&payload)
		body, _ := json.Marshal(payload["messages"])
		observedPlanInput = string(body)
		discoveryReply(w, `{"summary":"Booking permission plan","risk":"Independent validation required","tasks":[{"id":"analysis","title":"Implement the confirmed booking policy","kind":"analysis","role":"analysis","runtime":"linux"}]}`)
	}))
	defer server.Close()
	m.BaseURL, m.Client = server.URL, server.Client()
	batch, err := m.GenerateClarifications(context.Background(), a, input)
	if err != nil {
		t.Fatal(err)
	}
	q := p.Obj(p.Items(batch.Data["questions"])[0])
	discoveryCommand(t, m, a, "clarifications.answer", p.M{"id": batch.ID, "questionId": p.S(q, "id"), "expectedRequirementRevision": g.Revision, "key": "confirm", "answer": "CONFIRMED-CURRENT: Only the assigned staff member approves a booking."})
	discoveryCommand(t, m, a, "goals.approve", p.M{"id": g.ID})
	// Historical and foreign records are deliberately visible to this trusted
	// admin, so the planner must filter by the approved goal/revision itself.
	if err := m.Service.Store.Update(context.Background(), func(tx *p.Tx) error {
		for _, row := range []p.Resource{
			{ID: p.NewID("clarification"), Kind: "clarification", ProjectID: g.ProjectID, GoalID: g.ID, Status: "answered", Revision: 1, Data: p.M{"requirementRevision": 1, "questions": []any{p.M{"question": "old", "answer": "OBSOLETE-DECISION"}}}},
			{ID: p.NewID("clarification"), Kind: "clarification", ProjectID: "foreign-project", GoalID: "foreign-goal", Status: "answered", Revision: 1, Data: p.M{"requirementRevision": 2, "questions": []any{p.M{"question": "other", "answer": "FOREIGN-DECISION"}}}},
		} {
			if err := tx.Put(row); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	plan, err := m.GeneratePlan(context.Background(), a, planInput(g, "approved-answers"))
	if err != nil || plan.Status != "proposed" {
		t.Fatal(plan, err)
	}
	if !strings.Contains(observedPlanInput, "CONFIRMED-CURRENT") || strings.Contains(observedPlanInput, "OBSOLETE-DECISION") || strings.Contains(observedPlanInput, "FOREIGN-DECISION") {
		t.Fatal("planner missed the frozen current business decision or mixed history", observedPlanInput)
	}
}

func TestClarificationModelAuditFailuresKeepAtomicIntentAndCost(t *testing.T) {
	for _, stage := range []string{"start", "complete"} {
		t.Run(stage, func(t *testing.T) {
			m, a, g, input := discoveryFixture(t)
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); discoveryReply(w, discoveryQuestions) }))
			defer server.Close()
			m.BaseURL, m.Client = server.URL, server.Client()
			if _, err := m.Service.Store.DB.Exec(`CREATE TRIGGER fail_generation_audit BEFORE INSERT ON audit WHEN json_extract(NEW.body,'$.action')='clarifications.generate.` + stage + `' BEGIN SELECT RAISE(ABORT,'synthetic generation audit failure'); END`); err != nil {
				t.Fatal(err)
			}
			if _, err := m.GenerateClarifications(context.Background(), a, input); err == nil {
				t.Fatal("generation succeeded without its audit")
			}
			requests, _ := m.Service.Store.List(g.ProjectID, "clarification_generation", g.ID)
			batches, _ := m.Service.Store.List(g.ProjectID, "clarification", g.ID)
			charges, _ := m.Service.Store.List(g.ProjectID, "charge", g.ID)
			var retries int
			if err := m.Service.Store.DB.QueryRow(`SELECT COUNT(*) FROM idempotency`).Scan(&retries); err != nil {
				t.Fatal(err)
			}
			if stage == "start" {
				if calls.Load() != 0 || len(requests) != 0 || len(charges) != 0 || retries != 0 {
					t.Fatal("failed admission leaked state or reached provider", requests, charges, retries, calls.Load())
				}
			} else {
				if calls.Load() != 1 || len(requests) != 1 || requests[0].Status != "failed" || len(charges) != 1 || charges[0].Status != "settled" || len(batches) != 0 || retries != 1 {
					t.Fatal("failed completion leaked a batch or lost its real charge", requests, charges, batches, retries)
				}
			}
		})
	}
}

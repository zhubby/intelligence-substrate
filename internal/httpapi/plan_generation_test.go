package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/intelligence-substrate/platform/internal/integrations"
	p "github.com/intelligence-substrate/platform/internal/platform"
)

// Losing a response must not bill inference twice or create another proposal.
func TestPlanGenerationHTTPReplayDoesNotRepeatProviderOrProposal(t *testing.T) {
	h := setup(t)
	cookie, csrf := login(t, h)
	identity, _ := h.Auth.IdentityByEmail("owner@example.com")
	a := identityActor(identity)
	project := authorityCommand(t, h, a, "projects.create", p.M{"title": "Durable planning"})
	g := httpCostGoal(t, h, a, project)
	authorityCommand(t, h, a, "goals.approve", p.M{"id": g.ID})
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_ = json.NewEncoder(w).Encode(p.M{"choices": []any{p.M{"finish_reason": "stop", "message": p.M{"content": `{"summary":"Implement traceable costs","tasks":[{"id":"analysis","title":"Analyze accounting scope","kind":"analysis","role":"analysis","runtime":"linux"}]}`}}}, "usage": p.M{"prompt_tokens": 10, "completion_tokens": 30}})
	}))
	defer provider.Close()
	model := &integrations.Model{Service: h.Service, BaseURL: provider.URL, Name: "local-plan", Client: provider.Client(), InputUSD: 1, OutputUSD: 1}
	h.GeneratePlan = model.GeneratePlan
	in := p.M{"goalId": g.ID, "expectedRequirementRevision": 1, "key": "lost-response"}
	var first p.Resource
	for i := 0; i < 2; i++ {
		response := req(h, "POST", "/api/v1/plans/generate", in, cookie, csrf)
		if response.Code != 200 {
			t.Fatal(response.Code, response.Body.String())
		}
		var result p.Resource
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = result
		} else if result.ID != first.ID {
			t.Fatalf("replay created a second proposal: first=%s replay=%s providerCalls=%d", first.ID, result.ID, calls.Load())
		}
	}
	if calls.Load() != 1 {
		t.Fatal("replay repeated paid inference", calls.Load())
	}
	charges, _ := h.Service.Store.List(project.ID, "charge", g.ID)
	requests, _ := h.Service.Store.List(project.ID, "plan_generation", g.ID)
	if len(charges) != 1 || len(requests) != 1 || requests[0].Status != "succeeded" || p.S(charges[0].Data, "planRequestId") != requests[0].ID {
		t.Fatal(charges, requests)
	}
	if response := req(h, "POST", "/api/v1/plans/generate", in, cookie, ""); response.Code != 403 {
		t.Fatal("CSRF protection lost", response.Code)
	}
	in["expectedRequirementRevision"] = 2
	if response := req(h, "POST", "/api/v1/plans/generate", in, cookie, csrf); response.Code != 409 || calls.Load() != 1 {
		t.Fatal("changed payload reused a key", response.Code, calls.Load())
	}
}

func TestPlanGenerationHTTPAgentMayProposeButCannotApproveOrForgeLifecycle(t *testing.T) {
	h := setup(t)
	cookie, csrf := login(t, h)
	identity, _ := h.Auth.IdentityByEmail("owner@example.com")
	owner := identityActor(identity)
	project := authorityCommand(t, h, owner, "projects.create", p.M{"title": "Authorized Agent planning"})
	g := httpCostGoal(t, h, owner, project)
	authorityCommand(t, h, owner, "goals.approve", p.M{"id": g.ID})
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_ = json.NewEncoder(w).Encode(p.M{"choices": []any{p.M{"finish_reason": "stop", "message": p.M{"content": `{"summary":"Agent-authored plan","tasks":[{"id":"analysis","title":"Analyze accounting scope","kind":"analysis","role":"analysis","runtime":"linux"}]}`}}}, "usage": p.M{"prompt_tokens": 10, "completion_tokens": 30}})
	}))
	defer provider.Close()
	model := &integrations.Model{Service: h.Service, BaseURL: provider.URL, Name: "local-plan", Client: provider.Client(), InputUSD: 1, OutputUSD: 1}
	h.GeneratePlan, h.ModelCall = model.GeneratePlan, model.Chat
	token, _, err := h.Auth.IssueToken(context.Background(), owner, p.M{"projectId": project.ID, "name": "Planning Agent"})
	if err != nil {
		t.Fatal(err)
	}
	call := func(path string, input p.M) *httptest.ResponseRecorder {
		body, _ := json.Marshal(input)
		r := httptest.NewRequest("POST", path, strings.NewReader(string(body)))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	in := p.M{"goalId": g.ID, "expectedRequirementRevision": 1, "key": "agent-request"}
	response := call("/api/v1/plans/generate", in)
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	var plan p.Resource
	_ = json.Unmarshal(response.Body.Bytes(), &plan)
	if response = call("/api/v1/commands/plans.approve", p.M{"id": plan.ID}); response.Code != 403 {
		t.Fatal("agent approved its own plan", response.Code, response.Body.String())
	}
	for _, command := range []string{"plans.generate.start", "plans.generate.complete", "plans.generate.fail"} {
		if response = call("/api/v1/commands/"+command, p.M{}); response.Code == 200 {
			t.Fatal("public lifecycle command", command, response.Body.String())
		}
		response = call("/mcp", p.M{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": p.M{"name": "platform_command", "arguments": p.M{"command": command, "input": p.M{}}}})
		if response.Code != 200 || !strings.Contains(response.Body.String(), `"isError":true`) {
			t.Fatal("MCP lifecycle forgery", command, response.Code, response.Body.String())
		}
	}
	for _, input := range []p.M{{"goalId": g.ID}, {"goalId": g.ID, "key": "no-revision"}, {"goalId": g.ID, "key": "extra", "expectedRequirementRevision": 1, "planRequestId": plan.Data["generationId"]}} {
		if response = call("/api/v1/plans/generate", input); response.Code != 422 {
			t.Fatal("unchecked plan request", response.Code, response.Body.String())
		}
	}
	// Public chat cannot select the internal generation path for a draft goal.
	draft := httpCostGoal(t, h, owner, project)
	response = call("/api/v1/model/chat", p.M{"goalId": draft.ID, "planRequestId": plan.Data["generationId"], "allowDraft": true, "messages": []any{p.M{"role": "user", "content": "bypass approval"}}})
	if response.Code != 409 || calls.Load() != 1 {
		t.Fatal(response.Code, response.Body.String(), calls.Load())
	}
	if response = req(h, "POST", "/api/v1/commands/plans.approve", p.M{"id": plan.ID}, cookie, csrf); response.Code != 200 {
		t.Fatal("human architect cannot approve", response.Code, response.Body.String())
	}
}

func TestPlanGenerationHTTPCredentialRevokedDuringInferenceCannotComplete(t *testing.T) {
	h := setup(t)
	identity, _ := h.Auth.IdentityByEmail("owner@example.com")
	owner := identityActor(identity)
	project := authorityCommand(t, h, owner, "projects.create", p.M{"title": "Revocable planning"})
	g := httpCostGoal(t, h, owner, project)
	authorityCommand(t, h, owner, "goals.approve", p.M{"id": g.ID})
	token, _, err := h.Auth.IssueToken(context.Background(), owner, p.M{"projectId": project.ID, "name": "Revocable Agent"})
	if err != nil {
		t.Fatal(err)
	}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := h.Auth.Revoke(r.Context(), owner, p.M{"projectId": project.ID, "id": p.Hash(token)}); err != nil {
			t.Error(err)
		}
		_ = json.NewEncoder(w).Encode(p.M{"choices": []any{p.M{"finish_reason": "stop", "message": p.M{"content": `{"summary":"Late result","tasks":[{"id":"a","title":"Analyze scope","kind":"analysis","role":"analysis","runtime":"linux"}]}`}}}, "usage": p.M{"prompt_tokens": 10, "completion_tokens": 30}})
	}))
	defer provider.Close()
	model := &integrations.Model{Service: h.Service, BaseURL: provider.URL, Name: "local-plan", Client: provider.Client(), InputUSD: 1, OutputUSD: 1}
	h.GeneratePlan = model.GeneratePlan
	body, _ := json.Marshal(p.M{"goalId": g.ID, "expectedRequirementRevision": 1, "key": "revoked-before-return"})
	r := httptest.NewRequest("POST", "/api/v1/plans/generate", strings.NewReader(string(body)))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal(w.Code, w.Body.String())
	}
	plans, _ := h.Service.Store.List(project.ID, "plan", g.ID)
	requests, _ := h.Service.Store.List(project.ID, "plan_generation", g.ID)
	charges, _ := h.Service.Store.List(project.ID, "charge", g.ID)
	if len(plans) != 0 || len(requests) != 1 || requests[0].Status != "failed" || len(charges) != 1 || charges[0].Status != "settled" {
		t.Fatal(plans, requests, charges)
	}
}

package httpapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/intelligence-substrate/platform/internal/integrations"
	p "github.com/intelligence-substrate/platform/internal/platform"
)

func TestRequirementDraftHTTPUnconfiguredCSRFAndAgentBoundaries(t *testing.T) {
	h := setup(t)
	cookie, csrf := login(t, h)
	identity, _ := h.Auth.IdentityByEmail("owner@example.com")
	owner := identityActor(identity)
	project := authorityCommand(t, h, owner, "projects.create", p.M{"title": "Requirement review"})
	goal := httpCostGoal(t, h, owner, project)
	in := p.M{"goalId": goal.ID, "expectedRequirementRevision": 1, "key": "draft"}
	if response := req(h, "POST", "/api/v1/requirements/generate", in, cookie, csrf); response.Code != 424 {
		t.Fatal(response.Code, response.Body.String())
	}
	if response := req(h, "POST", "/api/v1/requirements/generate", in, cookie, ""); response.Code != 403 {
		t.Fatal(response.Code, response.Body.String())
	}
	token, _, err := h.Auth.IssueToken(context.Background(), owner, p.M{"projectId": project.ID, "roles": []any{"product"}})
	if err != nil {
		t.Fatal(err)
	}
	model := &integrations.Model{Service: h.Service}
	h.GenerateRequirements = model.GenerateRequirements
	call := func(path string, input p.M) *httptest.ResponseRecorder {
		body, _ := json.Marshal(input)
		r := httptest.NewRequest("POST", path, strings.NewReader(string(body)))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	mcp := func(command string, input p.M) *httptest.ResponseRecorder {
		return call("/mcp", p.M{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": p.M{"name": "platform_command", "arguments": p.M{"command": command, "input": input}}})
	}
	in["content"] = p.M{"outcome": "Customers book services", "brief": "Customers manage own bookings. Payments excluded.", "criteria": []any{p.M{"text": "Customers see their own bookings", "method": "automated"}}}
	if response := mcp("requirements.propose", in); response.Code != 200 || strings.Contains(response.Body.String(), `"isError":true`) {
		t.Fatal(response.Code, response.Body.String())
	}
	drafts, _ := h.Service.Store.List(project.ID, "requirement_proposal", goal.ID)
	if len(drafts) != 1 || p.S(drafts[0].Data, "proposedKind") != "agent" {
		t.Fatal(drafts)
	}
	if response := call("/api/v1/commands/requirements.apply", p.M{"id": drafts[0].ID}); response.Code != 403 {
		t.Fatal(response.Code, response.Body.String())
	}
	for _, cmd := range []string{"requirements.apply", "requirements.generate.start", "requirements.generate.complete", "requirements.generate.fail"} {
		if response := mcp(cmd, p.M{}); response.Code != 200 || !strings.Contains(response.Body.String(), `"isError":true`) {
			t.Fatal(cmd, response.Code, response.Body.String())
		}
	}
	delete(in, "content")
	if response := call("/api/v1/requirements/generate", in); response.Code != 403 {
		t.Fatal(response.Code, response.Body.String())
	}
	requests, _ := h.Service.Store.List(project.ID, "requirement_generation", goal.ID)
	if len(requests) != 0 {
		t.Fatal(requests)
	}
}

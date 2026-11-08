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

func TestClarificationHTTPUnconfiguredAndMCPHumanBoundary(t *testing.T) {
	h := setup(t)
	cookie, csrf := login(t, h)
	identity, _ := h.Auth.IdentityByEmail("owner@example.com")
	a := identityActor(identity)
	project := authorityCommand(t, h, a, "projects.create", p.M{"title": "Requirement discovery"})
	g := httpCostGoal(t, h, a, project)
	in := p.M{"goalId": g.ID, "expectedRequirementRevision": 1, "key": "discovery"}
	if response := req(h, "POST", "/api/v1/clarifications/generate", in, cookie, csrf); response.Code != 424 {
		t.Fatal("unconfigured discovery should report its integration prerequisite", response.Code, response.Body.String())
	}
	if response := req(h, "POST", "/api/v1/clarifications/generate", in, cookie, ""); response.Code != 403 {
		t.Fatal("discovery bypassed CSRF", response.Code)
	}
	call := func(command string, input p.M) p.M {
		t.Helper()
		response := req(h, "POST", "/mcp", p.M{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": p.M{"name": "platform_command", "arguments": p.M{"command": command, "input": input}}}, cookie, csrf)
		var out p.M
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &out) != nil {
			t.Fatal(response.Code, response.Body.String())
		}
		return p.Obj(out["result"])
	}
	proposal := call("clarifications.propose", p.M{"goalId": g.ID, "expectedRequirementRevision": 1, "key": "mcp-question", "questions": []any{p.M{"question": "Who may approve a booking?"}}})
	if p.B(proposal, "isError") || !strings.Contains(string(mustCostJSON(t, proposal)), "Who may approve a booking?") {
		t.Fatal("Agent tool cannot propose scoped questions", proposal)
	}
	for _, command := range []string{"clarifications.answer", "clarifications.generate.complete", "clarifications.generate.start"} {
		if result := call(command, p.M{}); !p.B(result, "isError") {
			t.Fatal("Agent tool exposed a human or internal generation decision", command, result)
		}
	}
}

func TestClarificationRealAgentCredentialCanAskButCannotAnswerOrGenerate(t *testing.T) {
	h := setup(t)
	identity, _ := h.Auth.IdentityByEmail("owner@example.com")
	owner := identityActor(identity)
	project := authorityCommand(t, h, owner, "projects.create", p.M{"title": "Agent discovery scope"})
	goal := httpCostGoal(t, h, owner, project)
	token, _, err := h.Auth.IssueToken(context.Background(), owner, p.M{"projectId": project.ID, "roles": []any{"developer"}})
	if err != nil {
		t.Fatal(err)
	}
	model := &integrations.Model{Service: h.Service}
	h.GenerateClarifications = model.GenerateClarifications
	call := func(path string, input p.M) *httptest.ResponseRecorder {
		t.Helper()
		body, _ := json.Marshal(input)
		request := httptest.NewRequest("POST", path, strings.NewReader(string(body)))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		h.ServeHTTP(response, request)
		return response
	}
	proposal := call("/mcp", p.M{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": p.M{"name": "platform_command", "arguments": p.M{"command": "clarifications.propose", "input": p.M{"goalId": goal.ID, "expectedRequirementRevision": 1, "key": "agent-ask", "questions": []any{p.M{"question": "Who may cancel a booking?"}}}}}})
	if proposal.Code != 200 || strings.Contains(proposal.Body.String(), `"isError":true`) {
		t.Fatal("real Agent credential cannot ask", proposal.Code, proposal.Body.String())
	}
	batches, _ := h.Service.Store.List(project.ID, "clarification", goal.ID)
	if len(batches) != 1 || p.S(batches[0].Data, "proposedKind") != "agent" {
		t.Fatal("Agent provenance missing", batches)
	}
	q := p.Obj(p.Items(batches[0].Data["questions"])[0])
	if response := call("/api/v1/commands/clarifications.answer", p.M{"id": batches[0].ID, "questionId": p.S(q, "id"), "expectedRequirementRevision": 1, "key": "agent-answer", "answer": "Agent decided"}); response.Code != 403 {
		t.Fatal("Agent answered", response.Code, response.Body.String())
	}
	for _, command := range []string{"clarifications.answer", "clarifications.generate.complete"} {
		response := call("/mcp", p.M{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": p.M{"name": "platform_command", "arguments": p.M{"command": command, "input": p.M{}}}})
		if response.Code != 200 || !strings.Contains(response.Body.String(), `"isError":true`) {
			t.Fatal("MCP exposed human/internal decision", response.Code, response.Body.String())
		}
	}
	if response := call("/api/v1/clarifications/generate", p.M{"goalId": goal.ID, "expectedRequirementRevision": 1, "key": "agent-generate"}); response.Code != 403 {
		t.Fatal("Agent generated questions as a human", response.Code, response.Body.String())
	}
	requests, _ := h.Service.Store.List(project.ID, "clarification_generation", goal.ID)
	if len(requests) != 0 {
		t.Fatal("Agent created a human generation intent", requests)
	}
}

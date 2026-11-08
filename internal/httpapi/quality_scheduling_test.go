package httpapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	p "github.com/intelligence-substrate/platform/internal/platform"
)

func TestQualitySchedulingHTTPRequiresCurrentHumanQualityAndCSRF(t *testing.T) {
	h := setup(t)
	cookie, csrf := login(t, h)
	identity, _ := h.Auth.IdentityByEmail("owner@example.com")
	owner := identityActor(identity)
	project := authorityCommand(t, h, owner, "projects.create", p.M{"title": "Protected automatic QA"})
	g := httpCostGoal(t, h, owner, project)
	authorityCommand(t, h, owner, "goals.approve", p.M{"id": g.ID})
	plan := authorityCommand(t, h, owner, "plans.propose", p.M{"goalId": g.ID, "summary": "Approved source work", "tasks": []any{p.M{"id": "analysis", "title": "Review source", "role": "developer", "runtime": "linux", "kind": "analysis"}}})
	authorityCommand(t, h, owner, "plans.approve", p.M{"id": plan.ID})
	pipeline := authorityCommand(t, h, owner, "pipelines.create", p.M{"projectId": project.ID, "title": "Protected check", "runtime": "linux", "method": "automated", "command": []any{"true"}})
	in := p.M{"goalId": g.ID, "expectedRequirementRevision": g.Revision, "expectedPlanId": plan.ID, "pipelineId": pipeline.ID, "target": "web", "criteria": []any{p.S(p.Obj(p.Items(g.Data["criteria"])[0]), "id")}, "budgetCents": 10, "key": "http-policy"}
	if response := req(h, "POST", "/api/v1/commands/quality.bind", in, cookie, ""); response.Code != 403 {
		t.Fatal("quality binding bypassed CSRF", response.Code, response.Body.String())
	}
	response := req(h, "POST", "/api/v1/commands/quality.bind", in, cookie, csrf)
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	var policy p.Resource
	if err := json.Unmarshal(response.Body.Bytes(), &policy); err != nil {
		t.Fatal(err)
	}
	worker := authorityCommand(t, h, owner, "workers.register", p.M{"projectId": project.ID, "title": "Quality worker", "roles": []any{"quality"}, "capabilities": []any{"test"}, "runtime": "linux", "trustedQuality": true})
	for _, credential := range []p.M{{"projectId": project.ID, "name": "Owner-delegated Agent"}, {"projectId": project.ID, "workerId": worker.ID}} {
		token, _, err := h.Auth.IssueToken(context.Background(), owner, credential)
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
		for _, entry := range []struct {
			name  string
			input p.M
		}{{"quality.bind", in}, {"quality.revoke", p.M{"id": policy.ID, "reason": "Unauthorized revocation", "key": "denied"}}} {
			if result := call("/api/v1/commands/"+entry.name, entry.input); result.Code != 403 {
				t.Fatal("delegated identity changed protected policy", entry.name, result.Code, result.Body.String())
			}
			result := call("/mcp", p.M{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": p.M{"name": "platform_command", "arguments": p.M{"command": entry.name, "input": entry.input}}})
			if result.Code != 200 || !strings.Contains(result.Body.String(), `"isError":true`) {
				t.Fatal("MCP exposed protected policy mutation", result.Code, result.Body.String())
			}
		}
	}
	// Membership changes must affect an already-authenticated human request too.
	qualityIdentity, err := h.Auth.CreateIdentity("qa-policy@example.test", "Quality manager", "quality-policy-password-2026", false)
	if err != nil {
		t.Fatal(err)
	}
	member := authorityCommand(t, h, owner, "members.grant", p.M{"projectId": project.ID, "identityId": qualityIdentity.ID, "name": qualityIdentity.Name, "roles": []any{"quality"}})
	qaLogin := req(h, "POST", "/api/v1/auth/login", p.M{"email": "qa-policy@example.test", "password": "quality-policy-password-2026"}, nil, "")
	if qaLogin.Code != 200 {
		t.Fatal(qaLogin.Code, qaLogin.Body.String())
	}
	var session struct {
		CSRF string `json:"csrf"`
	}
	_ = json.Unmarshal(qaLogin.Body.Bytes(), &session)
	qaCookie := qaLogin.Result().Cookies()[0]
	authorityCommand(t, h, owner, "members.revoke", p.M{"id": member.ID, "expectedRevision": member.Revision, "reason": "Quality membership ended"})
	if result := req(h, "POST", "/api/v1/commands/quality.revoke", p.M{"id": policy.ID, "reason": "Removed membership cannot revoke", "key": "late"}, qaCookie, session.CSRF); result.Code != 403 {
		t.Fatal("removed quality member used cached session", result.Code, result.Body.String())
	}
	stored, _ := h.Service.Store.Get(policy.ID)
	if stored.Status != "active" {
		t.Fatal("denied identity mutated policy", stored)
	}
}

package httpapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	p "github.com/intelligence-substrate/platform/internal/platform"
)

func TestDesignHTTPMCPProposalParityAndHumanOnlyApproval(t *testing.T) {
	h := setup(t)
	cookie, csrf := login(t, h)
	identity, _ := h.Auth.IdentityByEmail("owner@example.com")
	owner := identityActor(identity)
	project := authorityCommand(t, h, owner, "projects.create", p.M{"title": "Design parity"})
	g := httpCostGoal(t, h, owner, project)
	authorityCommand(t, h, owner, "goals.approve", p.M{"id": g.ID})
	token, _, err := h.Auth.IssueToken(context.Background(), owner, p.M{"projectId": project.ID, "name": "Design Agent"})
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
	in := p.M{"goalId": g.ID, "expectedRequirementRevision": 1, "key": "same-design", "title": "Cost presentation", "summary": "Show actual and reserved costs separately", "decisions": "Pending costs do not count as settled", "criteria": []any{"accounting"}, "reason": "Make cost status visible"}
	response := call("/api/v1/commands/designs.propose", in)
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	var design p.Resource
	_ = json.Unmarshal(response.Body.Bytes(), &design)
	response = call("/mcp", p.M{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": p.M{"name": "platform_command", "arguments": p.M{"command": "designs.propose", "input": in}}})
	var rpc p.M
	_ = json.Unmarshal(response.Body.Bytes(), &rpc)
	result := p.Obj(rpc["result"])
	if response.Code != 200 || p.B(result, "isError") || p.S(p.Obj(p.Obj(result["structuredContent"])["resource"]), "id") != design.ID {
		t.Fatal("MCP did not replay original proposal", response.Code, response.Body.String())
	}
	if response = req(h, "POST", "/api/v1/commands/designs.propose", in, cookie, ""); response.Code != 403 {
		t.Fatal("design mutation lost CSRF protection", response.Code)
	}
	plan := authorityCommand(t, h, owner, "plans.propose", p.M{"goalId": g.ID, "summary": "Implement reviewed cost design", "designId": design.ID, "expectedDesignBinding": design.Data["binding"], "tasks": []any{p.M{"id": "a", "title": "Analyze costs", "kind": "analysis", "role": "analysis", "runtime": "linux"}}})
	if response = call("/api/v1/commands/plans.approve", p.M{"id": plan.ID}); response.Code != 403 {
		t.Fatal("Agent approved its own design", response.Code)
	}
	if response = req(h, "POST", "/api/v1/commands/plans.approve", p.M{"id": plan.ID}, cookie, csrf); response.Code != 200 {
		t.Fatal("human architect could not finalize selected design", response.Code, response.Body.String())
	}
	other := authorityCommand(t, h, owner, "projects.create", p.M{"title": "Other project"})
	foreign := httpCostGoal(t, h, owner, other)
	in["goalId"], in["key"] = foreign.ID, "foreign"
	if response = call("/api/v1/commands/designs.propose", in); response.Code != 403 {
		t.Fatal("design token crossed project boundary", response.Code)
	}
	if err := h.Auth.Revoke(context.Background(), owner, p.M{"projectId": project.ID, "id": p.Hash(token)}); err != nil {
		t.Fatal(err)
	}
	if response = call("/api/v1/commands/designs.propose", in); response.Code != 401 {
		t.Fatal("revoked design token stayed usable", response.Code)
	}
}

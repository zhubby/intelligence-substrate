package httpapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	p "github.com/intelligence-substrate/platform/internal/platform"
)

func TestReleaseIssueHTTPHumanDecisionAndMCPPlanningParity(t *testing.T) {
	f := newTraceHTTPFixture(t)
	officer := p.Actor{ID: "issue-officer", Kind: "human", Admin: true}
	authorityCommand(t, f.h, officer, "releases.approve", p.M{"id": f.release.ID})
	authorityCommand(t, f.h, officer, "releases.transition", p.M{"id": f.release.ID, "target": "web", "state": "awaiting_review", "externalId": "synthetic-channel-100", "proofArtifactId": f.proof.ID})
	authorityCommand(t, f.h, officer, "releases.transition", p.M{"id": f.release.ID, "target": "web", "state": "rejected", "reason": "Fix privacy confirmation"})
	issues, _ := f.h.Service.Store.List(f.project.ID, "release_issue", f.goal.ID)
	if len(issues) != 1 {
		t.Fatal("HTTP candidate rejection lost durable issue", issues)
	}
	issue := issues[0]
	in := p.M{"id": issue.ID, "expectedRevision": issue.Revision, "expectedBinding": p.S(issue.Data, "binding"), "key": "human-confirmation", "strategy": "new-build", "instructions": "Implement privacy confirmation and reverify exact package"}
	path := "/api/v1/commands/release_issues.triage"
	if response := req(f.h, "POST", path, in, f.cookie, ""); response.Code != 403 {
		t.Fatal("triage bypassed CSRF", response.Code)
	}
	token, _, err := f.h.Auth.IssueToken(context.Background(), f.actor, p.M{"projectId": f.project.ID, "name": "Repair planner"})
	if err != nil {
		t.Fatal(err)
	}
	call := func(path string, input p.M) *httptest.ResponseRecorder {
		body, _ := json.Marshal(input)
		r := httptest.NewRequest("POST", path, strings.NewReader(string(body)))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		f.h.ServeHTTP(response, r)
		return response
	}
	if response := call(path, in); response.Code != 403 {
		t.Fatal("Agent confirmed human repair requirements", response.Code)
	}
	rpc := func(name string, args p.M) p.M {
		response := call("/mcp", p.M{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": p.M{"name": name, "arguments": args}})
		if response.Code != 200 {
			t.Fatal(response.Code, response.Body.String())
		}
		return p.Obj(traceObject(t, response.Body.Bytes())["result"])
	}
	read := rpc("get_resource", p.M{"id": issue.ID})
	if p.B(read, "isError") || p.S(p.Obj(p.Obj(read["structuredContent"])["resource"]), "id") != issue.ID {
		t.Fatal("MCP could not read exact rejection context", read)
	}
	if result := rpc("platform_command", p.M{"command": "release_issues.triage", "input": in}); !p.B(result, "isError") {
		t.Fatal("MCP offered a human decision", result)
	}
	if response := req(f.h, "POST", path, in, f.cookie, f.csrf); response.Code != 200 {
		t.Fatal("authorized human could not confirm repair", response.Code, response.Body.String())
	}
	result := rpc("platform_command", p.M{"command": "plans.propose", "input": p.M{"goalId": f.goal.ID, "summary": "Repair the human-confirmed privacy issue", "tasks": []any{p.M{"id": "analysis", "title": "Review exact rejected source", "kind": "analysis", "role": "analysis", "runtime": "linux"}}}})
	plan := p.Obj(p.Obj(result["structuredContent"])["resource"])
	if p.B(result, "isError") || len(p.Items(p.Obj(plan["data"])["releaseIssues"])) != 1 {
		t.Fatal("MCP proposal lost repair parity", result)
	}
	for _, name := range []string{"release_issues.resolve", "release_issues.close"} {
		if response := call("/api/v1/commands/"+name, p.M{"id": issue.ID}); response.Code < 400 {
			t.Fatal("Agent directly closed issue", name)
		}
	}
	if err = f.h.Auth.Revoke(context.Background(), f.actor, p.M{"projectId": f.project.ID, "id": p.Hash(token)}); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/api/v1/resources/"+issue.ID, nil)
	r.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	f.h.ServeHTTP(response, r)
	if response.Code != 401 {
		t.Fatal("revoked HTTP agent retained issue access", response.Code)
	}
}

package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	p "github.com/intelligence-substrate/platform/internal/platform"
)

func TestMessageWaitRESTAndMCPEnforceCurrentWorkerAuthority(t *testing.T) {
	h := setup(t)
	cookie, csrf := login(t, h)
	identity, _ := h.Auth.IdentityByEmail("owner@example.com")
	owner := identityActor(identity)
	command := func(a p.Actor, name string, in p.M) p.Resource { return authorityCommand(t, h, a, name, in) }
	project := command(owner, "projects.create", p.M{"title": "HTTP wait"})
	g := command(owner, "goals.create", p.M{"projectId": project.ID, "title": "Interface", "outcome": "Document UTC", "targets": []any{"web"}, "criteria": []any{p.M{"id": "utc", "text": "Use UTC"}}, "budget": 50})
	command(owner, "goals.approve", p.M{"id": g.ID})
	plan := command(owner, "plans.propose", p.M{"goalId": g.ID, "summary": "Analyze convention", "tasks": []any{p.M{"id": "analyze", "title": "Analyze UTC", "kind": "analysis", "role": "analysis", "runtime": "linux"}}})
	command(owner, "plans.approve", p.M{"id": plan.ID})
	worker := command(owner, "workers.register", p.M{"projectId": project.ID, "title": "Analyst", "roles": []any{"analysis"}, "capabilities": []any{"analysis"}, "runtime": "linux"})
	token, actor, e := h.Auth.IssueToken(context.Background(), owner, p.M{"projectId": project.ID, "workerId": worker.ID})
	if e != nil {
		t.Fatal(e)
	}
	run := command(actor, "tasks.claim", p.M{})
	request := command(actor, "messages.send", p.M{"projectId": project.ID, "goalId": g.ID, "runId": run.ID, "leaseToken": p.S(run.Data, "leaseToken"), "recipient": owner.ID, "type": "clarification", "key": "http-request", "payload": p.M{"question": "Confirm UTC?"}})
	in := p.M{"id": run.ID, "leaseToken": p.S(run.Data, "leaseToken"), "requestMessageId": request.ID, "reason": "Await exact collaborator reply", "summary": "No tools executed; original scope retained.", "timeoutSeconds": 600, "steps": 2, "source": p.M{"repositoryId": "", "commit": ""}}
	if out := req(h, "POST", "/api/v1/commands/tasks.wait", in, cookie, csrf); out.Code != 403 {
		t.Fatal("human could impersonate Worker suspension", out.Code, out.Body.String())
	}
	call := func(path string, body p.M) *httptest.ResponseRecorder {
		b, _ := json.Marshal(body)
		r := httptest.NewRequest("POST", path, bytes.NewReader(b))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	response := call("/mcp", p.M{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": p.M{"name": "platform_command", "arguments": p.M{"command": "tasks.wait", "input": in}}})
	body := traceObject(t, response.Body.Bytes())
	result := p.Obj(body["result"])
	if response.Code != 200 || p.B(result, "isError") {
		t.Fatal("governed MCP wait unavailable", response.Body.String())
	}
	resource := p.Obj(p.Obj(result["structuredContent"])["resource"])
	id := p.S(resource, "id")
	if id == "" || p.S(resource, "status") != "waiting" || strings.Contains(response.Body.String(), "leaseToken") {
		t.Fatal("MCP wait lost scope or leaked authority", response.Body.String())
	}
	replay := call("/api/v1/commands/tasks.wait", in)
	if replay.Code != 200 || p.S(traceObject(t, replay.Body.Bytes()), "id") != id {
		t.Fatal("REST/MCP replay differs", replay.Code, replay.Body.String())
	}
	read := req(h, "GET", "/api/v1/resources/"+id, nil, cookie, "")
	if read.Code != 200 || strings.Contains(read.Body.String(), p.S(run.Data, "leaseToken")) {
		t.Fatal("authorized wait read failed or leaked authority", read.Code)
	}
	// Revocation is checked again even for an exact previously accepted replay.
	command(owner, "workers.disable", p.M{"id": worker.ID})
	if out := call("/api/v1/commands/tasks.wait", in); out.Code != 403 && out.Code != 401 {
		t.Fatal("disabled Worker replay accepted", out.Code, out.Body.String())
	}
}

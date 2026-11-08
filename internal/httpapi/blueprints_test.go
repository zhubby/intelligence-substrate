package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/intelligence-substrate/platform/internal/blueprint"
	p "github.com/intelligence-substrate/platform/internal/platform"
)

func TestBlueprintHTTPMCPCatalogProposalAndWorkerLeaseParity(t *testing.T) {
	h := setup(t)
	var err error
	h.Service.Blueprints, err = blueprint.Load("../..")
	if err != nil {
		t.Fatal(err)
	}
	cookie, csrf := login(t, h)
	identity, _ := h.Auth.IdentityByEmail("owner@example.com")
	owner := identityActor(identity)
	project := authorityCommand(t, h, owner, "projects.create", p.M{"title": "Blueprint protocol parity"})
	g := httpCostGoal(t, h, owner, project)
	authorityCommand(t, h, owner, "goals.approve", p.M{"id": g.ID})
	token, _, err := h.Auth.IssueToken(context.Background(), owner, p.M{"projectId": project.ID, "name": "Planning agent"})
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path, token string, input p.M) *httptest.ResponseRecorder {
		body, _ := json.Marshal(input)
		r := httptest.NewRequest(method, path, strings.NewReader(string(body)))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	response := call("GET", "/api/v1/projects/"+project.ID+"/blueprints", token, nil)
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	var catalog []blueprint.Descriptor
	if json.Unmarshal(response.Body.Bytes(), &catalog) != nil || len(catalog) != 1 {
		t.Fatal("catalog not exposed", response.Body.String())
	}
	rpc := func(name string, args p.M) *httptest.ResponseRecorder {
		return call("POST", "/mcp", token, p.M{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": p.M{"name": name, "arguments": args}})
	}
	response = rpc("blueprint_catalog", p.M{"projectId": project.ID})
	var wire p.M
	_ = json.Unmarshal(response.Body.Bytes(), &wire)
	if p.B(p.Obj(wire["result"]), "isError") || len(p.Items(p.Obj(p.Obj(wire["result"])["structuredContent"])["resource"])) != 1 {
		t.Fatal("MCP lacks catalog parity", response.Body.String())
	}
	in := p.M{"goalId": g.ID, "expectedRequirementRevision": g.Revision, "key": "same-selection", "blueprintId": catalog[0].ID, "expectedCatalogDigest": catalog[0].Digest, "applicationName": "enterprise_app", "organization": "com.enterprise", "mode": "booking", "reason": "Start a governed booking application"}
	response = call("POST", "/api/v1/commands/blueprints.propose", token, in)
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	var selection p.Resource
	_ = json.Unmarshal(response.Body.Bytes(), &selection)
	response = rpc("platform_command", p.M{"command": "blueprints.propose", "input": in})
	_ = json.Unmarshal(response.Body.Bytes(), &wire)
	if p.B(p.Obj(wire["result"]), "isError") || p.S(p.Obj(p.Obj(p.Obj(wire["result"])["structuredContent"])["resource"]), "id") != selection.ID {
		t.Fatal("MCP did not replay HTTP proposal", response.Body.String())
	}
	if response = req(h, "POST", "/api/v1/commands/blueprints.propose", in, cookie, ""); response.Code != 403 {
		t.Fatal("mutation bypassed CSRF", response.Code)
	}
	foreign := authorityCommand(t, h, owner, "projects.create", p.M{"title": "Foreign project"})
	if response = call("GET", "/api/v1/projects/"+foreign.ID+"/blueprints", token, nil); response.Code != 403 {
		t.Fatal("catalog crossed token project", response.Code)
	}
	plan := authorityCommand(t, h, owner, "plans.propose", p.M{"goalId": g.ID, "summary": "Initialize selected sources", "blueprintId": selection.ID, "expectedBlueprintBinding": selection.Data["binding"], "initializeBlueprint": true, "tasks": []any{p.M{"id": "analysis", "title": "Review business scope", "kind": "analysis", "role": "analysis", "runtime": "linux"}}})
	if response = call("POST", "/api/v1/commands/plans.approve", token, p.M{"id": plan.ID}); response.Code != 403 {
		t.Fatal("agent approved initializer", response.Code)
	}
	authorityCommand(t, h, owner, "plans.approve", p.M{"id": plan.ID})
	node := authorityCommand(t, h, owner, "workers.register", p.M{"projectId": project.ID, "title": "Source seed node", "runtime": "linux", "roles": []any{"developer"}, "capabilities": []any{"code"}})
	workerToken, worker, err := h.Auth.IssueToken(context.Background(), owner, p.M{"projectId": project.ID, "workerId": node.ID})
	if err != nil {
		t.Fatal(err)
	}
	run := authorityCommand(t, h, worker, "tasks.claim", p.M{})
	path := "/api/v1/runs/" + run.ID + "/blueprint"
	lease := p.M{"leaseToken": p.S(run.Data, "leaseToken")}
	if response = call("POST", path, token, lease); response.Code != 403 {
		t.Fatal("planning agent read worker seed", response.Code)
	}
	if response = req(h, "POST", path, lease, cookie, csrf); response.Code != 403 {
		t.Fatal("human session bypassed worker lease", response.Code)
	}
	if response = call("POST", path, workerToken, p.M{"leaseToken": "wrong"}); response.Code != 409 {
		t.Fatal("wrong lease accepted", response.Code)
	}
	if response = call("POST", path+"?leaseToken=leak", workerToken, lease); response.Code != 422 {
		t.Fatal("query-based lease allowed", response.Code)
	}
	response = call("POST", path, workerToken, lease)
	sum := sha256.Sum256(response.Body.Bytes())
	if response.Code != 200 || response.Header().Get("Content-Type") != "application/x-tar" || hex.EncodeToString(sum[:]) != p.S(selection.Data, "digest") {
		t.Fatal("worker did not receive exact approved archive", response.Code)
	}
	if err = h.Auth.Revoke(context.Background(), owner, p.M{"projectId": project.ID, "id": p.Hash(workerToken)}); err != nil {
		t.Fatal(err)
	}
	if response = call("POST", path, workerToken, lease); response.Code != 401 {
		t.Fatal("revoked worker could read seed", response.Code)
	}
}

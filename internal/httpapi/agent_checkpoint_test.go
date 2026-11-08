package httpapi

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	p "github.com/intelligence-substrate/platform/internal/platform"
)

func TestAgentCheckpointHTTPExactBytesAndLeaseBoundary(t *testing.T) {
	h := setup(t)
	cookie, csrf := login(t, h)
	identity, _ := h.Auth.IdentityByEmail("owner@example.com")
	owner := identityActor(identity)
	command := func(actor p.Actor, name string, in p.M) p.Resource { return authorityCommand(t, h, actor, name, in) }
	project := command(owner, "projects.create", p.M{"title": "Agent checkpoint HTTP"})
	goal := command(owner, "goals.create", p.M{"projectId": project.ID, "title": "Exact resume", "outcome": "Preserve real bytes", "targets": []any{"web"}, "criteria": []any{p.M{"id": "resume", "text": "Exact bytes"}}, "budget": 50})
	command(owner, "goals.approve", p.M{"id": goal.ID})
	repo := command(owner, "repositories.create", p.M{"projectId": project.ID, "title": "source"})
	plan := command(owner, "plans.propose", p.M{"goalId": goal.ID, "summary": "Exact code recovery", "tasks": []any{p.M{"id": "code", "title": "Code", "kind": "code", "role": "developer", "runtime": "linux", "repositoryId": repo.ID, "branch": "checkpoint"}}})
	command(owner, "plans.approve", p.M{"id": plan.ID})
	worker := command(owner, "workers.register", p.M{"projectId": project.ID, "title": "Original", "roles": []any{"developer"}, "capabilities": []any{"code"}, "runtime": "linux"})
	token, actor, e := h.Auth.IssueToken(context.Background(), owner, p.M{"projectId": project.ID, "workerId": worker.ID})
	if e != nil {
		t.Fatal(e)
	}
	run := command(actor, "tasks.claim", p.M{})
	step := command(actor, "agent_steps.prepare", p.M{"runId": run.ID, "leaseToken": run.Data["leaseToken"], "step": 1, "action": p.M{"action": "write", "path": "asset.bin", "content": "fixture"}, "source": p.M{"repositoryId": repo.ID, "commit": p.Obj(run.Data["task"])["commit"]}})
	var body bytes.Buffer
	tw := tar.NewWriter(&body)
	_ = tw.WriteHeader(&tar.Header{Name: "asset.bin", Mode: 0755, Size: 3})
	_, _ = tw.Write([]byte{0, 255, 2})
	_ = tw.Close()
	request := func(method, path, mode, lease string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/api/v1/"+path, bytes.NewReader(body.Bytes()))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-IS-Lease", lease)
		req.Header.Set("Content-Type", "application/x-tar")
		switch mode {
		case "missing":
			req.Header.Del("X-IS-Lease")
		case "duplicate":
			req.Header.Add("X-IS-Lease", lease)
		case "wrong":
			req.Header.Set("X-IS-Lease", "different")
		case "query":
			req.URL.RawQuery = "leaseToken=never-in-url"
		case "media":
			req.Header.Set("Content-Type", "application/json")
		case "human":
			req.Header.Del("Authorization")
			req.AddCookie(cookie)
			req.Header.Set("Origin", "http://localhost")
			req.Header.Set("X-CSRF-Token", csrf)
		}
		response := httptest.NewRecorder()
		h.ServeHTTP(response, req)
		return response
	}
	for _, mode := range []string{"missing", "duplicate", "wrong", "query", "media", "human"} {
		r := request("POST", "agent-steps/"+step.ID+"/checkpoint", mode, p.S(run.Data, "leaseToken"))
		if r.Code < 400 {
			t.Fatal("unsafe upload accepted", mode, r.Code)
		}
	}
	r := request("POST", "agent-steps/"+step.ID+"/checkpoint", "valid", p.S(run.Data, "leaseToken"))
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	var checkpoint p.Resource
	if e = json.Unmarshal(r.Body.Bytes(), &checkpoint); e != nil {
		t.Fatal(e)
	}
	command(actor, "agent_steps.complete", p.M{"id": step.ID, "runId": run.ID, "leaseToken": run.Data["leaseToken"], "checkpointId": checkpoint.ID, "observation": "source saved"})
	command(actor, "tasks.complete", p.M{"id": run.ID, "leaseToken": run.Data["leaseToken"], "exitCode": 1, "result": p.M{"blocked": true}})
	command(owner, "tasks.retry", p.M{"id": p.S(run.Data, "taskId")})
	next := command(actor, "tasks.claim", p.M{})
	for _, mode := range []string{"missing", "duplicate", "wrong", "query", "human"} {
		r = request("GET", "runs/"+next.ID+"/agent-checkpoint", mode, p.S(next.Data, "leaseToken"))
		if r.Code < 400 {
			t.Fatal("unsafe download accepted", mode, r.Code)
		}
	}
	r = request("GET", "runs/"+next.ID+"/agent-checkpoint", "valid", p.S(next.Data, "leaseToken"))
	if r.Code != 200 || !bytes.Equal(r.Body.Bytes(), body.Bytes()) || r.Header().Get("Cache-Control") != "no-store" || r.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("download changed exact archive/boundary", r.Code)
	}
	command(owner, "workers.disable", p.M{"id": worker.ID})
	if r = request("GET", "runs/"+next.ID+"/agent-checkpoint", "valid", p.S(next.Data, "leaseToken")); r.Code < 400 {
		t.Fatal("revoked worker restored bytes")
	}
}

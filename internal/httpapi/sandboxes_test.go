package httpapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	p "github.com/intelligence-substrate/platform/internal/platform"
)

func TestSandboxHTTPCurrentAuthorityCSRFAndReconciliation(t *testing.T) {
	h := setup(t)
	cookie, csrf := login(t, h)
	identity, _ := h.Auth.IdentityByEmail("owner@example.com")
	owner := identityActor(identity)
	cmd := func(a p.Actor, name string, in p.M) p.Resource { return authorityCommand(t, h, a, name, in) }
	project := cmd(owner, "projects.create", p.M{"title": "Durable AX ownership"})
	g := httpCostGoal(t, h, owner, project)
	cmd(owner, "goals.approve", p.M{"id": g.ID})
	plan := cmd(owner, "plans.propose", p.M{"goalId": g.ID, "summary": "Bound CLI", "tasks": []any{p.M{"id": "analysis", "title": "Analyze", "role": "developer", "kind": "analysis", "runtime": "linux"}}})
	cmd(owner, "plans.approve", p.M{"id": plan.ID})
	w := cmd(owner, "workers.register", p.M{"projectId": project.ID, "title": "Original", "roles": []any{"developer"}, "capabilities": []any{"analysis"}, "runtime": "linux"})
	token, actor, e := h.Auth.IssueToken(context.Background(), owner, p.M{"projectId": project.ID, "workerId": w.ID})
	if e != nil {
		t.Fatal(e)
	}
	call := func(bearer, name string, in p.M) *httptest.ResponseRecorder {
		body, _ := json.Marshal(in)
		r := httptest.NewRequest("POST", "/api/v1/commands/"+name, strings.NewReader(string(body)))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+bearer)
		out := httptest.NewRecorder()
		h.ServeHTTP(out, r)
		return out
	}
	run := cmd(actor, "tasks.claim", p.M{})
	in := p.M{"runId": run.ID, "leaseToken": run.Data["leaseToken"], "key": "http-owned", "command": []any{"/bin/true"}, "backend": p.M{"context": "gke_enterprise/zone:cluster", "namespace": "ax-system", "atespace": "http-test", "image": "runner@sha256:" + strings.Repeat("a", 64)}}
	if out := req(h, "POST", "/api/v1/commands/sandboxes.prepare", in, cookie, csrf); out.Code != 403 {
		t.Fatal("human impersonated controller", out.Code)
	}
	out := call(token, "sandboxes.prepare", in)
	if out.Code != 200 {
		t.Fatal(out.Code, out.Body.String())
	}
	var sandbox p.Resource
	if e = json.Unmarshal(out.Body.Bytes(), &sandbox); e != nil {
		t.Fatal(e)
	}
	if replay := call(token, "sandboxes.prepare", in); replay.Code != 200 || !strings.Contains(replay.Body.String(), `"replayed":true`) {
		t.Fatal("exact prepare replay unavailable", replay.Code, replay.Body.String())
	}
	// Replay still checks current capabilities; it never grants another execution.
	if e = h.Service.Store.Update(context.Background(), func(tx *p.Tx) error {
		current, err := tx.Get(w.ID)
		if err != nil {
			return err
		}
		current.Data["capabilities"] = []any{"build"}
		return tx.Put(current)
	}); e != nil {
		t.Fatal(e)
	}
	if replay := call(token, "sandboxes.prepare", in); replay.Code != 403 {
		t.Fatal("replay bypassed current capability", replay.Code, replay.Body.String())
	}
	decision := p.M{"id": sandbox.ID, "name": sandbox.Data["name"], "backendDigest": sandbox.Data["backendDigest"], "key": "owner-checked", "observation": "Original node isolated and AX credential revoked; exact Task Workspace and Actor verified stopped"}
	if out = req(h, "POST", "/api/v1/commands/sandboxes.reconcile", decision, cookie, ""); out.Code != 403 {
		t.Fatal("decision bypassed CSRF", out.Code)
	}
	agent, _, err := h.Auth.IssueToken(context.Background(), owner, p.M{"projectId": project.ID})
	if err != nil {
		t.Fatal(err)
	}
	if out = call(agent, "sandboxes.reconcile", decision); out.Code != 403 {
		t.Fatal("delegated Agent reconciled", out.Code)
	}
	if out = call(token, "sandboxes.reconcile", decision); out.Code != 403 {
		t.Fatal("Worker reconciled", out.Code)
	}
	if out = req(h, "POST", "/api/v1/commands/sandboxes.reconcile", decision, cookie, csrf); out.Code != 409 {
		t.Fatal("active Worker reconciled", out.Code)
	}
	cmd(owner, "workers.disable", p.M{"id": w.ID})
	if out = call(token, "sandboxes.prepare", in); out.Code != 401 && out.Code != 403 {
		t.Fatal("disabled token replayed", out.Code)
	}
	if e = h.Service.Store.Update(context.Background(), func(tx *p.Tx) error {
		r, err := tx.Get(run.ID)
		if err != nil {
			return err
		}
		r.Data["leaseUntil"] = time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)
		return tx.Put(r)
	}); e != nil {
		t.Fatal(e)
	}
	if out = req(h, "POST", "/api/v1/commands/sandboxes.reconcile", decision, cookie, csrf); out.Code != 200 {
		t.Fatal(out.Code, out.Body.String())
	}
	if out = req(h, "POST", "/api/v1/commands/sandboxes.reconcile", decision, cookie, csrf); out.Code != 200 {
		t.Fatal("same-key decision retry failed", out.Code)
	}
	stored, _ := h.Service.Store.Get(sandbox.ID)
	if stored.Status != "stopped" || p.S(stored.Data, "reconciledBy") != owner.ID {
		t.Fatal(stored)
	}
}

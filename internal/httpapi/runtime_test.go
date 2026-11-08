package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	p "github.com/intelligence-substrate/platform/internal/platform"
)

func TestHistoricalRuntimeReportUploadThroughHTTP(t *testing.T) {
	h := setup(t)
	cookie, csrf := login(t, h)
	identity, _ := h.Auth.IdentityByEmail("owner@example.com")
	a := identityActor(identity)
	ctx := context.Background()
	command := func(name string, in p.M) p.Resource {
		t.Helper()
		r, e := h.Service.Command(ctx, a, name, in)
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	project := command("projects.create", p.M{"title": "Historical operational report"})
	goal := command("goals.create", p.M{"projectId": project.ID, "title": "Running Web application", "outcome": "Keep the deployed service verifiable", "targets": []any{"web"}, "criteria": []any{p.M{"id": "works", "text": "Service works", "method": "manual"}}, "budget": 10})
	repo := command("repositories.create", p.M{"projectId": project.ID, "title": "runtime-source"})
	change := command("repositories.commit", p.M{"id": repo.ID, "branch": "runtime", "message": "Add application", "files": p.M{"app.txt": "app"}})
	meta := p.M{"goalId": goal.ID, "repositoryId": repo.ID, "commit": p.S(change.Data, "lastCommit"), "target": "web", "type": "package", "name": "historical-package"}
	artifact, e := h.Service.Upload(ctx, a, meta, bytes.NewBufferString("historical package fixture"))
	if e != nil {
		t.Fatal(e)
	}
	deployment := p.Resource{ID: "deployment_http_fixture", Kind: "deployment", ProjectID: project.ID, GoalID: goal.ID, Status: "active", Data: p.M{"artifactId": artifact.ID, "commit": meta["commit"]}}
	if e = h.Service.Store.Update(ctx, func(tx *p.Tx) error { return tx.Put(deployment) }); e != nil {
		t.Fatal(e)
	}
	command("goals.revise", p.M{"id": goal.ID, "reason": "Future version targets desktop; keep monitoring existing Web", "targets": []any{"windows"}})
	query := url.Values{"goalId": {goal.ID}, "repositoryId": {repo.ID}, "commit": {p.S(meta, "commit")}, "target": {"web"}, "type": {"report"}, "name": {"actual-runtime.txt"}, "deploymentId": {deployment.ID}}
	r := httptest.NewRequest("POST", "/api/v1/artifacts/upload?"+query.Encode(), bytes.NewBufferString("Actual operational report fixture"))
	r.AddCookie(cookie)
	r.Header.Set("X-CSRF-Token", csrf)
	r.Header.Set("Content-Type", "application/octet-stream")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal("HTTP upload lost the historical deployment binding", w.Code, w.Body.String())
	}
	var uploaded p.Resource
	if e = json.Unmarshal(w.Body.Bytes(), &uploaded); e != nil {
		t.Fatal(e)
	}
	if p.S(uploaded.Data, "deploymentId") != deployment.ID {
		t.Fatal("HTTP report did not retain the deployment identity")
	}
}

func TestRuntimeIncidentAcknowledgementThroughAuthenticatedHTTP(t *testing.T) {
	h := setup(t)
	cookie, csrf := login(t, h)
	identity, err := h.Auth.IdentityByEmail("owner@example.com")
	if err != nil {
		t.Fatal(err)
	}
	a := identityActor(identity)
	project, err := h.Service.Command(context.Background(), a, "projects.create", p.M{"title": "HTTP runtime incidents"})
	if err != nil {
		t.Fatal(err)
	}
	d := p.Resource{ID: "runtime_http_deployment", Kind: "deployment", Title: "HTTP runtime fixture", ProjectID: project.ID, Status: "active", Data: p.M{"application": "feedback", "workerId": "node", "activatedAt": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)}}
	if err = h.Service.Store.Update(context.Background(), func(tx *p.Tx) error { return tx.Put(d) }); err != nil {
		t.Fatal(err)
	}
	if err = h.Service.ReconcileRuntimeSignals(context.Background()); err != nil {
		t.Fatal(err)
	}
	incidents, err := h.Service.Store.List(project.ID, "incident", "")
	if err != nil || len(incidents) != 1 {
		t.Fatal(err, incidents)
	}
	in := p.M{"id": incidents[0].ID, "key": "http-owner", "note": "Actual HTTP session takes ownership of the signal alert"}
	route := "/api/v1/commands/runtime.incident.acknowledge"
	if response := req(h, "POST", route, in, nil, ""); response.Code != 401 {
		t.Fatal("anonymous ownership accepted", response.Code)
	}
	if response := req(h, "POST", route, in, cookie, ""); response.Code != 403 {
		t.Fatal("ownership bypassed session CSRF", response.Code)
	}
	response := req(h, "POST", route, in, cookie, csrf)
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	var owned p.Resource
	if err = json.Unmarshal(response.Body.Bytes(), &owned); err != nil {
		t.Fatal(err)
	}
	if owned.Status != "open" || p.S(owned.Data, "acknowledgedBy") != identity.ID {
		t.Fatal("HTTP acknowledgement lost identity/state", owned)
	}
	if response := req(h, "GET", "/api/v1/resources/"+owned.ID, nil, cookie, ""); response.Code != 200 {
		t.Fatal("acknowledged event not readable", response.Code)
	}
	tokenResponse := req(h, "POST", "/api/v1/tokens", p.M{"projectId": project.ID, "roles": []any{"release"}}, cookie, csrf)
	if tokenResponse.Code != 201 {
		t.Fatal(tokenResponse.Body.String())
	}
	var token p.M
	if err = json.Unmarshal(tokenResponse.Body.Bytes(), &token); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("POST", route, bytes.NewBufferString(`{"id":"`+owned.ID+`","key":"agent-ack","note":"Agent cannot take human ownership"}`))
	request.Header.Set("Authorization", "Bearer "+p.S(token, "token"))
	request.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, request)
	if w.Code != 403 {
		t.Fatal("agent token took human ownership", w.Code, w.Body.String())
	}
}

package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	p "github.com/intelligence-substrate/platform/internal/platform"
)

type traceHTTPFixture struct {
	h                                                                   *Handler
	actor                                                               p.Actor
	cookie                                                              *http.Cookie
	csrf                                                                string
	project, goal, plan, repository, artifact, proof, evidence, release p.Resource
}

func newTraceHTTPFixture(t *testing.T) traceHTTPFixture {
	t.Helper()
	h := setup(t)
	cookie, csrf := login(t, h)
	identity, _ := h.Auth.IdentityByEmail("owner@example.com")
	a := identityActor(identity)
	cmd := func(name string, in p.M) p.Resource { return authorityCommand(t, h, a, name, in) }
	project := cmd("projects.create", p.M{"title": "Acceptance trace"})
	g := cmd("goals.create", p.M{"projectId": project.ID, "title": "Request portal", "outcome": "Users submit requests", "criteria": []any{p.M{"id": "submit", "text": "A user submits a request", "method": "manual"}}, "targets": []any{"web"}, "budget": 50})
	cmd("goals.approve", p.M{"id": g.ID})
	plan := cmd("plans.propose", p.M{"goalId": g.ID, "summary": "Implement the portal", "tasks": []any{p.M{"id": "analysis", "title": "Review implementation", "kind": "analysis", "role": "analysis", "runtime": "linux"}}})
	plan = cmd("plans.approve", p.M{"id": plan.ID})
	repo := cmd("repositories.create", p.M{"projectId": project.ID, "title": "portal"})
	repo = cmd("repositories.commit", p.M{"id": repo.ID, "branch": "feature", "message": "Portal source", "files": p.M{"index.html": "<h1>Portal</h1>"}})
	upload := func(actor p.Actor, kind, name, body string) p.Resource {
		r, err := h.Service.Upload(context.Background(), actor, p.M{"goalId": g.ID, "repositoryId": repo.ID, "commit": p.S(repo.Data, "lastCommit"), "target": "web", "type": kind, "name": name}, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	artifact := upload(a, "package", "portal.html", "<h1>Portal</h1>")
	qa := p.Actor{ID: "trace-qa", Kind: "human", Admin: true}
	proof := upload(qa, "report", "manual.txt", "Actual local fixture verification")
	ev := authorityCommand(t, h, qa, "evidence.record", p.M{"artifactId": artifact.ID, "proofArtifactId": proof.ID, "method": "manual", "verificationEnvironment": "browser", "criteria": []any{"submit"}, "statement": "Request was submitted"})
	release := cmd("releases.create", p.M{"goalId": g.ID, "version": "1.0.0", "commit": p.S(repo.Data, "lastCommit"), "artifactIds": []any{artifact.ID}})
	return traceHTTPFixture{h, a, cookie, csrf, project, g, plan, repo, artifact, proof, ev, release}
}

func (f traceHTTPFixture) route() string {
	return "/api/v1/projects/" + f.project.ID + "/acceptance-trace?goalId=" + f.goal.ID
}
func traceObject(t *testing.T, body []byte) p.M {
	t.Helper()
	var result p.M
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}
	return result
}
func firstTraceCoverage(t *testing.T, report p.M) p.M {
	t.Helper()
	criteria := p.Items(report["criteria"])
	if len(criteria) == 0 {
		t.Fatal("missing requirement criteria", report)
	}
	targets := p.Items(p.Obj(criteria[0])["targets"])
	if len(targets) == 0 {
		t.Fatal("missing target coverage", report)
	}
	return p.Obj(targets[0])
}

func TestAcceptanceTraceHTTPExactEvidenceAndReadOnlyProvenance(t *testing.T) {
	f := newTraceHTTPFixture(t)
	before, _ := f.h.Service.Workspace(context.Background(), f.actor, f.project.ID)
	response := req(f.h, "GET", f.route(), nil, f.cookie, "")
	if response.Code != 200 {
		t.Fatal("acceptance trace unavailable", response.Code, response.Body.String())
	}
	report := traceObject(t, response.Body.Bytes())
	if !p.B(report, "current") || !p.B(report, "baselineApproved") || p.N(report, "requirementRevision") != 1 || !p.B(firstTraceCoverage(t, report), "covered") {
		t.Fatal("accurate independent evidence not traced", report)
	}
	artifacts := p.Items(report["artifacts"])
	if len(artifacts) != 1 {
		t.Fatal("report attachments were counted as packages", artifacts)
	}
	ar := p.Obj(artifacts[0])
	if p.S(ar, "id") != f.artifact.ID || p.S(ar, "repositoryId") != f.repository.ID || p.S(ar, "commit") != p.S(f.artifact.Data, "commit") || !p.B(ar, "integrityVerified") || !p.B(ar, "sourceVerified") || !p.B(ar, "environmentVerified") || ar["build"] != nil {
		t.Fatal("human upload invented build provenance", ar)
	}
	evs := p.Items(ar["evidence"])
	if len(evs) != 1 || p.S(p.Obj(evs[0]), "id") != f.evidence.ID || !p.B(p.Obj(evs[0]), "usable") || !p.B(p.Obj(evs[0]), "reportVerified") || p.S(p.Obj(evs[0]), "reportId") != f.proof.ID {
		t.Fatal("lost independently verified report", ar)
	}
	releases := p.Items(report["releases"])
	if len(releases) != 1 || p.S(p.Obj(releases[0]), "status") != "awaiting_approval" || !p.B(p.Obj(releases[0]), "gatePassed") || p.B(p.Obj(releases[0]), "approved") {
		t.Fatal("candidate was conflated with release approval", releases)
	}
	after, _ := f.h.Service.Workspace(context.Background(), f.actor, f.project.ID)
	if p.Hash(before) != p.Hash(after) {
		t.Fatal("trace read mutated resources")
	}
	if bytes.Contains(response.Body.Bytes(), []byte("leaseToken")) || bytes.Contains(response.Body.Bytes(), []byte("completionDigest")) {
		t.Fatal("trace leaked execution credentials")
	}
}

func TestAcceptanceTraceHTTPTamperedHumanReportCannotCoverOrAuthorizeRelease(t *testing.T) {
	f := newTraceHTTPFixture(t)
	if err := os.WriteFile(f.h.Service.ArtifactPath(p.S(f.proof.Data, "digest")), []byte("corrupt report"), 0600); err != nil {
		t.Fatal(err)
	}
	response := req(f.h, "GET", f.route(), nil, f.cookie, "")
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	report := traceObject(t, response.Body.Bytes())
	if p.B(firstTraceCoverage(t, report), "covered") {
		t.Fatal("corrupt manual report counted as verified", report)
	}
	ar := p.Obj(p.Items(report["artifacts"])[0])
	ev := p.Obj(p.Items(ar["evidence"])[0])
	if p.B(ev, "usable") || p.B(ev, "reportVerified") || len(p.Items(ev["issues"])) == 0 || p.B(p.Obj(p.Items(report["releases"])[0]), "gatePassed") {
		t.Fatal("invalid proof did not explain evidence and release gaps", report)
	}
	officer := p.Actor{ID: "trace-release-officer", Kind: "human", Admin: true}
	if _, err := f.h.Service.Command(context.Background(), officer, "releases.approve", p.M{"id": f.release.ID}); err == nil {
		t.Fatal("corrupt human report bypassed the actual release gate")
	}
}

func TestHumanAcceptanceReportRecheckedBeforeReleaseApproval(t *testing.T) {
	f := newTraceHTTPFixture(t)
	if err := os.WriteFile(f.h.Service.ArtifactPath(p.S(f.proof.Data, "digest")), []byte("corrupt after candidate creation"), 0600); err != nil {
		t.Fatal(err)
	}
	officer := p.Actor{ID: "independent-release-officer", Kind: "human", Admin: true}
	if _, err := f.h.Service.Command(context.Background(), officer, "releases.approve", p.M{"id": f.release.ID}); err == nil {
		t.Fatal("damaged manual acceptance attachment authorized release approval")
	}
}

func TestAcceptanceTraceHTTPHistoricalSnapshotNeverReusesCurrentCriterionID(t *testing.T) {
	f := newTraceHTTPFixture(t)
	authorityCommand(t, f.h, f.actor, "goals.revise", p.M{"id": f.goal.ID, "reason": "New scope", "criteria": []any{p.M{"id": "submit", "text": "Users submit encrypted requests", "method": "automated"}}, "targets": []any{"web", "ios-appstore"}})
	current := req(f.h, "GET", f.route(), nil, f.cookie, "")
	if current.Code != 200 {
		t.Fatal(current.Code, current.Body.String())
	}
	r := traceObject(t, current.Body.Bytes())
	if p.N(r, "requirementRevision") != 2 || p.B(firstTraceCoverage(t, r), "covered") || len(p.Items(r["artifacts"])) != 0 || len(p.Items(p.Obj(p.Items(r["criteria"])[0])["targets"])) != 2 {
		t.Fatal("old evidence or targets reused on revised baseline", r)
	}
	old := req(f.h, "GET", f.route()+"&revision=1", nil, f.cookie, "")
	if old.Code != 200 {
		t.Fatal(old.Code, old.Body.String())
	}
	r = traceObject(t, old.Body.Bytes())
	criterion := p.Obj(p.Items(r["criteria"])[0])
	if p.B(r, "current") || p.B(firstTraceCoverage(t, r), "covered") || p.S(criterion, "text") != "A user submits a request" || p.S(criterion, "method") != "manual" {
		t.Fatal("historical report borrowed live criterion or asserted current usability", r)
	}
	ar := p.Obj(p.Items(r["artifacts"])[0])
	ev := p.Obj(p.Items(ar["evidence"])[0])
	if p.S(ev, "status") != "stale" || p.B(ev, "usable") || p.S(p.Obj(p.Items(r["releases"])[0]), "status") != "stale" {
		t.Fatal("invented status before invalidation", r)
	}
	for _, revision := range []string{"0", "-1", "1.5", "wat", "99999999999999999999999", "1&revision=2"} {
		if out := req(f.h, "GET", f.route()+"&revision="+revision, nil, f.cookie, ""); out.Code != 400 {
			t.Fatal("invalid revision accepted", revision, out.Code)
		}
	}
	if out := req(f.h, "GET", f.route()+"&revision=8", nil, f.cookie, ""); out.Code != 404 {
		t.Fatal("missing snapshot accepted", out.Code)
	}
}

func TestAcceptanceTraceMCPParityAndCurrentMembership(t *testing.T) {
	f := newTraceHTTPFixture(t)
	mcp := func(cookie *http.Cookie, name string, args p.M) p.M {
		t.Helper()
		response := req(f.h, "POST", "/mcp", p.M{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": p.M{"name": name, "arguments": args}}, cookie, f.csrf)
		if response.Code != 200 {
			t.Fatal(response.Code, response.Body.String())
		}
		return traceObject(t, response.Body.Bytes())
	}
	rest := req(f.h, "GET", f.route(), nil, f.cookie, "")
	if rest.Code != 200 {
		t.Fatal(rest.Code, rest.Body.String())
	}
	response := mcp(f.cookie, "acceptance_trace", p.M{"projectId": f.project.ID, "goalId": f.goal.ID, "revision": 1})
	result := p.Obj(response["result"])
	if p.B(result, "isError") || response["error"] != nil {
		t.Fatal("MCP acceptance trace unavailable", response)
	}
	actual := p.Obj(p.Obj(result["structuredContent"])["resource"])
	expected := traceObject(t, rest.Body.Bytes())
	delete(actual, "generatedAt")
	delete(expected, "generatedAt")
	if p.Hash(actual) != p.Hash(expected) {
		t.Fatal("REST and MCP disagree on acceptance evidence", actual, expected)
	}
	for _, v := range []any{0, -1, 1.5, "1", nil} {
		out := mcp(f.cookie, "acceptance_trace", p.M{"projectId": f.project.ID, "goalId": f.goal.ID, "revision": v})
		if !p.B(p.Obj(out["result"]), "isError") {
			t.Fatal("MCP accepted invalid revision", v, out)
		}
	}
	if out := req(f.h, "GET", f.route(), nil, nil, ""); out.Code != 401 {
		t.Fatal("anonymous trace accepted", out.Code)
	}
	other := authorityCommand(t, f.h, f.actor, "projects.create", p.M{"title": "Unrelated project"})
	if out := req(f.h, "GET", "/api/v1/projects/"+other.ID+"/acceptance-trace?goalId="+f.goal.ID, nil, f.cookie, ""); out.Code != 403 {
		t.Fatal("cross-project goal trace accepted", out.Code)
	}
	viewer, err := f.h.Auth.CreateIdentity("trace-viewer@example.test", "Trace observer", "synthetic-trace-password-2026", false)
	if err != nil {
		t.Fatal(err)
	}
	member := authorityCommand(t, f.h, f.actor, "members.grant", p.M{"projectId": f.project.ID, "identityId": viewer.ID, "roles": []string{"viewer"}})
	logged := req(f.h, "POST", "/api/v1/auth/login", p.M{"email": viewer.Email, "password": "synthetic-trace-password-2026"}, nil, "")
	if logged.Code != 200 {
		t.Fatal(logged.Body.String())
	}
	viewerCookie := logged.Result().Cookies()[0]
	if out := req(f.h, "GET", f.route(), nil, viewerCookie, ""); out.Code != 200 {
		t.Fatal("viewer cannot read trace", out.Code)
	}
	delegated := req(f.h, "POST", "/api/v1/tokens", p.M{"projectId": f.project.ID}, f.cookie, f.csrf)
	if delegated.Code != 201 {
		t.Fatal(delegated.Body.String())
	}
	token := p.S(traceObject(t, delegated.Body.Bytes()), "token")
	r := httptest.NewRequest("POST", "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"acceptance_trace","arguments":{"projectId":"`+f.project.ID+`","goalId":"`+f.goal.ID+`"}}}`))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	f.h.ServeHTTP(recorder, r)
	if recorder.Code != 200 || p.B(p.Obj(traceObject(t, recorder.Body.Bytes())["result"]), "isError") {
		t.Fatal("scoped Agent read unavailable", recorder.Code, recorder.Body.String())
	}
	authorityCommand(t, f.h, f.actor, "members.revoke", p.M{"id": member.ID, "expectedRevision": member.Revision, "reason": "Observer left project"})
	if out := req(f.h, "GET", f.route(), nil, viewerCookie, ""); out.Code != 403 {
		t.Fatal("removed member retained trace access", out.Code)
	}
}

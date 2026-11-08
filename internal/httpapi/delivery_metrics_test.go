package httpapi

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	p "github.com/intelligence-substrate/platform/internal/platform"
)

func TestDeliveryMetricsHTTPHumanWritesAndReadOnlyMCPParity(t *testing.T) {
	f := newTraceHTTPFixture(t)
	route := "/api/v1/projects/" + f.project.ID + "/delivery-metrics?goalId=" + f.goal.ID
	in := p.M{"goalId": f.goal.ID, "type": "business_acceptance", "releaseId": f.release.ID, "proofArtifactId": f.proof.ID, "result": "failed", "reference": "review-1", "key": "review-1", "note": "Actual local synthetic HTTP observation"}
	if r := req(f.h, "POST", "/api/v1/commands/measurements.record", in, f.cookie, ""); r.Code != 403 {
		t.Fatal("observation bypassed CSRF", r.Code)
	}
	if r := req(f.h, "POST", "/api/v1/commands/measurements.record", in, f.cookie, f.csrf); r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	response := req(f.h, "GET", route, nil, f.cookie, "")
	if response.Code != 200 {
		t.Fatal("metrics unavailable", response.Code, response.Body.String())
	}
	report := traceObject(t, response.Body.Bytes())
	if p.N(report, "acceptanceSamples") != 1 || report["firstPassRate"] != float64(0) || report["meanPreviewSeconds"] != nil || report["totalCostPerSuccessfulDeliveryCents"] != nil {
		t.Fatal("wrong business or missing-data semantics", report)
	}
	listed := req(f.h, "POST", "/mcp", p.M{"jsonrpc": "2.0", "id": 1, "method": "tools/list"}, f.cookie, f.csrf)
	if !strings.Contains(listed.Body.String(), `"delivery_metrics"`) {
		t.Fatal("Agent cannot discover readonly metrics")
	}
	mcp := req(f.h, "POST", "/mcp", p.M{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": p.M{"name": "delivery_metrics", "arguments": p.M{"projectId": f.project.ID, "goalId": f.goal.ID}}}, f.cookie, f.csrf)
	result := p.Obj(traceObject(t, mcp.Body.Bytes())["result"])
	value := p.Obj(p.Obj(result["structuredContent"])["resource"])
	if p.B(result, "isError") || p.N(value, "acceptanceSamples") != 1 || value["firstPassRate"] != float64(0) {
		t.Fatal("MCP disagrees with HTTP", mcp.Body.String())
	}
	delegated := req(f.h, "POST", "/api/v1/tokens", p.M{"projectId": f.project.ID}, f.cookie, f.csrf)
	token := p.S(traceObject(t, delegated.Body.Bytes()), "token")
	for _, path := range []string{route, "/api/v1/commands/measurements.record"} {
		method, body, want := "GET", "", 200
		if strings.Contains(path, "commands/") {
			method, want = "POST", 403
			data, _ := json.Marshal(in)
			body = string(data)
		}
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		f.h.ServeHTTP(recorder, request)
		if recorder.Code != want {
			t.Fatal("Agent read/write boundary incorrect", path, recorder.Code, recorder.Body.String())
		}
	}
}

func TestDeliveryMetricsHTTPStrictScopeQueriesAndRevokedMembership(t *testing.T) {
	f := newTraceHTTPFixture(t)
	base := "/api/v1/projects/" + f.project.ID + "/delivery-metrics"
	for _, query := range []string{"?goalId=" + f.goal.ID + "&goalId=" + f.goal.ID, "?revision=1", "?goalId=" + f.goal.ID + "&revision=0", "?goalId=" + f.goal.ID + "&revision=-1", "?goalId=" + f.goal.ID + "&revision=1.5", "?goalId=" + f.goal.ID + "&revision=1&revision=1", "?unexpected=value"} {
		if r := req(f.h, "GET", base+query, nil, f.cookie, ""); r.Code != 400 {
			t.Fatal("ambiguous metrics query accepted", query, r.Code)
		}
	}
	if r := req(f.h, "GET", base, nil, nil, ""); r.Code != 401 {
		t.Fatal("anonymous metric read", r.Code)
	}
	other := authorityCommand(t, f.h, f.actor, "projects.create", p.M{"title": "Other metrics"})
	g := httpCostGoal(t, f.h, f.actor, other)
	if r := req(f.h, "GET", base+"?goalId="+g.ID, nil, f.cookie, ""); r.Code != 403 {
		t.Fatal("foreign goal admitted", r.Code)
	}
	if r := req(f.h, "GET", base+"?goalId="+f.goal.ID+"&revision=900", nil, f.cookie, ""); r.Code != 404 {
		t.Fatal("unknown baseline invented", r.Code)
	}
	viewer, err := f.h.Auth.CreateIdentity("metric-viewer@example.test", "Metrics observer", "synthetic-metric-password-2026", false)
	if err != nil {
		t.Fatal(err)
	}
	member := authorityCommand(t, f.h, f.actor, "members.grant", p.M{"projectId": f.project.ID, "identityId": viewer.ID, "roles": []string{"viewer"}})
	logged := req(f.h, "POST", "/api/v1/auth/login", p.M{"email": viewer.Email, "password": "synthetic-metric-password-2026"}, nil, "")
	cookie := logged.Result().Cookies()[0]
	if r := req(f.h, "GET", base, nil, cookie, ""); r.Code != 200 {
		t.Fatal("current viewer denied", r.Code)
	}
	authorityCommand(t, f.h, f.actor, "members.revoke", p.M{"id": member.ID, "expectedRevision": member.Revision, "reason": "No longer an observer"})
	if r := req(f.h, "GET", base, nil, cookie, ""); r.Code != 403 {
		t.Fatal("stale membership still authorizes metrics", r.Code)
	}
	bad := req(f.h, "POST", "/mcp", p.M{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": p.M{"name": "delivery_metrics", "arguments": p.M{"projectId": f.project.ID, "goalId": f.goal.ID, "revision": 1.5}}}, f.cookie, f.csrf)
	if !p.B(p.Obj(traceObject(t, bad.Body.Bytes())["result"]), "isError") {
		t.Fatal("MCP accepted fractional version")
	}
}

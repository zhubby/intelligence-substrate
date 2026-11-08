package httpapi

import (
	"bytes"
	"context"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	p "github.com/intelligence-substrate/platform/internal/platform"
)

func TestReleaseFeedbackHTTPExactHistoricalReportAndDownload(t *testing.T) {
	f := newTraceHTTPFixture(t)
	qa := p.Actor{ID: "feedback-release-officer", Kind: "human", Admin: true}
	authorityCommand(t, f.h, qa, "releases.approve", p.M{"id": f.release.ID})
	authorityCommand(t, f.h, qa, "releases.transition", p.M{"id": f.release.ID, "target": "web", "state": "published", "externalId": "synthetic-public-version", "proofArtifactId": f.proof.ID})
	authorityCommand(t, f.h, f.actor, "goals.revise", p.M{"id": f.goal.ID, "reason": "New scope removes Web", "targets": []any{"windows"}})
	query := url.Values{"artifactId": {f.artifact.ID}, "name": {"晚到问题.txt"}, "key": {"http-ticket"}}
	route := "/api/v1/releases/" + f.release.ID + "/reports?" + query.Encode()
	body := []byte("Synthetic late issue report after requirement revision\n")
	upload := func(csrf string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", route, bytes.NewReader(body))
		r.AddCookie(f.cookie)
		r.Header.Set("X-CSRF-Token", csrf)
		r.Header.Set("Content-Type", "application/octet-stream")
		w := httptest.NewRecorder()
		f.h.ServeHTTP(w, r)
		return w
	}
	if w := upload(""); w.Code != 403 {
		t.Fatal("report bypassed CSRF", w.Code)
	}
	w := upload(f.csrf)
	if w.Code != 200 {
		t.Fatal("historical upload failed", w.Code, w.Body.String())
	}
	report := traceObject(t, w.Body.Bytes())
	data := p.Obj(report["data"])
	if p.N(data, "requirementRevision") != 1 || p.S(data, "releaseId") != f.release.ID || p.S(data, "commit") != p.S(f.release.Data, "commit") || p.S(data, "purpose") != "release-feedback" {
		t.Fatal("HTTP source was caller-controlled", report)
	}
	if repeated := upload(f.csrf); p.S(traceObject(t, repeated.Body.Bytes()), "id") != p.S(report, "id") {
		t.Fatal("HTTP retry duplicated report", repeated.Code)
	}
	download := req(f.h, "GET", "/api/v1/artifacts/"+p.S(report, "id")+"/content", nil, f.cookie, "")
	if download.Code != 200 || !bytes.Equal(download.Body.Bytes(), body) || !strings.HasPrefix(download.Header().Get("Content-Disposition"), "attachment;") {
		t.Fatal("report bytes or attachment behavior changed", download.Code)
	}
	in := p.M{"goalId": f.goal.ID, "type": "production_defect", "releaseId": f.release.ID, "requirementRevision": 1, "proofArtifactId": p.S(report, "id"), "key": "late-http", "reference": "late-http", "note": "Synthetic record, no real production defect claim"}
	if r := req(f.h, "POST", "/api/v1/commands/measurements.record", in, f.cookie, f.csrf); r.Code != 200 {
		t.Fatal("old report cannot be used for its old version", r.Code, r.Body.String())
	}
	for _, revision := range []string{"", "&revision=1"} {
		r := req(f.h, "GET", "/api/v1/projects/"+f.project.ID+"/delivery-metrics?goalId="+f.goal.ID+revision, nil, f.cookie, "")
		got := p.N(traceObject(t, r.Body.Bytes()), "reportedProductionDefects")
		if (revision == "" && got != 0) || (revision != "" && got != 1) {
			t.Fatal("late defect crossed requirement boundary", revision, got)
		}
	}
}

func TestReleaseFeedbackHTTPRejectsAmbiguousQueriesAndDelegates(t *testing.T) {
	f := newTraceHTTPFixture(t)
	base := "/api/v1/releases/" + f.release.ID + "/reports"
	valid := "artifactId=" + f.artifact.ID + "&name=issue.txt&key=ticket"
	for _, query := range []string{"", valid + "&artifactId=" + f.artifact.ID, valid + "&name=other.txt", valid + "&key=duplicate", valid + "&revision=1", "artifactId=&name=issue.txt&key=ticket", valid + "&commit=forged", valid + "&name=%zz"} {
		r := httptest.NewRequest("POST", base+"?"+query, strings.NewReader("report"))
		r.AddCookie(f.cookie)
		r.Header.Set("X-CSRF-Token", f.csrf)
		w := httptest.NewRecorder()
		f.h.ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatal("ambiguous report scope accepted", query, w.Code, w.Body.String())
		}
	}
	if w := req(f.h, "POST", base+"?"+valid, nil, nil, ""); w.Code != 401 {
		t.Fatal("anonymous report write", w.Code)
	}
	token, _, err := f.h.Auth.IssueToken(context.Background(), f.actor, p.M{"projectId": f.project.ID})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", base+"?"+valid, strings.NewReader("report"))
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("Agent uploaded human production evidence", w.Code, w.Body.String())
	}
}

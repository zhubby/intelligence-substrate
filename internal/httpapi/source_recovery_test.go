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

func TestSourceRecoveryHTTPAndMCPRespectHumanDecisionBoundary(t *testing.T) {
	h := setup(t)
	cookie, csrf := login(t, h)
	identity, _ := h.Auth.IdentityByEmail("owner@example.com")
	a := p.Actor{ID: identity.ID, Kind: "human", Admin: true}
	command := func(name string, in p.M) p.Resource {
		t.Helper()
		v, e := h.Service.Command(context.Background(), a, name, in)
		if e != nil {
			t.Fatal(e)
		}
		return v
	}
	project := command("projects.create", p.M{"title": "Source recovery HTTP"})
	repo := command("repositories.create", p.M{"projectId": project.ID, "title": "source"})
	var body bytes.Buffer
	tw := tar.NewWriter(&body)
	_ = tw.WriteHeader(&tar.Header{Name: "app.txt", Mode: 0644, Size: 6})
	_, _ = tw.Write([]byte("source"))
	_ = tw.Close()
	if _, e := h.Service.Store.DB.Exec(`CREATE TRIGGER http_recovery_failure BEFORE UPDATE ON resources WHEN NEW.kind='source_commit' AND NEW.status='committed' BEGIN SELECT RAISE(ABORT,'injected'); END`); e != nil {
		t.Fatal(e)
	}
	if _, e := h.Service.CommitSnapshot(context.Background(), a, repo.ID, p.M{"key": "http-source", "branch": "feature", "baseCommit": p.S(repo.Data, "head"), "expectedHead": "", "message": "Interrupted source"}, &body); e == nil {
		t.Fatal("SQL fault not reached")
	}
	if _, e := h.Service.Store.DB.Exec(`DROP TRIGGER http_recovery_failure`); e != nil {
		t.Fatal(e)
	}
	ops, _ := h.Service.Store.List(project.ID, "source_commit", "")
	if len(ops) != 1 {
		t.Fatal("source missing")
	}
	op := ops[0]
	request := func(method, path string, input any, auth bool, token string) *httptest.ResponseRecorder {
		t.Helper()
		data, _ := json.Marshal(input)
		r := httptest.NewRequest(method, path, bytes.NewReader(data))
		if auth {
			r.AddCookie(cookie)
		}
		r.Header.Set("X-CSRF-Token", token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := request("GET", "/api/v1/source-operations/"+op.ID, nil, false, ""); w.Code != 401 {
		t.Fatal("anonymous source inspection", w.Code)
	}
	w := request("GET", "/api/v1/source-operations/"+op.ID, nil, true, "")
	if w.Code != 200 {
		t.Fatalf("inspection: %d %s", w.Code, w.Body.String())
	}
	var view p.M
	_ = json.Unmarshal(w.Body.Bytes(), &view)
	if !p.B(view, "canConfirm") || p.S(view, "observedHead") != p.S(op.Data, "commit") || bytes.Contains(w.Body.Bytes(), []byte("leaseToken")) {
		t.Fatal("inspection missing authoritative safe context", w.Body.String())
	}
	mcp := request("POST", "/mcp", p.M{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": p.M{"name": "inspect_source_operation", "arguments": p.M{"id": op.ID}}}, true, csrf)
	if mcp.Code != 200 || !bytes.Contains(mcp.Body.Bytes(), []byte(op.ID)) || bytes.Contains(mcp.Body.Bytes(), []byte(`"isError":true`)) {
		t.Fatal("MCP inspection unavailable", mcp.Body.String())
	}
	in := p.M{"id": op.ID, "key": "http-recovery", "commit": p.S(op.Data, "commit"), "observedHead": p.S(op.Data, "commit"), "decision": "confirm", "reason": "Read actual candidate before adoption"}
	if w := request("POST", "/api/v1/commands/sources.reconcile", in, true, ""); w.Code != 403 {
		t.Fatal("decision skipped session guard", w.Code)
	}
	token, _, e := h.Auth.IssueToken(context.Background(), a, p.M{"projectId": project.ID, "name": "agent inspector"})
	if e != nil {
		t.Fatal(e)
	}
	r := httptest.NewRequest("POST", "/api/v1/commands/sources.reconcile", bytes.NewReader(mustJSONForSource(t, in)))
	r.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("agent decided source recovery", w.Code, w.Body.String())
	}
	w = request("POST", "/api/v1/commands/sources.reconcile", in, true, csrf)
	if w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte(`"status":"confirmed"`)) {
		t.Fatal("human could not confirm", w.Code, w.Body.String())
	}
	w = request("GET", "/api/v1/source-operations/"+op.ID, nil, true, "")
	if w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte(`"recovery"`)) {
		t.Fatal("decision not inspectable", w.Body.String())
	}
}

func mustJSONForSource(t *testing.T, v any) []byte {
	t.Helper()
	b, e := json.Marshal(v)
	if e != nil {
		t.Fatal(e)
	}
	return b
}

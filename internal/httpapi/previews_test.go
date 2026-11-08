package httpapi

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	p "github.com/intelligence-substrate/platform/internal/platform"
)

type previewHTTPFixture struct {
	h                                *Handler
	a                                p.Actor
	cookie                           *http.Cookie
	csrf                             string
	project, goal, artifact, preview p.Resource
}

func previewHTTPArchive(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	for _, file := range []struct{ name, body string }{{"index.html", `<!doctype html><meta charset="utf-8"><script type="module" src="./assets/app.js"></script><h1>Preview sample</h1>`}, {"assets/app.js", `document.body.dataset.ready='yes'`}, {"data/items.json", `["one"]`}, {"image.svg", `<svg xmlns="http://www.w3.org/2000/svg"><script>location.href='https://example.test'</script></svg>`}} {
		if e := tw.WriteHeader(&tar.Header{Name: file.name, Mode: 0644, Typeflag: tar.TypeReg, Size: int64(len(file.body))}); e != nil {
			t.Fatal(e)
		}
		if _, e := tw.Write([]byte(file.body)); e != nil {
			t.Fatal(e)
		}
	}
	if e := tw.Close(); e != nil {
		t.Fatal(e)
	}
	return b.Bytes()
}

func makePreviewHTTPFixture(t *testing.T) previewHTTPFixture {
	t.Helper()
	h := setup(t)
	h.PublicURL = "http://127.0.0.1:18082"
	if e := h.ConfigurePreviews("127.0.0.1:18083", "http://localhost:18083", true); e != nil {
		t.Fatal(e)
	}
	cookie, csrf := login(t, h)
	identity, e := h.Auth.IdentityByEmail("owner@example.com")
	if e != nil {
		t.Fatal(e)
	}
	a := identityActor(identity)
	project := authorityCommand(t, h, a, "projects.create", p.M{"title": "Development previews"})
	g := authorityCommand(t, h, a, "goals.create", p.M{"projectId": project.ID, "title": "Preview application", "outcome": "Review the submitted customer application", "targets": []string{"web"}, "criteria": []any{p.M{"id": "request", "text": "Customer creates a request"}}})
	authorityCommand(t, h, a, "goals.approve", p.M{"id": g.ID})
	repo := authorityCommand(t, h, a, "repositories.create", p.M{"projectId": project.ID, "title": "Preview source"})
	artifact, e := h.Service.Upload(context.Background(), a, p.M{"goalId": g.ID, "repositoryId": repo.ID, "commit": p.S(repo.Data, "head"), "target": "web", "type": "package", "name": "application.tar"}, bytes.NewReader(previewHTTPArchive(t)))
	if e != nil {
		t.Fatal(e)
	}
	preview := authorityCommand(t, h, a, "previews.create", p.M{"artifactId": artifact.ID, "expectedRequirementRevision": g.Revision, "title": "Customer application preview", "reason": "Review the current application", "key": "preview"})
	return previewHTTPFixture{h: h, a: a, cookie: cookie, csrf: csrf, project: project, goal: g, artifact: artifact, preview: preview}
}

func (f previewHTTPFixture) access(t *testing.T) string {
	t.Helper()
	w := req(f.h, "POST", "/api/v1/previews/"+f.preview.ID+"/access", p.M{}, f.cookie, f.csrf)
	if w.Code != 200 {
		t.Fatal("preview access", w.Code, w.Body.String())
	}
	var result struct {
		URL       string `json:"url"`
		ExpiresAt string `json:"expiresAt"`
	}
	if e := json.Unmarshal(w.Body.Bytes(), &result); e != nil {
		t.Fatal(e)
	}
	u, e := url.Parse(result.URL)
	if e != nil || u.Host != "localhost:18083" || !strings.HasPrefix(u.Path, "/p/") || u.RawQuery != "" {
		t.Fatal("wrong isolated link", result)
	}
	deadline, e := time.Parse(time.RFC3339Nano, result.ExpiresAt)
	if e != nil || time.Until(deadline) > 10*time.Minute || time.Until(deadline) < 9*time.Minute {
		t.Fatal("incorrect bounded access", deadline, e)
	}
	return result.URL
}

func previewGatewayRequest(h *Handler, method, target string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, nil)
	if strings.HasSuffix(r.URL.Path, "/index.html") {
		r.Header.Set("Sec-Fetch-Dest", "iframe")
	}
	w := httptest.NewRecorder()
	h.PreviewGateway().ServeHTTP(w, r)
	return w
}

func TestDevelopmentPreviewHTTPIsolatedMultiFileGatewayAndCredentials(t *testing.T) {
	f := makePreviewHTTPFixture(t)
	link := f.access(t)
	root := previewGatewayRequest(f.h, "GET", link)
	if root.Code != 200 || !strings.Contains(root.Body.String(), `sandbox="allow-scripts"`) || !strings.Contains(root.Header().Get("Content-Security-Policy"), "frame-src http://localhost:18083/p/") {
		t.Fatal("preview wrapper missing isolation", root.Code, root.Header(), root.Body.String())
	}
	for _, name := range []string{"index.html", "assets/app.js", "data/items.json"} {
		w := previewGatewayRequest(f.h, "GET", link+"files/"+name)
		if w.Code != 200 || w.Header().Get("X-Substrate-Artifact-SHA256") != p.S(f.artifact.Data, "digest") || w.Header().Get("X-Substrate-Commit") != p.S(f.artifact.Data, "commit") || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("incorrect file binding", name, w.Code, w.Header())
		}
		if w.Header().Get("Access-Control-Allow-Origin") != "*" || w.Header().Get("Access-Control-Allow-Credentials") != "" || w.Header().Get("Set-Cookie") != "" {
			t.Fatal("static access leaked ambient credentials")
		}
		if !strings.Contains(w.Header().Get("Content-Security-Policy"), "sandbox allow-scripts;") || strings.Contains(w.Header().Get("Content-Security-Policy"), "allow-same-origin") {
			t.Fatal("application gained platform origin")
		}
	}
	for _, target := range []string{link + "files/../index.html", link + "files/%2e%2e/index.html", link + "files/.env", link + "files/missing.js", "http://localhost:18083/api/v1/projects", "http://localhost:18083/git/test.git"} {
		if w := previewGatewayRequest(f.h, "GET", target); w.Code != 404 {
			t.Fatal("gateway escaped static scope", target, w.Code)
		}
	}
	if w := previewGatewayRequest(f.h, "POST", link); w.Code != 405 {
		t.Fatal("gateway accepted mutation", w.Code)
	}
	token := strings.Split(strings.TrimPrefix(link, "http://localhost:18083/p/"), "/")[0]
	for _, basic := range []bool{false, true} {
		w := httptest.NewRecorder()
		f.h.ServeHTTP(w, credentialRequest(token, basic, "/api/v1/projects"))
		if w.Code != 401 {
			t.Fatal("preview link became control credential", basic, w.Code)
		}
	}
	workspace := req(f.h, "GET", "/api/v1/projects/"+f.project.ID+"/workspace", nil, f.cookie, "")
	audit := req(f.h, "GET", "/api/v1/audit?projectId="+f.project.ID, nil, f.cookie, "")
	if strings.Contains(workspace.Body.String(), token) || strings.Contains(audit.Body.String(), token) {
		t.Fatal("access secret persisted in product history")
	}
	if w := previewGatewayRequest(f.h, "GET", strings.Replace(link, "localhost:18083", "attacker.test", 1)); w.Code != 404 {
		t.Fatal("unconfigured Host served preview")
	}
	r := httptest.NewRequest("GET", link+"files/index.html", nil)
	r.Header.Set("Sec-Fetch-Dest", "document")
	w := httptest.NewRecorder()
	f.h.PreviewGateway().ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("application escaped navigation wrapper", w.Code)
	}
	r = httptest.NewRequest("GET", link+"files/image.svg", nil)
	r.Header.Set("Sec-Fetch-Dest", "document")
	w = httptest.NewRecorder()
	f.h.PreviewGateway().ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("executable SVG escaped navigation wrapper", w.Code)
	}
}

func TestDevelopmentPreviewHTTPCurrentStateAndParentSession(t *testing.T) {
	for _, change := range []string{"stop", "baseline", "expiry", "ticket-expiry", "logout", "identity-disabled", "parent-expired"} {
		t.Run(change, func(t *testing.T) {
			f := makePreviewHTTPFixture(t)
			link := f.access(t)
			switch change {
			case "stop":
				authorityCommand(t, f.h, f.a, "previews.stop", p.M{"id": f.preview.ID, "expectedRevision": 1, "reason": "Finished review", "key": "stop"})
			case "baseline":
				authorityCommand(t, f.h, f.a, "goals.revise", p.M{"id": f.goal.ID, "reason": "Scope changed", "outcome": "Customers can cancel their own requests"})
			case "expiry":
				e := f.h.Service.Store.Update(context.Background(), func(tx *p.Tx) error {
					r, e := tx.Get(f.preview.ID)
					if e != nil {
						return e
					}
					r.Data["expiresAt"] = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
					return tx.Put(r)
				})
				if e != nil {
					t.Fatal(e)
				}
			case "ticket-expiry", "parent-expired":
				kind := "preview"
				if change == "parent-expired" {
					kind = "session"
				}
				if _, e := f.h.Service.Store.DB.Exec(`UPDATE credentials SET expires_at=$1 WHERE kind=$2`, time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano), kind); e != nil {
					t.Fatal(e)
				}
			case "logout":
				if w := req(f.h, "POST", "/api/v1/auth/logout", p.M{}, f.cookie, f.csrf); w.Code != 200 {
					t.Fatal(w.Code)
				}
			case "identity-disabled":
				admin, e := f.h.Auth.CreateIdentity("second-admin@example.test", "Second admin", "another-preview-password-2026", true)
				if e != nil {
					t.Fatal(e)
				}
				authorityCommand(t, f.h, f.a, "members.grant", p.M{"projectId": f.project.ID, "identityId": admin.ID, "roles": []string{"owner"}})
				if e = f.h.Auth.Disable(context.Background(), identityActor(admin), f.a.ID); e != nil {
					t.Fatal(e)
				}
			}
			if w := previewGatewayRequest(f.h, "GET", link+"files/assets/app.js"); w.Code != 404 {
				t.Fatal("old link retained access", change, w.Code)
			}
		})
	}
}

func TestDevelopmentPreviewHTTPAccessCSRFAndDisabledConfiguration(t *testing.T) {
	f := makePreviewHTTPFixture(t)
	path := "/api/v1/previews/" + f.preview.ID + "/access"
	if w := req(f.h, "POST", path, p.M{}, f.cookie, ""); w.Code != 403 {
		t.Fatal("missing CSRF minted access", w.Code)
	}
	f.h.PreviewURL = ""
	if w := req(f.h, "POST", path, p.M{}, f.cookie, f.csrf); w.Code != 409 {
		t.Fatal("unconfigured gateway minted link", w.Code)
	}
	for _, config := range []struct {
		listen, public, platform string
		dev                      bool
	}{
		{"127.0.0.1:18083", "http://127.0.0.1:18083", "http://127.0.0.1:18082", true},
		{"127.0.0.1:18083", "http://remote.example", "https://platform.example", true},
		{"127.0.0.1:18083", "http://localhost:18083", "https://platform.example", false},
		{"127.0.0.1:18083", "https://preview.example/path", "https://platform.example", false},
		{"", "https://preview.example", "https://platform.example", false},
		{"127.0.0.1:18083", "https://preview.example;injected", "https://platform.example", false},
	} {
		f.h.PublicURL = config.platform
		if e := f.h.ConfigurePreviews(config.listen, config.public, config.dev); e == nil {
			t.Fatal("unsafe preview configuration accepted", config)
		}
	}
}

func TestDevelopmentPreviewHTTPAccessAuditFailureRollsBack(t *testing.T) {
	f := makePreviewHTTPFixture(t)
	if _, e := f.h.Service.Store.DB.Exec(`CREATE TRIGGER fail_preview_access BEFORE INSERT ON audit WHEN json_extract(NEW.body,'$.action')='previews.access.issue' BEGIN SELECT RAISE(FAIL,'injected preview audit failure'); END`); e != nil {
		t.Fatal(e)
	}
	w := req(f.h, "POST", "/api/v1/previews/"+f.preview.ID+"/access", p.M{}, f.cookie, f.csrf)
	if w.Code == 200 {
		t.Fatal("unaudited link issued")
	}
	var n int
	if e := f.h.Service.Store.DB.QueryRow(`SELECT count(*) FROM credentials WHERE kind='preview'`).Scan(&n); e != nil || n != 0 {
		t.Fatal("access credential survived failed transaction", n, e)
	}
}

func TestDevelopmentPreviewHTTPAccessQuotaAndParentDeadline(t *testing.T) {
	f := makePreviewHTTPFixture(t)
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(f.cookie)
	a, _, _, e := f.h.Auth.Authenticate(r)
	if e != nil {
		t.Fatal(e)
	}
	for range 20 {
		if _, e = f.h.Service.IssuePreviewAccess(context.Background(), a, f.preview.ID); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = f.h.Service.IssuePreviewAccess(context.Background(), a, f.preview.ID); e == nil {
		t.Fatal("unbounded preview access issuance")
	}
	if _, e = f.h.Service.Store.DB.Exec(`UPDATE credentials SET expires_at=$1 WHERE kind='preview'`, time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)); e != nil {
		t.Fatal(e)
	}
	parentDeadline := time.Now().Add(2 * time.Minute).UTC().Format(time.RFC3339Nano)
	if _, e = f.h.Service.Store.DB.Exec(`UPDATE credentials SET expires_at=$1 WHERE kind='session'`, parentDeadline); e != nil {
		t.Fatal(e)
	}
	fresh, e := f.h.Service.IssuePreviewAccess(context.Background(), a, f.preview.ID)
	if e != nil || fresh.ExpiresAt != parentDeadline {
		t.Fatal("access outlived parent session", fresh.ExpiresAt, e)
	}
	var count int
	if e = f.h.Service.Store.DB.QueryRow(`SELECT count(*) FROM credentials WHERE kind='preview'`).Scan(&count); e != nil || count != 1 {
		t.Fatal("expired access quota not reclaimed", count, e)
	}
}

func TestDevelopmentPreviewHTTPAgentCannotMintHumanAccess(t *testing.T) {
	f := makePreviewHTTPFixture(t)
	token, _, e := f.h.Auth.IssueToken(context.Background(), f.a, p.M{"projectId": f.project.ID})
	if e != nil {
		t.Fatal(e)
	}
	r := httptest.NewRequest("POST", "/api/v1/previews/"+f.preview.ID+"/access", strings.NewReader("{}"))
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("Agent minted a human browser session link", w.Code)
	}
}

func TestDevelopmentPreviewMCPAgentLifecycleAndAuthority(t *testing.T) {
	f := makePreviewHTTPFixture(t)
	issue := func(input p.M) string {
		t.Helper()
		token, _, err := f.h.Auth.IssueToken(context.Background(), f.a, input)
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	developer := issue(p.M{"projectId": f.project.ID})
	call := func(token, method string, params p.M) p.M {
		t.Helper()
		body, _ := json.Marshal(p.M{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
		r := httptest.NewRequest("POST", "/mcp", bytes.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		f.h.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var response p.M
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return p.Obj(response["result"])
	}
	command := func(token, name string, input p.M) p.M {
		return call(token, "tools/call", p.M{"name": "platform_command", "arguments": p.M{"command": name, "input": input}})
	}
	input := p.M{"artifactId": f.artifact.ID, "expectedRequirementRevision": f.goal.Revision, "title": "Agent review", "reason": "Review exact build", "key": "mcp-preview"}
	result := command(developer, "previews.create", input)
	created := p.Obj(p.Obj(result["structuredContent"])["resource"])
	id := p.S(created, "id")
	if p.B(result, "isError") || id == "" || p.S(created, "status") != "active" {
		t.Fatal("developer Agent cannot create a governed preview", result)
	}
	listed, _ := json.Marshal(call(developer, "tools/list", nil))
	for _, name := range []string{"previews.create", "previews.stop"} {
		if !bytes.Contains(listed, []byte(name)) {
			t.Fatal("preview command not discoverable", name)
		}
	}
	other := authorityCommand(t, f.h, f.a, "projects.create", p.M{"title": "Other project"})
	worker := authorityCommand(t, f.h, f.a, "workers.register", p.M{"projectId": f.project.ID, "title": "Build worker", "runtime": "linux", "roles": []string{"developer"}, "capabilities": []string{"build"}})
	for _, token := range []string{
		issue(p.M{"projectId": other.ID}),
		issue(p.M{"projectId": f.project.ID, "workerId": worker.ID}),
	} {
		if result = command(token, "previews.create", input); !p.B(result, "isError") {
			t.Fatal("unauthorized MCP creation/replay accepted", result)
		}
		if result = command(token, "previews.stop", p.M{"id": id, "expectedRevision": 1, "reason": "Invalid stop", "key": "forbidden"}); !p.B(result, "isError") {
			t.Fatal("unauthorized MCP stop accepted", result)
		}
	}
	viewer, err := f.h.Auth.CreateIdentity("mcp-preview-viewer@example.test", "Viewer", "mcp-preview-password-2026", false)
	if err != nil {
		t.Fatal(err)
	}
	authorityCommand(t, f.h, f.a, "members.grant", p.M{"projectId": f.project.ID, "identityId": viewer.ID, "roles": []string{"viewer"}})
	session, csrf, err := f.h.Auth.Login(viewer.Email, "mcp-preview-password-2026")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"previews.create", "previews.stop"} {
		response := req(f.h, "POST", "/mcp", p.M{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": p.M{"name": "platform_command", "arguments": p.M{"command": name, "input": p.M{"id": id, "artifactId": f.artifact.ID, "expectedRequirementRevision": f.goal.Revision, "title": "Forbidden", "reason": "Viewer cannot mutate", "key": "forbidden"}}}}, &http.Cookie{Name: "is_session", Value: session}, csrf.CSRF)
		if response.Code != 200 || !strings.Contains(response.Body.String(), `"isError":true`) {
			t.Fatal("viewer MCP mutation accepted", name, response.Code, response.Body.String())
		}
	}
	stop := p.M{"id": id, "expectedRevision": 1, "reason": "Review complete", "key": "mcp-stop"}
	result = command(developer, "previews.stop", stop)
	stopped := p.Obj(p.Obj(result["structuredContent"])["resource"])
	if p.B(result, "isError") || p.S(stopped, "status") != "stopped" || p.N(stopped, "revision") != 2 {
		t.Fatal("MCP stop did not preserve lifecycle", result)
	}
	result = command(developer, "previews.create", input)
	if p.S(p.Obj(p.Obj(result["structuredContent"])["resource"]), "status") != "stopped" {
		t.Fatal("MCP replay revived stopped preview", result)
	}
}

func TestDevelopmentPreviewHTTPCacheServesVerifiedBytesAndReloadRejectsCorruption(t *testing.T) {
	f := makePreviewHTTPFixture(t)
	link := f.access(t)
	first := previewGatewayRequest(f.h, "GET", link+"files/assets/app.js")
	if first.Code != 200 {
		t.Fatal(first.Code)
	}
	if e := os.WriteFile(f.h.Service.ArtifactPath(p.S(f.artifact.Data, "digest")), []byte("corrupt archive"), 0600); e != nil {
		t.Fatal(e)
	}
	second := previewGatewayRequest(f.h, "GET", link+"files/assets/app.js")
	if second.Code != 200 || second.Body.String() != first.Body.String() {
		t.Fatal("verified cache changed with corrupted source")
	}
	restarted := New(f.h.Service, false)
	restarted.PublicURL = f.h.PublicURL
	if e := restarted.ConfigurePreviews("127.0.0.1:18083", f.h.PreviewURL, true); e != nil {
		t.Fatal(e)
	}
	if w := previewGatewayRequest(restarted, "GET", link+"files/assets/app.js"); w.Code != 409 {
		t.Fatal("fresh gateway served unverified archive", w.Code)
	}
}

func TestDevelopmentPreviewHTTPDirectRemovalRevokesOnlyProjectLinks(t *testing.T) {
	f := makePreviewHTTPFixture(t)
	u, e := f.h.Auth.CreateIdentity("preview-direct@example.test", "Reviewer", "direct-preview-password-2026", false)
	if e != nil {
		t.Fatal(e)
	}
	member := authorityCommand(t, f.h, f.a, "members.grant", p.M{"projectId": f.project.ID, "identityId": u.ID, "roles": []string{"viewer"}})
	token, _, e := f.h.Auth.Login(u.Email, "direct-preview-password-2026")
	if e != nil {
		t.Fatal(e)
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: "is_session", Value: token})
	b, _, _, e := f.h.Auth.Authenticate(r)
	if e != nil {
		t.Fatal(e)
	}
	old, e := f.h.Service.IssuePreviewAccess(context.Background(), b, f.preview.ID)
	if e != nil {
		t.Fatal(e)
	}
	adminLink := f.access(t)
	authorityCommand(t, f.h, f.a, "members.revoke", p.M{"id": member.ID, "expectedRevision": 1, "reason": "Review complete"})
	if _, _, e = f.h.Service.ResolvePreviewAccess(context.Background(), old.Token); e == nil {
		t.Fatal("direct removal retained access")
	}
	authorityCommand(t, f.h, f.a, "members.grant", p.M{"projectId": f.project.ID, "identityId": u.ID, "roles": []string{"viewer"}, "expectedRevision": 2})
	if _, _, e = f.h.Service.ResolvePreviewAccess(context.Background(), old.Token); e == nil {
		t.Fatal("role return resurrected old link")
	}
	if w := previewGatewayRequest(f.h, "GET", adminLink); w.Code != 200 {
		t.Fatal("another issuer lost access", w.Code)
	}
	if _, e = f.h.Service.IssuePreviewAccess(context.Background(), b, f.preview.ID); e != nil {
		t.Fatal("current returned member cannot mint fresh access", e)
	}
}

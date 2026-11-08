package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	p "github.com/intelligence-substrate/platform/internal/platform"
)

func TestScopedTokenRevocationHumanGatesAndMCP(t *testing.T) {
	h := setup(t)
	cookie, csrf := login(t, h)
	w := req(h, "POST", "/api/v1/commands/projects.create", p.M{"title": "Token project"}, cookie, csrf)
	var project p.Resource
	_ = json.Unmarshal(w.Body.Bytes(), &project)
	identity, _ := h.Auth.IdentityByEmail("owner@example.com")
	owner := identityActor(identity)
	token, _, e := h.Auth.IssueToken(context.Background(), owner, p.M{"projectId": project.ID, "name": "Developer"})
	if e != nil {
		t.Fatal(e)
	}
	bearer := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		out := httptest.NewRecorder()
		h.ServeHTTP(out, r)
		return out
	}
	if out := bearer("GET", "/api/v1/projects/"+project.ID+"/workspace", ""); out.Code != 200 {
		t.Fatal(out.Body.String())
	}
	if out := bearer("POST", "/mcp", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"platform_command","arguments":{"command":"releases.approve","input":{}}}}`); out.Code != 200 || !strings.Contains(out.Body.String(), `"isError":true`) {
		t.Fatal("MCP exposed a human approval")
	}
	tokens, e := h.Auth.Tokens(context.Background(), owner, project.ID)
	if e != nil || len(tokens) != 1 {
		t.Fatal(e)
	}
	if e = h.Auth.Revoke(context.Background(), owner, p.M{"projectId": project.ID, "id": p.S(tokens[0], "id")}); e != nil {
		t.Fatal(e)
	}
	if out := bearer("GET", "/api/v1/projects", ""); out.Code != 401 {
		t.Fatal("revoked token still authenticated")
	}
}

func TestRealGitHTTPClonePushAndProtectedMain(t *testing.T) {
	if _, e := exec.LookPath("git"); e != nil {
		t.Skip("git unavailable")
	}
	h := setup(t)
	identity, _ := h.Auth.IdentityByEmail("owner@example.com")
	a := identityActor(identity)
	ctx := context.Background()
	project, e := h.Service.Command(ctx, a, "projects.create", p.M{"title": "Git transport"})
	if e != nil {
		t.Fatal(e)
	}
	repo, e := h.Service.Command(ctx, a, "repositories.create", p.M{"projectId": project.ID, "title": "source"})
	if e != nil {
		t.Fatal(e)
	}
	token, actor, e := h.Auth.IssueToken(ctx, a, p.M{"projectId": project.ID, "name": "Git developer"})
	if e != nil {
		t.Fatal(e)
	}
	server := httptest.NewServer(h)
	defer server.Close()
	dir := filepath.Join(t.TempDir(), "checkout")
	auth := "Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte("agent:"+token))
	git := func(args ...string) (string, error) {
		t.Helper()
		command := exec.Command("git", args...)
		command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http.extraHeader", "GIT_CONFIG_VALUE_0="+auth)
		b, e := command.CombinedOutput()
		return string(b), e
	}
	for _, args := range [][]string{{"clone", server.URL + "/git/" + repo.ID + ".git", dir}, {"-C", dir, "checkout", "-b", "feature"}} {
		if b, e := git(args...); e != nil {
			t.Fatalf("Git transport failed: %v %s", e, b)
		}
	}
	if e = os.WriteFile(filepath.Join(dir, "app.txt"), []byte("real Git HTTP\n"), 0600); e != nil {
		t.Fatal(e)
	}
	for _, args := range [][]string{{"-C", dir, "add", "app.txt"}, {"-C", dir, "-c", "user.name=Developer", "-c", "user.email=dev@example.test", "commit", "-m", "Add app"}, {"-C", dir, "push", "origin", "feature"}} {
		if b, e := git(args...); e != nil {
			t.Fatalf("Git push failed: %v %s", e, b)
		}
	}
	if b, e := git("-C", dir, "push", "origin", "HEAD:main"); e == nil || !strings.Contains(b, "protected") {
		t.Fatalf("direct main push was not rejected: %v %s", e, b)
	}
	if b, e := git("-C", dir, "push", "origin", "HEAD:refs/intelligence/source/reserved"); e == nil {
		t.Fatalf("client changed controller-owned source recovery ref: %s", b)
	}
	changes, _ := h.Service.Store.List(project.ID, "change", "")
	if len(changes) != 1 || p.S(changes[0].Data, "authorId") != actor.ID {
		t.Fatalf("wrong push provenance: %+v", changes)
	}
}

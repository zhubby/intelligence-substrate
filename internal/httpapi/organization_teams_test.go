package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	p "github.com/intelligence-substrate/platform/internal/platform"
)

func teamHTTPFixture(t *testing.T) (authorityFixture, p.Resource, p.Resource) {
	t.Helper()
	f := newAuthorityFixture(t)
	team := authorityCommand(t, f.h, f.admin, "teams.create", p.M{"title": "Enterprise engineering", "memberIds": []string{f.owner.ID}, "key": "create"})
	grant := authorityCommand(t, f.h, f.admin, "teams.grant", p.M{"projectId": f.project.ID, "teamId": team.ID, "expectedTeamRevision": 1, "roles": []string{"owner"}, "reason": "Team owns delivery", "key": "grant"})
	authorityCommand(t, f.h, f.admin, "members.revoke", p.M{"id": f.member.ID, "expectedRevision": f.member.Revision, "reason": "Inherited access only"})
	return f, team, grant
}

func sessionRequest(t *testing.T, h *Handler, identity Identity, path string) *httptest.ResponseRecorder {
	t.Helper()
	token, _, e := h.Auth.Login(identity.Email, "member-test-password-2026")
	if e != nil {
		t.Fatal(e)
	}
	r := httptest.NewRequest("GET", path, nil)
	r.AddCookie(&http.Cookie{Name: "is_session", Value: token})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// Catalog reads must use live owner authority while omitting global membership
// lists and other-project bindings; global detail stays administrator-only.
func TestOrganizationTeamCatalogBoundaries(t *testing.T) {
	f, team, _ := teamHTTPFixture(t)
	cookie, csrf := login(t, f.h)
	w := req(f.h, "GET", "/api/v1/teams", nil, cookie, csrf)
	if w.Code != 200 {
		t.Fatalf("administrator catalog unavailable: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), f.owner.ID) || strings.Contains(w.Body.String(), "memberIds") {
		t.Fatal("catalog leaked global member list")
	}
	w = sessionRequest(t, f.h, f.user, "/api/v1/teams?projectId="+f.project.ID)
	if w.Code != 200 || !strings.Contains(w.Body.String(), team.ID) || strings.Contains(w.Body.String(), f.owner.ID) {
		t.Fatal("owner catalog missing or leaked membership", w.Code)
	}
	for _, path := range []string{"/api/v1/teams", "/api/v1/resources/" + team.ID, "/api/v1/teams?projectId=unrelated"} {
		if w := sessionRequest(t, f.h, f.user, path); w.Code != 403 {
			t.Fatalf("global team access leaked %s: %d", path, w.Code)
		}
	}
	for _, query := range []string{"?projectId=" + f.project.ID + "&projectId=" + f.project.ID, "?projectId=", "?unknown=1", "?projectId=%ZZ"} {
		if w := req(f.h, "GET", "/api/v1/teams"+query, nil, cookie, csrf); w.Code != 400 {
			t.Fatalf("ambiguous catalog query accepted %s: %d", query, w.Code)
		}
	}
	if w := req(f.h, "GET", "/api/v1/resources/"+team.ID, nil, cookie, csrf); w.Code != 200 || !strings.Contains(w.Body.String(), f.owner.ID) {
		t.Fatal("administrator cannot inspect exact team", w.Code)
	}
}

func TestOrganizationTeamRevokesCachedAgentWorkerAndOwnerAuthority(t *testing.T) {
	for _, mode := range []string{"member", "team", "grant-roles", "grant-revoke", "identity"} {
		t.Run(mode, func(t *testing.T) {
			f, team, grant := teamHTTPFixture(t)
			ctx := context.Background()
			agent, _, e := f.h.Auth.IssueToken(ctx, f.owner, p.M{"projectId": f.project.ID})
			if e != nil {
				t.Fatal("inherited owner cannot delegate", e)
			}
			worker, _, e := f.h.Auth.IssueToken(ctx, f.owner, p.M{"projectId": f.project.ID, "workerId": f.worker.ID})
			if e != nil {
				t.Fatal(e)
			}
			cached := []p.Actor{cachedTokenActor(t, f.h, agent), cachedTokenActor(t, f.h, worker)}
			switch mode {
			case "member":
				authorityCommand(t, f.h, f.admin, "teams.update", p.M{"id": team.ID, "expectedRevision": 1, "title": team.Title, "memberIds": []string{}, "reason": "Transfer", "key": "remove"})
			case "team":
				authorityCommand(t, f.h, f.admin, "teams.disable", p.M{"id": team.ID, "expectedRevision": 1, "reason": "Suspend", "key": "disable"})
			case "grant-roles":
				authorityCommand(t, f.h, f.admin, "teams.grant", p.M{"projectId": f.project.ID, "teamId": team.ID, "expectedRevision": grant.Revision, "expectedTeamRevision": 1, "roles": []string{"viewer"}, "reason": "Reduce roles", "key": "viewer"})
			case "grant-revoke":
				authorityCommand(t, f.h, f.admin, "teams.revoke", p.M{"id": grant.ID, "expectedRevision": grant.Revision, "reason": "Project exit", "key": "revoke"})
			case "identity":
				if e = f.h.Auth.Disable(ctx, f.admin, f.owner.ID); e != nil {
					t.Fatal(e)
				}
			}
			for i, token := range []string{agent, worker} {
				for _, basic := range []bool{false, true} {
					w := httptest.NewRecorder()
					f.h.ServeHTTP(w, credentialRequest(token, basic, "/api/v1/projects"))
					if w.Code != 401 {
						t.Fatal("revoked token accepted", mode, i, basic, w.Code)
					}
				}
				if e = f.h.Service.CanAccess(ctx, cached[i], f.project.ID); e == nil {
					t.Fatal("cached token retained access")
				}
			}
			if _, _, e = f.h.Auth.IssueToken(ctx, f.owner, p.M{"projectId": f.project.ID}); e == nil {
				t.Fatal("cached human owner issued after loss of authority")
			}
			if e = f.h.Service.CanAccess(ctx, f.owner, f.project.ID, "owner"); e == nil {
				t.Fatal("cached owner retained team authority")
			}
			if mode == "grant-roles" {
				if e = f.h.Service.CanAccess(ctx, f.owner, f.project.ID, "viewer"); e != nil {
					t.Fatal("role reduction erased retained read access", e)
				}
			}
		})
	}
}

func TestOrganizationTeamHTTPMutationsRequireHumanAdminOwnerAndCSRF(t *testing.T) {
	f, team, _ := teamHTTPFixture(t)
	cookie, csrf := login(t, f.h)
	in := p.M{"id": team.ID, "expectedRevision": 1, "title": team.Title, "memberIds": []string{}, "reason": "Change", "key": "http"}
	if w := req(f.h, "POST", "/api/v1/commands/teams.update", in, cookie, ""); w.Code != 403 {
		t.Fatal("missing CSRF accepted")
	}
	token, _, e := f.h.Auth.IssueToken(context.Background(), f.owner, p.M{"projectId": f.project.ID})
	if e != nil {
		t.Fatal(e)
	}
	for _, command := range []string{"teams.update", "teams.grant", "teams.revoke"} {
		body, _ := json.Marshal(in)
		r := httptest.NewRequest("POST", "/api/v1/commands/"+command, strings.NewReader(string(body)))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		f.h.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatal("Agent mutated organization authority", command, w.Code)
		}
	}
	w := req(f.h, "POST", "/mcp", p.M{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": p.M{"name": "platform_command", "arguments": p.M{"command": "teams.update", "input": in}}}, cookie, csrf)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"isError":true`) {
		t.Fatal("MCP exposed team mutation")
	}
}

// Losing inherited ownership and issuing a token are serialized, so either
// issuance is denied, or its newly issued token is removed by the same change.
func TestOrganizationTeamConcurrentIssuanceCannotOutliveRemoval(t *testing.T) {
	f, team, _ := teamHTTPFixture(t)
	var issued string
	var issueErr, changeErr error
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		issued, _, issueErr = f.h.Auth.IssueToken(context.Background(), f.owner, p.M{"projectId": f.project.ID})
	}()
	go func() {
		defer wg.Done()
		<-start
		_, changeErr = f.h.Service.Command(context.Background(), f.admin, "teams.update", p.M{"id": team.ID, "expectedRevision": 1, "title": team.Title, "memberIds": []string{}, "reason": "Concurrent removal", "key": "concurrent"})
	}()
	close(start)
	wg.Wait()
	if changeErr != nil {
		t.Fatal(changeErr)
	}
	if issueErr == nil {
		if _, _, _, e := f.h.Auth.Authenticate(credentialRequest(issued, false, "/")); e == nil {
			t.Fatal("concurrent token escaped revocation")
		}
	}
}

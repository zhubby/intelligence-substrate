package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	p "github.com/intelligence-substrate/platform/internal/platform"
)

type authorityFixture struct {
	h                       *Handler
	admin, owner            p.Actor
	user                    Identity
	project, member, worker p.Resource
}

func newAuthorityFixture(t *testing.T) authorityFixture {
	t.Helper()
	h := setup(t)
	adminIdentity, err := h.Auth.IdentityByEmail("owner@example.com")
	if err != nil {
		t.Fatal(err)
	}
	admin := identityActor(adminIdentity)
	project := authorityCommand(t, h, admin, "projects.create", p.M{"title": "Delegation acceptance"})
	user, err := h.Auth.CreateIdentity("member@example.test", "Project owner", "member-test-password-2026", false)
	if err != nil {
		t.Fatal(err)
	}
	member := authorityCommand(t, h, admin, "members.grant", p.M{"projectId": project.ID, "identityId": user.ID, "roles": []string{"owner"}})
	token, _, err := h.Auth.Login(user.Email, "member-test-password-2026")
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: "is_session", Value: token})
	owner, _, _, err := h.Auth.Authenticate(r)
	if err != nil {
		t.Fatal(err)
	}
	worker := authorityCommand(t, h, admin, "workers.register", p.M{"projectId": project.ID, "title": "Owned Worker", "runtime": "linux", "roles": []string{"developer"}, "capabilities": []string{"code"}})
	return authorityFixture{h, admin, owner, user, project, member, worker}
}

func authorityCommand(t *testing.T, h *Handler, actor p.Actor, command string, in p.M) p.Resource {
	t.Helper()
	r, err := h.Service.Command(context.Background(), actor, command, in)
	if err != nil {
		t.Fatalf("%s: %v", command, err)
	}
	return r
}

func credentialRequest(token string, basic bool, path string) *http.Request {
	r := httptest.NewRequest("GET", path, nil)
	if basic {
		r.SetBasicAuth("agent", token)
	} else {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	return r
}

func cachedTokenActor(t *testing.T, h *Handler, token string) p.Actor {
	t.Helper()
	a, _, _, err := h.Auth.Authenticate(credentialRequest(token, false, "/"))
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestDelegatedAuthorityMembershipChanges(t *testing.T) {
	for _, action := range []string{"demote", "revoke"} {
		for _, kind := range []string{"agent", "worker"} {
			t.Run(action+"-"+kind, func(t *testing.T) {
				f := newAuthorityFixture(t)
				in := p.M{"projectId": f.project.ID}
				if kind == "worker" {
					in["workerId"] = f.worker.ID
				}
				token, _, err := f.h.Auth.IssueToken(context.Background(), f.owner, in)
				if err != nil {
					t.Fatal(err)
				}
				cached := cachedTokenActor(t, f.h, token)
				unrelated, _, err := f.h.Auth.IssueToken(context.Background(), f.admin, p.M{"projectId": f.project.ID})
				if err != nil {
					t.Fatal(err)
				}
				second := authorityCommand(t, f.h, f.admin, "projects.create", p.M{"title": "Unaffected project"})
				authorityCommand(t, f.h, f.admin, "members.grant", p.M{"projectId": second.ID, "identityId": f.owner.ID, "roles": []string{"owner"}})
				otherProject, _, err := f.h.Auth.IssueToken(context.Background(), f.owner, p.M{"projectId": second.ID})
				if err != nil {
					t.Fatal(err)
				}
				if action == "demote" {
					authorityCommand(t, f.h, f.admin, "members.grant", p.M{"projectId": f.project.ID, "identityId": f.owner.ID, "roles": []string{"viewer"}, "expectedRevision": f.member.Revision})
				} else {
					authorityCommand(t, f.h, f.admin, "members.revoke", p.M{"id": f.member.ID, "expectedRevision": f.member.Revision, "reason": "Role transfer"})
				}
				for _, basic := range []bool{false, true} {
					w := httptest.NewRecorder()
					f.h.ServeHTTP(w, credentialRequest(token, basic, "/api/v1/projects/"+f.project.ID+"/workspace"))
					if w.Code != 401 {
						t.Fatalf("removed delegation reused over HTTP basic=%t: %d", basic, w.Code)
					}
				}
				if err = f.h.Service.CanAccess(context.Background(), cached, f.project.ID); err == nil {
					t.Fatal("cached delegated actor retained access")
				}
				if _, err = f.h.Service.Command(context.Background(), cached, "messages.send", p.M{"projectId": f.project.ID, "recipient": f.admin.ID, "type": "request", "key": "revoked", "payload": p.M{"text": "stale"}}); err == nil {
					t.Fatal("cached delegated actor mutated state")
				}
				for _, valid := range []string{unrelated, otherProject} {
					w := httptest.NewRecorder()
					f.h.ServeHTTP(w, credentialRequest(valid, false, "/api/v1/projects"))
					if w.Code != 200 {
						t.Fatalf("unrelated delegation removed: %d %s", w.Code, w.Body.String())
					}
				}
				authorityCommand(t, f.h, f.admin, "members.grant", p.M{"projectId": f.project.ID, "identityId": f.owner.ID, "roles": []string{"owner"}})
				if _, _, _, err = f.h.Auth.Authenticate(credentialRequest(token, false, "/")); err == nil {
					t.Fatal("promotion resurrected old delegation")
				}
			})
		}
	}
}

func TestCachedCredentialAuthorityRejectsRevocationAndTampering(t *testing.T) {
	for _, mode := range []string{"token-revoke", "session-logout", "actor-id", "actor-project", "actor-kind", "global-disable"} {
		t.Run(mode, func(t *testing.T) {
			f := newAuthorityFixture(t)
			token, _, err := f.h.Auth.IssueToken(context.Background(), f.owner, p.M{"projectId": f.project.ID})
			if err != nil {
				t.Fatal(err)
			}
			actor := cachedTokenActor(t, f.h, token)
			switch mode {
			case "token-revoke":
				err = f.h.Auth.Revoke(context.Background(), f.admin, p.M{"projectId": f.project.ID, "id": p.Hash(token)})
			case "session-logout":
				actor = f.owner
				cookie, _, e := f.h.Auth.Login(f.user.Email, "member-test-password-2026")
				if e != nil {
					t.Fatal(e)
				}
				r := httptest.NewRequest("POST", "/", nil)
				r.AddCookie(&http.Cookie{Name: "is_session", Value: cookie})
				actor, _, _, e = f.h.Auth.Authenticate(r)
				if e != nil {
					t.Fatal(e)
				}
				f.h.Auth.Logout(r)
			case "actor-id":
				actor.ID = "forged-actor"
			case "actor-project":
				actor.ProjectID = "forged-project"
			case "actor-kind":
				actor.Kind = "human"
				actor.Admin = true
			case "global-disable":
				err = f.h.Auth.Disable(context.Background(), f.admin, f.user.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = f.h.Service.CanAccess(context.Background(), actor, f.project.ID); err == nil {
				t.Fatal("stale/tampered actor retained authorization")
			}
			if _, err = f.h.Service.Command(context.Background(), actor, "projects.create", p.M{"title": "Forged"}); err == nil {
				t.Fatal("stale/tampered actor created project")
			}
		})
	}
}

func TestCachedWorkerAuthorityUsesCurrentRolesAndStatus(t *testing.T) {
	for _, mode := range []string{"roles", "disabled"} {
		t.Run(mode, func(t *testing.T) {
			f := newAuthorityFixture(t)
			token, _, err := f.h.Auth.IssueToken(context.Background(), f.owner, p.M{"projectId": f.project.ID, "workerId": f.worker.ID})
			if err != nil {
				t.Fatal(err)
			}
			cached := cachedTokenActor(t, f.h, token)
			if mode == "roles" {
				authorityCommand(t, f.h, f.admin, "workers.update", p.M{"id": f.worker.ID, "projectId": f.project.ID, "title": f.worker.Title, "runtime": "linux", "roles": []string{"quality"}, "capabilities": []string{"test"}})
			} else {
				authorityCommand(t, f.h, f.admin, "workers.disable", p.M{"id": f.worker.ID})
			}
			if err = f.h.Service.CanAccess(context.Background(), cached, f.project.ID, "developer"); err == nil {
				t.Fatal("cached worker retained old role")
			}
			if mode == "roles" {
				if err = f.h.Service.CanAccess(context.Background(), cached, f.project.ID, "quality"); err != nil {
					t.Fatal("current worker role unavailable", err)
				}
			}
		})
	}
}

func TestTokenIssuanceAuditFailureRollsBackCredential(t *testing.T) {
	f := newAuthorityFixture(t)
	var before, after int
	if err := f.h.Auth.Store.DB.QueryRow(`SELECT count(*) FROM credentials`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := f.h.Auth.Store.DB.Exec(`CREATE TRIGGER token_audit_failure BEFORE INSERT ON audit BEGIN SELECT RAISE(FAIL, 'injected token audit failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.h.Auth.IssueToken(context.Background(), f.owner, p.M{"projectId": f.project.ID}); err == nil {
		t.Fatal("audit failure ignored")
	}
	if err := f.h.Auth.Store.DB.QueryRow(`SELECT count(*) FROM credentials`).Scan(&after); err != nil || after != before {
		t.Fatal("unaudited credential survived", before, after, err)
	}
}

func TestIssuanceRechecksIdentitySessionProjectAndWorker(t *testing.T) {
	for _, mode := range []string{"missing-project", "disabled-worker", "disabled-session", "logged-out-session"} {
		t.Run(mode, func(t *testing.T) {
			f := newAuthorityFixture(t)
			issuer := f.owner
			in := p.M{"projectId": f.project.ID}
			switch mode {
			case "missing-project":
				issuer = f.admin
				in["projectId"] = "project_missing"
			case "disabled-worker":
				authorityCommand(t, f.h, f.admin, "workers.disable", p.M{"id": f.worker.ID})
				in["workerId"] = f.worker.ID
			case "disabled-session":
				if err := f.h.Auth.Disable(context.Background(), f.admin, f.user.ID); err != nil {
					t.Fatal(err)
				}
			case "logged-out-session":
				token, _, err := f.h.Auth.Login(f.user.Email, "member-test-password-2026")
				if err != nil {
					t.Fatal(err)
				}
				r := httptest.NewRequest("POST", "/", nil)
				r.AddCookie(&http.Cookie{Name: "is_session", Value: token})
				issuer, _, _, err = f.h.Auth.Authenticate(r)
				if err != nil {
					t.Fatal(err)
				}
				f.h.Auth.Logout(r)
			}
			if _, _, err := f.h.Auth.IssueToken(context.Background(), issuer, in); err == nil {
				t.Fatal("invalid authority minted credential")
			}
		})
	}
}

func TestIdentityDisableProtectsOwnerAndPreservesBinding(t *testing.T) {
	f := newAuthorityFixture(t)
	members, err := f.h.Auth.Store.List(f.project.ID, "member", "")
	if err != nil {
		t.Fatal(err)
	}
	var creator p.Resource
	for _, m := range members {
		if p.S(m.Data, "identityId") == f.admin.ID {
			creator = m
		}
	}
	removed := authorityCommand(t, f.h, f.owner, "members.revoke", p.M{"id": creator.ID, "expectedRevision": creator.Revision, "reason": "Transfer ownership"})
	if err = f.h.Auth.Disable(context.Background(), f.admin, f.user.ID); err == nil {
		t.Fatal("globally disabled the last project owner")
	}
	authorityCommand(t, f.h, f.admin, "members.grant", p.M{"projectId": f.project.ID, "identityId": f.admin.ID, "roles": []string{"owner"}, "expectedRevision": removed.Revision})
	if err = f.h.Auth.Store.Update(context.Background(), func(tx *p.Tx) error { return tx.BindOIDCSubject(f.user.ID, "latest-oidc-subject") }); err != nil {
		t.Fatal(err)
	}
	if err = f.h.Auth.Disable(context.Background(), f.admin, f.user.ID); err != nil {
		t.Fatal(err)
	}
	current, err := f.h.Auth.identity(f.user.ID)
	if err != nil || !current.Disabled || current.OIDCSub != "latest-oidc-subject" {
		t.Fatal("identity disable lost current binding", current, err)
	}
	m, err := f.h.Auth.Store.Get(f.member.ID)
	if err != nil || m.Status != "revoked" || p.S(m.Data, "revokedBy") != f.admin.ID {
		t.Fatal("global disable retained active membership", m, err)
	}
	if _, _, err = f.h.Auth.newSession(f.user); err == nil {
		t.Fatal("stale pre-disable identity minted session")
	}
	if _, _, err = f.h.Auth.IssueToken(context.Background(), f.owner, p.M{"projectId": f.project.ID}); err == nil {
		t.Fatal("disabled cached issuer minted token")
	}
	audits, err := f.h.Auth.Store.Audits(f.project.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, a := range audits {
		if a.Action == "members.revoke" && a.ResourceID == f.member.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("project offboarding not audited")
	}
}

func TestConcurrentIssuanceAndDemotionLeavesNoDelegatedCredential(t *testing.T) {
	f := newAuthorityFixture(t)
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make(chan error, 9)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, _, err := f.h.Auth.IssueToken(context.Background(), f.owner, p.M{"projectId": f.project.ID})
			if err != nil {
				var fault *p.Fault
				if !errors.As(err, &fault) || fault.Status != 403 {
					results <- err
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		_, err := f.h.Service.Command(context.Background(), f.admin, "members.grant", p.M{"projectId": f.project.ID, "identityId": f.owner.ID, "roles": []string{"viewer"}})
		results <- err
	}()
	close(start)
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := f.h.Auth.Store.DB.QueryRow(`SELECT count(*) FROM credentials WHERE identity_id=$1 AND kind='token'`, f.owner.ID).Scan(&n); err != nil || n != 0 {
		t.Fatal("issuance raced past demotion", n, err)
	}
}

func TestGovernanceCannotBeDelegatedViaMCP(t *testing.T) {
	f := newAuthorityFixture(t)
	token, _, err := f.h.Auth.IssueToken(context.Background(), f.owner, p.M{"projectId": f.project.ID})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"platform_command","arguments":{"command":"members.revoke","input":{}}}}`))
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	var out p.M
	if err = json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"isError":true`) {
		t.Fatal("MCP exposed membership governance", w.Body.String())
	}
}

func TestDelegatedAuthorityRejectsLegacyOwnerLoss(t *testing.T) {
	f := newAuthorityFixture(t)
	token, _, err := f.h.Auth.IssueToken(context.Background(), f.owner, p.M{"projectId": f.project.ID})
	if err != nil {
		t.Fatal(err)
	}
	cached := cachedTokenActor(t, f.h, token)
	err = f.h.Auth.Store.Update(context.Background(), func(tx *p.Tx) error {
		member, e := tx.Get(f.member.ID)
		if e != nil {
			return e
		}
		member.Data["roles"] = []string{"viewer"}
		return tx.Put(member)
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = f.h.Auth.Authenticate(credentialRequest(token, false, "/")); err == nil {
		t.Fatal("legacy token outlived current issuer ownership")
	}
	if err = f.h.Service.CanAccess(context.Background(), cached, f.project.ID); err == nil {
		t.Fatal("cached legacy token outlived issuer ownership")
	}
}

func TestIdentityDisableAuditFailureRollsBackEntireOffboarding(t *testing.T) {
	f := newAuthorityFixture(t)
	token, _, err := f.h.Auth.IssueToken(context.Background(), f.owner, p.M{"projectId": f.project.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.h.Auth.Store.DB.Exec(`CREATE TRIGGER offboarding_audit_failure BEFORE INSERT ON audit BEGIN SELECT RAISE(FAIL, 'injected offboarding audit failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err = f.h.Auth.Disable(context.Background(), f.admin, f.user.ID); err == nil {
		t.Fatal("offboarding ignored audit failure")
	}
	u, err := f.h.Auth.identity(f.user.ID)
	if err != nil || u.Disabled {
		t.Fatal("failed offboarding disabled identity", err)
	}
	m, err := f.h.Auth.Store.Get(f.member.ID)
	if err != nil || m.Status != "active" || m.Revision != f.member.Revision {
		t.Fatal("failed offboarding changed membership", m, err)
	}
	if _, _, _, err = f.h.Auth.Authenticate(credentialRequest(token, false, "/")); err != nil {
		t.Fatal("failed offboarding deleted credential", err)
	}
}

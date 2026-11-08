package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	p "github.com/intelligence-substrate/platform/internal/platform"
)

// Actual two-connection contention checks issuance, exact mutation replay and
// scoped revocation together. The archive is then restored into SQLite, with a
// pristine copy retained for independent SQL/tar readback outside this test.
func TestPostgresOrganizationTeamsConcurrentIssuanceAndRestore(t *testing.T) {
	h, other := postgresAuthorityHandlers(t)
	root := t.TempDir()
	if evidence := os.Getenv("IS_ORGANIZATION_EVIDENCE_DIR"); evidence != "" {
		root = filepath.Join(evidence, "organization-postgres-proof")
		if e := os.Mkdir(root, 0700); e != nil {
			t.Fatal(e)
		}
	}
	h.Service.Root, other.Service.Root = filepath.Join(root, "source"), filepath.Join(root, "source")
	admin, e := h.Auth.CreateIdentity("teams-pg-admin@example.test", "Enterprise administrator", "organization-test-password-2026", true)
	if e != nil {
		t.Fatal(e)
	}
	user, e := h.Auth.CreateIdentity("teams-pg-owner@example.test", "Inherited project owner", "organization-test-password-2026", false)
	if e != nil {
		t.Fatal(e)
	}
	a, b := identityActor(admin), identityActor(user)
	ctx := context.Background()
	project := authorityCommand(t, h, a, "projects.create", p.M{"title": "Team-governed delivery"})
	team := authorityCommand(t, h, a, "teams.create", p.M{"title": "Enterprise engineering", "memberIds": []string{b.ID}, "key": "pg-create"})
	grant := authorityCommand(t, h, a, "teams.grant", p.M{"projectId": project.ID, "teamId": team.ID, "expectedTeamRevision": 1, "roles": []string{"owner"}, "reason": "Delegate delivery", "key": "pg-grant"})
	worker := authorityCommand(t, h, a, "workers.register", p.M{"projectId": project.ID, "title": "Team Worker", "roles": []string{"developer"}, "runtime": "linux", "capabilities": []string{"code"}})
	session, _, e := h.Auth.Login(user.Email, "organization-test-password-2026")
	if e != nil {
		t.Fatal(e)
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: "is_session", Value: session})
	b, _, _, e = h.Auth.Authenticate(r)
	if e != nil {
		t.Fatal(e)
	}
	oldTokens := []string{}
	for _, input := range []p.M{{"projectId": project.ID}, {"projectId": project.ID, "workerId": worker.ID}} {
		token, _, e := h.Auth.IssueToken(ctx, b, input)
		if e != nil {
			t.Fatal(e)
		}
		oldTokens = append(oldTokens, token)
	}
	unrelated := authorityCommand(t, h, a, "projects.create", p.M{"title": "Unrelated direct authority"})
	authorityCommand(t, h, a, "members.grant", p.M{"projectId": unrelated.ID, "identityId": b.ID, "roles": []string{"owner"}})
	otherProject, _, e := h.Auth.IssueToken(ctx, b, p.M{"projectId": unrelated.ID})
	if e != nil {
		t.Fatal(e)
	}
	otherIssuer, _, e := h.Auth.IssueToken(ctx, a, p.M{"projectId": project.ID})
	if e != nil {
		t.Fatal(e)
	}
	remove := p.M{"id": team.ID, "expectedRevision": 1, "title": team.Title, "memberIds": []string{}, "reason": "Concurrent transfer", "key": "pg-remove"}
	start := make(chan struct{})
	results := make(chan error, 14)
	var wg sync.WaitGroup
	var issued, rejected, acknowledgements atomic.Int32
	for i := range 12 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			auth := h.Auth
			if i%2 == 0 {
				auth = other.Auth
			}
			_, _, e := auth.IssueToken(ctx, b, p.M{"projectId": project.ID})
			if e == nil {
				issued.Add(1)
				return
			}
			var fault *p.Fault
			if !errors.As(e, &fault) || fault.Status != 403 {
				results <- e
			} else {
				rejected.Add(1)
			}
		}(i)
	}
	for _, handler := range []*Handler{h, other} {
		wg.Add(1)
		go func(handler *Handler) {
			defer wg.Done()
			<-start
			got, e := handler.Service.Command(ctx, a, "teams.update", remove)
			if e != nil {
				results <- e
				return
			}
			if got.ID != team.ID || got.Revision != 2 {
				results <- errors.New("exact concurrent team mutation diverged")
				return
			}
			acknowledgements.Add(1)
		}(handler)
	}
	close(start)
	wg.Wait()
	close(results)
	for e := range results {
		if e != nil {
			t.Fatal(e)
		}
	}
	if acknowledgements.Load() != 2 || issued.Load()+rejected.Load() != 12 {
		t.Fatal("missing contention results")
	}
	var n int
	if e = h.Auth.Store.DB.QueryRow(`SELECT count(*) FROM credentials WHERE identity_id=$1 AND kind='token' AND body LIKE $2`, b.ID, "%"+project.ID+"%").Scan(&n); e != nil || n != 0 {
		t.Fatal("issuance escaped team removal", n, e)
	}
	for _, token := range oldTokens {
		if _, _, _, e = other.Auth.Authenticate(credentialRequest(token, false, "/")); e == nil {
			t.Fatal("old delegated token survived")
		}
	}
	for _, token := range []string{otherProject, otherIssuer} {
		if _, _, _, e = other.Auth.Authenticate(credentialRequest(token, false, "/")); e != nil {
			t.Fatal("unrelated credentials removed", e)
		}
	}
	if _, _, _, e = other.Auth.Authenticate(r); e != nil {
		t.Fatal("team change removed human session", e)
	}
	current := authorityCommand(t, other, a, "teams.update", p.M{"id": team.ID, "expectedRevision": 2, "title": team.Title, "memberIds": []string{b.ID}, "reason": "Explicit department return", "key": "pg-return"})
	for _, token := range oldTokens {
		if _, _, _, e = other.Auth.Authenticate(credentialRequest(token, false, "/")); e == nil {
			t.Fatal("return resurrected old credential")
		}
	}
	if e = h.Service.VerifyIntegrity(ctx); e != nil {
		t.Fatal(e)
	}
	archive := filepath.Join(root, "backup.tar.gz")
	if e = h.Service.Backup(ctx, archive); e != nil {
		t.Fatal(e)
	}
	destination := filepath.Join(root, "restored")
	if e = p.Restore(ctx, archive, destination, ""); e != nil {
		t.Fatal(e)
	}
	pristine, e := os.ReadFile(filepath.Join(destination, "platform.db"))
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(root, "pristine.db"), pristine, 0600); e != nil {
		t.Fatal(e)
	}
	store, e := p.Open(filepath.Join(destination, "platform.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	restored := New(&p.Service{Store: store, Root: destination}, false)
	if e = restored.Service.VerifyIntegrity(ctx); e != nil {
		t.Fatal(e)
	}
	if e = store.DB.QueryRow(`SELECT count(*) FROM credentials`).Scan(&n); e != nil || n != 0 {
		t.Fatal("restore retained credentials", n, e)
	}
	fresh, _, e := restored.Auth.Login(user.Email, "organization-test-password-2026")
	if e != nil {
		t.Fatal(e)
	}
	r = httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: "is_session", Value: fresh})
	restoredActor, _, _, e := restored.Auth.Authenticate(r)
	if e != nil {
		t.Fatal(e)
	}
	if e = restored.Service.CanAccess(ctx, restoredActor, project.ID, "owner"); e != nil {
		t.Fatal("restore lost inherited authority", e)
	}
	replay := authorityCommand(t, restored, a, "teams.update", remove)
	if replay.Revision != current.Revision || !p.Has(p.Strings(replay.Data["memberIds"]), b.ID) {
		t.Fatal("restore replay applied obsolete mutation")
	}
	facts := p.M{"passed": true, "syntheticOnly": true, "projectId": project.ID, "unrelatedProjectId": unrelated.ID, "teamId": team.ID, "teamRevision": current.Revision, "grantId": grant.ID, "ownerId": b.ID, "administratorId": a.ID, "concurrentIssuanceAttempts": 12, "issuedBeforeRemoval": issued.Load(), "rejectedAfterRemoval": rejected.Load(), "concurrentAcknowledgements": acknowledgements.Load(), "unrelatedTokensPreserved": 2, "humanSessionPreserved": true, "restoredCredentials": 0, "restoredCurrentAuthority": true, "obsoleteReplayMutated": false, "paidModelCalled": false}
	encoded, e := json.MarshalIndent(facts, "", "  ")
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(root, "facts.json"), append(encoded, '\n'), 0600); e != nil {
		t.Fatal(e)
	}
}

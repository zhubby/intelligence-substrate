package httpapi

import (
	"bytes"
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
	"github.com/intelligence-substrate/platform/internal/webpreview"
)

// Two actual PostgreSQL clients contend on issuance and offboarding. The
// portable backup is independently inspected outside Go after restoring SQLite.
func TestPostgresDevelopmentPreviewConcurrentRevocationAndRestore(t *testing.T) {
	h, other := postgresAuthorityHandlers(t)
	root := t.TempDir()
	if evidence := os.Getenv("IS_PREVIEW_EVIDENCE_DIR"); evidence != "" {
		root = filepath.Join(evidence, "preview-postgres-proof")
		if e := os.Mkdir(root, 0700); e != nil {
			t.Fatal(e)
		}
	}
	h.Service.Root, other.Service.Root = filepath.Join(root, "source"), filepath.Join(root, "source")
	ctx := context.Background()
	admin, e := h.Auth.CreateIdentity("preview-pg-admin@example.test", "Administrator", "preview-test-password-2026", true)
	if e != nil {
		t.Fatal(e)
	}
	user, e := h.Auth.CreateIdentity("preview-pg-viewer@example.test", "Preview reviewer", "preview-test-password-2026", false)
	if e != nil {
		t.Fatal(e)
	}
	a := identityActor(admin)
	project := authorityCommand(t, h, a, "projects.create", p.M{"title": "PostgreSQL preview delivery"})
	g := authorityCommand(t, h, a, "goals.create", p.M{"projectId": project.ID, "title": "Static preview", "outcome": "Review a static application before release", "targets": []string{"web"}, "criteria": []any{p.M{"id": "preview", "text": "Customer can review the application"}}})
	authorityCommand(t, h, a, "goals.approve", p.M{"id": g.ID})
	repo := authorityCommand(t, h, a, "repositories.create", p.M{"projectId": project.ID, "title": "Preview repository"})
	artifact, e := h.Service.Upload(ctx, a, p.M{"goalId": g.ID, "repositoryId": repo.ID, "commit": p.S(repo.Data, "head"), "target": "web", "type": "package", "name": "preview.tar"}, bytes.NewReader(previewHTTPArchive(t)))
	if e != nil {
		t.Fatal(e)
	}
	input := p.M{"artifactId": artifact.ID, "expectedRequirementRevision": 1, "title": "PostgreSQL preview", "key": "create-preview", "reason": "Review current build"}
	preview := authorityCommand(t, h, a, "previews.create", input)
	team := authorityCommand(t, h, a, "teams.create", p.M{"title": "Preview reviewers", "memberIds": []string{user.ID}, "key": "create-team"})
	authorityCommand(t, h, a, "teams.grant", p.M{"projectId": project.ID, "teamId": team.ID, "expectedTeamRevision": 1, "roles": []string{"viewer"}, "reason": "Review application", "key": "grant-team"})
	authenticated := func(handler *Handler, email string) p.Actor {
		t.Helper()
		token, _, e := handler.Auth.Login(email, "preview-test-password-2026")
		if e != nil {
			t.Fatal(e)
		}
		r := httptest.NewRequest("GET", "/", nil)
		r.AddCookie(&http.Cookie{Name: "is_session", Value: token})
		actor, _, _, e := handler.Auth.Authenticate(r)
		if e != nil {
			t.Fatal(e)
		}
		return actor
	}
	b := authenticated(h, user.Email)
	adminSession := authenticated(h, admin.Email)
	old, e := h.Service.IssuePreviewAccess(ctx, b, preview.ID)
	if e != nil {
		t.Fatal(e)
	}
	independent, e := h.Service.IssuePreviewAccess(ctx, adminSession, preview.ID)
	if e != nil {
		t.Fatal(e)
	}
	remove := p.M{"id": team.ID, "expectedRevision": 1, "title": team.Title, "memberIds": []string{}, "reason": "Review completed", "key": "remove-team-member"}
	start := make(chan struct{})
	failures := make(chan error, 14)
	issuedTokens := make(chan string, 12)
	var wg sync.WaitGroup
	var issued, rejected, acknowledged atomic.Int32
	for i := range 12 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			service := h.Service
			if i%2 == 0 {
				service = other.Service
			}
			access, e := service.IssuePreviewAccess(ctx, b, preview.ID)
			if e == nil {
				issued.Add(1)
				issuedTokens <- access.Token
				return
			}
			var f *p.Fault
			if !errors.As(e, &f) || f.Status != 403 {
				failures <- e
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
			r, e := handler.Service.Command(ctx, a, "teams.update", remove)
			if e != nil {
				failures <- e
				return
			}
			if r.ID != team.ID || r.Revision != 2 {
				failures <- errors.New("concurrent team removal diverged")
				return
			}
			acknowledged.Add(1)
		}(handler)
	}
	close(start)
	wg.Wait()
	close(failures)
	close(issuedTokens)
	for e := range failures {
		if e != nil {
			t.Fatal(e)
		}
	}
	if issued.Load()+rejected.Load() != 12 || acknowledged.Load() != 2 {
		t.Fatal("contention results missing")
	}
	if _, _, e = other.Service.ResolvePreviewAccess(ctx, old.Token); e == nil {
		t.Fatal("old link survived removal")
	}
	for token := range issuedTokens {
		if _, _, e = other.Service.ResolvePreviewAccess(ctx, token); e == nil {
			t.Fatal("concurrent link escaped offboarding")
		}
	}
	var n int
	if e = h.Service.Store.DB.QueryRow(`SELECT count(*) FROM credentials WHERE identity_id=$1 AND kind='preview'`, user.ID).Scan(&n); e != nil || n != 0 {
		t.Fatal("offboarding retained preview credentials", n, e)
	}
	if _, _, e = other.Service.ResolvePreviewAccess(ctx, independent.Token); e != nil {
		t.Fatal("unrelated issuer lost preview access", e)
	}
	authorityCommand(t, other, a, "teams.update", p.M{"id": team.ID, "expectedRevision": 2, "title": team.Title, "memberIds": []string{user.ID}, "reason": "Explicit reviewer return", "key": "return-team-member"})
	if _, _, e = h.Service.ResolvePreviewAccess(ctx, old.Token); e == nil {
		t.Fatal("return resurrected revoked link")
	}
	if _, e = h.Service.IssuePreviewAccess(ctx, b, preview.ID); e != nil {
		t.Fatal("current session could not obtain a new link", e)
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
		t.Fatal("restore retained preview or session credentials", n, e)
	}
	for _, token := range []string{old.Token, independent.Token} {
		if _, _, e = restored.Service.ResolvePreviewAccess(ctx, token); e == nil {
			t.Fatal("backup resurrected private access")
		}
	}
	freshActor := authenticated(restored, user.Email)
	fresh, e := restored.Service.IssuePreviewAccess(ctx, freshActor, preview.ID)
	if e != nil {
		t.Fatal(e)
	}
	r, source, e := restored.Service.ResolvePreviewAccess(ctx, fresh.Token)
	if e != nil || r.ID != preview.ID || source.ID != artifact.ID || p.S(source.Data, "digest") != p.S(artifact.Data, "digest") {
		t.Fatal("restore lost exact preview binding", e)
	}
	bundle, e := webpreview.Read(restored.Service.ArtifactPath(p.S(source.Data, "digest")), p.S(source.Data, "digest"), int64(p.N(source.Data, "size")))
	if e != nil || bundle.FileCount != 4 {
		t.Fatal("restored static content differs", e)
	}
	facts := p.M{"passed": true, "syntheticOnly": true, "projectId": project.ID, "goalId": g.ID, "repositoryId": repo.ID, "artifactId": artifact.ID, "previewId": preview.ID, "previewBinding": p.S(preview.Data, "binding"), "artifactDigest": p.S(artifact.Data, "digest"), "artifactSize": p.N(artifact.Data, "size"), "commit": p.S(artifact.Data, "commit"), "fileCount": 4, "concurrentIssuanceAttempts": 12, "issuedBeforeRemoval": issued.Load(), "rejectedAfterRemoval": rejected.Load(), "concurrentRemovalAcknowledgements": acknowledged.Load(), "unrelatedIssuerPreserved": true, "oldLinkRevivedOnReturn": false, "restoredCredentials": 0, "freshRestoredAccess": true, "paidModelCalled": false}
	encoded, e := json.MarshalIndent(facts, "", "  ")
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(root, "facts.json"), append(encoded, '\n'), 0600); e != nil {
		t.Fatal(e)
	}
}

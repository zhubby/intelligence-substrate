package httpapi

import (
	"context"
	"net/url"
	"os"
	"sync"
	"testing"

	p "github.com/intelligence-substrate/platform/internal/platform"
)

func postgresAuthorityHandlers(t *testing.T) (*Handler, *Handler) {
	t.Helper()
	dsn := os.Getenv("IS_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("set IS_TEST_POSTGRES_URL to an isolated PostgreSQL database")
	}
	admin, err := p.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := p.NewID("authority_test")
	if _, err = admin.DB.Exec(`CREATE SCHEMA ` + schema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.DB.Exec(`DROP SCHEMA ` + schema + ` CASCADE`); admin.Close() })
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	query := u.Query()
	query.Set("search_path", schema)
	u.RawQuery = query.Encode()
	first, err := p.Open(u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { first.Close() })
	second, err := p.Open(u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { second.Close() })
	service := p.NewService(first)
	service.Root = t.TempDir()
	other := p.NewService(second)
	other.Root = service.Root
	return New(service, false), New(other, false)
}

func TestPostgresAuthorityConcurrentIssuanceAndOwnerRemoval(t *testing.T) {
	h, other := postgresAuthorityHandlers(t)
	admin, err := h.Auth.CreateIdentity("pg-admin@example.test", "Admin", "postgres-password-2026", true)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := h.Auth.CreateIdentity("pg-owner@example.test", "Owner", "postgres-password-2026", false)
	if err != nil {
		t.Fatal(err)
	}
	a, b := identityActor(admin), identityActor(owner)
	project := authorityCommand(t, h, a, "projects.create", p.M{"title": "PostgreSQL authority"})
	member := authorityCommand(t, h, a, "members.grant", p.M{"projectId": project.ID, "identityId": b.ID, "roles": []string{"owner"}})
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make(chan error, 13)
	for i := range 12 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			auth := h.Auth
			if i%2 == 0 {
				auth = other.Auth
			}
			_, _, e := auth.IssueToken(context.Background(), b, p.M{"projectId": project.ID})
			if e != nil {
				fault, ok := e.(*p.Fault)
				if !ok || fault.Status != 403 {
					results <- e
				}
			}
		}(i)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		_, e := other.Service.Command(context.Background(), a, "members.revoke", p.M{"id": member.ID, "expectedRevision": member.Revision, "reason": "Concurrent departure"})
		results <- e
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
	if err = h.Auth.Store.DB.QueryRow(`SELECT count(*) FROM credentials WHERE identity_id=$1 AND kind='token'`, b.ID).Scan(&n); err != nil || n != 0 {
		t.Fatal("PostgreSQL issuance outlived membership", n, err)
	}
	// A fresh project with exactly two real owners cannot lose both through
	// concurrent transactions issued by an independent platform administrator.
	second := authorityCommand(t, h, a, "projects.create", p.M{"title": "Concurrent last owner"})
	authorityCommand(t, h, a, "members.grant", p.M{"projectId": second.ID, "identityId": b.ID, "roles": []string{"owner"}})
	members, err := h.Auth.Store.List(second.ID, "member", "")
	if err != nil || len(members) != 2 {
		t.Fatal(err, members)
	}
	start = make(chan struct{})
	results = make(chan error, 2)
	for i, m := range members {
		wg.Add(1)
		go func(i int, m p.Resource) {
			defer wg.Done()
			<-start
			handler := h
			if i == 1 {
				handler = other
			}
			_, e := handler.Service.Command(context.Background(), a, "members.revoke", p.M{"id": m.ID, "expectedRevision": m.Revision, "reason": "Concurrent transfer"})
			results <- e
		}(i, m)
	}
	close(start)
	wg.Wait()
	close(results)
	success := 0
	for e := range results {
		if e == nil {
			success++
		} else if fault, ok := e.(*p.Fault); !ok || fault.Status != 409 {
			t.Fatal(e)
		}
	}
	if success != 1 {
		t.Fatalf("concurrent owner removals succeeded %d times", success)
	}
	members, err = h.Auth.Store.List(second.ID, "member", "")
	if err != nil {
		t.Fatal(err)
	}
	active := 0
	for _, m := range members {
		if m.Status == "active" {
			active++
		}
	}
	if active != 1 {
		t.Fatal("project orphaned", members)
	}
}

func TestPostgresIdentityDisableReadsLatestOIDCBinding(t *testing.T) {
	h, other := postgresAuthorityHandlers(t)
	admin, err := h.Auth.CreateIdentity("binding-admin@example.test", "Admin", "postgres-password-2026", true)
	if err != nil {
		t.Fatal(err)
	}
	user, err := h.Auth.CreateIdentity("binding-user@example.test", "User", "postgres-password-2026", false)
	if err != nil {
		t.Fatal(err)
	}
	// Hold the serialization lock with an uncommitted IdP binding. The other
	// connection can read old state but must authorize and mutate only afterward.
	bound := make(chan struct{})
	release := make(chan struct{})
	binding := make(chan error, 1)
	go func() {
		binding <- h.Auth.Store.Update(context.Background(), func(tx *p.Tx) error {
			if e := tx.BindOIDCSubject(user.ID, "new-subject"); e != nil {
				return e
			}
			close(bound)
			<-release
			return nil
		})
	}()
	<-bound
	done := make(chan error, 1)
	go func() { done <- other.Auth.Disable(context.Background(), identityActor(admin), user.ID) }()
	close(release)
	if err = <-binding; err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	current, err := h.Auth.identity(user.ID)
	if err != nil || !current.Disabled || current.OIDCSub != "new-subject" {
		t.Fatal("disable overwrote transaction-current IdP binding", current, err)
	}
}

package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	p "github.com/intelligence-substrate/platform/internal/platform"
)

func TestOIDCLegacyDuplicateBindingFailsClosed(t *testing.T) {
	h := setup(t)
	other, err := h.Auth.CreateIdentity("legacy@example.test", "Legacy conflict", "boundary-password-2026", false)
	if err != nil {
		t.Fatal(err)
	}
	f := boundaryIssuer(t, h)
	owner, _ := h.Auth.IdentityByEmail("owner@example.com")
	for _, identity := range []Identity{owner, other} {
		identity.OIDCSub = f.provider.URL + "|employee-stable-1"
		body, _ := json.Marshal(identity)
		if _, err = h.Service.Store.DB.Exec(`UPDATE identities SET body=$1 WHERE id=$2`, string(body), identity.ID); err != nil {
			t.Fatal(err)
		}
	}
	for _, email := range []string{owner.Email, other.Email} {
		code, state, cookie := f.begin(t, email, "employee-stable-1")
		w := f.callback(code, state, cookie)
		if w.Code != 403 {
			t.Fatal("legacy ambiguity selected an account", w.Code)
		}
		noOIDCSession(t, h, w)
	}
	for _, original := range []Identity{owner, other} {
		current, _ := h.Auth.identity(original.ID)
		if current.OIDCSub != f.provider.URL+"|employee-stable-1" {
			t.Fatal("login silently repaired ambiguous identity history")
		}
	}
}

func TestOIDCConcurrentCallbackConsumesExactlyOneState(t *testing.T) {
	h := setup(t)
	f := boundaryIssuer(t, h)
	code, state, cookie := f.begin(t, "owner@example.com", "employee-stable-1")
	start := make(chan struct{})
	results := make(chan *httptest.ResponseRecorder, 2)
	for range 2 {
		go func() { <-start; results <- f.callback(code, state, cookie) }()
	}
	close(start)
	counts := map[int]int{}
	for range 2 {
		w := <-results
		counts[w.Code]++
		if w.Code != 302 {
			noOIDCSession(t, h, w)
		}
	}
	if counts[302] != 1 || counts[403] != 1 || f.exchanges() != 1 {
		t.Fatal("concurrent callback did not consume exactly once", counts, f.exchanges())
	}
	var sessions, states, successes int
	if err := h.Service.Store.DB.QueryRow(`SELECT COUNT(*) FROM credentials WHERE kind='session'`).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	_ = h.Service.Store.DB.QueryRow(`SELECT COUNT(*) FROM credentials WHERE kind='oidc'`).Scan(&states)
	_ = h.Service.Store.DB.QueryRow(`SELECT COUNT(*) FROM audit WHERE body LIKE '%"auth.oidc"%'`).Scan(&successes)
	if sessions != 1 || states != 0 || successes != 1 {
		t.Fatal("callback side effects duplicated", sessions, states, successes)
	}
}

func TestOIDCFailedExchangeCannotReplayState(t *testing.T) {
	h := setup(t)
	f := boundaryIssuer(t, h)
	code, state, cookie := f.begin(t, "owner@example.com", "employee-stable-1")
	if w := f.callback("unknown-code", state, cookie); w.Code != 401 {
		t.Fatal("provider failure was not preserved", w.Code)
	}
	before := f.exchanges()
	if w := f.callback(code, state, cookie); w.Code != 403 {
		t.Fatal("failed exchange state could be retried", w.Code)
	}
	if f.exchanges() != before {
		t.Fatal("failed state replay contacted the provider")
	}
}

func oidcBindingRaceAndRestore(t *testing.T, h, other *Handler, label string) {
	t.Helper()
	ctx := context.Background()
	first, err := h.Auth.CreateIdentity("first@example.test", "First", "boundary-password-2026", true)
	if err != nil {
		t.Fatal(err)
	}
	second, err := h.Auth.CreateIdentity("second@example.test", "Second", "boundary-password-2026", false)
	if err != nil {
		t.Fatal(err)
	}
	subject := "https://issuer.example.test|Case-Sensitive-Employee"
	rollback := errors.New("rollback entire binding and audit")
	err = h.Service.Store.Update(ctx, func(tx *p.Tx) error {
		if err := tx.BindOIDCSubject(first.ID, subject); err != nil {
			return err
		}
		if err := tx.Audit(identityActor(first), "auth.oidc", p.Resource{ID: first.ID}, p.M{}); err != nil {
			return err
		}
		return rollback
	})
	current, _ := h.Auth.identity(first.ID)
	if !errors.Is(err, rollback) || current.OIDCSub != "" {
		t.Fatal("binding escaped rollback", err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i, handler := range []*Handler{h, other} {
		identity := []Identity{first, second}[i]
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results <- handler.Service.Store.Update(ctx, func(tx *p.Tx) error {
				if err := tx.BindOIDCSubject(identity.ID, subject); err != nil {
					return err
				}
				return tx.Audit(identityActor(identity), "auth.oidc", p.Resource{ID: identity.ID}, p.M{})
			})
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	succeeded, rejected := 0, 0
	for err := range results {
		if err == nil {
			succeeded++
		} else if fault, ok := err.(*p.Fault); ok && fault.Status == 403 {
			rejected++
		} else {
			t.Fatal("unexpected concurrency error", err)
		}
	}
	if succeeded != 1 || rejected != 1 {
		t.Fatal("same stable subject bound twice", succeeded, rejected)
	}
	root := t.TempDir()
	if evidence := os.Getenv("IS_SSO_IDENTITY_EVIDENCE_DIR"); evidence != "" {
		root = filepath.Join(evidence, label)
		if err = os.Mkdir(root, 0700); err != nil {
			t.Fatal(err)
		}
	}
	h.Service.Root = filepath.Join(root, "source")
	if err = os.MkdirAll(h.Service.Root, 0700); err != nil {
		t.Fatal(err)
	}
	// Real sessions/state exist before backup and must never be restored.
	if _, _, err = h.Auth.Login(first.Email, "boundary-password-2026"); err != nil {
		t.Fatal(err)
	}
	f := boundaryIssuer(t, h)
	_, _, _ = f.begin(t, first.Email, "unused-authorization")
	archive := filepath.Join(root, "backup.tar.gz")
	if err = h.Service.Backup(ctx, archive); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(root, "restored")
	if err = p.Restore(ctx, archive, dest, ""); err != nil {
		t.Fatal(err)
	}
	store, err := p.Open(filepath.Join(dest, "platform.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	restored := New(&p.Service{Store: store, Root: dest}, false)
	if err = restored.Service.VerifyIntegrity(ctx); err != nil {
		t.Fatal(err)
	}
	winner, loser := first, second
	if current, _ := h.Auth.identity(second.ID); current.OIDCSub == subject {
		winner, loser = second, first
	}
	restoredWinner, _ := restored.Auth.identity(winner.ID)
	var credentials, audits int
	_ = store.DB.QueryRow(`SELECT COUNT(*) FROM credentials`).Scan(&credentials)
	_ = store.DB.QueryRow(`SELECT COUNT(*) FROM audit`).Scan(&audits)
	if restoredWinner.OIDCSub != subject || credentials != 0 || audits != 1 {
		t.Fatal("portable backup changed stable identity, credentials or audit", credentials, audits)
	}
	if err = store.Update(ctx, func(tx *p.Tx) error { return tx.BindOIDCSubject(loser.ID, subject) }); err == nil {
		t.Fatal("restore lost the identity collision fence")
	}
	record := p.M{"passed": true, "engine": label, "winnerId": winner.ID, "loserId": loser.ID, "subject": subject, "successfulBindings": succeeded, "rejectedBindings": rejected, "credentialsRestored": credentials, "auditRecords": audits, "rollbackVerified": true}
	body, _ := json.MarshalIndent(record, "", "  ")
	if err = os.WriteFile(filepath.Join(root, "proof.json"), append(body, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestOIDCBindingConcurrentSQLitePortableRecovery(t *testing.T) {
	h := setup(t)
	store, err := p.Open(h.Service.Store.DSN)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	oidcBindingRaceAndRestore(t, h, New(p.NewService(store), false), "sqlite")
}

func TestPostgresOIDCBindingConcurrentPortableRecovery(t *testing.T) {
	h, other := postgresAuthorityHandlers(t)
	oidcBindingRaceAndRestore(t, h, other, "postgres")
}

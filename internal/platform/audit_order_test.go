package platform

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

func auditVariablePrecisionOrder(t *testing.T, open func() *Store) {
	t.Helper()
	for _, appendEvent := range []bool{false, true} {
		name := "read-existing-chain"
		if appendEvent {
			name = "append-to-actual-tail"
		}
		t.Run(name, func(t *testing.T) {
			store := open()
			defer store.Close()
			service := &Service{Store: store, Root: t.TempDir()}
			// These valid, immutable UTC records have ascending real times but
			// descending text order at fractional-prefix boundaries.
			previous := ""
			originals := []string{}
			ids := []string{"event_first", "event_second", "event_third"}
			for i, at := range []string{"2000-01-01T00:00:00Z", "2000-01-01T00:00:00.7Z", "2000-01-01T00:00:00.71Z"} {
				a := Audit{ID: ids[i], ProjectID: "project_audit", ActorID: "owner", Action: "fixture", ResourceID: "resource_audit", At: at, Detail: M{"index": i}, Previous: previous}
				a.Hash = Hash(a)
				previous = a.Hash
				body, err := json.Marshal(a)
				if err != nil {
					t.Fatal(err)
				}
				originals = append(originals, string(body))
				if _, err = store.DB.Exec("INSERT INTO audit(id,project_id,at,body) VALUES($1,$2,$3,$4)", a.ID, a.ProjectID, a.At, string(body)); err != nil {
					t.Fatal(err)
				}
			}
			if appendEvent {
				if err := store.Update(context.Background(), func(tx *Tx) error {
					return tx.Audit(Actor{ID: "owner", Kind: "human"}, "next", Resource{ID: "resource_audit", ProjectID: "project_audit"}, M{})
				}); err != nil {
					t.Fatal(err)
				}
			}
			audits, err := store.Audits("")
			if err != nil {
				t.Fatal(err)
			}
			for i, id := range ids {
				if audits[i].ID != id {
					t.Errorf("UTC audit chronology differs at %d: got %s want %s", i, audits[i].ID, id)
				}
				var body string
				if err = store.DB.QueryRow("SELECT body FROM audit WHERE id=$1", id).Scan(&body); err != nil || body != originals[i] {
					t.Fatal("historical audit bytes were changed", err)
				}
			}
			if appendEvent && (len(audits) != 4 || audits[3].Previous != previous) {
				t.Errorf("new event did not extend the actual chain tail: %+v", audits)
			}
			if err = service.VerifyIntegrity(context.Background()); err != nil {
				t.Fatal("valid historical chain rejected", err)
			}
			if _, err = store.DB.Exec("UPDATE audit SET body=$1 WHERE id=$2", originals[0]+" ", ids[1]); err != nil {
				t.Fatal(err)
			}
			if err = service.VerifyIntegrity(context.Background()); err == nil {
				t.Fatal("reordering fix accepted a duplicated/tampered audit body")
			}
		})
	}
}

func TestAuditVariablePrecisionOrderAndAppend(t *testing.T) {
	auditVariablePrecisionOrder(t, func() *Store {
		store, err := Open(filepath.Join(t.TempDir(), "audit.db"))
		if err != nil {
			t.Fatal(err)
		}
		return store
	})
}

func TestPostgresAuditVariablePrecisionOrderAndAppend(t *testing.T) {
	dsn := os.Getenv("IS_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("set IS_TEST_POSTGRES_URL to an isolated PostgreSQL database")
	}
	admin, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	auditVariablePrecisionOrder(t, func() *Store {
		schema := NewID("audit_order_test")
		if _, err = admin.DB.Exec("CREATE SCHEMA " + schema); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = admin.DB.Exec("DROP SCHEMA " + schema + " CASCADE") })
		u, err := url.Parse(dsn)
		if err != nil {
			t.Fatal(err)
		}
		q := u.Query()
		q.Set("search_path", schema)
		u.RawQuery = q.Encode()
		store, err := Open(u.String())
		if err != nil {
			t.Fatal(err)
		}
		return store
	})
}

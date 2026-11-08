package platform

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func checkStoreUpdateCancellation(t *testing.T, store *Store) {
	t.Helper()
	for _, phase := range []string{"callback", "commit"} {
		for _, cause := range []string{"cancel", "deadline"} {
			t.Run(phase+"/"+cause, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				want := context.Canceled
				if cause == "deadline" {
					cancel()
					ctx, cancel = context.WithTimeout(context.Background(), 100*time.Millisecond)
					want = context.DeadlineExceeded
				}
				defer cancel()
				id := NewID("cancelled_update")
				reached := false
				err := store.Update(ctx, func(tx *Tx) error {
					reached = true
					if err := tx.Put(Resource{ID: id, Kind: "test", Status: "pending", Revision: 1, Data: M{}}); err != nil {
						return err
					}
					if cause == "cancel" {
						cancel()
					}
					<-ctx.Done()
					// Observe the real database/sql background rollback before the
					// callback/commit, making the original cancellation race deterministic.
					until := time.Now().Add(2 * time.Second)
					for {
						var value int
						err := tx.tx.QueryRow("SELECT 1").Scan(&value)
						if errors.Is(err, sql.ErrTxDone) {
							if phase == "callback" {
								return err
							}
							return nil
						}
						if time.Now().After(until) {
							t.Fatal("database/sql did not roll back the cancelled transaction")
						}
						runtime.Gosched()
					}
				})
				if !reached || !errors.Is(err, want) {
					t.Errorf("cancelled update lost its context cause: reached=%v got=%v want=%v", reached, err, want)
				}
				if _, err := store.Get(id); err == nil {
					t.Fatal("cancelled transaction persisted its write")
				}
				if err := store.Update(context.Background(), func(tx *Tx) error { return nil }); err != nil {
					t.Fatal("store did not recover after cancellation", err)
				}
			})
		}
	}
	t.Run("active-context-keeps-transaction-error", func(t *testing.T) {
		err := store.Update(context.Background(), func(tx *Tx) error { return tx.tx.Rollback() })
		if !errors.Is(err, sql.ErrTxDone) {
			t.Fatal("unrelated transaction completion must retain its error", err)
		}
	})
}

func TestStoreUpdatePreservesCancellationCause(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	checkStoreUpdateCancellation(t, store)
}

func TestPostgresStoreUpdatePreservesCancellationCause(t *testing.T) {
	dsn := os.Getenv("IS_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("set IS_TEST_POSTGRES_URL to an isolated PostgreSQL database")
	}
	admin, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := NewID("cancellation_test")
	if _, err = admin.DB.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatal(err)
	}
	defer admin.DB.Exec("DROP SCHEMA " + schema + " CASCADE")
	connection, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	query := connection.Query()
	query.Set("search_path", schema)
	connection.RawQuery = query.Encode()
	store, err := Open(connection.String())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	checkStoreUpdateCancellation(t, store)
}

package platform

import (
	"database/sql"
	"net/url"
	"os"
	"sync"
	"testing"
)

func TestPostgresConcurrentFreshSchemaBootstrapIsAtomic(t *testing.T) {
	dsn := os.Getenv("IS_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("set an isolated PostgreSQL database")
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	for round := range 3 {
		t.Run(string(rune('a'+round)), func(t *testing.T) {
			schema := NewID("bootstrap_test")
			if _, err := admin.Exec("CREATE SCHEMA " + schema); err != nil {
				t.Fatal(err)
			}
			defer admin.Exec("DROP SCHEMA " + schema + " CASCADE")
			u, err := url.Parse(dsn)
			if err != nil {
				t.Fatal(err)
			}
			q := u.Query()
			q.Set("search_path", schema)
			u.RawQuery = q.Encode()
			start := make(chan struct{})
			failures := make(chan error, 8)
			var group sync.WaitGroup
			for range 8 {
				group.Add(1)
				go func() {
					defer group.Done()
					<-start
					store, e := Open(u.String())
					if e == nil {
						var count int
						e = store.DB.QueryRow("SELECT count(*) FROM information_schema.tables WHERE table_schema=$1", schema).Scan(&count)
						if e == nil && count != 5 {
							t.Errorf("partial bootstrap: %d tables", count)
						}
						_ = store.Close()
					}
					failures <- e
				}()
			}
			close(start)
			group.Wait()
			close(failures)
			for e := range failures {
				if e != nil {
					t.Errorf("independent bootstrap failed: %v", e)
				}
			}
		})
	}
}

package platform

import (
	"net/url"
	"os"
	"testing"
)

func TestPostgresQualityReportArchiveRestoresExactProvenance(t *testing.T) {
	dsn := os.Getenv("IS_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("set IS_TEST_POSTGRES_URL to an isolated PostgreSQL database")
	}
	admin, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := NewID("quality_reports_test")
	if _, err = admin.DB.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	defer admin.DB.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
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
	defer store.Close()
	s := &Service{Store: store, Root: t.TempDir()}
	owner := Actor{ID: "quality-report-owner", Kind: "human", Admin: true}
	project := must(t, s, owner, "projects.create", M{"title": "Durable PostgreSQL test reports"})
	verifyQualityReportRestore(t, setupQualityFixture(t, s, owner, project))
}

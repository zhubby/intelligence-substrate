package platform

import (
	"bytes"
	"context"
	"net/url"
	"os"
	"strings"
	"testing"
)

func TestPostgresWriteScopeRejectsAndResumesAcrossConnections(t *testing.T) {
	dsn := os.Getenv("IS_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("set IS_TEST_POSTGRES_URL to an isolated PostgreSQL database")
	}
	admin, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := NewID("scope_test")
	if _, err = admin.DB.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	defer admin.DB.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
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
	other, err := Open(connection.String())
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	s := &Service{Store: store, Root: t.TempDir()}
	second := &Service{Store: other, Root: s.Root}
	owner := Actor{ID: "scope-owner", Kind: "human", Admin: true}
	project := must(t, s, owner, "projects.create", M{"title": "Postgres scoped source"})
	f := newSourceFixtureOn(t, s, owner, project, M{"id": "code", "writePaths": []any{"backend/"}}, M{"id": "build", "kind": "build", "dependsOn": []any{"code"}})
	run := f.claim(t)
	in := writeScopeInput(run)
	if _, err = second.CommitSnapshot(context.Background(), f.worker, f.repo.ID, in, bytes.NewReader(sourceTar(t, map[string][]byte{"frontend/ui.txt": []byte("denied")}, ""))); err == nil || !strings.Contains(err.Error(), "writePaths") {
		t.Fatal("second connection bypassed scope", err)
	}
	if ops, err := store.List(project.ID, "source_commit", f.g.ID); err != nil || len(ops) != 0 {
		t.Fatal("rejection created a source operation", ops, err)
	}
	body := sourceTar(t, map[string][]byte{"backend/main.go": []byte("package backend\n")}, "")
	op, err := second.CommitSnapshot(context.Background(), f.worker, f.repo.ID, in, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.CommitSnapshot(context.Background(), f.worker, f.repo.ID, in, bytes.NewReader(body))
	if err != nil || again.ID != op.ID {
		t.Fatal("cross-connection replay duplicated or lost scoped source", again, err)
	}
	must(t, second, f.worker, "tasks.complete", M{"id": run.ID, "leaseToken": run.Data["leaseToken"], "exitCode": 0, "result": M{"repositoryId": f.repo.ID, "commit": op.Data["commit"], "sourceOperationId": op.ID}})
	build := f.claim(t)
	if S(Obj(build.Data["task"]), "commit") != S(op.Data, "commit") {
		t.Fatal("PostgreSQL downstream input was not the admitted source", build)
	}
}

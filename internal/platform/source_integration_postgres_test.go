package platform

import (
	"bytes"
	"context"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestPostgresIntegrationPreparationRecoveryAndRestore(t *testing.T) {
	dsn := os.Getenv("IS_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("set IS_TEST_POSTGRES_URL to an isolated PostgreSQL database")
	}
	admin, e := Open(dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer admin.Close()
	schema := NewID("integration_test")
	if _, e = admin.DB.Exec(`CREATE SCHEMA ` + schema); e != nil {
		t.Fatal(e)
	}
	defer admin.DB.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
	connection, e := url.Parse(dsn)
	if e != nil {
		t.Fatal(e)
	}
	query := connection.Query()
	query.Set("search_path", schema)
	connection.RawQuery = query.Encode()
	store, e := Open(connection.String())
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	other, e := Open(connection.String())
	if e != nil {
		t.Fatal(e)
	}
	defer other.Close()
	svc := &Service{Store: store, Root: t.TempDir()}
	second := &Service{Store: other, Root: svc.Root}
	a := Actor{ID: "integration-owner", Kind: "human", Admin: true}
	project := must(t, svc, a, "projects.create", M{"title": "PostgreSQL integration"})
	f := newSourceFixtureOn(t, svc, a, project, M{"id": "left", "branch": "left"}, M{"id": "right", "branch": "right"}, M{"id": "join", "sourceMode": "integrate", "dependsOn": []any{"left", "right"}})
	integrationInputs(t, f, true)
	run := f.claim(t)
	request := M{"runId": run.ID, "leaseToken": S(run.Data, "leaseToken")}
	var wg sync.WaitGroup
	results, failures := make(chan Resource, 2), make(chan error, 2)
	for _, service := range []*Service{svc, second} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, e := service.Command(context.Background(), f.worker, "sources.prepare", request)
			results <- result
			failures <- e
		}()
	}
	wg.Wait()
	close(results)
	close(failures)
	for e := range failures {
		if e != nil {
			t.Fatal(e)
		}
	}
	for result := range results {
		if S(run.Data, "integrationId") != "" && S(run.Data, "integrationId") != S(result.Data, "integrationId") {
			t.Fatal("concurrent preparation duplicated integration")
		}
		run = result
	}
	request["resolutions"] = []any{M{"path": "shared.txt", "deleted": true}}
	run = must(t, second, f.worker, "sources.resolve", request)
	in := integrationSnapshotInput(run)
	body := sourceTar(t, map[string][]byte{"left.txt": []byte("left\n"), "right.txt": []byte("right\n")}, "")
	if _, e = store.DB.Exec(`CREATE FUNCTION fail_integration_confirm() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected integration confirmation failure'; END; $$`); e != nil {
		t.Fatal(e)
	}
	if _, e = store.DB.Exec(`CREATE TRIGGER integration_confirm BEFORE UPDATE ON resources FOR EACH ROW WHEN (NEW.kind='source_commit' AND NEW.status='committed') EXECUTE FUNCTION fail_integration_confirm()`); e != nil {
		t.Fatal(e)
	}
	if _, e = svc.CommitSnapshot(context.Background(), f.worker, f.repo.ID, in, bytes.NewReader(body)); e == nil {
		t.Fatal("confirmation fault did not trigger")
	}
	ops, _ := store.List(project.ID, "source_commit", f.g.ID)
	if len(ops) != 1 || ops[0].Status != "prepared" {
		t.Fatal("integration prepared state lost", ops)
	}
	completion := M{"id": run.ID, "leaseToken": S(run.Data, "leaseToken"), "exitCode": 0, "result": M{"repositoryId": f.repo.ID, "commit": S(ops[0].Data, "commit"), "sourceOperationId": ops[0].ID}}
	if _, e = svc.Command(context.Background(), f.worker, "tasks.complete", completion); e == nil {
		t.Fatal("unconfirmed integration completed")
	}
	if _, e = store.DB.Exec(`DROP TRIGGER integration_confirm ON resources`); e != nil {
		t.Fatal(e)
	}
	op, e := second.CommitSnapshot(context.Background(), f.worker, f.repo.ID, in, bytes.NewReader(body))
	if e != nil || op.ID != ops[0].ID || op.Status != "committed" {
		t.Fatal("integration recovery failed", e, op)
	}
	must(t, second, f.worker, "tasks.complete", completion)
	archive := filepath.Join(t.TempDir(), "integration.tar.gz")
	if e = svc.Backup(context.Background(), archive); e != nil {
		t.Fatal(e)
	}
	restoredRoot := filepath.Join(t.TempDir(), "restored")
	if e = Restore(context.Background(), archive, restoredRoot, ""); e != nil {
		t.Fatal(e)
	}
	restored, e := Open(filepath.Join(restoredRoot, "platform.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer restored.Close()
	prep, e := restored.Get(S(run.Data, "integrationId"))
	if e != nil || prep.Kind != "integration" || S(prep.Data, "workspaceCommit") != S(run.Data, "integrationWorkspaceCommit") {
		t.Fatal("restore lost integration preparation", e, prep)
	}
	if e = svc.VerifyIntegrity(context.Background()); e != nil {
		t.Fatal(e)
	}
}

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

func TestPostgresSourceRecoveryDecisionRollbackConcurrencyAndRestore(t *testing.T) {
	dsn := os.Getenv("IS_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("set IS_TEST_POSTGRES_URL to an isolated PostgreSQL database")
	}
	admin, e := Open(dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer admin.Close()
	schema := NewID("source_recovery_test")
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
	a := Actor{ID: "recovery-owner", Kind: "human", Admin: true}
	project := must(t, svc, a, "projects.create", M{"title": "PostgreSQL source recovery"})
	f := newSourceFixtureOn(t, svc, a, project, M{"id": "code", "branch": "feature"}, M{"id": "build", "kind": "build", "dependsOn": []any{"code"}})
	run := f.claim(t)
	binding := Obj(Obj(run.Data["task"])["sourceBinding"])
	input := M{"key": run.ID, "runId": run.ID, "leaseToken": S(run.Data, "leaseToken"), "branch": S(binding, "targetBranch"), "baseCommit": S(binding, "commit"), "expectedHead": S(binding, "expectedHead"), "message": "Interrupted PostgreSQL source"}
	if _, e = store.DB.Exec(`CREATE FUNCTION fail_recovery() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected source recovery fault'; END; $$`); e != nil {
		t.Fatal(e)
	}
	if _, e = store.DB.Exec(`CREATE TRIGGER interrupt_source BEFORE UPDATE ON resources FOR EACH ROW WHEN (NEW.kind='source_commit' AND NEW.status='committed') EXECUTE FUNCTION fail_recovery()`); e != nil {
		t.Fatal(e)
	}
	if _, e = svc.CommitSnapshot(context.Background(), f.worker, f.repo.ID, input, bytes.NewReader(sourceTar(t, map[string][]byte{"app.txt": []byte("source\n")}, ""))); e == nil {
		t.Fatal("source SQL interruption did not fire")
	}
	if _, e = store.DB.Exec(`DROP TRIGGER interrupt_source ON resources`); e != nil {
		t.Fatal(e)
	}
	ops, _ := store.List(project.ID, "source_commit", f.g.ID)
	if len(ops) != 1 || ops[0].Status != "prepared" {
		t.Fatal("candidate not durable", ops)
	}
	op := ops[0]
	expireSourceRun(t, f, run)
	if _, e = store.DB.Exec(`CREATE TRIGGER interrupt_recovery BEFORE INSERT ON resources FOR EACH ROW WHEN (NEW.kind='source_recovery') EXECUTE FUNCTION fail_recovery()`); e != nil {
		t.Fatal(e)
	}
	if _, e = second.Command(context.Background(), a, "sources.reconcile", recoveryInput(op)); e == nil {
		t.Fatal("decision SQL interruption did not fire")
	}
	unchanged, _ := store.Get(op.ID)
	if unchanged.Status != "prepared" {
		t.Fatal("failed decision partially finalized source")
	}
	if _, e = store.DB.Exec(`DROP TRIGGER interrupt_recovery ON resources`); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	errors := make(chan error, 2)
	for i, service := range []*Service{svc, second} {
		in := recoveryInput(op)
		in["key"] = []string{"client-a", "client-b"}[i]
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := service.Command(context.Background(), a, "sources.reconcile", in)
			errors <- e
		}()
	}
	wg.Wait()
	close(errors)
	success := 0
	for e := range errors {
		if e == nil {
			success++
		}
	}
	decisions, _ := store.List(project.ID, "source_recovery", f.g.ID)
	if success != 1 || len(decisions) != 1 {
		t.Fatal("concurrent clients created competing decisions", success, decisions)
	}
	archive := filepath.Join(t.TempDir(), "source-recovery.tar.gz")
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
	restoredService := &Service{Store: restored, Root: restoredRoot}
	proof, e := restored.Get(decisions[0].ID)
	if e != nil || proof.Status != "confirmed" {
		t.Fatal("backup lost human decision", e)
	}
	producer, e := restored.Get(run.ID)
	if e != nil || producer.Status != "recovered" {
		t.Fatal("backup lost recovered execution", e)
	}
	build := must(t, restoredService, f.worker, "tasks.claim", M{})
	assertSource(t, build, f.repo.ID, S(op.Data, "commit"), producer)
	if e = svc.VerifyIntegrity(context.Background()); e != nil {
		t.Fatal(e)
	}
}

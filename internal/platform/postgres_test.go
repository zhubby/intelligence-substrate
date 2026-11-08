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

func TestPostgresSourceSnapshotRecoveryAndConcurrentCAS(t *testing.T) {
	dsn := os.Getenv("IS_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("set IS_TEST_POSTGRES_URL to an isolated PostgreSQL database")
	}
	// Each acceptance owns both its SQL namespace and its filesystem. The
	// existing backup acceptance must never see repositories in this temp root.
	admin, e := Open(dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer admin.Close()
	schema := NewID("source_test")
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
	dsn = connection.String()
	store, e := Open(dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	other, e := Open(dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer other.Close()
	service := &Service{Store: store, Root: t.TempDir()}
	second := &Service{Store: other, Root: service.Root}
	a := Actor{ID: "postgres-source-owner", Kind: "human", Admin: true}
	project := must(t, service, a, "projects.create", M{"title": "PostgreSQL source snapshots"})
	repo := must(t, service, a, "repositories.create", M{"projectId": project.ID, "title": "source"})
	in := snapshotInput(repo)
	body := sourceTar(t, map[string][]byte{"binary": {0, 255, 128}}, "")
	_, e = store.DB.Exec(`CREATE FUNCTION is_test_source_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected finalization failure'; END; $$`)
	if e != nil {
		t.Fatal(e)
	}
	defer store.DB.Exec(`DROP FUNCTION IF EXISTS is_test_source_fail() CASCADE`)
	_, e = store.DB.Exec(`CREATE TRIGGER is_source_fail BEFORE UPDATE ON resources FOR EACH ROW WHEN (NEW.kind='source_commit' AND NEW.status='committed') EXECUTE FUNCTION is_test_source_fail()`)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = service.CommitSnapshot(context.Background(), a, repo.ID, in, bytes.NewReader(body)); e == nil {
		t.Fatal("PostgreSQL finalization fault did not trigger")
	}
	ops, e := store.List(project.ID, "source_commit", "")
	if e != nil || len(ops) != 1 || ops[0].Status != "prepared" {
		t.Fatal("PostgreSQL prepared provenance lost", e, ops)
	}
	candidate := S(ops[0].Data, "commit")
	head, _ := service.Git(repo, "rev-parse", "refs/heads/feature")
	if head != candidate {
		t.Fatal("fault did not occur after Git movement")
	}
	if _, e = store.DB.Exec(`DROP TRIGGER is_source_fail ON resources`); e != nil {
		t.Fatal(e)
	}
	recovered, e := second.CommitSnapshot(context.Background(), a, repo.ID, in, bytes.NewReader(body))
	if e != nil || recovered.Status != "committed" || S(recovered.Data, "commit") != candidate {
		t.Fatal("second database client could not recover", e, recovered)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i, svc := range []*Service{service, second} {
		request := M{"key": []string{"writer-one", "writer-two"}[i], "branch": "concurrent", "baseCommit": candidate, "message": "Concurrent source"}
		archive := sourceTar(t, map[string][]byte{"value": []byte(request["key"].(string))}, "")
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := svc.CommitSnapshot(context.Background(), a, repo.ID, request, bytes.NewReader(archive))
			results <- e
		}()
	}
	wg.Wait()
	close(results)
	success := 0
	for e := range results {
		if e == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("PostgreSQL CAS accepted %d writers", success)
	}
}

func TestPostgresConcurrentLeaseAndPortableRestore(t *testing.T) {
	dsn := os.Getenv("IS_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("set IS_TEST_POSTGRES_URL to an isolated empty PostgreSQL database")
	}
	store, e := Open(dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	other, e := Open(dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer other.Close()
	service := &Service{Store: store, Root: t.TempDir()}
	second := &Service{Store: other, Root: service.Root}
	a := Actor{ID: "postgres-owner", Kind: "human", Admin: true}
	project := must(t, service, a, "projects.create", M{"title": "PostgreSQL acceptance"})
	g := goal(t, service, a, project)
	must(t, service, a, "goals.approve", M{"id": g.ID})
	repo := must(t, service, a, "repositories.create", M{"projectId": project.ID, "title": "concurrent-claim-source"})
	plan := must(t, service, a, "plans.propose", M{"goalId": g.ID, "summary": "One atomic job", "tasks": []any{M{"id": "build", "title": "Build", "kind": "build", "role": "developer", "runtime": "linux", "repositoryId": repo.ID}}})
	must(t, service, a, "plans.approve", M{"id": plan.ID})
	workers := []Actor{}
	for _, name := range []string{"A", "B"} {
		w := must(t, service, a, "workers.register", M{"projectId": project.ID, "title": name, "runtime": "linux", "roles": []any{"developer"}, "capabilities": []any{"build"}})
		workers = append(workers, Actor{ID: "worker:" + w.ID, Kind: "worker", ProjectID: project.ID, WorkerID: w.ID, Roles: []string{"developer"}})
	}
	var wg sync.WaitGroup
	results := make(chan Resource, 2)
	errors := make(chan error, 2)
	for i, svc := range []*Service{service, second} {
		wg.Add(1)
		go func(s *Service, actor Actor) {
			defer wg.Done()
			v, e := s.Command(context.Background(), actor, "tasks.claim", M{})
			results <- v
			errors <- e
		}(svc, workers[i])
	}
	wg.Wait()
	close(results)
	close(errors)
	for e := range errors {
		if e != nil {
			t.Fatal(e)
		}
	}
	runs := 0
	for v := range results {
		if v.Kind == "run" {
			runs++
			assertSource(t, v, repo.ID, S(repo.Data, "head"))
		}
	}
	if runs != 1 {
		t.Fatalf("concurrent database clients claimed %d copies of one job", runs)
	}
	archive := filepath.Join(t.TempDir(), "postgres.tar.gz")
	if e = service.Backup(context.Background(), archive); e != nil {
		t.Fatal(e)
	}
	if e = Restore(context.Background(), archive, filepath.Join(t.TempDir(), "restored"), ""); e != nil {
		t.Fatal(e)
	}
	if e = service.VerifyIntegrity(context.Background()); e != nil {
		t.Fatal(e)
	}
}

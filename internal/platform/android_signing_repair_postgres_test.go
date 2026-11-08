package platform

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestPostgresAndroidSigningRepairConcurrentLeaseReplayAndRestore(t *testing.T) {
	testPostgresAndroidSigningRepairConcurrentLeaseReplayAndRestore(t, "android-huawei")
}

func TestPostgresAABSigningRepairConcurrentLeaseReplayAndRestore(t *testing.T) {
	testPostgresAndroidSigningRepairConcurrentLeaseReplayAndRestore(t, "android-google-play")
}

func testPostgresAndroidSigningRepairConcurrentLeaseReplayAndRestore(t *testing.T, target string) {
	dsn := os.Getenv("IS_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("set IS_TEST_POSTGRES_URL to an isolated PostgreSQL database")
	}
	admin, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := NewID("signing_repair_test")
	if _, err = admin.DB.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	defer admin.DB.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	query := u.Query()
	query.Set("search_path", schema)
	u.RawQuery = query.Encode()
	store, err := Open(u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	other, err := Open(u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	s := &Service{Store: store, Root: t.TempDir()}
	second := &Service{Store: other, Root: s.Root}
	owner := Actor{ID: "signing-repair-owner", Kind: "human", Admin: true}
	project := must(t, s, owner, "projects.create", M{"title": "Durable signing repair"})
	f := setupSigningRepairFixtureTarget(t, s, owner, project, target)
	defect, _, _ := f.rejected(t)
	f.code(t)
	f.unsignedBuild(t)
	w := must(t, s, owner, "workers.register", M{"projectId": project.ID, "title": "Concurrent repair signer", "runtime": "linux", "roles": []any{"release"}, "capabilities": []any{"build"}})
	actors := []Actor{f.signer, {ID: "worker:" + w.ID, Kind: "worker", WorkerID: w.ID, ProjectID: project.ID, Roles: []string{"release"}}}
	type response struct {
		r Resource
		a Actor
		e error
	}
	results := make(chan response, 2)
	var group sync.WaitGroup
	for i, svc := range []*Service{s, second} {
		group.Add(1)
		go func(svc *Service, a Actor) {
			defer group.Done()
			r, e := svc.Command(context.Background(), a, "tasks.claim", M{})
			results <- response{r, a, e}
		}(svc, actors[i])
	}
	group.Wait()
	close(results)
	var run Resource
	claims := 0
	for result := range results {
		if result.e != nil {
			t.Fatal(result.e)
		}
		if result.r.Kind == "run" {
			claims++
			run, f.signer = result.r, result.a
		}
	}
	if claims != 1 || S(Obj(run.Data["task"]), "signingRepairId") == "" {
		t.Fatal("repair signing did not get exactly one authoritative lease", claims, run)
	}
	a := f.uploadSigned(t, run)
	completion := f.completion(run, a)
	errors := make(chan error, 2)
	for _, svc := range []*Service{s, second} {
		group.Add(1)
		go func(svc *Service) {
			defer group.Done()
			_, e := svc.Command(context.Background(), f.signer, "tasks.complete", completion)
			errors <- e
		}(svc)
	}
	group.Wait()
	close(errors)
	for e := range errors {
		if e != nil {
			t.Fatal("concurrent completion did not replay safely", e)
		}
	}
	qa := must(t, s, f.quality, "tasks.claim", M{})
	must(t, s, f.quality, "tasks.complete", M{"id": qa.ID, "leaseToken": S(qa.Data, "leaseToken"), "exitCode": 0, "result": M{}})
	defect, _ = store.Get(defect.ID)
	if defect.Status != "closed" || len(Items(defect.Data["androidSigningRepairs"])) != 1 {
		t.Fatal("PostgreSQL lost completed repair lineage", defect)
	}
	archive := filepath.Join(t.TempDir(), "repair.tar.gz")
	if err = s.Backup(context.Background(), archive); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "restored")
	if err = Restore(context.Background(), archive, destination, ""); err != nil {
		t.Fatal(err)
	}
	restoredStore, err := Open(filepath.Join(destination, "platform.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer restoredStore.Close()
	restored := &Service{Store: restoredStore, Root: destination}
	for _, kind := range []string{"defect", "task", "run", "artifact", "quality_policy", "quality_dispatch", "evidence"} {
		before, e := store.List(project.ID, kind, f.goal.ID)
		if e != nil {
			t.Fatal(e)
		}
		for _, row := range before {
			after, e := restoredStore.Get(row.ID)
			if e != nil || Hash(row) != Hash(after) {
				t.Fatal("archive lost exact repair record", kind, row.ID, e)
			}
		}
	}
	if err = restored.Store.Update(context.Background(), func(tx *Tx) error { return restored.validateSigningArtifact(tx, a) }); err != nil {
		t.Fatal("restored repair authority is not usable", err)
	}
	for _, actor := range []Actor{f.builder, f.signer, f.quality} {
		if idle := must(t, restored, actor, "tasks.claim", M{}); idle.Kind != "worker" {
			t.Fatal("restore repeated completed repair work", idle)
		}
	}
	if err = restored.VerifyIntegrity(context.Background()); err != nil {
		t.Fatal(err)
	}
}

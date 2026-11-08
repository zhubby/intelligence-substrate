package platform

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestPostgresAndroidSigningConcurrentLeaseAndExactRestore(t *testing.T) {
	testPostgresAndroidSigningConcurrentLeaseAndExactRestore(t, "android-huawei")
}

func TestPostgresAABSigningConcurrentLeaseAndExactRestore(t *testing.T) {
	testPostgresAndroidSigningConcurrentLeaseAndExactRestore(t, "android-google-play")
}

func testPostgresAndroidSigningConcurrentLeaseAndExactRestore(t *testing.T, target string) {
	dsn := os.Getenv("IS_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("set IS_TEST_POSTGRES_URL to an isolated PostgreSQL database")
	}
	admin, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := NewID("android_signing_test")
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
	owner := Actor{ID: "android-signing-owner", Kind: "human", Admin: true}
	project := must(t, s, owner, "projects.create", M{"title": "Durable trusted APK signing"})
	f := setupAndroidSigningFixtureTarget(t, s, owner, project, target)
	f.build(t)
	w := must(t, s, owner, "workers.register", M{"projectId": project.ID, "title": "Second dedicated signer", "runtime": "linux", "roles": []any{"release"}, "capabilities": []any{"build"}})
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
			run, e := svc.Command(context.Background(), a, "tasks.claim", M{})
			results <- response{run, a, e}
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
	if claims != 1 {
		t.Fatal("concurrent signers got multiple leases", claims)
	}
	artifact := f.uploadSigned(t, run)
	completion := f.completion(run, artifact)
	finished := must(t, s, f.signer, "tasks.complete", completion)
	must(t, second, f.signer, "tasks.complete", completion)
	archive := filepath.Join(t.TempDir(), "signing.tar.gz")
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
	for _, id := range []string{f.unsigned.ID, artifact.ID, finished.ID, S(run.Data, "taskId")} {
		before, e := store.Get(id)
		if e != nil {
			t.Fatal(e)
		}
		after, e := restoredStore.Get(id)
		if e != nil || Hash(before) != Hash(after) {
			t.Fatal("restore lost exact signing provenance", id, e)
		}
	}
	if err = restored.VerifyArtifact(artifact); err != nil {
		t.Fatal(err)
	}
	if idle := must(t, restored, f.signer, "tasks.claim", M{}); idle.Kind != "worker" {
		t.Fatal("restore repeated signing", idle)
	}
	if err = restored.VerifyIntegrity(context.Background()); err != nil {
		t.Fatal(err)
	}
}

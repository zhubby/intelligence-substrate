package platform

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestPostgresQualitySchedulingConcurrentClaimsAndRestore(t *testing.T) {
	dsn := os.Getenv("IS_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("set IS_TEST_POSTGRES_URL to an isolated PostgreSQL database")
	}
	admin, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := NewID("quality_scheduling_test")
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
	first, err := Open(u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Open(u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	s := &Service{Store: first, Root: t.TempDir()}
	other := &Service{Store: second, Root: s.Root}
	owner := Actor{ID: "quality-project-owner", Kind: "human", Admin: true}
	project := must(t, s, owner, "projects.create", M{"title": "Concurrent independent verification"})
	f := setupQualityFixture(t, s, owner, project)
	type outcome struct {
		r   Resource
		err error
	}
	start := make(chan struct{})
	results := make(chan outcome, 2)
	var wg sync.WaitGroup
	for _, service := range []*Service{s, other} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			r, err := service.Command(context.Background(), f.quality, "quality.bind", f.input("one-policy"))
			results <- outcome{r, err}
		}()
	}
	close(start)
	wg.Wait()
	var policy Resource
	for range 2 {
		item := <-results
		if item.err != nil || (policy.ID != "" && item.r.ID != policy.ID) {
			t.Fatal("concurrent policy binding duplicated", item)
		}
		policy = item.r
	}
	f.build(t)
	start = make(chan struct{})
	results = make(chan outcome, 2)
	for _, service := range []*Service{s, other} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			r, err := service.Command(context.Background(), f.verifier, "tasks.claim", M{})
			results <- outcome{r, err}
		}()
	}
	close(start)
	wg.Wait()
	var run Resource
	winners := 0
	for range 2 {
		item := <-results
		if item.err == nil && item.r.Kind == "run" {
			winners++
			run = item.r
		} else if item.err == nil {
			t.Fatal("concurrent claim returned an unexpected resource", item.r)
		}
	}
	if winners != 1 {
		t.Fatal("two SQL clients claimed duplicate verification leases", winners)
	}
	finished := must(t, s, f.verifier, "tasks.complete", M{"id": run.ID, "leaseToken": S(run.Data, "leaseToken"), "exitCode": 0, "result": M{}})
	dispatches, _ := first.List(project.ID, "quality_dispatch", f.goal.ID)
	if len(dispatches) != 1 {
		t.Fatal("duplicate dispatch after concurrent polling", dispatches)
	}
	ids := []string{policy.ID, dispatches[0].ID, S(run.Data, "taskId"), run.ID, S(finished.Data, "evidenceId")}
	before := map[string]string{}
	for _, id := range ids {
		r, err := first.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		before[id] = Hash(r)
	}
	var history int
	if err = first.DB.QueryRow(`SELECT COUNT(*) FROM idempotency`).Scan(&history); err != nil || history < 2 {
		t.Fatal("missing durable idempotency history", history, err)
	}
	archive := filepath.Join(t.TempDir(), "quality.tar.gz")
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
	for _, id := range ids {
		r, err := restoredStore.Get(id)
		if err != nil || Hash(r) != before[id] {
			t.Fatal("restore changed exact quality provenance", id, r, err)
		}
	}
	if replay := must(t, restored, f.quality, "quality.bind", f.input("one-policy")); replay.ID != policy.ID {
		t.Fatal("restore duplicated policy", replay)
	}
	for range 2 {
		if idle := must(t, restored, f.verifier, "tasks.claim", M{}); idle.Kind != "worker" {
			t.Fatal("restore duplicated completed verification", idle)
		}
	}
	dispatches, _ = restoredStore.List(project.ID, "quality_dispatch", f.goal.ID)
	if len(dispatches) != 1 {
		t.Fatal("restore duplicated dispatch", dispatches)
	}
	var after int
	if err = restoredStore.DB.QueryRow(`SELECT COUNT(*) FROM idempotency`).Scan(&after); err != nil || after != history {
		t.Fatal("restore lost/duplicated idempotency history", history, after, err)
	}
	if err = restored.VerifyIntegrity(context.Background()); err != nil {
		t.Fatal(err)
	}
}

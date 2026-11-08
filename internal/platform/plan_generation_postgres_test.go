package platform

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestPostgresPlanGenerationConcurrentIntentAndRestore(t *testing.T) {
	dsn := os.Getenv("IS_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("set IS_TEST_POSTGRES_URL to an isolated PostgreSQL database")
	}
	admin, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := NewID("plan_generation_test")
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
	a := Actor{ID: "business-owner", Name: "Owner", Kind: "human", Admin: true}
	project := must(t, s, a, "projects.create", M{"title": "Concurrent durable planning"})
	g := goal(t, s, a, project)
	answeredDraftQuestion(t, s, a, g)
	must(t, s, a, "goals.approve", M{"id": g.ID})
	in := planGenerationInput(g, "same-request")
	type result struct {
		request Resource
		started bool
		err     error
	}
	start, results := make(chan struct{}), make(chan result, 2)
	var wg sync.WaitGroup
	for _, service := range []*Service{s, other} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			r, began, err := service.BeginPlanGeneration(context.Background(), a, in)
			results <- result{r, began, err}
		}()
	}
	close(start)
	wg.Wait()
	var request Resource
	winners := 0
	for range 2 {
		r := <-results
		if r.err != nil || (request.ID != "" && r.request.ID != request.ID) {
			t.Fatal(r)
		}
		request = r.request
		if r.started {
			winners++
		}
	}
	if winners != 1 {
		t.Fatal("two SQL clients authorized duplicate inference", winners)
	}
	putPlanGenerationCharge(t, s, request)
	plan, err := s.CompletePlanGeneration(context.Background(), a, request.ID, generatedPlanContent())
	if err != nil {
		t.Fatal(err)
	}
	// Compare restored bytes with the actual pre-backup database row. Tx.Put
	// stamps UpdatedAt on its value copy; a returned command value is not that row.
	persisted, err := first.Get(plan.ID)
	if err != nil || Hash(persisted.Data) != Hash(plan.Data) || persisted.Status != "proposed" {
		t.Fatal("committed plan differs from completion", persisted, plan, err)
	}
	runningInput := planGenerationInput(g, "interrupted-request")
	running, began, err := s.BeginPlanGeneration(context.Background(), a, runningInput)
	if err != nil || !began {
		t.Fatal(running, began, err)
	}
	var before int
	if err = first.DB.QueryRow(`SELECT COUNT(*) FROM idempotency`).Scan(&before); err != nil || before < 2 {
		t.Fatal(before, err)
	}
	archive := filepath.Join(t.TempDir(), "planning.tar.gz")
	if err = s.Backup(context.Background(), archive); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "restored")
	if err = Restore(context.Background(), archive, destination, ""); err != nil {
		t.Fatal(err)
	}
	store, err := Open(filepath.Join(destination, "platform.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	restored := &Service{Store: store, Root: destination}
	after, _ := store.Get(plan.ID)
	if Hash(after) != Hash(persisted) {
		t.Fatal("restore changed plan/provenance", after)
	}
	for _, input := range []M{in, runningInput} {
		replay, newAttempt, err := restored.BeginPlanGeneration(context.Background(), a, input)
		if err != nil || newAttempt {
			t.Fatal("restore authorized a second provider attempt", replay, newAttempt, err)
		}
		if S(input, "key") == "same-request" && (replay.Status != "succeeded" || S(replay.Data, "resultId") != plan.ID) {
			t.Fatal(replay)
		}
		if S(input, "key") == "interrupted-request" && (replay.ID != running.ID || replay.Status != "running") {
			t.Fatal(replay)
		}
	}
	var count int
	if err = store.DB.QueryRow(`SELECT COUNT(*) FROM idempotency`).Scan(&count); err != nil || count != before {
		t.Fatal(before, count, err)
	}
	if err = restored.VerifyIntegrity(context.Background()); err != nil {
		t.Fatal(err)
	}
}

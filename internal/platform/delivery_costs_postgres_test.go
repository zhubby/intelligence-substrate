package platform

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// Separate SQL clients must share source uniqueness, and restored accounting
// must retain both correction provenance and nonempty replay history.
func TestPostgresDeliveryCostsSerializeSourceAndRestoreLedger(t *testing.T) {
	dsn := os.Getenv("IS_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("set IS_TEST_POSTGRES_URL to an isolated PostgreSQL database")
	}
	admin, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := NewID("delivery_cost_test")
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
	a := Actor{ID: "cost-owner", Kind: "human", Admin: true}
	project := must(t, s, a, "projects.create", M{"title": "Concurrent cost allocation"})
	g := goal(t, s, a, project)
	type result struct {
		r   Resource
		err error
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for i, service := range []*Service{s, other} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			in := expenseInput(g, "one-provider-line")
			if i == 1 {
				in["key"] = "second-request"
			}
			r, err := service.Command(context.Background(), a, "expenses.record", in)
			results <- result{r, err}
		}()
	}
	wg.Wait()
	var winner Resource
	successes := 0
	for range 2 {
		outcome := <-results
		if outcome.err == nil {
			winner = outcome.r
			successes++
		} else {
			expectCostFault(t, outcome.err, 409)
		}
	}
	if successes != 1 {
		t.Fatalf("same provider line counted %d times", successes)
	}
	voidInput := M{"id": winner.ID, "key": "void-once", "reason": "Correct the synthetic provider allocation"}
	must(t, s, a, "expenses.void", voidInput)
	corrected := expenseInput(g, "one-provider-line")
	corrected["amountCents"], corrected["key"] = 900, "correction"
	current := must(t, s, a, "expenses.record", corrected)
	before, err := s.DeliveryCosts(context.Background(), a, project.ID, g.ID)
	if err != nil || before.ExternalRecordedCents != 900 || len(before.Expenses) != 2 {
		t.Fatal("incorrect pre-backup ledger", before, err)
	}
	archive := filepath.Join(t.TempDir(), "costs.tar.gz")
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
	after, err := restored.DeliveryCosts(context.Background(), a, project.ID, g.ID)
	if err != nil || after.RecordedTotalCents != 900 || after.ModelBudgetCents != 10000 || len(after.Expenses) != 2 {
		t.Fatal("restore changed accounting", after, err)
	}
	var remembered int
	if err = store.DB.QueryRow(`SELECT COUNT(*) FROM idempotency`).Scan(&remembered); err != nil || remembered != 3 {
		t.Fatal("restore lost expense replay history", remembered, err)
	}
	replay := must(t, restored, a, "expenses.record", corrected)
	if replay.ID != current.ID || S(replay.Data, "replacesExpenseId") != winner.ID {
		t.Fatal("restore replay lost correction identity", replay)
	}
	if err = restored.VerifyIntegrity(context.Background()); err != nil {
		t.Fatal(err)
	}
}

package platform

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestPostgresRequirementDraftConcurrentAdoptionAndRestore(t *testing.T) {
	dsn := os.Getenv("IS_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("set IS_TEST_POSTGRES_URL to an isolated PostgreSQL database")
	}
	admin, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := NewID("requirement_draft_test")
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
	a := Actor{ID: "business-owner", Kind: "human", Admin: true}
	p := must(t, s, a, "projects.create", M{"title": "Concurrent requirement review"})
	g := goal(t, s, a, p)
	answeredDraftQuestion(t, s, a, g)
	drafts := []Resource{must(t, s, a, "requirements.propose", requirementDraftInput(g, "a")), must(t, s, a, "requirements.propose", requirementDraftInput(g, "b"))}
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i, service := range []*Service{s, other} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := service.Command(context.Background(), a, "requirements.apply", requirementDraftApply(drafts[i], "adopt"))
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	winners := 0
	for range 2 {
		if err := <-results; err == nil {
			winners++
		} else {
			clarificationFault(t, err, 409)
		}
	}
	if winners != 1 {
		t.Fatal("concurrent adoption wrote multiple revisions", winners)
	}
	current, _ := first.Get(g.ID)
	if current.Revision != 2 {
		t.Fatal(current)
	}
	proposal, _ := first.Get(S(current.Data, "requirementProposalId"))
	must(t, s, a, "goals.approve", M{"id": g.ID, "expectedRequirementRevision": 2})
	requests, started, err := s.BeginRequirementGeneration(context.Background(), a, M{"goalId": goal(t, s, a, p).ID, "expectedRequirementRevision": 1, "key": "running-intent"})
	if err != nil || !started {
		t.Fatal(requests, err)
	}
	var before int
	if err = first.DB.QueryRow(`SELECT COUNT(*) FROM idempotency`).Scan(&before); err != nil || before < 4 {
		t.Fatal(before, err)
	}
	archive := filepath.Join(t.TempDir(), "requirements.tar.gz")
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
	after, _ := store.Get(proposal.ID)
	if Hash(after) != Hash(proposal) {
		t.Fatal("restore changed reviewed content or provenance")
	}
	resources, _ := store.List(p.ID, "", g.ID)
	approved, _ := store.Get(g.ID)
	if answers, err := ApprovedClarifications(approved, resources); err != nil || len(answers) != 1 {
		t.Fatal(answers, err)
	}
	var count int
	if err = store.DB.QueryRow(`SELECT COUNT(*) FROM idempotency`).Scan(&count); err != nil || count != before {
		t.Fatal(before, count, err)
	}
	if replay := must(t, restored, a, "requirements.apply", requirementDraftApply(proposal, "adopt")); replay.Revision != 2 {
		t.Fatal(replay)
	}
	intent, newAttempt, err := restored.BeginRequirementGeneration(context.Background(), a, M{"goalId": requests.GoalID, "expectedRequirementRevision": 1, "key": "running-intent"})
	if err != nil || newAttempt || intent.ID != requests.ID || intent.Status != "running" {
		t.Fatal("restored intent allowed model resend", intent, newAttempt, err)
	}
	if err = restored.VerifyIntegrity(context.Background()); err != nil {
		t.Fatal(err)
	}
}

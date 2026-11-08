package platform

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestPostgresClarificationDecisionsSerializeAndSurviveRestore(t *testing.T) {
	dsn := os.Getenv("IS_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("set IS_TEST_POSTGRES_URL to an isolated PostgreSQL database")
	}
	admin, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := NewID("clarification_test")
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
	a := Actor{ID: "business-owner", Kind: "human", Admin: true}
	project := must(t, s, a, "projects.create", M{"title": "Concurrent clarification decisions"})
	g := goal(t, s, a, project)
	batch := must(t, s, a, "clarifications.propose", clarificationInput(g, "question"))
	question := Obj(Items(batch.Data["questions"])[0])
	answers := []M{
		{"id": batch.ID, "questionId": S(question, "id"), "expectedRequirementRevision": 1, "key": "decision-a", "answer": "Only the customer can cancel."},
		{"id": batch.ID, "questionId": S(question, "id"), "expectedRequirementRevision": 1, "key": "decision-b", "answer": "Staff can cancel with a reason."},
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i, service := range []*Service{s, other} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := service.Command(context.Background(), a, "clarifications.answer", answers[i])
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
		t.Fatal("concurrent people overwrote the first answer", winners)
	}
	must(t, s, a, "goals.approve", M{"id": g.ID, "expectedRequirementRevision": 1})
	approved, _ := s.Store.List(project.ID, "approval", g.ID)
	confirmed, _ := s.Store.Get(batch.ID)

	// Approval and a new question serialize: either question wins and blocks
	// approval, or approval wins and freezes discovery. Never both succeed.
	race := goal(t, s, a, project)
	start = make(chan struct{})
	results = make(chan error, 2)
	for i, service := range []*Service{s, other} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			command, input := "goals.approve", M{"id": race.ID}
			if i == 1 {
				command, input = "clarifications.propose", clarificationInput(race, "racing-question")
			}
			_, err := service.Command(context.Background(), a, command, input)
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	winners = 0
	for range 2 {
		if err := <-results; err == nil {
			winners++
		} else {
			clarificationFault(t, err, 409)
		}
	}
	if winners != 1 {
		t.Fatal("new open question raced through approval", winners)
	}

	var beforeKeys int
	if err := first.DB.QueryRow(`SELECT COUNT(*) FROM idempotency`).Scan(&beforeKeys); err != nil || beforeKeys < 2 {
		t.Fatal(beforeKeys, err)
	}
	archive := filepath.Join(t.TempDir(), "clarifications.tar.gz")
	if err := s.Backup(context.Background(), archive); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "restored")
	if err := Restore(context.Background(), archive, destination, ""); err != nil {
		t.Fatal(err)
	}
	store, err := Open(filepath.Join(destination, "platform.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	restored := &Service{Store: store, Root: destination}
	after, _ := store.Get(batch.ID)
	if Hash(after) != Hash(confirmed) {
		t.Fatal("restore changed the human answer or its provenance")
	}
	approvals, _ := store.List(project.ID, "approval", g.ID)
	if len(approvals) != 1 || Hash(approvals[0]) != Hash(approved[0]) {
		t.Fatal("restore changed the frozen question snapshot")
	}
	var afterKeys int
	if err := store.DB.QueryRow(`SELECT COUNT(*) FROM idempotency`).Scan(&afterKeys); err != nil || afterKeys != beforeKeys {
		t.Fatal("lost nonempty retry history", beforeKeys, afterKeys, err)
	}
	if replay := must(t, restored, a, "clarifications.propose", clarificationInput(g, "question")); replay.ID != batch.ID || replay.Status != "answered" {
		t.Fatal("restore resurrected draft questions", replay)
	}
	if err := restored.VerifyIntegrity(context.Background()); err != nil {
		t.Fatal(err)
	}
}

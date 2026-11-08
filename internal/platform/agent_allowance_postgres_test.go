package platform

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestPostgresAgentAllowanceReplacementAndUnsettledRestore(t *testing.T) {
	dsn := os.Getenv("IS_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("set IS_TEST_POSTGRES_URL to an isolated PostgreSQL database")
	}
	admin, e := Open(dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer admin.Close()
	schema := NewID("agent_allowance_test")
	if _, e = admin.DB.Exec(`CREATE SCHEMA ` + schema); e != nil {
		t.Fatal(e)
	}
	defer admin.DB.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
	u, e := url.Parse(dsn)
	if e != nil {
		t.Fatal(e)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	first, e := Open(u.String())
	if e != nil {
		t.Fatal(e)
	}
	defer first.Close()
	second, e := Open(u.String())
	if e != nil {
		t.Fatal(e)
	}
	defer second.Close()
	root := t.TempDir()
	if evidence := os.Getenv("IS_AGENT_ALLOWANCE_EVIDENCE_DIR"); evidence != "" {
		root = filepath.Join(evidence, "postgres")
		if e = os.Mkdir(root, 0700); e != nil {
			t.Fatal(e)
		}
	}
	s := &Service{Store: first, Root: filepath.Join(root, "source")}
	other := &Service{Store: second, Root: s.Root}
	owner := Actor{ID: "allowance-pg-owner", Kind: "human", Admin: true}
	project := must(t, s, owner, "projects.create", M{"title": "PostgreSQL bounded Agent recovery"})
	f := newMessageFixtureOn(t, s, owner, project, M{"id": "work", "budgetCents": 100, "timeoutSeconds": 600})
	seedAgentUsage(t, f, "running", 40, 7, "uncertain", 120)
	replacement := f.register(t, "Replacement")
	for _, service := range []*Service{s, other} {
		if got := must(t, service, replacement, "tasks.claim", M{}); got.Kind == "run" {
			t.Fatal("unknown expense escaped the cross-client fence")
		}
	}
	charges, _ := first.List(project.ID, "charge", f.g.ID)
	for _, charge := range charges {
		if charge.Status == "uncertain" {
			must(t, s, owner, "charges.reconcile", M{"id": charge.ID, "chargedCents": 25, "reason": "Synthetic PostgreSQL statement confirms 25 cents"})
		}
	}
	must(t, s, owner, "tasks.retry", M{"id": S(f.run.Data, "taskId")})
	rollback := errors.New("abort replacement transaction")
	e = first.Update(context.Background(), func(tx *Tx) error {
		worker, err := tx.Get(replacement.WorkerID)
		if err != nil {
			return err
		}
		next, err := s.claimTask(tx, replacement, worker)
		if err != nil {
			return err
		}
		if next.Kind != "run" || N(next.Data, "reservedCents") != 75 {
			t.Fatal("incorrect tentative allowance")
		}
		return rollback
	})
	if !errors.Is(e, rollback) {
		t.Fatal(e)
	}
	runs, _ := first.List(project.ID, "run", f.g.ID)
	if len(runs) != 1 {
		t.Fatal("rolled-back lease survived")
	}
	type outcome struct {
		run Resource
		err error
	}
	results := make(chan outcome, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, service := range []*Service{s, other} {
		wg.Add(1)
		go func(service *Service) {
			defer wg.Done()
			<-start
			r, e := service.Command(context.Background(), replacement, "tasks.claim", M{})
			results <- outcome{r, e}
		}(service)
	}
	close(start)
	wg.Wait()
	close(results)
	var winner Resource
	successes, rejections := 0, 0
	for result := range results {
		if result.err == nil && result.run.Kind == "run" {
			winner = result.run
			successes++
		} else if fault, ok := result.err.(*Fault); ok && fault.Status == 409 {
			rejections++
		} else {
			t.Fatal("unexpected claim", result.err, result.run.Kind)
		}
	}
	if successes != 1 || rejections != 1 || N(winner.Data, "reservedCents") != 75 || N(Obj(winner.Data["agentAllowance"]), "usedModelCalls") != 7 {
		t.Fatal("concurrent replacement reset or duplicated allowance", successes, rejections)
	}
	must(t, s, replacement, "tasks.complete", M{"id": winner.ID, "leaseToken": S(winner.Data, "leaseToken"), "exitCode": 0, "result": M{"summary": "Continued within the original remaining allowance"}})
	// Save an actually unresolved/expired run, not just a completed happy path.
	paused := newMessageFixtureOn(t, s, owner, project, M{"id": "work", "budgetCents": 100, "timeoutSeconds": 600})
	seedAgentUsage(t, paused, "running", 0, 1, "reserved", 120)
	archive := filepath.Join(root, "backup.tar.gz")
	if e = s.Backup(context.Background(), archive); e != nil {
		t.Fatal(e)
	}
	destination := filepath.Join(root, "restored")
	if e = Restore(context.Background(), archive, destination, ""); e != nil {
		t.Fatal(e)
	}
	restored, e := Open(filepath.Join(destination, "platform.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer restored.Close()
	paused.s = &Service{Store: restored, Root: destination}
	if e = paused.s.VerifyIntegrity(context.Background()); e != nil {
		t.Fatal(e)
	}
	worker := paused.register(t, "Restored replacement")
	if got := must(t, paused.s, worker, "tasks.claim", M{}); got.Kind == "run" {
		t.Fatal("restore forgot unresolved provider intent")
	}
	unknown, _ := restored.List(project.ID, "charge", paused.g.ID)
	if len(unknown) != 1 || unknown[0].Status != "reserved" {
		t.Fatal("restore erased provider uncertainty")
	}
	must(t, paused.s, owner, "charges.reconcile", M{"id": unknown[0].ID, "chargedCents": 5, "reason": "Synthetic provider receipt after restore"})
	must(t, paused.s, owner, "tasks.retry", M{"id": S(paused.run.Data, "taskId")})
	next := must(t, paused.s, worker, "tasks.claim", M{})
	if next.Kind != "run" || N(next.Data, "reservedCents") != 95 || N(Obj(next.Data["agentAllowance"]), "usedModelCalls") != 1 {
		t.Fatal("restore/reconcile refunded a call or reset money")
	}
	if e = paused.s.VerifyIntegrity(context.Background()); e != nil {
		t.Fatal(e)
	}
	facts := M{"passed": true, "projectId": project.ID, "concurrentGoalId": f.g.ID, "concurrentTaskId": S(f.run.Data, "taskId"), "replacementRunId": winner.ID, "mutatingClaims": successes, "conflictingClaims": rejections, "rollbackRetainedNoLease": true, "unsettledGoalId": paused.g.ID, "unsettledTaskId": S(paused.run.Data, "taskId"), "unsettledRunId": paused.run.ID, "restoredRunId": next.ID, "archive": archive, "restoredDatabase": filepath.Join(destination, "platform.db")}
	body, _ := json.MarshalIndent(facts, "", "  ")
	if e = os.WriteFile(filepath.Join(root, "proof.json"), append(body, '\n'), 0600); e != nil {
		t.Fatal(e)
	}
}

package platform

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestPostgresBlueprintConcurrentProposalApprovalClaimAndRestore(t *testing.T) {
	dsn := os.Getenv("IS_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("set IS_TEST_POSTGRES_URL to an isolated PostgreSQL database")
	}
	admin, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := NewID("blueprint_test")
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
	enableBlueprints(t, s)
	other := &Service{Store: second, Root: s.Root, Blueprints: s.Blueprints}
	a := Actor{ID: "blueprint-owner", Kind: "human", Admin: true}
	project := must(t, s, a, "projects.create", M{"title": "Concurrent governed application"})
	g := goal(t, s, a, project)
	must(t, s, a, "goals.approve", M{"id": g.ID})
	type outcome struct {
		r   Resource
		err error
	}
	concurrent := func(command string, actor Actor, in M) []outcome {
		t.Helper()
		start := make(chan struct{})
		results := make(chan outcome, 2)
		var wg sync.WaitGroup
		for _, svc := range []*Service{s, other} {
			wg.Add(1)
			go func(svc *Service) {
				defer wg.Done()
				<-start
				r, err := svc.Command(context.Background(), actor, command, in)
				results <- outcome{r, err}
			}(svc)
		}
		close(start)
		wg.Wait()
		return []outcome{<-results, <-results}
	}
	in := blueprintInput(s, g, "one-source-selection")
	var selection Resource
	for _, result := range concurrent("blueprints.propose", a, in) {
		if result.err != nil || (selection.ID != "" && selection.ID != result.r.ID) {
			t.Fatal("concurrent proposal duplicated", result)
		}
		selection = result.r
	}
	plan := must(t, s, a, "plans.propose", blueprintPlan(g, selection))
	winners := 0
	for _, result := range concurrent("plans.approve", a, M{"id": plan.ID}) {
		if result.err == nil {
			winners++
		} else {
			clarificationFault(t, result.err, 409)
		}
	}
	if winners != 1 {
		t.Fatal("approval did not have one winner", winners)
	}
	w := must(t, s, a, "workers.register", M{"projectId": project.ID, "title": "Concurrent seed node", "runtime": "linux", "roles": []any{"developer"}, "capabilities": []any{"code"}})
	worker := Actor{ID: "worker:" + w.ID, Kind: "worker", ProjectID: project.ID, WorkerID: w.ID, Roles: []string{"developer"}}
	claims, busy := 0, 0
	for _, result := range concurrent("tasks.claim", worker, M{}) {
		if result.err != nil {
			clarificationFault(t, result.err, 409)
			if result.err.Error() != "节点已有正在执行的工作项" {
				t.Fatal("unexpected competing claim conflict", result.err)
			}
			busy++
		} else if result.r.Kind == "run" {
			claims++
		} else {
			t.Fatal("initializer claim did not return a run", result.r)
		}
	}
	if claims != 1 || busy != 1 {
		t.Fatal("initializer must have one lease and one busy conflict", claims, busy)
	}
	archive := filepath.Join(t.TempDir(), "blueprint.tar.gz")
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
	if replay := must(t, restored, a, "blueprints.propose", in); replay.ID != selection.ID {
		t.Fatal("restore lost idempotency")
	}
	resources, _ := first.List(project.ID, "", g.ID)
	for _, before := range resources {
		after, err := store.Get(before.ID)
		if err != nil || Hash(before) != Hash(after) {
			t.Fatal("backup changed blueprint authority", before.ID, err)
		}
	}
	if err = restored.VerifyIntegrity(context.Background()); err != nil {
		t.Fatal(err)
	}
}

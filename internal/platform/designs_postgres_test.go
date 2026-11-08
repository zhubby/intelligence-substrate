package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestPostgresDesignConcurrentProposalApprovalAndRestore(t *testing.T) {
	dsn := os.Getenv("IS_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("set IS_TEST_POSTGRES_URL to an isolated PostgreSQL database")
	}
	admin, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := NewID("design_test")
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
	root := t.TempDir()
	if evidence := os.Getenv("IS_DESIGN_EVIDENCE_DIR"); evidence != "" {
		root = filepath.Join(evidence, "postgres-proof")
		if err = os.Mkdir(root, 0700); err != nil {
			t.Fatal(err)
		}
	}
	s := &Service{Store: first, Root: filepath.Join(root, "source")}
	other := &Service{Store: second, Root: s.Root}
	owner := Actor{ID: "design-project-owner", Kind: "human", Admin: true}
	project := must(t, s, owner, "projects.create", M{"title": "Concurrent reviewed design"})
	g := goal(t, s, owner, project)
	must(t, s, owner, "goals.approve", M{"id": g.ID})
	repo := must(t, s, owner, "repositories.create", M{"projectId": project.ID, "title": "Prototype source"})
	prototype, err := s.Upload(context.Background(), owner, M{"goalId": g.ID, "repositoryId": repo.ID, "commit": repo.Data["head"], "target": "web", "type": "prototype", "name": "prototype.html"}, bytes.NewBufferString("<!doctype html><button>POSTGRES-DESIGN-PROTOTYPE</button>"))
	if err != nil {
		t.Fatal(err)
	}
	in := designInput(g, prototype, "same-durable-key")
	type outcome struct {
		r   Resource
		err error
	}
	concurrent := func(command string, input M) []outcome {
		start, results := make(chan struct{}), make(chan outcome, 2)
		var wg sync.WaitGroup
		for _, service := range []*Service{s, other} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				r, err := service.Command(context.Background(), owner, command, input)
				results <- outcome{r, err}
			}()
		}
		close(start)
		wg.Wait()
		return []outcome{<-results, <-results}
	}
	var design Resource
	for _, item := range concurrent("designs.propose", in) {
		if item.err != nil || item.r.Revision != 1 || (design.ID != "" && design.ID != item.r.ID) {
			t.Fatal("two SQL clients created duplicate designs", item)
		}
		design = item.r
	}
	plan := must(t, s, owner, "plans.propose", designPlan(g, design))
	winners := 0
	for _, item := range concurrent("plans.approve", M{"id": plan.ID}) {
		if item.err == nil {
			winners++
		} else {
			clarificationFault(t, item.err, 409)
		}
	}
	if winners != 1 {
		t.Fatal("concurrent approvals did not have one winner", winners)
	}
	tasks, _ := first.List(project.ID, "task", g.ID)
	approvals, _ := first.List(project.ID, "approval", g.ID)
	if len(tasks) != 1 || len(approvals) != 2 {
		t.Fatal("approval duplicated or partially committed work", tasks, approvals)
	}
	archive := filepath.Join(root, "backup.tar.gz")
	if err = s.Backup(context.Background(), archive); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(root, "restored")
	if err = Restore(context.Background(), archive, destination, ""); err != nil {
		t.Fatal(err)
	}
	store, err := Open(filepath.Join(destination, "platform.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	restored := &Service{Store: store, Root: destination}
	resources, _ := first.List(project.ID, "", g.ID)
	for _, before := range resources {
		after, err := store.Get(before.ID)
		if err != nil || Hash(after) != Hash(before) {
			t.Fatal("restore changed a design/approval/task/artifact row", before.ID, after, err)
		}
	}
	replay := must(t, restored, owner, "designs.propose", in)
	if replay.ID != design.ID {
		t.Fatal("restore lost durable proposal idempotency", replay)
	}
	current, _ := store.Get(g.ID)
	frozen, err := ApprovedDesign(current, plan.ID, resources)
	if err != nil || S(frozen, "id") != design.ID {
		t.Fatal("restored approval lost design authority", frozen, err)
	}
	if err = restored.VerifyIntegrity(context.Background()); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("IS_DESIGN_EVIDENCE_DIR") != "" {
		body, _ := json.MarshalIndent(M{"passed": true, "projectId": project.ID, "goalId": g.ID, "designId": design.ID, "planId": plan.ID, "prototypeId": prototype.ID, "prototypeDigest": prototype.Data["digest"], "concurrentProposalResults": 2, "uniqueDesigns": 1, "approvalWinners": winners, "tasks": len(tasks), "approvals": len(approvals), "restoredRowsCompared": len(resources), "frozenDesign": frozen, "proposalInput": in}, "", "  ")
		if err = os.WriteFile(filepath.Join(root, "result.json"), body, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

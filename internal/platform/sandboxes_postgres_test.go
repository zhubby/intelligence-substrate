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

func TestPostgresSandboxConcurrentPrepareReconcileAndRestore(t *testing.T) {
	dsn := os.Getenv("IS_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("set IS_TEST_POSTGRES_URL to an isolated PostgreSQL database")
	}
	admin, e := Open(dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer admin.Close()
	schema := NewID("sandbox_test")
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
	if evidence := os.Getenv("IS_AX_SANDBOX_EVIDENCE_DIR"); evidence != "" {
		root = filepath.Join(evidence, "postgres")
		if e = os.Mkdir(root, 0700); e != nil {
			t.Fatal(e)
		}
	}
	s := &Service{Store: first, Root: filepath.Join(root, "source")}
	other := &Service{Store: second, Root: s.Root}
	owner := Actor{ID: "sandbox-pg-owner", Kind: "human", Admin: true}
	project := must(t, s, owner, "projects.create", M{"title": "Durable AX lifecycle"})
	f := newMessageFixtureOn(t, s, owner, project)
	input := sandboxInput(f)
	Obj(input["backend"])["transport"] = sandboxTransport()
	abort := errors.New("abort prepared remote identity")
	if e = first.Update(context.Background(), func(tx *Tx) error {
		if _, e := s.prepareSandbox(tx, f.worker, input); e != nil {
			return e
		}
		return abort
	}); !errors.Is(e, abort) {
		t.Fatal(e)
	}
	if items, _ := first.List(project.ID, "sandbox", f.g.ID); len(items) != 0 {
		t.Fatal("rolled-back ownership survived")
	}
	type outcome struct {
		r Resource
		e error
	}
	race := func(actor Actor, command string, input M) []outcome {
		start := make(chan struct{})
		results := make(chan outcome, 2)
		var wg sync.WaitGroup
		for _, service := range []*Service{s, other} {
			wg.Add(1)
			go func(service *Service) {
				defer wg.Done()
				<-start
				r, e := service.Command(context.Background(), actor, command, input)
				results <- outcome{r, e}
			}(service)
		}
		close(start)
		wg.Wait()
		close(results)
		all := []outcome{}
		for r := range results {
			all = append(all, r)
		}
		return all
	}
	prepared := race(f.worker, "sandboxes.prepare", input)
	var remote Resource
	fresh := 0
	for _, item := range prepared {
		if item.e != nil || remote.ID != "" && item.r.ID != remote.ID {
			t.Fatal("duplicate ownership", item.e)
		}
		remote = item.r
		if !B(item.r.Data, "replayed") {
			fresh++
		}
	}
	if fresh != 1 {
		t.Fatal("prepare did not distinguish exact replay", fresh)
	}
	must(t, s, owner, "workers.disable", M{"id": f.worker.WorkerID})
	expireSandboxRun(t, f)
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
	f.s = &Service{Store: restored, Root: destination}
	stored, e := restored.Get(remote.ID)
	if e != nil || Hash(Obj(stored.Data["backend"])["transport"]) != Hash(sandboxTransport()) || S(stored.Data, "backendDigest") != Hash(input["backend"]) {
		t.Fatal("portable restore lost exact public transport identity", e)
	}
	replacement := f.register(t, "Replacement after portable restore")
	if next := must(t, f.s, replacement, "tasks.claim", M{}); next.Kind == "run" {
		t.Fatal("restore lost the remote execution barrier")
	}
	decision := M{"id": remote.ID, "name": remote.Data["name"], "backendDigest": remote.Data["backendDigest"], "key": "operator-observation", "observation": "Operator isolated the original host and verified exact Task, Workspace and Actor deletion"}
	for _, item := range race(owner, "sandboxes.reconcile", decision) {
		if item.e != nil || item.r.ID != remote.ID || item.r.Status != "stopped" {
			t.Fatal("concurrent decision duplicated or failed", item.e)
		}
	}
	must(t, f.s, owner, "sandboxes.reconcile", decision)
	must(t, f.s, owner, "tasks.retry", M{"id": S(f.run.Data, "taskId")})
	next := must(t, f.s, replacement, "tasks.claim", M{})
	if next.Kind != "run" || next.ID == f.run.ID {
		t.Fatal("reconciled task did not get a fresh lease")
	}
	if e = f.s.VerifyIntegrity(context.Background()); e != nil {
		t.Fatal(e)
	}
	proof := M{"passed": true, "projectId": project.ID, "goalId": f.g.ID, "taskId": S(f.run.Data, "taskId"), "sandboxId": remote.ID, "originalRunId": f.run.ID, "replacementRunId": next.ID, "freshPrepareResponses": fresh, "replayedPrepareResponses": 1, "rollbackRetainedNoOwnership": true, "restoreBlockedBeforeDecision": true, "realAXOrSubstrate": false}
	body, _ := json.MarshalIndent(proof, "", "  ")
	if e = os.WriteFile(filepath.Join(root, "proof.json"), append(body, '\n'), 0600); e != nil {
		t.Fatal(e)
	}
}

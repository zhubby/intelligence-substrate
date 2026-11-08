package platform

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestPostgresMacOSSigningWaitConcurrentStatusAndExactRestore(t *testing.T) {
	dsn := os.Getenv("IS_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("set isolated PostgreSQL database")
	}
	root := t.TempDir()
	if evidence := os.Getenv("IS_MACOS_SIGNING_EVIDENCE_DIR"); evidence != "" {
		root = filepath.Join(evidence, "postgres-proof")
		if err := os.Mkdir(root, 0700); err != nil {
			t.Fatal(err)
		}
	}
	admin, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := NewID("macos_signing_test")
	if _, err = admin.DB.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatal(err)
	}
	defer admin.DB.Exec("DROP SCHEMA " + schema + " CASCADE")
	u, _ := url.Parse(dsn)
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	one, err := Open(u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer one.Close()
	two, err := Open(u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer two.Close()
	s, other := &Service{Store: one, Root: filepath.Join(root, "pg")}, &Service{Store: two, Root: filepath.Join(root, "pg")}
	owner := Actor{ID: "macos-signing-owner", Kind: "human", Admin: true}
	project := must(t, s, owner, "projects.create", M{"title": "Durable macOS notarization"})
	f := setupMacOSSigningFixture(t, s, owner, project)
	f.build(t)
	w := must(t, s, owner, "workers.register", M{"projectId": project.ID, "title": "Competing native signer", "runtime": "darwin", "roles": []any{"release"}, "capabilities": []any{"build"}})
	actors := []Actor{f.signer, {ID: "worker:" + w.ID, Kind: "worker", ProjectID: project.ID, WorkerID: w.ID, Roles: []string{"release"}}}
	type outcome struct {
		r   Resource
		a   Actor
		err error
	}
	concurrent := func(actors []Actor) []outcome {
		start := make(chan struct{})
		results := make(chan outcome, 2)
		var group sync.WaitGroup
		for i, svc := range []*Service{s, other} {
			group.Add(1)
			go func(svc *Service, a Actor) {
				defer group.Done()
				<-start
				r, e := svc.Command(context.Background(), a, "tasks.claim", M{})
				results <- outcome{r, a, e}
			}(svc, actors[i])
		}
		close(start)
		group.Wait()
		return []outcome{<-results, <-results}
	}
	var first Resource
	claims := 0
	for _, result := range concurrent(actors) {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.r.Kind == "run" {
			claims++
			first, f.signer = result.r, result.a
		}
	}
	if claims != 1 {
		t.Fatal("multiple mutating signing leases", claims)
	}
	pending := macOSSigningPending(f, first)
	must(t, s, f.signer, "tasks.complete", pending)
	must(t, other, f.signer, "tasks.complete", pending)
	taskID := S(first.Data, "taskId")
	archive := filepath.Join(root, "waiting.tar.gz")
	if err = s.Backup(context.Background(), archive); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(root, "restored")
	if err = Restore(context.Background(), archive, destination, ""); err != nil {
		t.Fatal(err)
	}
	restoredStore, err := Open(filepath.Join(destination, "platform.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer restoredStore.Close()
	restored := &Service{Store: restoredStore, Root: destination}
	for _, id := range []string{f.unsigned.ID, first.ID, taskID} {
		before, _ := one.Get(id)
		after, e := restoredStore.Get(id)
		if e != nil || Hash(before) != Hash(after) {
			t.Fatal("wait restore changed exact original state", id, e)
		}
	}
	if idle := must(t, restored, f.signer, "tasks.claim", M{}); idle.Kind != "worker" {
		t.Fatal("restore queried early or signed again")
	}
	dueMacOSSigning(t, s, taskID)
	claims, busy := 0, 0
	var status Resource
	for _, result := range concurrent([]Actor{f.signer, f.signer}) {
		if result.err != nil {
			clarificationFault(t, result.err, 409)
			if result.err.Error() != "节点已有正在执行的工作项" {
				t.Fatal(result.err)
			}
			busy++
			continue
		}
		if result.r.Kind == "run" {
			claims++
			status = result.r
		}
	}
	if claims != 1 || busy != 1 || S(Obj(status.Data["task"]), "signingPhase") != "status" {
		t.Fatal("status competition did not produce one read-only lease", claims, busy, status)
	}
	artifact := f.uploadSigned(t, status)
	completion := macOSSigningCompletion(f, status, artifact)
	must(t, s, f.signer, "tasks.complete", completion)
	must(t, other, f.signer, "tasks.complete", completion)
	task, _ := one.Get(taskID)
	if N(task.Data, "attempts") != 1 || N(task.Data, "signingPolls") != 1 {
		t.Fatal("status repeated mutating attempt", task)
	}
	// Resume the archived waiting state independently. It can only query the same
	// original operation, even though it is a new control-plane database.
	dueMacOSSigning(t, restored, taskID)
	run := must(t, restored, f.signer, "tasks.claim", M{})
	if S(Obj(run.Data["task"]), "signingPhase") != "status" || Hash(Obj(run.Data["task"])["signingOperation"]) != Hash(task.Data["signingOperation"]) {
		t.Fatal("restored operation lost its immutable notary binding", run)
	}
	restoredFixture := f
	restoredFixture.s = restored
	restoredArtifact := restoredFixture.uploadSigned(t, run)
	must(t, restored, f.signer, "tasks.complete", macOSSigningCompletion(restoredFixture, run, restoredArtifact))
	if err = restored.VerifyArtifact(restoredArtifact); err != nil {
		t.Fatal(err)
	}
	if err = restored.VerifyIntegrity(context.Background()); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("IS_MACOS_SIGNING_EVIDENCE_DIR") != "" {
		if err = restored.Backup(context.Background(), filepath.Join(root, "completed.tar.gz")); err != nil {
			t.Fatal(err)
		}
		facts := M{"passed": true, "syntheticAppleOnly": true, "realNotarization": false, "projectId": project.ID, "goalId": f.goal.ID, "taskId": taskID, "firstRunId": first.ID, "statusRunId": run.ID, "artifactId": restoredArtifact.ID, "unsignedArtifactId": f.unsigned.ID, "mutatingClaims": 1, "statusClaims": 1, "busyConflicts": busy, "attempts": 1, "polls": 1}
		body, _ := json.MarshalIndent(facts, "", "  ")
		if err = os.WriteFile(filepath.Join(root, "result.json"), append(body, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

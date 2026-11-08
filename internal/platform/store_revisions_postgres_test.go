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

func TestPostgresStoreRevisionConcurrentApprovalDispatchAndRestore(t *testing.T) {
	testPostgresStoreRevision(t, false)
}

func TestPostgresStoreScreenshotRevisionConcurrentApprovalDispatchAndRestore(t *testing.T) {
	testPostgresStoreRevision(t, true)
}

func testPostgresStoreRevision(t *testing.T, screenshots bool) {
	dsn := os.Getenv("IS_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("set isolated PostgreSQL database")
	}
	fixture := storeRevisionFixture
	if screenshots {
		fixture = storeScreenshotFixture
	}
	source, officer, worker, r, issue, material := fixture(t)
	root := t.TempDir()
	evidence := os.Getenv("IS_STORE_REVISION_EVIDENCE_DIR")
	if screenshots {
		evidence = os.Getenv("IS_SCREENSHOT_REVISION_EVIDENCE_DIR")
	}
	if evidence != "" {
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
	schema := NewID("store_revision_test")
	if _, err = admin.DB.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatal(err)
	}
	defer admin.DB.Exec("DROP SCHEMA " + schema + " CASCADE")
	u, _ := url.Parse(dsn)
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	seed := filepath.Join(root, "source.tar.gz")
	if err = source.Backup(context.Background(), seed); err != nil {
		t.Fatal(err)
	}
	pgRoot := filepath.Join(root, "pg")
	if err = Restore(context.Background(), seed, pgRoot, u.String()); err != nil {
		t.Fatal(err)
	}
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
	s, other := &Service{Store: one, Root: pgRoot}, &Service{Store: two, Root: pgRoot}
	type outcome struct {
		r   Resource
		err error
	}
	concurrent := func(actor Actor, name string, in M) []outcome {
		start := make(chan struct{})
		results := make(chan outcome, 2)
		var wg sync.WaitGroup
		for _, svc := range []*Service{s, other} {
			wg.Add(1)
			go func(svc *Service) {
				defer wg.Done()
				<-start
				value, err := svc.Command(context.Background(), actor, name, in)
				results <- outcome{value, err}
			}(svc)
		}
		close(start)
		wg.Wait()
		return []outcome{<-results, <-results}
	}
	author := Actor{ID: "pg-material-author", Kind: "human", Admin: true}
	proposal := M{"issueId": issue.ID, "metadataArtifactId": material.ID, "key": "one-proposal"}
	outcomes := concurrent(author, "store_revisions.propose", proposal)
	rev := outcomes[0].r
	for _, result := range outcomes {
		if result.err != nil || result.r.ID != rev.ID {
			t.Fatal("concurrent proposal duplicated", result)
		}
	}
	approval := M{"id": rev.ID, "expectedBinding": S(rev.Data, "binding")}
	for _, result := range concurrent(officer, "store_revisions.approve", approval) {
		if result.err != nil || result.r.ID != rev.ID {
			t.Fatal("concurrent approval failed", result)
		}
	}
	dispatch := M{"id": rev.ID, "workerId": worker.WorkerID, "key": "one-material-operation"}
	outcomes = concurrent(officer, "store_revisions.dispatch", dispatch)
	task := outcomes[0].r
	for _, result := range outcomes {
		if result.err != nil || result.r.ID != task.ID {
			t.Fatal("concurrent dispatch duplicated", result)
		}
	}
	runs := concurrent(worker, "tasks.claim", M{})
	count, busy := 0, 0
	var run Resource
	for _, result := range runs {
		if result.err != nil {
			clarificationFault(t, result.err, 409)
			if result.err.Error() != "节点已有正在执行的工作项" {
				t.Fatal("unexpected competing material claim conflict", result.err)
			}
			busy++
		} else if result.r.Kind == "run" {
			count++
			run = result.r
		} else {
			t.Fatal("material claim returned an unexpected resource", result.r)
		}
	}
	if count != 1 || busy != 1 {
		t.Fatal("material operation must have one lease and one busy conflict", count, busy)
	}
	state := "awaiting_review"
	if screenshots {
		state = "processing"
	}
	complete := M{"id": run.ID, "leaseToken": S(run.Data, "leaseToken"), "exitCode": 0, "result": M{"state": state, "externalId": "apple:123:upload1"}}
	for _, result := range concurrent(worker, "tasks.complete", complete) {
		if result.err != nil || result.r.ID != run.ID {
			t.Fatal("completion replay not idempotent", result)
		}
	}
	if screenshots {
		must(t, s, officer, "releases.refresh", M{"id": r.ID, "target": "ios-appstore", "key": "ready"})
		query := must(t, s, worker, "tasks.claim", M{})
		must(t, s, worker, "tasks.complete", M{"id": query.ID, "leaseToken": S(query.Data, "leaseToken"), "exitCode": 0, "result": M{"state": "processing", "externalId": "apple:123:upload1", "continueRequired": true}})
	}
	current, _ := one.Get(r.ID)
	if S(current.Data, "binding") != S(r.Data, "binding") || S(releaseTarget(current, "ios-appstore"), "artifactId") != S(releaseTarget(r, "ios-appstore"), "artifactId") {
		t.Fatal("material operation replaced original package or release approval")
	}
	archive := filepath.Join(root, "backup.tar.gz")
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
	resources, _ := one.List(r.ProjectID, "", "")
	for _, before := range resources {
		after, err := restoredStore.Get(before.ID)
		if err != nil || Hash(before) != Hash(after) {
			t.Fatal("restore changed material approval", before.ID, err)
		}
	}
	if replay := must(t, restored, officer, "store_revisions.dispatch", dispatch); replay.ID != task.ID {
		t.Fatal("restore lost dispatch idempotency")
	}
	if screenshots {
		second, err := Open(filepath.Join(destination, "platform.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer second.Close()
		s, other = &Service{Store: restoredStore, Root: destination}, &Service{Store: second, Root: destination}
		winners := 0
		var next Resource
		for _, result := range concurrent(worker, "tasks.claim", M{}) {
			if result.err == nil && result.r.Kind == "run" {
				winners++
				next = result.r
			}
		}
		if winners != 1 {
			t.Fatal("restored continuation must have one active lease", winners)
		}
		data := Obj(next.Data["task"])
		if S(data, "kind") != "publish_continue" || S(data, "materialScope") != "screenshots" || S(data, "operationKey") != S(task.Data, "operationKey") {
			t.Fatal("restored task lost exact screenshot authority", data)
		}
		done := M{"id": next.ID, "leaseToken": S(next.Data, "leaseToken"), "exitCode": 0, "result": M{"state": "awaiting_review", "externalId": "apple:123:upload1"}}
		for _, result := range concurrent(worker, "tasks.complete", done) {
			if result.err != nil {
				t.Fatal("restored completion is not idempotent", result.err)
			}
		}
	}
	if err = restored.VerifyIntegrity(context.Background()); err != nil {
		t.Fatal(err)
	}
	facts := M{"passed": true, "syntheticChannelOnly": true, "paidModelCalled": false, "releaseId": r.ID, "issueId": issue.ID, "revisionId": rev.ID, "taskId": task.ID, "runId": run.ID, "claims": count, "busyConflicts": busy, "restoredResources": len(resources), "screenshots": screenshots}
	body, _ := json.MarshalIndent(facts, "", "  ")
	if err = os.WriteFile(filepath.Join(root, "result.json"), append(body, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
}

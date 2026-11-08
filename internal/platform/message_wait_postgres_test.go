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

func TestPostgresMessageWaitConcurrentWakeClaimAndRestore(t *testing.T) {
	dsn := os.Getenv("IS_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("set IS_TEST_POSTGRES_URL to an isolated PostgreSQL database")
	}
	admin, e := Open(dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer admin.Close()
	schema := NewID("wait_test")
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
	if evidence := os.Getenv("IS_WAIT_EVIDENCE_DIR"); evidence != "" {
		root = filepath.Join(evidence, "wait-postgres-proof")
		if e = os.Mkdir(root, 0700); e != nil {
			t.Fatal(e)
		}
	}
	s := &Service{Store: first, Root: filepath.Join(root, "source")}
	other := &Service{Store: second, Root: s.Root}
	owner := Actor{ID: "wait-postgres-owner", Kind: "human", Admin: true}
	project := must(t, s, owner, "projects.create", M{"title": "PostgreSQL collaboration wait"})
	f := newMessageFixtureOn(t, s, owner, project)
	request, input := waitRequest(t, f)
	reply := M{"projectId": project.ID, "goalId": f.g.ID, "recipient": f.worker.ID, "recipientTaskId": S(f.run.Data, "taskId"), "type": "result", "key": "racing-reply", "causationId": request.ID, "payload": M{"answer": "UTC survives concurrent suspension and reply."}}
	start := make(chan struct{})
	errs := make(chan error, 4)
	var wg sync.WaitGroup
	for _, svc := range []*Service{s, other} {
		for _, command := range []string{"tasks.wait", "messages.send"} {
			wg.Add(1)
			go func(svc *Service, command string) {
				defer wg.Done()
				<-start
				a, in := f.worker, input
				if command == "messages.send" {
					a, in = owner, reply
				}
				_, e := svc.Command(context.Background(), a, command, in)
				errs <- e
			}(svc, command)
		}
	}
	close(start)
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	waits, _ := first.List(project.ID, "message_wait", f.g.ID)
	receipts, _ := first.List(project.ID, "message_receipt", f.g.ID)
	if len(waits) != 1 || waits[0].Status != "ready" || len(receipts) != 1 {
		t.Fatal("concurrent clients lost/duplicated wake", waits, receipts)
	}
	claims := make(chan Resource, 2)
	claimErrs := make(chan error, 2)
	for _, svc := range []*Service{s, other} {
		wg.Add(1)
		go func(svc *Service) {
			defer wg.Done()
			run, e := svc.Command(context.Background(), f.worker, "tasks.claim", M{})
			if e != nil {
				claimErrs <- e
			} else {
				claims <- run
			}
		}(svc)
	}
	wg.Wait()
	close(claims)
	close(claimErrs)
	count := 0
	var resumed Resource
	for run := range claims {
		if run.Kind == "run" {
			count++
			resumed = run
		}
	}
	if count != 1 || len(claimErrs) != 1 {
		t.Fatal("parallel continuation claim did not reserve one run", count, len(claimErrs))
	}
	for e := range claimErrs {
		if fault, ok := e.(*Fault); !ok || fault.Status != 409 {
			t.Fatal(e)
		}
	}
	must(t, s, f.worker, "tasks.complete", M{"id": resumed.ID, "leaseToken": S(resumed.Data, "leaseToken"), "exitCode": 0, "result": M{"summary": "Confirmed UTC"}})
	// A second goal is backed up while suspended, before any reply exists.
	paused := newMessageFixtureOn(t, s, owner, project)
	pendingRequest, pendingInput := waitRequest(t, paused)
	pendingWait := must(t, s, paused.worker, "tasks.wait", pendingInput)
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
	copyWait, e := restored.Get(pendingWait.ID)
	if e != nil || copyWait.Status != "waiting" {
		t.Fatal("restore lost suspension", e, copyWait)
	}
	must(t, paused.s, owner, "workers.disable", M{"id": paused.worker.WorkerID})
	waitReply(t, paused, pendingRequest, "restored-reply")
	replacement := paused.register(t, "Restored replacement")
	next := must(t, paused.s, replacement, "tasks.claim", M{})
	inbox := must(t, paused.s, replacement, "messages.receive", M{"runId": next.ID, "leaseToken": S(next.Data, "leaseToken")})
	if next.Kind != "run" || len(Items(inbox.Data["items"])) != 1 || S(Obj(next.Data["messageContinuation"]), "waitId") != pendingWait.ID {
		t.Fatal("restored wait could not resume", next, inbox)
	}
	record := M{"projectId": project.ID, "concurrentGoalId": f.g.ID, "concurrentWaitId": waits[0].ID, "concurrentReceiptId": receipts[0].ID, "suspendedGoalId": paused.g.ID, "suspendedWaitId": pendingWait.ID, "suspendedRequestId": pendingRequest.ID, "suspendedRunId": paused.run.ID, "restoredResumeRunId": next.ID, "archive": archive, "restoredDatabase": filepath.Join(destination, "platform.db")}
	if e = paused.s.VerifyIntegrity(context.Background()); e != nil {
		t.Fatal(e)
	}
	body, _ := json.MarshalIndent(record, "", "  ")
	if e = os.WriteFile(filepath.Join(root, "proof.json"), append(body, '\n'), 0600); e != nil {
		t.Fatal(e)
	}
	t.Log("Two PostgreSQL clients raced wait/reply, reserved one continuation, then restored a still-waiting goal and resumed it on a replacement Worker")
}

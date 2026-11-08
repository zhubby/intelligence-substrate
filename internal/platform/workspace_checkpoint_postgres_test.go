package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestPostgresWorkspaceCheckpointConcurrentReplayAndRestore(t *testing.T) {
	dsn := os.Getenv("IS_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("set IS_TEST_POSTGRES_URL to an isolated PostgreSQL database")
	}
	admin, e := Open(dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer admin.Close()
	schema := NewID("checkpoint_test")
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
	if evidence := os.Getenv("IS_WORKSPACE_EVIDENCE_DIR"); evidence != "" {
		root = filepath.Join(evidence, "workspace-postgres-proof")
		if e = os.Mkdir(root, 0700); e != nil {
			t.Fatal(e)
		}
	}
	s := &Service{Store: first, Root: filepath.Join(root, "source")}
	other := &Service{Store: second, Root: s.Root}
	owner := Actor{ID: "checkpoint-postgres-owner", Kind: "human", Admin: true}
	project := must(t, s, owner, "projects.create", M{"title": "Workspace recovery concurrency"})
	f, repo, meta := checkpointFixtureOn(t, s, owner, project, "code")
	body := checkpointTar(t, map[string]string{"asset.bin": string([]byte{0, 255, 1}), "tool.sh": "#!/bin/sh\ntrue\n"}, "tool.sh")
	start := make(chan struct{})
	results := make(chan Resource, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, svc := range []*Service{s, other} {
		wg.Add(1)
		go func(svc *Service) {
			defer wg.Done()
			<-start
			checkpoint, e := svc.UploadWorkspaceCheckpoint(context.Background(), f.worker, f.run.ID, meta, bytes.NewReader(body))
			if e != nil {
				errs <- e
			} else {
				results <- checkpoint
			}
		}(svc)
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	var checkpoint Resource
	count := 0
	for got := range results {
		count++
		if checkpoint.ID != "" && got.ID != checkpoint.ID {
			t.Fatal("duplicate checkpoint")
		}
		checkpoint = got
	}
	if count != 2 {
		t.Fatal("missing exact concurrent acknowledgements", count)
	}
	request, in := waitRequest(t, f)
	in["checkpointId"], in["source"] = checkpoint.ID, M{"repositoryId": repo.ID, "commit": meta["commit"]}
	for _, svc := range []*Service{s, other} {
		wg.Add(1)
		go func(svc *Service) {
			defer wg.Done()
			_, e := svc.Command(context.Background(), f.worker, "tasks.wait", in)
			if e != nil {
				t.Error(e)
			}
		}(svc)
	}
	wg.Wait()
	waits, _ := first.List(project.ID, "message_wait", f.g.ID)
	if len(waits) != 1 || waits[0].Status != "waiting" {
		t.Fatal("wait duplicated or lost", waits)
	}
	if e = s.VerifyIntegrity(context.Background()); e != nil {
		t.Fatal(e)
	}
	archive := filepath.Join(root, "backup.tar.gz")
	if e = s.Backup(context.Background(), archive); e != nil {
		t.Fatal(e)
	}
	dest := filepath.Join(root, "restored")
	if e = Restore(context.Background(), archive, dest, ""); e != nil {
		t.Fatal(e)
	}
	pristine, e := os.ReadFile(filepath.Join(dest, "platform.db"))
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(root, "pristine.db"), pristine, 0600); e != nil {
		t.Fatal(e)
	}
	store, e := Open(filepath.Join(dest, "platform.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	restored := &Service{Store: store, Root: dest}
	if e = restored.VerifyIntegrity(context.Background()); e != nil {
		t.Fatal(e)
	}
	must(t, restored, owner, "workers.disable", M{"id": f.worker.WorkerID})
	f.s = restored
	waitReply(t, f, request, "reply-after-restore")
	w := must(t, restored, owner, "workers.register", M{"projectId": project.ID, "title": "Restored code Worker", "roles": []any{"developer"}, "runtime": "linux", "capabilities": []any{"code"}})
	replacement := Actor{ID: "worker:" + w.ID, Kind: "worker", WorkerID: w.ID, ProjectID: project.ID, Roles: []string{"developer"}}
	next := must(t, restored, replacement, "tasks.claim", M{})
	file, saved, e := restored.WorkspaceCheckpoint(context.Background(), replacement, next.ID, S(next.Data, "leaseToken"))
	if e != nil {
		t.Fatal(e)
	}
	actual, e := io.ReadAll(file)
	file.Close()
	if e != nil || !bytes.Equal(actual, body) || saved.ID != checkpoint.ID {
		t.Fatal("restored exact bytes changed", e)
	}
	inbox := must(t, restored, replacement, "messages.receive", M{"runId": next.ID, "leaseToken": next.Data["leaseToken"]})
	if len(Items(inbox.Data["items"])) != 1 {
		t.Fatal("replacement lost exact reply")
	}
	for _, kind := range []string{"artifact", "evidence", "source_commit", "change"} {
		all, _ := store.List(project.ID, kind, f.g.ID)
		if len(all) > 0 {
			t.Fatal("backup resume adopted checkpoint", kind)
		}
	}
	facts := M{"passed": true, "syntheticOnly": true, "projectId": project.ID, "goalId": f.g.ID, "repositoryId": repo.ID, "baseCommit": meta["commit"], "checkpointId": checkpoint.ID, "checkpointDigest": checkpoint.Data["digest"], "checkpointBinding": checkpoint.Data["binding"], "sourceBindingDigest": Obj(checkpoint.Data["sourceBinding"])["digest"], "fileCount": 2, "waitId": waits[0].ID, "requestId": request.ID, "originalRunId": f.run.ID, "resumedRunId": next.ID, "concurrentAcknowledgements": count, "paidModelCalled": false}
	encoded, _ := json.MarshalIndent(facts, "", "  ")
	if e = os.WriteFile(filepath.Join(root, "facts.json"), append(encoded, '\n'), 0600); e != nil {
		t.Fatal(e)
	}
}

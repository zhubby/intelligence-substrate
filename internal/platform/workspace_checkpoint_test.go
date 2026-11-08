package platform

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func checkpointFixture(t *testing.T, kind string) (*messageFixture, Resource, M) {
	t.Helper()
	s, a, project := fixture(t)
	return checkpointFixtureOn(t, s, a, project, kind)
}

func TestWorkspaceCheckpointRechecksAuthorityAfterStreaming(t *testing.T) {
	for _, mode := range []string{"requirement", "worker", "target", "budget"} {
		t.Run(mode, func(t *testing.T) {
			f, repo, meta := checkpointFixture(t, "code")
			var once sync.Once
			body := &afterRead{Reader: bytes.NewReader(checkpointTar(t, map[string]string{"saved.txt": "actual bytes"})), after: func() {
				once.Do(func() {
					switch mode {
					case "requirement":
						must(t, f.s, f.a, "goals.revise", M{"id": f.g.ID, "outcome": "Changed scope", "reason": "During upload"})
					case "worker":
						must(t, f.s, f.a, "workers.disable", M{"id": f.worker.WorkerID})
					case "target":
						must(t, f.s, f.a, "repositories.commit", M{"id": repo.ID, "branch": "checkpoint", "message": "Another writer", "baseCommit": meta["commit"], "files": M{"other": "data"}})
					case "budget":
						run, _ := f.s.Store.Get(f.run.ID)
						run.Data["spentCents"] = 100
						if e := f.s.Store.Update(context.Background(), func(tx *Tx) error { return tx.Put(run) }); e != nil {
							t.Fatal(e)
						}
					}
				})
			}}
			if _, e := f.s.UploadWorkspaceCheckpoint(context.Background(), f.worker, f.run.ID, meta, body); e == nil {
				t.Fatal("changed streaming authority accepted", mode)
			}
			all, _ := f.s.Store.List(f.p.ID, "workspace_checkpoint", f.g.ID)
			if len(all) != 0 {
				t.Fatal("rejected stream saved checkpoint")
			}
		})
	}
}

func TestWorkspaceCheckpointWaitRejectsMissingForeignOrStaleState(t *testing.T) {
	for _, mode := range []string{"missing", "foreign", "corrupt", "target", "recipe", "submitted"} {
		t.Run(mode, func(t *testing.T) {
			f, repo, meta := checkpointFixture(t, "code")
			checkpoint, e := f.s.UploadWorkspaceCheckpoint(context.Background(), f.worker, f.run.ID, meta, bytes.NewReader(checkpointTar(t, map[string]string{"saved": "source"})))
			if e != nil {
				t.Fatal(e)
			}
			_, in := waitRequest(t, f)
			in["source"], in["checkpointId"] = M{"repositoryId": repo.ID, "commit": meta["commit"]}, checkpoint.ID
			switch mode {
			case "missing":
				delete(in, "checkpointId")
			case "foreign":
				in["checkpointId"] = "another-checkpoint"
			case "corrupt":
				if e = os.WriteFile(f.s.ArtifactPath(S(checkpoint.Data, "digest")), []byte("changed"), 0600); e != nil {
					t.Fatal(e)
				}
			case "target":
				must(t, f.s, f.a, "repositories.commit", M{"id": repo.ID, "branch": "checkpoint", "message": "Another writer", "baseCommit": meta["commit"], "files": M{"other": "source"}})
			case "recipe":
				task, _ := f.s.Store.Get(S(f.run.Data, "taskId"))
				task.Data["instructions"] = "changed"
				if e = f.s.Store.Update(context.Background(), func(tx *Tx) error { return tx.Put(task) }); e != nil {
					t.Fatal(e)
				}
			case "submitted":
				op := resource("source_commit", "Prepared after checkpoint", f.p.ID, f.g.ID, M{"runId": f.run.ID})
				op.Status = "prepared"
				if e = f.s.Store.Update(context.Background(), func(tx *Tx) error { return tx.Put(op) }); e != nil {
					t.Fatal(e)
				}
			}
			if _, e = f.s.Command(context.Background(), f.worker, "tasks.wait", in); e == nil {
				t.Fatal("unsafe wait accepted", mode)
			}
			run, _ := f.s.Store.Get(f.run.ID)
			if run.Status == "suspended" {
				t.Fatal("invalid wait released authority")
			}
		})
	}
}

func TestWorkspaceCheckpointCannotWaitAfterChangingSavedIntegrationDecisions(t *testing.T) {
	f, _, _ := integrationFixture(t, true)
	run, _ := prepareIntegration(t, f)
	meta := M{"leaseToken": run.Data["leaseToken"], "repositoryId": f.repo.ID, "commit": Obj(run.Data["task"])["commit"]}
	checkpoint, e := f.s.UploadWorkspaceCheckpoint(context.Background(), f.worker, run.ID, meta, bytes.NewReader(checkpointTar(t, map[string]string{"shared.txt": "in progress"})))
	if e != nil {
		t.Fatal(e)
	}
	mf := &messageFixture{s: f.s, a: f.a, p: f.p, g: f.g, worker: f.worker, run: run}
	_, in := waitRequest(t, mf)
	in["source"], in["checkpointId"] = M{"repositoryId": f.repo.ID, "commit": meta["commit"]}, checkpoint.ID
	must(t, f.s, f.worker, "sources.resolve", M{"runId": run.ID, "leaseToken": run.Data["leaseToken"], "resolutions": []any{M{"path": "shared.txt", "deleted": true}}})
	if _, e = f.s.Command(context.Background(), f.worker, "tasks.wait", in); e == nil {
		t.Fatal("wait inherited decisions different from its saved checkpoint")
	}
}

func TestWorkspaceCheckpointThirdWaitCanResumeButCannotExpandEnvelope(t *testing.T) {
	f, repo, meta := checkpointFixture(t, "code")
	for i := 0; i < 3; i++ {
		meta["leaseToken"] = S(f.run.Data, "leaseToken")
		checkpoint, e := f.s.UploadWorkspaceCheckpoint(context.Background(), f.worker, f.run.ID, meta, bytes.NewReader(checkpointTar(t, map[string]string{"saved.txt": "same exact code"})))
		if e != nil {
			t.Fatal(e)
		}
		request, in := waitRequest(t, f)
		in["steps"], in["checkpointId"], in["source"] = (i+1)*3, checkpoint.ID, M{"repositoryId": repo.ID, "commit": meta["commit"]}
		must(t, f.s, f.worker, "tasks.wait", in)
		waitReply(t, f, request, "reply:"+f.run.ID)
		f.run = must(t, f.s, f.worker, "tasks.claim", M{})
		if f.run.Kind != "run" {
			t.Fatal("valid third wait could not resume", i, f.run)
		}
		file, _, e := f.s.WorkspaceCheckpoint(context.Background(), f.worker, f.run.ID, S(f.run.Data, "leaseToken"))
		if e != nil {
			t.Fatal("valid saved tree could not restore", i, e)
		}
		file.Close()
	}
	meta["leaseToken"] = S(f.run.Data, "leaseToken")
	reader := &unreadSource{}
	if _, e := f.s.UploadWorkspaceCheckpoint(context.Background(), f.worker, f.run.ID, meta, reader); e == nil || reader.reads != 0 {
		t.Fatal("fourth wait expanded task envelope", e, reader.reads)
	}
	if N(Obj(f.run.Data["task"]), "attempts") != 1 || N(Obj(f.run.Data["task"]), "messageStepsUsed") != 9 {
		t.Fatal("suspension reset its bounded attempt/steps", f.run)
	}
}

func TestWorkspaceCheckpointRegistrationRollsBackAuthorityWithAudit(t *testing.T) {
	f, _, meta := checkpointFixture(t, "code")
	if _, e := f.s.Store.DB.Exec(`CREATE TRIGGER reject_workspace_audit BEFORE INSERT ON audit WHEN json_extract(NEW.body,'$.action')='workspace.checkpoint' BEGIN SELECT RAISE(ABORT,'test checkpoint audit failure'); END`); e != nil {
		t.Fatal(e)
	}
	body := checkpointTar(t, map[string]string{"saved": "code"})
	if _, e := f.s.UploadWorkspaceCheckpoint(context.Background(), f.worker, f.run.ID, meta, bytes.NewReader(body)); e == nil || !strings.Contains(e.Error(), "test checkpoint audit failure") {
		t.Fatal("expected checkpoint audit failure", e)
	}
	all, _ := f.s.Store.List(f.p.ID, "workspace_checkpoint", f.g.ID)
	if len(all) != 0 {
		t.Fatal("resource survived failed registration")
	}
	var count int
	if e := f.s.Store.DB.QueryRow(`SELECT COUNT(*) FROM idempotency WHERE scope=$1`, "workspace-checkpoint:"+f.run.ID).Scan(&count); e != nil || count != 0 {
		t.Fatal("replay authority survived failed registration", count, e)
	}
	if _, e := f.s.Store.DB.Exec(`DROP TRIGGER reject_workspace_audit`); e != nil {
		t.Fatal(e)
	}
	if _, e := f.s.UploadWorkspaceCheckpoint(context.Background(), f.worker, f.run.ID, meta, bytes.NewReader(body)); e != nil {
		t.Fatal("exact retry after rollback failed", e)
	}
}

func checkpointFixtureOn(t *testing.T, s *Service, a Actor, project Resource, kind string) (*messageFixture, Resource, M) {
	t.Helper()
	f := &messageFixture{s: s, a: a, p: project}
	f.g = goal(t, s, a, project)
	must(t, s, a, "goals.approve", M{"id": f.g.ID})
	repo := must(t, s, a, "repositories.create", M{"projectId": project.ID, "title": "Checkpoint source"})
	seed := must(t, s, a, "repositories.commit", M{"id": repo.ID, "branch": "seed", "message": "Original", "files": M{"original.txt": "original", "before.html": "<p>before</p>"}})
	base := S(seed.Data, "lastCommit")
	f.plan = must(t, s, a, "plans.propose", M{"goalId": f.g.ID, "summary": "Preserve workspace while clarifying", "tasks": []any{M{"id": "work", "title": "Implement approved work", "kind": kind, "role": "developer", "runtime": "linux", "repositoryId": repo.ID, "commit": base, "branch": "checkpoint", "budgetCents": 100}}})
	must(t, s, a, "plans.approve", M{"id": f.plan.ID})
	w := must(t, s, a, "workers.register", M{"projectId": project.ID, "title": "Checkpoint Worker", "roles": []any{"developer"}, "runtime": "linux", "capabilities": []any{"code", "design"}})
	f.worker = Actor{ID: "worker:" + w.ID, Kind: "worker", WorkerID: w.ID, ProjectID: project.ID, Roles: []string{"developer"}}
	f.run = must(t, s, f.worker, "tasks.claim", M{})
	if f.run.Kind != "run" {
		t.Fatal("workspace task was not claimed", f.run)
	}
	return f, repo, M{"leaseToken": S(f.run.Data, "leaseToken"), "repositoryId": repo.ID, "commit": base}
}

func checkpointTar(t *testing.T, files map[string]string, executable ...string) []byte {
	t.Helper()
	var body bytes.Buffer
	tw := tar.NewWriter(&body)
	for name, content := range files {
		mode := int64(0644)
		if Has(executable, name) {
			mode = 0755
		}
		if e := tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(content)), Typeflag: tar.TypeReg}); e != nil {
			t.Fatal(e)
		}
		if _, e := tw.Write([]byte(content)); e != nil {
			t.Fatal(e)
		}
	}
	if e := tw.Close(); e != nil {
		t.Fatal(e)
	}
	return body.Bytes()
}

func TestWorkspaceCheckpointBindsWaitAndDownloadsOnlyForActiveContinuation(t *testing.T) {
	f, repo, meta := checkpointFixture(t, "code")
	body := checkpointTar(t, map[string]string{"binary.dat": string([]byte{0, 255, 1}), "tool.sh": "#!/bin/sh\ntrue\n"}, "tool.sh")
	checkpoint, e := f.s.UploadWorkspaceCheckpoint(context.Background(), f.worker, f.run.ID, meta, bytes.NewReader(body))
	if e != nil {
		t.Fatal(e)
	}
	if checkpoint.Kind != "workspace_checkpoint" || checkpoint.Status != "saved" || N(checkpoint.Data, "fileCount") != 2 {
		t.Fatal(checkpoint)
	}
	for i := 0; i < 2; i++ {
		replay, err := f.s.UploadWorkspaceCheckpoint(context.Background(), f.worker, f.run.ID, meta, bytes.NewReader(body))
		if err != nil || replay.ID != checkpoint.ID {
			t.Fatal("exact replay differs", replay, err)
		}
	}
	if _, e = f.s.UploadWorkspaceCheckpoint(context.Background(), f.worker, f.run.ID, meta, bytes.NewReader(checkpointTar(t, map[string]string{"changed": "bytes"}))); e == nil {
		t.Fatal("different snapshot replay accepted")
	}
	request, in := waitRequest(t, f)
	in["source"], in["checkpointId"] = M{"repositoryId": repo.ID, "commit": meta["commit"]}, checkpoint.ID
	wait := must(t, f.s, f.worker, "tasks.wait", in)
	if S(wait.Data, "checkpointId") != checkpoint.ID || S(wait.Data, "checkpointDigest") != S(checkpoint.Data, "digest") {
		t.Fatal("wait lost checkpoint", wait)
	}
	if _, _, e = f.s.WorkspaceCheckpoint(context.Background(), f.worker, f.run.ID, S(meta, "leaseToken")); e == nil {
		t.Fatal("suspended lease downloaded checkpoint")
	}
	waitReply(t, f, request, "restore")
	w := must(t, f.s, f.a, "workers.register", M{"projectId": f.p.ID, "title": "Replacement", "roles": []any{"developer"}, "runtime": "linux", "capabilities": []any{"code"}})
	actor := Actor{ID: "worker:" + w.ID, Kind: "worker", WorkerID: w.ID, ProjectID: f.p.ID, Roles: []string{"developer"}}
	next := must(t, f.s, actor, "tasks.claim", M{})
	if next.Kind != "run" {
		t.Fatal("continuation could not claim", next)
	}
	file, got, e := f.s.WorkspaceCheckpoint(context.Background(), actor, next.ID, S(next.Data, "leaseToken"))
	if e != nil {
		t.Fatal(e)
	}
	actual, e := io.ReadAll(file)
	file.Close()
	if e != nil || !bytes.Equal(actual, body) || got.ID != checkpoint.ID {
		t.Fatal("download bytes/binding changed", e)
	}
	if _, _, e = f.s.WorkspaceCheckpoint(context.Background(), f.worker, next.ID, S(next.Data, "leaseToken")); e == nil {
		t.Fatal("original Worker stole continuation")
	}
	if _, _, e = f.s.WorkspaceCheckpoint(context.Background(), f.a, next.ID, S(next.Data, "leaseToken")); e == nil {
		t.Fatal("human bypassed Worker-only download")
	}
	for _, kind := range []string{"artifact", "source_commit", "evidence", "change"} {
		all, _ := f.s.Store.List(f.p.ID, kind, f.g.ID)
		if len(all) > 0 {
			t.Fatal("checkpoint became output", kind)
		}
	}
	serialized, _ := json.Marshal(checkpoint)
	if strings.Contains(string(serialized), "leaseToken") || strings.Contains(string(serialized), S(meta, "leaseToken")) {
		t.Fatal("checkpoint leaked authority")
	}
	if e = os.WriteFile(f.s.ArtifactPath(S(checkpoint.Data, "digest")), []byte("corrupted"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, _, e = f.s.WorkspaceCheckpoint(context.Background(), actor, next.ID, S(next.Data, "leaseToken")); e == nil {
		t.Fatal("corrupt checkpoint downloaded")
	}
	if e = f.s.VerifyIntegrity(context.Background()); e == nil {
		t.Fatal("backup integrity ignored checkpoint corruption")
	}
}

func TestWorkspaceCheckpointRejectsAuthorityBeforeReadingBody(t *testing.T) {
	for _, mode := range []string{"human", "foreign-worker", "wrong-lease", "disabled", "role", "capability", "runtime", "deadline", "source", "recipe", "target", "submitted", "budget"} {
		t.Run(mode, func(t *testing.T) {
			f, repo, meta := checkpointFixture(t, "code")
			actor := f.worker
			switch mode {
			case "human":
				actor = f.a
			case "foreign-worker":
				actor = f.register(t, "Other worker")
			case "wrong-lease":
				meta["leaseToken"] = "wrong"
			case "disabled":
				must(t, f.s, f.a, "workers.disable", M{"id": f.worker.WorkerID})
			case "role":
				must(t, f.s, f.a, "workers.update", M{"id": f.worker.WorkerID, "projectId": f.p.ID, "title": "Changed role", "roles": []any{"quality"}, "runtime": "linux", "capabilities": []any{"code"}})
			case "capability":
				must(t, f.s, f.a, "workers.update", M{"id": f.worker.WorkerID, "projectId": f.p.ID, "title": "Changed capability", "roles": []any{"developer"}, "runtime": "linux", "capabilities": []any{"analysis"}})
			case "runtime":
				must(t, f.s, f.a, "workers.update", M{"id": f.worker.WorkerID, "projectId": f.p.ID, "title": "Changed runtime", "roles": []any{"developer"}, "runtime": "windows", "capabilities": []any{"code"}})
			case "deadline":
				f.run.Data["deadline"] = time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)
			case "source":
				meta["commit"] = strings.Repeat("a", 40)
			case "recipe":
				task, _ := f.s.Store.Get(S(f.run.Data, "taskId"))
				task.Data["instructions"] = "unapproved change"
				if e := f.s.Store.Update(context.Background(), func(tx *Tx) error { return tx.Put(task) }); e != nil {
					t.Fatal(e)
				}
			case "target":
				must(t, f.s, f.a, "repositories.commit", M{"id": repo.ID, "branch": "checkpoint", "message": "Other writer", "baseCommit": meta["commit"], "files": M{"other": "source"}})
			case "submitted":
				op := resource("source_commit", "Already prepared", f.p.ID, f.g.ID, M{"runId": f.run.ID})
				op.Status = "prepared"
				if e := f.s.Store.Update(context.Background(), func(tx *Tx) error { return tx.Put(op) }); e != nil {
					t.Fatal(e)
				}
			case "budget":
				f.run.Data["spentCents"] = 100
			}
			if e := f.s.Store.Update(context.Background(), func(tx *Tx) error { return tx.Put(f.run) }); e != nil {
				t.Fatal(e)
			}
			reader := &unreadSource{}
			if _, e := f.s.UploadWorkspaceCheckpoint(context.Background(), actor, f.run.ID, meta, reader); e == nil || reader.reads != 0 {
				t.Fatal("unauthorized body consumed", e, reader.reads)
			}
		})
	}
}

func TestWorkspaceCheckpointDesignCannotSmuggleSourceChanges(t *testing.T) {
	for _, mode := range []string{"valid", "code", "deletion", "executable", "empty-html", "oversized-html", "reserved", "traversal", "link", "truncated", "trailing"} {
		t.Run(mode, func(t *testing.T) {
			f, _, meta := checkpointFixture(t, "design")
			files := map[string]string{"original.txt": "original", "before.html": "<p>updated</p>", "new.html": "<p>draft</p>"}
			executable := []string{}
			switch mode {
			case "code":
				files["original.txt"] = "changed"
			case "deletion":
				delete(files, "original.txt")
			case "executable":
				executable = []string{"new.html"}
			case "empty-html":
				files["new.html"] = ""
			case "oversized-html":
				files["new.html"] = strings.Repeat("x", 2<<20+1)
			case "reserved":
				files[".intelligence-artifact"] = "hidden"
			case "traversal":
				files["../escape"] = "hidden"
			}
			body := checkpointTar(t, files, executable...)
			if mode == "link" {
				var b bytes.Buffer
				tw := tar.NewWriter(&b)
				_ = tw.WriteHeader(&tar.Header{Name: "link", Linkname: "/tmp/escape", Typeflag: tar.TypeSymlink})
				_ = tw.Close()
				body = b.Bytes()
			}
			if mode == "truncated" {
				body = body[:600]
			}
			if mode == "trailing" {
				body = append(body, 1)
			}
			_, err := f.s.UploadWorkspaceCheckpoint(context.Background(), f.worker, f.run.ID, meta, bytes.NewReader(body))
			if mode == "valid" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil {
				t.Fatal("unsafe design checkpoint accepted", mode)
			}
			all, _ := f.s.Store.List(f.p.ID, "workspace_checkpoint", f.g.ID)
			if len(all) != 0 {
				t.Fatal("invalid checkpoint persisted")
			}
		})
	}
}

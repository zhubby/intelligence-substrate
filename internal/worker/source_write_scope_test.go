package worker

import (
	"archive/tar"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	p "github.com/intelligence-substrate/platform/internal/platform"
)

func scopedWorkspaceHarness(t *testing.T) (*inboxHarness, p.Resource, string) {
	t.Helper()
	f, repo, base := workspaceHarness(t, "code")
	plan := f.command(t, f.owner, "plans.propose", p.M{"goalId": f.goal.ID, "summary": "Only readme may change", "tasks": []any{p.M{"id": "left", "title": "Edit approved file", "kind": "code", "role": "developer", "runtime": runtime.GOOS, "repositoryId": repo.ID, "commit": base, "branch": "scope-work", "writePaths": []any{"readme.txt"}, "budgetCents": 100}, p.M{"id": "build", "title": "Build only adopted code", "kind": "build", "role": "developer", "runtime": runtime.GOOS, "repositoryId": repo.ID, "dependsOn": []any{"left"}, "command": []any{"/bin/sh", "-c", "cat readme.txt > package.txt"}, "outputPath": "package.txt", "target": "web", "budgetCents": 100}}})
	f.command(t, f.owner, "plans.approve", p.M{"id": plan.ID})
	return f, repo, base
}

// Without the early worker check, real filesystem writes/deletes happen before
// the final controller rejection. The command inspects those actual effects.
func TestWriteScopeWorkerRejectsDirectWritesBeforeFilesystemChanges(t *testing.T) {
	f, repo, _ := scopedWorkspaceHarness(t)
	runner, _ := workspaceRunner(t, f)
	calls := 0
	f.h.ModelCall = func(_ context.Context, _ p.Actor, in p.M) (p.M, error) {
		calls++
		messages, _ := json.Marshal(in["messages"])
		switch calls {
		case 1:
			return actionResponse(p.M{"action": "write", "path": "outside.txt", "content": "should never exist"}), nil
		case 2:
			if !strings.Contains(string(messages), "writePaths") {
				return nil, fmt.Errorf("write did not report the approved scope")
			}
			return actionResponse(p.M{"action": "delete", "path": "remove.txt"}), nil
		case 3:
			return actionResponse(p.M{"action": "run", "command": []string{"/bin/sh", "-c", "test ! -e outside.txt && test -f remove.txt && printf untouched"}}), nil
		case 4:
			if !strings.Contains(string(messages), "exitCode=0\\nuntouched") {
				return nil, fmt.Errorf("scope denial did not preserve the actual filesystem")
			}
			return actionResponse(p.M{"action": "write", "path": "readme.txt", "content": "permitted source\n"}), nil
		case 5:
			return actionResponse(p.M{"action": "finish", "summary": "Only approved file changed; QA remains independent."}), nil
		}
		return nil, fmt.Errorf("unexpected fixture call")
	}
	if worked, err := runner.RunOnce(context.Background()); !worked || err != nil {
		t.Fatal(worked, err)
	}
	task := workspaceTask(t, f, "left")
	if task.Status != "succeeded" || calls != 5 {
		t.Fatal("permitted correction did not finish", task.Status, task.Data["reason"], calls)
	}
	ops, _ := f.svc.Store.List(f.project.ID, "source_commit", f.goal.ID)
	if len(ops) != 1 || ops[0].Status != "committed" {
		t.Fatal("expected exactly one corrected candidate", ops)
	}
	commit := p.S(ops[0].Data, "commit")
	if body, err := f.svc.Git(repo, "show", commit+":remove.txt"); err != nil || body != "remove me" {
		t.Fatal("protected deletion escaped", body, err)
	}
	if body, err := f.svc.Git(repo, "show", commit+":readme.txt"); err != nil || body != "permitted source" {
		t.Fatal("approved edit lost", body, err)
	}
}

// A shell command bypasses direct write actions; only the authoritative final
// Git tree gate can stop it from becoming adopted source for downstream builds.
func TestWriteScopeWorkerRunBypassRetainsPrivateFailureWithoutCandidate(t *testing.T) {
	f, repo, _ := scopedWorkspaceHarness(t)
	runner, _ := workspaceRunner(t, f)
	calls := 0
	f.h.ModelCall = func(_ context.Context, _ p.Actor, in p.M) (p.M, error) {
		calls++
		if calls == 1 {
			return actionResponse(p.M{"action": "run", "command": []string{"/bin/sh", "-c", "printf unapproved > remove.txt; printf approved > readme.txt"}}), nil
		}
		if calls == 2 {
			return actionResponse(p.M{"action": "finish", "summary": "Cannot authorize an out-of-scope command result."}), nil
		}
		messages, _ := json.Marshal(in["messages"])
		if !strings.Contains(string(messages), "source_write_scope_rejected") {
			return nil, fmt.Errorf("missing explicit pre-candidate scope rejection")
		}
		return actionResponse(p.M{"action": "blocked", "reason": "writePaths rejects these uncorrected command changes"}), nil
	}
	if worked, err := runner.RunOnce(context.Background()); !worked || err != nil {
		t.Fatal(worked, err)
	}
	task := workspaceTask(t, f, "left")
	runs, _ := f.svc.Store.List(f.project.ID, "run", f.goal.ID)
	if task.Status != "blocked" || len(runs) != 1 || calls != 3 || !strings.Contains(p.S(p.Obj(runs[0].Data["result"]), "reason"), "writePaths") {
		t.Fatal("command bypass became successful source", task.Status, task.Data["reason"])
	}
	ops, _ := f.svc.Store.List(f.project.ID, "source_commit", f.goal.ID)
	if len(ops) != 0 {
		t.Fatal("out-of-scope command created a candidate", ops)
	}
	if head, _ := f.svc.Git(repo, "show-ref", "--verify", "refs/heads/scope-work"); head != "" {
		t.Fatal("command bypass moved target branch", head)
	}
	if len(runs) != 1 {
		t.Fatal("unexpected execution count", len(runs))
	}
	saved := p.Obj(p.Obj(runs[0].Data["result"])["stoppedSource"])
	if p.S(saved, "status") != "local_only" || !p.B(saved, "incomplete") || p.S(saved, "sourceOperationId") != "" {
		t.Fatal("failure source was discarded or presented as adopted", saved)
	}
	file, err := os.Open(filepath.Join(runner.Config.StateDir, p.S(saved, "archive")))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	archive, found := tar.NewReader(file), false
	for {
		h, err := archive.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if h.Name == "remove.txt" {
			body, err := io.ReadAll(archive)
			if err != nil || string(body) != "unapproved" {
				t.Fatal("retained failure bytes changed", err)
			}
			found = true
		}
	}
	if !found || workspaceTask(t, f, "build").Status != "pending" {
		t.Fatal("failure retention or downstream blocking lost")
	}
}

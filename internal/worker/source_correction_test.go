package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	p "github.com/intelligence-substrate/platform/internal/platform"
	"github.com/intelligence-substrate/platform/internal/source"
)

// Catches ending the task on a known unadmitted snapshot, reusing a stale
// archive, or restoring mutable branch bytes instead of the approved base.
func TestSourceCorrectionRejectedFinishRestoresBoundBaseAndAdmitsOnlyScopedChange(t *testing.T) {
	f, repo, base := scopedWorkspaceHarness(t)
	runner, _ := workspaceRunner(t, f)
	calls := 0
	f.h.ModelCall = func(_ context.Context, _ p.Actor, in p.M) (p.M, error) {
		calls++
		switch calls {
		case 1:
			return actionResponse(p.M{"action": "run", "command": []string{"/bin/sh", "-c", "printf unapproved > remove.txt; printf accidental > outside.txt; printf approved > readme.txt"}}), nil
		case 2:
			return actionResponse(p.M{"action": "finish", "summary": "Check the complete result before adoption"}), nil
		case 3:
			body, _ := json.Marshal(in["messages"])
			if !strings.Contains(string(body), "source_write_scope_rejected") || !strings.Contains(string(body), "remove.txt") {
				return nil, fmt.Errorf("known scope rejection was not delivered as a correction observation")
			}
			ops, _ := f.svc.Store.List(f.project.ID, "source_commit", f.goal.ID)
			if len(ops) != 0 {
				return nil, fmt.Errorf("rejected bytes already became a candidate")
			}
			f.command(t, f.owner, "repositories.commit", p.M{"id": repo.ID, "branch": "seed", "expectedHead": base, "message": "Later unrelated seed branch", "files": p.M{"remove.txt": "mutable branch must not be restored"}})
			return actionResponse(p.M{"action": "restore_base", "path": "remove.txt"}), nil
		case 4:
			return actionResponse(p.M{"action": "restore_base", "path": "outside.txt"}), nil
		case 5:
			return actionResponse(p.M{"action": "finish", "summary": "Only approved readme change remains; independent QA still required"}), nil
		}
		return nil, fmt.Errorf("unexpected extra correction inference")
	}
	if worked, err := runner.RunOnce(context.Background()); !worked || err != nil {
		t.Fatal(worked, err)
	}
	task := workspaceTask(t, f, "left")
	if task.Status != "succeeded" || calls != 5 {
		t.Fatal("known rejection could not be corrected in the same task", task.Status, calls)
	}
	ops, _ := f.svc.Store.List(f.project.ID, "source_commit", f.goal.ID)
	if len(ops) != 1 || ops[0].Status != "committed" {
		t.Fatal("correction did not create exactly one adopted candidate", ops)
	}
	commit := p.S(ops[0].Data, "commit")
	if body, err := f.svc.Git(repo, "show", commit+":remove.txt"); err != nil || body != "remove me" {
		t.Fatal("restore changed protected bytes or used mutable branch", body, err)
	}
	if body, err := f.svc.Git(repo, "show", commit+":readme.txt"); err != nil || body != "approved" {
		t.Fatal("correction lost permitted work", body, err)
	}
	changed, err := f.svc.Git(repo, "diff-tree", "--no-commit-id", "--name-only", "-r", base, commit, "--")
	if err != nil || changed != "readme.txt" {
		t.Fatal("correction admitted an unapproved path", changed, err)
	}
	steps, _ := f.svc.Store.List(f.project.ID, "agent_step", f.goal.ID)
	restored := 0
	for _, step := range steps {
		if p.S(p.Obj(step.Data["action"]), "action") == "restore_base" && step.Status == "completed" {
			restored++
		}
	}
	if restored != 2 || p.Hash(task.Data["writePaths"]) != p.Hash([]any{"readme.txt"}) {
		t.Fatal("correction lost durable restoration or widened scope", restored, task.Data["writePaths"])
	}
}

// A normal branch conflict is not proof that the previous archive was rejected
// before any candidate; it must not become an editable correction loop.
func TestSourceCorrectionStopsOnRealBranchConflict(t *testing.T) {
	f, repo, _ := scopedWorkspaceHarness(t)
	runner, _ := workspaceRunner(t, f)
	calls := 0
	f.h.ModelCall = func(_ context.Context, _ p.Actor, _ p.M) (p.M, error) {
		calls++
		if calls == 1 {
			return actionResponse(p.M{"action": "write", "path": "readme.txt", "content": "permitted"}), nil
		}
		if calls == 2 {
			f.command(t, f.owner, "repositories.commit", p.M{"id": repo.ID, "branch": "scope-work", "message": "Concurrent target change", "files": p.M{"remove.txt": "external"}})
			return actionResponse(p.M{"action": "finish", "summary": "Must stop on changed branch"}), nil
		}
		return nil, fmt.Errorf("ordinary conflict incorrectly resumed editing")
	}
	if worked, err := runner.RunOnce(context.Background()); !worked || err != nil {
		t.Fatal(worked, err)
	}
	if task := workspaceTask(t, f, "left"); task.Status != "failed" || calls != 2 {
		t.Fatal("ordinary conflict did not stop", task.Status, calls)
	}
	ops, _ := f.svc.Store.List(f.project.ID, "source_commit", f.goal.ID)
	if len(ops) != 0 {
		t.Fatal("conflicting source was adopted", ops)
	}
}

func TestSourceCorrectionRepeatedRejectedFinishesUseOriginalStepLimit(t *testing.T) {
	f, _, _ := scopedWorkspaceHarness(t)
	runner, _ := workspaceRunner(t, f)
	calls := 0
	f.h.ModelCall = func(_ context.Context, _ p.Actor, _ p.M) (p.M, error) {
		calls++
		if calls == 1 {
			return actionResponse(p.M{"action": "run", "command": []string{"/bin/sh", "-c", "printf unapproved > remove.txt"}}), nil
		}
		return actionResponse(p.M{"action": "finish", "summary": "Uncorrected result must stay rejected"}), nil
	}
	if worked, err := runner.RunOnce(context.Background()); !worked || err != nil {
		t.Fatal(worked, err)
	}
	if task := workspaceTask(t, f, "left"); task.Status != "blocked" || calls != 24 {
		t.Fatal("scope correction reset or skipped the original step envelope", task.Status, calls)
	}
	ops, _ := f.svc.Store.List(f.project.ID, "source_commit", f.goal.ID)
	if len(ops) != 0 || workspaceTask(t, f, "build").Status != "pending" {
		t.Fatal("repeated rejection produced source or advanced the build", ops)
	}
}

func TestSourceCorrectionRestoresBinaryExecutableAndDeletedBaseFiles(t *testing.T) {
	f, repo, _ := scopedWorkspaceHarness(t)
	root := t.TempDir()
	entries := []source.Entry{}
	for name, body := range map[string][]byte{"asset.bin": {0, 255, 2}, "tool.sh": []byte("#!/bin/sh\nexit 7\n"), "readme.txt": []byte("original")} {
		if err := os.WriteFile(filepath.Join(root, name), body, 0600); err != nil {
			t.Fatal(err)
		}
		entries = append(entries, source.Entry{Name: name, Size: int64(len(body)), Executable: name == "tool.sh"})
	}
	var archive bytes.Buffer
	if err := source.Write(&archive, root, entries); err != nil {
		t.Fatal(err)
	}
	seed, err := f.svc.CommitSnapshot(context.Background(), f.owner, repo.ID, p.M{"key": "binary-base", "branch": "binary-seed", "baseCommit": repo.Data["head"], "message": "Immutable binary and mode base"}, &archive)
	if err != nil {
		t.Fatal(err)
	}
	base := p.S(seed.Data, "commit")
	plan := f.command(t, f.owner, "plans.propose", p.M{"goalId": f.goal.ID, "summary": "Restore exact binary base without granting changes", "tasks": []any{p.M{"id": "left", "title": "Only readme may change", "kind": "code", "role": "developer", "runtime": runtime.GOOS, "repositoryId": repo.ID, "commit": base, "branch": "binary-work", "writePaths": []any{"readme.txt"}, "budgetCents": 100}}})
	f.command(t, f.owner, "plans.approve", p.M{"id": plan.ID})
	runner, _ := workspaceRunner(t, f)
	calls := 0
	f.h.ModelCall = func(_ context.Context, _ p.Actor, _ p.M) (p.M, error) {
		calls++
		actions := []p.M{
			{"action": "run", "command": []string{"/bin/sh", "-c", "printf wrong > asset.bin; rm tool.sh; printf approved > readme.txt"}},
			{"action": "finish", "summary": "Controller must reject accidental binary and deletion"},
			{"action": "restore_base", "path": "asset.bin"},
			{"action": "restore_base", "path": "tool.sh"},
			{"action": "finish", "summary": "Binary and executable restored, only readme changed"},
		}
		if calls > len(actions) {
			return nil, fmt.Errorf("exact base restoration did not permit completion")
		}
		return actionResponse(actions[calls-1]), nil
	}
	if worked, err := runner.RunOnce(context.Background()); !worked || err != nil {
		t.Fatal(worked, err)
	}
	if task := workspaceTask(t, f, "left"); task.Status != "succeeded" || calls != 5 {
		t.Fatal("base binary/mode restoration failed", task.Status, calls)
	}
	ops, _ := f.svc.Store.List(f.project.ID, "source_commit", f.goal.ID)
	if len(ops) != 1 {
		t.Fatal("unexpected corrected source count", len(ops))
	}
	commit := p.S(ops[0].Data, "commit")
	if body, err := f.svc.Git(repo, "show", commit+":asset.bin"); err != nil || body != string([]byte{0, 255, 2}) {
		t.Fatal("binary bytes changed", []byte(body), err)
	}
	if tree, err := f.svc.Git(repo, "ls-tree", commit, "tool.sh"); err != nil || !strings.HasPrefix(tree, "100755 blob") {
		t.Fatal("base executable mode was not restored", tree, err)
	}
}

func TestSourceCorrectionCanWaitAfterRestoreAndResumeOnReplacement(t *testing.T) {
	f, repo, base := scopedWorkspaceHarness(t)
	first, actor := workspaceRunner(t, f)
	phase, calls := 0, 0
	f.h.ModelCall = func(_ context.Context, a p.Actor, in p.M) (p.M, error) {
		calls++
		if phase == 0 {
			actions := []p.M{
				{"action": "run", "command": []string{"/bin/sh", "-c", "printf unapproved > remove.txt; printf approved > readme.txt"}},
				{"action": "finish", "summary": "Expect a known pre-candidate rejection"},
				{"action": "restore_base", "path": "remove.txt"},
				{"action": "message", "recipient": f.owner.ID, "type": "review", "payload": p.M{"question": "Review restored draft; permissions are unchanged"}},
			}
			if calls <= len(actions) {
				return actionResponse(actions[calls-1]), nil
			}
			if calls == 5 {
				return actionResponse(workspaceWaitAction(t, f, a)), nil
			}
		} else {
			if calls == 1 {
				messages := p.Items(in["messages"])
				var initial p.M
				_ = json.Unmarshal([]byte(p.S(p.Obj(messages[1]), "content")), &initial)
				if p.S(initial, "commit") != base || p.Hash(p.Obj(initial["task"])["writePaths"]) != p.Hash([]any{"readme.txt"}) || len(p.Obj(initial["untrustedWorkspaceContinuation"])) == 0 {
					return nil, fmt.Errorf("correction continuation changed its fixed source or permissions")
				}
				return actionResponse(p.M{"action": "run", "command": []string{"/bin/sh", "-c", "test \"$(cat remove.txt)\" = 'remove me' && test \"$(cat readme.txt)\" = approved && printf restored-draft"}}), nil
			}
			if calls == 2 {
				body, _ := json.Marshal(in["messages"])
				if !strings.Contains(string(body), "exitCode=0\\nrestored-draft") {
					return nil, fmt.Errorf("replacement did not load exact corrected checkpoint bytes")
				}
				return actionResponse(p.M{"action": "finish", "summary": "Complete original scoped work after exact reply"}), nil
			}
		}
		return nil, fmt.Errorf("unexpected correction continuation inference")
	}
	if worked, err := first.RunOnce(context.Background()); !worked || err != nil {
		t.Fatal(worked, err)
	}
	if task := workspaceTask(t, f, "left"); task.Status != "waiting_message" || calls != 5 {
		t.Fatal("known rejection prevented saving a corrected wait checkpoint", task.Status, calls)
	}
	ops, _ := f.svc.Store.List(f.project.ID, "source_commit", f.goal.ID)
	if len(ops) != 0 {
		t.Fatal("waiting corrected draft became adopted source", ops)
	}
	workspaceReply(t, f)
	f.command(t, f.owner, "workers.disable", p.M{"id": actor.WorkerID})
	phase, calls = 1, 0
	replacement, _ := workspaceRunner(t, f)
	if worked, err := replacement.RunOnce(context.Background()); !worked || err != nil {
		t.Fatal(worked, err)
	}
	if task := workspaceTask(t, f, "left"); task.Status != "succeeded" || calls != 2 {
		t.Fatal("replacement did not finish the same corrected task", task.Status, calls)
	}
	ops, _ = f.svc.Store.List(f.project.ID, "source_commit", f.goal.ID)
	if len(ops) != 1 || p.S(ops[0].Data, "baseCommit") != base {
		t.Fatal("replacement changed the approved base", ops)
	}
	if changed, err := f.svc.Git(repo, "diff-tree", "--no-commit-id", "--name-only", "-r", base, p.S(ops[0].Data, "commit"), "--"); err != nil || changed != "readme.txt" {
		t.Fatal("resumed source escaped its original scope", changed, err)
	}
	runs, _ := f.svc.Store.List(f.project.ID, "run", f.goal.ID)
	found := false
	for _, run := range runs {
		if run.Status == "succeeded" {
			found = p.N(p.Obj(run.Data["result"]), "steps") == 7
		}
	}
	if !found {
		t.Fatal("wait reset the original Agent step envelope", runs)
	}
}

func TestSourceCorrectionRestoreRejectsTraversalAndLinksWithoutChangingTargets(t *testing.T) {
	f, repo, base := scopedWorkspaceHarness(t)
	runner, actor := workspaceRunner(t, f)
	run := f.command(t, actor, "tasks.claim", p.M{})
	state, err := newSourceWorkspace(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer state.close()
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "external.txt")
	if err := os.WriteFile(outside, []byte("external target remains exact"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "remove.txt")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"remove.txt", "../external.txt", ".git/config", "bad/../remove.txt", ""} {
		if err := runner.restoreBase(context.Background(), run, state, root, repo.ID, base, p.M{"action": "restore_base", "path": name}); err == nil {
			t.Fatal("restore accepted a link or noncanonical path", name)
		}
	}
	if body, err := os.ReadFile(outside); err != nil || string(body) != "external target remains exact" {
		t.Fatal("restore changed an external link target", string(body), err)
	}
	if info, err := os.Lstat(filepath.Join(root, "remove.txt")); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("rejected restore replaced the link", err)
	}
}

func TestSourceCorrectionRestoreReplacesHardLinkWithoutWritingItsExternalTarget(t *testing.T) {
	f, repo, base := scopedWorkspaceHarness(t)
	runner, actor := workspaceRunner(t, f)
	run := f.command(t, actor, "tasks.claim", p.M{})
	state, err := newSourceWorkspace(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer state.close()
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "external.txt")
	if err := os.WriteFile(outside, []byte("external target remains exact"), 0600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(root, "remove.txt")
	if err := os.Link(outside, dest); err != nil {
		t.Fatal(err)
	}
	if err := runner.restoreBase(context.Background(), run, state, root, repo.ID, base, p.M{"action": "restore_base", "path": "remove.txt"}); err != nil {
		t.Fatal(err)
	}
	if body, err := os.ReadFile(dest); err != nil || string(body) != "remove me" {
		t.Fatal("base file was not restored", string(body), err)
	}
	if body, err := os.ReadFile(outside); err != nil || string(body) != "external target remains exact" {
		t.Fatal("restore wrote through a hard link", string(body), err)
	}
}

func TestSourceCorrectionRestoreRejectsCaseAliasOnCaseInsensitiveFilesystem(t *testing.T) {
	f, repo, base := scopedWorkspaceHarness(t)
	runner, actor := workspaceRunner(t, f)
	run := f.command(t, actor, "tasks.claim", p.M{})
	state, err := newSourceWorkspace(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer state.close()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "remove.txt"), []byte("current file"), 0600); err != nil {
		t.Fatal(err)
	}
	_, aliasErr := os.Lstat(filepath.Join(root, "REMOVE.TXT"))
	err = runner.restoreBase(context.Background(), run, state, root, repo.ID, base, p.M{"action": "restore_base", "path": "REMOVE.TXT"})
	if aliasErr == nil && err == nil {
		t.Fatal("case alias of existing baseline was treated as absence")
	}
	if body, err := os.ReadFile(filepath.Join(root, "remove.txt")); err != nil || string(body) != "current file" {
		t.Fatal("alias restoration deleted the actual baseline path", string(body), err)
	}
}

func TestSourceCorrectionRestoreCannotSelectAnotherVersion(t *testing.T) {
	f, _, _ := scopedWorkspaceHarness(t)
	runner, _ := workspaceRunner(t, f)
	f.h.ModelCall = func(_ context.Context, _ p.Actor, _ p.M) (p.M, error) {
		return actionResponse(p.M{"action": "restore_base", "path": "remove.txt", "commit": strings.Repeat("1", 40)}), nil
	}
	if worked, err := runner.RunOnce(context.Background()); !worked || err != nil {
		t.Fatal(worked, err)
	}
	if task := workspaceTask(t, f, "left"); task.Status != "blocked" {
		t.Fatal("restore accepted an Agent-selected version", task.Status)
	}
	ops, _ := f.svc.Store.List(f.project.ID, "source_commit", f.goal.ID)
	if len(ops) != 0 {
		t.Fatal("invalid restore version created a candidate", ops)
	}
}

func TestSourceCorrectionRestoreDoesNotRemoveDirectories(t *testing.T) {
	f, _, _ := scopedWorkspaceHarness(t)
	runner, _ := workspaceRunner(t, f)
	calls := 0
	f.h.ModelCall = func(_ context.Context, _ p.Actor, in p.M) (p.M, error) {
		calls++
		switch calls {
		case 1:
			return actionResponse(p.M{"action": "run", "command": []string{"/bin/sh", "-c", "mkdir preserve; printf protected > preserve/child.txt"}}), nil
		case 2:
			return actionResponse(p.M{"action": "restore_base", "path": "preserve"}), nil
		case 3:
			return actionResponse(p.M{"action": "run", "command": []string{"/bin/sh", "-c", "test -d preserve && test -f preserve/child.txt && printf directory-preserved"}}), nil
		case 4:
			body, _ := json.Marshal(in["messages"])
			if !strings.Contains(string(body), "exitCode=0\\ndirectory-preserved") {
				return nil, fmt.Errorf("restore recursively deleted an absent baseline directory")
			}
			return actionResponse(p.M{"action": "blocked", "reason": "Directory requires explicit scoped work"}), nil
		}
		return nil, fmt.Errorf("unexpected directory restoration call")
	}
	if worked, err := runner.RunOnce(context.Background()); !worked || err != nil {
		t.Fatal(worked, err)
	}
	if task := workspaceTask(t, f, "left"); task.Status != "blocked" || calls != 4 {
		t.Fatal("directory protection was not observed", task.Status, calls)
	}
}

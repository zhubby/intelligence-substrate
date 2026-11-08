package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	p "github.com/intelligence-substrate/platform/internal/platform"
)

func TestAgentProgressWorkerRestoresConfirmedFilesWithoutReplayingTool(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("actual POSIX process fixture")
	}
	f, repo, _ := workspaceHarness(t, "code")
	first, actor := workspaceRunner(t, f)
	marker := filepath.Join(t.TempDir(), "once.txt")
	phase, calls := 0, 0
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.h.ModelCall = func(_ context.Context, _ p.Actor, in p.M) (p.M, error) {
		calls++
		messages := p.Items(in["messages"])
		if phase == 0 {
			if calls == 1 {
				response := actionResponse(p.M{"action": "run", "command": []string{"/bin/sh", "-c", "printf once >> \"$1\"; printf '\\000\\377\\002' > binary.dat; printf '#!/bin/sh\\nexit 7\\n' > tool.sh; chmod +x tool.sh; rm remove.txt; printf actual-checkpoint", "fixture", marker}})
				p.Obj(p.Obj(p.Items(response["choices"])[0])["message"])["reasoning_content"] = "opaque-provider-private-context"
				return response, nil
			}
			cancel()
			return nil, context.Canceled
		}
		if calls == 1 {
			body, _ := json.Marshal(messages)
			if !strings.Contains(string(body), "untrustedConfirmedProgress") || !strings.Contains(string(body), "actual-checkpoint") || strings.Contains(string(body), "opaque-provider-private-context") || len(messages) != 2 {
				return nil, fmt.Errorf("replacement lost fresh public tool progress: %s", body)
			}
			return actionResponse(p.M{"action": "run", "command": []string{"/bin/sh", "-c", "test ! -e remove.txt && test -x tool.sh && test \"$(od -An -tx1 binary.dat | tr -d ' \\n')\" = 00ff02 && printf restored-exact"}}), nil
		}
		if calls == 2 {
			body, _ := json.Marshal(messages)
			if !strings.Contains(string(body), "exitCode=0\\nrestored-exact") {
				return nil, fmt.Errorf("restored bytes failed actual check: %s", body)
			}
			return actionResponse(p.M{"action": "finish", "summary": "Confirmed source restored; independent QA still required."}), nil
		}
		return nil, fmt.Errorf("unexpected replacement inference")
	}
	if worked, e := first.RunOnce(ctx); !worked || e != nil {
		t.Fatal(worked, e)
	}
	task := workspaceTask(t, f, "left")
	steps, _ := f.svc.Store.List(f.project.ID, "agent_step", f.goal.ID)
	if len(steps) != 1 || steps[0].Status != "completed" || p.S(task.Data, "agentProgressId") != steps[0].ID {
		t.Fatal("completed tool progress was not durable", steps, task.Status)
	}
	stored, _ := json.Marshal(steps)
	if strings.Contains(string(stored), "opaque-provider-private-context") {
		t.Fatal("persisted provider reasoning")
	}
	f.command(t, f.owner, "workers.disable", p.M{"id": actor.WorkerID})
	f.command(t, f.owner, "tasks.retry", p.M{"id": task.ID})
	phase, calls = 1, 0
	replacement, _ := workspaceRunner(t, f)
	if worked, e := replacement.RunOnce(context.Background()); !worked || e != nil {
		t.Fatal(worked, e)
	}
	task = workspaceTask(t, f, "left")
	if task.Status != "succeeded" {
		t.Fatal("replacement could not finish", task.Status, task.Data["reason"])
	}
	actual, e := os.ReadFile(marker)
	if e != nil || string(actual) != "once" {
		t.Fatal("completed tool was replayed", string(actual), e)
	}
	ops, _ := f.svc.Store.List(f.project.ID, "source_commit", f.goal.ID)
	if len(ops) != 1 || ops[0].Status != "committed" {
		t.Fatal("progress adopted intermediate source", ops)
	}
	files, e := f.svc.Git(repo, "ls-tree", "-r", p.S(ops[0].Data, "commit"))
	if e != nil || !strings.Contains(files, "100755 blob") || strings.Contains(files, "remove.txt") || !strings.Contains(files, "binary.dat") {
		t.Fatal("source submission lost restored files/modes/deletion", files, e)
	}
}

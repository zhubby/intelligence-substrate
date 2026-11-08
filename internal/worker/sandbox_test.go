package worker

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/intelligence-substrate/platform/internal/integrations"
	p "github.com/intelligence-substrate/platform/internal/platform"
)

func sandboxRunner(t *testing.T, mode string) (*inboxHarness, *Runner, p.Actor, string) {
	t.Helper()
	f := newInboxHarness(t, false)
	argv := []any{"/bin/sh", "-c", "printf 'actual-local-command\\n' > result.txt"}
	if mode == "nonzero" {
		argv = []any{"/bin/sh", "-c", "printf 'nonzero-local-command\\n' > result.txt; exit 7"}
	}
	plan := f.command(t, f.owner, "plans.propose", p.M{"goalId": f.goal.ID, "summary": "Actual local CLI processes under a durable sandbox", "tasks": []any{p.M{"id": "ax-command", "title": "Run isolated fixed command", "kind": "analysis", "role": "developer", "runtime": "linux", "budgetCents": 100, "command": argv}}})
	f.command(t, f.owner, "plans.approve", p.M{"id": plan.ID})
	w := f.command(t, f.owner, "workers.register", p.M{"projectId": f.project.ID, "title": "Linux AX controller", "roles": []any{"developer"}, "runtime": "linux", "capabilities": []any{"analysis"}})
	token, actor, e := f.h.Auth.IssueToken(context.Background(), f.owner, p.M{"projectId": f.project.ID, "workerId": w.ID})
	if e != nil {
		t.Fatal(e)
	}
	client, e := NewClient(f.server.URL, token, true)
	if e != nil {
		t.Fatal(e)
	}
	dir := t.TempDir()
	body, e := os.ReadFile("testdata/ax_cli_fixture.py")
	if e != nil {
		t.Fatal(e)
	}
	python, e := exec.LookPath("python3")
	if e != nil {
		t.Fatal(e)
	}
	binary := filepath.Join(dir, "ax")
	if e = os.WriteFile(binary, append([]byte("#!"+python+"\n"), body...), 0700); e != nil {
		t.Fatal(e)
	}
	config, _ := json.Marshal(p.M{"database": f.svc.Store.DSN, "mode": mode})
	if e = os.WriteFile(filepath.Join(dir, "config.json"), config, 0600); e != nil {
		t.Fatal(e)
	}
	runner := &Runner{Client: client, Config: Config{Driver: "ax", StateDir: t.TempDir(), AX: integrations.AX{Binary: binary, AllowLegacyCLI: true, Context: "enterprise", Namespace: "ax-system", Atespace: "test", Image: "runner@sha256:" + strings.Repeat("a", 64)}}}
	return f, runner, actor, dir
}

func TestWorkerAXPersistsBeforeApplyAndRetainsCleanupBarrier(t *testing.T) {
	for _, mode := range []string{"normal", "nonzero", "partial", "cleanup"} {
		t.Run(mode, func(t *testing.T) {
			f, runner, _, dir := sandboxRunner(t, mode)
			if worked, e := runner.RunOnce(context.Background()); e != nil || !worked {
				t.Fatal(worked, e)
			}
			items, _ := f.svc.Store.List(f.project.ID, "sandbox", f.goal.ID)
			if len(items) != 1 {
				t.Fatal("no durable ownership before external apply", len(items))
			}
			want := "stopped"
			if mode == "cleanup" {
				want = "unresolved"
			}
			if items[0].Status != want {
				t.Fatal("incorrect cleanup disposition", items[0].Status, want)
			}
			runs, _ := f.svc.Store.List(f.project.ID, "run", f.goal.ID)
			if len(runs) != 1 {
				t.Fatal(len(runs))
			}
			if mode == "normal" && runs[0].Status != "succeeded" || mode != "normal" && runs[0].Status == "succeeded" {
				t.Fatal("incorrect command result", mode, runs[0].Status, runs[0].Data)
			}
			calls, e := os.ReadFile(filepath.Join(dir, "calls.jsonl"))
			if e != nil {
				t.Fatal(e)
			}
			if !strings.Contains(string(calls), `"preparedBeforeApply": true`) || strings.Contains(string(calls), "unowned-apply") {
				t.Fatal("side effect preceded durable registration", string(calls))
			}
			if mode == "cleanup" {
				if _, e = f.svc.Command(context.Background(), f.owner, "tasks.retry", p.M{"id": p.S(runs[0].Data, "taskId")}); e == nil {
					t.Fatal("retry started while remote Task remains")
				}
			}
			if evidence := os.Getenv("IS_AX_SANDBOX_EVIDENCE_DIR"); evidence != "" {
				root := filepath.Join(evidence, "worker-"+mode)
				if e = os.Mkdir(root, 0700); e != nil {
					t.Fatal(e)
				}
				if e = f.svc.Backup(context.Background(), filepath.Join(root, "backup.tar.gz")); e != nil {
					t.Fatal(e)
				}
				if e = p.Restore(context.Background(), filepath.Join(root, "backup.tar.gz"), filepath.Join(root, "restored"), ""); e != nil {
					t.Fatal(e)
				}
				for _, name := range []string{"calls.jsonl", "remote.json"} {
					body, e := os.ReadFile(filepath.Join(dir, name))
					if e != nil {
						t.Fatal(e)
					}
					if e = os.WriteFile(filepath.Join(root, name), body, 0600); e != nil {
						t.Fatal(e)
					}
				}
				body, _ := json.MarshalIndent(p.M{"passed": true, "mode": mode, "sandboxId": items[0].ID, "runId": runs[0].ID, "taskId": p.S(runs[0].Data, "taskId"), "status": items[0].Status, "runStatus": runs[0].Status, "realAXOrSubstrate": false}, "", "  ")
				if e = os.WriteFile(filepath.Join(root, "proof.json"), append(body, '\n'), 0600); e != nil {
					t.Fatal(e)
				}
			}
		})
	}
}

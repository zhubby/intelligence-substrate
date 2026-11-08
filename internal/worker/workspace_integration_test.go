package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"testing"

	p "github.com/intelligence-substrate/platform/internal/platform"
)

func TestWorkspaceWaitIntegrationCarriesOnlyBoundPartialDecisions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX source producer fixture")
	}
	for _, mode := range []string{"complete", "edited-without-resolve"} {
		t.Run(mode, func(t *testing.T) {
			f, repo, base := workspaceHarness(t, "code")
			tasks := []any{}
			for _, side := range []string{"left-input", "right-input"} {
				tasks = append(tasks, p.M{"id": side, "title": side, "kind": "code", "role": "developer", "runtime": runtime.GOOS, "repositoryId": repo.ID, "commit": base, "branch": side, "command": []any{"/bin/sh", "-c", fmt.Sprintf("printf '%s' > original.txt; printf '<p>%s</p>' > before.html", side, side)}})
			}
			tasks = append(tasks, p.M{"id": "join", "title": "Integrate both versions", "kind": "code", "role": "developer", "runtime": runtime.GOOS, "repositoryId": repo.ID, "sourceMode": "integrate", "dependsOn": []any{"left-input", "right-input"}, "budgetCents": 100})
			plan := f.command(t, f.owner, "plans.propose", p.M{"goalId": f.goal.ID, "summary": "Resolve one conflict, ask, then resume remaining conflict", "tasks": tasks})
			f.command(t, f.owner, "plans.approve", p.M{"id": plan.ID})
			runner, actor := workspaceRunner(t, f)
			phase, calls := 0, 0
			var savedParents []string
			f.h.ModelCall = func(_ context.Context, a p.Actor, in p.M) (p.M, error) {
				calls++
				messages := p.Items(in["messages"])
				var initial p.M
				_ = json.Unmarshal([]byte(p.S(p.Obj(messages[1]), "content")), &initial)
				integration := p.Obj(initial["integration"])
				parents := p.Strings(integration["parents"])
				if len(parents) != 2 || len(p.Strings(integration["conflicts"])) != 2 {
					return nil, fmt.Errorf("missing actual integration context")
				}
				if phase == 0 {
					savedParents = parents
					switch calls {
					case 1:
						return actionResponse(p.M{"action": "take_input", "commit": parents[0], "path": "original.txt"}), nil
					case 2:
						return actionResponse(p.M{"action": "message", "recipient": f.owner.ID, "type": "review", "payload": p.M{"question": "Review partial integration before resolving the HTML?"}}), nil
					case 3:
						return actionResponse(workspaceWaitAction(t, f, a)), nil
					}
				} else {
					if len(messages) == 3 {
						runs, _ := f.svc.Store.List(f.project.ID, "run", f.goal.ID)
						found := false
						for _, run := range runs {
							if run.Status == "running" {
								decisions := p.Obj(run.Data["integrationResolutions"])
								if len(decisions) != 1 || len(p.Obj(decisions["original.txt"])) == 0 {
									return nil, fmt.Errorf("partial decisions vanished or new decisions invented")
								}
								found = true
							}
						}
						if !found {
							return nil, fmt.Errorf("no actual resumed integration")
						}
					}
					switch calls {
					case 1:
						return actionResponse(p.M{"action": "take_input", "commit": parents[1], "path": "before.html"}), nil
					case 2:
						if mode == "edited-without-resolve" {
							return actionResponse(p.M{"action": "write", "path": "original.txt", "content": "unresolved later bytes"}), nil
						}
						return actionResponse(p.M{"action": "finish", "summary": "Integrated both selected versions with explicit decisions"}), nil
					case 3:
						if mode == "edited-without-resolve" {
							return actionResponse(p.M{"action": "finish", "summary": "Try to finish with a stale decision"}), nil
						}
					}
				}
				return nil, fmt.Errorf("unexpected integration inference %d/%d", phase, calls)
			}
			for i := 0; i < 3; i++ {
				if worked, e := runner.RunOnce(context.Background()); e != nil || !worked {
					t.Fatal(worked, e)
				}
			}
			if workspaceTask(t, f, "join").Status != "waiting_message" {
				t.Fatal("partially resolved workspace did not suspend", workspaceTask(t, f, "join"))
			}
			checkpoints, _ := f.svc.Store.List(f.project.ID, "workspace_checkpoint", f.goal.ID)
			if len(checkpoints) != 1 || len(p.Obj(p.Obj(checkpoints[0].Data["integration"])["resolutions"])) != 1 {
				t.Fatal("checkpoint lost partial decisions", checkpoints)
			}
			f.command(t, f.owner, "workers.disable", p.M{"id": actor.WorkerID})
			workspaceReply(t, f)
			runner, _ = workspaceRunner(t, f)
			phase, calls = 1, 0
			if worked, e := runner.RunOnce(context.Background()); e != nil || !worked {
				t.Fatal(worked, e)
			}
			ops, _ := f.svc.Store.List(f.project.ID, "source_commit", f.goal.ID)
			if mode == "edited-without-resolve" {
				if workspaceTask(t, f, "join").Status == "succeeded" || len(ops) != 2 {
					t.Fatal("stale conflict decision adopted", workspaceTask(t, f, "join"), len(ops))
				}
				return
			}
			if workspaceTask(t, f, "join").Status != "succeeded" || len(ops) != 3 {
				t.Fatal("valid integration failed", workspaceTask(t, f, "join"), len(ops))
			}
			var output p.Resource
			for _, op := range ops {
				if p.S(op.Data, "integrationId") != "" {
					output = op
				}
			}
			actual, e := f.svc.Git(repo, "show", "-s", "--format=%P", p.S(output.Data, "commit"))
			if e != nil || len(strings.Fields(actual)) != 2 {
				t.Fatal("not the actual source merge", actual, e)
			}
			for _, parent := range savedParents {
				if !strings.Contains(actual, parent) {
					t.Fatal("lost integration parent", parent, actual)
				}
			}
		})
	}
}

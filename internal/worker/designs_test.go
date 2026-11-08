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
)

func TestDesignWritesRefuseExecutableAndReservedPaths(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"../outside.html", ".git/config.html", "application.go"} {
		if err := writeDesignPrototype(root, p.M{"path": name, "content": "<p>design</p>"}); err == nil {
			t.Fatal("design write escaped HTML-only workspace boundary", name)
		}
	}
	executable := filepath.Join(root, "existing.html")
	if err := os.WriteFile(executable, []byte("preserve executable source"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := writeDesignPrototype(root, p.M{"path": "existing.html", "content": "<p>replace</p>"}); err == nil {
		t.Fatal("design write retained executable source mode")
	}
	if body, _ := os.ReadFile(executable); string(body) != "preserve executable source" {
		t.Fatal("refused write changed existing source", string(body))
	}
}

func TestWorkerReceivesFrozenDesignAndCanReadOnlyItsPrototype(t *testing.T) {
	for _, damaged := range []bool{false, true} {
		t.Run(fmt.Sprint(damaged), func(t *testing.T) {
			f := newInboxHarness(t, false)
			repo := f.command(t, f.owner, "repositories.create", p.M{"projectId": f.project.ID, "title": "Prototype source"})
			prototype, err := f.svc.Upload(context.Background(), f.owner, p.M{"goalId": f.goal.ID, "repositoryId": repo.ID, "commit": repo.Data["head"], "target": "web", "name": "prototype.html", "type": "prototype"}, bytes.NewBufferString("<button>FROZEN-PROTOTYPE-UTC</button>"))
			if err != nil {
				t.Fatal(err)
			}
			design := f.command(t, f.owner, "designs.propose", p.M{"goalId": f.goal.ID, "expectedRequirementRevision": 1, "key": "reviewed", "title": "UTC workflow", "summary": "Show UTC dates", "decisions": "FROZEN-DESIGN: show UTC explicitly", "criteria": []any{"utc"}, "prototypeArtifactIds": []any{prototype.ID}, "reason": "Confirm timestamp presentation"})
			plan := f.command(t, f.owner, "plans.propose", p.M{"goalId": f.goal.ID, "summary": "Implement the reviewed design", "designId": design.ID, "expectedDesignBinding": design.Data["binding"], "tasks": []any{p.M{"id": "reviewed", "title": "Read the confirmed design", "role": "developer", "kind": "analysis", "runtime": runtime.GOOS}}})
			f.command(t, f.owner, "plans.approve", p.M{"id": plan.ID})
			if err := f.svc.Store.Update(context.Background(), func(tx *p.Tx) error {
				design.Data["decisions"] = "UNAPPROVED-LIVE-DESIGN"
				if err := tx.Put(design); err != nil {
					return err
				}
				if damaged {
					approvals, _ := tx.List(f.project.ID, "approval", f.goal.ID)
					for _, approval := range approvals {
						if p.S(approval.Data, "planId") == plan.ID {
							approval.Data["designDigest"] = "incorrect"
							return tx.Put(approval)
						}
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			runner, _ := f.runner(t, "developer")
			calls := 0
			f.h.ModelCall = func(_ context.Context, _ p.Actor, in p.M) (p.M, error) {
				calls++
				body, _ := json.Marshal(in["messages"])
				text := string(body)
				if !strings.Contains(text, "FROZEN-DESIGN") || strings.Contains(text, "UNAPPROVED-LIVE-DESIGN") {
					return nil, fmt.Errorf("worker used live or missing design")
				}
				switch calls {
				case 1:
					return actionResponse(p.M{"action": "read_prototype", "artifactId": "not-approved"}), nil
				case 2:
					if !strings.Contains(text, "not part of the approved design") {
						return nil, fmt.Errorf("unapproved prototype was not rejected")
					}
					return actionResponse(p.M{"action": "read_prototype", "artifactId": prototype.ID}), nil
				default:
					if !strings.Contains(text, "FROZEN-PROTOTYPE-UTC") {
						return nil, fmt.Errorf("frozen prototype bytes not available to implementation")
					}
					return actionResponse(p.M{"action": "finish", "summary": "Read the approved design and exact prototype"}), nil
				}
			}
			worked, err := runner.RunOnce(context.Background())
			if err != nil || !worked {
				t.Fatal(worked, err)
			}
			task := f.task(t, "reviewed")
			if damaged {
				if calls != 0 || task.Status != "blocked" {
					t.Fatal("invalid approval reached inference", calls, task)
				}
			} else if calls != 3 || task.Status != "succeeded" {
				t.Fatal(calls, task)
			}
		})
	}
}

func TestDesignWorkerPublishesRealPrototypeWithoutApprovingIt(t *testing.T) {
	f := newInboxHarness(t, false)
	repo := f.command(t, f.owner, "repositories.create", p.M{"projectId": f.project.ID, "title": "UTC design source"})
	plan := f.command(t, f.owner, "plans.propose", p.M{"goalId": f.goal.ID, "summary": "Explore timestamp presentation", "tasks": []any{p.M{"id": "prototype", "title": "Design UTC presentation", "kind": "design", "role": "design", "runtime": runtime.GOOS, "repositoryId": repo.ID, "target": "web"}}})
	f.command(t, f.owner, "plans.approve", p.M{"id": plan.ID})
	worker := f.command(t, f.owner, "workers.register", p.M{"projectId": f.project.ID, "title": "Design worker", "roles": []any{"design"}, "runtime": runtime.GOOS, "capabilities": []any{"design"}})
	token, _, err := f.h.Auth.IssueToken(context.Background(), f.owner, p.M{"projectId": f.project.ID, "workerId": worker.ID})
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(f.server.URL, token, true)
	if err != nil {
		t.Fatal(err)
	}
	runner := &Runner{Client: client, Config: Config{Driver: "native", AllowNative: true, StateDir: t.TempDir()}}
	calls := 0
	f.h.ModelCall = func(context.Context, p.Actor, p.M) (p.M, error) {
		calls++
		if calls == 1 {
			return actionResponse(p.M{"action": "write", "path": "utc.html", "content": "<!doctype html><button>GENERATED-UTC-PROTOTYPE</button>"}), nil
		}
		return actionResponse(p.M{"action": "finish", "summary": "Prototype prepared for human review", "design": p.M{"title": "UTC interaction", "summary": "Show the timezone next to every date", "decisions": "Use explicit UTC text", "criteria": []any{"utc"}, "reason": "Make the time contract visible", "prototypePath": "utc.html", "target": "web"}}), nil
	}
	if worked, err := runner.RunOnce(context.Background()); err != nil || !worked {
		t.Fatal(worked, err)
	}
	designs, _ := f.svc.Store.List(f.project.ID, "design", f.goal.ID)
	if len(designs) != 1 || designs[0].Status != "proposed" || p.S(designs[0].Data, "runId") == "" {
		runs, _ := f.svc.Store.List(f.project.ID, "run", f.goal.ID)
		t.Fatal("worker lost structured design result", designs, f.task(t, "prototype"), runs)
	}
	prototypes := p.Items(designs[0].Data["prototypes"])
	if len(prototypes) != 1 {
		t.Fatal(designs)
	}
	artifact, err := f.svc.Store.Get(p.S(p.Obj(prototypes[0]), "artifactId"))
	if err != nil || f.svc.VerifyArtifact(artifact) != nil || p.S(artifact.Data, "type") != "prototype" {
		t.Fatal(artifact, err)
	}
	response, err := client.request(context.Background(), "GET", "artifacts/"+artifact.ID+"/preview", nil)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if !strings.Contains(response.Header.Get("Content-Security-Policy"), "sandbox allow-scripts") || !strings.Contains(response.Header.Get("Content-Security-Policy"), "connect-src 'none'") {
		t.Fatal("prototype preview lost sandbox", response.Header)
	}
	approvals, _ := f.svc.Store.List(f.project.ID, "approval", f.goal.ID)
	if len(approvals) != 3 || f.task(t, "prototype").Status != "succeeded" {
		t.Fatal("design generation created approval or failed task", approvals)
	}
}

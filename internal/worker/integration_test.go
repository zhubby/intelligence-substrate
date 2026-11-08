package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/intelligence-substrate/platform/internal/httpapi"
	p "github.com/intelligence-substrate/platform/internal/platform"
)

// Only the model responses are scripted. Producers, merge preparation, conflict
// selection, snapshots, branch movement, packaging and QA use real HTTP/Git/tools.
func TestWorkerIntegratesParallelSourcesAndVerifiesPinnedOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX acceptance; Windows executable is cross-compiled separately")
	}
	for _, mode := range []string{"clean", "command", "model-and-repair"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			store, e := p.Open(filepath.Join(t.TempDir(), "control.db"))
			if e != nil {
				t.Fatal(e)
			}
			defer store.Close()
			svc := p.NewService(store)
			h := httpapi.New(svc, false)
			if e = h.Auth.Bootstrap("integration@example.test", "integration-password-123"); e != nil {
				t.Fatal(e)
			}
			identity, _ := h.Auth.IdentityByEmail("integration@example.test")
			owner := p.Actor{ID: identity.ID, Kind: "human", Admin: true}
			command := func(a p.Actor, name string, in p.M) p.Resource {
				t.Helper()
				v, err := svc.Command(ctx, a, name, in)
				if err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				return v
			}
			project := command(owner, "projects.create", p.M{"title": "Parallel integration"})
			goal := command(owner, "goals.create", p.M{"projectId": project.ID, "title": "Combined application", "outcome": "Deliver both independently developed changes", "targets": []any{"web"}, "criteria": []any{p.M{"id": "combined", "text": "Both features pass independent checks", "method": "automated"}}, "budget": 100})
			command(owner, "goals.approve", p.M{"id": goal.ID})
			repo := command(owner, "repositories.create", p.M{"projectId": project.ID, "title": "application"})
			seed := command(owner, "repositories.commit", p.M{"id": repo.ID, "branch": "seed", "message": "Common source", "files": p.M{"shared.txt": "base\n"}})
			base := p.S(seed.Data, "lastCommit")
			join := p.M{"id": "join", "title": "Integrate approved features", "kind": "code", "sourceMode": "integrate", "role": "developer", "runtime": runtime.GOOS, "repositoryId": repo.ID, "dependsOn": []any{"left", "right"}}
			if mode == "command" {
				join["command"] = []any{"/bin/sh", "-c", "printf 'combined\n' > shared.txt"}
			}
			tasks := []any{}
			for _, side := range []string{"left", "right"} {
				script := fmt.Sprintf("printf '%s\\n' > %s.txt", side, side)
				if mode != "clean" {
					script += fmt.Sprintf(" && printf '%s\\n' > shared.txt", side)
				}
				tasks = append(tasks, p.M{"id": side, "title": "Implement " + side, "kind": "code", "role": "developer", "runtime": runtime.GOOS, "repositoryId": repo.ID, "branch": side, "commit": base, "command": []any{"/bin/sh", "-c", script}})
			}
			tasks = append(tasks, join, p.M{"id": "build", "title": "Package combined source", "kind": "build", "role": "developer", "runtime": runtime.GOOS, "repositoryId": repo.ID, "dependsOn": []any{"join"}, "command": []any{"/bin/sh", "-c", "cat left.txt right.txt shared.txt > package.txt"}, "outputPath": "package.txt", "target": "web"})
			plan := command(owner, "plans.propose", p.M{"goalId": goal.ID, "summary": "Parallel source → governed integration → exact build → independent QA", "tasks": tasks})
			command(owner, "plans.approve", p.M{"id": plan.ID})
			step := 0
			h.ModelCall = func(_ context.Context, _ p.Actor, in p.M) (p.M, error) {
				if mode != "model-and-repair" {
					t.Error("clean/command integration called the model")
					return nil, io.EOF
				}
				var contextData p.M
				messages := p.Items(in["messages"])
				_ = json.Unmarshal([]byte(p.S(p.Obj(messages[1]), "content")), &contextData)
				var action p.M
				if approved := p.Obj(contextData["approvedTaskContext"]); p.S(p.Obj(approved["task"]), "defectId") != "" {
					if p.S(p.Obj(approved["task"]), "sourceMode") != "" {
						t.Error("repair inherited integration mode")
					}
					if len(messages) == 2 {
						action = p.M{"action": "write", "path": "shared.txt", "content": "combined\n"}
					} else {
						action = p.M{"action": "finish", "summary": "Fixed independent acceptance failure"}
					}
				} else {
					integration := p.Obj(contextData["integration"])
					if p.S(integration, "id") == "" || len(p.Strings(integration["conflicts"])) != 1 {
						t.Error("Agent has no prepared conflict context", integration)
					}
					parents := p.Strings(integration["parents"])
					if len(parents) != 2 {
						return nil, fmt.Errorf("missing exact input versions")
					}
					actions := []p.M{
						{"action": "read_input", "commit": parents[0], "path": "shared.txt"},
						{"action": "take_input", "commit": parents[1], "path": "shared.txt"},
						{"action": "resolve", "paths": []any{"shared.txt"}},
						{"action": "finish", "summary": "Integrated both features with an explicit selected version"},
					}
					if step >= len(actions) {
						return nil, io.EOF
					}
					action = actions[step]
					step++
				}
				body, _ := json.Marshal(action)
				return p.M{"choices": []any{p.M{"message": p.M{"content": string(body)}}}}, nil
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.Method == "GET" && strings.HasPrefix(req.URL.Path, "/api/v1/repositories/") && req.URL.Query().Has("branch") {
					t.Error("bound execution discovered a branch instead of exact source")
					http.Error(w, "disabled", 503)
					return
				}
				h.ServeHTTP(w, req)
			}))
			defer server.Close()
			runner := func(role string, caps []any, quality bool) *Runner {
				t.Helper()
				w := command(owner, "workers.register", p.M{"projectId": project.ID, "title": role, "roles": []any{role}, "runtime": runtime.GOOS, "capabilities": caps, "trustedQuality": quality})
				token, _, err := h.Auth.IssueToken(ctx, owner, p.M{"projectId": project.ID, "workerId": w.ID})
				if err != nil {
					t.Fatal(err)
				}
				client, err := NewClient(server.URL, token, true)
				if err != nil {
					t.Fatal(err)
				}
				return &Runner{Client: client, Config: Config{Driver: "native", AllowNative: true, StateDir: t.TempDir()}}
			}
			dev := runner("developer", []any{"code", "build"}, false)
			qa := runner("quality", []any{"test"}, true)
			run := func(r *Runner) {
				t.Helper()
				if worked, err := r.RunOnce(ctx); err != nil || !worked {
					t.Fatalf("worked=%v: %v", worked, err)
				}
			}
			for i := 0; i < 3; i++ {
				run(dev)
			}
			runs, _ := store.List(project.ID, "run", goal.ID)
			var integrated p.Resource
			parents := []string{}
			for _, item := range runs {
				if item.Status != "succeeded" {
					t.Fatal("source execution failed", item)
				}
				if p.S(p.Obj(item.Data["task"]), "sourceMode") == "integrate" {
					integrated = item
				} else {
					parents = append(parents, p.S(p.Obj(item.Data["result"]), "commit"))
				}
			}
			if integrated.ID == "" {
				t.Fatal("integration never completed")
			}
			result := p.Obj(integrated.Data["result"])
			commit := p.S(result, "commit")
			actual, err := svc.Git(repo, "show", "-s", "--format=%P", commit)
			if err != nil || len(strings.Fields(actual)) != 2 {
				t.Fatal("not a merge of the actual producers", actual, err)
			}
			for _, parent := range parents {
				if !strings.Contains(actual, parent) {
					t.Fatal("producer lost from final ancestry", actual, parent)
				}
			}
			command(owner, "repositories.commit", p.M{"id": repo.ID, "branch": p.S(result, "branch"), "expectedHead": commit, "baseCommit": commit, "message": "Advance target after integration", "files": p.M{"left.txt": "wrong later bytes"}})
			run(dev)
			artifacts, _ := store.List(project.ID, "artifact", goal.ID)
			if len(artifacts) != 1 || p.S(artifacts[0].Data, "commit") != commit {
				t.Fatal("build followed moved branch", artifacts)
			}
			want := "combined"
			if mode == "clean" {
				want = "base"
			}
			pipeline := command(owner, "pipelines.create", p.M{"projectId": project.ID, "title": "Check combined package", "runtime": runtime.GOOS, "method": "automated", "command": []any{"/bin/sh", "-c", "test \"$(cat \"$IS_ARTIFACT_PATH\")\" = \"$(printf 'left\\nright\\n" + want + "')\""}})
			command(owner, "pipelines.run", p.M{"pipelineId": pipeline.ID, "artifactId": artifacts[0].ID, "criteria": []any{"combined"}})
			run(qa)
			if mode == "model-and-repair" {
				defects, _ := store.List(project.ID, "defect", goal.ID)
				if len(defects) != 1 || defects[0].Status != "repairing" {
					t.Fatal("independent failure did not create a repair", defects)
				}
				for i := 0; i < 2; i++ {
					run(dev)
				}
				run(qa)
				defects, _ = store.List(project.ID, "defect", goal.ID)
				if defects[0].Status != "closed" {
					t.Fatal("integrated repair failed", defects)
				}
			}
			evidence, _ := store.List(project.ID, "evidence", goal.ID)
			if len(evidence) != 1 || evidence[0].Status != "passed" {
				t.Fatal("no independent success evidence", evidence)
			}
		})
	}
}

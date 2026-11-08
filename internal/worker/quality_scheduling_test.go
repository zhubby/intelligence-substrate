package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/intelligence-substrate/platform/internal/httpapi"
	p "github.com/intelligence-substrate/platform/internal/platform"
)

func TestWorkerQualitySchedulingAutomaticRepairAndReverification(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX tool acceptance; native Windows execution remains separate")
	}
	ctx := context.Background()
	store, err := p.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	svc := p.NewService(store)
	h := httpapi.New(svc, false)
	if err = h.Auth.Bootstrap("quality@example.test", "quality-worker-password-2026"); err != nil {
		t.Fatal(err)
	}
	identity, _ := h.Auth.IdentityByEmail("quality@example.test")
	owner := p.Actor{ID: identity.ID, Kind: "human", Admin: true}
	command := func(name string, in p.M) p.Resource {
		t.Helper()
		r, err := svc.Command(ctx, owner, name, in)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return r
	}
	project := command("projects.create", p.M{"title": "Automatic independent QA"})
	g := command("goals.create", p.M{"projectId": project.ID, "title": "Verified package", "outcome": "Build and independently verify exact repaired content", "criteria": []any{p.M{"id": "content", "text": "Package contains expected content", "method": "automated"}}, "targets": []any{"web"}, "budget": 100})
	command("goals.approve", p.M{"id": g.ID})
	repo := command("repositories.create", p.M{"projectId": project.ID, "title": "actual-source"})
	seed := command("repositories.commit", p.M{"id": repo.ID, "branch": "seed", "message": "Actual baseline", "files": p.M{"source.txt": "broken\n"}})
	plan := command("plans.propose", p.M{"goalId": g.ID, "summary": "Implement, package and independently repair within the approved recipe", "tasks": []any{
		p.M{"id": "code", "title": "Implement source", "role": "developer", "kind": "code", "runtime": runtime.GOOS, "repositoryId": repo.ID, "branch": "implementation", "commit": p.S(seed.Data, "lastCommit"), "budgetCents": 100, "command": []any{"/bin/sh", "-c", "if test -f implemented; then printf 'expected\\n' > source.txt; else touch implemented; fi"}},
		p.M{"id": "build", "title": "Build source package", "role": "developer", "kind": "build", "runtime": runtime.GOOS, "repositoryId": repo.ID, "dependsOn": []any{"code"}, "budgetCents": 100, "command": []any{"/bin/sh", "-c", "test -z \"$IS_WORKER_TOKEN\" && cat source.txt > package.txt"}, "outputPath": "package.txt", "target": "web"},
	}})
	command("plans.approve", p.M{"id": plan.ID})
	pipeline := command("pipelines.create", p.M{"projectId": project.ID, "title": "Exact package content", "runtime": runtime.GOOS, "method": "automated", "command": []any{"/bin/sh", "-c", "test -z \"$IS_WORKER_TOKEN\" && grep -qx expected \"$IS_ARTIFACT_PATH\""}})
	policy := command("quality.bind", p.M{"goalId": g.ID, "expectedRequirementRevision": g.Revision, "expectedPlanId": plan.ID, "pipelineId": pipeline.ID, "target": "web", "criteria": []any{"content"}, "budgetCents": 100, "key": "approved-qa"})
	h.ModelCall = func(context.Context, p.Actor, p.M) (p.M, error) {
		t.Error("fixed-command QA/repair unexpectedly used a model")
		return nil, fmt.Errorf("model calls are disabled")
	}
	// Simulate a fetched strategy with the same revision but different content.
	// The controller must reject it before executing any of its commands.
	var alterPipeline atomic.Bool
	marker := filepath.Join(t.TempDir(), "unapproved-command-executed")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if alterPipeline.Load() && req.Method == http.MethodGet && req.URL.Path == "/api/v1/resources/"+pipeline.ID {
			changed := pipeline
			changed.Data = p.M{}
			for key, value := range pipeline.Data {
				changed.Data[key] = value
			}
			changed.Data["command"] = []any{"/usr/bin/touch", marker}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(changed)
			return
		}
		h.ServeHTTP(w, req)
	}))
	defer server.Close()
	runner := func(role string, caps []any, quality bool) *Runner {
		t.Helper()
		w := command("workers.register", p.M{"projectId": project.ID, "title": role, "runtime": runtime.GOOS, "roles": []any{role}, "capabilities": caps, "trustedQuality": quality})
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
	developer := runner("developer", []any{"code", "build"}, false)
	qa := runner("quality", []any{"test"}, true)
	t.Setenv("IS_WORKER_TOKEN", "must-never-reach-job")
	work := func(r *Runner) {
		t.Helper()
		worked, err := r.RunOnce(ctx)
		if err != nil || !worked {
			t.Fatal("worker step failed", worked, err)
		}
	}
	work(developer)
	work(developer)
	work(qa)
	defects, _ := store.List(project.ID, "defect", g.ID)
	if len(defects) != 1 || defects[0].Status != "repairing" {
		t.Fatal("actual failing tool did not start a repair", defects)
	}
	work(developer)
	tasks, _ := store.List(project.ID, "task", g.ID)
	for _, task := range tasks {
		if p.S(task.Data, "requiresArtifactFrom") != "" && p.S(task.Data, "qualityDispatchId") != "" {
			t.Fatal("repair recheck inherited the previous artifact dispatch", task)
		}
	}
	work(developer)
	work(qa)
	if worked, err := qa.RunOnce(ctx); err != nil || worked {
		t.Fatal("original repair check was dispatched twice", worked, err)
	}
	defects, _ = store.List(project.ID, "defect", g.ID)
	if defects[0].Status != "closed" {
		t.Fatal("actual repaired content did not close defect", defects)
	}
	artifacts, _ := store.List(project.ID, "artifact", g.ID)
	if len(artifacts) != 2 {
		t.Fatal("expected original and rebuilt immutable packages", artifacts)
	}
	dispatches, _ := store.List(project.ID, "quality_dispatch", g.ID)
	evidence, _ := store.List(project.ID, "evidence", g.ID)
	if len(dispatches) != 2 || len(evidence) != 1 || p.S(evidence[0].Data, "qualityPolicyId") != policy.ID {
		t.Fatal("repair lost exact independent policy evidence", dispatches, evidence)
	}
	verified, err := store.Get(p.S(evidence[0].Data, "artifactId"))
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(svc.ArtifactPath(p.S(verified.Data, "digest")))
	if err != nil || string(content) != "expected\n" {
		t.Fatal("evidence is not for actual repaired package", string(content), err)
	}
	tasks, _ = store.List(project.ID, "task", g.ID)
	count := 0
	for _, task := range tasks {
		if p.S(task.Data, "kind") == "test" {
			count++
		}
	}
	if count != 2 {
		t.Fatal("repair created duplicate protected verification work", count)
	}
	if _, err = svc.Command(ctx, owner, "releases.create", p.M{"goalId": g.ID, "version": "1.0.0", "commit": p.S(verified.Data, "commit"), "artifactIds": []any{verified.ID}}); err == nil {
		t.Fatal("CI-only execution impersonated real browser acceptance")
	}
	command("quality.revoke", p.M{"id": policy.ID, "reason": "Review replacement binding", "key": "revoke-original"})
	replacement := command("quality.bind", p.M{"goalId": g.ID, "expectedRequirementRevision": g.Revision, "expectedPlanId": plan.ID, "pipelineId": pipeline.ID, "target": "web", "criteria": []any{"content"}, "budgetCents": 100, "key": "replacement-qa"})
	alterPipeline.Store(true)
	work(qa)
	if _, err = os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("unapproved pipeline command executed", err)
	}
	runs, _ := store.List(project.ID, "run", g.ID)
	blocked := 0
	for _, run := range runs {
		if p.S(p.Obj(run.Data["task"]), "qualityPolicyId") == replacement.ID {
			if run.Status != "blocked" || !strings.Contains(p.S(p.Obj(run.Data["result"]), "reason"), "pipeline content changed") {
				t.Fatal("same-revision pipeline mismatch was not blocked before tools", run)
			}
			blocked++
		}
	}
	if blocked != 1 {
		t.Fatal("missing actual Worker content-hash rejection", blocked)
	}
	evidence, _ = store.List(project.ID, "evidence", g.ID)
	if len(evidence) != 1 {
		t.Fatal("altered pipeline added evidence", evidence)
	}
}

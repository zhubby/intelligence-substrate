package worker

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/intelligence-substrate/platform/internal/blueprint"
	"github.com/intelligence-substrate/platform/internal/httpapi"
	p "github.com/intelligence-substrate/platform/internal/platform"
)

func TestBlueprintWorkerInitializesTwoRealApplicationsAndChecksBackend(t *testing.T) {
	for _, mode := range []string{"feedback", "booking"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			if evidence := os.Getenv("IS_BLUEPRINT_EVIDENCE_DIR"); evidence != "" {
				root = filepath.Join(evidence, mode)
				if err := os.Mkdir(root, 0700); err != nil {
					t.Fatal(err)
				}
			}
			store, err := p.Open(filepath.Join(root, "control.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			s := &p.Service{Store: store, Root: root}
			s.Blueprints, err = blueprint.Load("../..")
			if err != nil {
				t.Fatal(err)
			}
			h := httpapi.New(s, false)
			if err = h.Auth.Bootstrap("blueprint@example.test", "blueprint-test-password-2026"); err != nil {
				t.Fatal(err)
			}
			identity, err := h.Auth.IdentityByEmail("blueprint@example.test")
			if err != nil {
				t.Fatal(err)
			}
			owner := p.Actor{ID: identity.ID, Kind: "human", Admin: true}
			command := func(a p.Actor, name string, in p.M) p.Resource {
				t.Helper()
				r, e := s.Command(ctx, a, name, in)
				if e != nil {
					t.Fatalf("%s: %v", name, e)
				}
				return r
			}
			project := command(owner, "projects.create", p.M{"title": "Governed " + mode + " application"})
			goal := command(owner, "goals.create", p.M{"projectId": project.ID, "title": mode + " application", "outcome": "Implement the reviewed business scope", "criteria": []any{p.M{"id": "business", "text": "A real user can complete the primary journey"}}, "targets": []any{"web"}, "budget": 100})
			command(owner, "goals.approve", p.M{"id": goal.ID})
			selection := command(owner, "blueprints.propose", p.M{"goalId": goal.ID, "expectedRequirementRevision": goal.Revision, "key": "source-seed", "blueprintId": "flutter-go-v1", "expectedCatalogDigest": s.Blueprints.List()[0].Digest, "applicationName": "enterprise_" + mode, "organization": "com.enterprise", "mode": mode, "reason": "Initialize a versioned reference application"})
			plan := command(owner, "plans.propose", p.M{"goalId": goal.ID, "blueprintId": selection.ID, "expectedBlueprintBinding": selection.Data["binding"], "initializeBlueprint": true, "summary": "Initialize exact sources, then execute actual backend tests", "tasks": []any{p.M{"id": "backend-check", "title": "Run actual backend checks", "kind": "security", "role": "developer", "runtime": runtime.GOOS, "command": []any{"/bin/sh", "-c", "cd backend && go test -count=1 -v ./..."}}}})
			node := command(owner, "workers.register", p.M{"projectId": project.ID, "title": "Disposable native source worker", "runtime": runtime.GOOS, "roles": []any{"developer"}, "capabilities": []any{"code", "security"}})
			token, _, err := h.Auth.IssueToken(ctx, owner, p.M{"projectId": project.ID, "workerId": node.ID})
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(h)
			defer server.Close()
			client, err := NewClient(server.URL, token, true)
			if err != nil {
				t.Fatal(err)
			}
			runner := &Runner{Client: client, Config: Config{Driver: "native", AllowNative: true, StateDir: filepath.Join(root, "worker-state")}}
			if worked, err := runner.RunOnce(ctx); err != nil || worked {
				t.Fatal("unapproved blueprint ran", worked, err)
			}
			command(owner, "plans.approve", p.M{"id": plan.ID})
			// The persisted approved seed remains authoritative after the
			// administrator removes/replaces the installed catalog.
			s.Blueprints = nil
			for range 2 {
				if worked, err := runner.RunOnce(ctx); err != nil || !worked {
					t.Fatal("blueprint workflow did not execute", worked, err)
				}
			}
			runs, err := store.List(project.ID, "run", goal.ID)
			if err != nil || len(runs) != 2 {
				t.Fatal(runs, err)
			}
			var initializer, check p.Resource
			for _, run := range runs {
				if run.Status != "succeeded" {
					t.Fatal("actual execution failed", run)
				}
				if p.S(p.Obj(run.Data["task"]), "logicalId") == "blueprint-initialize" {
					initializer = run
				} else {
					check = run
				}
			}
			if !strings.Contains(p.S(check.Data, "log"), "PASS") || p.S(p.Obj(initializer.Data["result"]), "source") != "controller-blueprint" {
				t.Fatal("no real backend test output or deterministic initializer", runs)
			}
			result := p.Obj(initializer.Data["result"])
			repo, err := store.Get(p.S(result, "repositoryId"))
			if err != nil {
				t.Fatal(err)
			}
			commit := p.S(result, "commit")
			config, err := s.Git(repo, "show", commit+":application.json")
			if err != nil {
				t.Fatal(err)
			}
			var application p.M
			if json.Unmarshal([]byte(config), &application) != nil || p.S(application, "name") != "enterprise_"+mode || p.S(application, "mode") != mode {
				t.Fatal("wrong generated application", application)
			}
			if tree, err := s.Git(repo, "ls-tree", "-r", "refs/heads/main"); err != nil || tree != "" {
				t.Fatal("initializer wrote protected main", tree, err)
			}
			binding := p.Obj(p.Obj(check.Data["task"])["sourceBinding"])
			if p.S(binding, "commit") != commit || len(p.Items(binding["inputs"])) != 1 {
				t.Fatal("downstream check ignored initializer source", binding)
			}
			for _, kind := range []string{"artifact", "evidence", "release"} {
				rows, err := store.List(project.ID, kind, goal.ID)
				if err != nil || len(rows) != 0 {
					t.Fatal("source initialization invented delivery proof", kind, rows, err)
				}
			}
			if err = s.VerifyIntegrity(ctx); err != nil {
				t.Fatal(err)
			}
			if os.Getenv("IS_BLUEPRINT_EVIDENCE_DIR") != "" {
				backup := filepath.Join(root, "backup.tar.gz")
				if err = s.Backup(ctx, backup); err != nil {
					t.Fatal(err)
				}
				proof := p.M{"passed": true, "projectId": project.ID, "goalId": goal.ID, "blueprintId": selection.ID, "planId": plan.ID, "repositoryId": repo.ID, "commit": commit, "initializerRunId": initializer.ID, "backendCheckRunId": check.ID, "archiveDigest": selection.Data["digest"], "application": application, "mainUnchanged": true, "paidModelCalled": false, "productionDelivered": false}
				body, _ := json.MarshalIndent(proof, "", "  ")
				if err = os.WriteFile(filepath.Join(root, "result.json"), body, 0600); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

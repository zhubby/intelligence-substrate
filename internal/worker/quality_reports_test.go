package worker

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/intelligence-substrate/platform/internal/httpapi"
	p "github.com/intelligence-substrate/platform/internal/platform"
)

func TestWorkerCollectsProtectedQualityReports(t *testing.T) {
	for _, mode := range []string{"success", "failure", "missing", "preexisting", "symlink", "failure-without-report", "no-report"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			store, err := p.Open(filepath.Join(t.TempDir(), "control.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			svc := p.NewService(store)
			h := httpapi.New(svc, false)
			if err := h.Auth.Bootstrap("report@example.test", "test-report-password-123"); err != nil {
				t.Fatal(err)
			}
			identity, _ := h.Auth.IdentityByEmail("report@example.test")
			owner := p.Actor{ID: identity.ID, Kind: "human", Admin: true}
			command := func(a p.Actor, name string, in p.M) p.Resource {
				t.Helper()
				r, err := svc.Command(ctx, a, name, in)
				if err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				return r
			}
			project := command(owner, "projects.create", p.M{"title": "Report collection"})
			goal := command(owner, "goals.create", p.M{"projectId": project.ID, "title": "Archived quality", "outcome": "Exact tested package and report", "targets": []any{"web"}, "criteria": []any{p.M{"id": "business", "text": "Actual validation report", "method": "automated"}}, "budget": 100})
			command(owner, "goals.approve", p.M{"id": goal.ID})
			repo := command(owner, "repositories.create", p.M{"projectId": project.ID, "title": "source"})
			files := p.M{"index.html": "actual package"}
			if mode == "preexisting" {
				files["quality/report.txt"] = "stale checked-in report"
			}
			repo = command(owner, "repositories.commit", p.M{"id": repo.ID, "branch": "feature", "message": "Application source", "files": files})
			plan := command(owner, "plans.propose", p.M{"goalId": goal.ID, "summary": "Actual build", "tasks": []any{p.M{"id": "build", "title": "Build package", "role": "developer", "kind": "build", "runtime": runtime.GOOS, "repositoryId": repo.ID, "commit": p.S(repo.Data, "lastCommit"), "command": []any{"/bin/sh", "-c", "cp index.html package.html"}, "outputPath": "package.html", "target": "web"}}})
			command(owner, "plans.approve", p.M{"id": plan.ID})
			server := httptest.NewServer(h)
			defer server.Close()
			runnerFor := func(role, capability string, quality bool) *Runner {
				t.Helper()
				w := command(owner, "workers.register", p.M{"projectId": project.ID, "title": role, "runtime": runtime.GOOS, "roles": []any{role}, "capabilities": []any{capability}, "trustedQuality": quality})
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
			developer := runnerFor("developer", "build", false)
			if worked, err := developer.RunOnce(ctx); err != nil || !worked {
				t.Fatal("build", worked, err)
			}
			artifacts, _ := store.List(project.ID, "artifact", goal.ID)
			if len(artifacts) != 1 {
				t.Fatal("missing package", artifacts)
			}
			argv := "mkdir -p quality && cat \"$IS_ARTIFACT_PATH\" > quality/report.txt"
			switch mode {
			case "failure":
				argv += "; echo actual-failure; exit 7"
			case "missing", "preexisting":
				argv = "echo no-report-produced"
			case "symlink":
				argv = "mkdir -p quality && ln -s \"$IS_ARTIFACT_PATH\" quality/report.txt"
			case "failure-without-report":
				argv = "echo actual-failure; exit 7"
			}
			in := p.M{"projectId": project.ID, "title": "Protected report", "command": []any{"/bin/sh", "-c", argv}, "method": "automated", "runtime": runtime.GOOS, "verificationEnvironment": "browser"}
			if mode != "no-report" {
				in["reportPath"] = "quality/report.txt"
			}
			pipe := command(owner, "pipelines.create", in)
			command(owner, "quality.bind", p.M{"goalId": goal.ID, "expectedRequirementRevision": goal.Revision, "expectedPlanId": plan.ID, "pipelineId": pipe.ID, "target": "web", "criteria": []any{"business"}, "budgetCents": 100, "key": mode})
			qa := runnerFor("quality", "test", true)
			if worked, err := qa.RunOnce(ctx); err != nil || !worked {
				t.Fatal("QA", worked, err)
			}
			runs, _ := store.List(project.ID, "run", goal.ID)
			var run p.Resource
			for _, r := range runs {
				if p.S(p.Obj(r.Data["task"]), "kind") == "test" {
					run = r
				}
			}
			want := "succeeded"
			if mode == "missing" || mode == "preexisting" || mode == "symlink" {
				want = "blocked"
			}
			if mode == "failure" || mode == "failure-without-report" {
				want = "failed"
			}
			if run.Status != want {
				t.Fatalf("report collection state=%s want=%s: %+v", run.Status, want, run)
			}
			if mode == "failure" || mode == "failure-without-report" {
				if p.N(run.Data, "exitCode") != 7 {
					t.Fatal("report handling replaced actual command failure", run)
				}
			}
			evidence, _ := store.List(project.ID, "evidence", goal.ID)
			if want != "succeeded" && len(evidence) != 0 {
				t.Fatal("report failure created passing evidence", evidence)
			}
			if mode == "success" || mode == "failure" {
				report, err := store.Get(p.S(p.Obj(run.Data["result"]), "reportArtifactId"))
				if err != nil || p.S(report.Data, "runId") != run.ID || p.S(report.Data, "testedArtifactId") != artifacts[0].ID || p.S(report.Data, "type") != "report" {
					t.Fatal("controller lost report provenance", report, err)
				}
				b, err := os.ReadFile(svc.ArtifactPath(p.S(report.Data, "digest")))
				if err != nil || string(b) != "actual package" {
					t.Fatal("wrong report bytes", string(b), err)
				}
				if mode == "success" && (len(evidence) != 1 || p.S(evidence[0].Data, "proofArtifactId") != report.ID) {
					t.Fatal("passing evidence lost report", evidence)
				}
			}
		})
	}
}

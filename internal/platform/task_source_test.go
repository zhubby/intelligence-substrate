package platform

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// These fixtures model persisted, completed work. Commits and the claim API use
// real Git and SQL; mutations below deliberately exercise corrupt/stale records.
type sourceFixture struct {
	s                *Service
	a, worker        Actor
	p, g, repo, plan Resource
}

func newSourceFixture(t *testing.T, recipes ...M) *sourceFixture {
	t.Helper()
	s, a, p := fixture(t)
	return newSourceFixtureOn(t, s, a, p, recipes...)
}

func newSourceFixtureOn(t *testing.T, s *Service, a Actor, p Resource, recipes ...M) *sourceFixture {
	t.Helper()
	f := &sourceFixture{s: s, a: a, p: p}
	f.repo = must(t, s, a, "repositories.create", M{"projectId": p.ID, "title": "application"})
	f.g = goal(t, s, a, p)
	must(t, s, a, "goals.approve", M{"id": f.g.ID})
	items := []any{}
	for _, recipe := range recipes {
		m := M{"title": S(recipe, "id"), "kind": "code", "role": "developer", "runtime": "linux", "repositoryId": f.repo.ID, "budgetCents": 10}
		for k, v := range recipe {
			m[k] = v
		}
		items = append(items, m)
	}
	f.plan = must(t, s, a, "plans.propose", M{"goalId": f.g.ID, "summary": "Source provenance acceptance", "tasks": items})
	must(t, s, a, "plans.approve", M{"id": f.plan.ID})
	w := must(t, s, a, "workers.register", M{"projectId": p.ID, "title": "Source worker", "roles": []any{"developer"}, "runtime": "linux", "capabilities": []any{"code", "build", "security", "analysis", "design"}})
	f.worker = Actor{ID: "worker:" + w.ID, Kind: "worker", WorkerID: w.ID, ProjectID: p.ID, Roles: []string{"developer"}}
	return f
}
func (f *sourceFixture) task(t *testing.T, logical string) Resource {
	t.Helper()
	tasks, e := f.s.Store.List(f.p.ID, "task", f.g.ID)
	if e != nil {
		t.Fatal(e)
	}
	for _, task := range tasks {
		if S(task.Data, "logicalId") == logical {
			return task
		}
	}
	t.Fatal("missing task", logical)
	return Resource{}
}
func (f *sourceFixture) put(t *testing.T, r Resource) {
	t.Helper()
	if e := f.s.Store.Update(context.Background(), func(tx *Tx) error { return tx.Put(r) }); e != nil {
		t.Fatal(e)
	}
}
func (f *sourceFixture) commit(t *testing.T, branch, base string) string {
	t.Helper()
	head, _ := f.s.Git(f.repo, "rev-parse", "--verify", "refs/heads/"+branch)
	r := must(t, f.s, f.a, "repositories.commit", M{"id": f.repo.ID, "branch": branch, "baseCommit": base, "expectedHead": head, "message": "Implement " + branch, "files": M{branch + ".txt": branch + base}})
	return S(r.Data, "lastCommit")
}
func (f *sourceFixture) result(t *testing.T, logical, commit string) Resource {
	t.Helper()
	task := f.task(t, logical)
	task.Status = "succeeded"
	f.put(t, task)
	run := resource("run", task.Title, f.p.ID, f.g.ID, M{"taskId": task.ID, "workerId": f.worker.WorkerID, "requirementRevision": f.g.Revision, "planId": f.plan.ID, "task": copyM(task.Data), "result": M{"repositoryId": f.repo.ID, "commit": commit}, "exitCode": 0})
	run.Status = "succeeded"
	f.put(t, run)
	change := resource("change", "Persisted worker change", f.p.ID, f.g.ID, M{"repositoryId": f.repo.ID, "commit": commit, "authorId": f.worker.ID, "runId": run.ID})
	f.put(t, change)
	return run
}
func (f *sourceFixture) claim(t *testing.T) Resource {
	t.Helper()
	return must(t, f.s, f.worker, "tasks.claim", M{})
}
func assertSource(t *testing.T, run Resource, repo, commit string, producers ...Resource) M {
	t.Helper()
	if run.Kind != "run" {
		t.Fatalf("expected source-bound run, got %+v", run)
	}
	task := Obj(run.Data["task"])
	binding := Obj(task["sourceBinding"])
	if S(task, "commit") != commit || S(task, "repositoryId") != repo || S(binding, "commit") != commit || S(binding, "repositoryId") != repo || N(binding, "version") != 1 || len(S(binding, "digest")) != 64 {
		t.Fatalf("missing or wrong immutable source: %+v", task)
	}
	inputs := Items(binding["inputs"])
	if len(inputs) != len(producers) {
		t.Fatalf("wrong producer frontier: %+v", binding)
	}
	for _, producer := range producers {
		found := false
		for _, item := range inputs {
			m := Obj(item)
			if S(m, "runId") == producer.ID && S(m, "taskId") == S(producer.Data, "taskId") && S(m, "commit") == S(Obj(producer.Data["result"]), "commit") {
				found = true
			}
		}
		if !found {
			t.Fatal("producer provenance missing", producer.ID, binding)
		}
	}
	return binding
}

func TestClaimPinsDependencySourceThroughDesignDespiteBranchMovement(t *testing.T) {
	f := newSourceFixture(t, M{"id": "code", "branch": "feature"}, M{"id": "design", "kind": "design", "dependsOn": []any{"code"}}, M{"id": "build", "kind": "build", "branch": "feature", "dependsOn": []any{"design"}})
	commit := f.commit(t, "feature", S(f.repo.Data, "head"))
	producer := f.result(t, "code", commit)
	design := f.task(t, "design")
	design.Status = "succeeded"
	f.put(t, design)
	f.commit(t, "feature", commit) // Same branch now contains a different application.
	run := f.claim(t)
	assertSource(t, run, f.repo.ID, commit, producer)
	task, _ := f.s.Store.Get(S(run.Data, "taskId"))
	if Hash(task.Data["sourceBinding"]) != Hash(Obj(run.Data["task"])["sourceBinding"]) {
		t.Fatal("lease snapshot lost source authority")
	}
}

func TestSourceInputsRequireIntegrationOrApprovedSelection(t *testing.T) {
	for _, mode := range []string{"unmerged", "dominant", "selected", "explicit-integration", "explicit-excludes-input", "multiple-repositories", "repository-filter", "split-parent", "split-integrated"} {
		t.Run(mode, func(t *testing.T) {
			consumer := M{"id": "build", "kind": "build", "dependsOn": []any{"left", "right"}}
			if mode == "selected" {
				consumer["sourceFrom"] = []any{"left"}
			}
			if mode == "multiple-repositories" || mode == "repository-filter" {
				consumer["repositoryId"] = ""
			}
			f := newSourceFixture(t, M{"id": "left", "branch": "left"}, M{"id": "right", "branch": "right"}, consumer)
			base := S(f.repo.Data, "head")
			left := f.commit(t, "left", base)
			rightBase := base
			if mode == "dominant" || mode == "split-integrated" {
				rightBase = left
			}
			right := f.commit(t, "right", rightBase)
			lr, rr := f.result(t, "left", left), f.result(t, "right", right)
			if mode == "explicit-integration" {
				tree, e := f.s.Git(f.repo, "rev-parse", right+"^{tree}")
				if e != nil {
					t.Fatal(e)
				}
				merged, e := f.s.gitIdentity(f.repo, "commit-tree", tree, "-p", left, "-p", right, "-m", "Integrate both inputs")
				if e != nil {
					t.Fatal(e)
				}
				task := f.task(t, "build")
				task.Data["commit"] = merged
				f.put(t, task)
				run := f.claim(t)
				assertSource(t, run, f.repo.ID, merged, lr, rr)
				return
			}
			if mode == "explicit-excludes-input" {
				task := f.task(t, "build")
				task.Data["commit"] = left
				f.put(t, task)
			}
			if mode == "multiple-repositories" || mode == "repository-filter" {
				other := must(t, f.s, f.a, "repositories.create", M{"projectId": f.p.ID, "title": "other"})
				other = must(t, f.s, f.a, "repositories.commit", M{"id": other.ID, "branch": "right", "message": "Other source", "files": M{"other": "source"}})
				task := f.task(t, "right")
				task.Data["repositoryId"] = other.ID
				f.put(t, task)
				rr.Data["result"] = M{"repositoryId": other.ID, "commit": S(other.Data, "lastCommit")}
				rr.Data["task"] = task.Data
				f.put(t, rr)
				f.put(t, resource("change", "Other producer", f.p.ID, f.g.ID, M{"repositoryId": other.ID, "commit": S(other.Data, "lastCommit"), "authorId": f.worker.ID, "runId": rr.ID}))
				if mode == "repository-filter" {
					task = f.task(t, "build")
					task.Data["repositoryId"] = f.repo.ID
					f.put(t, task)
				}
			}
			if mode == "split-parent" || mode == "split-integrated" {
				parent := resource("task", "Aggregate", f.p.ID, f.g.ID, M{"id": "aggregate", "logicalId": "aggregate", "planId": f.plan.ID, "requirementRevision": 1, "kind": "code", "children": []any{S(lr.Data, "taskId"), S(rr.Data, "taskId")}, "dependsOn": []any{"left", "right"}})
				parent.Status = "succeeded"
				f.put(t, parent)
				for _, id := range []string{"left", "right"} {
					child := f.task(t, id)
					child.Data["parentId"] = parent.ID
					f.put(t, child)
				}
				task := f.task(t, "build")
				task.Data["dependsOn"] = []any{"aggregate"}
				f.put(t, task)
			}
			run := f.claim(t)
			switch mode {
			case "dominant", "split-integrated":
				assertSource(t, run, f.repo.ID, right, lr, rr)
			case "selected", "repository-filter":
				assertSource(t, run, f.repo.ID, left, lr)
			default:
				if run.Kind == "run" {
					t.Fatal("unintegrated/ambiguous source was executed", run)
				}
				task := f.task(t, "build")
				detail := Obj(task.Data["sourceConflict"])
				if task.Status != "blocked" || S(detail, "code") == "" || len(Items(detail["inputs"])) != 2 || N(task.Data, "attempts") != 0 {
					t.Fatalf("source conflict lacks actionable provenance: %+v", task)
				}
			}
		})
	}
}

func TestSourceRetryRetainsExactInputAndProtectsAdaptiveBoundary(t *testing.T) {
	f := newSourceFixture(t, M{"id": "build", "kind": "build", "branch": "main"})
	commit := S(f.repo.Data, "head")
	run := f.claim(t)
	binding := assertSource(t, run, f.repo.ID, commit)
	must(t, f.s, f.worker, "tasks.complete", M{"id": run.ID, "leaseToken": S(run.Data, "leaseToken"), "exitCode": 1, "result": M{"reason": "tool failure"}})
	newHead := f.commit(t, "advance", commit)
	if _, e := f.s.Git(f.repo, "update-ref", "refs/heads/main", newHead, commit); e != nil {
		t.Fatal(e)
	}
	task := f.task(t, "build")
	must(t, f.s, f.a, "tasks.retry", M{"id": task.ID})
	if _, e := f.s.Command(context.Background(), f.a, "plans.reorder", M{"goalId": f.g.ID, "reason": "Change source", "impact": "Changes the input", "tasks": []any{M{"id": "build", "dependsOn": []any{}}}}); e == nil {
		t.Fatal("bound task dependencies were rewritten")
	}
	run = f.claim(t)
	again := assertSource(t, run, f.repo.ID, commit)
	if Hash(binding) != Hash(again) {
		t.Fatal("retry changed immutable source")
	}
}

func TestSourceBindingRejectsUntrustedPlanFieldsAndInvalidSelectors(t *testing.T) {
	for _, extra := range []M{{"sourceBinding": M{}}, {"sourceConflict": M{}}, {"sourceFrom": "a"}, {"sourceFrom": []any{1}}, {"sourceFrom": []any{"a", "a"}}, {"sourceFrom": []any{"b"}}, {"sourceFrom": []any{"consumer"}}, {"sourceFrom": []any{"a/escape"}}, {"sourceFrom": []any{}}, {"commit": "main"}, {"branch": "../bad"}, {"commit": 12}, {"branch": true}} {
		t.Run(Hash(extra)[:8], func(t *testing.T) {
			s, a, p := fixture(t)
			g := goal(t, s, a, p)
			must(t, s, a, "goals.approve", M{"id": g.ID})
			consumer := M{"id": "consumer", "title": "Consumer", "kind": "build", "role": "developer", "runtime": "linux", "dependsOn": []any{"a"}}
			for k, v := range extra {
				consumer[k] = v
			}
			_, e := s.Command(context.Background(), a, "plans.propose", M{"goalId": g.ID, "summary": "Reject forged source", "tasks": []any{M{"id": "a", "title": "A", "kind": "code", "role": "developer", "runtime": "linux"}, M{"id": "b", "title": "B", "kind": "code", "role": "developer", "runtime": "linux"}, consumer}})
			if e == nil {
				t.Fatal("untrusted source contract accepted", extra)
			}
		})
	}
}

func TestSourceProvenanceRejectsMissingAmbiguousStaleAndForeignResults(t *testing.T) {
	for _, mode := range []string{"missing", "duplicate", "foreign-goal", "foreign-plan", "foreign-revision", "wrong-snapshot", "no-producer", "missing-commit"} {
		t.Run(mode, func(t *testing.T) {
			f := newSourceFixture(t, M{"id": "code", "branch": "feature"}, M{"id": "build", "kind": "build", "dependsOn": []any{"code"}})
			commit := f.commit(t, "feature", S(f.repo.Data, "head"))
			run := f.result(t, "code", commit)
			switch mode {
			case "missing":
				run.Status = "failed"
			case "duplicate":
				duplicate := run
				duplicate.ID = NewID("run")
				f.put(t, duplicate)
			case "foreign-goal":
				run.GoalID = "other-goal"
			case "foreign-plan":
				run.Data["planId"] = "other-plan"
			case "foreign-revision":
				run.Data["requirementRevision"] = 2
			case "wrong-snapshot":
				data := copyM(Obj(run.Data["task"]))
				data["logicalId"] = "other"
				run.Data["task"] = data
			case "no-producer":
				changes, _ := f.s.Store.List(f.p.ID, "change", "")
				for _, c := range changes {
					c.Status = "prepared"
					f.put(t, c)
				}
			case "missing-commit":
				run.Data["result"] = M{"repositoryId": f.repo.ID, "commit": strings.Repeat("a", 40)}
			}
			f.put(t, run)
			if claimed := f.claim(t); claimed.Kind == "run" {
				t.Fatal("invalid source provenance executed", mode)
			}
			if task := f.task(t, "build"); task.Status != "blocked" || S(Obj(task.Data["sourceConflict"]), "code") == "" {
				t.Fatal("invalid source was not explainably blocked", task)
			}
		})
	}
}

func TestBoundCodeCompletionRequiresCurrentRunAndPreservesBase(t *testing.T) {
	for _, mode := range []string{"previous-run", "unrelated-history", "current-run"} {
		t.Run(mode, func(t *testing.T) {
			f := newSourceFixture(t, M{"id": "code", "branch": "feature"})
			run := f.claim(t)
			binding := assertSource(t, run, f.repo.ID, S(f.repo.Data, "head"))
			commit := ""
			if mode == "current-run" {
				r := must(t, f.s, f.worker, "repositories.commit", M{"id": f.repo.ID, "branch": "feature", "baseCommit": S(binding, "commit"), "expectedHead": S(binding, "expectedHead"), "runId": run.ID, "leaseToken": S(run.Data, "leaseToken"), "message": "Current execution", "files": M{"source.txt": "implementation"}})
				commit = S(r.Data, "lastCommit")
			} else {
				commit = f.commit(t, "outside", S(f.repo.Data, "head"))
				if mode == "unrelated-history" {
					tree, e := f.s.Git(f.repo, "rev-parse", commit+"^{tree}")
					if e != nil {
						t.Fatal(e)
					}
					commit, e = f.s.gitIdentity(f.repo, "commit-tree", tree, "-m", "Unrelated history")
					if e != nil {
						t.Fatal(e)
					}
				}
				producerRun := "run-previous"
				if mode == "unrelated-history" {
					producerRun = run.ID
				}
				f.put(t, resource("change", "Invalid completion", f.p.ID, f.g.ID, M{"repositoryId": f.repo.ID, "commit": commit, "authorId": f.worker.ID, "runId": producerRun}))
			}
			completed, e := f.s.Command(context.Background(), f.worker, "tasks.complete", M{"id": run.ID, "leaseToken": S(run.Data, "leaseToken"), "exitCode": 0, "result": M{"repositoryId": f.repo.ID, "commit": commit}})
			if mode == "current-run" {
				if e != nil || completed.Status != "succeeded" {
					t.Fatal("valid current-run source rejected", e, completed)
				}
			} else if e == nil {
				t.Fatal("invalid source result was accepted", mode)
			}
		})
	}
}

func TestSecurityCannotReportDifferentSourceThanItsLease(t *testing.T) {
	f := newSourceFixture(t, M{"id": "security", "kind": "security"})
	run := f.claim(t)
	assertSource(t, run, f.repo.ID, S(f.repo.Data, "head"))
	other := f.commit(t, "other", S(f.repo.Data, "head"))
	if _, e := f.s.Command(context.Background(), f.worker, "tasks.complete", M{"id": run.ID, "leaseToken": S(run.Data, "leaseToken"), "exitCode": 0, "result": M{"repositoryId": f.repo.ID, "commit": other}}); e == nil {
		t.Fatal("security task reported a different source than its lease")
	}
}

func TestRootCodeClaimCreatesRepositoryAndPinsActualDefaultRef(t *testing.T) {
	s, a, p := fixture(t)
	g := goal(t, s, a, p)
	must(t, s, a, "goals.approve", M{"id": g.ID})
	plan := must(t, s, a, "plans.propose", M{"goalId": g.ID, "summary": "New application", "tasks": []any{M{"id": "code", "title": "Create application", "kind": "code", "role": "developer", "runtime": "linux"}}})
	must(t, s, a, "plans.approve", M{"id": plan.ID})
	w := must(t, s, a, "workers.register", M{"projectId": p.ID, "title": "Developer", "roles": []any{"developer"}, "runtime": "linux", "capabilities": []any{"code"}})
	actor := Actor{ID: "worker:" + w.ID, Kind: "worker", WorkerID: w.ID, ProjectID: p.ID, Roles: []string{"developer"}}
	run := must(t, s, actor, "tasks.claim", M{})
	repos, e := s.Store.List(p.ID, "repository", "")
	if e != nil || len(repos) != 1 {
		t.Fatal("root claim did not create exactly one project repository", repos, e)
	}
	assertSource(t, run, repos[0].ID, S(repos[0].Data, "head"))
	// A later task reads the ref, not stale repository.Data.head metadata.
	f := newSourceFixture(t, M{"id": "build", "kind": "build"})
	old := S(f.repo.Data, "head")
	newHead := f.commit(t, "updated-main", old)
	if _, e = f.s.Git(f.repo, "update-ref", "refs/heads/main", newHead, old); e != nil {
		t.Fatal(e)
	}
	assertSource(t, f.claim(t), f.repo.ID, newHead)
}

func TestNearestSourceFrontierAndExplicitAncestorSelection(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(map[bool]string{false: "nearest", true: "explicit-ancestor"}[explicit], func(t *testing.T) {
			consumer := M{"id": "build", "kind": "build", "dependsOn": []any{"second"}}
			if explicit {
				consumer["sourceFrom"] = []any{"first"}
			}
			f := newSourceFixture(t, M{"id": "first", "branch": "first"}, M{"id": "second", "branch": "second", "dependsOn": []any{"first"}}, consumer)
			first := f.commit(t, "first", S(f.repo.Data, "head"))
			one := f.result(t, "first", first)
			second := f.commit(t, "second", first)
			two := f.result(t, "second", second)
			if explicit {
				assertSource(t, f.claim(t), f.repo.ID, first, one)
			} else {
				assertSource(t, f.claim(t), f.repo.ID, second, two)
			}
		})
	}
}

func TestBoundCodeRetryRejectsTargetMovementMutationAndSplit(t *testing.T) {
	for _, mode := range []string{"target-moved", "binding-mutated", "split"} {
		t.Run(mode, func(t *testing.T) {
			f := newSourceFixture(t, M{"id": "code", "branch": "feature"})
			run := f.claim(t)
			assertSource(t, run, f.repo.ID, S(f.repo.Data, "head"))
			must(t, f.s, f.worker, "tasks.complete", M{"id": run.ID, "leaseToken": S(run.Data, "leaseToken"), "exitCode": 1, "result": M{}})
			task := f.task(t, "code")
			must(t, f.s, f.a, "tasks.retry", M{"id": task.ID})
			if mode == "split" {
				if _, e := f.s.Command(context.Background(), f.a, "tasks.split", M{"id": task.ID, "reason": "Split failed implementation", "impact": "Keep source stable", "children": []any{M{"title": "UI", "instructions": "Implement UI"}, M{"title": "API", "instructions": "Implement API"}}}); e == nil {
					t.Fatal("bound task was split")
				}
				return
			}
			if mode == "target-moved" {
				f.commit(t, "feature", S(f.repo.Data, "head"))
			} else {
				task = f.task(t, "code")
				Obj(task.Data["sourceBinding"])["commit"] = strings.Repeat("a", 40)
				f.put(t, task)
			}
			if result := f.claim(t); result.Kind == "run" {
				t.Fatal("retry changed immutable code source", result)
			}
			task = f.task(t, "code")
			if task.Status != "blocked" || N(task.Data, "attempts") != 1 {
				t.Fatal("source conflict consumed an execution attempt", task)
			}
		})
	}
}

func TestActualBuildResultFeedsSecuritySource(t *testing.T) {
	f := newSourceFixture(t, M{"id": "build", "kind": "build", "target": "web"}, M{"id": "security", "kind": "security", "dependsOn": []any{"build"}})
	run := f.claim(t)
	binding := assertSource(t, run, f.repo.ID, S(f.repo.Data, "head"))
	artifact, e := f.s.Upload(context.Background(), f.worker, M{"goalId": f.g.ID, "repositoryId": f.repo.ID, "commit": S(binding, "commit"), "runId": run.ID, "leaseToken": S(run.Data, "leaseToken"), "target": "web", "type": "package", "name": "application.bin"}, bytes.NewBufferString("actual build bytes"))
	if e != nil {
		t.Fatal(e)
	}
	complete := must(t, f.s, f.worker, "tasks.complete", M{"id": run.ID, "leaseToken": S(run.Data, "leaseToken"), "exitCode": 0, "result": M{"artifactId": artifact.ID, "repositoryId": f.repo.ID, "commit": S(binding, "commit")}})
	assertSource(t, f.claim(t), f.repo.ID, S(binding, "commit"), complete)
}

func TestRootSourceCannotGuessAmongMultipleRepositories(t *testing.T) {
	f := newSourceFixture(t, M{"id": "code", "repositoryId": ""})
	must(t, f.s, f.a, "repositories.create", M{"projectId": f.p.ID, "title": "second"})
	if result := f.claim(t); result.Kind == "run" {
		t.Fatal("guessed a repository", result)
	}
	if task := f.task(t, "code"); task.Status != "blocked" || N(task.Data, "attempts") != 0 {
		t.Fatal("ambiguous repository was not blocked before execution", task)
	}
}

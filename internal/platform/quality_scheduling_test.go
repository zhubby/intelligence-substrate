package platform

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

type qualityFixture struct {
	s                                         *Service
	owner, quality, developer, verifier       Actor
	project, goal, plan, repository, pipeline Resource
	commit                                    string
}

func TestQualitySchedulingRejectsStaleFailedResultBeforeRepair(t *testing.T) {
	// A changed protected command must not authorize a repair from an obsolete test.
	f := newQualityFixture(t)
	must(t, f.s, f.quality, "quality.bind", f.input("protected"))
	f.build(t)
	run := must(t, f.s, f.verifier, "tasks.claim", M{})
	if err := f.s.Store.Update(context.Background(), func(tx *Tx) error {
		pipeline, err := tx.Get(f.pipeline.ID)
		if err != nil {
			return err
		}
		pipeline.Data["command"] = []any{"/bin/sh", "-c", "exit 8"}
		return tx.Put(pipeline)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Command(context.Background(), f.verifier, "tasks.complete", M{"id": run.ID, "leaseToken": S(run.Data, "leaseToken"), "exitCode": 7, "result": M{}}); err == nil {
		t.Fatal("obsolete failed result authorized a defect/repair")
	}
	defects, _ := f.s.Store.List(f.project.ID, "defect", f.goal.ID)
	if len(defects) != 0 {
		t.Fatal("obsolete policy created defects", defects)
	}
}

func TestQualitySchedulingOnlySuccessfulBuildsAreEligible(t *testing.T) {
	for _, state := range []string{"human-upload", "in-flight", "failed"} {
		t.Run(state, func(t *testing.T) {
			f := newQualityFixture(t)
			must(t, f.s, f.quality, "quality.bind", f.input(state))
			var run Resource
			actor := f.owner
			meta := M{"goalId": f.goal.ID, "repositoryId": f.repository.ID, "commit": f.commit, "target": "web", "name": "candidate.html", "type": "package"}
			if state != "human-upload" {
				run = must(t, f.s, f.developer, "tasks.claim", M{})
				actor = f.developer
				meta["runId"] = run.ID
				meta["leaseToken"] = S(run.Data, "leaseToken")
			}
			artifact, err := f.s.Upload(context.Background(), actor, meta, bytes.NewBufferString("<h1>unconfirmed build</h1>"))
			if err != nil {
				t.Fatal(err)
			}
			if state == "failed" {
				must(t, f.s, f.developer, "tasks.complete", M{"id": run.ID, "leaseToken": S(run.Data, "leaseToken"), "exitCode": 9, "result": M{"artifactId": artifact.ID, "commit": f.commit, "repositoryId": f.repository.ID}})
			}
			idle := must(t, f.s, f.verifier, "tasks.claim", M{})
			dispatches, _ := f.s.Store.List(f.project.ID, "quality_dispatch", f.goal.ID)
			if idle.Kind != "worker" || len(dispatches) != 0 {
				t.Fatal("unconfirmed artifact got automatic QA", idle, dispatches)
			}
		})
	}
}

func TestQualitySchedulingDriftAndBudgetRemainVisible(t *testing.T) {
	for _, mode := range []string{"drift-before-scheduling", "drift-before-claim", "insufficient-budget"} {
		t.Run(mode, func(t *testing.T) {
			f := newQualityFixture(t)
			must(t, f.s, f.quality, "quality.bind", f.input(mode))
			f.build(t)
			if mode == "drift-before-claim" {
				if err := f.s.Store.Update(context.Background(), func(tx *Tx) error {
					w, err := tx.Get(f.verifier.WorkerID)
					if err != nil {
						return err
					}
					return f.s.scheduleQuality(tx, f.verifier, w)
				}); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.s.Store.Update(context.Background(), func(tx *Tx) error {
				if mode == "insufficient-budget" {
					g, err := tx.Get(f.goal.ID)
					if err != nil {
						return err
					}
					g.Data["spentCents"] = 9999
					return tx.Put(g)
				}
				pipeline, err := tx.Get(f.pipeline.ID)
				if err != nil {
					return err
				}
				pipeline.Data["command"] = []any{"true"}
				return tx.Put(pipeline)
			}); err != nil {
				t.Fatal(err)
			}
			if idle := must(t, f.s, f.verifier, "tasks.claim", M{}); idle.Kind != "worker" {
				t.Fatal("invalid or unbudgeted task ran", idle)
			}
			dispatches, _ := f.s.Store.List(f.project.ID, "quality_dispatch", f.goal.ID)
			if len(dispatches) != 1 {
				t.Fatal("missing durable blocked decision", dispatches)
			}
			if mode == "drift-before-scheduling" {
				if dispatches[0].Status != "blocked" || S(dispatches[0].Data, "reason") == "" || S(dispatches[0].Data, "taskId") != "" {
					t.Fatal("unsafe policy was dispatched", dispatches)
				}
			} else {
				task, err := f.s.Store.Get(S(dispatches[0].Data, "taskId"))
				if err != nil || task.Status != "blocked" || S(task.Data, "reason") == "" {
					t.Fatal("blocked task not explained", task, err)
				}
			}
		})
	}
}

func TestQualitySchedulingBaselineAndPlanInvalidatePolicies(t *testing.T) {
	for _, mode := range []string{"baseline", "plan"} {
		t.Run(mode, func(t *testing.T) {
			f := newQualityFixture(t)
			policy := must(t, f.s, f.quality, "quality.bind", f.input(mode))
			f.build(t)
			if mode == "baseline" {
				must(t, f.s, f.owner, "goals.revise", M{"id": f.goal.ID, "reason": "Changed acceptance baseline", "outcome": "Changed business outcome"})
			} else {
				next := must(t, f.s, f.owner, "plans.propose", M{"goalId": f.goal.ID, "summary": "Reviewed replacement plan", "tasks": []any{M{"id": "analysis", "title": "Review next change", "role": "developer", "kind": "analysis", "runtime": "linux"}}})
				must(t, f.s, f.owner, "plans.approve", M{"id": next.ID})
			}
			stored, _ := f.s.Store.Get(policy.ID)
			if stored.Status != "stale" {
				t.Fatal("changed approval retained active quality policy", stored)
			}
			if idle := must(t, f.s, f.verifier, "tasks.claim", M{}); idle.Kind != "worker" {
				t.Fatal("old policy scheduled after version change", idle)
			}
		})
	}
}

func TestQualitySchedulingOrdinaryEvidenceCannotReplaceBoundPolicy(t *testing.T) {
	f := newQualityFixture(t)
	_, artifact, _ := f.build(t)
	// An ordinary human-started run may pass but has no policy provenance.
	must(t, f.s, f.quality, "pipelines.run", M{"pipelineId": f.pipeline.ID, "artifactId": artifact.ID, "criteria": []any{"ticket"}})
	run := must(t, f.s, f.verifier, "tasks.claim", M{})
	must(t, f.s, f.verifier, "tasks.complete", M{"id": run.ID, "leaseToken": S(run.Data, "leaseToken"), "exitCode": 0, "result": M{}})
	must(t, f.s, f.quality, "quality.bind", f.input("required"))
	if _, err := f.s.Command(context.Background(), f.owner, "releases.create", M{"goalId": f.goal.ID, "version": "1.0.0", "commit": f.commit, "artifactIds": []any{artifact.ID}}); err == nil || !strings.Contains(err.Error(), "准确自动质量绑定") {
		t.Fatal("ordinary evidence bypassed required bound policy", err)
	}
}

func TestQualitySchedulingExternalPlansCannotForgeProvenance(t *testing.T) {
	f := newQualityFixture(t)
	for _, field := range []string{"qualityPolicyId", "qualityPolicyDigest", "qualityPipelineDigest", "qualityDispatchId"} {
		task := M{"id": "forged", "title": "Pretend protected QA", "kind": "analysis", "role": "developer", "runtime": "linux", field: "fabricated"}
		if _, err := f.s.Command(context.Background(), f.owner, "plans.propose", M{"goalId": f.goal.ID, "summary": "Forge control-plane policy", "tasks": []any{task}}); err == nil || !strings.Contains(err.Error(), "保留字段") {
			t.Fatal("plan forged quality provenance", field, err)
		}
	}
}

func TestQualitySchedulingAgentCannotReorderProtectedVerification(t *testing.T) {
	// Developer plan adjustments cannot change independent quality scheduling.
	f := newQualityFixture(t)
	must(t, f.s, f.quality, "quality.bind", f.input("first"))
	second := must(t, f.s, f.quality, "pipelines.create", M{"projectId": f.project.ID, "title": "Additional independent check", "runtime": "linux", "method": "automated", "verificationEnvironment": "browser", "command": []any{"true"}})
	in := f.input("second")
	in["pipelineId"] = second.ID
	must(t, f.s, f.quality, "quality.bind", in)
	f.build(t)
	must(t, f.s, f.verifier, "tasks.claim", M{})
	tasks, _ := f.s.Store.List(f.project.ID, "task", f.goal.ID)
	for _, task := range tasks {
		if task.Status == "pending" && S(task.Data, "qualityPolicyId") != "" {
			_, err := f.s.Command(context.Background(), Actor{ID: "developer-agent", Kind: "agent", ProjectID: f.project.ID, Roles: []string{"developer"}}, "plans.reorder", M{"goalId": f.goal.ID, "reason": "Change protected QA order", "impact": "Delay independent verification", "tasks": []any{M{"id": S(task.Data, "logicalId"), "dependsOn": []any{"build"}}}})
			if err == nil || !strings.Contains(err.Error(), "独立质量") {
				t.Fatal("protected dependency change lacked its independent-quality rejection", err)
			}
			return
		}
	}
	t.Fatal("no second protected verification was queued")
}

func TestQualitySchedulingDeveloperCanReorderOwnPendingWork(t *testing.T) {
	f := newQualityFixture(t)
	work := append([]any{}, Items(f.plan.Data["tasks"])...)
	work = append(work, M{"id": "next", "title": "Review next approved step", "kind": "analysis", "role": "analysis", "runtime": "linux", "dependsOn": []any{"build"}, "budgetCents": 10})
	f.plan = must(t, f.s, f.owner, "plans.propose", M{"goalId": f.goal.ID, "summary": "Build and review subsequent work", "tasks": work})
	f.plan = must(t, f.s, f.owner, "plans.approve", M{"id": f.plan.ID})
	f.goal, _ = f.s.Store.Get(f.goal.ID)
	must(t, f.s, f.quality, "quality.bind", f.input("protected"))
	f.build(t)
	run := must(t, f.s, f.verifier, "tasks.claim", M{})
	before, _ := f.s.Store.Get(S(run.Data, "taskId"))
	change, err := f.s.Command(context.Background(), Actor{ID: "developer-agent", Kind: "agent", ProjectID: f.project.ID, Roles: []string{"developer"}}, "plans.reorder", M{"goalId": f.goal.ID, "reason": "The approved analysis can run independently", "impact": "No change to quality criteria, permissions or budget", "tasks": []any{M{"id": "next", "dependsOn": []any{}}}})
	if err != nil || change.Kind != "plan_change" {
		t.Fatal("independent QA broke ordinary approved plan adjustment", change, err)
	}
	after, _ := f.s.Store.Get(before.ID)
	if Hash(before.Data) != Hash(after.Data) {
		t.Fatal("ordinary adjustment changed protected QA data", before, after)
	}
}

func TestQualitySchedulingWorkerAuthorityIsRecheckedOnFailure(t *testing.T) {
	for _, mode := range []string{"trust-removed", "runtime-changed", "capability-removed"} {
		t.Run(mode, func(t *testing.T) {
			f := newQualityFixture(t)
			must(t, f.s, f.quality, "quality.bind", f.input(mode))
			f.build(t)
			run := must(t, f.s, f.verifier, "tasks.claim", M{})
			update := M{"id": f.verifier.WorkerID, "projectId": f.project.ID, "title": "Updated verification authority", "roles": []any{"quality"}, "capabilities": []any{"test"}, "runtime": "linux", "trustedQuality": true}
			if mode == "trust-removed" {
				update["trustedQuality"] = false
			}
			if mode == "runtime-changed" {
				update["runtime"] = "darwin"
			}
			if mode == "capability-removed" {
				update["capabilities"] = []any{"security"}
			}
			must(t, f.s, f.owner, "workers.update", update)
			if _, err := f.s.Command(context.Background(), f.verifier, "tasks.complete", M{"id": run.ID, "leaseToken": S(run.Data, "leaseToken"), "exitCode": 7, "result": M{}}); err == nil {
				t.Fatal("changed worker authority authorized a defect/repair", mode)
			}
			defects, _ := f.s.Store.List(f.project.ID, "defect", f.goal.ID)
			if len(defects) != 0 {
				t.Fatal("untrusted failed result created repair", defects)
			}
		})
	}
}

func newQualityFixture(t *testing.T) qualityFixture {
	t.Helper()
	s, owner, project := fixture(t)
	return setupQualityFixture(t, s, owner, project)
}

func setupQualityFixture(t *testing.T, s *Service, owner Actor, project Resource) qualityFixture {
	t.Helper()
	g := goal(t, s, owner, project)
	must(t, s, owner, "goals.approve", M{"id": g.ID})
	repository := must(t, s, owner, "repositories.create", M{"projectId": project.ID, "title": "quality-source"})
	repository = must(t, s, owner, "repositories.commit", M{"id": repository.ID, "branch": "feature", "message": "Actual application source", "files": M{"index.html": "<h1>actual application</h1>"}})
	commit := S(repository.Data, "lastCommit")
	plan := must(t, s, owner, "plans.propose", M{"goalId": g.ID, "summary": "Build the approved application", "tasks": []any{M{"id": "build", "title": "Build application", "role": "developer", "runtime": "linux", "kind": "build", "repositoryId": repository.ID, "commit": commit, "target": "web", "command": []any{"/bin/sh", "-c", "cp index.html package.html"}, "outputPath": "package.html", "budgetCents": 100, "timeoutSeconds": 30}}})
	plan = must(t, s, owner, "plans.approve", M{"id": plan.ID})
	g, _ = s.Store.Get(g.ID)
	quality := Actor{ID: "independent-quality-manager", Kind: "human", Admin: true}
	pipeline := must(t, s, quality, "pipelines.create", M{"projectId": project.ID, "title": "Verify real package", "command": []any{"/bin/sh", "-c", "test -s \"$IS_ARTIFACT_PATH\""}, "runtime": "linux", "method": "automated", "verificationEnvironment": "browser"})
	w := must(t, s, owner, "workers.register", M{"projectId": project.ID, "title": "Build worker", "roles": []any{"developer"}, "runtime": "linux", "capabilities": []any{"build"}})
	q := must(t, s, owner, "workers.register", M{"projectId": project.ID, "title": "Independent quality worker", "roles": []any{"quality"}, "runtime": "linux", "capabilities": []any{"test"}, "trustedQuality": true})
	return qualityFixture{s: s, owner: owner, quality: quality, developer: Actor{ID: "worker:" + w.ID, Kind: "worker", ProjectID: project.ID, WorkerID: w.ID, Roles: []string{"developer"}}, verifier: Actor{ID: "worker:" + q.ID, Kind: "worker", ProjectID: project.ID, WorkerID: q.ID, Roles: []string{"quality"}}, project: project, goal: g, plan: plan, repository: repository, pipeline: pipeline, commit: commit}
}

func (f qualityFixture) input(key string) M {
	return M{"goalId": f.goal.ID, "expectedRequirementRevision": f.goal.Revision, "expectedPlanId": f.plan.ID, "pipelineId": f.pipeline.ID, "target": "web", "criteria": []any{"ticket"}, "budgetCents": 100, "key": key}
}

func (f qualityFixture) build(t *testing.T) (Resource, Resource, M) {
	t.Helper()
	run := must(t, f.s, f.developer, "tasks.claim", M{})
	if run.Kind != "run" {
		t.Fatal("build not claimed", run)
	}
	artifact, err := f.s.Upload(context.Background(), f.developer, M{"goalId": f.goal.ID, "repositoryId": f.repository.ID, "commit": f.commit, "target": "web", "name": "application.html", "type": "package", "runId": run.ID, "leaseToken": S(run.Data, "leaseToken")}, bytes.NewBufferString("<h1>actual application</h1>"))
	if err != nil {
		t.Fatal(err)
	}
	input := M{"id": run.ID, "leaseToken": S(run.Data, "leaseToken"), "exitCode": 0, "result": M{"artifactId": artifact.ID, "repositoryId": f.repository.ID, "commit": f.commit}}
	return must(t, f.s, f.developer, "tasks.complete", input), artifact, input
}

func TestQualitySchedulingBuildToIndependentEvidence(t *testing.T) {
	// Removing automatic scheduling would leave the verifier idle after the build.
	f := newQualityFixture(t)
	policy := must(t, f.s, f.quality, "quality.bind", f.input("initial-policy"))
	replayed := must(t, f.s, f.quality, "quality.bind", f.input("initial-policy"))
	if policy.ID != replayed.ID || policy.Status != "active" {
		t.Fatal("binding replay changed policy", policy, replayed)
	}
	_, artifact, completion := f.build(t)
	must(t, f.s, f.developer, "tasks.complete", completion)
	run := must(t, f.s, f.verifier, "tasks.claim", M{})
	task := Obj(run.Data["task"])
	if run.Kind != "run" || S(task, "qualityPolicyId") != policy.ID || S(task, "artifactId") != artifact.ID || S(task, "qualityDispatchId") == "" {
		t.Fatal("successful build did not get its protected verification", run)
	}
	finished := must(t, f.s, f.verifier, "tasks.complete", M{"id": run.ID, "leaseToken": S(run.Data, "leaseToken"), "exitCode": 0, "result": M{}})
	evidence, err := f.s.Store.Get(S(finished.Data, "evidenceId"))
	if err != nil || evidence.Status != "passed" || S(evidence.Data, "qualityPolicyId") != policy.ID || S(evidence.Data, "artifactId") != artifact.ID || S(evidence.Data, "actorId") == S(artifact.Data, "producerId") {
		t.Fatal("independent evidence lost exact policy provenance", evidence, err)
	}
	for range 3 {
		if idle := must(t, f.s, f.verifier, "tasks.claim", M{}); idle.Kind != "worker" {
			t.Fatal("repeat polling duplicated QA", idle)
		}
	}
	dispatches, _ := f.s.Store.List(f.project.ID, "quality_dispatch", f.goal.ID)
	if len(dispatches) != 1 || S(dispatches[0].Data, "taskId") != S(run.Data, "taskId") {
		t.Fatal("dispatch was not durable and unique", dispatches)
	}
	if _, err = f.s.Command(context.Background(), f.owner, "releases.create", M{"goalId": f.goal.ID, "version": "1.0.0", "commit": f.commit, "artifactIds": []any{artifact.ID}}); err != nil {
		t.Fatal("exact independent policy evidence could not reach candidate", err)
	}
}

func TestQualitySchedulingBindingRejectsUnsafeInputsAndCallers(t *testing.T) {
	f := newQualityFixture(t)
	for _, actor := range []Actor{{ID: "quality-agent", Kind: "agent", ProjectID: f.project.ID, Roles: []string{"quality"}}, f.developer, f.verifier, {ID: "outsider", Kind: "human"}} {
		if _, err := f.s.Command(context.Background(), actor, "quality.bind", f.input("unauthorized")); err == nil {
			t.Fatal("non-human or unauthorized identity bound quality policy", actor)
		}
	}
	for name, change := range map[string]M{
		"old-revision":       {"expectedRequirementRevision": f.goal.Revision + 1},
		"wrong-plan":         {"expectedPlanId": "plan_other"},
		"bad-target":         {"target": "windows"},
		"duplicate-criteria": {"criteria": []any{"ticket", "ticket"}},
		"unknown-criterion":  {"criteria": []any{"unknown"}},
		"null-criteria":      {"criteria": nil},
		"fraction-budget":    {"budgetCents": 0.5},
		"negative-budget":    {"budgetCents": -1},
		"large-budget":       {"budgetCents": 1000001},
		"missing-key":        {"key": ""},
		"injected-command":   {"command": []any{"true"}},
	} {
		t.Run(name, func(t *testing.T) {
			in := f.input(name)
			for k, v := range change {
				in[k] = v
			}
			if _, err := f.s.Command(context.Background(), f.quality, "quality.bind", in); err == nil {
				t.Fatal("unsafe binding accepted", in)
			}
		})
	}
	policy := must(t, f.s, f.quality, "quality.bind", f.input("replay"))
	changed := f.input("replay")
	changed["budgetCents"] = 200
	if _, err := f.s.Command(context.Background(), f.quality, "quality.bind", changed); err == nil {
		t.Fatal("same key changed policy input")
	}
	if _, err := f.s.Command(context.Background(), f.quality, "quality.bind", f.input("duplicate")); err == nil {
		t.Fatal("equivalent active policy created twice", policy)
	}
}

func TestQualitySchedulingRevokeRejectsLateEvidenceAndOldCandidate(t *testing.T) {
	f := newQualityFixture(t)
	policy := must(t, f.s, f.quality, "quality.bind", f.input("first"))
	_, artifact, _ := f.build(t)
	run := must(t, f.s, f.verifier, "tasks.claim", M{})
	if run.Kind != "run" {
		t.Fatal(run)
	}
	taskID := S(run.Data, "taskId")
	if _, err := f.s.Command(context.Background(), Actor{ID: "developer-human", Kind: "human", ProjectID: f.project.ID, Roles: []string{"developer"}}, "tasks.cancel", M{"id": taskID}); err == nil {
		t.Fatal("developer/architect cancellation bypassed quality ownership")
	}
	must(t, f.s, f.quality, "quality.revoke", M{"id": policy.ID, "key": "revoke", "reason": "Protected verification scope needs review"})
	if _, err := f.s.Command(context.Background(), f.verifier, "tasks.complete", M{"id": run.ID, "leaseToken": S(run.Data, "leaseToken"), "exitCode": 0, "result": M{}}); err == nil {
		t.Fatal("revoked policy accepted late verification")
	}
	evidence, _ := f.s.Store.List(f.project.ID, "evidence", f.goal.ID)
	if len(evidence) != 0 {
		t.Fatal("revoked verification wrote evidence", evidence)
	}
	next := must(t, f.s, f.quality, "quality.bind", f.input("replacement"))
	retry := must(t, f.s, f.verifier, "tasks.claim", M{})
	if retry.Kind != "run" || S(Obj(retry.Data["task"]), "qualityPolicyId") != next.ID || S(Obj(retry.Data["task"]), "artifactId") != artifact.ID {
		t.Fatal("replacement policy did not schedule existing successful build", retry)
	}
	must(t, f.s, f.verifier, "tasks.complete", M{"id": retry.ID, "leaseToken": S(retry.Data, "leaseToken"), "exitCode": 0, "result": M{}})
	candidate := must(t, f.s, f.owner, "releases.create", M{"goalId": f.goal.ID, "version": "1.0.0", "commit": f.commit, "artifactIds": []any{artifact.ID}})
	must(t, f.s, f.quality, "quality.revoke", M{"id": next.ID, "key": "revoke-next", "reason": "New quality decision requires a new candidate"})
	if _, err := f.s.Command(context.Background(), Actor{ID: "release-officer", Kind: "human", Admin: true}, "releases.approve", M{"id": candidate.ID}); err == nil {
		t.Fatal("policy change retained old candidate approval binding")
	}
}

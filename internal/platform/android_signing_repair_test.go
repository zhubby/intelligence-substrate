package platform

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

// Control-plane fixture bytes are not Android signatures. Native acceptance
// separately uses Flutter, apksigner and an installed release APK.
type signingRepairFixture struct {
	androidSigningFixture
	pipeline, policy Resource
}

func setupSigningRepairFixture(t *testing.T, s *Service, owner Actor, project Resource) *signingRepairFixture {
	return setupSigningRepairFixtureTarget(t, s, owner, project, "android-huawei")
}

func setupSigningRepairFixtureTarget(t *testing.T, s *Service, owner Actor, project Resource, target string) *signingRepairFixture {
	mode := "android-apk"
	if target == "android-google-play" {
		mode = "android-aab"
	}
	t.Helper()
	f := &signingRepairFixture{androidSigningFixture: setupAndroidSigningFixtureTarget(t, s, owner, project, target)}
	f.plan = must(t, s, owner, "plans.propose", M{"goalId": f.goal.ID, "summary": "Approved source, unsigned build and fixed signing recipes", "tasks": []any{
		M{"id": "code", "title": "Implement Android source", "kind": "code", "role": "developer", "runtime": "linux", "repositoryId": f.repository.ID, "commit": f.commit, "branch": "implementation", "command": []any{"true"}, "budgetCents": 100},
		M{"id": "unsigned", "title": "Unsigned release", "kind": "build", "role": "developer", "runtime": "linux", "repositoryId": f.repository.ID, "dependsOn": []any{"code"}, "target": target, "artifactStage": "unsigned", "command": []any{"true"}, "outputPath": "unsigned.apk", "budgetCents": 100},
		M{"id": "sign", "title": "Controlled APK signing", "kind": "build", "role": "release", "runtime": "linux", "dependsOn": []any{"unsigned"}, "target": target, "signingMode": mode, "inputArtifactFrom": "unsigned", "expectedSigner": strings.Repeat("a", 64), "budgetCents": 100},
	}})
	must(t, s, owner, "plans.approve", M{"id": f.plan.ID})
	w := must(t, s, owner, "workers.register", M{"projectId": project.ID, "title": "Source/build node", "runtime": "linux", "roles": []any{"developer"}, "capabilities": []any{"code", "build"}})
	f.builder = Actor{ID: "worker:" + w.ID, Kind: "worker", WorkerID: w.ID, ProjectID: project.ID, Roles: []string{"developer"}}
	f.pipeline = must(t, s, owner, "pipelines.create", M{"projectId": project.ID, "title": "Protected final package QA", "runtime": "linux", "method": "simulator", "verificationEnvironment": "simulator", "command": []any{"true"}})
	f.policy = must(t, s, owner, "quality.bind", M{"goalId": f.goal.ID, "expectedRequirementRevision": f.goal.Revision, "expectedPlanId": f.plan.ID, "pipelineId": f.pipeline.ID, "target": target, "criteria": []any{"submit"}, "budgetCents": 100, "key": "signed-repair-policy"})
	return f
}

func newSigningRepairFixture(t *testing.T) *signingRepairFixture {
	s, owner, project := fixture(t)
	return setupSigningRepairFixture(t, s, owner, project)
}

func (f *signingRepairFixture) code(t *testing.T) Resource {
	t.Helper()
	run := must(t, f.s, f.builder, "tasks.claim", M{})
	if run.Kind != "run" || S(Obj(run.Data["task"]), "kind") != "code" {
		t.Fatal("code/repair not claimable", run)
	}
	return f.finishCode(t, run)
}

func (f *signingRepairFixture) finishCode(t *testing.T, run Resource) Resource {
	t.Helper()
	task := Obj(run.Data["task"])
	b := Obj(task["sourceBinding"])
	repo := must(t, f.s, f.builder, "repositories.commit", M{"id": f.repository.ID, "branch": S(task, "branch"), "baseCommit": S(b, "commit"), "expectedHead": S(b, "expectedHead"), "runId": run.ID, "leaseToken": S(run.Data, "leaseToken"), "message": "Actual implementation or repair", "files": M{"source.txt": "source from " + run.ID + "\n"}})
	f.commit = S(repo.Data, "lastCommit")
	return must(t, f.s, f.builder, "tasks.complete", M{"id": run.ID, "leaseToken": S(run.Data, "leaseToken"), "exitCode": 0, "result": M{"repositoryId": f.repository.ID, "commit": f.commit, "branch": S(task, "branch")}})
}

func (f *signingRepairFixture) unsignedBuild(t *testing.T) Resource {
	t.Helper()
	run := must(t, f.s, f.builder, "tasks.claim", M{})
	if run.Kind != "run" || S(Obj(run.Data["task"]), "artifactStage") != "unsigned" {
		t.Fatal("unsigned rebuild not claimable", run)
	}
	artifact, err := f.s.Upload(context.Background(), f.builder, M{"goalId": f.goal.ID, "repositoryId": f.repository.ID, "commit": f.commit, "target": f.target, "type": "unsigned-package", "name": "unsigned.apk", "runId": run.ID, "leaseToken": S(run.Data, "leaseToken")}, bytes.NewBufferString("unsigned "+f.commit))
	if err != nil {
		t.Fatal(err)
	}
	f.unsigned = artifact
	must(t, f.s, f.builder, "tasks.complete", M{"id": run.ID, "leaseToken": S(run.Data, "leaseToken"), "exitCode": 0, "result": M{"artifactId": artifact.ID, "repositoryId": f.repository.ID, "commit": f.commit}})
	return run
}

func (f *signingRepairFixture) sign(t *testing.T) (Resource, Resource) {
	t.Helper()
	run := must(t, f.s, f.signer, "tasks.claim", M{})
	if run.Kind != "run" || !androidSigningTask(Resource{Data: Obj(run.Data["task"])}) {
		t.Fatal("fresh signing not claimable", run)
	}
	artifact, err := f.s.Upload(context.Background(), f.signer, M{"goalId": f.goal.ID, "repositoryId": f.repository.ID, "commit": f.commit, "target": f.target, "type": "package", "name": "signed.apk", "runId": run.ID, "leaseToken": S(run.Data, "leaseToken")}, bytes.NewBufferString("signed "+f.commit))
	if err != nil {
		t.Fatal(err)
	}
	input := f.completion(run, artifact)
	must(t, f.s, f.signer, "tasks.complete", input)
	must(t, f.s, f.signer, "tasks.complete", input)
	return run, artifact
}

func (f *signingRepairFixture) fail(t *testing.T) Resource {
	t.Helper()
	run := must(t, f.s, f.quality, "tasks.claim", M{})
	if run.Kind != "run" {
		t.Fatal("protected signed QA not claimable", run)
	}
	input := M{"id": run.ID, "leaseToken": S(run.Data, "leaseToken"), "exitCode": 7, "log": "Actual protected check rejected final package", "result": M{}}
	must(t, f.s, f.quality, "tasks.complete", input)
	must(t, f.s, f.quality, "tasks.complete", input)
	defects, _ := f.s.Store.List(f.project.ID, "defect", f.goal.ID)
	if len(defects) != 1 {
		t.Fatal("expected one persistent defect", defects)
	}
	return defects[0]
}

func (f *signingRepairFixture) rejected(t *testing.T) (Resource, Resource, Resource) {
	t.Helper()
	f.code(t)
	f.unsignedBuild(t)
	sign, artifact := f.sign(t)
	return f.fail(t), sign, artifact
}

func TestAndroidSigningRepairCreatesFreshUnsignedSignAndProtectedRecheck(t *testing.T) {
	testSigningRepairFreshChain(t, newSigningRepairFixture(t))
}

func testSigningRepairFreshChain(t *testing.T, f *signingRepairFixture) {
	defect, oldSign, oldArtifact := f.rejected(t)
	oldCommit := f.commit
	if defect.Status != "repairing" {
		t.Fatal("signed failure did not authorize controlled code repair", defect)
	}
	f.code(t)
	defect, _ = f.s.Store.Get(defect.ID)
	if defect.Status != "awaiting_verification" {
		t.Fatal("signed repair must create fresh unsigned/sign/recheck chain", defect.Status, S(defect.Data, "reason"))
	}
	if f.commit == oldCommit || len(Items(defect.Data["androidSigningRepairs"])) != 1 {
		t.Fatal("missing durable fresh repair lineage", defect)
	}
	f.unsignedBuild(t)
	if idle := must(t, f.s, f.quality, "tasks.claim", M{}); idle.Kind != "worker" {
		t.Fatal("unsigned repair package got QA", idle)
	}
	newSign, final := f.sign(t)
	oldBinding, newBinding := Obj(Obj(oldSign.Data["task"])["signingInput"]), Obj(Obj(newSign.Data["task"])["signingInput"])
	if S(newBinding, "operationKey") == S(oldBinding, "operationKey") || S(newBinding, "artifactId") == S(oldBinding, "artifactId") || final.ID == oldArtifact.ID || S(newBinding, "expectedSigner") != S(oldBinding, "expectedSigner") || S(newBinding, "commit") != f.commit {
		t.Fatal("new signing must preserve authority and use a fresh exact input", newBinding)
	}
	qa := must(t, f.s, f.quality, "tasks.claim", M{})
	if S(Obj(qa.Data["task"]), "artifactId") != final.ID || S(Obj(qa.Data["task"]), "qualityPolicyId") != f.policy.ID {
		t.Fatal("recheck is not for new exact package/original policy", qa)
	}
	must(t, f.s, f.quality, "tasks.complete", M{"id": qa.ID, "leaseToken": S(qa.Data, "leaseToken"), "exitCode": 0, "result": M{}})
	defect, _ = f.s.Store.Get(defect.ID)
	if defect.Status != "closed" || S(defect.Data, "verifiedRunId") != qa.ID {
		t.Fatal("reverified repair not closed", defect)
	}
	dispatches, _ := f.s.Store.List(f.project.ID, "quality_dispatch", f.goal.ID)
	evidence, _ := f.s.Store.List(f.project.ID, "evidence", f.goal.ID)
	if len(dispatches) != 2 || len(evidence) != 1 || S(evidence[0].Data, "artifactId") != final.ID {
		t.Fatal("unexpected dispatch/evidence count", len(dispatches), len(evidence))
	}
	if idle := must(t, f.s, f.quality, "tasks.claim", M{}); idle.Kind != "worker" {
		t.Fatal("recheck duplicated", idle)
	}
}

func TestAndroidSigningRepairTwoRoundsUseLatestFailureAndStop(t *testing.T) {
	f := newSigningRepairFixture(t)
	defect, _, previous := f.rejected(t)
	for round := 1; round <= 2; round++ {
		f.code(t)
		f.unsignedBuild(t)
		_, signed := f.sign(t)
		defect = f.fail(t)
		if S(defect.Data, "artifactId") != signed.ID || signed.ID == previous.ID || int(N(defect.Data, "repairAttempts")) != round+map[bool]int{true: 1, false: 0}[round < 2] {
			t.Fatal("repair round lost latest source/artifact", round, defect)
		}
		previous = signed
	}
	if defect.Status != "blocked" || !strings.Contains(S(defect.Data, "reason"), "两次") || len(Items(defect.Data["androidSigningRepairs"])) != 2 {
		t.Fatal("unbounded or unrecorded signed repair", defect)
	}
	if idle := must(t, f.s, f.builder, "tasks.claim", M{}); idle.Kind != "worker" {
		t.Fatal("third automatic repair dispatched", idle)
	}
}

func TestAndroidSigningRepairPlanCannotForgeContext(t *testing.T) {
	if _, err := validateTasks([]any{M{"id": "forged", "title": "Forged repair", "kind": "analysis", "role": "developer", "runtime": "linux", "signingRepairId": "repair_forged"}}); err == nil || !strings.Contains(err.Error(), "保留字段") {
		t.Fatal("external plan forged signing repair authority", err)
	}
}

func TestAndroidSigningRepairRevokedBeforeCodeClaimBlocksWithoutAttempt(t *testing.T) {
	f := newSigningRepairFixture(t)
	defect, _, _ := f.rejected(t)
	must(t, f.s, f.owner, "quality.revoke", M{"id": f.policy.ID, "reason": "Withdraw automatic repair authority", "key": "revoke-signed-repair"})
	if idle := must(t, f.s, f.builder, "tasks.claim", M{}); idle.Kind != "worker" {
		t.Fatal("revoked policy authorized code repair", idle)
	}
	task, _ := f.s.Store.Get(S(defect.Data, "repairTaskId"))
	if task.Status != "blocked" || N(task.Data, "attempts") != 0 || S(task.Data, "reason") == "" {
		t.Fatal("revocation must block before budget/attempt", task)
	}
}

func TestAndroidSigningRepairUnboundFailureCannotAuthorizeSigning(t *testing.T) {
	f := newSigningRepairFixture(t)
	f.code(t)
	f.unsignedBuild(t)
	_, artifact := f.sign(t)
	must(t, f.s, f.owner, "quality.revoke", M{"id": f.policy.ID, "reason": "Use a one-off manual check", "key": "manual-only"})
	must(t, f.s, f.owner, "pipelines.run", M{"pipelineId": f.pipeline.ID, "artifactId": artifact.ID, "criteria": []any{"submit"}})
	defect := f.fail(t)
	if defect.Status != "blocked" || !strings.Contains(S(defect.Data, "reason"), "自动质量绑定") || len(Items(defect.Data["androidSigningRepairs"])) != 0 {
		t.Fatal("unbound failure expanded signing authorization", defect)
	}
	if idle := must(t, f.s, f.builder, "tasks.claim", M{}); idle.Kind != "worker" {
		t.Fatal("unbound check created automatic source repair", idle)
	}
}

func (f *signingRepairFixture) drift(t *testing.T, defect Resource, taskID, mode string) {
	t.Helper()
	err := f.s.Store.Update(context.Background(), func(tx *Tx) error {
		d, _ := tx.Get(defect.ID)
		record := Obj(Items(d.Data["androidSigningRepairs"])[0])
		id, field, value := taskID, "command", any([]any{"unapproved"})
		switch mode {
		case "policy":
			id, field, value = f.policy.ID, "binding", "changed"
		case "pipeline":
			id = f.pipeline.ID
		case "plan":
			id, field, value = f.plan.ID, "tasks", []any{}
		case "root-build":
			id = S(record, "rootUnsignedTaskId")
		case "certificate":
			id, field, value = S(record, "rootSigningTaskId"), "expectedSigner", strings.Repeat("d", 64)
		case "failed-artifact":
			id, field, value = S(record, "failedArtifactId"), "commit", strings.Repeat("d", 40)
		case "failed-run":
			run, _ := tx.Get(S(record, "failedRunId"))
			run.Status = "succeeded"
			return tx.Put(run)
		case "record":
			record["expectedSigner"] = strings.Repeat("d", 64)
			return tx.Put(d)
		case "continuation":
			Obj(record["continuation"])["commit"] = strings.Repeat("d", 40)
			return tx.Put(d)
		case "source":
			field, value = "commit", strings.Repeat("d", 40)
		case "repository":
			field, value = "repositoryId", "repository_foreign"
		case "missing-id":
			x, _ := tx.Get(taskID)
			delete(x.Data, "signingRepairId")
			return tx.Put(x)
		}
		x, err := tx.Get(id)
		if err != nil {
			return err
		}
		x.Data[field] = value
		return tx.Put(x)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestAndroidSigningRepairClaimRejectsAuthorityDrift(t *testing.T) {
	for _, phase := range []string{"code", "unsigned", "signing"} {
		modes := []string{"policy", "pipeline", "plan", "root-build", "certificate", "failed-artifact", "failed-run", "record", "command", "source", "repository", "missing-id"}
		if phase != "code" {
			modes = append(modes, "continuation")
		}
		for _, mode := range modes {
			t.Run(phase+"/"+mode, func(t *testing.T) {
				f := newSigningRepairFixture(t)
				defect, _, _ := f.rejected(t)
				actor, taskID := f.builder, S(defect.Data, "repairTaskId")
				if phase != "code" {
					f.code(t)
					defect, _ = f.s.Store.Get(defect.ID)
					taskID = S(defect.Data, "buildTaskId")
				}
				if phase == "signing" {
					f.unsignedBuild(t)
					actor, taskID = f.signer, S(defect.Data, "signingTaskId")
				}
				f.drift(t, defect, taskID, mode)
				if idle := must(t, f.s, actor, "tasks.claim", M{}); idle.Kind != "worker" {
					t.Fatal("changed repair authority consumed a lease", phase, mode, idle)
				}
				task, _ := f.s.Store.Get(taskID)
				if task.Status != "blocked" || N(task.Data, "attempts") != 0 || S(task.Data, "reason") == "" {
					t.Fatal("changed repair authority not durably blocked", phase, mode, task)
				}
			})
		}
	}
}

func TestAndroidSigningRepairAuthorityRecheckedAtSourceUpload(t *testing.T) {
	f := newSigningRepairFixture(t)
	defect, _, _ := f.rejected(t)
	run := must(t, f.s, f.builder, "tasks.claim", M{})
	b := Obj(Obj(run.Data["task"])["sourceBinding"])
	must(t, f.s, f.owner, "quality.revoke", M{"id": f.policy.ID, "reason": "Stop code repair while leased", "key": "stop-code"})
	if _, err := f.s.Command(context.Background(), f.builder, "repositories.commit", M{"id": f.repository.ID, "branch": S(Obj(run.Data["task"]), "branch"), "baseCommit": S(b, "commit"), "expectedHead": S(b, "expectedHead"), "runId": run.ID, "leaseToken": S(run.Data, "leaseToken"), "message": "Rejected repair", "files": M{"source.txt": "must not be accepted"}}); err == nil {
		t.Fatal("revoked repair wrote repository state", defect.ID)
	}
}

func TestAndroidSigningRepairAuthorityRecheckedAtBuildUploadAndCompletion(t *testing.T) {
	for _, phase := range []string{"unsigned-upload", "unsigned-complete", "signing-upload", "signing-complete", "signed-artifact-use"} {
		t.Run(phase, func(t *testing.T) {
			f := newSigningRepairFixture(t)
			defect, _, _ := f.rejected(t)
			f.code(t)
			actor, kind := f.builder, "unsigned-package"
			if strings.HasPrefix(phase, "signing") || phase == "signed-artifact-use" {
				f.unsignedBuild(t)
				actor, kind = f.signer, "package"
			}
			run := must(t, f.s, actor, "tasks.claim", M{})
			if run.Kind != "run" {
				t.Fatal("expected work before revocation", run)
			}
			meta := M{"goalId": f.goal.ID, "repositoryId": f.repository.ID, "commit": f.commit, "target": "android-huawei", "type": kind, "name": "repair.apk", "runId": run.ID, "leaseToken": S(run.Data, "leaseToken")}
			var artifact Resource
			if !strings.HasSuffix(phase, "upload") {
				var err error
				artifact, err = f.s.Upload(context.Background(), actor, meta, bytes.NewBufferString("transformed "+f.commit))
				if err != nil {
					t.Fatal(err)
				}
			}
			if phase == "signed-artifact-use" {
				must(t, f.s, actor, "tasks.complete", f.completion(run, artifact))
			}
			must(t, f.s, f.owner, "quality.revoke", M{"id": f.policy.ID, "reason": "Stop repair after work began", "key": phase})
			if strings.HasSuffix(phase, "upload") {
				if _, err := f.s.Upload(context.Background(), actor, meta, bytes.NewBufferString("revoked")); err == nil {
					t.Fatal("revoked repair uploaded artifact", phase, defect.ID)
				}
			} else if phase == "signed-artifact-use" {
				if err := f.s.Store.Update(context.Background(), func(tx *Tx) error { return f.s.validateSigningArtifact(tx, artifact) }); err == nil {
					t.Fatal("revoked repair signature accepted for new evidence/release")
				}
			} else {
				completion := f.completion(run, artifact)
				if kind == "unsigned-package" {
					completion = M{"id": run.ID, "leaseToken": S(run.Data, "leaseToken"), "exitCode": 0, "result": M{"artifactId": artifact.ID, "commit": f.commit, "repositoryId": f.repository.ID}}
				}
				if _, err := f.s.Command(context.Background(), actor, "tasks.complete", completion); err == nil {
					t.Fatal("revoked repair accepted completion", phase)
				}
			}
		})
	}
}

func TestAndroidSigningRepairRevocationStopsHeartbeat(t *testing.T) {
	f := newSigningRepairFixture(t)
	f.rejected(t)
	run := must(t, f.s, f.builder, "tasks.claim", M{})
	must(t, f.s, f.builder, "tasks.progress", M{"id": run.ID, "leaseToken": S(run.Data, "leaseToken"), "message": "Repair in progress"})
	must(t, f.s, f.owner, "quality.revoke", M{"id": f.policy.ID, "reason": "Stop repair execution", "key": "stop-heartbeat"})
	if _, err := f.s.Command(context.Background(), f.builder, "tasks.progress", M{"id": run.ID, "leaseToken": S(run.Data, "leaseToken"), "message": "Must not extend revoked work"}); err == nil {
		t.Fatal("revoked repair kept extending its execution lease")
	}
}

func TestAndroidSigningRepairProtectedChainCannotSplitOrReorder(t *testing.T) {
	for _, command := range []string{"tasks.split", "plans.reorder"} {
		t.Run(command, func(t *testing.T) {
			f := newSigningRepairFixture(t)
			defect, _, _ := f.rejected(t)
			task, _ := f.s.Store.Get(S(defect.Data, "repairTaskId"))
			before, _ := f.s.Store.List(f.project.ID, "task", f.goal.ID)
			input := M{"id": task.ID, "goalId": f.goal.ID, "reason": "Restructure repair", "impact": "Change protected chain", "children": []any{M{"title": "One", "instructions": "First part"}, M{"title": "Two", "instructions": "Second part"}}, "tasks": []any{M{"id": S(task.Data, "logicalId"), "dependsOn": []any{}}}}
			if _, err := f.s.Command(context.Background(), f.owner, command, input); err == nil {
				t.Fatal("protected repair chain was rewritten outside its authority record", command)
			}
			after, _ := f.s.Store.List(f.project.ID, "task", f.goal.ID)
			if Hash(before) != Hash(after) {
				t.Fatal("rejected protected-chain change mutated tasks")
			}
		})
	}
}

func TestAndroidSigningRepairUnknownSigningNeverCreatesAnotherOperation(t *testing.T) {
	f := newSigningRepairFixture(t)
	defect, _, _ := f.rejected(t)
	f.code(t)
	f.unsignedBuild(t)
	run := must(t, f.s, f.signer, "tasks.claim", M{})
	if err := f.s.Store.Update(context.Background(), func(tx *Tx) error {
		r, _ := tx.Get(run.ID)
		r.Data["leaseUntil"] = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
		return tx.Put(r)
	}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if idle := must(t, f.s, f.signer, "tasks.claim", M{}); idle.Kind != "worker" {
			t.Fatal("unknown repair signing was automatically repeated", idle)
		}
	}
	task, _ := f.s.Store.Get(S(run.Data, "taskId"))
	if task.Status != "blocked" || N(task.Data, "attempts") != 1 {
		t.Fatal("unknown signing did not retain one blocked attempt", task)
	}
	if _, err := f.s.Command(context.Background(), f.owner, "tasks.retry", M{"id": task.ID}); err == nil {
		t.Fatal("generic retry reauthorized an unknown repair signature")
	}
	defect, _ = f.s.Store.Get(defect.ID)
	if len(Items(defect.Data["androidSigningRepairs"])) != 1 {
		t.Fatal("signing interruption created a fresh repair round", defect)
	}
}

func TestAndroidSigningRepairInterruptedSourceRequiresExplicitCurrentRecovery(t *testing.T) {
	for _, revoke := range []bool{false, true} {
		t.Run(map[bool]string{false: "current", true: "revoked"}[revoke], func(t *testing.T) {
			f := newSigningRepairFixture(t)
			defect, _, _ := f.rejected(t)
			run := must(t, f.s, f.builder, "tasks.claim", M{})
			b := Obj(Obj(run.Data["task"])["sourceBinding"])
			input := M{"key": run.ID, "runId": run.ID, "leaseToken": S(run.Data, "leaseToken"), "branch": S(b, "targetBranch"), "baseCommit": S(b, "commit"), "expectedHead": S(b, "expectedHead"), "message": "Actual source before interrupted completion"}
			body := sourceTar(t, map[string][]byte{"source.txt": []byte("recovered repair source\n")}, "")
			if _, err := f.s.CommitSnapshot(context.Background(), f.builder, f.repository.ID, input, bytes.NewReader(body)); err != nil {
				t.Fatal(err)
			}
			ops, _ := f.s.Store.List(f.project.ID, "source_commit", f.goal.ID)
			if len(ops) != 1 {
				t.Fatal("missing actual recoverable source candidate", ops)
			}
			if err := f.s.Store.Update(context.Background(), func(tx *Tx) error {
				r, _ := tx.Get(run.ID)
				r.Data["leaseUntil"] = "2000-01-01T00:00:00Z"
				return tx.Put(r)
			}); err != nil {
				t.Fatal(err)
			}
			if revoke {
				must(t, f.s, f.owner, "quality.revoke", M{"id": f.policy.ID, "reason": "Withdraw while source completion is interrupted", "key": "revoke-recovery"})
			}
			decision, err := f.s.Command(context.Background(), f.owner, "sources.reconcile", recoveryInput(ops[0]))
			if revoke {
				if err == nil {
					t.Fatal("revoked policy authorized a recovered repair continuation", decision)
				}
				d, _ := f.s.Store.Get(defect.ID)
				if S(d.Data, "buildTaskId") != "" {
					t.Fatal("rejected recovery created a new build/sign chain", d)
				}
				return
			}
			if err != nil || decision.Status != "confirmed" {
				t.Fatal("current manually confirmed repair source was rejected", err, decision)
			}
			f.commit = S(ops[0].Data, "commit")
			f.unsignedBuild(t)
			_, artifact := f.sign(t)
			qa := must(t, f.s, f.quality, "tasks.claim", M{})
			if S(Obj(qa.Data["task"]), "artifactId") != artifact.ID {
				t.Fatal("recovered repair lost final independent QA", qa)
			}
			must(t, f.s, f.quality, "tasks.complete", M{"id": qa.ID, "leaseToken": S(qa.Data, "leaseToken"), "exitCode": 0, "result": M{}})
			d, _ := f.s.Store.Get(defect.ID)
			if d.Status != "closed" {
				t.Fatal("verified recovered repair remained open", d)
			}
		})
	}
}

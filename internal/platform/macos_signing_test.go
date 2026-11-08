package platform

import (
	"context"
	"strings"
	"testing"
	"time"
)

func macOSSigningPlan() []any {
	return []any{
		M{"id": "unsigned", "title": "Build unsigned macOS application", "kind": "build", "role": "developer", "runtime": "darwin", "target": "macos", "artifactStage": "unsigned", "command": []any{"true"}, "outputPath": "unsigned.app.tar.gz"},
		M{"id": "sign", "title": "Sign and notarize exact macOS application", "kind": "build", "role": "release", "runtime": "darwin", "target": "macos", "signingMode": "macos-pkg", "inputArtifactFrom": "unsigned", "dependsOn": []any{"unsigned"}, "expectedSigner": strings.Repeat("a", 64), "expectedApplicationSigner": strings.Repeat("d", 64), "expectedTeamId": "ABCD123456"},
	}
}

func macOSSigningPending(f androidSigningFixture, run Resource) M {
	in := Obj(Obj(run.Data["task"])["signingInput"])
	return M{"id": run.ID, "leaseToken": S(run.Data, "leaseToken"), "exitCode": 0, "result": M{
		"signingPending": true, "repositoryId": f.repository.ID, "commit": f.commit,
		"signingInputDigest": S(in, "digest"), "operationKey": S(in, "operationKey"),
		"certificateSHA256": strings.Repeat("a", 64), "applicationCertificateSHA256": strings.Repeat("d", 64),
		"teamId": "ABCD123456", "notarizationId": "11111111-2222-4333-8444-555555555555",
		"payloadDigest": strings.Repeat("b", 64), "requestBinding": strings.Repeat("c", 64),
	}}
}

func dueMacOSSigning(t *testing.T, s *Service, id string) {
	t.Helper()
	if err := s.Store.Update(context.Background(), func(tx *Tx) error {
		task, err := tx.Get(id)
		if err != nil {
			return err
		}
		task.Data["signingNextCheckAt"] = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
		return tx.Put(task)
	}); err != nil {
		t.Fatal(err)
	}
}

func TestMacOSSigningWaitReleasesLeaseAndOnlyResumesExactStatus(t *testing.T) {
	s, owner, project := fixture(t)
	f := setupMacOSSigningFixture(t, s, owner, project)
	f.build(t)
	first := must(t, s, f.signer, "tasks.claim", M{})
	completed := must(t, s, f.signer, "tasks.complete", macOSSigningPending(f, first))
	taskID := S(first.Data, "taskId")
	task, _ := s.Store.Get(taskID)
	if completed.Status != "waiting_signature" || task.Status != "awaiting_signature" || S(completed.Data, "leaseToken") != "" || S(task.Data, "signingPhase") != "status" || S(task.Data, "assignedWorkerId") != f.signer.WorkerID {
		t.Fatal("known notarization wait did not persist and release the lease", completed, task)
	}
	if idle := must(t, s, f.signer, "tasks.claim", M{}); idle.Kind != "worker" {
		t.Fatal("queried before persisted due time")
	}
	other := setupMacOSSigningFixture(t, s, owner, project)
	other.build(t)
	other.signer = f.signer
	otherRun := must(t, s, f.signer, "tasks.claim", M{})
	if otherRun.GoalID != other.goal.ID {
		t.Fatal("waiting operation prevented work on a different application")
	}
	otherArtifact := other.uploadSigned(t, otherRun)
	must(t, s, f.signer, "tasks.complete", macOSSigningCompletion(other, otherRun, otherArtifact))
	dueMacOSSigning(t, s, taskID)
	status := must(t, s, f.signer, "tasks.claim", M{})
	if status.Kind != "run" || S(status.Data, "taskId") != taskID || status.ID == first.ID || S(Obj(status.Data["task"]), "signingPhase") != "status" || Hash(Obj(status.Data["task"])["signingInput"]) != Hash(Obj(first.Data["task"])["signingInput"]) {
		t.Fatal("continued by signing again or changed authoritative input", status)
	}
	must(t, s, f.signer, "tasks.complete", macOSSigningPending(f, first))
	if current, _ := s.Store.Get(taskID); current.Status != "running" {
		t.Fatal("old completion rewound current status lease")
	}
	artifact := f.uploadSigned(t, status)
	must(t, s, f.signer, "tasks.complete", macOSSigningCompletion(f, status, artifact))
	if current, _ := s.Store.Get(taskID); current.Status != "succeeded" || N(current.Data, "attempts") != 1 || N(current.Data, "signingPolls") != 1 {
		t.Fatal("signature wait repeated the mutating attempt or lost query history", current)
	}
}

func TestMacOSSigningWaitRejectsChangedObservationAndStopsAtBoundaries(t *testing.T) {
	for _, mode := range []string{"changed-notary", "changed-binding", "changed-payload", "changed-final-notary", "changed-final-binding", "changed-final-payload", "cancelled", "worker-disabled", "baseline", "deadline", "poll-limit", "active-time"} {
		t.Run(mode, func(t *testing.T) {
			s, owner, project := fixture(t)
			f := setupMacOSSigningFixture(t, s, owner, project)
			f.build(t)
			first := must(t, s, f.signer, "tasks.claim", M{})
			must(t, s, f.signer, "tasks.complete", macOSSigningPending(f, first))
			taskID := S(first.Data, "taskId")
			dueMacOSSigning(t, s, taskID)
			if strings.HasPrefix(mode, "changed-") {
				run := must(t, s, f.signer, "tasks.claim", M{})
				input := macOSSigningPending(f, run)
				if strings.HasPrefix(mode, "changed-final-") {
					input = macOSSigningCompletion(f, run, f.uploadSigned(t, run))
				}
				key, value := "notarizationId", "99999999-2222-4333-8444-555555555555"
				if strings.HasSuffix(mode, "-binding") {
					key, value = "requestBinding", strings.Repeat("e", 64)
				} else if strings.HasSuffix(mode, "-payload") {
					key, value = "payloadDigest", strings.Repeat("e", 64)
				}
				Obj(input["result"])[key] = value
				if _, err := s.Command(context.Background(), f.signer, "tasks.complete", input); err == nil {
					t.Fatal("status moved to another signed operation")
				}
				return
			}
			if mode == "cancelled" {
				must(t, s, owner, "tasks.cancel", M{"id": taskID})
			} else if mode == "worker-disabled" {
				must(t, s, owner, "workers.disable", M{"id": f.signer.WorkerID})
			} else if mode == "baseline" {
				must(t, s, owner, "goals.revise", M{"id": f.goal.ID, "reason": "New acceptance", "outcome": "Changed application"})
			} else if err := s.Store.Update(context.Background(), func(tx *Tx) error {
				task, _ := tx.Get(taskID)
				switch mode {
				case "deadline":
					task.Data["signingDeadline"] = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
				case "poll-limit":
					task.Data["signingPolls"] = 288
				case "active-time":
					task.Data["signingActiveSeconds"] = 900
				}
				return tx.Put(task)
			}); err != nil {
				t.Fatal(err)
			}
			if mode == "worker-disabled" {
				if _, err := s.Command(context.Background(), f.signer, "tasks.claim", M{}); err == nil {
					t.Fatal("disabled signer resumed status")
				}
			} else if idle := must(t, s, f.signer, "tasks.claim", M{}); idle.Kind != "worker" {
				t.Fatal("invalid wait resumed a task", idle)
			}
			if _, err := s.Command(context.Background(), owner, "tasks.retry", M{"id": taskID}); err == nil {
				t.Fatal("generic retry reauthorized a signing intent")
			}
		})
	}
}

func TestMacOSSigningProgressTimeoutBlocksAndAccountsActiveTime(t *testing.T) {
	s, owner, project := fixture(t)
	f := setupMacOSSigningFixture(t, s, owner, project)
	f.build(t)
	run := must(t, s, f.signer, "tasks.claim", M{})
	if err := s.Store.Update(context.Background(), func(tx *Tx) error {
		current, err := tx.Get(run.ID)
		if err != nil {
			return err
		}
		current.CreatedAt = time.Now().Add(-2 * time.Second).UTC().Format(time.RFC3339Nano)
		current.Data["deadline"] = time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)
		current.Data["leaseUntil"] = time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano)
		return tx.Put(current)
	}); err != nil {
		t.Fatal(err)
	}
	result := must(t, s, f.signer, "tasks.progress", M{"id": run.ID, "leaseToken": S(run.Data, "leaseToken"), "message": "Checking private operation"})
	task, _ := s.Store.Get(S(run.Data, "taskId"))
	if result.Status != "blocked" || task.Status != "blocked" || S(result.Data, "leaseToken") != "" || N(task.Data, "signingActiveSeconds") < 2 || !strings.Contains(S(task.Data, "reason"), "核对") {
		t.Fatal("timed out signing remained retryable or lost its active time", result, task)
	}
	if _, err := s.Command(context.Background(), owner, "tasks.retry", M{"id": task.ID}); err == nil {
		t.Fatal("timeout reauthorized the original signing intent")
	}
}

func TestMacOSSigningAcceptsNativePlanAndRejectsUnsafeTransforms(t *testing.T) {
	if _, err := validateTasks(macOSSigningPlan()); err != nil {
		t.Fatal("native macOS signing plan rejected", err)
	}
	for name, change := range map[string]M{
		"linux": {"runtime": "linux"}, "windows": {"runtime": "windows"}, "android": {"target": "android-huawei"},
		"developer": {"role": "developer"}, "shell": {"command": []any{"true"}}, "output": {"outputPath": "signed.pkg"},
		"app-certificate": {"expectedApplicationSigner": "Developer ID"}, "app-uppercase": {"expectedApplicationSigner": strings.Repeat("D", 64)},
		"installer": {"expectedSigner": "ABCD123456"}, "team": {"expectedTeamId": "other"}, "lower-team": {"expectedTeamId": "abcd123456"},
		"no-app-certificate": {"expectedApplicationSigner": nil}, "no-team": {"expectedTeamId": nil},
		"foreign-input": {"inputArtifactFrom": "foreign"}, "not-ancestor": {"dependsOn": []any{}},
		"raw-phase": {"signingPhase": "status"}, "raw-notary-id": {"notarizationId": "11111111-2222-4333-8444-555555555555"},
	} {
		t.Run(name, func(t *testing.T) {
			items := macOSSigningPlan()
			for key, value := range change {
				Obj(items[1])[key] = value
			}
			if _, err := validateTasks(items); err == nil {
				t.Fatal("unsafe macOS signing transform accepted")
			}
		})
	}
}

func setupMacOSSigningFixture(t *testing.T, s *Service, owner Actor, project Resource) androidSigningFixture {
	t.Helper()
	g := must(t, s, owner, "goals.create", M{"projectId": project.ID, "title": "Release macOS application", "outcome": "Sign and notarize exact application, then independently verify it", "targets": []any{"macos"}, "criteria": []any{M{"id": "submit", "text": "Users can submit feedback", "method": "automated"}}, "budget": 100})
	must(t, s, owner, "goals.approve", M{"id": g.ID})
	repo := must(t, s, owner, "repositories.create", M{"projectId": project.ID, "title": "macos-source"})
	repo = must(t, s, owner, "repositories.commit", M{"id": repo.ID, "branch": "feature", "message": "Exact macOS source", "files": M{"source.txt": "unsigned fixture bytes\n"}})
	commit := S(repo.Data, "lastCommit")
	items := macOSSigningPlan()
	Obj(items[0])["repositoryId"], Obj(items[0])["commit"] = repo.ID, commit
	plan := must(t, s, owner, "plans.propose", M{"goalId": g.ID, "summary": "Native build then isolated signing and notarization", "tasks": items})
	must(t, s, owner, "plans.approve", M{"id": plan.ID})
	g, _ = s.Store.Get(g.ID)
	worker := func(role string, trusted bool) Actor {
		w := must(t, s, owner, "workers.register", M{"projectId": project.ID, "title": role + " macOS node", "roles": []any{role}, "runtime": "darwin", "capabilities": []any{map[bool]string{true: "test", false: "build"}[trusted]}, "trustedQuality": trusted})
		return Actor{ID: "worker:" + w.ID, Kind: "worker", ProjectID: project.ID, WorkerID: w.ID, Roles: []string{role}}
	}
	return androidSigningFixture{s: s, owner: owner, builder: worker("developer", false), signer: worker("release", false), quality: worker("quality", true), project: project, goal: g, repository: repo, plan: plan, commit: commit, target: "macos"}
}

func macOSSigningCompletion(f androidSigningFixture, run, artifact Resource) M {
	in := f.completion(run, artifact)
	result := Obj(in["result"])
	result["applicationCertificateSHA256"] = strings.Repeat("d", 64)
	result["teamId"] = "ABCD123456"
	result["notarizationId"] = "11111111-2222-4333-8444-555555555555"
	result["notarized"] = true
	return in
}

func TestMacOSSigningDerivesNativeInputAndRequiresIndependentAcceptance(t *testing.T) {
	s, owner, project := fixture(t)
	f := setupMacOSSigningFixture(t, s, owner, project)
	if idle := must(t, s, f.signer, "tasks.claim", M{}); idle.Kind != "worker" {
		t.Fatal("signer executed before unsigned dependency")
	}
	f.build(t)
	pipeline := must(t, s, owner, "pipelines.create", M{"projectId": project.ID, "title": "Independent macOS CI verification", "runtime": "darwin", "method": "automated", "verificationEnvironment": "ci", "command": []any{"true"}})
	must(t, s, owner, "quality.bind", M{"goalId": f.goal.ID, "expectedRequirementRevision": f.goal.Revision, "expectedPlanId": f.plan.ID, "target": "macos", "pipelineId": pipeline.ID, "criteria": []any{"submit"}, "budgetCents": 100, "key": "macos-quality"})
	if idle := must(t, s, f.quality, "tasks.claim", M{}); idle.Kind != "worker" {
		t.Fatal("unsigned app scheduled as final independent quality")
	}
	run := must(t, s, f.signer, "tasks.claim", M{})
	in := Obj(Obj(run.Data["task"])["signingInput"])
	if S(in, "expectedApplicationSigner") != strings.Repeat("d", 64) || S(in, "expectedTeamId") != "ABCD123456" || S(in, "artifactId") != f.unsigned.ID || S(in, "commit") != f.commit {
		t.Fatal("native application certificate/team/input were not frozen", in)
	}
	artifact := f.uploadSigned(t, run)
	if S(artifact.Data, "expectedApplicationSigner") != strings.Repeat("d", 64) || S(artifact.Data, "expectedTeamId") != "ABCD123456" {
		t.Fatal("final package lost authoritative Apple identity", artifact)
	}
	completion := macOSSigningCompletion(f, run, artifact)
	must(t, s, f.signer, "tasks.complete", completion)
	must(t, s, f.signer, "tasks.complete", completion)
	qa := must(t, s, f.quality, "tasks.claim", M{})
	if qa.Kind != "run" || S(Obj(qa.Data["task"]), "artifactId") != artifact.ID {
		t.Fatal("independent quality did not bind the final package")
	}
	must(t, s, f.quality, "tasks.complete", M{"id": qa.ID, "leaseToken": S(qa.Data, "leaseToken"), "exitCode": 0, "result": M{}})
	if _, err := s.Command(context.Background(), owner, "releases.create", M{"goalId": f.goal.ID, "version": "1.0.0", "commit": f.commit, "artifactIds": []any{artifact.ID}}); err == nil {
		t.Fatal("CI validation bypassed actual desktop-native acceptance")
	}
	if releases, _ := s.Store.List(project.ID, "release", f.goal.ID); len(releases) != 0 {
		t.Fatal("unaccepted native release persisted")
	}
}

func TestMacOSSigningRejectsIncompleteOrForeignNotarizationReceipt(t *testing.T) {
	for name, change := range map[string]M{
		"not-notarized": {"notarized": false}, "string-notarized": {"notarized": "true"},
		"app-certificate": {"applicationCertificateSHA256": strings.Repeat("e", 64)},
		"team":            {"teamId": "OTHER12345"}, "notary": {"notarizationId": "foreign"},
		"missing-notary": {"notarizationId": nil}, "missing-app-certificate": {"applicationCertificateSHA256": nil},
	} {
		t.Run(name, func(t *testing.T) {
			s, owner, project := fixture(t)
			f := setupMacOSSigningFixture(t, s, owner, project)
			f.build(t)
			run := must(t, s, f.signer, "tasks.claim", M{})
			artifact := f.uploadSigned(t, run)
			completion := macOSSigningCompletion(f, run, artifact)
			for key, value := range change {
				Obj(completion["result"])[key] = value
			}
			if _, err := s.Command(context.Background(), f.signer, "tasks.complete", completion); err == nil {
				t.Fatal("invalid native signing receipt accepted")
			}
			if current, _ := s.Store.Get(S(run.Data, "taskId")); current.Status == "succeeded" {
				t.Fatal("failed receipt marked signing succeeded")
			}
		})
	}
}

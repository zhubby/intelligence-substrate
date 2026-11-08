package platform

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func iOSSigningPlan() []any {
	return []any{
		M{"id": "unsigned", "title": "Build unsigned iPhoneOS application", "kind": "build", "role": "developer", "runtime": "darwin", "target": "ios-appstore", "artifactStage": "unsigned", "command": []any{"true"}, "outputPath": "unsigned.app.tar.gz"},
		M{"id": "sign", "title": "Sign exact application for App Store", "kind": "build", "role": "release", "runtime": "darwin", "target": "ios-appstore", "signingMode": "ios-ipa", "inputArtifactFrom": "unsigned", "dependsOn": []any{"unsigned"}, "expectedSigner": strings.Repeat("a", 64), "expectedTeamId": "ABCD123456", "expectedProvisioningProfile": strings.Repeat("e", 64)},
	}
}

func setupIOSSigningFixture(t *testing.T, s *Service, owner Actor, project Resource) androidSigningFixture {
	t.Helper()
	g := must(t, s, owner, "goals.create", M{"projectId": project.ID, "title": "Release iOS application", "outcome": "Sign exact device app and independently verify it", "targets": []any{"ios-appstore"}, "criteria": []any{M{"id": "submit", "text": "Users submit feedback", "method": "automated"}}, "budget": 100})
	must(t, s, owner, "goals.approve", M{"id": g.ID})
	repo := must(t, s, owner, "repositories.create", M{"projectId": project.ID, "title": "ios-source"})
	repo = must(t, s, owner, "repositories.commit", M{"id": repo.ID, "branch": "feature", "message": "Exact iOS source", "files": M{"source.txt": "native device source\n"}})
	commit := S(repo.Data, "lastCommit")
	items := iOSSigningPlan()
	Obj(items[0])["repositoryId"], Obj(items[0])["commit"] = repo.ID, commit
	plan := must(t, s, owner, "plans.propose", M{"goalId": g.ID, "summary": "Build device app then isolated distribution signing", "tasks": items})
	must(t, s, owner, "plans.approve", M{"id": plan.ID})
	g, _ = s.Store.Get(g.ID)
	worker := func(role, capability string, trusted bool) Actor {
		w := must(t, s, owner, "workers.register", M{"projectId": project.ID, "title": role + " iOS node", "roles": []any{role}, "runtime": "darwin", "capabilities": []any{capability}, "trustedQuality": trusted})
		return Actor{ID: "worker:" + w.ID, Kind: "worker", ProjectID: project.ID, WorkerID: w.ID, Roles: []string{role}}
	}
	return androidSigningFixture{s: s, owner: owner, builder: worker("developer", "build", false), signer: worker("release", "build", false), quality: worker("quality", "test", true), project: project, goal: g, repository: repo, plan: plan, commit: commit, target: "ios-appstore"}
}

func iOSSigningCompletion(f androidSigningFixture, run, artifact Resource) M {
	in := f.completion(run, artifact)
	result := Obj(in["result"])
	result["teamId"], result["profileSHA256"] = "ABCD123456", strings.Repeat("e", 64)
	return in
}

func TestIOSSigningPlanRequiresNativeDistributionIdentityAndExactProfile(t *testing.T) {
	if _, err := validateTasks(iOSSigningPlan()); err != nil {
		t.Fatal("App Store signing plan rejected", err)
	}
	for name, change := range map[string]M{
		"linux": {"runtime": "linux"}, "windows": {"runtime": "windows"}, "android": {"target": "android-huawei"},
		"developer": {"role": "developer"}, "shell": {"command": []any{"true"}}, "output": {"outputPath": "signed.ipa"},
		"team": {"expectedTeamId": "bad"}, "missing-team": {"expectedTeamId": nil},
		"profile": {"expectedProvisioningProfile": "bad"}, "missing-profile": {"expectedProvisioningProfile": nil},
		"cert": {"expectedSigner": "Apple Distribution"}, "macos-cert": {"expectedApplicationSigner": strings.Repeat("d", 64)},
		"not-ancestor": {"dependsOn": []any{}}, "alternate-input": {"artifactId": "artifact_other"},
	} {
		t.Run(name, func(t *testing.T) {
			items := iOSSigningPlan()
			for key, value := range change {
				Obj(items[1])[key] = value
			}
			if _, err := validateTasks(items); err == nil {
				t.Fatal("unsafe App Store signing accepted")
			}
		})
	}
	for _, field := range []string{"expectedProvisioningProfile", "expectedTeamId"} {
		items := iOSSigningPlan()
		Obj(items[0])[field] = Obj(items[1])[field]
		if _, err := validateTasks(items); err == nil {
			t.Fatal("developer build selected signing identity", field)
		}
	}
}

func TestIOSSigningBindsExactInputAndSchedulesOnlyFinalIndependentQuality(t *testing.T) {
	s, owner, project := fixture(t)
	f := setupIOSSigningFixture(t, s, owner, project)
	f.build(t)
	pipeline := must(t, s, owner, "pipelines.create", M{"projectId": project.ID, "title": "Independent iOS CI", "runtime": "darwin", "method": "automated", "verificationEnvironment": "ci", "command": []any{"true"}})
	must(t, s, owner, "quality.bind", M{"goalId": f.goal.ID, "expectedRequirementRevision": f.goal.Revision, "expectedPlanId": f.plan.ID, "target": f.target, "pipelineId": pipeline.ID, "criteria": []any{"submit"}, "budgetCents": 100, "key": "ios-quality"})
	if idle := must(t, s, f.quality, "tasks.claim", M{}); idle.Kind != "worker" {
		t.Fatal("unsigned app was treated as final quality input")
	}
	run := must(t, s, f.signer, "tasks.claim", M{})
	in := Obj(Obj(run.Data["task"])["signingInput"])
	if S(in, "expectedTeamId") != "ABCD123456" || S(in, "expectedProvisioningProfile") != strings.Repeat("e", 64) || S(in, "artifactId") != f.unsigned.ID || S(in, "commit") != f.commit {
		t.Fatal("distribution input identity was not frozen", in)
	}
	artifact := f.uploadSigned(t, run)
	if S(artifact.Data, "expectedTeamId") != "ABCD123456" || S(artifact.Data, "expectedProvisioningProfile") != strings.Repeat("e", 64) {
		t.Fatal("IPA lost its approved profile", artifact)
	}
	completion := iOSSigningCompletion(f, run, artifact)
	must(t, s, f.signer, "tasks.complete", completion)
	must(t, s, f.signer, "tasks.complete", completion)
	qa := must(t, s, f.quality, "tasks.claim", M{})
	if qa.Kind != "run" || S(Obj(qa.Data["task"]), "artifactId") != artifact.ID {
		t.Fatal("final IPA independent QA was not scheduled")
	}
	must(t, s, f.quality, "tasks.complete", M{"id": qa.ID, "leaseToken": S(qa.Data, "leaseToken"), "exitCode": 0, "result": M{}})
	if _, err := s.Command(context.Background(), owner, "releases.create", M{"goalId": f.goal.ID, "version": "1.0.0", "commit": f.commit, "artifactIds": []any{artifact.ID}}); err == nil || !strings.Contains(err.Error(), "目标环境") {
		t.Fatal("CI bypassed real iOS device acceptance", err)
	}
}

func TestIOSSigningRejectsChangedProfileTeamReceiptAndInputAuthority(t *testing.T) {
	for _, mode := range []string{"profile", "team", "missing-profile", "foreign-artifact-field", "baseline", "disabled", "generic-retry"} {
		t.Run(mode, func(t *testing.T) {
			s, owner, project := fixture(t)
			f := setupIOSSigningFixture(t, s, owner, project)
			f.build(t)
			run := must(t, s, f.signer, "tasks.claim", M{})
			if mode == "foreign-artifact-field" {
				_, err := s.Upload(context.Background(), f.signer, M{"goalId": f.goal.ID, "runId": run.ID, "leaseToken": S(run.Data, "leaseToken"), "target": f.target, "type": "package", "repositoryId": f.repository.ID, "commit": f.commit, "name": "signed.ipa", "expectedProvisioningProfile": strings.Repeat("f", 64)}, bytes.NewBufferString("synthetic signed bytes"))
				if err == nil || !strings.Contains(err.Error(), "来源只能由控制面推导") {
					t.Fatal("Worker supplied authoritative profile metadata", err)
				}
				return
			}
			artifact := f.uploadSigned(t, run)
			completion := iOSSigningCompletion(f, run, artifact)
			switch mode {
			case "profile":
				Obj(completion["result"])["profileSHA256"] = strings.Repeat("f", 64)
			case "team":
				Obj(completion["result"])["teamId"] = "OTHER12345"
			case "missing-profile":
				delete(Obj(completion["result"]), "profileSHA256")
			case "baseline":
				must(t, s, owner, "goals.revise", M{"id": f.goal.ID, "reason": "Changed acceptance", "outcome": "Changed app"})
			case "disabled":
				must(t, s, owner, "workers.disable", M{"id": f.signer.WorkerID})
			case "generic-retry":
				must(t, s, f.signer, "tasks.complete", M{"id": run.ID, "leaseToken": S(run.Data, "leaseToken"), "exitCode": 1, "result": M{"blocked": true}})
				if _, err := s.Command(context.Background(), owner, "tasks.retry", M{"id": S(run.Data, "taskId")}); err == nil {
					t.Fatal("unknown signing intent was automatically retried")
				}
				return
			}
			if _, err := s.Command(context.Background(), f.signer, "tasks.complete", completion); err == nil {
				t.Fatal("changed distribution authority accepted")
			}
		})
	}
}

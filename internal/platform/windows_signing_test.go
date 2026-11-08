package platform

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func windowsSigningPlan() []any {
	return []any{
		M{"id": "unsigned", "title": "Build unsigned Windows MSIX", "kind": "build", "role": "developer", "runtime": "windows", "target": "windows", "artifactStage": "unsigned", "command": []any{"true"}, "outputPath": "unsigned.msix"},
		M{"id": "sign", "title": "Sign exact Windows MSIX", "kind": "build", "role": "release", "runtime": "windows", "target": "windows", "signingMode": "windows-msix", "inputArtifactFrom": "unsigned", "dependsOn": []any{"unsigned"}, "expectedSigner": strings.Repeat("a", 64), "expectedWindowsPublisher": "CN=Company", "expectedWindowsPackage": "com.company.feedback", "expectedWindowsVersion": "1.0.0.0"},
	}
}

func setupWindowsSigningFixture(t *testing.T, s *Service, owner Actor, project Resource) androidSigningFixture {
	t.Helper()
	g := must(t, s, owner, "goals.create", M{"projectId": project.ID, "title": "Release Windows application", "outcome": "Sign exact Windows package then independently verify", "targets": []any{"windows"}, "criteria": []any{M{"id": "submit", "text": "Users submit feedback", "method": "automated"}}, "budget": 100})
	must(t, s, owner, "goals.approve", M{"id": g.ID})
	repo := must(t, s, owner, "repositories.create", M{"projectId": project.ID, "title": "windows-source"})
	repo = must(t, s, owner, "repositories.commit", M{"id": repo.ID, "branch": "feature", "message": "Exact Windows source", "files": M{"source.txt": "native Windows source\n"}})
	commit := S(repo.Data, "lastCommit")
	items := windowsSigningPlan()
	Obj(items[0])["repositoryId"], Obj(items[0])["commit"] = repo.ID, commit
	plan := must(t, s, owner, "plans.propose", M{"goalId": g.ID, "summary": "Build exact MSIX and use isolated signature service", "tasks": items})
	must(t, s, owner, "plans.approve", M{"id": plan.ID})
	g, _ = s.Store.Get(g.ID)
	worker := func(role, capability string, trusted bool) Actor {
		w := must(t, s, owner, "workers.register", M{"projectId": project.ID, "title": role + " Windows node", "roles": []any{role}, "runtime": "windows", "capabilities": []any{capability}, "trustedQuality": trusted})
		return Actor{ID: "worker:" + w.ID, Kind: "worker", ProjectID: project.ID, WorkerID: w.ID, Roles: []string{role}}
	}
	return androidSigningFixture{s: s, owner: owner, builder: worker("developer", "build", false), signer: worker("release", "build", false), quality: worker("quality", "test", true), project: project, goal: g, repository: repo, plan: plan, commit: commit, target: "windows"}
}

func windowsSigningCompletion(f androidSigningFixture, run, artifact Resource) M {
	in := f.completion(run, artifact)
	r := Obj(in["result"])
	r["publisher"], r["packageName"], r["version"], r["architecture"], r["certificateSHA1"] = "CN=Company", "com.company.feedback", "1.0.0.0", "x64", strings.Repeat("A", 40)
	return in
}

func TestWindowsSigningPlanRequiresNativeApprovedIdentity(t *testing.T) {
	if _, err := validateTasks(windowsSigningPlan()); err != nil {
		t.Fatal("approved Windows signing rejected", err)
	}
	for name, change := range map[string]M{
		"linux": {"runtime": "linux"}, "darwin": {"runtime": "darwin"}, "wrong-mode": {"signingMode": "ios-ipa"},
		"developer": {"role": "developer"}, "shell": {"command": []any{"true"}}, "output": {"outputPath": "signed.msix"},
		"publisher": {"expectedWindowsPublisher": "CN=Company\nother"}, "missing-publisher": {"expectedWindowsPublisher": nil},
		"package": {"expectedWindowsPackage": "../escape"}, "missing-package": {"expectedWindowsPackage": nil},
		"version": {"expectedWindowsVersion": "1.0.0"}, "overflow": {"expectedWindowsVersion": "1.0.0.65536"}, "zero-major": {"expectedWindowsVersion": "0.1.0.0"}, "version-alias": {"expectedWindowsVersion": "01.0.0.0"},
		"cert-sha1": {"expectedSigner": strings.Repeat("a", 40)}, "apple-team": {"expectedTeamId": "ABCD123456"},
		"not-ancestor": {"dependsOn": []any{}}, "alternate-input": {"artifactId": "artifact_other"},
	} {
		t.Run(name, func(t *testing.T) {
			items := windowsSigningPlan()
			for k, v := range change {
				Obj(items[1])[k] = v
			}
			if _, err := validateTasks(items); err == nil {
				t.Fatal("unsafe Windows signing accepted")
			}
		})
	}
	for _, key := range []string{"expectedWindowsPublisher", "expectedWindowsPackage", "expectedWindowsVersion"} {
		items := windowsSigningPlan()
		Obj(items[0])[key] = Obj(items[1])[key]
		if _, err := validateTasks(items); err == nil {
			t.Fatal("build selected signing identity", key)
		}
		items = iOSSigningPlan()
		Obj(items[1])[key] = Obj(windowsSigningPlan()[1])[key]
		if _, err := validateTasks(items); err == nil {
			t.Fatal("Apple accepted Windows identity", key)
		}
	}
	for _, system := range []string{"linux", "darwin"} {
		items := windowsSigningPlan()
		Obj(items[0])["runtime"] = system
		if _, err := validateTasks(items); err == nil {
			t.Fatal("non-Windows unsigned build accepted")
		}
	}
}

func TestWindowsSigningBindsInputIdentityAndFinalIndependentQuality(t *testing.T) {
	s, owner, project := fixture(t)
	f := setupWindowsSigningFixture(t, s, owner, project)
	f.build(t)
	pipeline := must(t, s, owner, "pipelines.create", M{"projectId": project.ID, "title": "Independent Windows CI", "runtime": "windows", "method": "automated", "verificationEnvironment": "ci", "command": []any{"true"}})
	must(t, s, owner, "quality.bind", M{"goalId": f.goal.ID, "expectedRequirementRevision": f.goal.Revision, "expectedPlanId": f.plan.ID, "target": f.target, "pipelineId": pipeline.ID, "criteria": []any{"submit"}, "budgetCents": 100, "key": "windows-quality"})
	if idle := must(t, s, f.quality, "tasks.claim", M{}); idle.Kind != "worker" {
		t.Fatal("unsigned MSIX scheduled final QA")
	}
	if idle := must(t, s, f.builder, "tasks.claim", M{}); idle.Kind != "worker" {
		t.Fatal("developer received signing task")
	}
	run := must(t, s, f.signer, "tasks.claim", M{})
	in := Obj(Obj(run.Data["task"])["signingInput"])
	for k, v := range map[string]string{"artifactId": f.unsigned.ID, "commit": f.commit, "expectedWindowsPublisher": "CN=Company", "expectedWindowsPackage": "com.company.feedback", "expectedWindowsVersion": "1.0.0.0"} {
		if S(in, k) != v {
			t.Fatal("approved input lost", k, in)
		}
	}
	artifact := f.uploadSigned(t, run)
	for _, k := range []string{"expectedWindowsPublisher", "expectedWindowsPackage", "expectedWindowsVersion"} {
		if S(artifact.Data, k) != S(in, k) {
			t.Fatal("signed artifact lost identity", k)
		}
	}
	completion := windowsSigningCompletion(f, run, artifact)
	must(t, s, f.signer, "tasks.complete", completion)
	must(t, s, f.signer, "tasks.complete", completion)
	qa := must(t, s, f.quality, "tasks.claim", M{})
	if qa.Kind != "run" || S(Obj(qa.Data["task"]), "artifactId") != artifact.ID {
		t.Fatal("exact final MSIX QA missing")
	}
	must(t, s, f.quality, "tasks.complete", M{"id": qa.ID, "leaseToken": S(qa.Data, "leaseToken"), "exitCode": 0, "result": M{}})
	if _, err := s.Command(context.Background(), owner, "releases.create", M{"goalId": f.goal.ID, "version": "1.0.0", "commit": f.commit, "artifactIds": []any{artifact.ID}}); err == nil || !strings.Contains(err.Error(), "目标环境") {
		t.Fatal("CI bypassed native Windows acceptance", err)
	}
}

func TestWindowsSigningRejectsIdentityDriftAndUnknownReplay(t *testing.T) {
	for _, mode := range []string{"publisher", "packageName", "version", "architecture", "certificateSHA1", "missing-publisher", "foreign-artifact-field", "baseline", "disabled", "generic-retry"} {
		t.Run(mode, func(t *testing.T) {
			s, owner, project := fixture(t)
			f := setupWindowsSigningFixture(t, s, owner, project)
			f.build(t)
			run := must(t, s, f.signer, "tasks.claim", M{})
			if mode == "foreign-artifact-field" {
				_, err := s.Upload(context.Background(), f.signer, M{"goalId": f.goal.ID, "runId": run.ID, "leaseToken": S(run.Data, "leaseToken"), "target": f.target, "type": "package", "repositoryId": f.repository.ID, "commit": f.commit, "name": "signed.msix", "expectedWindowsPublisher": "CN=Other"}, bytes.NewBufferString("synthetic signed bytes"))
				if err == nil {
					t.Fatal("worker forged authoritative publisher")
				}
				return
			}
			artifact := f.uploadSigned(t, run)
			completion := windowsSigningCompletion(f, run, artifact)
			switch mode {
			case "missing-publisher":
				delete(Obj(completion["result"]), "publisher")
			case "baseline":
				must(t, s, owner, "goals.revise", M{"id": f.goal.ID, "reason": "Changed acceptance", "outcome": "Changed app"})
			case "disabled":
				must(t, s, owner, "workers.disable", M{"id": f.signer.WorkerID})
			case "generic-retry":
				must(t, s, f.signer, "tasks.complete", M{"id": run.ID, "leaseToken": S(run.Data, "leaseToken"), "exitCode": 1, "result": M{"blocked": true}})
				if _, err := s.Command(context.Background(), owner, "tasks.retry", M{"id": S(run.Data, "taskId")}); err == nil {
					t.Fatal("unknown signing retried")
				}
				return
			default:
				Obj(completion["result"])[mode] = "other"
			}
			if _, err := s.Command(context.Background(), f.signer, "tasks.complete", completion); err == nil {
				t.Fatal("drifted signing authority accepted")
			}
		})
	}
}

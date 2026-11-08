package platform

import (
	"context"
	"strings"
	"testing"
)

func TestAABControlledSigningAndIndependentFinalQuality(t *testing.T) {
	s, owner, project := fixture(t)
	f := setupAndroidSigningFixtureWithMethod(t, s, owner, project, "android-google-play", "automated")
	f.build(t)
	run := must(t, s, f.signer, "tasks.claim", M{})
	if run.Kind != "run" || S(Obj(run.Data["task"]), "signingMode") != "android-aab" {
		t.Fatal("AAB signing was not routed to the dedicated node", run)
	}
	artifact := f.uploadSigned(t, run)
	if S(artifact.Data, "signingMode") != "android-aab" || S(artifact.Data, "target") != "android-google-play" {
		t.Fatal("bundle provenance lost its format", artifact)
	}
	must(t, s, f.signer, "tasks.complete", f.completion(run, artifact))
	pipeline := must(t, s, owner, "pipelines.create", M{"projectId": project.ID, "title": "Exact upload bundle validation", "runtime": "linux", "method": "automated", "verificationEnvironment": "ci", "command": []any{"true"}})
	must(t, s, owner, "pipelines.run", M{"pipelineId": pipeline.ID, "artifactId": artifact.ID, "criteria": []any{"submit"}})
	qa := must(t, s, f.quality, "tasks.claim", M{})
	if qa.Kind != "run" || S(Obj(qa.Data["task"]), "artifactId") != artifact.ID {
		t.Fatal("bundle validation did not bind exact signed output", qa)
	}
	must(t, s, f.quality, "tasks.complete", M{"id": qa.ID, "leaseToken": S(qa.Data, "leaseToken"), "exitCode": 0, "result": M{}})
	if _, err := s.Command(context.Background(), owner, "releases.create", M{"goalId": f.goal.ID, "version": "1.0.0", "commit": f.commit, "artifactIds": []any{artifact.ID}}); err == nil {
		t.Fatal("CI bundle validation bypassed device acceptance")
	}
}

func TestAABSigningModeCannotCrossTargets(t *testing.T) {
	for _, target := range []string{"android-google-play", "android-huawei", "web"} {
		for _, mode := range []string{"android-apk", "android-aab", "unknown"} {
			t.Run(target+"/"+mode, func(t *testing.T) {
				unsigned := M{"id": "unsigned", "title": "Unsigned", "kind": "build", "role": "developer", "runtime": "linux", "target": target, "artifactStage": "unsigned"}
				sign := M{"id": "sign", "title": "Sign", "kind": "build", "role": "release", "runtime": "linux", "target": target, "signingMode": mode, "dependsOn": []any{"unsigned"}, "inputArtifactFrom": "unsigned", "expectedSigner": strings.Repeat("a", 64)}
				_, err := validateTasks([]any{unsigned, sign})
				valid := target == "android-google-play" && mode == "android-aab" || target == "android-huawei" && mode == "android-apk"
				if (err == nil) != valid {
					t.Fatal("wrong target/format decision", target, mode, err)
				}
			})
		}
	}
}

func TestAABSigningRepairUsesFreshOperationAndProtectedRecheck(t *testing.T) {
	s, owner, project := fixture(t)
	testSigningRepairFreshChain(t, setupSigningRepairFixtureTarget(t, s, owner, project, "android-google-play"))
}

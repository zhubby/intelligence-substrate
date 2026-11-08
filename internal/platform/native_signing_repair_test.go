package platform

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

// These are control-plane protocol fixtures, not native packages/signatures.
// macOS exercises the persisted notarization wait rather than simulating a
// synchronous root task that never acquired execution metadata.
type nativeSigningRepairFixture struct{ signingRepairFixture }

func setupNativeSigningRepairFixture(t *testing.T, s *Service, owner Actor, project Resource, target string) *nativeSigningRepairFixture {
	t.Helper()
	var base androidSigningFixture
	var items []any
	system := "darwin"
	switch target {
	case "macos":
		base, items = setupMacOSSigningFixture(t, s, owner, project), macOSSigningPlan()
	case "ios-appstore":
		base, items = setupIOSSigningFixture(t, s, owner, project), iOSSigningPlan()
	case "windows":
		base, items, system = setupWindowsSigningFixture(t, s, owner, project), windowsSigningPlan(), "windows"
	default:
		t.Fatal("unsupported fixture target", target)
	}
	f := &nativeSigningRepairFixture{signingRepairFixture{androidSigningFixture: base}}
	code := M{"id": "code", "title": "Implement or repair exact source", "kind": "code", "role": "developer", "runtime": system, "repositoryId": f.repository.ID, "commit": f.commit, "branch": "implementation", "command": []any{"true"}, "budgetCents": 100}
	Obj(items[0])["repositoryId"], Obj(items[0])["dependsOn"], Obj(items[0])["budgetCents"] = f.repository.ID, []any{"code"}, 100
	Obj(items[1])["budgetCents"] = 100
	f.plan = must(t, s, owner, "plans.propose", M{"goalId": f.goal.ID, "summary": "Approved native code, unsigned build and fixed identity signing", "tasks": append([]any{code}, items...)})
	must(t, s, owner, "plans.approve", M{"id": f.plan.ID})
	w := must(t, s, owner, "workers.register", M{"projectId": project.ID, "title": "Native source/build node", "runtime": system, "roles": []any{"developer"}, "capabilities": []any{"code", "build"}})
	f.builder = Actor{ID: "worker:" + w.ID, Kind: "worker", WorkerID: w.ID, ProjectID: project.ID, Roles: []string{"developer"}}
	f.pipeline = must(t, s, owner, "pipelines.create", M{"projectId": project.ID, "title": "Protected exact native package QA", "runtime": system, "method": "automated", "verificationEnvironment": "ci", "command": []any{"true"}})
	f.policy = must(t, s, owner, "quality.bind", M{"goalId": f.goal.ID, "expectedRequirementRevision": f.goal.Revision, "expectedPlanId": f.plan.ID, "pipelineId": f.pipeline.ID, "target": target, "criteria": []any{"submit"}, "budgetCents": 100, "key": "native-signed-repair"})
	return f
}

func newNativeSigningRepairFixture(t *testing.T, target string) *nativeSigningRepairFixture {
	s, owner, project := fixture(t)
	return setupNativeSigningRepairFixture(t, s, owner, project, target)
}

func (f *nativeSigningRepairFixture) signingCompletion(run, artifact Resource) M {
	switch f.target {
	case "macos":
		return macOSSigningCompletion(f.androidSigningFixture, run, artifact)
	case "ios-appstore":
		return iOSSigningCompletion(f.androidSigningFixture, run, artifact)
	default:
		return windowsSigningCompletion(f.androidSigningFixture, run, artifact)
	}
}

func (f *nativeSigningRepairFixture) sign(t *testing.T) (Resource, Resource) {
	t.Helper()
	run := must(t, f.s, f.signer, "tasks.claim", M{})
	if run.Kind != "run" || S(Obj(run.Data["task"]), "signingMode") != controlledSigningMode(f.target) {
		t.Fatal("fresh native signing not claimable", run)
	}
	if f.target == "macos" {
		if S(Obj(run.Data["task"]), "signingPhase") != "" || len(Obj(Obj(run.Data["task"])["signingOperation"])) != 0 || N(Obj(run.Data["task"]), "signingPolls") != 0 {
			t.Fatal("fresh repair inherited an old notarization observation", run)
		}
		must(t, f.s, f.signer, "tasks.complete", macOSSigningPending(f.androidSigningFixture, run))
		if idle := must(t, f.s, f.quality, "tasks.claim", M{}); idle.Kind != "worker" {
			t.Fatal("pending notarization scheduled final QA", idle)
		}
		dueMacOSSigning(t, f.s, S(run.Data, "taskId"))
		run = must(t, f.s, f.signer, "tasks.claim", M{})
		if run.Kind != "run" || S(Obj(run.Data["task"]), "signingPhase") != "status" {
			t.Fatal("repair notarization status did not resume", run)
		}
	}
	artifact := f.uploadSigned(t, run)
	input := f.signingCompletion(run, artifact)
	must(t, f.s, f.signer, "tasks.complete", input)
	must(t, f.s, f.signer, "tasks.complete", input)
	return run, artifact
}

func (f *nativeSigningRepairFixture) rejected(t *testing.T) (Resource, Resource, Resource) {
	t.Helper()
	f.code(t)
	f.unsignedBuild(t)
	sign, artifact := f.sign(t)
	return f.fail(t), sign, artifact
}

func TestNativeSigningRepairFreshExactChain(t *testing.T) {
	for _, target := range []string{"ios-appstore", "macos", "windows"} {
		t.Run(target, func(t *testing.T) {
			f := newNativeSigningRepairFixture(t, target)
			defect, oldSign, oldArtifact := f.rejected(t)
			oldCommit := f.commit
			f.code(t)
			defect, _ = f.s.Store.Get(defect.ID)
			if defect.Status != "awaiting_verification" {
				t.Fatal("native repair must create fresh unsigned/sign/recheck chain", defect.Status, S(defect.Data, "reason"))
			}
			if f.commit == oldCommit || len(Items(defect.Data["signingRepairs"])) != 1 || len(Items(defect.Data["androidSigningRepairs"])) != 0 {
				t.Fatal("missing native versioned repair lineage", defect)
			}
			f.unsignedBuild(t)
			if idle := must(t, f.s, f.quality, "tasks.claim", M{}); idle.Kind != "worker" {
				t.Fatal("unsigned repair got final QA", idle)
			}
			newSign, final := f.sign(t)
			oldInput, freshInput := Obj(Obj(oldSign.Data["task"])["signingInput"]), Obj(Obj(newSign.Data["task"])["signingInput"])
			if S(freshInput, "operationKey") == S(oldInput, "operationKey") || S(freshInput, "artifactId") == S(oldInput, "artifactId") || final.ID == oldArtifact.ID || S(freshInput, "commit") != f.commit {
				t.Fatal("repair reused original input or signature", freshInput)
			}
			for _, field := range []string{"expectedSigner", "expectedApplicationSigner", "expectedTeamId", "expectedProvisioningProfile", "expectedWindowsPublisher", "expectedWindowsPackage", "expectedWindowsVersion"} {
				if S(freshInput, field) != S(oldInput, field) {
					t.Fatal("repair changed approved identity", field)
				}
			}
			qa := must(t, f.s, f.quality, "tasks.claim", M{})
			if qa.Kind != "run" || S(Obj(qa.Data["task"]), "artifactId") != final.ID || S(Obj(qa.Data["task"]), "qualityPolicyId") != f.policy.ID {
				t.Fatal("recheck lost exact final package or approved policy", qa)
			}
			must(t, f.s, f.quality, "tasks.complete", M{"id": qa.ID, "leaseToken": S(qa.Data, "leaseToken"), "exitCode": 0, "result": M{}})
			defect, _ = f.s.Store.Get(defect.ID)
			dispatches, _ := f.s.Store.List(f.project.ID, "quality_dispatch", f.goal.ID)
			evidence, _ := f.s.Store.List(f.project.ID, "evidence", f.goal.ID)
			if defect.Status != "closed" || S(defect.Data, "verifiedRunId") != qa.ID || len(dispatches) != 2 || len(evidence) != 1 || S(evidence[0].Data, "artifactId") != final.ID {
				t.Fatal("incomplete native repaired final-package QA", defect, len(dispatches), len(evidence))
			}
			if _, err := f.s.Command(context.Background(), f.owner, "releases.create", M{"goalId": f.goal.ID, "version": "1.0.0", "commit": f.commit, "artifactIds": []any{final.ID}}); err == nil || !strings.Contains(err.Error(), "目标环境") {
				t.Fatal("protocol fixture CI bypassed real target acceptance", err)
			}
		})
	}
}

func TestNativeSigningRepairTwoRoundsStartAtLatestFailedSource(t *testing.T) {
	for _, target := range []string{"ios-appstore", "macos", "windows"} {
		t.Run(target, func(t *testing.T) {
			f := newNativeSigningRepairFixture(t, target)
			defect, _, previous := f.rejected(t)
			for round := 1; round <= 2; round++ {
				repair, _ := f.s.Store.Get(S(defect.Data, "repairTaskId"))
				if S(repair.Data, "commit") != S(previous.Data, "commit") {
					t.Fatal("repair did not start at latest failed package source", round)
				}
				f.code(t)
				f.unsignedBuild(t)
				_, previous = f.sign(t)
				defect = f.fail(t)
			}
			if defect.Status != "blocked" || !strings.Contains(S(defect.Data, "reason"), "两次") || len(Items(defect.Data["signingRepairs"])) != 2 || S(defect.Data, "artifactId") != previous.ID {
				t.Fatal("native repair was unbounded or lost latest failure", defect)
			}
			if idle := must(t, f.s, f.builder, "tasks.claim", M{}); idle.Kind != "worker" {
				t.Fatal("third repair dispatched", idle)
			}
		})
	}
}

func TestNativeSigningRepairDriftBlocksBeforeClaimAndBudget(t *testing.T) {
	for _, target := range []string{"ios-appstore", "macos", "windows"} {
		identities := []string{"expectedSigner"}
		switch target {
		case "macos":
			identities = append(identities, "expectedApplicationSigner", "expectedTeamId")
		case "ios-appstore":
			identities = append(identities, "expectedTeamId", "expectedProvisioningProfile")
		case "windows":
			identities = append(identities, "expectedWindowsPublisher", "expectedWindowsPackage", "expectedWindowsVersion")
		}
		modes := append([]string{"policy", "pipeline", "plan", "failed-artifact", "record", "missing-id", "mixed-records", "record-version", "record-target", "root-build"}, identities...)
		for _, mode := range modes {
			t.Run(target+"/"+mode, func(t *testing.T) {
				f := newNativeSigningRepairFixture(t, target)
				defect, _, _ := f.rejected(t)
				id := S(defect.Data, "repairTaskId")
				if err := f.s.Store.Update(context.Background(), func(tx *Tx) error {
					d, _ := tx.Get(defect.ID)
					r := Obj(Items(d.Data["signingRepairs"])[0])
					otherID, field, value := S(r, "rootSigningTaskId"), mode, any("changed")
					switch mode {
					case "policy":
						otherID, field = f.policy.ID, "binding"
					case "pipeline":
						otherID, field, value = f.pipeline.ID, "command", []any{"unapproved"}
					case "plan":
						otherID, field, value = f.plan.ID, "tasks", []any{}
					case "failed-artifact":
						otherID, field = S(r, "failedArtifactId"), "commit"
					case "root-build":
						otherID, field, value = S(r, "rootUnsignedTaskId"), "command", []any{"unapproved"}
					case "record":
						r["expectedSigner"] = strings.Repeat("f", 64)
						return tx.Put(d)
					case "record-version", "record-target":
						if mode == "record-version" {
							r["version"] = 1
						} else {
							r["target"] = "android-huawei"
						}
						// Recompute a consistency hash to isolate semantic validation.
						r["binding"] = signingRepairDigest(r)
						return tx.Put(d)
					case "mixed-records":
						d.Data["androidSigningRepairs"] = []any{copyM(r)}
						return tx.Put(d)
					case "missing-id":
						x, _ := tx.Get(id)
						delete(x.Data, "signingRepairId")
						return tx.Put(x)
					}
					x, e := tx.Get(otherID)
					if e != nil {
						return e
					}
					x.Data[field] = value
					return tx.Put(x)
				}); err != nil {
					t.Fatal(err)
				}
				if idle := must(t, f.s, f.builder, "tasks.claim", M{}); idle.Kind != "worker" {
					t.Fatal("drifted authority dispatched repair", mode, idle)
				}
				task, _ := f.s.Store.Get(id)
				if task.Status != "blocked" || N(task.Data, "attempts") != 0 || S(task.Data, "reason") == "" {
					t.Fatal("drift did not block before attempt/budget", mode, task)
				}
			})
		}
	}
}

func TestNativeSigningRepairRevocationStopsActiveWorkAndFinalUse(t *testing.T) {
	for _, target := range []string{"ios-appstore", "macos", "windows"} {
		for _, phase := range []string{"source-upload", "unsigned-upload", "signing-upload", "signing-complete", "signed-artifact-use"} {
			t.Run(target+"/"+phase, func(t *testing.T) {
				f := newNativeSigningRepairFixture(t, target)
				f.rejected(t)
				actor := f.builder
				if phase != "source-upload" {
					f.code(t)
				}
				if strings.HasPrefix(phase, "signing") || phase == "signed-artifact-use" {
					f.unsignedBuild(t)
					actor = f.signer
				}
				run := must(t, f.s, actor, "tasks.claim", M{})
				if run.Kind != "run" {
					t.Fatal("missing repair work", run)
				}
				var artifact Resource
				if phase == "signing-complete" || phase == "signed-artifact-use" {
					artifact = f.uploadSigned(t, run)
				}
				if phase == "signed-artifact-use" {
					must(t, f.s, actor, "tasks.complete", f.signingCompletion(run, artifact))
				}
				must(t, f.s, f.owner, "quality.revoke", M{"id": f.policy.ID, "reason": "Withdraw active native repair", "key": phase})
				if _, err := f.s.Command(context.Background(), actor, "tasks.progress", M{"id": run.ID, "leaseToken": S(run.Data, "leaseToken"), "message": "cannot extend revoked repair"}); err == nil && phase != "signed-artifact-use" {
					t.Fatal("revoked active repair kept its lease")
				}
				var err error
				switch phase {
				case "source-upload":
					b := Obj(Obj(run.Data["task"])["sourceBinding"])
					_, err = f.s.Command(context.Background(), actor, "repositories.commit", M{"id": f.repository.ID, "branch": S(b, "targetBranch"), "baseCommit": S(b, "commit"), "expectedHead": S(b, "expectedHead"), "runId": run.ID, "leaseToken": S(run.Data, "leaseToken"), "message": "revoked", "files": M{"source.txt": "revoked"}})
				case "unsigned-upload", "signing-upload":
					kind := "unsigned-package"
					if actor.WorkerID == f.signer.WorkerID {
						kind = "package"
					}
					_, err = f.s.Upload(context.Background(), actor, M{"goalId": f.goal.ID, "repositoryId": f.repository.ID, "commit": f.commit, "target": target, "type": kind, "name": "revoked.bin", "runId": run.ID, "leaseToken": S(run.Data, "leaseToken")}, bytes.NewBufferString("revoked"))
				case "signing-complete":
					_, err = f.s.Command(context.Background(), actor, "tasks.complete", f.signingCompletion(run, artifact))
				case "signed-artifact-use":
					err = f.s.Store.Update(context.Background(), func(tx *Tx) error { return f.s.validateSigningArtifact(tx, artifact) })
				}
				if err == nil {
					t.Fatal("revoked repair accepted work or final package", phase)
				}
			})
		}
	}
}

func TestNativeSigningRepairUnknownSignatureNeverCreatesAnotherOperation(t *testing.T) {
	for _, target := range []string{"ios-appstore", "macos", "windows"} {
		t.Run(target, func(t *testing.T) {
			f := newNativeSigningRepairFixture(t, target)
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
					t.Fatal("unknown native signature retried", idle)
				}
			}
			task, _ := f.s.Store.Get(S(run.Data, "taskId"))
			if task.Status != "blocked" || N(task.Data, "attempts") != 1 {
				t.Fatal("uncertain operation must keep one blocked attempt", task)
			}
			if _, err := f.s.Command(context.Background(), f.owner, "tasks.retry", M{"id": task.ID}); err == nil {
				t.Fatal("generic retry bypassed signing intent")
			}
			defect, _ = f.s.Store.Get(defect.ID)
			if len(Items(defect.Data["signingRepairs"])) != 1 {
				t.Fatal("unknown signature created another repair round", defect)
			}
		})
	}
}

func TestNativeSigningRepairUnboundFailureCannotAuthorizeNewSignature(t *testing.T) {
	for _, target := range []string{"ios-appstore", "macos", "windows"} {
		t.Run(target, func(t *testing.T) {
			f := newNativeSigningRepairFixture(t, target)
			f.code(t)
			f.unsignedBuild(t)
			_, artifact := f.sign(t)
			must(t, f.s, f.owner, "quality.revoke", M{"id": f.policy.ID, "reason": "One-off check only", "key": "manual-only"})
			must(t, f.s, f.owner, "pipelines.run", M{"pipelineId": f.pipeline.ID, "artifactId": artifact.ID, "criteria": []any{"submit"}})
			defect := f.fail(t)
			if defect.Status != "blocked" || !strings.Contains(S(defect.Data, "reason"), "自动质量绑定") || len(Items(defect.Data["signingRepairs"])) != 0 {
				t.Fatal("one-off native QA silently authorized a fresh signature", defect)
			}
			if idle := must(t, f.s, f.builder, "tasks.claim", M{}); idle.Kind != "worker" {
				t.Fatal("unbound failure dispatched automatic repair", idle)
			}
		})
	}
}

func TestNativeSigningRepairInterruptedSourceUsesExplicitCurrentRecovery(t *testing.T) {
	for _, target := range []string{"ios-appstore", "macos", "windows"} {
		for _, revoke := range []bool{false, true} {
			t.Run(target+"/"+map[bool]string{false: "current", true: "revoked"}[revoke], func(t *testing.T) {
				f := newNativeSigningRepairFixture(t, target)
				defect, _, _ := f.rejected(t)
				run := must(t, f.s, f.builder, "tasks.claim", M{})
				b := Obj(Obj(run.Data["task"])["sourceBinding"])
				body := sourceTar(t, map[string][]byte{"source.txt": []byte("recovered native repair\n")}, "")
				input := M{"key": run.ID, "runId": run.ID, "leaseToken": S(run.Data, "leaseToken"), "branch": S(b, "targetBranch"), "baseCommit": S(b, "commit"), "expectedHead": S(b, "expectedHead"), "message": "Actual repair snapshot before interruption"}
				if _, err := f.s.CommitSnapshot(context.Background(), f.builder, f.repository.ID, input, bytes.NewReader(body)); err != nil {
					t.Fatal(err)
				}
				ops, _ := f.s.Store.List(f.project.ID, "source_commit", f.goal.ID)
				if len(ops) != 1 {
					t.Fatal("missing recoverable source candidate", ops)
				}
				if err := f.s.Store.Update(context.Background(), func(tx *Tx) error {
					r, _ := tx.Get(run.ID)
					r.Data["leaseUntil"] = "2000-01-01T00:00:00Z"
					return tx.Put(r)
				}); err != nil {
					t.Fatal(err)
				}
				if revoke {
					must(t, f.s, f.owner, "quality.revoke", M{"id": f.policy.ID, "reason": "Withdraw interrupted repair", "key": "stop-recovery"})
				}
				decision, err := f.s.Command(context.Background(), f.owner, "sources.reconcile", recoveryInput(ops[0]))
				if revoke {
					if err == nil {
						t.Fatal("revoked repair source was accepted", decision)
					}
					defect, _ = f.s.Store.Get(defect.ID)
					if S(defect.Data, "buildTaskId") != "" {
						t.Fatal("rejected recovery created a signing continuation", defect)
					}
					return
				}
				if err != nil || decision.Status != "confirmed" {
					t.Fatal("explicit current source recovery rejected", err, decision)
				}
				f.commit = S(ops[0].Data, "commit")
				f.unsignedBuild(t)
				_, final := f.sign(t)
				qa := must(t, f.s, f.quality, "tasks.claim", M{})
				if S(Obj(qa.Data["task"]), "artifactId") != final.ID {
					t.Fatal("recovered source did not receive exact final QA", qa)
				}
				must(t, f.s, f.quality, "tasks.complete", M{"id": qa.ID, "leaseToken": S(qa.Data, "leaseToken"), "exitCode": 0, "result": M{}})
				defect, _ = f.s.Store.Get(defect.ID)
				if defect.Status != "closed" {
					t.Fatal("recovered and reverified native repair stayed open", defect)
				}
			})
		}
	}
}

func TestNativeSigningRepairMacOSWaitStopsAfterPolicyRevocation(t *testing.T) {
	f := newNativeSigningRepairFixture(t, "macos")
	f.rejected(t)
	f.code(t)
	f.unsignedBuild(t)
	run := must(t, f.s, f.signer, "tasks.claim", M{})
	must(t, f.s, f.signer, "tasks.complete", macOSSigningPending(f.androidSigningFixture, run))
	must(t, f.s, f.owner, "quality.revoke", M{"id": f.policy.ID, "reason": "Stop waiting native repair", "key": "stop-notary-wait"})
	dueMacOSSigning(t, f.s, S(run.Data, "taskId"))
	if idle := must(t, f.s, f.signer, "tasks.claim", M{}); idle.Kind != "worker" {
		t.Fatal("revoked notarization repair resumed", idle)
	}
	task, _ := f.s.Store.Get(S(run.Data, "taskId"))
	if task.Status != "blocked" || N(task.Data, "attempts") != 1 || N(task.Data, "signingPolls") != 0 {
		t.Fatal("revoked wait used another signing/query attempt", task)
	}
}

package platform

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

type androidSigningFixture struct {
	s                                         *Service
	owner, builder, signer, quality           Actor
	project, goal, repository, plan, unsigned Resource
	commit                                    string
	target                                    string
}

func setupAndroidSigningFixture(t *testing.T, s *Service, owner Actor, project Resource) androidSigningFixture {
	return setupAndroidSigningFixtureTarget(t, s, owner, project, "android-huawei")
}

func setupAndroidSigningFixtureTarget(t *testing.T, s *Service, owner Actor, project Resource, target string) androidSigningFixture {
	return setupAndroidSigningFixtureWithMethod(t, s, owner, project, target, "simulator")
}

func setupAndroidSigningFixtureWithMethod(t *testing.T, s *Service, owner Actor, project Resource, target, method string) androidSigningFixture {
	mode := "android-apk"
	if target == "android-google-play" {
		mode = "android-aab"
	}
	t.Helper()
	g := must(t, s, owner, "goals.create", M{"projectId": project.ID, "title": "Release Android application", "outcome": "Build, sign and independently verify exact APK", "targets": []any{target}, "criteria": []any{M{"id": "submit", "text": "Users can submit feedback", "method": method}}, "budget": 100})
	must(t, s, owner, "goals.approve", M{"id": g.ID})
	repo := must(t, s, owner, "repositories.create", M{"projectId": project.ID, "title": "android-source"})
	repo = must(t, s, owner, "repositories.commit", M{"id": repo.ID, "branch": "feature", "message": "Exact Android source", "files": M{"source.txt": "unsigned fixture bytes\n"}})
	commit := S(repo.Data, "lastCommit")
	plan := must(t, s, owner, "plans.propose", M{"goalId": g.ID, "summary": "Build unsigned, sign on a dedicated node", "tasks": []any{
		M{"id": "unsigned", "title": "Build unsigned APK", "kind": "build", "role": "developer", "runtime": "linux", "repositoryId": repo.ID, "commit": commit, "target": target, "artifactStage": "unsigned", "command": []any{"true"}, "outputPath": "unsigned.apk", "budgetCents": 100},
		M{"id": "sign", "title": "Sign exact APK", "kind": "build", "role": "release", "runtime": "linux", "target": target, "dependsOn": []any{"unsigned"}, "signingMode": mode, "inputArtifactFrom": "unsigned", "expectedSigner": strings.Repeat("a", 64), "budgetCents": 100},
	}})
	must(t, s, owner, "plans.approve", M{"id": plan.ID})
	g, _ = s.Store.Get(g.ID)
	worker := func(role string, trusted bool) Actor {
		w := must(t, s, owner, "workers.register", M{"projectId": project.ID, "title": role + " node", "roles": []any{role}, "runtime": "linux", "capabilities": []any{map[bool]string{true: "test", false: "build"}[trusted]}, "trustedQuality": trusted})
		return Actor{ID: "worker:" + w.ID, Kind: "worker", ProjectID: project.ID, WorkerID: w.ID, Roles: []string{role}}
	}
	return androidSigningFixture{s: s, owner: owner, builder: worker("developer", false), signer: worker("release", false), quality: worker("quality", true), project: project, goal: g, repository: repo, plan: plan, commit: commit, target: target}
}

func newAndroidSigningFixture(t *testing.T) androidSigningFixture {
	s, owner, project := fixture(t)
	return setupAndroidSigningFixture(t, s, owner, project)
}

func (f *androidSigningFixture) build(t *testing.T) Resource {
	t.Helper()
	run := must(t, f.s, f.builder, "tasks.claim", M{})
	var err error
	f.unsigned, err = f.s.Upload(context.Background(), f.builder, M{"goalId": f.goal.ID, "repositoryId": f.repository.ID, "commit": f.commit, "target": f.target, "type": "unsigned-package", "name": "unsigned.apk", "runId": run.ID, "leaseToken": S(run.Data, "leaseToken")}, bytes.NewBufferString("unsigned fixture bytes\n"))
	if err != nil {
		t.Fatal(err)
	}
	return must(t, f.s, f.builder, "tasks.complete", M{"id": run.ID, "leaseToken": S(run.Data, "leaseToken"), "exitCode": 0, "result": M{"artifactId": f.unsigned.ID, "repositoryId": f.repository.ID, "commit": f.commit}})
}

func (f androidSigningFixture) uploadSigned(t *testing.T, run Resource) Resource {
	t.Helper()
	a, err := f.s.Upload(context.Background(), f.signer, M{"goalId": f.goal.ID, "repositoryId": f.repository.ID, "commit": f.commit, "target": f.target, "type": "package", "name": "signed.apk", "runId": run.ID, "leaseToken": S(run.Data, "leaseToken")}, bytes.NewBufferString("signed fixture bytes\n"))
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func (f androidSigningFixture) completion(run, artifact Resource) M {
	b := Obj(Obj(run.Data["task"])["signingInput"])
	return M{"id": run.ID, "leaseToken": S(run.Data, "leaseToken"), "exitCode": 0, "result": M{"artifactId": artifact.ID, "repositoryId": f.repository.ID, "commit": f.commit, "signingInputDigest": S(b, "digest"), "operationKey": S(b, "operationKey"), "certificateSHA256": strings.Repeat("a", 64), "payloadDigest": strings.Repeat("b", 64), "requestBinding": strings.Repeat("c", 64)}}
}

func TestAndroidSigningPlansRejectForgedAndUnsafeTransforms(t *testing.T) {
	base := M{"id": "unsigned", "title": "Unsigned build", "kind": "build", "role": "developer", "runtime": "linux", "target": "android-huawei", "artifactStage": "unsigned"}
	sign := M{"id": "sign", "title": "Trusted signing", "kind": "build", "role": "release", "runtime": "linux", "target": "android-huawei", "dependsOn": []any{"unsigned"}, "signingMode": "android-apk", "inputArtifactFrom": "unsigned", "expectedSigner": strings.Repeat("a", 64)}
	for name, change := range map[string]M{
		"command": {"command": []any{"true"}}, "output": {"outputPath": "output.apk"}, "developer": {"role": "developer"},
		"web": {"target": "web"}, "mode": {"signingMode": "unknown"}, "source": {"inputArtifactFrom": "foreign"},
		"not-ancestor": {"dependsOn": []any{}}, "certificate": {"expectedSigner": "debug"}, "case-cert": {"expectedSigner": strings.Repeat("A", 64)},
		"raw-input": {"signingInput": M{}}, "raw-binding": {"signingInputDigest": strings.Repeat("a", 64)}, "stage": {"artifactStage": "unsigned"},
	} {
		t.Run(name, func(t *testing.T) {
			m := copyM(sign)
			for k, v := range change {
				m[k] = v
			}
			if _, err := validateTasks([]any{base, m}); err == nil {
				t.Fatal("unsafe signing plan accepted", name)
			}
		})
	}
	for _, field := range []string{"inputArtifactFrom", "expectedSigner", "signingInput", "signingMode"} {
		m := copyM(base)
		m[field] = sign[field]
		if _, err := validateTasks([]any{m}); err == nil {
			t.Fatal("ordinary build accepted signing field", field)
		}
	}
	if _, err := validateTasks([]any{base, sign}); err != nil {
		t.Fatal(err)
	}
}

func TestAndroidSigningDerivesExactLeaseAndFreshQuality(t *testing.T) {
	f := newAndroidSigningFixture(t)
	if idle := must(t, f.s, f.signer, "tasks.claim", M{}); idle.Kind != "worker" {
		t.Fatal("signed before dependency", idle)
	}
	build := f.build(t)
	pipeline := must(t, f.s, f.owner, "pipelines.create", M{"projectId": f.project.ID, "title": "Actual Android simulator verification", "runtime": "linux", "method": "simulator", "verificationEnvironment": "simulator", "command": []any{"true"}})
	must(t, f.s, f.owner, "quality.bind", M{"goalId": f.goal.ID, "expectedRequirementRevision": f.goal.Revision, "expectedPlanId": f.plan.ID, "target": "android-huawei", "pipelineId": pipeline.ID, "criteria": []any{"submit"}, "budgetCents": 100, "key": "signed-quality"})
	if idle := must(t, f.s, f.quality, "tasks.claim", M{}); idle.Kind != "worker" {
		t.Fatal("unsigned package got QA", idle)
	}
	if _, err := f.s.Command(context.Background(), f.owner, "pipelines.run", M{"pipelineId": pipeline.ID, "artifactId": f.unsigned.ID, "criteria": []any{"submit"}}); err == nil {
		t.Fatal("unsigned package accepted by manual pipeline")
	}
	run := must(t, f.s, f.signer, "tasks.claim", M{})
	b := Obj(Obj(run.Data["task"])["signingInput"])
	if run.Kind != "run" || S(b, "artifactId") != f.unsigned.ID || S(b, "artifactDigest") != S(f.unsigned.Data, "digest") || S(b, "producerRunId") != build.ID || S(b, "commit") != f.commit || S(b, "repositoryId") != f.repository.ID || S(b, "digest") == "" {
		t.Fatal("missing authoritative exact signing binding", run)
	}
	a := f.uploadSigned(t, run)
	if S(a.Data, "inputArtifactId") != f.unsigned.ID || S(a.Data, "signingInputDigest") != S(b, "digest") || S(a.Data, "expectedSigner") != strings.Repeat("a", 64) {
		t.Fatal("upload not derived from lease", a)
	}
	completed := must(t, f.s, f.signer, "tasks.complete", f.completion(run, a))
	if replay := must(t, f.s, f.signer, "tasks.complete", f.completion(run, a)); replay.ID != completed.ID {
		t.Fatal("duplicate completion")
	}
	quality := must(t, f.s, f.quality, "tasks.claim", M{})
	if quality.Kind != "run" || S(Obj(quality.Data["task"]), "artifactId") != a.ID {
		t.Fatal("signed exact package not independently scheduled", quality)
	}
	must(t, f.s, f.quality, "tasks.complete", M{"id": quality.ID, "leaseToken": S(quality.Data, "leaseToken"), "exitCode": 0, "result": M{}})
	if _, err := f.s.Command(context.Background(), f.owner, "releases.create", M{"goalId": f.goal.ID, "version": "1.0.0", "commit": f.commit, "artifactIds": []any{a.ID}}); err == nil || !strings.Contains(err.Error(), "目标环境") {
		t.Fatal("simulator bypassed physical Android gate", err)
	}
	releases, _ := f.s.Store.List(f.project.ID, "release", f.goal.ID)
	if len(releases) != 0 {
		t.Fatal("persisted unverified release")
	}
}

func TestAndroidSigningRejectsInputAndLeaseDrift(t *testing.T) {
	for _, mode := range []string{"artifact-bytes", "input-revision", "producer-run", "input-target", "input-type", "input-project", "input-commit", "input-binding", "source-binding", "worker-role", "worker-capability", "worker-runtime", "requirement", "plan", "lease"} {
		t.Run(mode, func(t *testing.T) {
			f := newAndroidSigningFixture(t)
			f.build(t)
			run := must(t, f.s, f.signer, "tasks.claim", M{})
			artifact := f.uploadSigned(t, run)
			if mode == "artifact-bytes" {
				if err := os.WriteFile(f.s.ArtifactPath(S(f.unsigned.Data, "digest")), []byte("corrupted"), 0600); err != nil {
					t.Fatal(err)
				}
			} else if mode == "requirement" {
				must(t, f.s, f.owner, "goals.revise", M{"id": f.goal.ID, "reason": "Changed baseline", "outcome": "New acceptance"})
			} else if mode == "plan" {
				next := must(t, f.s, f.owner, "plans.propose", M{"goalId": f.goal.ID, "summary": "New approved plan", "tasks": []any{M{"id": "next", "title": "Review scope", "kind": "analysis", "role": "analysis", "runtime": "linux"}}})
				must(t, f.s, f.owner, "plans.approve", M{"id": next.ID})
			} else {
				err := f.s.Store.Update(context.Background(), func(tx *Tx) error {
					if strings.HasPrefix(mode, "worker-") {
						w, _ := tx.Get(f.signer.WorkerID)
						key, value := "roles", any([]any{"developer"})
						if mode == "worker-capability" {
							key, value = "capabilities", []any{"publish"}
						}
						if mode == "worker-runtime" {
							key, value = "runtime", "darwin"
						}
						w.Data[key] = value
						return tx.Put(w)
					}
					if mode == "lease" {
						r, _ := tx.Get(run.ID)
						r.Data["leaseUntil"] = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
						return tx.Put(r)
					}
					if mode == "input-binding" || mode == "source-binding" {
						task, _ := tx.Get(S(run.Data, "taskId"))
						if mode == "input-binding" {
							Obj(task.Data["signingInput"])["artifactId"] = "artifact_other"
						} else {
							Obj(task.Data["sourceBinding"])["commit"] = strings.Repeat("d", 40)
						}
						return tx.Put(task)
					}
					a, _ := tx.Get(f.unsigned.ID)
					switch mode {
					case "input-revision":
						a.Data["requirementRevision"] = f.goal.Revision + 1
					case "producer-run":
						a.Data["runId"] = "run_foreign"
					case "input-target":
						a.Data["target"] = "android-xiaomi"
					case "input-type":
						a.Data["type"] = "package"
					case "input-project":
						a.ProjectID = "project_foreign"
					case "input-commit":
						a.Data["commit"] = strings.Repeat("d", 40)
					}
					return tx.Put(a)
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.s.Command(context.Background(), f.signer, "tasks.complete", f.completion(run, artifact)); err == nil {
				t.Fatal("accepted drift", mode)
			}
		})
	}
}

func TestAndroidSigningCannotRelabelOrForgeProvenance(t *testing.T) {
	f := newAndroidSigningFixture(t)
	run := must(t, f.s, f.builder, "tasks.claim", M{})
	meta := M{"goalId": f.goal.ID, "repositoryId": f.repository.ID, "commit": f.commit, "target": "android-huawei", "type": "package", "name": "pretend-signed.apk", "runId": run.ID, "leaseToken": S(run.Data, "leaseToken")}
	if _, err := f.s.Upload(context.Background(), f.builder, meta, bytes.NewBufferString("bytes")); err == nil {
		t.Fatal("unsigned build forged final package")
	}
	delete(meta, "runId")
	delete(meta, "leaseToken")
	meta["type"] = "unsigned-package"
	if _, err := f.s.Upload(context.Background(), f.owner, meta, bytes.NewBufferString("bytes")); err == nil {
		t.Fatal("human forged unsigned producer")
	}
	meta["type"] = "package"
	meta["signingInputDigest"] = strings.Repeat("a", 64)
	if _, err := f.s.Upload(context.Background(), f.owner, meta, bytes.NewBufferString("bytes")); err == nil {
		t.Fatal("human forged signing provenance")
	}
}

func TestAndroidSigningUncertainLeaseAndWrongReceiptNeverResign(t *testing.T) {
	for _, mode := range []string{"certificateSHA256", "signingInputDigest", "operationKey", "payloadDigest", "requestBinding", "expired"} {
		t.Run(mode, func(t *testing.T) {
			f := newAndroidSigningFixture(t)
			f.build(t)
			run := must(t, f.s, f.signer, "tasks.claim", M{})
			a := f.uploadSigned(t, run)
			if mode == "expired" {
				if err := f.s.Store.Update(context.Background(), func(tx *Tx) error {
					r, _ := tx.Get(run.ID)
					r.Data["leaseUntil"] = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
					return tx.Put(r)
				}); err != nil {
					t.Fatal(err)
				}
				if idle := must(t, f.s, f.signer, "tasks.claim", M{}); idle.Kind != "worker" {
					t.Fatal("expired signing automatically retried", idle)
				}
				task, _ := f.s.Store.Get(S(run.Data, "taskId"))
				if task.Status != "blocked" {
					t.Fatal("uncertain signing not blocked", task)
				}
				if _, err := f.s.Command(context.Background(), f.owner, "tasks.retry", M{"id": task.ID}); err == nil {
					t.Fatal("generic retry reauthorized uncertain signing")
				}
			} else {
				input := f.completion(run, a)
				Obj(input["result"])[mode] = "wrong"
				if _, err := f.s.Command(context.Background(), f.signer, "tasks.complete", input); err == nil {
					t.Fatal("accepted wrong signing receipt", mode)
				}
			}
		})
	}
}

func TestAndroidSigningRefusesChangedInputBeforeClaimAndMixedNode(t *testing.T) {
	for _, mode := range []string{"input", "mixed-role", "mixed-capability"} {
		t.Run(mode, func(t *testing.T) {
			f := newAndroidSigningFixture(t)
			f.build(t)
			if err := f.s.Store.Update(context.Background(), func(tx *Tx) error {
				if mode == "input" {
					a, _ := tx.Get(f.unsigned.ID)
					a.Data["type"] = "package"
					return tx.Put(a)
				}
				w, _ := tx.Get(f.signer.WorkerID)
				if mode == "mixed-role" {
					w.Data["roles"] = []any{"release", "developer"}
				} else {
					w.Data["capabilities"] = []any{"build", "code"}
				}
				return tx.Put(w)
			}); err != nil {
				t.Fatal(err)
			}
			if idle := must(t, f.s, f.signer, "tasks.claim", M{}); idle.Kind != "worker" {
				t.Fatal("untrusted input or node claimed signing", idle)
			}
		})
	}
}

func TestAndroidSigningNodeNeverClaimsOrdinaryRepositoryBuild(t *testing.T) {
	f := newAndroidSigningFixture(t)
	plan := must(t, f.s, f.owner, "plans.propose", M{"goalId": f.goal.ID, "summary": "Ordinary release-role repository build", "tasks": []any{
		M{"id": "ordinary", "title": "Repository build on release role", "kind": "build", "role": "release", "runtime": "linux", "repositoryId": f.repository.ID, "commit": f.commit, "target": "android-huawei", "command": []any{"true"}, "outputPath": "output.apk"},
	}})
	must(t, f.s, f.owner, "plans.approve", M{"id": plan.ID})
	if idle := must(t, f.s, f.signer, "tasks.claim", M{}); idle.Kind != "worker" {
		t.Fatal("dedicated signer claimed ordinary repository execution", idle.Kind)
	}
}

func TestAndroidSigningQualityRequiresConfirmedOriginalReceipt(t *testing.T) {
	for _, mode := range []string{"unconfirmed", "receipt-drift"} {
		t.Run(mode, func(t *testing.T) {
			f := newAndroidSigningFixture(t)
			f.build(t)
			pipeline := must(t, f.s, f.owner, "pipelines.create", M{"projectId": f.project.ID, "title": "Verify final signing provenance", "runtime": "linux", "method": "simulator", "verificationEnvironment": "simulator", "command": []any{"true"}})
			must(t, f.s, f.owner, "quality.bind", M{"goalId": f.goal.ID, "expectedRequirementRevision": f.goal.Revision, "expectedPlanId": f.plan.ID, "target": "android-huawei", "pipelineId": pipeline.ID, "criteria": []any{"submit"}, "budgetCents": 100, "key": mode})
			run := must(t, f.s, f.signer, "tasks.claim", M{})
			artifact := f.uploadSigned(t, run)
			if mode == "unconfirmed" {
				if _, err := f.s.Command(context.Background(), f.owner, "pipelines.run", M{"pipelineId": pipeline.ID, "artifactId": artifact.ID, "criteria": []any{"submit"}}); err == nil {
					t.Fatal("unconfirmed signing got verification authority")
				}
				return
			}
			must(t, f.s, f.signer, "tasks.complete", f.completion(run, artifact))
			if err := f.s.Store.Update(context.Background(), func(tx *Tx) error {
				r, _ := tx.Get(run.ID)
				Obj(r.Data["result"])["certificateSHA256"] = strings.Repeat("d", 64)
				return tx.Put(r)
			}); err != nil {
				t.Fatal(err)
			}
			if idle := must(t, f.s, f.quality, "tasks.claim", M{}); idle.Kind != "worker" {
				t.Fatal("changed signing receipt got automatic verification", idle)
			}
			dispatch, _ := f.s.Store.List(f.project.ID, "quality_dispatch", f.goal.ID)
			if len(dispatch) != 1 || dispatch[0].Status != "blocked" {
				t.Fatal("missing durable signing-provenance blocker", dispatch)
			}
		})
	}
}

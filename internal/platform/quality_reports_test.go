package platform

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestQualityReportProtectedPaths(t *testing.T) {
	f := newQualityFixture(t)
	base := M{"projectId": f.project.ID, "title": "Archived browser verification", "command": []any{"true"}, "runtime": "linux", "method": "automated", "verificationEnvironment": "browser"}
	for _, path := range []any{"/tmp/report", "../report", "a/../report", "a//report", "./report", ".", "C:/report", "a\\report", ".git/config", ".intelligence-artifact", "a/.intelligence-result", "report\n.json", 42, nil} {
		in := copyM(base)
		in["reportPath"] = path
		if _, err := f.s.Command(context.Background(), f.quality, "pipelines.create", in); err == nil {
			t.Errorf("unsafe report path accepted: %#v", path)
		}
	}
	in := copyM(base)
	in["reportPath"] = "quality/browser.tar.gz"
	pipe := must(t, f.s, f.quality, "pipelines.create", in)
	if S(pipe.Data, "reportPath") != "quality/browser.tar.gz" {
		t.Fatal("protected pipeline lost report collection contract", pipe)
	}
	for _, a := range []Actor{f.developer, f.verifier, {ID: "qa-agent", Kind: "agent", ProjectID: f.project.ID, Roles: []string{"quality"}}} {
		if _, err := f.s.Command(context.Background(), a, "pipelines.create", in); err == nil {
			t.Fatal("non-human changed protected report collection", a)
		}
	}
	if _, err := f.s.Command(context.Background(), f.owner, "plans.propose", M{"goalId": f.goal.ID, "summary": "Untrusted report path", "tasks": []any{M{"id": "untrusted", "title": "Override collection", "kind": "analysis", "role": "developer", "runtime": "linux", "reportPath": "forged.json"}}}); err == nil {
		t.Fatal("external plan overrode protected report path")
	}
}

func reportFixture(t *testing.T) (qualityFixture, Resource, Resource) {
	t.Helper()
	f := newQualityFixture(t)
	f.pipeline = must(t, f.s, f.quality, "pipelines.create", M{"projectId": f.project.ID, "title": "Browser report", "command": []any{"true"}, "reportPath": "browser.tar.gz", "runtime": "linux", "method": "automated", "verificationEnvironment": "browser"})
	must(t, f.s, f.quality, "quality.bind", f.input("report-policy"))
	_, artifact, _ := f.build(t)
	run := must(t, f.s, f.verifier, "tasks.claim", M{})
	return f, artifact, run
}

func uploadQualityReport(t *testing.T, f qualityFixture, run Resource) Resource {
	t.Helper()
	report, err := f.s.Upload(context.Background(), f.verifier, M{"goalId": f.goal.ID, "repositoryId": f.repository.ID, "commit": f.commit, "target": "web", "name": "browser.tar.gz", "type": "report", "runId": run.ID, "leaseToken": S(run.Data, "leaseToken")}, bytes.NewBufferString("actual browser report bytes"))
	if err != nil {
		t.Fatal(err)
	}
	return report
}

func TestQualityReportRequiredAndExactProvenance(t *testing.T) {
	for _, mode := range []string{"missing", "wrong-run", "wrong-worker", "wrong-project", "wrong-goal", "wrong-revision", "wrong-repository", "wrong-commit", "wrong-target", "wrong-package", "wrong-type", "corrupt"} {
		t.Run(mode, func(t *testing.T) {
			f, _, run := reportFixture(t)
			result := M{}
			if mode != "missing" {
				report := uploadQualityReport(t, f, run)
				result["reportArtifactId"] = report.ID
				if mode == "corrupt" {
					if err := os.WriteFile(f.s.ArtifactPath(S(report.Data, "digest")), []byte("replaced"), 0600); err != nil {
						t.Fatal(err)
					}
				} else if err := f.s.Store.Update(context.Background(), func(tx *Tx) error {
					r, err := tx.Get(report.ID)
					if err != nil {
						return err
					}
					switch mode {
					case "wrong-run":
						r.Data["runId"] = "different-run"
					case "wrong-worker":
						r.Data["producerId"] = f.developer.ID
					case "wrong-project":
						r.ProjectID = "other-project"
					case "wrong-goal":
						r.GoalID = "other-goal"
					case "wrong-revision":
						r.Data["requirementRevision"] = f.goal.Revision + 1
					case "wrong-repository":
						r.Data["repositoryId"] = "other-repo"
					case "wrong-commit":
						r.Data["commit"] = "0000000000000000000000000000000000000000"
					case "wrong-target":
						r.Data["target"] = "macos-direct"
					case "wrong-package":
						r.Data["testedArtifactId"] = "another-package"
					case "wrong-type":
						r.Data["type"] = "package"
					}
					return tx.Put(r)
				}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.s.Command(context.Background(), f.verifier, "tasks.complete", M{"id": run.ID, "leaseToken": S(run.Data, "leaseToken"), "exitCode": 0, "result": result}); err == nil {
				t.Fatal("unbound or missing report authorized passing evidence", mode)
			}
			evidence, _ := f.s.Store.List(f.project.ID, "evidence", f.goal.ID)
			stored, _ := f.s.Store.Get(run.ID)
			if len(evidence) != 0 || stored.Status != "running" {
				t.Fatal("invalid report partially completed transaction", evidence, stored)
			}
		})
	}
}

func TestQualityReportEvidenceAndReleaseRecheck(t *testing.T) {
	for _, mode := range []string{"valid", "corrupt", "borrowed", "missing-proof", "wrong-digest", "changed-policy"} {
		t.Run(mode, func(t *testing.T) {
			f, artifact, run := reportFixture(t)
			report := uploadQualityReport(t, f, run)
			if S(report.Data, "testedArtifactId") != artifact.ID {
				t.Error("upload was not bound by the controller to its exact package", report)
			}
			finished := must(t, f.s, f.verifier, "tasks.complete", M{"id": run.ID, "leaseToken": S(run.Data, "leaseToken"), "exitCode": 0, "result": M{"reportArtifactId": report.ID}})
			ev, err := f.s.Store.Get(S(finished.Data, "evidenceId"))
			if err != nil || S(ev.Data, "proofArtifactId") != report.ID || S(ev.Data, "proofDigest") != S(report.Data, "digest") {
				t.Fatal("evidence lost report provenance", ev, err)
			}
			if mode == "corrupt" {
				if err := os.WriteFile(f.s.ArtifactPath(S(report.Data, "digest")), []byte("changed"), 0600); err != nil {
					t.Fatal(err)
				}
			} else if mode != "valid" {
				if err := f.s.Store.Update(context.Background(), func(tx *Tx) error {
					switch mode {
					case "borrowed":
						report.Data["runId"] = "other-run"
						return tx.Put(report)
					case "missing-proof":
						delete(ev.Data, "proofArtifactId")
						return tx.Put(ev)
					case "wrong-digest":
						ev.Data["proofDigest"] = "0000000000000000000000000000000000000000000000000000000000000000"
						return tx.Put(ev)
					case "changed-policy":
						f.pipeline.Data["reportPath"] = "replacement.json"
						return tx.Put(f.pipeline)
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			_, err = f.s.Command(context.Background(), f.owner, "releases.create", M{"goalId": f.goal.ID, "version": "1.0.0", "commit": f.commit, "artifactIds": []any{artifact.ID}})
			if mode == "valid" && err != nil {
				t.Fatal("accurate report did not authorize candidate", err)
			}
			if mode != "valid" && err == nil {
				t.Fatal("invalidated report authorized release", mode)
			}
		})
	}
}

func TestQualityReportUnboundPipelineStillRechecksProofAtRelease(t *testing.T) {
	f := newQualityFixture(t)
	f.pipeline = must(t, f.s, f.quality, "pipelines.create", M{"projectId": f.project.ID, "title": "Manual quality run with report", "command": []any{"true"}, "reportPath": "report.json", "runtime": "linux", "method": "automated", "verificationEnvironment": "browser"})
	_, artifact, _ := f.build(t)
	must(t, f.s, f.quality, "pipelines.run", M{"pipelineId": f.pipeline.ID, "artifactId": artifact.ID, "criteria": []any{"ticket"}})
	run := must(t, f.s, f.verifier, "tasks.claim", M{})
	report := uploadQualityReport(t, f, run)
	must(t, f.s, f.verifier, "tasks.complete", M{"id": run.ID, "leaseToken": S(run.Data, "leaseToken"), "exitCode": 0, "result": M{"reportArtifactId": report.ID}})
	input := M{"goalId": f.goal.ID, "version": "1.0.0", "commit": f.commit, "artifactIds": []any{artifact.ID}}
	must(t, f.s, f.owner, "releases.create", input)
	if err := os.WriteFile(f.s.ArtifactPath(S(report.Data, "digest")), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	input["version"] = "1.0.1"
	if _, err := f.s.Command(context.Background(), f.owner, "releases.create", input); err == nil {
		t.Fatal("unbound report bypassed integrity gate")
	}
}

func verifyQualityReportRestore(t *testing.T, f qualityFixture) {
	t.Helper()
	f.pipeline = must(t, f.s, f.quality, "pipelines.create", M{"projectId": f.project.ID, "title": "Durable report", "command": []any{"true"}, "reportPath": "report.json", "runtime": "linux", "method": "automated", "verificationEnvironment": "browser"})
	policy := must(t, f.s, f.quality, "quality.bind", f.input("durable-report"))
	_, artifact, _ := f.build(t)
	run := must(t, f.s, f.verifier, "tasks.claim", M{})
	report := uploadQualityReport(t, f, run)
	finished := must(t, f.s, f.verifier, "tasks.complete", M{"id": run.ID, "leaseToken": S(run.Data, "leaseToken"), "exitCode": 0, "result": M{"reportArtifactId": report.ID}})
	archive := filepath.Join(t.TempDir(), "reports.tar.gz")
	if err := f.s.Backup(context.Background(), archive); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "restored")
	if err := Restore(context.Background(), archive, destination, ""); err != nil {
		t.Fatal(err)
	}
	store, err := Open(filepath.Join(destination, "platform.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	restored := &Service{Store: store, Root: destination}
	for _, id := range []string{policy.ID, artifact.ID, report.ID, run.ID, S(finished.Data, "evidenceId")} {
		before, err := f.s.Store.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		after, err := store.Get(id)
		if err != nil || Hash(before) != Hash(after) {
			t.Fatal("restore lost report provenance", before, after, err)
		}
	}
	if err := restored.VerifyArtifact(report); err != nil {
		t.Fatal(err)
	}
	must(t, restored, f.owner, "releases.create", M{"goalId": f.goal.ID, "version": "1.0.0", "commit": f.commit, "artifactIds": []any{artifact.ID}})
	if idle := must(t, restored, f.verifier, "tasks.claim", M{}); idle.Kind != "worker" {
		t.Fatal("restore repeated completed quality", idle)
	}
}

func TestQualityReportArchiveRestoresExactReportAndCandidateAuthority(t *testing.T) {
	verifyQualityReportRestore(t, newQualityFixture(t))
}

func TestQualityReportFailureKeepsReportButRejectsBorrowing(t *testing.T) {
	for _, borrowed := range []bool{false, true} {
		f, _, run := reportFixture(t)
		report := uploadQualityReport(t, f, run)
		if borrowed {
			if err := f.s.Store.Update(context.Background(), func(tx *Tx) error { report.Data["runId"] = "old-run"; return tx.Put(report) }); err != nil {
				t.Fatal(err)
			}
		}
		finished, err := f.s.Command(context.Background(), f.verifier, "tasks.complete", M{"id": run.ID, "leaseToken": S(run.Data, "leaseToken"), "exitCode": 7, "result": M{"reportArtifactId": report.ID}})
		if borrowed && err == nil {
			t.Fatal("failed result accepted borrowed report")
		}
		if !borrowed && (err != nil || finished.Status != "failed" || S(Obj(finished.Data["result"]), "reportArtifactId") != report.ID) {
			t.Fatal("failed command lost real report or exit", finished, err)
		}
	}
}

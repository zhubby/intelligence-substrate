package platform

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func readTrace(t *testing.T, f qualityFixture, rev int) AcceptanceTrace {
	t.Helper()
	r, err := f.s.AcceptanceTrace(context.Background(), f.owner, f.project.ID, f.goal.ID, rev)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestAcceptanceTraceRejectsInexactOrInvalidEvidence(t *testing.T) {
	for _, mode := range []string{"package-bytes", "report-bytes", "digest", "commit", "target", "revision", "self-verification", "stale", "publication", "method", "report-scope", "source"} {
		t.Run(mode, func(t *testing.T) {
			f, artifact, run := reportFixture(t)
			proof := uploadQualityReport(t, f, run)
			finished := must(t, f.s, f.verifier, "tasks.complete", M{"id": run.ID, "leaseToken": S(run.Data, "leaseToken"), "exitCode": 0, "result": M{"reportArtifactId": proof.ID}})
			before := readTrace(t, f, 0)
			if !before.Criteria[0].Targets[0].Covered || !before.Artifacts[0].EnvironmentVerified {
				t.Fatal("fixture missing independent verification", before)
			}
			if mode == "package-bytes" || mode == "report-bytes" {
				a := artifact
				if mode == "report-bytes" {
					a = proof
				}
				if err := os.WriteFile(f.s.ArtifactPath(S(a.Data, "digest")), []byte("corrupted"), 0600); err != nil {
					t.Fatal(err)
				}
			} else if err := f.s.Store.Update(context.Background(), func(tx *Tx) error {
				ev, err := tx.Get(S(finished.Data, "evidenceId"))
				if err != nil {
					return err
				}
				switch mode {
				case "digest":
					ev.Data["digest"] = strings.Repeat("0", 64)
				case "commit":
					ev.Data["commit"] = strings.Repeat("0", 40)
				case "target":
					ev.Data["target"] = "ios-appstore"
				case "revision":
					ev.Data["requirementRevision"] = 2
				case "self-verification":
					ev.Data["actorId"] = S(artifact.Data, "producerId")
				case "stale":
					ev.Status = "stale"
				case "publication":
					ev.Data["purpose"] = "publication"
				case "method":
					ev.Data["method"] = "simulator"
				case "source":
					ev.Data["source"] = "arbitrary-assertion"
				case "report-scope":
					proof.ProjectID = "outside-project"
					return tx.Put(proof)
				}
				return tx.Put(ev)
			}); err != nil {
				t.Fatal(err)
			}
			after := readTrace(t, f, 0)
			if after.Criteria[0].Targets[0].Covered {
				t.Fatal("invalid evidence covered acceptance", mode, after)
			}
			if len(after.Criteria[0].Targets[0].Gaps) == 0 {
				t.Fatal("missing actionable gap", mode, after)
			}
		})
	}
}

func TestAcceptanceTraceLogOnlyEvidenceRequiresOriginalSuccessfulRun(t *testing.T) {
	f := newQualityFixture(t)
	must(t, f.s, f.quality, "quality.bind", f.input("log-only"))
	f.build(t)
	run := must(t, f.s, f.verifier, "tasks.claim", M{})
	finished := must(t, f.s, f.verifier, "tasks.complete", M{"id": run.ID, "leaseToken": S(run.Data, "leaseToken"), "exitCode": 0, "result": M{}})
	r := readTrace(t, f, 0)
	ev := r.Artifacts[0].Evidence[0]
	if !r.Criteria[0].Targets[0].Covered || ev.ReportID != "" || ev.ReportVerified || ev.RunID != run.ID || ev.TaskID == "" {
		t.Fatal("log-only evidence invented an attachment", r)
	}
	if err := f.s.Store.Update(context.Background(), func(tx *Tx) error {
		run.Status = "failed"
		return tx.Put(run)
	}); err != nil {
		t.Fatal(err)
	}
	r = readTrace(t, f, 0)
	if r.Criteria[0].Targets[0].Covered || r.Artifacts[0].Evidence[0].Usable {
		t.Fatal("log-only evidence survived a missing original success", finished.ID, r)
	}
}

func TestAcceptanceTraceBuildUsesActualPlanAndFrozenDesign(t *testing.T) {
	f := newQualityFixture(t)
	prototype, err := f.s.Upload(context.Background(), f.owner, M{"goalId": f.goal.ID, "repositoryId": f.repository.ID, "commit": f.commit, "target": "web", "type": "prototype", "name": "design.html"}, strings.NewReader("<button>Trace prototype</button>"))
	if err != nil {
		t.Fatal(err)
	}
	design := must(t, f.s, f.owner, "designs.propose", designInput(f.goal, prototype, "build-design"))
	in := M{"goalId": f.goal.ID, "summary": "Build approved design", "tasks": Items(f.plan.Data["tasks"]), "designId": design.ID, "expectedDesignBinding": S(design.Data, "binding")}
	f.plan = must(t, f.s, f.owner, "plans.propose", in)
	f.plan = must(t, f.s, f.owner, "plans.approve", M{"id": f.plan.ID})
	f.goal, _ = f.s.Store.Get(f.goal.ID)
	f.build(t)
	later := designInput(f.goal, prototype, "unapproved-later-design")
	later["title"] = "Later unapproved design"
	must(t, f.s, f.owner, "designs.propose", later)
	r := readTrace(t, f, 0)
	build := r.Artifacts[0].Build
	if build == nil || !build.Verified || build.PlanID != f.plan.ID || build.DesignID != design.ID || build.ApprovalID == "" || build.DesignDigest != Hash(f.plan.Data["design"]) || len(build.PrototypeIDs) != 1 || build.PrototypeIDs[0] != prototype.ID {
		body, _ := json.Marshal(build)
		t.Fatal("trace borrowed a live design or dropped original approval", string(body))
	}
	body, _ := json.Marshal(r)
	for _, hidden := range []string{"leaseToken", "completionDigest", "reservedCents", "sourceBinding", "IS_ARTIFACT_PATH"} {
		if strings.Contains(string(body), hidden) {
			t.Fatal("report included unbounded execution payload", hidden)
		}
	}
}

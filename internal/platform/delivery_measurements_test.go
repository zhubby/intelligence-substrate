package platform

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

type metricFixture struct {
	s                                                 *Service
	owner, qa                                         Actor
	project, goal, artifact, proof, evidence, release Resource
}

func newMetricFixture(t *testing.T) metricFixture {
	t.Helper()
	s, owner, project := fixture(t)
	return metricFixtureOn(t, s, owner, project)
}

func metricFixtureOn(t *testing.T, s *Service, owner Actor, project Resource) metricFixture {
	t.Helper()
	g := must(t, s, owner, "goals.create", M{"projectId": project.ID, "title": "Measured portal", "outcome": "Customers submit requests", "criteria": []any{M{"id": "submit", "text": "Submit a request", "method": "manual"}}, "targets": []any{"web"}, "budget": 50})
	must(t, s, owner, "goals.approve", M{"id": g.ID})
	plan := must(t, s, owner, "plans.propose", M{"goalId": g.ID, "summary": "Implement portal", "tasks": []any{M{"id": "code", "title": "Implement", "role": "developer", "runtime": "linux", "kind": "code"}}})
	must(t, s, owner, "plans.approve", M{"id": plan.ID})
	repo := must(t, s, owner, "repositories.create", M{"projectId": project.ID, "title": "measured-portal"})
	repo = must(t, s, owner, "repositories.commit", M{"id": repo.ID, "branch": "feature", "message": "Add portal", "files": M{"index.html": "<h1>Measured portal</h1>"}})
	meta := M{"goalId": g.ID, "repositoryId": repo.ID, "commit": S(repo.Data, "lastCommit"), "target": "web", "name": "portal.html", "type": "package"}
	artifact, err := s.Upload(context.Background(), owner, meta, strings.NewReader("<h1>Measured portal</h1>"))
	if err != nil {
		t.Fatal(err)
	}
	qa := Actor{ID: "metric-qa", Kind: "human", Admin: true}
	meta["name"], meta["type"] = "measurement.txt", "report"
	proof, err := s.Upload(context.Background(), qa, meta, strings.NewReader("Synthetic browser and business observation; not a production result"))
	if err != nil {
		t.Fatal(err)
	}
	evidence := must(t, s, qa, "evidence.record", M{"artifactId": artifact.ID, "proofArtifactId": proof.ID, "method": "manual", "verificationEnvironment": "browser", "criteria": []any{"submit"}, "statement": "Synthetic browser observation"})
	release := must(t, s, owner, "releases.create", M{"goalId": g.ID, "version": "1.0.0", "commit": S(repo.Data, "lastCommit"), "artifactIds": []any{artifact.ID}})
	return metricFixture{s, owner, qa, project, g, artifact, proof, evidence, release}
}

func (f metricFixture) input(kind, reference string) M {
	return M{"goalId": f.goal.ID, "type": kind, "reference": reference, "key": reference, "note": "Synthetic observation, never a real enterprise outcome", "artifactId": f.artifact.ID, "evidenceId": f.evidence.ID, "releaseId": f.release.ID, "proofArtifactId": f.proof.ID, "result": "failed"}
}

// Catch duplicate facts, mutation of delivery gates, erased failure history and
// replay resurrecting voided facts. All referenced files and release gates are real.
func TestDeliveryMeasurementRecordReplayVoidAndCorrection(t *testing.T) {
	f := newMetricFixture(t)
	before, _ := f.s.Store.Get(f.goal.ID)
	in := f.input("business_acceptance", "business-review-1")
	r := must(t, f.s, f.owner, "measurements.record", in)
	if r.Kind != "measurement" || r.Status != "recorded" || revision(r) != 1 || S(r.Data, "releaseBinding") != S(f.release.Data, "binding") || S(r.Data, "proofDigest") != S(f.proof.Data, "digest") || S(r.Data, "recordedBy") != f.owner.ID || S(r.Data, "result") != "failed" {
		t.Fatal("lost immutable business provenance", r)
	}
	if must(t, f.s, f.owner, "measurements.record", in).ID != r.ID {
		t.Fatal("retry double counted")
	}
	changed := copyM(in)
	changed["result"] = "passed"
	_, err := f.s.Command(context.Background(), f.owner, "measurements.record", changed)
	expectCostFault(t, err, 409)
	changed["key"] = "another-key"
	_, err = f.s.Command(context.Background(), f.qa, "measurements.record", changed)
	expectCostFault(t, err, 409)
	void := M{"id": r.ID, "key": "void-1", "reason": "Incorrect source, keep original history"}
	must(t, f.s, f.qa, "measurements.void", void)
	if must(t, f.s, f.owner, "measurements.record", in).Status != "voided" {
		t.Fatal("replay resurrected observation")
	}
	corrected := must(t, f.s, f.owner, "measurements.record", changed)
	if S(corrected.Data, "replacesMeasurementId") != r.ID {
		t.Fatal("correction lost original record")
	}
	after, _ := f.s.Store.Get(f.goal.ID)
	release, _ := f.s.Store.Get(f.release.ID)
	if Hash(before) != Hash(after) || release.Status != "awaiting_approval" {
		t.Fatal("measurement changed delivery authority")
	}
}

func TestDeliveryMeasurementRejectsInvalidBindingsAndUnpublishedDefects(t *testing.T) {
	for _, mode := range []string{"wrong-goal", "wrong-revision", "corrupt-proof", "ci-preview", "publication-preview", "unpublished-defect", "invalid-result", "no-reference", "no-key", "bad-type", "forged-revision"} {
		t.Run(mode, func(t *testing.T) {
			f := newMetricFixture(t)
			in := f.input("business_acceptance", mode)
			switch mode {
			case "wrong-goal":
				in["goalId"] = goal(t, f.s, f.owner, f.project).ID
			case "wrong-revision":
				must(t, f.s, f.owner, "goals.revise", M{"id": f.goal.ID, "reason": "New scope"})
			case "corrupt-proof":
				if err := os.WriteFile(f.s.ArtifactPath(S(f.proof.Data, "digest")), []byte("corrupt"), 0600); err != nil {
					t.Fatal(err)
				}
			case "ci-preview", "publication-preview":
				in["type"] = "preview"
				if mode == "ci-preview" {
					f.evidence.Data["verificationEnvironment"] = "ci"
				} else {
					f.evidence.Data["purpose"] = "publication"
				}
				if err := f.s.Store.Update(context.Background(), func(tx *Tx) error { return tx.Put(f.evidence) }); err != nil {
					t.Fatal(err)
				}
			case "unpublished-defect":
				in["type"] = "production_defect"
			case "invalid-result":
				in["result"] = "success"
			case "no-reference":
				in["reference"] = ""
			case "no-key":
				in["key"] = ""
			case "bad-type":
				in["type"] = "business_success"
			case "forged-revision":
				in["requirementRevision"] = 900
			}
			if _, err := f.s.Command(context.Background(), f.owner, "measurements.record", in); err == nil {
				t.Fatal("unprovable measurement accepted", mode)
			}
			rows, _ := f.s.Store.List(f.project.ID, "measurement", "")
			if len(rows) != 0 {
				t.Fatal("invalid fact persisted")
			}
		})
	}
}

func TestDeliveryMeasurementHumanEffortUsesOwnNonOverlappingIntervals(t *testing.T) {
	f := newMetricFixture(t)
	// Temporal fixtures replace only the baseline origin so closed intervals can
	// be verified without sleeps or dependence on host wall-clock speed.
	origin := time.Now().Add(-2 * time.Hour).UTC()
	if err := f.s.Store.Update(context.Background(), func(tx *Tx) error {
		rows, err := tx.List(f.project.ID, "requirement", f.goal.ID)
		if err != nil {
			return err
		}
		r := rows[0]
		r.CreatedAt = origin.Format(time.RFC3339Nano)
		return tx.Put(r)
	}); err != nil {
		t.Fatal(err)
	}
	in := f.input("human_effort", "effort-1")
	in["startedAt"], in["endedAt"] = origin.Add(time.Minute).Format(time.RFC3339Nano), origin.Add(31*time.Minute).Format(time.RFC3339Nano)
	r := must(t, f.s, f.owner, "measurements.record", in)
	if N(r.Data, "durationSeconds") != 1800 || S(r.Data, "personId") != f.owner.ID {
		t.Fatal("wrong person-time", r)
	}
	for _, mode := range []string{"overlap", "future", "reverse", "before-baseline", "forged-person", "missing-end"} {
		x := copyM(in)
		x["reference"], x["key"] = mode, mode
		switch mode {
		case "future":
			x["endedAt"] = time.Now().Add(time.Hour).Format(time.RFC3339Nano)
		case "reverse":
			x["endedAt"] = origin.Format(time.RFC3339Nano)
		case "before-baseline":
			x["startedAt"] = origin.Add(-time.Hour).Format(time.RFC3339Nano)
		case "forged-person":
			x["personId"] = "someone-else"
		case "missing-end":
			delete(x, "endedAt")
		}
		if _, err := f.s.Command(context.Background(), f.owner, "measurements.record", x); err == nil {
			t.Fatal("invalid person-time accepted", mode)
		}
	}
}

func TestDeliveryMeasurementUsesCurrentHumanAuthorityAndAtomicAudit(t *testing.T) {
	s, admin, project, owner, member := membershipFixture(t)
	g := goal(t, s, admin, project)
	in := M{"goalId": g.ID, "type": "human_effort", "reference": "restricted", "key": "restricted", "note": "must fail before interval validation"}
	for _, kind := range []string{"agent", "worker"} {
		_, err := s.Command(context.Background(), Actor{ID: kind, Kind: kind, ProjectID: project.ID, Roles: []string{"owner"}}, "measurements.record", in)
		expectCostFault(t, err, 403)
	}
	must(t, s, admin, "members.grant", M{"projectId": project.ID, "identityId": owner.ID, "roles": []string{"viewer"}, "expectedRevision": member.Revision})
	owner.Roles = []string{"owner"}
	_, err := s.Command(context.Background(), owner, "measurements.record", in)
	expectCostFault(t, err, 403)
	f := newMetricFixture(t)
	if _, err := f.s.Store.DB.Exec(`CREATE TRIGGER fail_measurement_audit BEFORE INSERT ON audit BEGIN SELECT RAISE(ABORT, 'synthetic audit failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Command(context.Background(), f.owner, "measurements.record", f.input("preview", "atomic")); err == nil {
		t.Fatal("acknowledged without audit")
	}
	rows, _ := f.s.Store.List(f.project.ID, "measurement", "")
	var n int
	if err := f.s.Store.DB.QueryRow(`SELECT COUNT(*) FROM idempotency WHERE scope LIKE 'measurement-%'`).Scan(&n); err != nil || len(rows) != 0 || n != 0 {
		t.Fatal("failed audit left facts or replay key", err, n)
	}
}

func TestDeliveryMeasurementTrustedWorkerDefaultAcceptanceCanConfirmPreview(t *testing.T) {
	f, artifact, run := reportFixture(t)
	proof := uploadQualityReport(t, f, run)
	finished := must(t, f.s, f.verifier, "tasks.complete", M{"id": run.ID, "leaseToken": S(run.Data, "leaseToken"), "exitCode": 0, "result": M{"reportArtifactId": proof.ID}})
	must(t, f.s, f.owner, "measurements.record", M{"goalId": f.goal.ID, "type": "preview", "artifactId": artifact.ID, "evidenceId": S(finished.Data, "evidenceId"), "proofArtifactId": proof.ID, "reference": "worker-browser", "key": "worker-browser", "note": "Human confirms the exact independently tested runnable preview"})
}

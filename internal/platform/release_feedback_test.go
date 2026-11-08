package platform

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func publishedFeedbackFixture(t *testing.T) metricFixture {
	t.Helper()
	f := newMetricFixture(t)
	return publishFeedbackFixture(t, f)
}

func publishFeedbackFixture(t *testing.T, f metricFixture) metricFixture {
	t.Helper()
	must(t, f.s, f.qa, "releases.approve", M{"id": f.release.ID})
	f.release = must(t, f.s, f.qa, "releases.transition", M{"id": f.release.ID, "target": "web", "state": "published", "externalId": "synthetic-published-version", "proofArtifactId": f.proof.ID})
	return f
}

func feedbackInput(f metricFixture) M {
	return M{"releaseId": f.release.ID, "artifactId": f.artifact.ID, "name": "late-issue.txt", "key": "ticket-42"}
}

func attachFeedback(t *testing.T, f metricFixture) Resource {
	t.Helper()
	r, err := f.s.UploadReleaseReport(context.Background(), f.owner, feedbackInput(f), strings.NewReader("Synthetic late issue report, not an actual production defect"))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestReleaseFeedbackPreservesPublishedSourceAcrossRequirementChanges(t *testing.T) {
	f := publishedFeedbackFixture(t)
	must(t, f.s, f.owner, "releases.transition", M{"id": f.release.ID, "target": "web", "state": "halted", "reason": "Synthetic operational pause after publication"})
	must(t, f.s, f.owner, "goals.revise", M{"id": f.goal.ID, "targets": []any{"windows"}, "reason": "Next revision removes Web while historical clients remain"})
	beforeGoal, _ := f.s.Store.Get(f.goal.ID)
	beforeRelease, _ := f.s.Store.Get(f.release.ID)
	r := attachFeedback(t, f)
	for key, want := range map[string]string{"type": "report", "purpose": "release-feedback", "releaseId": f.release.ID, "releaseBinding": S(f.release.Data, "binding"), "releaseArtifactId": f.artifact.ID, "releaseArtifactDigest": S(f.artifact.Data, "digest"), "commit": S(f.artifact.Data, "commit"), "repositoryId": S(f.artifact.Data, "repositoryId"), "target": "web", "producerId": f.owner.ID} {
		if S(r.Data, key) != want {
			t.Fatalf("lost exact feedback binding %s: %v", key, r)
		}
	}
	if revision(r) != 1 || r.GoalID != f.goal.ID || r.ProjectID != f.project.ID {
		t.Fatal("report inherited current scope", r)
	}
	afterGoal, _ := f.s.Store.Get(f.goal.ID)
	afterRelease, _ := f.s.Store.Get(f.release.ID)
	if Hash(beforeGoal) != Hash(afterGoal) || Hash(beforeRelease) != Hash(afterRelease) {
		t.Fatal("late attachment changed delivery authority")
	}
	if err := f.s.VerifyArtifact(r); err != nil {
		t.Fatal(err)
	}
	second := attachFeedback(t, f)
	if second.ID != r.ID {
		t.Fatal("retry duplicated report")
	}
	_, err := f.s.UploadReleaseReport(context.Background(), f.owner, feedbackInput(f), strings.NewReader("different report bytes"))
	expectCostFault(t, err, 409)
}

func TestReleaseFeedbackRejectsUnpublishedOrInvalidSource(t *testing.T) {
	for _, mode := range []string{"unpublished", "other-target-published", "published-package-mismatch", "wrong-package", "wrong-release", "corrupt-package", "package-corrupts-during-stream", "binding-drift", "empty-body", "oversize", "missing-key", "missing-name", "reader-error"} {
		t.Run(mode, func(t *testing.T) {
			f := publishedFeedbackFixture(t)
			in := feedbackInput(f)
			var body io.Reader = strings.NewReader("Synthetic feedback")
			switch mode {
			case "unpublished":
				f.release.Data["targets"] = []any{M{"target": "web", "artifactId": f.artifact.ID, "status": "halted"}}
				if err := f.s.Store.Update(context.Background(), func(tx *Tx) error { return tx.Put(f.release) }); err != nil {
					t.Fatal(err)
				}
			case "other-target-published", "published-package-mismatch":
				target, artifactID := "web", "different-package"
				if mode == "other-target-published" {
					target, artifactID = "windows", f.artifact.ID
				}
				f.release.Data["targets"] = []any{M{"target": target, "artifactId": artifactID, "status": "published"}}
				if err := f.s.Store.Update(context.Background(), func(tx *Tx) error { return tx.Put(f.release) }); err != nil {
					t.Fatal(err)
				}
			case "wrong-package":
				in["artifactId"] = f.proof.ID
			case "wrong-release":
				in["releaseId"] = f.goal.ID
			case "corrupt-package":
				if err := os.WriteFile(f.s.ArtifactPath(S(f.artifact.Data, "digest")), []byte("corrupt"), 0600); err != nil {
					t.Fatal(err)
				}
			case "package-corrupts-during-stream":
				body = &feedbackTriggerReader{body: body, action: func() {
					if err := os.WriteFile(f.s.ArtifactPath(S(f.artifact.Data, "digest")), []byte("corrupt"), 0600); err != nil {
						t.Fatal(err)
					}
				}}
			case "binding-drift":
				f.release.Data["binding"] = "not-the-candidate-binding"
				if err := f.s.Store.Update(context.Background(), func(tx *Tx) error { return tx.Put(f.release) }); err != nil {
					t.Fatal(err)
				}
			case "empty-body":
				body = strings.NewReader("")
			case "oversize":
				body = bytes.NewReader(make([]byte, (16<<20)+1))
			case "missing-key":
				delete(in, "key")
			case "missing-name":
				delete(in, "name")
			case "reader-error":
				body = feedbackBrokenReader{}
			}
			before, _ := f.s.Store.List(f.project.ID, "artifact", f.goal.ID)
			if _, err := f.s.UploadReleaseReport(context.Background(), f.owner, in, body); err == nil {
				t.Fatal("accepted unverifiable report", mode)
			}
			after, _ := f.s.Store.List(f.project.ID, "artifact", f.goal.ID)
			if len(after) != len(before) {
				t.Fatal("failed attachment persisted a resource")
			}
			if files, err := os.ReadDir(filepath.Join(f.s.Root, "uploads")); (err != nil && !os.IsNotExist(err)) || len(files) != 0 {
				t.Fatal("failed attachment left temporary upload files", err, files)
			}
		})
	}
}

type feedbackBrokenReader struct{}

func (feedbackBrokenReader) Read([]byte) (int, error) {
	return 0, errors.New("synthetic interrupted report stream")
}

type feedbackTriggerReader struct {
	action func()
	body   io.Reader
}

func (r *feedbackTriggerReader) Read(p []byte) (int, error) {
	if r.action != nil {
		fn := r.action
		r.action = nil
		fn()
	}
	return r.body.Read(p)
}

func TestReleaseFeedbackUsesCurrentHumanAuthorityAndAtomicAudit(t *testing.T) {
	for _, kind := range []string{"agent", "worker"} {
		f := publishedFeedbackFixture(t)
		_, err := f.s.UploadReleaseReport(context.Background(), Actor{ID: kind, Kind: kind, Admin: true}, feedbackInput(f), strings.NewReader("Synthetic feedback"))
		expectCostFault(t, err, 403)
	}
	s, admin, project, owner, member := membershipFixture(t)
	f := publishFeedbackFixture(t, metricFixtureOn(t, s, admin, project))
	body := &feedbackTriggerReader{body: strings.NewReader("Synthetic feedback"), action: func() {
		must(t, s, admin, "members.grant", M{"projectId": project.ID, "identityId": owner.ID, "roles": []string{"viewer"}, "expectedRevision": member.Revision})
	}}
	_, err := s.UploadReleaseReport(context.Background(), owner, feedbackInput(f), body)
	expectCostFault(t, err, 403)
	rows, _ := s.Store.List(project.ID, "artifact", f.goal.ID)
	if len(rows) != 2 {
		t.Fatal("revocation during stream still published report", len(rows))
	}
	f = publishedFeedbackFixture(t)
	if _, err = f.s.Store.DB.Exec(`CREATE TRIGGER fail_feedback_audit BEFORE INSERT ON audit BEGIN SELECT RAISE(ABORT, 'synthetic feedback audit failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.UploadReleaseReport(context.Background(), f.owner, feedbackInput(f), strings.NewReader("Synthetic feedback")); err == nil {
		t.Fatal("report acknowledged without audit")
	}
	rows, _ = f.s.Store.List(f.project.ID, "artifact", f.goal.ID)
	var keys int
	if err = f.s.Store.DB.QueryRow(`SELECT COUNT(*) FROM idempotency WHERE scope LIKE 'release-report:%'`).Scan(&keys); err != nil || len(rows) != 2 || keys != 0 {
		t.Fatal("failed audit left report or replay key", err, len(rows), keys)
	}
}

func TestReleaseFeedbackLateDefectIsBoundToHistoricalVersion(t *testing.T) {
	f := publishedFeedbackFixture(t)
	must(t, f.s, f.owner, "goals.revise", M{"id": f.goal.ID, "reason": "New requirement version"})
	proof := attachFeedback(t, f)
	in := f.input("production_defect", "late-ticket")
	in["proofArtifactId"], in["requirementRevision"] = proof.ID, 1
	fact := must(t, f.s, f.owner, "measurements.record", in)
	if revision(fact) != 1 {
		t.Fatal("late defect belongs to current version", fact)
	}
	history := metricJSON(t, f.s, f.owner, f.project.ID, f.goal.ID, 1)
	current := metricJSON(t, f.s, f.owner, f.project.ID, f.goal.ID, 0)
	if N(history, "reportedProductionDefects") != 1 || N(current, "reportedProductionDefects") != 0 {
		t.Fatal("late defect crossed version scope", history, current)
	}
	wrong := copyM(in)
	wrong["key"], wrong["reference"], wrong["requirementRevision"] = "wrong-rev", "wrong-rev", 2
	if _, err := f.s.Command(context.Background(), f.owner, "measurements.record", wrong); err == nil {
		t.Fatal("incorrect explicit source version accepted")
	}
	if err := os.WriteFile(f.s.ArtifactPath(S(proof.Data, "digest")), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	history = metricJSON(t, f.s, f.owner, f.project.ID, f.goal.ID, 1)
	if N(history, "reportedProductionDefects") != 0 || len(Items(history["observations"])) != 1 {
		t.Fatal("integrity failure silently counted or deleted history", history)
	}
}

func TestReleaseFeedbackCannotAuthorizeAcceptanceOrProductionActions(t *testing.T) {
	for _, use := range []string{"business", "manual-evidence", "cached-evidence", "publication", "runtime"} {
		t.Run(use, func(t *testing.T) {
			f := publishedFeedbackFixture(t)
			proof := attachFeedback(t, f)
			var err error
			switch use {
			case "business":
				in := f.input("business_acceptance", "feedback-is-not-acceptance")
				in["proofArtifactId"] = proof.ID
				_, err = f.s.Command(context.Background(), f.owner, "measurements.record", in)
			case "manual-evidence":
				_, err = f.s.Command(context.Background(), f.qa, "evidence.record", M{"artifactId": f.artifact.ID, "proofArtifactId": proof.ID, "method": "manual", "verificationEnvironment": "browser", "criteria": []any{"submit"}, "statement": "Feedback is not independent testing"})
			case "cached-evidence":
				ev := f.evidence
				ev.Data["proofArtifactId"] = proof.ID
				err = f.s.Store.Update(context.Background(), func(tx *Tx) error { return f.s.qualityEvidenceReport(tx, ev) })
			case "publication":
				r := must(t, f.s, f.owner, "releases.create", M{"goalId": f.goal.ID, "version": "1.0.1", "commit": S(f.release.Data, "commit"), "artifactIds": []any{f.artifact.ID}})
				must(t, f.s, f.qa, "releases.approve", M{"id": r.ID})
				_, err = f.s.Command(context.Background(), f.qa, "releases.transition", M{"id": r.ID, "target": "web", "state": "published", "externalId": "synthetic-next-version", "proofArtifactId": proof.ID})
			case "runtime":
				d := Resource{ID: "synthetic-deployment", GoalID: f.goal.ID, ProjectID: f.project.ID, Data: M{"commit": S(f.release.Data, "commit"), "activatedAt": f.goal.CreatedAt}}
				err = f.s.Store.Update(context.Background(), func(tx *Tx) error {
					_, err := f.s.runtimeProof(tx, f.qa, d, M{"proofArtifactId": proof.ID})
					return err
				})
			}
			if err == nil {
				t.Fatal("operational issue report acquired acceptance or production authority", use)
			}
		})
	}
}

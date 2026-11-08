package platform

import (
	"bytes"
	"context"
	"testing"
)

// Store receipts and device attestations below are synthetic. Real control-plane
// commands/SQLite enforce gates, but these are not live OPPO or physical evidence.
func TestOppoAsyncReviewPublicationStillRequiresIndependentAcceptance(t *testing.T) {
	s, officer, worker, release := publishTargetFixture(t, "android-oppo")
	must(t, s, officer, "releases.dispatch", M{"id": release.ID, "target": "android-oppo", "workerId": worker.WorkerID, "key": "oppo-submit"})
	upload := must(t, s, worker, "tasks.claim", M{})
	externalID := "oppo:100123:42"
	must(t, s, worker, "tasks.complete", M{"id": upload.ID, "leaseToken": S(upload.Data, "leaseToken"), "exitCode": 0, "result": M{"externalId": externalID, "state": "processing"}})
	for _, state := range []string{"awaiting_review", "awaiting_release", "published"} {
		if idle := must(t, s, worker, "tasks.claim", M{}); idle.Kind == "run" {
			t.Fatal("external OPPO wait held a worker lease")
		}
		must(t, s, officer, "releases.refresh", M{"id": release.ID, "target": "android-oppo", "key": "oppo-" + state})
		query := must(t, s, worker, "tasks.claim", M{})
		if task := Obj(query.Data["task"]); S(task, "kind") != "publish_status" || S(task, "externalId") != externalID {
			t.Fatal("query lost exact version binding")
		}
		must(t, s, worker, "tasks.complete", M{"id": query.ID, "leaseToken": S(query.Data, "leaseToken"), "exitCode": 0, "result": M{"externalId": externalID, "state": state}})
		current, _ := s.Store.Get(release.ID)
		if S(releaseTarget(current, "android-oppo"), "status") != state || current.Status == "delivered" {
			t.Fatal("channel observation counted as independent delivery")
		}
	}
	current, _ := s.Store.Get(release.ID)
	target := releaseTarget(current, "android-oppo")
	artifact, err := s.Store.Get(S(target, "artifactId"))
	if err != nil {
		t.Fatal(err)
	}
	prior, _ := s.Store.List(release.ProjectID, "evidence", release.GoalID)
	for _, ev := range prior {
		if _, err = s.Command(context.Background(), officer, "releases.verify", M{"id": release.ID, "target": "android-oppo", "evidenceId": ev.ID}); err == nil {
			t.Fatal("pre-publication QA reused for publication")
		}
	}
	qa := Actor{ID: "independent-oppo-qa", Kind: "human", Admin: true}
	proof, err := s.Upload(context.Background(), qa, M{"goalId": release.GoalID, "repositoryId": S(artifact.Data, "repositoryId"), "commit": S(release.Data, "commit"), "target": "android-oppo", "type": "report", "name": "synthetic-oppo-device-attestation.txt"}, bytes.NewBufferString("Synthetic only: hypothetical OPPO exact-version device verification"))
	if err != nil {
		t.Fatal(err)
	}
	// Automated publication records the worker as the executor. The approver is
	// a different principal; independence is checked against the executor ID.
	executor := Actor{ID: S(target, "updatedBy"), Kind: "human", Admin: true}
	for _, actor := range []Actor{executor, qa} {
		verification := must(t, s, actor, "evidence.record", M{"artifactId": artifact.ID, "proofArtifactId": proof.ID, "method": "device", "purpose": "publication", "verificationEnvironment": targetEnvironment("android-oppo"), "externalId": externalID, "statement": "Synthetic attestation; not a real OPPO account/device result"})
		out, err := s.Command(context.Background(), actor, "releases.verify", M{"id": release.ID, "target": "android-oppo", "evidenceId": verification.ID})
		if actor.ID == executor.ID {
			if err == nil {
				t.Fatal("publication executor verified own release")
			}
		} else if err != nil || out.Status != "delivered" {
			t.Fatal("independent exact verification rejected", err)
		}
	}
}

func TestOppoFirstObservationMayAlreadyBeAwaitingRelease(t *testing.T) {
	s, officer, worker, release := publishTargetFixture(t, "android-oppo")
	must(t, s, officer, "releases.dispatch", M{"id": release.ID, "target": "android-oppo", "workerId": worker.WorkerID, "key": "oppo-submit"})
	upload := must(t, s, worker, "tasks.claim", M{})
	must(t, s, worker, "tasks.complete", M{"id": upload.ID, "leaseToken": S(upload.Data, "leaseToken"), "exitCode": 0, "result": M{"externalId": "oppo:100123:42", "state": "processing"}})
	must(t, s, officer, "releases.refresh", M{"id": release.ID, "target": "android-oppo", "key": "oppo-already-reviewed"})
	query := must(t, s, worker, "tasks.claim", M{})
	must(t, s, worker, "tasks.complete", M{"id": query.ID, "leaseToken": S(query.Data, "leaseToken"), "exitCode": 0, "result": M{"externalId": "oppo:100123:42", "state": "awaiting_release"}})
	current, _ := s.Store.Get(release.ID)
	if S(releaseTarget(current, "android-oppo"), "status") != "awaiting_release" || current.Status == "delivered" {
		t.Fatal("skipped observation phase corrupted release")
	}
}

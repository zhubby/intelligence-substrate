package platform

import (
	"bytes"
	"context"
	"testing"
)

// Synthetic portal/device attestations exercise authorization and state gates.
// They are not evidence of any real Xiaomi account, publication or device run.
func TestXiaomiManualPortalObservationAndIndependentAcceptance(t *testing.T) {
	s, officer, worker, release := publishTargetFixture(t, "android-xiaomi")
	must(t, s, officer, "releases.dispatch", M{"id": release.ID, "target": "android-xiaomi", "workerId": worker.WorkerID, "key": "xiaomi-push"})
	upload := must(t, s, worker, "tasks.claim", M{})
	externalID := "xiaomi:com.company.feedback:42"
	must(t, s, worker, "tasks.complete", M{"id": upload.ID, "leaseToken": S(upload.Data, "leaseToken"), "exitCode": 0, "result": M{"externalId": externalID, "state": "awaiting_review"}})
	if idle := must(t, s, worker, "tasks.claim", M{}); idle.Kind == "run" {
		t.Fatal("external review wait held an execution lease")
	}
	must(t, s, officer, "releases.refresh", M{"id": release.ID, "target": "android-xiaomi", "key": "xiaomi-manual-required"})
	query := must(t, s, worker, "tasks.claim", M{})
	must(t, s, worker, "tasks.complete", M{"id": query.ID, "leaseToken": S(query.Data, "leaseToken"), "exitCode": 0, "result": M{"externalId": externalID, "state": "unchanged", "upstreamState": "MANUAL_VERIFICATION_REQUIRED", "reason": "官方无审核/上架状态 API，须人工核对"}})
	current, _ := s.Store.Get(release.ID)
	target := releaseTarget(current, "android-xiaomi")
	if S(target, "status") != "awaiting_review" || current.Status == "delivered" || S(target, "nextCheckAt") != "" {
		t.Fatal("manual-only observation changed store state, marked delivery or scheduled unsupported automatic polling")
	}
	if _, err := s.Command(context.Background(), worker, "releases.transition", M{"id": release.ID, "target": "android-xiaomi", "state": "published", "externalId": externalID}); err == nil {
		t.Fatal("worker impersonated a portal operator")
	}
	if _, err := s.Command(context.Background(), officer, "releases.transition", M{"id": release.ID, "target": "android-xiaomi", "state": "published", "externalId": externalID}); err == nil {
		t.Fatal("portal publication was asserted without a report")
	}
	artifact, err := s.Store.Get(S(target, "artifactId"))
	if err != nil {
		t.Fatal(err)
	}
	proof, err := s.Upload(context.Background(), officer, M{"goalId": release.GoalID, "repositoryId": S(artifact.Data, "repositoryId"), "commit": S(release.Data, "commit"), "target": "android-xiaomi", "type": "report", "name": "synthetic-portal-observation.txt"}, bytes.NewBufferString("Protocol fixture only; package/version verified manually in a hypothetical portal"))
	if err != nil {
		t.Fatal(err)
	}
	must(t, s, officer, "releases.transition", M{"id": release.ID, "target": "android-xiaomi", "state": "published", "externalId": externalID, "proofArtifactId": proof.ID})
	current, _ = s.Store.Get(release.ID)
	if S(releaseTarget(current, "android-xiaomi"), "status") != "published" || current.Status == "delivered" {
		t.Fatal("portal state replaced independent release acceptance")
	}
	// Existing pre-release QA has the same artifact but the wrong purpose and
	// external-version binding, so it cannot serve as post-publication evidence.
	evidence, _ := s.Store.List(release.ProjectID, "evidence", release.GoalID)
	for _, ev := range evidence {
		if _, err := s.Command(context.Background(), officer, "releases.verify", M{"id": release.ID, "target": "android-xiaomi", "evidenceId": ev.ID}); err == nil {
			t.Fatal("pre-release evidence passed post-publication verification")
		}
	}
	qa := Actor{ID: "independent-xiaomi-qa", Kind: "human", Admin: true}
	deviceProof, err := s.Upload(context.Background(), qa, M{"goalId": release.GoalID, "repositoryId": S(artifact.Data, "repositoryId"), "commit": S(release.Data, "commit"), "target": "android-xiaomi", "type": "report", "name": "synthetic-device-attestation.txt"}, bytes.NewBufferString("Synthetic physical-device attestation to exercise state gates only"))
	if err != nil {
		t.Fatal(err)
	}
	for _, observer := range []Actor{officer, qa} {
		verification := must(t, s, observer, "evidence.record", M{"artifactId": artifact.ID, "proofArtifactId": deviceProof.ID, "method": "device", "purpose": "publication", "verificationEnvironment": targetEnvironment("android-xiaomi"), "externalId": externalID, "statement": "Protocol fixture only; not real Xiaomi or device evidence"})
		result, err := s.Command(context.Background(), observer, "releases.verify", M{"id": release.ID, "target": "android-xiaomi", "evidenceId": verification.ID})
		if observer.ID == officer.ID {
			if err == nil {
				t.Fatal("publication officer verified their own release")
			}
		} else if err != nil || result.Status != "delivered" {
			t.Fatal("independent correctly bound acceptance did not finish the synthetic release", err)
		}
	}
}

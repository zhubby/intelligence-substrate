package platform

import (
	"bytes"
	"context"
	"testing"
)

// Reports and device statements are synthetic fixtures. Commands, authorization,
// SQLite and wait scheduling execute normally; this is not store/device evidence.
func TestVivoPortalPublicationNeedsIndependentDeviceAcceptance(t *testing.T) {
	s, officer, worker, release := publishTargetFixture(t, "android-vivo")
	must(t, s, officer, "releases.dispatch", M{"id": release.ID, "target": "android-vivo", "workerId": worker.WorkerID, "key": "vivo-update"})
	upload := must(t, s, worker, "tasks.claim", M{})
	externalID := "vivo:production:com.company.feedback:42"
	must(t, s, worker, "tasks.complete", M{"id": upload.ID, "leaseToken": S(upload.Data, "leaseToken"), "exitCode": 0, "result": M{"externalId": externalID, "state": "awaiting_review"}})
	if idle := must(t, s, worker, "tasks.claim", M{}); idle.Kind == "run" {
		t.Fatal("vivo review wait held an execution lease")
	}
	for _, observation := range []M{{"state": "awaiting_release", "upstreamState": "REVIEW_PASSED_AWAITING_SALE"}, {"state": "unchanged", "upstreamState": "MANUAL_VERIFICATION_REQUIRED", "reason": "详情不能关联准确在架制品，需要门户证明"}} {
		must(t, s, officer, "releases.refresh", M{"id": release.ID, "target": "android-vivo", "key": S(observation, "upstreamState")})
		query := must(t, s, worker, "tasks.claim", M{})
		observation["externalId"] = externalID
		must(t, s, worker, "tasks.complete", M{"id": query.ID, "leaseToken": S(query.Data, "leaseToken"), "exitCode": 0, "result": observation})
	}
	current, _ := s.Store.Get(release.ID)
	target := releaseTarget(current, "android-vivo")
	if S(target, "status") != "awaiting_release" || S(target, "nextCheckAt") != "" || current.Status == "delivered" {
		t.Fatal("manual-required observation falsely confirmed publication or kept polling")
	}
	for _, actor := range []Actor{worker, officer} {
		if _, err := s.Command(context.Background(), actor, "releases.transition", M{"id": release.ID, "target": "android-vivo", "state": "published", "externalId": externalID}); err == nil {
			t.Fatal("portal publication accepted without human authority and a report")
		}
	}
	artifact, err := s.Store.Get(S(target, "artifactId"))
	if err != nil {
		t.Fatal(err)
	}
	proof, err := s.Upload(context.Background(), officer, M{"goalId": release.GoalID, "repositoryId": S(artifact.Data, "repositoryId"), "commit": S(release.Data, "commit"), "target": "android-vivo", "type": "report", "name": "synthetic-vivo-portal-report.txt"}, bytes.NewBufferString("Synthetic portal observation, not a live vivo publication"))
	if err != nil {
		t.Fatal(err)
	}
	must(t, s, officer, "releases.transition", M{"id": release.ID, "target": "android-vivo", "state": "published", "externalId": externalID, "proofArtifactId": proof.ID})
	current, _ = s.Store.Get(release.ID)
	if current.Status == "delivered" || S(releaseTarget(current, "android-vivo"), "status") != "published" {
		t.Fatal("portal proof replaced independent device acceptance")
	}
	priorEvidence, _ := s.Store.List(release.ProjectID, "evidence", release.GoalID)
	for _, ev := range priorEvidence {
		if _, err := s.Command(context.Background(), officer, "releases.verify", M{"id": release.ID, "target": "android-vivo", "evidenceId": ev.ID}); err == nil {
			t.Fatal("pre-publication QA reused as publication evidence")
		}
	}
	qa := Actor{ID: "independent-vivo-qa", Kind: "human", Admin: true}
	device, err := s.Upload(context.Background(), qa, M{"goalId": release.GoalID, "repositoryId": S(artifact.Data, "repositoryId"), "commit": S(release.Data, "commit"), "target": "android-vivo", "type": "report", "name": "synthetic-vivo-device-report.txt"}, bytes.NewBufferString("Synthetic acceptance only, not a physical Android device run"))
	if err != nil {
		t.Fatal(err)
	}
	for _, actor := range []Actor{officer, qa} {
		evidence := must(t, s, actor, "evidence.record", M{"artifactId": artifact.ID, "proofArtifactId": device.ID, "method": "device", "purpose": "publication", "verificationEnvironment": targetEnvironment("android-vivo"), "externalId": externalID, "statement": "Synthetic statement to exercise independence and binding"})
		result, err := s.Command(context.Background(), actor, "releases.verify", M{"id": release.ID, "target": "android-vivo", "evidenceId": evidence.ID})
		if actor.ID == officer.ID {
			if err == nil {
				t.Fatal("portal publication executor verified their own release")
			}
		} else if err != nil || result.Status != "delivered" {
			t.Fatal("independent exact-version acceptance failed", err)
		}
	}
}

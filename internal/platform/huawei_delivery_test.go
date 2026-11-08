package platform

import (
	"context"
	"testing"
	"time"
)

// This exercises the Huawei target through the real control-plane commands;
// receipt identities are synthetic and never assert a real device/store result.
func TestHuaweiProcessingUsesOriginalApprovalAndNeverInfersDelivery(t *testing.T) {
	for _, scenario := range []string{"submitted", "lost continuation lease", "revoked approval"} {
		t.Run(scenario, func(t *testing.T) {
			s, officer, worker, release := publishTargetFixture(t, "android-huawei")
			must(t, s, officer, "releases.dispatch", M{"id": release.ID, "target": "android-huawei", "workerId": worker.WorkerID, "key": "huawei-upload"})
			upload := must(t, s, worker, "tasks.claim", M{})
			must(t, s, worker, "tasks.complete", M{"id": upload.ID, "leaseToken": S(upload.Data, "leaseToken"), "exitCode": 0, "result": M{"externalId": "huawei:1234567890:pkg-42", "state": "processing"}})
			if idle := must(t, s, worker, "tasks.claim", M{}); idle.Kind == "run" {
				t.Fatal("Huawei parsing wait held an execution lease")
			}
			must(t, s, officer, "releases.refresh", M{"id": release.ID, "target": "android-huawei", "key": "huawei-parsed"})
			query := must(t, s, worker, "tasks.claim", M{})
			must(t, s, worker, "tasks.complete", M{"id": query.ID, "leaseToken": S(query.Data, "leaseToken"), "exitCode": 0, "result": M{"externalId": "huawei:1234567890:pkg-42", "state": "processing", "continueRequired": true}})
			if scenario == "revoked approval" {
				if err := s.Store.Update(context.Background(), func(tx *Tx) error {
					r, err := tx.Get(release.ID)
					if err != nil {
						return err
					}
					r.Data["approvedBinding"] = "revoked"
					return tx.Put(r)
				}); err != nil {
					t.Fatal(err)
				}
				if idle := must(t, s, worker, "tasks.claim", M{}); idle.Kind == "run" {
					t.Fatal("revoked Huawei approval allowed submission")
				}
				return
			}
			continuation := must(t, s, worker, "tasks.claim", M{})
			task, original := Obj(continuation.Data["task"]), Obj(upload.Data["task"])
			for _, field := range []string{"operationKey", "binding", "commit", "artifactId", "artifactDigest", "releaseId"} {
				if S(task, field) != S(original, field) {
					t.Fatalf("Huawei continuation changed %s", field)
				}
			}
			if S(task, "kind") != "publish_continue" || S(task, "externalId") != "huawei:1234567890:pkg-42" {
				t.Fatal("Huawei continuation lost the exact uploaded package")
			}
			if scenario == "lost continuation lease" {
				continuation.Data["leaseUntil"] = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
				if err := s.Store.Update(context.Background(), func(tx *Tx) error { return tx.Put(continuation) }); err != nil {
					t.Fatal(err)
				}
				if idle := must(t, s, worker, "tasks.claim", M{}); idle.Kind == "run" {
					t.Fatal("uncertain Huawei submission was replayed")
				}
				current, _ := s.Store.Get(release.ID)
				if S(releaseTarget(current, "android-huawei"), "status") != "reconciliation_required" {
					t.Fatal("lost Huawei lease was not retained for reconciliation")
				}
				return
			}
			must(t, s, worker, "tasks.complete", M{"id": continuation.ID, "leaseToken": S(continuation.Data, "leaseToken"), "exitCode": 0, "result": M{"externalId": "huawei:1234567890:pkg-42", "state": "awaiting_review"}})
			must(t, s, officer, "releases.refresh", M{"id": release.ID, "target": "android-huawei", "key": "huawei-published"})
			query = must(t, s, worker, "tasks.claim", M{})
			must(t, s, worker, "tasks.complete", M{"id": query.ID, "leaseToken": S(query.Data, "leaseToken"), "exitCode": 0, "result": M{"externalId": "huawei:1234567890:pkg-42", "state": "published"}})
			current, _ := s.Store.Get(release.ID)
			if S(releaseTarget(current, "android-huawei"), "status") != "published" || current.Status == "delivered" {
				t.Fatal("channel publication replaced independent device acceptance")
			}
		})
	}
}

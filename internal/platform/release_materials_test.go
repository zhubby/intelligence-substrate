package platform

import (
	"context"
	"os"
	"strings"
	"testing"
)

func releaseMaterialFixture(t *testing.T) (*Service, Actor, Actor, Resource, Resource, M) {
	t.Helper()
	s, officer, worker, original := publishTargetFixture(t, "ios-appstore")
	packageID := S(Obj(Items(original.Data["artifacts"])[0]), "id")
	pkg, err := s.Store.Get(packageID)
	if err != nil {
		t.Fatal(err)
	}
	producer := Actor{ID: "material-author", Kind: "human", Admin: true}
	meta := M{"goalId": original.GoalID, "target": "ios-appstore", "type": "report", "name": "App Store reviewed material", "commit": S(original.Data, "commit"), "repositoryId": S(pkg.Data, "repositoryId"), "mediaType": "application/json"}
	material, err := s.Upload(context.Background(), producer, meta, strings.NewReader(`{"schemaVersion":1,"description":"Reviewed description"}`))
	if err != nil {
		t.Fatal(err)
	}
	input := M{"goalId": original.GoalID, "version": "1.0.0", "commit": S(original.Data, "commit"), "artifactIds": []any{packageID}, "storeMetadataArtifactIds": []any{material.ID}}
	return s, officer, worker, original, material, input
}

// Dropping materials from the release hash or any dispatched task breaks this test.
func TestReleaseMaterialApprovalAndAllTaskBindings(t *testing.T) {
	s, officer, worker, original, material, input := releaseMaterialFixture(t)
	r := must(t, s, officer, "releases.create", input)
	if S(r.Data, "binding") == S(original.Data, "binding") || S(Obj(Items(r.Data["storeMetadata"])[0]), "digest") != S(material.Data, "digest") {
		t.Fatal("material bytes were not included in the approval")
	}
	must(t, s, officer, "releases.approve", M{"id": r.ID})
	must(t, s, officer, "releases.dispatch", M{"id": r.ID, "target": "ios-appstore", "workerId": worker.WorkerID, "key": "materials"})
	upload := must(t, s, worker, "tasks.claim", M{})
	check := func(run Resource) {
		t.Helper()
		if S(Obj(run.Data["task"]), "metadataDigest") != S(material.Data, "digest") {
			t.Fatalf("material approval lost: %+v", run.Data["task"])
		}
	}
	check(upload)
	must(t, s, worker, "tasks.complete", M{"id": upload.ID, "leaseToken": S(upload.Data, "leaseToken"), "exitCode": 0, "result": M{"state": "processing", "externalId": "apple:123:upload-materials"}})
	must(t, s, officer, "releases.refresh", M{"id": r.ID, "target": "ios-appstore", "key": "material-ready"})
	query := must(t, s, worker, "tasks.claim", M{})
	check(query)
	must(t, s, worker, "tasks.complete", M{"id": query.ID, "leaseToken": S(query.Data, "leaseToken"), "exitCode": 0, "result": M{"state": "processing", "externalId": "apple:123:upload-materials", "continueRequired": true}})
	check(must(t, s, worker, "tasks.claim", M{}))
}

func TestReleaseMaterialScopeAndImmutableContent(t *testing.T) {
	for _, scenario := range []string{"wrong-goal", "wrong-revision", "wrong-commit", "wrong-repository", "wrong-target", "wrong-type", "duplicate", "non-json", "tampered", "wrong-input-type", "nonstring-input"} {
		t.Run(scenario, func(t *testing.T) {
			s, officer, _, _, material, input := releaseMaterialFixture(t)
			switch scenario {
			case "wrong-goal":
				material.GoalID = "goal_other"
			case "wrong-revision":
				material.Data["requirementRevision"] = 99
			case "wrong-commit":
				material.Data["commit"] = strings.Repeat("f", 40)
			case "wrong-repository":
				material.Data["repositoryId"] = "repository_other"
			case "wrong-target":
				material.Data["target"] = "web"
			case "wrong-type":
				material.Data["type"] = "package"
			case "duplicate":
				input["storeMetadataArtifactIds"] = []any{material.ID, material.ID}
			case "non-json":
				meta := M{"goalId": material.GoalID, "target": "ios-appstore", "type": "report", "name": "Non-JSON material", "commit": S(material.Data, "commit"), "repositoryId": S(material.Data, "repositoryId"), "mediaType": "application/json"}
				nonJSON, err := s.Upload(context.Background(), Actor{ID: "material-author", Kind: "human", Admin: true}, meta, strings.NewReader("not JSON, with its authentic digest"))
				if err != nil {
					t.Fatal(err)
				}
				input["storeMetadataArtifactIds"] = []any{nonJSON.ID}
			case "tampered":
				if err := os.WriteFile(s.ArtifactPath(S(material.Data, "digest")), []byte("changed material"), 0600); err != nil {
					t.Fatal(err)
				}
			case "wrong-input-type":
				input["storeMetadataArtifactIds"] = material.ID
			case "nonstring-input":
				input["storeMetadataArtifactIds"] = []any{material.ID, 12}
			}
			if err := s.Store.Update(context.Background(), func(tx *Tx) error { return tx.Put(material) }); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Command(context.Background(), officer, "releases.create", input); err == nil {
				t.Fatal("unreviewable or out-of-scope material accepted")
			}
		})
	}
}

func TestReleaseMaterialProducerCannotApproveAndTamperingInvalidatesGate(t *testing.T) {
	s, officer, _, _, material, input := releaseMaterialFixture(t)
	r := must(t, s, officer, "releases.create", input)
	producer := Actor{ID: "material-author", Kind: "human", Admin: true}
	if _, err := s.Command(context.Background(), producer, "releases.approve", M{"id": r.ID}); err == nil {
		t.Fatal("material author approved own release")
	}
	if err := os.WriteFile(s.ArtifactPath(S(material.Data, "digest")), []byte("changed after candidate"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Command(context.Background(), officer, "releases.approve", M{"id": r.ID}); err == nil {
		t.Fatal("approved altered material content")
	}
}

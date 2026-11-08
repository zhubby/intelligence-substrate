package platform

import (
	"context"
	"strings"
	"testing"
)

func storeRevisionFixture(t *testing.T) (*Service, Actor, Actor, Resource, Resource, Resource) {
	t.Helper()
	s, officer, worker, _, old, input := releaseMaterialFixture(t)
	r := must(t, s, officer, "releases.create", input)
	r = must(t, s, officer, "releases.approve", M{"id": r.ID})
	must(t, s, officer, "releases.dispatch", M{"id": r.ID, "target": "ios-appstore", "workerId": worker.WorkerID, "key": "original"})
	run := must(t, s, worker, "tasks.claim", M{})
	must(t, s, worker, "tasks.complete", M{"id": run.ID, "leaseToken": S(run.Data, "leaseToken"), "exitCode": 0, "result": M{"state": "awaiting_review", "externalId": "apple:123:upload1"}})
	must(t, s, officer, "releases.refresh", M{"id": r.ID, "target": "ios-appstore", "key": "rejected"})
	run = must(t, s, worker, "tasks.claim", M{})
	must(t, s, worker, "tasks.complete", M{"id": run.ID, "leaseToken": S(run.Data, "leaseToken"), "exitCode": 0, "result": M{"state": "rejected", "externalId": "apple:123:upload1", "reason": "Privacy policy link needs correction", "upstreamState": "METADATA_REJECTED"}})
	r, _ = s.Store.Get(r.ID)
	issue := onlyReleaseIssue(t, s, r)
	triage := triageReleaseIssue(issue, "correct-text")
	triage["strategy"] = "store-text"
	issue = must(t, s, officer, "release_issues.triage", triage)
	meta := M{"goalId": r.GoalID, "target": "ios-appstore", "type": "report", "name": "Corrected store text", "commit": S(r.Data, "commit"), "repositoryId": S(old.Data, "repositoryId"), "mediaType": "application/json"}
	material, err := s.Upload(context.Background(), Actor{ID: "material-author", Kind: "human", Admin: true}, meta, strings.NewReader(`{"schemaVersion":1,"description":"Corrected privacy URL"}`))
	if err != nil {
		t.Fatal(err)
	}
	return s, officer, worker, r, issue, material
}

func proposeStoreRevision(t *testing.T, s *Service, officer Actor, issue, material Resource) Resource {
	t.Helper()
	return must(t, s, officer, "store_revisions.propose", M{"issueId": issue.ID, "metadataArtifactId": material.ID, "key": "propose-correction"})
}

func TestStoreRevisionApprovalDispatchAndExactPublicationEvidence(t *testing.T) {
	s, officer, worker, r, issue, material := storeRevisionFixture(t)
	author := Actor{ID: "revision-author", Kind: "human", Admin: true}
	rev := proposeStoreRevision(t, s, author, issue, material)
	if _, err := s.Command(context.Background(), author, "store_revisions.approve", M{"id": rev.ID, "expectedBinding": S(rev.Data, "binding")}); err == nil {
		t.Fatal("proposal author self-approved")
	}
	if _, err := s.Command(context.Background(), officer, "store_revisions.dispatch", M{"id": rev.ID, "workerId": worker.WorkerID, "key": "unapproved"}); err == nil {
		t.Fatal("old release approval authorized new material")
	}
	rev = must(t, s, officer, "store_revisions.approve", M{"id": rev.ID, "expectedBinding": S(rev.Data, "binding")})
	task := must(t, s, officer, "store_revisions.dispatch", M{"id": rev.ID, "workerId": worker.WorkerID, "key": "approved-correction"})
	if S(task.Data, "kind") != "publish_materials" || S(task.Data, "binding") != S(r.Data, "binding") || S(task.Data, "publicationBinding") != S(rev.Data, "binding") || S(task.Data, "sourceOperationKey") != S(Obj(issue.Data["source"]), "operationKey") {
		t.Fatal("material task lost original and new approval identity", task.Data)
	}
	run := must(t, s, worker, "tasks.claim", M{})
	if run.Kind != "run" {
		t.Fatal("approved correction not claimable", run)
	}
	claimed, _ := s.Store.Get(r.ID)
	if S(releaseTarget(claimed, "ios-appstore"), "status") != "revising_materials" {
		t.Fatal("channel did not truthfully enter material revision")
	}
	must(t, s, worker, "tasks.complete", M{"id": run.ID, "leaseToken": S(run.Data, "leaseToken"), "exitCode": 0, "result": M{"state": "awaiting_review", "externalId": "apple:123:upload1"}})
	// A second rejection creates a new issue/epoch, preserving the first one.
	must(t, s, officer, "releases.refresh", M{"id": r.ID, "target": "ios-appstore", "key": "second-rejection"})
	query2 := must(t, s, worker, "tasks.claim", M{})
	must(t, s, worker, "tasks.complete", M{"id": query2.ID, "leaseToken": S(query2.Data, "leaseToken"), "exitCode": 0, "result": M{"state": "rejected", "externalId": "apple:123:upload1", "reason": "Second synthetic metadata correction", "upstreamState": "METADATA_REJECTED"}})
	secondRelease, _ := s.Store.Get(r.ID)
	secondIssue, _ := s.Store.Get(S(releaseTarget(secondRelease, "ios-appstore"), "rejectionIssueId"))
	if secondIssue.ID == issue.ID || S(Obj(secondIssue.Data["source"]), "materialRevisionId") != rev.ID {
		t.Fatal("second rejection overwrote first issue or lost prior material epoch")
	}
	triage2 := triageReleaseIssue(secondIssue, "second-text-decision")
	triage2["strategy"] = "store-text"
	secondIssue = must(t, s, officer, "release_issues.triage", triage2)
	secondMaterial, err := s.Upload(context.Background(), Actor{ID: "material-author", Kind: "human", Admin: true}, M{"goalId": r.GoalID, "target": "ios-appstore", "type": "report", "name": "Second corrected material", "commit": S(material.Data, "commit"), "repositoryId": S(material.Data, "repositoryId")}, strings.NewReader(`{"schemaVersion":1,"description":"Second corrected URL"}`))
	if err != nil {
		t.Fatal(err)
	}
	secondRevision := proposeStoreRevision(t, s, author, secondIssue, secondMaterial)
	must(t, s, officer, "store_revisions.approve", M{"id": secondRevision.ID, "expectedBinding": S(secondRevision.Data, "binding")})
	must(t, s, officer, "store_revisions.dispatch", M{"id": secondRevision.ID, "workerId": worker.WorkerID, "key": "second-correction"})
	secondRun := must(t, s, worker, "tasks.claim", M{})
	must(t, s, worker, "tasks.complete", M{"id": secondRun.ID, "leaseToken": S(secondRun.Data, "leaseToken"), "exitCode": 0, "result": M{"state": "awaiting_review", "externalId": "apple:123:upload1"}})
	rev, material = secondRevision, secondMaterial
	must(t, s, officer, "releases.refresh", M{"id": r.ID, "target": "ios-appstore", "key": "corrected-published"})
	query := must(t, s, worker, "tasks.claim", M{})
	if S(Obj(query.Data["task"]), "metadataDigest") != S(material.Data, "digest") || S(Obj(query.Data["task"]), "publicationBinding") != S(rev.Data, "binding") {
		t.Fatal("query reused old material approval")
	}
	must(t, s, worker, "tasks.complete", M{"id": query.ID, "leaseToken": S(query.Data, "leaseToken"), "exitCode": 0, "result": M{"state": "published", "externalId": "apple:123:upload1"}})
	open, _ := s.Store.Get(issue.ID)
	if open.Status == "resolved" {
		t.Fatal("published observation resolved issue")
	}
	current, _ := s.Store.Get(r.ID)
	pkg, _ := s.Store.Get(S(releaseTarget(current, "ios-appstore"), "artifactId"))
	qa := Actor{ID: "material-independent-qa", Kind: "human", Admin: true}
	proof, err := s.Upload(context.Background(), qa, M{"goalId": r.GoalID, "target": "ios-appstore", "type": "report", "name": "Synthetic publication fixture", "commit": S(pkg.Data, "commit"), "repositoryId": S(pkg.Data, "repositoryId")}, strings.NewReader("Synthetic local channel proof, not a real Apple release"))
	if err != nil {
		t.Fatal(err)
	}
	evInput := M{"artifactId": pkg.ID, "proofArtifactId": proof.ID, "method": "device", "verificationEnvironment": "ios-device", "purpose": "publication", "externalId": "apple:123:upload1", "statement": "Synthetic acceptance test for exact corrected material"}
	old := must(t, s, qa, "evidence.record", evInput)
	if _, err = s.Command(context.Background(), qa, "releases.verify", M{"id": r.ID, "target": "ios-appstore", "evidenceId": old.ID}); err == nil {
		t.Fatal("unbound old material publication evidence accepted")
	}
	evInput["releaseId"], evInput["publicationBinding"] = r.ID, S(rev.Data, "binding")
	ev := must(t, s, qa, "evidence.record", evInput)
	result := must(t, s, qa, "releases.verify", M{"id": r.ID, "target": "ios-appstore", "evidenceId": ev.ID})
	if result.Status != "delivered" || S(result.Data, "binding") != S(r.Data, "binding") {
		t.Fatal("same-package material delivery changed base approval")
	}
	done, _ := s.Store.Get(issue.ID)
	if done.Status != "resolved" || S(Obj(done.Data["resolution"]), "materialRevisionId") != rev.ID {
		t.Fatal("independent evidence failed to resolve exact material chain")
	}
	secondDone, _ := s.Store.Get(secondIssue.ID)
	if secondDone.Status != "resolved" {
		t.Fatal("latest rejection was not resolved with its predecessor")
	}
}

func TestStoreRevisionRejectsChangedSourceAndDecision(t *testing.T) {
	for _, scenario := range []string{"wrong-binding", "same-material", "wrong-worker", "changed-triage", "changed-requirements", "unknown-fields", "agent-approval"} {
		t.Run(scenario, func(t *testing.T) {
			s, officer, worker, r, issue, material := storeRevisionFixture(t)
			rev := proposeStoreRevision(t, s, Actor{ID: "revision-author", Kind: "human", Admin: true}, issue, material)
			input := M{"id": rev.ID, "expectedBinding": S(rev.Data, "binding")}
			actor := officer
			switch scenario {
			case "wrong-binding":
				input["expectedBinding"] = strings.Repeat("f", 64)
			case "same-material":
				old := S(Obj(Items(r.Data["storeMetadata"])[0]), "id")
				if _, err := s.Command(context.Background(), officer, "store_revisions.propose", M{"issueId": issue.ID, "metadataArtifactId": old, "key": "old"}); err == nil {
					t.Fatal("unchanged material accepted")
				}
				return
			case "wrong-worker":
				must(t, s, officer, "store_revisions.approve", input)
				if _, err := s.Command(context.Background(), officer, "store_revisions.dispatch", M{"id": rev.ID, "workerId": "worker_other", "key": "wrong-node"}); err == nil {
					t.Fatal("revision moved away from original publishing journal")
				}
				return
			case "changed-triage":
				must(t, s, officer, "release_issues.triage", triageReleaseIssue(issue, "new-build-instead"))
			case "changed-requirements":
				if err := s.Store.Update(context.Background(), func(tx *Tx) error { g, _ := tx.Get(r.GoalID); g.Revision++; return tx.Put(g) }); err != nil {
					t.Fatal(err)
				}
			case "unknown-fields":
				input["force"] = true
			case "agent-approval":
				actor = Actor{ID: "agent", Kind: "agent", Admin: true}
			}
			if _, err := s.Command(context.Background(), actor, "store_revisions.approve", input); err == nil {
				t.Fatal("unsafe material approval accepted", scenario)
			}
			_ = worker
		})
	}
}

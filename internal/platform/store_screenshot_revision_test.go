package platform

import (
	"context"
	"strings"
	"testing"
)

func storeScreenshotFixture(t *testing.T) (*Service, Actor, Actor, Resource, Resource, Resource) {
	t.Helper()
	s, officer, worker, release, issue, material := storeRevisionFixture(t)
	in := triageReleaseIssue(issue, "approved-screenshot-correction")
	in["strategy"], in["instructions"] = "store-media", "Replace the inaccurate screenshots using the original reviewed build"
	issue = must(t, s, officer, "release_issues.triage", in)
	return s, officer, worker, release, issue, material
}

// Removing the explicit screenshot scope or allowing a continuation to lose it
// must fail before a new task can run under a text-only approval.
func TestStoreScreenshotRevisionProcessingAndExactContinuation(t *testing.T) {
	s, officer, worker, release, issue, material := storeScreenshotFixture(t)
	rev := proposeStoreRevision(t, s, Actor{ID: "screenshot-author", Kind: "human", Admin: true}, issue, material)
	if S(Obj(rev.Data["spec"]), "materialScope") != "screenshots" {
		t.Fatal("screenshot authority was not included in the approval binding")
	}
	must(t, s, officer, "store_revisions.approve", M{"id": rev.ID, "expectedBinding": S(rev.Data, "binding")})
	task := must(t, s, officer, "store_revisions.dispatch", M{"id": rev.ID, "workerId": worker.WorkerID, "key": "screenshots"})
	if S(task.Data, "materialScope") != "screenshots" {
		t.Fatal("dispatch lost screenshot scope")
	}
	run := must(t, s, worker, "tasks.claim", M{})
	must(t, s, worker, "tasks.complete", M{"id": run.ID, "leaseToken": S(run.Data, "leaseToken"), "exitCode": 0, "result": M{"state": "processing", "externalId": "apple:123:upload1", "reason": "Synthetic screenshot processing; original IPA retained"}})
	current, _ := s.Store.Get(release.ID)
	if S(releaseTarget(current, "ios-appstore"), "status") != "processing" || S(current.Data, "binding") != S(release.Data, "binding") || Hash(current.Data["artifacts"]) != Hash(release.Data["artifacts"]) {
		t.Fatal("processing changed the original package or failed to persist waiting state")
	}
	if idle := must(t, s, worker, "tasks.claim", M{}); idle.Kind == "run" {
		t.Fatal("processing retained or immediately repeated a mutating task")
	}
	must(t, s, officer, "releases.refresh", M{"id": release.ID, "target": "ios-appstore", "key": "screenshots-ready"})
	query := must(t, s, worker, "tasks.claim", M{})
	if S(Obj(query.Data["task"]), "materialScope") != "screenshots" {
		t.Fatal("read-only query lost exact approved scope")
	}
	must(t, s, worker, "tasks.complete", M{"id": query.ID, "leaseToken": S(query.Data, "leaseToken"), "exitCode": 0, "result": M{"state": "processing", "externalId": "apple:123:upload1", "continueRequired": true}})
	next := must(t, s, worker, "tasks.claim", M{})
	data := Obj(next.Data["task"])
	if S(data, "kind") != "publish_continue" || S(data, "materialScope") != "screenshots" || S(data, "publicationBinding") != S(rev.Data, "binding") || S(data, "operationKey") != S(task.Data, "operationKey") {
		t.Fatal("continuation did not retain the one approved material operation", data)
	}
	must(t, s, worker, "tasks.complete", M{"id": next.ID, "leaseToken": S(next.Data, "leaseToken"), "exitCode": 0, "result": M{"state": "awaiting_review", "externalId": "apple:123:upload1"}})
	current, _ = s.Store.Get(release.ID)
	open, _ := s.Store.Get(issue.ID)
	if S(releaseTarget(current, "ios-appstore"), "status") != "awaiting_review" || open.Status == "resolved" {
		t.Fatal("resubmission must still wait for actual publication and independent acceptance")
	}
	if again := must(t, s, officer, "store_revisions.dispatch", M{"id": rev.ID, "workerId": worker.WorkerID, "key": "screenshots"}); again.ID != task.ID {
		t.Fatal("dispatch replay created another screenshot operation")
	}
}

func TestStoreScreenshotRevisionScopeCannotDriftOrEscalateText(t *testing.T) {
	for _, scenario := range []string{"scope-task", "scope-target", "scope-spec", "decision", "revoked"} {
		t.Run(scenario, func(t *testing.T) {
			s, officer, worker, release, issue, material := storeScreenshotFixture(t)
			rev := proposeStoreRevision(t, s, Actor{ID: "screenshot-author", Kind: "human", Admin: true}, issue, material)
			must(t, s, officer, "store_revisions.approve", M{"id": rev.ID, "expectedBinding": S(rev.Data, "binding")})
			task := must(t, s, officer, "store_revisions.dispatch", M{"id": rev.ID, "workerId": worker.WorkerID, "key": scenario})
			if err := s.Store.Update(context.Background(), func(tx *Tx) error {
				switch scenario {
				case "scope-task":
					v, _ := tx.Get(task.ID)
					delete(v.Data, "materialScope")
					return tx.Put(v)
				case "scope-target":
					v, _ := tx.Get(release.ID)
					delete(releaseTarget(v, "ios-appstore"), "materialScope")
					return tx.Put(v)
				case "scope-spec":
					v, _ := tx.Get(rev.ID)
					Obj(v.Data["spec"])["materialScope"] = "text"
					return tx.Put(v)
				case "decision":
					v, _ := tx.Get(issue.ID)
					v.Revision++
					return tx.Put(v)
				default:
					v, _ := tx.Get(release.GoalID)
					v.Revision++
					return tx.Put(v)
				}
			}); err != nil {
				t.Fatal(err)
			}
			if claim := must(t, s, worker, "tasks.claim", M{}); claim.Kind == "run" {
				t.Fatal("changed scope or authority still granted execution")
			}
		})
	}
	s, officer, worker, _, issue, material := storeRevisionFixture(t)
	rev := proposeStoreRevision(t, s, Actor{ID: "text-author", Kind: "human", Admin: true}, issue, material)
	must(t, s, officer, "store_revisions.approve", M{"id": rev.ID, "expectedBinding": S(rev.Data, "binding")})
	task := must(t, s, officer, "store_revisions.dispatch", M{"id": rev.ID, "workerId": worker.WorkerID, "key": "legacy-text"})
	if S(task.Data, "materialScope") != "" {
		t.Fatal("legacy text approval silently gained screenshot authority")
	}
	run := must(t, s, worker, "tasks.claim", M{})
	_, err := s.Command(context.Background(), worker, "tasks.complete", M{"id": run.ID, "leaseToken": S(run.Data, "leaseToken"), "exitCode": 0, "result": M{"state": "processing", "externalId": "apple:123:upload1", "reason": strings.Repeat("x", 4)}})
	if err == nil {
		t.Fatal("text-only correction entered a screenshot processing continuation")
	}
}

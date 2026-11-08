package platform

import (
	"bytes"
	"context"
	"testing"
	"time"
)

func publishFixture(t *testing.T) (*Service, Actor, Actor, Resource) {
	return publishTargetFixture(t, "web")
}

func publishTargetFixture(t *testing.T, target string) (*Service, Actor, Actor, Resource) {
	t.Helper()
	s, owner, project := fixture(t)
	return setupPublishTargetFixture(t, s, owner, project, target)
}

func setupPublishTargetFixture(t *testing.T, s *Service, owner Actor, project Resource, target string) (*Service, Actor, Actor, Resource) {
	t.Helper()
	g := must(t, s, owner, "goals.create", M{"projectId": project.ID, "title": "Publish recovery", "outcome": "Deliver the verified portal", "targets": []any{target}, "criteria": []any{M{"id": "content", "text": "Portal loads", "method": "manual"}}, "budget": 50})
	must(t, s, owner, "goals.approve", M{"id": g.ID})
	plan := must(t, s, owner, "plans.propose", M{"goalId": g.ID, "summary": "Build portal", "tasks": []any{M{"id": "code", "title": "Implement", "kind": "code", "role": "developer", "runtime": "linux"}}})
	must(t, s, owner, "plans.approve", M{"id": plan.ID})
	repo := must(t, s, owner, "repositories.create", M{"projectId": project.ID, "title": "portal"})
	repo = must(t, s, owner, "repositories.commit", M{"id": repo.ID, "branch": "feature", "message": "Add portal", "files": M{"index.html": "Portal"}})
	meta := M{"goalId": g.ID, "repositoryId": repo.ID, "commit": S(repo.Data, "lastCommit"), "target": target, "name": "portal.html", "type": "package"}
	artifact, e := s.Upload(context.Background(), owner, meta, bytes.NewBufferString("Portal"))
	if e != nil {
		t.Fatal(e)
	}
	qa := Actor{ID: "qa", Kind: "human", Admin: true}
	meta["type"] = "report"
	meta["name"] = "report.txt"
	proof, e := s.Upload(context.Background(), qa, meta, bytes.NewBufferString("Portal loads in browser"))
	if e != nil {
		t.Fatal(e)
	}
	must(t, s, qa, "evidence.record", M{"artifactId": artifact.ID, "proofArtifactId": proof.ID, "method": "manual", "verificationEnvironment": func() string {
		if target == "web" {
			return "browser"
		}
		return "simulator"
	}(), "criteria": []any{"content"}, "statement": "Synthetic acceptance fixture"})
	if target != "web" {
		must(t, s, qa, "evidence.record", M{"artifactId": artifact.ID, "proofArtifactId": proof.ID, "method": "device", "purpose": "environment", "verificationEnvironment": targetEnvironment(target), "statement": "Synthetic device attestation for protocol test; no physical device acceptance"})
	}
	release := must(t, s, owner, "releases.create", M{"goalId": g.ID, "version": "1.0.0", "commit": S(repo.Data, "lastCommit"), "artifactIds": []any{artifact.ID}})
	officer := Actor{ID: "release-officer", Kind: "human", Admin: true}
	must(t, s, officer, "releases.approve", M{"id": release.ID})
	w := must(t, s, owner, "workers.register", M{"projectId": project.ID, "title": "Publisher", "runtime": "linux", "roles": []any{"release"}, "capabilities": []any{"publish"}})
	worker := Actor{ID: "worker:" + w.ID, WorkerID: w.ID, ProjectID: project.ID, Kind: "worker", Roles: []string{"release"}}
	return s, officer, worker, release
}

func TestPublishRecoveryNeverReplaysUnknownExternalAction(t *testing.T) {
	for _, scenario := range []string{"lease_expired", "deadline", "manual_reconcile"} {
		t.Run(scenario, func(t *testing.T) {
			s, officer, worker, release := publishFixture(t)
			request := M{"id": release.ID, "target": "web", "workerId": worker.WorkerID, "key": "first-operation"}
			task := must(t, s, officer, "releases.dispatch", request)
			if replay := must(t, s, officer, "releases.dispatch", request); replay.ID != task.ID {
				t.Fatal("duplicate request created a second publication")
			}
			run := must(t, s, worker, "tasks.claim", M{})
			lease := S(run.Data, "leaseToken")
			if scenario == "manual_reconcile" {
				must(t, s, officer, "releases.reconcile", M{"id": release.ID, "target": "web", "state": "reconciliation_required", "reason": "Operator is checking the external channel"})
			} else {
				if scenario == "lease_expired" {
					run.Data["leaseUntil"] = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
				} else {
					run.Data["deadline"] = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
				}
				if e := s.Store.Update(context.Background(), func(tx *Tx) error { return tx.Put(run) }); e != nil {
					t.Fatal(e)
				}
				if scenario == "lease_expired" {
					idle := must(t, s, worker, "tasks.claim", M{})
					if idle.Kind == "run" {
						t.Fatal("unknown publication was retried")
					}
				} else {
					must(t, s, worker, "tasks.progress", M{"id": run.ID, "leaseToken": lease, "message": "Still running"})
				}
			}
			current, _ := s.Store.Get(release.ID)
			if S(Obj(Items(current.Data["targets"])[0]), "status") != "reconciliation_required" {
				t.Fatalf("missing reconciliation: %+v", current)
			}
			if _, e := s.Command(context.Background(), officer, "tasks.retry", M{"id": task.ID}); e == nil {
				t.Fatal("publication bypassed reconciliation")
			}
			must(t, s, officer, "releases.reconcile", M{"id": release.ID, "target": "web", "state": "ready", "reason": "Confirmed no external operation was accepted"})
			if _, e := s.Command(context.Background(), worker, "tasks.complete", M{"id": run.ID, "leaseToken": lease, "exitCode": 0, "result": M{"externalId": "late-result", "state": "published"}}); e == nil {
				t.Fatal("old publisher overwrote reconciled state")
			}
			request["key"] = "new-operation"
			next := must(t, s, officer, "releases.dispatch", request)
			if next.ID == task.ID {
				t.Fatal("explicit reconciled retry reused stale task")
			}
		})
	}
}

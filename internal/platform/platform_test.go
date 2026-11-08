package platform

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
)

func fixture(t *testing.T) (*Service, Actor, Resource) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	svc := NewService(s)
	a := Actor{ID: "human-admin", Name: "Owner", Kind: "human", Admin: true}
	p := must(t, svc, a, "projects.create", M{"title": "Customer support", "description": "Support application"})
	return svc, a, p
}

func must(t *testing.T, s *Service, a Actor, command string, input M) Resource {
	t.Helper()
	r, err := s.Command(context.Background(), a, command, input)
	if err != nil {
		t.Fatalf("%s: %v", command, err)
	}
	return r
}

func goal(t *testing.T, s *Service, a Actor, p Resource) Resource {
	return must(t, s, a, "goals.create", M{"projectId": p.ID, "title": "Support portal", "outcome": "Customers can create and track requests", "criteria": []any{M{"id": "ticket", "text": "A customer can submit a request"}}, "targets": []any{"web"}, "budget": 100.0})
}

func TestBaselineRequiresHumanAndPlanApproval(t *testing.T) {
	s, a, p := fixture(t)
	g := goal(t, s, a, p)
	agent := Actor{ID: "agent", Kind: "agent", ProjectID: p.ID, Roles: []string{"developer"}}
	if _, err := s.Command(context.Background(), agent, "goals.approve", M{"id": g.ID}); err == nil {
		t.Fatal("agent approved a human requirement gate")
	}
	must(t, s, a, "goals.approve", M{"id": g.ID})
	plan := must(t, s, a, "plans.propose", M{"goalId": g.ID, "summary": "Build request workflow", "tasks": []any{M{"id": "implement", "title": "Implement API", "role": "developer", "runtime": "linux", "kind": "code"}}})
	if plan.Status != "proposed" {
		t.Fatalf("unreviewed plan became %s", plan.Status)
	}
	must(t, s, a, "plans.approve", M{"id": plan.ID})
}

func TestProjectIsolation(t *testing.T) {
	s, a, p := fixture(t)
	g := goal(t, s, a, p)
	outsider := Actor{ID: "outsider", Kind: "human"}
	if _, err := s.Command(context.Background(), outsider, "goals.revise", M{"id": g.ID, "outcome": "Change scope"}); err == nil {
		t.Fatal("outsider changed a requirement")
	}
}

func TestPlanRejectsCycles(t *testing.T) {
	s, a, p := fixture(t)
	g := goal(t, s, a, p)
	must(t, s, a, "goals.approve", M{"id": g.ID})
	_, err := s.Command(context.Background(), a, "plans.propose", M{"goalId": g.ID, "summary": "Cycle", "tasks": []any{M{"id": "a", "title": "A", "role": "developer", "runtime": "linux", "kind": "code", "dependsOn": []any{"b"}}, M{"id": "b", "title": "B", "role": "quality", "runtime": "linux", "kind": "test", "dependsOn": []any{"a"}}}})
	if err == nil {
		t.Fatal("cyclic plan accepted")
	}
}

func TestMessageIdempotencyAndPayloadConflict(t *testing.T) {
	s, a, p := fixture(t)
	g := goal(t, s, a, p)
	registerMembershipIdentity(t, s, "reviewer", false, false)
	must(t, s, a, "members.grant", M{"projectId": p.ID, "identityId": "reviewer", "name": "Reviewer", "roles": []any{"developer"}})
	in := M{"projectId": p.ID, "goalId": g.ID, "recipient": "reviewer", "type": "clarification", "key": "one", "payload": M{"question": "Which users?"}}
	x := must(t, s, a, "messages.send", in)
	y := must(t, s, a, "messages.send", in)
	if x.ID != y.ID {
		t.Fatal("duplicate delivery created another message")
	}
	in["payload"] = M{"question": "Different question"}
	if _, err := s.Command(context.Background(), a, "messages.send", in); err == nil {
		t.Fatal("idempotency key accepted a different payload")
	}
}

func TestUnverifiedTargetsCannotDeliverRelease(t *testing.T) {
	s, a, p := fixture(t)
	g := goal(t, s, a, p)
	if _, err := s.Command(context.Background(), a, "releases.create", M{"goalId": g.ID, "version": "1.0.0", "commit": "0123456789012345678901234567890123456789"}); err == nil {
		t.Fatal("release without artifacts and independent evidence accepted")
	}
}

func TestWorkerIdleHeartbeatPersists(t *testing.T) {
	s, a, p := fixture(t)
	w := must(t, s, a, "workers.register", M{"projectId": p.ID, "title": "Linux runner", "roles": []any{"developer"}, "capabilities": []any{"code"}, "runtime": "linux"})
	wa := Actor{ID: "worker:" + w.ID, Kind: "worker", WorkerID: w.ID, ProjectID: p.ID, Roles: []string{"developer"}}
	_, err := s.Command(context.Background(), wa, "tasks.claim", M{})
	if err != nil {
		t.Fatalf("an idle heartbeat must persist: %v", err)
	}
	stored, err := s.Store.Get(w.ID)
	if err != nil || S(stored.Data, "lastSeen") == "" {
		t.Fatal("idle worker heartbeat lost")
	}
}

func TestRevisionInvalidatesApprovedPlan(t *testing.T) {
	s, a, p := fixture(t)
	g := goal(t, s, a, p)
	must(t, s, a, "goals.approve", M{"id": g.ID})
	pl := must(t, s, a, "plans.propose", M{"goalId": g.ID, "summary": "Implement", "tasks": []any{M{"id": "code", "title": "Implement", "role": "developer", "runtime": "linux", "kind": "code"}}})
	must(t, s, a, "plans.approve", M{"id": pl.ID})
	g = must(t, s, a, "goals.revise", M{"id": g.ID, "reason": "Customers need additional scope", "outcome": "New scope"})
	old, _ := s.Store.Get(pl.ID)
	if g.Revision != 2 || old.Status != "stale" || int(N(g.Data, "approvedRevision")) != 0 {
		t.Fatal("old approvals survived requirement change")
	}
}

func TestLeaseOwnershipAndDuplicateCompletion(t *testing.T) {
	s, a, p := fixture(t)
	g := goal(t, s, a, p)
	must(t, s, a, "goals.approve", M{"id": g.ID})
	pl := must(t, s, a, "plans.propose", M{"goalId": g.ID, "summary": "Analyze", "tasks": []any{M{"id": "analyze", "title": "Analyze", "role": "developer", "runtime": "linux", "kind": "analysis"}}})
	must(t, s, a, "plans.approve", M{"id": pl.ID})
	w := must(t, s, a, "workers.register", M{"projectId": p.ID, "title": "Runner", "roles": []any{"developer"}, "capabilities": []any{"analysis"}, "runtime": "linux"})
	wa := Actor{ID: "worker:" + w.ID, Kind: "worker", WorkerID: w.ID, ProjectID: p.ID, Roles: []string{"developer"}}
	run := must(t, s, wa, "tasks.claim", M{})
	in := M{"id": run.ID, "leaseToken": S(run.Data, "leaseToken"), "exitCode": 0, "result": M{"summary": "Completed"}}
	bad := wa
	bad.WorkerID = "another"
	if _, e := s.Command(context.Background(), bad, "tasks.complete", in); e == nil {
		t.Fatal("another worker completed the lease")
	}
	first := must(t, s, wa, "tasks.complete", in)
	second := must(t, s, wa, "tasks.complete", in)
	if first.ID != second.ID || first.Status != "succeeded" {
		t.Fatal("completion replay is not idempotent")
	}
}

func TestNativeGitCommitAndReviewBinding(t *testing.T) {
	s, a, p := fixture(t)
	repo := must(t, s, a, "repositories.create", M{"projectId": p.ID, "title": "app"})
	repo = must(t, s, a, "repositories.commit", M{"id": repo.ID, "branch": "feature", "message": "Add API", "files": M{"main.go": "package main\nfunc main() {}\n"}})
	review := must(t, s, a, "reviews.create", M{"repositoryId": repo.ID, "branch": "feature", "title": "Review API"})
	if _, e := s.Command(context.Background(), a, "reviews.approve", M{"id": review.ID}); e == nil {
		t.Fatal("author approved own code")
	}
	reviewer := Actor{ID: "reviewer", Kind: "human", Admin: true}
	must(t, s, reviewer, "reviews.approve", M{"id": review.ID})
	repo = must(t, s, a, "repositories.commit", M{"id": repo.ID, "branch": "feature", "expectedHead": S(repo.Data, "lastCommit"), "message": "Change API", "files": M{"main.go": "package main\nfunc main() { println(1) }\n"}})
	if _, e := s.Command(context.Background(), reviewer, "reviews.merge", M{"id": review.ID}); e == nil {
		t.Fatal("old approval applied to new commit")
	}
}

func TestSafePathRejectsTraversalAndGitMetadata(t *testing.T) {
	for _, path := range []string{"../outside", "/etc/passwd", "a/.git/config", "a\\b"} {
		if _, e := SafePath(t.TempDir(), path); e == nil {
			t.Fatalf("accepted unsafe path %q", path)
		}
	}
}

func TestIndependentManualReleaseLifecycle(t *testing.T) {
	s, a, p := fixture(t)
	g := must(t, s, a, "goals.create", M{"projectId": p.ID, "title": "Portal", "outcome": "Users submit requests", "criteria": []any{M{"id": "submit", "text": "A user submits a request", "method": "manual"}}, "targets": []any{"web"}, "budget": 50})
	must(t, s, a, "goals.approve", M{"id": g.ID})
	pl := must(t, s, a, "plans.propose", M{"goalId": g.ID, "summary": "Portal implementation", "tasks": []any{M{"id": "code", "title": "Implement", "role": "developer", "runtime": "linux", "kind": "code"}}})
	must(t, s, a, "plans.approve", M{"id": pl.ID})
	repo := must(t, s, a, "repositories.create", M{"projectId": p.ID, "title": "portal"})
	repo = must(t, s, a, "repositories.commit", M{"id": repo.ID, "branch": "feature", "message": "Portal", "files": M{"index.html": "<h1>Portal</h1>"}})
	commit := S(repo.Data, "lastCommit")
	artifact, e := s.Upload(context.Background(), a, M{"goalId": g.ID, "repositoryId": repo.ID, "commit": commit, "target": "web", "name": "portal.html", "type": "package"}, bytes.NewBufferString("<h1>Portal</h1>"))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Command(context.Background(), a, "evidence.record", M{"artifactId": artifact.ID, "method": "automated"}); e == nil {
		t.Fatal("human forged automated evidence")
	}
	qa := Actor{ID: "qa", Kind: "human", Admin: true}
	proof, e := s.Upload(context.Background(), qa, M{"goalId": g.ID, "repositoryId": repo.ID, "commit": commit, "target": "web", "name": "verification.txt", "type": "report"}, bytes.NewBufferString("Checked customer request submission"))
	if e != nil {
		t.Fatal(e)
	}
	must(t, s, qa, "evidence.record", M{"artifactId": artifact.ID, "proofArtifactId": proof.ID, "method": "manual", "verificationEnvironment": "browser", "criteria": []any{"submit"}, "statement": "Request was submitted and visible"})
	release := must(t, s, a, "releases.create", M{"goalId": g.ID, "version": "1.0.0", "commit": commit, "artifactIds": []any{artifact.ID}})
	officer := Actor{ID: "release-officer", Kind: "human", Admin: true}
	must(t, s, officer, "releases.approve", M{"id": release.ID})
	release = must(t, s, officer, "releases.transition", M{"id": release.ID, "target": "web", "state": "published", "externalId": "deployment-1", "proofArtifactId": proof.ID})
	if release.Status == "delivered" {
		t.Fatal("published was incorrectly treated as verified")
	}
	ev := must(t, s, qa, "evidence.record", M{"artifactId": artifact.ID, "proofArtifactId": proof.ID, "method": "manual", "verificationEnvironment": "browser", "purpose": "publication", "externalId": "deployment-1", "statement": "Verified installed production version"})
	release = must(t, s, qa, "releases.verify", M{"id": release.ID, "target": "web", "evidenceId": ev.ID})
	if release.Status != "delivered" {
		t.Fatalf("verified release status %s", release.Status)
	}
	must(t, s, a, "goals.revise", M{"id": g.ID, "reason": "New acceptance requirement", "outcome": "Changed"})
	old, _ := s.Store.Get(release.ID)
	if old.Status != "stale" {
		t.Fatal("released binding survived changed requirements")
	}
}

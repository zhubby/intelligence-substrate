package platform

import (
	"context"
	"strings"
	"testing"
)

func rejectedReleaseFixture(t *testing.T) (*Service, Actor, Actor, Resource, Resource) {
	t.Helper()
	s, officer, worker, r := awaitingChannel(t)
	must(t, s, officer, "releases.refresh", M{"id": r.ID, "target": "web", "key": "reject-query"})
	run := must(t, s, worker, "tasks.claim", M{})
	in := M{"id": run.ID, "leaseToken": S(run.Data, "leaseToken"), "exitCode": 0, "result": M{"externalId": "channel-version-123", "state": "rejected", "reason": "Contact flow does not persist submissions", "upstreamState": "REJECTED"}}
	must(t, s, worker, "tasks.complete", in)
	must(t, s, worker, "tasks.complete", in)
	r, _ = s.Store.Get(r.ID)
	return s, officer, worker, r, onlyReleaseIssue(t, s, r)
}

func onlyReleaseIssue(t *testing.T, s *Service, r Resource) Resource {
	t.Helper()
	issues, err := s.Store.List(r.ProjectID, "release_issue", r.GoalID)
	if err != nil || len(issues) != 1 {
		t.Fatalf("rejection must create exactly one durable issue, got %d: %v", len(issues), err)
	}
	return issues[0]
}

func triageReleaseIssue(issue Resource, key string) M {
	return M{"id": issue.ID, "expectedRevision": issue.Revision, "expectedBinding": S(issue.Data, "binding"), "key": key, "strategy": "new-build", "instructions": "Persist every accepted contact submission; add independent persistence and permissions tests before requesting release."}
}

func TestReleaseRejectionCreatesOneBoundIssueAndReleasesResources(t *testing.T) {
	s, _, worker, r, issue := rejectedReleaseFixture(t)
	target := releaseTarget(r, "web")
	source := Obj(issue.Data["source"])
	if issue.Status != "open" || S(target, "rejectionIssueId") != issue.ID || S(source, "releaseId") != r.ID || S(source, "releaseBinding") != S(r.Data, "binding") || S(source, "artifactId") != S(target, "artifactId") || S(source, "externalId") != "channel-version-123" || S(source, "reason") != "Contact flow does not persist submissions" || S(issue.Data, "binding") != Hash(source) {
		t.Fatalf("issue lost authoritative rejection context: %+v", issue)
	}
	pkg, _ := s.Store.Get(S(source, "artifactId"))
	if S(source, "artifactDigest") != S(pkg.Data, "digest") || S(source, "commit") != S(pkg.Data, "commit") || S(source, "repositoryId") != S(pkg.Data, "repositoryId") || S(Obj(source["observation"]), "actorId") != worker.ID {
		t.Fatal("issue does not identify the exact rejected package and observing actor")
	}
	if idle := must(t, s, worker, "tasks.claim", M{}); idle.Kind == "run" {
		t.Fatal("rejection retained resources or resubmitted a publication")
	}
	if S(target, "nextCheckAt") != "" || r.Status != "blocked" {
		t.Fatal("rejection lost truthful channel state")
	}
}

func TestRejectedCandidateCannotReuseApprovalByResettingState(t *testing.T) {
	for _, viaHalt := range []bool{false, true} {
		t.Run(map[bool]string{false: "direct", true: "halted"}[viaHalt], func(t *testing.T) {
			s, officer, _, r := awaitingChannel(t)
			must(t, s, officer, "releases.transition", M{"id": r.ID, "target": "web", "state": "rejected", "externalId": "channel-version-123", "reason": "Privacy flow rejected in a synthetic local channel"})
			if viaHalt {
				must(t, s, officer, "releases.transition", M{"id": r.ID, "target": "web", "state": "halted", "reason": "Stop rejected candidate"})
			}
			_, err := s.Command(context.Background(), officer, "releases.reconcile", M{"id": r.ID, "target": "web", "state": "ready", "reason": "Attempt to reuse old approval"})
			if err == nil || !strings.Contains(err.Error(), "拒审") {
				t.Fatalf("rejected package reused old release approval: %v", err)
			}
		})
	}
}

func TestReleaseIssueTriageIsHumanCASBoundAndIdempotent(t *testing.T) {
	s, officer, worker, r, issue := rejectedReleaseFixture(t)
	in := triageReleaseIssue(issue, "triage-once")
	for _, a := range []Actor{worker, {ID: "planner", Kind: "agent", Admin: true}, {ID: "viewer", Kind: "human"}} {
		if _, err := s.Command(context.Background(), a, "release_issues.triage", in); err == nil {
			t.Fatalf("non-authorized actor confirmed repair scope: %s", a.Kind)
		}
	}
	for _, key := range []string{"expectedRevision", "expectedBinding", "strategy", "instructions", "key"} {
		bad := copyM(in)
		delete(bad, key)
		if _, err := s.Command(context.Background(), officer, "release_issues.triage", bad); err == nil {
			t.Fatalf("triage accepted missing %s", key)
		}
	}
	bad := copyM(in)
	bad["strategy"] = "metadata-only"
	if _, err := s.Command(context.Background(), officer, "release_issues.triage", bad); err == nil {
		t.Fatal("unsupported material-only workflow claimed executable")
	}
	confirmed := must(t, s, officer, "release_issues.triage", in)
	if confirmed.Status != "triaged" || confirmed.Revision != issue.Revision+1 || S(Obj(confirmed.Data["triage"]), "instructions") != S(in, "instructions") || S(Obj(confirmed.Data["triage"]), "actorId") != officer.ID || S(confirmed.Data, "triageBinding") != Hash(confirmed.Data["triage"]) {
		t.Fatalf("human repair requirements not durably bound: %+v", confirmed)
	}
	if replay := must(t, s, officer, "release_issues.triage", in); replay.ID != issue.ID || replay.Revision != confirmed.Revision {
		t.Fatal("replayed triage changed revision")
	}
	bad = copyM(in)
	bad["instructions"] = "Different instructions under the same key"
	if _, err := s.Command(context.Background(), officer, "release_issues.triage", bad); err == nil {
		t.Fatal("triage key reused with different scope")
	}
	bad["key"] = "stale-version"
	if _, err := s.Command(context.Background(), officer, "release_issues.triage", bad); err == nil {
		t.Fatal("stale issue revision changed confirmed requirements")
	}
	if len(must(t, s, officer, "release_issues.triage", triageReleaseIssue(confirmed, "next-decision")).Data) == 0 {
		t.Fatal("current human decision did not apply")
	}
	if onlyReleaseIssue(t, s, r).Revision != 3 {
		t.Fatal("triage revisions were not preserved")
	}
}

func TestHumanRejectionRetainsKnownExternalVersion(t *testing.T) {
	s, officer, _, r := awaitingChannel(t)
	must(t, s, officer, "releases.transition", M{"id": r.ID, "target": "web", "state": "rejected", "reason": "Human checked rejection for the already submitted version"})
	issue := onlyReleaseIssue(t, s, r)
	if S(Obj(issue.Data["source"]), "externalId") != "channel-version-123" {
		t.Fatal("human rejection discarded the exact previously observed external version")
	}
}

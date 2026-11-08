package platform

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestPostgresReleaseIssueConcurrentObservationTriageAndRestore(t *testing.T) {
	dsn := os.Getenv("IS_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("set IS_TEST_POSTGRES_URL to an isolated PostgreSQL database")
	}
	admin, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := NewID("release_issue_test")
	if _, err = admin.DB.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	defer admin.DB.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	query := u.Query()
	query.Set("search_path", schema)
	u.RawQuery = query.Encode()
	first, err := Open(u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Open(u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	root := t.TempDir()
	if evidence := os.Getenv("IS_RELEASE_ISSUE_EVIDENCE_DIR"); evidence != "" {
		root = filepath.Join(evidence, "postgres-proof")
		if err = os.Mkdir(root, 0700); err != nil {
			t.Fatal(err)
		}
	}
	s := &Service{Store: first, Root: filepath.Join(root, "source")}
	other := &Service{Store: second, Root: s.Root}
	owner := Actor{ID: "issue-postgres-owner", Kind: "human", Admin: true}
	project := must(t, s, owner, "projects.create", M{"title": "Concurrent rejection remediation"})
	_, officer, worker, original := setupPublishTargetFixture(t, s, owner, project, "web")
	must(t, s, officer, "releases.dispatch", M{"id": original.ID, "target": "web", "workerId": worker.WorkerID, "key": "submitted"})
	run := must(t, s, worker, "tasks.claim", M{})
	must(t, s, worker, "tasks.complete", M{"id": run.ID, "leaseToken": S(run.Data, "leaseToken"), "exitCode": 0, "result": M{"state": "awaiting_review", "externalId": "local-channel-001"}})
	must(t, s, officer, "releases.refresh", M{"id": original.ID, "target": "web", "key": "rejection-query"})
	run = must(t, s, worker, "tasks.claim", M{})
	type result struct {
		r   Resource
		err error
	}
	concurrent := func(actor Actor, name string, inputs []M) []result {
		t.Helper()
		start, results := make(chan struct{}), make(chan result, 2)
		var wg sync.WaitGroup
		for i, svc := range []*Service{s, other} {
			wg.Add(1)
			go func(svc *Service, in M) {
				defer wg.Done()
				<-start
				r, err := svc.Command(context.Background(), actor, name, in)
				results <- result{r, err}
			}(svc, inputs[i])
		}
		close(start)
		wg.Wait()
		return []result{<-results, <-results}
	}
	observation := M{"id": run.ID, "leaseToken": S(run.Data, "leaseToken"), "exitCode": 0, "result": M{"state": "rejected", "externalId": "local-channel-001", "reason": "Synthetic channel rejects missing persistence"}}
	for _, outcome := range concurrent(worker, "tasks.complete", []M{observation, observation}) {
		if outcome.err != nil || outcome.r.ID != run.ID {
			t.Fatal("concurrent rejection acknowledgement failed", outcome)
		}
	}
	issue := onlyReleaseIssue(t, s, original)
	in := triageReleaseIssue(issue, "triage-once")
	for _, outcome := range concurrent(officer, "release_issues.triage", []M{in, in}) {
		if outcome.err != nil || outcome.r.ID != issue.ID || outcome.r.Revision != 2 {
			t.Fatal("parallel triage did not replay one decision", outcome)
		}
	}
	issue, _ = first.Get(issue.ID)
	one, two := triageReleaseIssue(issue, "first-edit"), triageReleaseIssue(issue, "second-edit")
	two["instructions"] = "Competing repair instructions must not overwrite the winning decision"
	winners, conflicts := 0, 0
	for _, outcome := range concurrent(officer, "release_issues.triage", []M{one, two}) {
		if outcome.err == nil {
			winners++
		} else {
			clarificationFault(t, outcome.err, 409)
			conflicts++
		}
	}
	if winners != 1 || conflicts != 1 {
		t.Fatal("optimistic repair decision lost its single winner", winners, conflicts)
	}
	plan := must(t, s, officer, "plans.propose", issuePlanInput(original))
	must(t, s, officer, "plans.approve", M{"id": plan.ID})
	archive := filepath.Join(root, "backup.tar.gz")
	if err = s.Backup(context.Background(), archive); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(root, "restored")
	if err = Restore(context.Background(), archive, destination, ""); err != nil {
		t.Fatal(err)
	}
	restoredStore, err := Open(filepath.Join(destination, "platform.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer restoredStore.Close()
	restored := &Service{Store: restoredStore, Root: destination}
	before, _ := first.List(project.ID, "", "")
	for _, resource := range before {
		after, err := restoredStore.Get(resource.ID)
		if err != nil || Hash(after) != Hash(resource) {
			t.Fatal("restore changed rejection authority", resource.ID, err)
		}
	}
	if replay := must(t, restored, officer, "release_issues.triage", in); replay.ID != issue.ID || len(Items(replay.Data["decisions"])) != 2 {
		t.Fatal("restore lost decision idempotency/history")
	}
	if err = restored.VerifyIntegrity(context.Background()); err != nil {
		t.Fatal(err)
	}
	facts := M{"passed": true, "syntheticChannelOnly": true, "paidModelCalled": false, "projectId": project.ID, "goalId": original.GoalID, "releaseId": original.ID, "issueId": issue.ID, "planId": plan.ID, "concurrentObservations": 2, "concurrentTriageAcknowledgements": 2, "editWinners": winners, "editConflicts": conflicts, "restoredResources": len(before)}
	body, _ := json.MarshalIndent(facts, "", "  ")
	if err = os.WriteFile(filepath.Join(root, "result.json"), append(body, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
}

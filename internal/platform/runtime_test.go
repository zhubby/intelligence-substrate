package platform

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

func deployedFixture(t *testing.T) (*Service, Actor, Actor, Resource, Resource) {
	t.Helper()
	s, officer, worker, release := publishFixture(t)
	task := must(t, s, officer, "releases.dispatch", M{"id": release.ID, "target": "web", "workerId": worker.WorkerID, "key": "runtime-first"})
	run := must(t, s, worker, "tasks.claim", M{})
	external := "runtime:feedback:" + S(task.Data, "operationKey")
	must(t, s, worker, "tasks.complete", M{"id": run.ID, "leaseToken": S(run.Data, "leaseToken"), "exitCode": 0, "result": M{"externalId": external, "state": "published", "url": "https://feedback.example.test", "runtime": M{"driver": "docker", "application": "feedback", "activationId": external, "imageId": "sha256:" + strings.Repeat("a", 64), "dataContract": "feedback-v1"}}})
	deployments, e := s.Store.List(release.ProjectID, "deployment", "")
	if e != nil || len(deployments) != 1 {
		t.Fatalf("publish did not register actual runtime deployment: %d %v", len(deployments), e)
	}
	return s, officer, worker, release, deployments[0]
}

func TestRollbackCreatesFreshOperationalAcceptanceAfterTargetRemoval(t *testing.T) {
	s, officer, worker, _, first := deployedFixture(t)
	second := secondDeployment(t, s, officer, worker, first)
	r := must(t, s, officer, "runtime.rollback.propose", M{"id": second.ID, "destinationId": first.ID, "reason": "Restore previous approved data-compatible backend", "key": "recovery"})
	must(t, s, officer, "runtime.rollback.approve", M{"id": r.ID})
	run := must(t, s, worker, "tasks.claim", M{})
	task := Obj(run.Data["task"])
	must(t, s, worker, "tasks.complete", M{"id": run.ID, "leaseToken": S(run.Data, "leaseToken"), "exitCode": 0, "result": M{"externalId": S(task, "expectedExternalId"), "state": "published"}})
	all, _ := s.Store.List(first.ProjectID, "deployment", first.GoalID)
	var active Resource
	for _, d := range all {
		if d.Status == "active" {
			active = d
		}
	}
	if active.ID == "" || active.ID == first.ID || S(active.Data, "externalId") == S(first.Data, "externalId") || S(active.Data, "verificationStatus") != "required" {
		t.Fatal("rollback reused historical activation or acceptance")
	}
	must(t, s, officer, "goals.revise", M{"id": first.GoalID, "reason": "A future client baseline removes Web while the existing service is maintained", "targets": []any{"windows"}})
	packageArtifact, _ := s.Store.Get(S(active.Data, "artifactId"))
	qa := Actor{ID: "runtime-quality", Kind: "human", Admin: true}
	report, e := s.Upload(context.Background(), qa, M{"goalId": active.GoalID, "deploymentId": active.ID, "repositoryId": S(packageArtifact.Data, "repositoryId"), "commit": S(active.Data, "commit"), "target": "web", "type": "report", "name": "actual-runtime.txt"}, bytes.NewBufferString("Actual old deployed version operational verification"))
	if e != nil {
		t.Fatal("future target removal blocked the running application's operational proof", e)
	}
	if _, e = s.Command(context.Background(), officer, "runtime.verify", M{"id": active.ID, "proofArtifactId": report.ID, "statement": "Operator cannot independently accept their own rollout"}); e == nil {
		t.Fatal("rollback approver accepted their own operational deployment")
	}
	verified := must(t, s, qa, "runtime.verify", M{"id": active.ID, "proofArtifactId": report.ID, "statement": "Verified actual running version and compatibility after recovery"})
	if S(verified.Data, "verificationStatus") != "verified" {
		t.Fatal("fresh independent operational verification missing")
	}
}
func reportRuntime(t *testing.T, s *Service, worker Actor, d Resource, sequence int, health string) Resource {
	t.Helper()
	return must(t, s, worker, "runtime.report", M{"externalId": S(d.Data, "externalId"), "releaseId": S(d.Data, "releaseId"), "binding": S(d.Data, "binding"), "sequence": sequence, "health": health, "checkedAt": now(), "statusCode": 200, "latencyMs": 12})
}
func TestRuntimeReportsIncidentsAndNeverGrantIndependentAcceptance(t *testing.T) {
	s, officer, worker, _, d := deployedFixture(t)
	for i := 1; i <= 3; i++ {
		d = reportRuntime(t, s, worker, d, i, "unhealthy")
	}
	incidents, _ := s.Store.List(d.ProjectID, "incident", "")
	if len(incidents) != 1 || incidents[0].Status != "open" {
		t.Fatal("sustained unhealthy probes did not open one incident")
	}
	if S(d.Data, "verificationStatus") != "required" {
		t.Fatal("operational health bypassed independent acceptance")
	}
	if _, e := s.Command(context.Background(), officer, "runtime.report", M{"externalId": S(d.Data, "externalId")}); e == nil {
		t.Fatal("untrusted human fabricated a runtime probe")
	}
	request := M{"externalId": S(d.Data, "externalId"), "releaseId": S(d.Data, "releaseId"), "binding": S(d.Data, "binding"), "sequence": 4, "health": "healthy", "checkedAt": now(), "statusCode": 200, "latencyMs": 10}
	first := must(t, s, worker, "runtime.report", request)
	again := must(t, s, worker, "runtime.report", request)
	if first.ID != again.ID {
		t.Fatal("duplicate report was not idempotent")
	}
	request["latencyMs"] = 99
	if _, e := s.Command(context.Background(), worker, "runtime.report", request); e == nil {
		t.Fatal("conflicting duplicate report accepted")
	}
	d = reportRuntime(t, s, worker, d, 5, "healthy")
	incidents, _ = s.Store.List(d.ProjectID, "incident", "")
	if len(incidents) != 1 || incidents[0].Status != "resolved" {
		t.Fatal("two healthy probes did not resolve the incident")
	}
	// Baseline changes must not disable observation of already-running software.
	must(t, s, officer, "goals.revise", M{"id": d.GoalID, "reason": "Future version scope changes"})
	reportRuntime(t, s, worker, d, 6, "healthy")
	if e := s.Store.Update(context.Background(), func(tx *Tx) error {
		current, _ := tx.Get(d.ID)
		current.Data["validUntil"] = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
		return tx.Put(current)
	}); e != nil {
		t.Fatal(e)
	}
	viewed, e := s.GetResource(context.Background(), officer, d.ID)
	if e != nil || S(viewed.Data, "health") != "unknown" {
		t.Fatal("expired monitor signal presented as current healthy state", e)
	}
}
func secondDeployment(t *testing.T, s *Service, officer, worker Actor, first Resource) Resource {
	t.Helper()
	release, err := s.Store.Get(S(first.Data, "releaseId"))
	if err != nil {
		t.Fatal(err)
	}
	// A second candidate using the same bytes is legitimate; its activation and
	// rollback authorization still have to be distinct.
	r := must(t, s, officer, "releases.create", M{"goalId": release.GoalID, "version": "1.1.0", "commit": S(release.Data, "commit"), "artifactIds": []any{S(first.Data, "artifactId")}})
	must(t, s, officer, "releases.approve", M{"id": r.ID})
	task := must(t, s, officer, "releases.dispatch", M{"id": r.ID, "target": "web", "workerId": worker.WorkerID, "key": "runtime-next"})
	run := must(t, s, worker, "tasks.claim", M{})
	external := "runtime:feedback:" + S(task.Data, "operationKey")
	must(t, s, worker, "tasks.complete", M{"id": run.ID, "leaseToken": S(run.Data, "leaseToken"), "exitCode": 0, "result": M{"externalId": external, "state": "published", "url": "https://feedback.example.test", "runtime": M{"driver": "docker", "application": "feedback", "activationId": external, "imageId": "sha256:" + strings.Repeat("b", 64), "dataContract": "feedback-v1"}}})
	deployments, _ := s.Store.List(r.ProjectID, "deployment", "")
	for _, d := range deployments {
		if d.Status == "active" {
			return d
		}
	}
	t.Fatal("new runtime was not activated")
	return Resource{}
}
func TestRuntimeRollbackRequiresHumanApprovalAndLostLeaseCannotReplay(t *testing.T) {
	s, officer, worker, _, first := deployedFixture(t)
	second := secondDeployment(t, s, officer, worker, first)
	if _, e := s.Command(context.Background(), worker, "runtime.report", M{"externalId": S(first.Data, "externalId"), "releaseId": S(first.Data, "releaseId"), "binding": S(first.Data, "binding"), "sequence": 1, "health": "healthy", "checkedAt": now(), "statusCode": 200, "latencyMs": 1}); e == nil {
		t.Fatal("old activation could overwrite current runtime health")
	}
	request := M{"id": second.ID, "destinationId": first.ID, "reason": "New version fails actual business checks", "key": "rollback-one"}
	rollback := must(t, s, officer, "runtime.rollback.propose", request)
	if _, e := s.Command(context.Background(), worker, "runtime.rollback.approve", M{"id": rollback.ID}); e == nil {
		t.Fatal("agent approved production rollback")
	}
	if _, e := s.Command(context.Background(), Actor{ID: "human-admin", Kind: "human", Admin: true}, "runtime.rollback.approve", M{"id": rollback.ID}); e == nil {
		t.Fatal("artifact producer approved their own production rollback")
	}
	must(t, s, officer, "runtime.rollback.approve", M{"id": rollback.ID})
	must(t, s, officer, "goals.revise", M{"id": second.GoalID, "reason": "New requirement does not disable safe operational recovery"})
	approvals, _ := s.Store.List(second.ProjectID, "approval", second.GoalID)
	for _, approval := range approvals {
		if S(approval.Data, "type") == "runtime_rollback" && approval.Status == "stale" {
			t.Fatal("future requirements invalidated already-approved operational recovery")
		}
	}
	run := must(t, s, worker, "tasks.claim", M{})
	if S(Obj(run.Data["task"]), "kind") != "runtime_rollback" {
		t.Fatal("approved recovery blocked by future requirement revision")
	}
	taskID := S(run.Data, "taskId")
	run.Data["leaseUntil"] = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
	if e := s.Store.Update(context.Background(), func(tx *Tx) error { return tx.Put(run) }); e != nil {
		t.Fatal(e)
	}
	if idle := must(t, s, worker, "tasks.claim", M{}); idle.Kind == "run" {
		t.Fatal("uncertain rollback was automatically retried")
	}
	current, _ := s.Store.Get(rollback.ID)
	if current.Status != "reconciliation_required" {
		t.Fatal("lost rollback lease did not require explicit reconciliation")
	}
	if _, e := s.Command(context.Background(), officer, "tasks.retry", M{"id": taskID}); e == nil {
		t.Fatal("generic retry bypassed rollback approval")
	}
}

func TestRuntimeDispatchCannotSilentlyMoveAnExistingApplicationToAnotherWorker(t *testing.T) {
	s, officer, _, release, first := deployedFixture(t)
	other := must(t, s, officer, "workers.register", M{"projectId": first.ProjectID, "title": "Other runtime node", "runtime": "linux", "roles": []any{"release"}, "capabilities": []any{"publish"}})
	r := must(t, s, officer, "releases.create", M{"goalId": release.GoalID, "version": "1.2.0", "commit": S(release.Data, "commit"), "artifactIds": []any{S(first.Data, "artifactId")}})
	must(t, s, officer, "releases.approve", M{"id": r.ID})
	if _, err := s.Command(context.Background(), officer, "releases.dispatch", M{"id": r.ID, "target": "web", "workerId": other.ID, "key": "move-node"}); err == nil {
		t.Fatal("publication could switch workers without a governed destination migration")
	}
}

func TestRuntimeMonitoringGapBreaksConsecutiveFailures(t *testing.T) {
	s, _, worker, _, d := deployedFixture(t)
	for i := 1; i <= 2; i++ {
		d = reportRuntime(t, s, worker, d, i, "unhealthy")
	}
	d.Data["validUntil"] = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
	if err := s.Store.Update(context.Background(), func(tx *Tx) error { return tx.Put(d) }); err != nil {
		t.Fatal(err)
	}
	d = reportRuntime(t, s, worker, d, 3, "unhealthy")
	incidents := runtimeIncidents(t, s, d, "application_unhealthy")
	if len(incidents) != 0 || N(d.Data, "failures") != 1 {
		t.Fatal("monitoring outage was counted as continuous observations")
	}
}

func TestRuntimeCompletionRequiresPublishedStateAndExactOperation(t *testing.T) {
	for _, scenario := range []string{"awaiting-review", "different-operation"} {
		t.Run(scenario, func(t *testing.T) {
			s, officer, worker, release := publishFixture(t)
			task := must(t, s, officer, "releases.dispatch", M{"id": release.ID, "target": "web", "workerId": worker.WorkerID, "key": "runtime-binding"})
			run := must(t, s, worker, "tasks.claim", M{})
			external := "runtime:feedback:" + S(task.Data, "operationKey")
			state := "published"
			if scenario == "awaiting-review" {
				state = "awaiting_review"
			} else {
				external = "runtime:feedback:" + strings.Repeat("f", 64)
			}
			_, err := s.Command(context.Background(), worker, "tasks.complete", M{"id": run.ID, "leaseToken": S(run.Data, "leaseToken"), "exitCode": 0, "result": M{"externalId": external, "state": state, "url": "https://feedback.example.test", "runtime": M{"driver": "docker", "application": "feedback", "activationId": external, "imageId": "sha256:" + strings.Repeat("a", 64), "dataContract": "feedback-v1"}}})
			if err == nil {
				t.Fatal("unpublished or differently bound runtime became an active deployment")
			}
			deployments, _ := s.Store.List(release.ProjectID, "deployment", release.GoalID)
			if len(deployments) != 0 {
				t.Fatal("invalid completion changed deployment state")
			}
		})
	}
}

func TestRuntimeVerificationCannotBorrowAnotherActivationReport(t *testing.T) {
	s, officer, worker, _, first := deployedFixture(t)
	second := secondDeployment(t, s, officer, worker, first)
	artifact, _ := s.Store.Get(S(first.Data, "artifactId"))
	qa := Actor{ID: "independent-runtime-qa", Kind: "human", Admin: true}
	proof, err := s.Upload(context.Background(), qa, M{"goalId": first.GoalID, "deploymentId": first.ID, "repositoryId": S(artifact.Data, "repositoryId"), "commit": S(first.Data, "commit"), "target": "web", "type": "report", "name": "other-activation.txt"}, bytes.NewBufferString("Report belongs to a different activation of the same bytes"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Command(context.Background(), qa, "runtime.verify", M{"id": second.ID, "proofArtifactId": proof.ID, "statement": "Same commit is insufficient to identify the actually checked deployment"}); err == nil {
		t.Fatal("another activation's report accepted")
	}
}

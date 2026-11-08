package platform

import (
	"context"
	"os"
	"strings"
	"testing"
)

func issuePlanInput(r Resource) M {
	in := generatedPlanContent()
	in["goalId"] = r.GoalID
	in["summary"] = "Repair persistence rejected by the channel, rebuild, and independently verify the new package."
	return in
}

func TestReleaseIssueMustBeTriagedAndFrozenInHumanAndModelPlans(t *testing.T) {
	s, officer, _, r, issue := rejectedReleaseFixture(t)
	if _, err := s.Command(context.Background(), officer, "plans.propose", issuePlanInput(r)); err == nil {
		t.Fatal("unconfirmed rejection did not block planning")
	}
	g, _ := s.Store.Get(r.GoalID)
	if _, _, err := s.BeginPlanGeneration(context.Background(), officer, planGenerationInput(g, "before-triage")); err == nil {
		t.Fatal("model planned against an unconfirmed rejection")
	}
	issue = must(t, s, officer, "release_issues.triage", triageReleaseIssue(issue, "confirm"))
	plan := must(t, s, officer, "plans.propose", issuePlanInput(r))
	entries := Items(plan.Data["releaseIssues"])
	if len(entries) != 1 || S(Obj(entries[0]), "id") != issue.ID || S(Obj(entries[0]), "triageBinding") != S(issue.Data, "triageBinding") || Hash(Obj(entries[0])["source"]) != S(issue.Data, "binding") {
		t.Fatal("manual plan lost frozen rejection context", plan)
	}
	request, started, err := s.BeginPlanGeneration(context.Background(), officer, planGenerationInput(g, "after-triage"))
	if err != nil || !started || Hash(Obj(request.Data["input"])["releaseIssues"]) != Hash(entries) {
		t.Fatal("model context did not include confirmed rejection", request, err)
	}
	putPlanGenerationCharge(t, s, request)
	generated, err := s.CompletePlanGeneration(context.Background(), officer, request.ID, generatedPlanContent())
	if err != nil || Hash(generated.Data["releaseIssues"]) != Hash(entries) {
		t.Fatal("model result lost frozen repair requirements", err)
	}
	issue = must(t, s, officer, "release_issues.triage", triageReleaseIssue(issue, "updated-scope"))
	if _, err := s.Command(context.Background(), officer, "plans.approve", M{"id": plan.ID}); err == nil {
		t.Fatal("changed human repair requirements did not invalidate old proposal")
	}
}

func TestReleaseIssueChangeInvalidatesInFlightGeneration(t *testing.T) {
	for _, mode := range []string{"triage", "baseline", "source-tamper", "source-bytes"} {
		t.Run(mode, func(t *testing.T) {
			s, officer, _, r, issue := rejectedReleaseFixture(t)
			issue = must(t, s, officer, "release_issues.triage", triageReleaseIssue(issue, "confirm"))
			g, _ := s.Store.Get(r.GoalID)
			request, _, err := s.BeginPlanGeneration(context.Background(), officer, planGenerationInput(g, "generate"))
			if err != nil {
				t.Fatal(err)
			}
			putPlanGenerationCharge(t, s, request)
			switch mode {
			case "triage":
				must(t, s, officer, "release_issues.triage", triageReleaseIssue(issue, "change"))
			case "baseline":
				must(t, s, officer, "goals.revise", M{"id": g.ID, "reason": "Updated requirements after rejection"})
				must(t, s, officer, "goals.approve", M{"id": g.ID})
			case "source-tamper":
				Obj(issue.Data["source"])["externalId"] = "another-app-version"
				if err := s.Store.Update(context.Background(), func(tx *Tx) error { return tx.Put(issue) }); err != nil {
					t.Fatal(err)
				}
			case "source-bytes":
				pkg, _ := s.Store.Get(S(Obj(issue.Data["source"]), "artifactId"))
				if err := os.WriteFile(s.ArtifactPath(S(pkg.Data, "digest")), []byte("altered rejected bytes"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = s.CompletePlanGeneration(context.Background(), officer, request.ID, generatedPlanContent()); err == nil {
				t.Fatal("stale or unverifiable rejection was adopted", mode)
			}
		})
	}
}

func repairIssueCandidate(t *testing.T, s *Service, officer Actor, sourceRelease, issue Resource, body string, verify bool) (Resource, Resource, Resource) {
	t.Helper()
	plan := must(t, s, officer, "plans.propose", issuePlanInput(sourceRelease))
	plan = must(t, s, officer, "plans.approve", M{"id": plan.ID})
	source := Obj(issue.Data["source"])
	producer := Actor{ID: "repair-developer", Kind: "human", Admin: true}
	repo := must(t, s, producer, "repositories.commit", M{"id": S(source, "repositoryId"), "branch": "repair-candidate", "baseCommit": S(source, "commit"), "message": "Persist rejected contact submissions", "files": M{"index.html": body, "repair-notes.txt": "Repair the persistence path and recheck ownership"}})
	meta := M{"goalId": sourceRelease.GoalID, "repositoryId": repo.ID, "commit": S(repo.Data, "lastCommit"), "target": "web", "name": "repaired.html", "type": "package"}
	pkg, err := s.Upload(context.Background(), producer, meta, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var proof Resource
	if verify {
		qa := Actor{ID: "repair-quality", Kind: "human", Admin: true}
		meta["type"] = "report"
		meta["name"] = "repair-acceptance.txt"
		proof, err = s.Upload(context.Background(), qa, meta, strings.NewReader("Synthetic local acceptance: persistence and permissions pass on repaired package"))
		if err != nil {
			t.Fatal(err)
		}
		must(t, s, qa, "evidence.record", M{"artifactId": pkg.ID, "proofArtifactId": proof.ID, "method": "manual", "verificationEnvironment": "browser", "criteria": []any{"content"}, "statement": "Synthetic protocol fixture; real execution has separate reports"})
	}
	return plan, pkg, proof
}

func TestReleaseIssueRequiresNewDescendantPackageAndIndependentEvidence(t *testing.T) {
	for _, mode := range []string{"old-candidate", "same-bytes", "no-evidence", "forged-snapshot", "fresh"} {
		t.Run(mode, func(t *testing.T) {
			s, officer, _, r, issue := rejectedReleaseFixture(t)
			issue = must(t, s, officer, "release_issues.triage", triageReleaseIssue(issue, "confirm"))
			pkg, _ := s.Store.Get(S(Obj(issue.Data["source"]), "artifactId"))
			if mode != "old-candidate" {
				body := "Portal stores contacts and checks ownership"
				if mode == "same-bytes" {
					body = "Portal"
				}
				_, pkg, _ = repairIssueCandidate(t, s, officer, r, issue, body, mode != "no-evidence")
			}
			in := M{"goalId": r.GoalID, "version": "1.0.1", "commit": S(pkg.Data, "commit"), "artifactIds": []any{pkg.ID}}
			if mode == "forged-snapshot" {
				in["releaseIssues"] = []any{}
			}
			next, err := s.Command(context.Background(), officer, "releases.create", in)
			if mode != "fresh" {
				if err == nil {
					t.Fatal("repair candidate bypassed fresh source/evidence/snapshot gate", mode)
				}
				return
			}
			if err != nil || next.Status != "awaiting_approval" || len(Items(next.Data["releaseIssues"])) != 1 || S(next.Data, "approvedBinding") != "" {
				t.Fatal("fresh repair did not require its own human approval", next, err)
			}
			if S(next.Data, "binding") != Hash(releaseBinding(next)) {
				t.Fatal("repair provenance missing from candidate binding")
			}
		})
	}
}

func TestReleaseIssueClosesOnlyAfterExactReplacementPublicationVerification(t *testing.T) {
	s, officer, worker, original, issue := rejectedReleaseFixture(t)
	issue = must(t, s, officer, "release_issues.triage", triageReleaseIssue(issue, "confirm"))
	_, pkg, proof := repairIssueCandidate(t, s, officer, original, issue, "Portal v2 correctly persists submissions", true)
	next := must(t, s, officer, "releases.create", M{"goalId": original.GoalID, "version": "1.0.1", "commit": S(pkg.Data, "commit"), "artifactIds": []any{pkg.ID}})
	if _, err := s.Command(context.Background(), officer, "releases.dispatch", M{"id": next.ID, "target": "web", "workerId": worker.WorkerID, "key": "without-approval"}); err == nil {
		t.Fatal("original release approval authorized the repaired candidate")
	}
	must(t, s, officer, "releases.approve", M{"id": next.ID})
	must(t, s, officer, "releases.dispatch", M{"id": next.ID, "target": "web", "workerId": worker.WorkerID, "key": "repaired-version"})
	run := must(t, s, worker, "tasks.claim", M{})
	must(t, s, worker, "tasks.complete", M{"id": run.ID, "leaseToken": S(run.Data, "leaseToken"), "exitCode": 0, "result": M{"externalId": "channel-repaired-124", "state": "published"}})
	current, _ := s.Store.Get(issue.ID)
	if current.Status == "resolved" {
		t.Fatal("publisher observation substituted for independent publication verification")
	}
	qa := Actor{ID: "repair-quality", Kind: "human", Admin: true}
	evidence := must(t, s, qa, "evidence.record", M{"artifactId": pkg.ID, "proofArtifactId": proof.ID, "method": "manual", "purpose": "publication", "verificationEnvironment": "browser", "externalId": "channel-repaired-124", "statement": "Synthetic channel publication verified independently for this exact replacement package"})
	must(t, s, qa, "releases.verify", M{"id": next.ID, "target": "web", "evidenceId": evidence.ID})
	current, _ = s.Store.Get(issue.ID)
	resolution := Obj(current.Data["resolution"])
	if current.Status != "resolved" || S(resolution, "releaseId") != next.ID || S(resolution, "releaseBinding") != S(next.Data, "binding") || S(resolution, "artifactId") != pkg.ID || S(resolution, "evidenceId") != evidence.ID {
		t.Fatal("exact replacement did not close the source issue", current)
	}
	if _, err := s.Command(context.Background(), officer, "release_issues.triage", triageReleaseIssue(current, "reopen-without-rejection")); err == nil {
		t.Fatal("resolved issue was reopened without a new rejection observation")
	}
}

func TestReleaseIssueHistoricalScopeRequiresReconfirmationWithoutErasingIssue(t *testing.T) {
	s, officer, _, r, issue := rejectedReleaseFixture(t)
	issue = must(t, s, officer, "release_issues.triage", triageReleaseIssue(issue, "original-confirmation"))
	must(t, s, officer, "goals.revise", M{"id": r.GoalID, "reason": "Changed business rules after channel rejection"})
	must(t, s, officer, "goals.approve", M{"id": r.GoalID})
	if _, err := s.Command(context.Background(), officer, "plans.propose", issuePlanInput(r)); err == nil {
		t.Fatal("old repair decision was silently carried into a new requirement baseline")
	}
	issue = must(t, s, officer, "release_issues.triage", triageReleaseIssue(issue, "confirmed-current-scope"))
	plan := must(t, s, officer, "plans.propose", issuePlanInput(r))
	entry := Obj(Items(plan.Data["releaseIssues"])[0])
	if N(Obj(entry["source"]), "requirementRevision") != 1 || N(Obj(entry["triage"]), "requirementRevision") != 2 {
		t.Fatal("historical rejection was confused with the new repair decision", entry)
	}
	must(t, s, officer, "goals.revise", M{"id": r.GoalID, "reason": "Current scope no longer ships Web", "targets": []any{"windows"}})
	must(t, s, officer, "goals.approve", M{"id": r.GoalID})
	plan = must(t, s, officer, "plans.propose", issuePlanInput(r))
	if len(Items(plan.Data["releaseIssues"])) != 0 {
		t.Fatal("removed channel blocked unrelated current scope")
	}
	current, _ := s.Store.Get(issue.ID)
	if current.Status == "resolved" || current.Data["resolution"] != nil {
		t.Fatal("scope removal fabricated successful remediation")
	}
}

func TestReleaseIssueRejectsWrongRepositoryUnrelatedHistoryAndAlteredPlan(t *testing.T) {
	for _, mode := range []string{"wrong-repository", "unrelated-history", "altered-plan"} {
		t.Run(mode, func(t *testing.T) {
			s, officer, _, r, issue := rejectedReleaseFixture(t)
			issue = must(t, s, officer, "release_issues.triage", triageReleaseIssue(issue, "confirm"))
			plan, pkg, _ := repairIssueCandidate(t, s, officer, r, issue, "Correct contact persistence", true)
			if mode == "altered-plan" {
				delete(plan.Data, "releaseIssues")
				if err := s.Store.Update(context.Background(), func(tx *Tx) error { return tx.Put(plan) }); err != nil {
					t.Fatal(err)
				}
			} else {
				source := Obj(issue.Data["source"])
				repo, _ := s.Store.Get(S(source, "repositoryId"))
				if mode == "wrong-repository" {
					repo = must(t, s, officer, "repositories.create", M{"projectId": r.ProjectID, "title": "Unrelated application source"})
				}
				base, err := s.Git(repo, "rev-parse", "refs/heads/main")
				if err != nil {
					t.Fatal(err)
				}
				repo = must(t, s, officer, "repositories.commit", M{"id": repo.ID, "branch": "unrelated-candidate", "baseCommit": base, "message": "Unrelated source cannot repair rejected lineage", "files": M{"index.html": "Unrelated application bytes"}})
				meta := M{"goalId": r.GoalID, "repositoryId": repo.ID, "commit": S(repo.Data, "lastCommit"), "target": "web", "type": "package", "name": "unrelated.html"}
				pkg, err = s.Upload(context.Background(), officer, meta, strings.NewReader("Unrelated new application bytes"))
				if err != nil {
					t.Fatal(err)
				}
				qa := Actor{ID: "unrelated-independent-qa", Kind: "human", Admin: true}
				meta["type"] = "report"
				proof, err := s.Upload(context.Background(), qa, meta, strings.NewReader("Synthetic independent acceptance for unrelated bytes"))
				if err != nil {
					t.Fatal(err)
				}
				must(t, s, qa, "evidence.record", M{"artifactId": pkg.ID, "proofArtifactId": proof.ID, "method": "manual", "verificationEnvironment": "browser", "criteria": []any{"content"}, "statement": "Unrelated source, synthetic fixture"})
			}
			_, err := s.Command(context.Background(), officer, "releases.create", M{"goalId": r.GoalID, "version": "1.0.1", "commit": S(pkg.Data, "commit"), "artifactIds": []any{pkg.ID}})
			if err == nil {
				t.Fatal("unrelated or altered repair was accepted", mode)
			}
		})
	}
}

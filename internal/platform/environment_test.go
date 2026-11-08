package platform

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestMobileCandidateRequiresIndependentRealDeviceEvidence(t *testing.T) {
	s, owner, project := fixture(t)
	g := must(t, s, owner, "goals.create", M{"projectId": project.ID, "title": "Mobile feedback", "outcome": "Users submit feedback", "targets": []any{"ios-appstore"}, "criteria": []any{M{"id": "submit", "text": "Submit feedback", "method": "manual"}}, "budget": 50})
	must(t, s, owner, "goals.approve", M{"id": g.ID})
	plan := must(t, s, owner, "plans.propose", M{"goalId": g.ID, "summary": "Build feedback application", "tasks": []any{M{"id": "code", "title": "Implement feedback", "kind": "code", "role": "developer", "runtime": "darwin"}}})
	must(t, s, owner, "plans.approve", M{"id": plan.ID})
	repo := must(t, s, owner, "repositories.create", M{"projectId": project.ID, "title": "mobile"})
	repo = must(t, s, owner, "repositories.commit", M{"id": repo.ID, "branch": "feature", "message": "Add source", "files": M{"app.txt": "source"}})
	meta := M{"goalId": g.ID, "repositoryId": repo.ID, "commit": S(repo.Data, "lastCommit"), "target": "ios-appstore", "name": "candidate.ipa", "type": "package"}
	artifact, e := s.Upload(context.Background(), owner, meta, bytes.NewBufferString("test fixture package; not a real native build"))
	if e != nil {
		t.Fatal(e)
	}
	qa := Actor{ID: "independent-qa", Kind: "human", Admin: true}
	meta["type"] = "report"
	meta["name"] = "report.txt"
	proof, e := s.Upload(context.Background(), qa, meta, bytes.NewBufferString("Test fixture report; no physical device claim"))
	if e != nil {
		t.Fatal(e)
	}
	must(t, s, qa, "evidence.record", M{"artifactId": artifact.ID, "proofArtifactId": proof.ID, "method": "manual", "verificationEnvironment": "simulator", "criteria": []any{"submit"}, "statement": "Business criteria checked in simulator"})
	candidate := M{"goalId": g.ID, "version": "1.0.0", "commit": S(repo.Data, "lastCommit"), "artifactIds": []any{artifact.ID}}
	if _, e = s.Command(context.Background(), owner, "releases.create", candidate); e == nil || !strings.Contains(e.Error(), "ios-device") {
		t.Fatalf("simulator bypassed device gate: %v", e)
	}
	if _, e = s.Command(context.Background(), qa, "evidence.record", M{"artifactId": artifact.ID, "proofArtifactId": proof.ID, "method": "manual", "purpose": "environment", "verificationEnvironment": "ios-device", "statement": "Invalid method"}); e == nil {
		t.Fatal("manual evidence mislabeled as physical-device evidence")
	}
	// Exercise the attestation trust boundary, not a real device verification.
	must(t, s, qa, "evidence.record", M{"artifactId": artifact.ID, "proofArtifactId": proof.ID, "method": "device", "purpose": "environment", "verificationEnvironment": "ios-device", "statement": "Synthetic attestation fixture exercises required device binding"})
	result := must(t, s, owner, "releases.create", candidate)
	if result.Status != "awaiting_approval" {
		t.Fatal("candidate skipped human release approval")
	}
}

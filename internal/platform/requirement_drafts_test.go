package platform

import (
	"context"
	"strings"
	"testing"
)

func requirementDraftInput(g Resource, key string) M {
	return M{"goalId": g.ID, "expectedRequirementRevision": g.Revision, "key": key,
		"content": M{"outcome": "Customers book services and cancel their own booking.", "brief": "Customers select a service and time. Staff manage availability. Payments are excluded.", "criteria": []any{M{"text": "An authenticated customer can cancel only their own booking.", "method": "automated"}}}}
}

func requirementDraftApply(draft Resource, key string) M {
	content := copyM(Obj(draft.Data["content"]))
	for _, item := range Items(content["criteria"]) {
		delete(Obj(item), "id")
	}
	return M{"id": draft.ID, "expectedRequirementRevision": draft.Data["requirementRevision"], "key": key, "reason": "Review clarified scope and verifiable acceptance", "content": content}
}

func answeredDraftQuestion(t *testing.T, s *Service, a Actor, g Resource) Resource {
	t.Helper()
	batch := must(t, s, a, "clarifications.propose", clarificationInput(g, "draft-question"))
	q := Obj(Items(batch.Data["questions"])[0])
	return must(t, s, a, "clarifications.answer", M{"id": batch.ID, "expectedRequirementRevision": g.Revision, "key": "answer", "questionId": q["id"], "answer": "Only the owner may cancel; payments are excluded."})
}

func TestRequirementDraftStartsWithBusinessGoalButEmptyCriteriaCannotApprove(t *testing.T) {
	s, a, p := fixture(t)
	g := must(t, s, a, "goals.create", M{"projectId": p.ID, "title": "Business description only", "outcome": "Help customers book a service", "targets": []string{"web"}})
	if g.Status != "awaiting_baseline" || len(Items(g.Data["criteria"])) != 0 {
		t.Fatal(g)
	}
	_, err := s.Command(context.Background(), a, "goals.approve", M{"id": g.ID})
	clarificationFault(t, err, 422)
	draft := must(t, s, a, "requirements.propose", requirementDraftInput(g, "first"))
	current := must(t, s, a, "requirements.apply", requirementDraftApply(draft, "adopt"))
	if current.Revision != 2 || current.Status != "awaiting_baseline" {
		t.Fatal(current)
	}
	must(t, s, a, "goals.approve", M{"id": g.ID, "expectedRequirementRevision": 2})
}

func TestRequirementDraftAppliesReviewedContentAndCarriesExactHumanDecision(t *testing.T) {
	s, a, p := fixture(t)
	g := goal(t, s, a, p)
	batch := answeredDraftQuestion(t, s, a, g)
	in := requirementDraftInput(g, "first")
	draft := must(t, s, a, "requirements.propose", in)
	if draft.Status != "proposed" || S(draft.Data, "inputDigest") != Hash(draft.Data["input"]) {
		t.Fatal(draft)
	}
	if replay := must(t, s, a, "requirements.propose", in); replay.ID != draft.ID {
		t.Fatal("duplicate draft")
	}
	apply := requirementDraftApply(draft, "adopt")
	Obj(apply["content"])["brief"] = "HUMAN-EDIT: Customers cancel their own booking. Payments excluded."
	current := must(t, s, a, "requirements.apply", apply)
	if current.Revision != 2 || S(current.Data, "requirementProposalId") != draft.ID || !strings.Contains(S(current.Data, "requirementBrief"), "HUMAN-EDIT") || N(current.Data, "approvedRevision") != 0 {
		t.Fatal(current)
	}
	if replay := must(t, s, a, "requirements.apply", apply); replay.Revision != 2 {
		t.Fatal("replayed adoption created revision")
	}
	_, err := s.Command(context.Background(), a, "requirements.apply", requirementDraftApply(draft, "different-key"))
	clarificationFault(t, err, 409)
	history, _ := s.Store.List(p.ID, "clarification", g.ID)
	if len(history) != 2 {
		t.Fatal(history)
	}
	var carried Resource
	for _, b := range history {
		if revision(b) == 2 {
			carried = b
		}
	}
	if S(carried.Data, "sourceBatchId") != batch.ID || N(carried.Data, "sourceRequirementRevision") != 1 || Hash(carried.Data["questions"]) != Hash(batch.Data["questions"]) {
		t.Fatal("lost original human answer", carried)
	}
	updated, _ := s.Store.Get(draft.ID)
	if updated.Status != "applied" || S(updated.Data, "appliedBy") != a.ID || S(Obj(updated.Data["appliedContent"]), "brief") != S(current.Data, "requirementBrief") {
		t.Fatal(updated)
	}
	must(t, s, a, "goals.approve", M{"id": g.ID, "expectedRequirementRevision": 2})
	resources, _ := s.Store.List(p.ID, "", g.ID)
	approved, _ := s.Store.Get(g.ID)
	answers, err := ApprovedClarifications(approved, resources)
	if err != nil || len(answers) != 1 {
		t.Fatal(answers, err)
	}
	versions, _ := s.Store.List(p.ID, "requirement", g.ID)
	for _, version := range versions {
		if version.Revision == 2 && S(version.Data, "requirementProposalId") != draft.ID {
			t.Fatal("revision missing proposal provenance", version)
		}
	}
}

func TestRequirementDraftRejectsChangedContextAndUnansweredQuestion(t *testing.T) {
	for _, mode := range []string{"question", "knowledge", "revision", "approval", "tamper"} {
		t.Run(mode, func(t *testing.T) {
			s, a, p := fixture(t)
			g := goal(t, s, a, p)
			draft := must(t, s, a, "requirements.propose", requirementDraftInput(g, "first"))
			switch mode {
			case "question":
				must(t, s, a, "clarifications.propose", clarificationInput(g, "new"))
			case "knowledge":
				must(t, s, a, "knowledge.create", M{"projectId": p.ID, "title": "Business scope", "content": "New payment constraints", "source": "Business review"})
			case "revision":
				must(t, s, a, "goals.revise", M{"id": g.ID, "reason": "Changed scope"})
			case "approval":
				must(t, s, a, "goals.approve", M{"id": g.ID})
			case "tamper":
				if err := s.Store.Update(context.Background(), func(tx *Tx) error { d, _ := tx.Get(draft.ID); d.Data["inputDigest"] = "wrong"; return tx.Put(d) }); err != nil {
					t.Fatal(err)
				}
			}
			_, err := s.Command(context.Background(), a, "requirements.apply", requirementDraftApply(draft, "adopt"))
			clarificationFault(t, err, 409)
			if mode == "question" {
				_, err = s.Command(context.Background(), a, "requirements.propose", requirementDraftInput(g, "second"))
				clarificationFault(t, err, 409)
			}
		})
	}
}

func TestRequirementDraftOnlyCurrentHumanProductMayApply(t *testing.T) {
	s, admin, p, owner, member := membershipFixture(t)
	g := goal(t, s, admin, p)
	agent := Actor{ID: "analyst", Kind: "agent", ProjectID: p.ID, Roles: []string{"analysis"}}
	draft := must(t, s, agent, "requirements.propose", requirementDraftInput(g, "agent-draft"))
	_, err := s.Command(context.Background(), agent, "requirements.apply", requirementDraftApply(draft, "agent-adopt"))
	clarificationFault(t, err, 403)
	worker := Actor{ID: "worker", Kind: "worker", ProjectID: p.ID, Roles: []string{"owner"}}
	_, err = s.Command(context.Background(), worker, "requirements.propose", requirementDraftInput(g, "worker-draft"))
	clarificationFault(t, err, 403)
	must(t, s, admin, "members.grant", M{"projectId": p.ID, "identityId": owner.ID, "roles": []string{"viewer"}, "expectedRevision": member.Revision})
	_, err = s.Command(context.Background(), owner, "requirements.apply", requirementDraftApply(draft, "revoked-adopt"))
	clarificationFault(t, err, 403)
	foreign := Actor{ID: "other", Kind: "human", ProjectID: "foreign", Roles: []string{"owner"}}
	_, err = s.Command(context.Background(), foreign, "requirements.apply", requirementDraftApply(draft, "foreign-adopt"))
	clarificationFault(t, err, 403)
}

func TestRequirementDraftStrictContentAndIdempotency(t *testing.T) {
	for _, mode := range []string{"budget", "id", "empty", "duplicate", "method", "long", "brief", "numeric"} {
		t.Run(mode, func(t *testing.T) {
			s, a, p := fixture(t)
			g := goal(t, s, a, p)
			in := requirementDraftInput(g, "first")
			c := Obj(in["content"])
			criteria := Items(c["criteria"])
			switch mode {
			case "budget":
				c["budget"] = 100000
			case "id":
				Obj(criteria[0])["id"] = "invented"
			case "empty":
				c["criteria"] = []any{}
			case "duplicate":
				c["criteria"] = append(criteria, copyM(Obj(criteria[0])))
			case "method":
				Obj(criteria[0])["method"] = "fake-device"
			case "long":
				Obj(criteria[0])["text"] = strings.Repeat("x", 2001)
			case "brief":
				c["brief"] = " "
			case "numeric":
				c["outcome"] = 12
			}
			_, err := s.Command(context.Background(), a, "requirements.propose", in)
			clarificationFault(t, err, 422)
			rows, _ := s.Store.List(p.ID, "requirement_proposal", g.ID)
			if len(rows) != 0 {
				t.Fatal(rows)
			}
		})
	}
	s, a, p := fixture(t)
	g := goal(t, s, a, p)
	in := requirementDraftInput(g, "first")
	must(t, s, a, "requirements.propose", in)
	Obj(in["content"])["brief"] = "Changed input"
	_, err := s.Command(context.Background(), a, "requirements.propose", in)
	clarificationFault(t, err, 409)
}

func TestRequirementDraftAdoptionAuditFailureRollsBackAllChanges(t *testing.T) {
	s, a, p := fixture(t)
	g := goal(t, s, a, p)
	answeredDraftQuestion(t, s, a, g)
	draft := must(t, s, a, "requirements.propose", requirementDraftInput(g, "first"))
	if _, err := s.Store.DB.Exec(`CREATE TRIGGER fail_requirement_audit BEFORE INSERT ON audit WHEN json_extract(NEW.body,'$.action')='requirements.apply' BEGIN SELECT RAISE(ABORT,'synthetic audit failure'); END`); err != nil {
		t.Fatal(err)
	}
	in := requirementDraftApply(draft, "adopt")
	if _, err := s.Command(context.Background(), a, "requirements.apply", in); err == nil {
		t.Fatal("unaudited adoption")
	}
	current, _ := s.Store.Get(g.ID)
	batches, _ := s.Store.List(p.ID, "clarification", g.ID)
	proposal, _ := s.Store.Get(draft.ID)
	if current.Revision != 1 || len(batches) != 1 || proposal.Status != "proposed" {
		t.Fatal(current, batches, proposal)
	}
	if _, err := s.Store.DB.Exec(`DROP TRIGGER fail_requirement_audit`); err != nil {
		t.Fatal(err)
	}
	if next := must(t, s, a, "requirements.apply", in); next.Revision != 2 {
		t.Fatal(next)
	}
}

func TestRequirementDraftManualRevisionCannotKeepOutdatedAdoptedDocument(t *testing.T) {
	s, a, p := fixture(t)
	g := goal(t, s, a, p)
	draft := must(t, s, a, "requirements.propose", requirementDraftInput(g, "first"))
	current := must(t, s, a, "requirements.apply", requirementDraftApply(draft, "adopt"))
	changed := must(t, s, a, "goals.revise", M{"id": g.ID, "expectedRequirementRevision": 2, "reason": "New business scope", "outcome": "An unrelated reporting application"})
	if S(changed.Data, "requirementBrief") != "" || S(changed.Data, "requirementProposalId") != "" || S(changed.Data, "requirementContentDigest") != "" {
		t.Fatal("manual scope change retained outdated adopted authority", changed)
	}
	_, err := s.Command(context.Background(), a, "goals.revise", M{"id": g.ID, "expectedRequirementRevision": current.Revision, "reason": "Stale edit", "outcome": "Old client overwrites new scope"})
	clarificationFault(t, err, 409)
}

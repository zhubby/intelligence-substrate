package platform

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func clarificationInput(g Resource, key string) M {
	return M{"goalId": g.ID, "expectedRequirementRevision": g.Revision, "key": key, "questions": []any{M{"question": "Who may cancel a confirmed booking?", "context": "This determines authorization and acceptance scenarios."}}}
}

func clarificationFault(t *testing.T, err error, status int) {
	t.Helper()
	var fault *Fault
	if !errors.As(err, &fault) || fault.Status != status {
		t.Fatalf("expected governed error %d, got %v", status, err)
	}
}

// An unresolved business decision must not disappear at baseline approval.
func TestClarificationBlocksBaselineAndBindsHumanAnswer(t *testing.T) {
	s, a, project := fixture(t)
	g := goal(t, s, a, project)
	batch := must(t, s, a, "clarifications.propose", clarificationInput(g, "booking-rights"))
	_, err := s.Command(context.Background(), a, "goals.approve", M{"id": g.ID})
	clarificationFault(t, err, 409)
	current, _ := s.Store.Get(g.ID)
	if current.Status != "awaiting_baseline" || N(current.Data, "approvedRevision") != 0 {
		t.Fatal("open clarification changed or bypassed the requirement gate", current)
	}
	question := Obj(Items(batch.Data["questions"])[0])
	answer := M{"id": batch.ID, "questionId": S(question, "id"), "expectedRequirementRevision": g.Revision, "key": "answer-once", "answer": "Customers may cancel their own booking until two hours before the start; staff may cancel any booking with a reason."}
	resolved := must(t, s, a, "clarifications.answer", answer)
	if resolved.Status != "answered" || N(resolved.Data, "requirementRevision") != 1 {
		t.Fatal("human answer lost its requirement binding", resolved)
	}
	got := Obj(Items(resolved.Data["questions"])[0])
	if S(got, "answer") != S(answer, "answer") || S(got, "answeredBy") != a.ID || S(got, "answeredAt") == "" {
		t.Fatal("confirmed answer has no human provenance", got)
	}
	must(t, s, a, "goals.approve", M{"id": g.ID, "expectedRequirementRevision": 1})
	approvals, _ := s.Store.List(project.ID, "approval", g.ID)
	if len(approvals) != 1 || len(Items(approvals[0].Data["clarifications"])) != 1 || len(S(approvals[0].Data, "clarificationsDigest")) != 64 {
		t.Fatal("approval did not retain the precise answered questions", approvals)
	}
	if Hash(approvals[0].Data["clarifications"]) != S(approvals[0].Data, "clarificationsDigest") {
		t.Fatal("persisted approval snapshot no longer matches its digest")
	}
	snapshot := Obj(Items(approvals[0].Data["clarifications"])[0])
	if S(snapshot, "id") != batch.ID || S(Obj(Items(Obj(snapshot["data"])["questions"])[0]), "answer") != S(answer, "answer") {
		t.Fatal("approval bound a different answer", snapshot)
	}
	_, err = s.Command(context.Background(), a, "clarifications.propose", clarificationInput(g, "after-approval"))
	clarificationFault(t, err, 409)
	answer["key"] = "overwrite-frozen"
	answer["answer"] = "Anyone may cancel everything"
	_, err = s.Command(context.Background(), a, "clarifications.answer", answer)
	clarificationFault(t, err, 409)
}

// Network retries cannot multiply questions or overwrite a confirmed answer.
func TestClarificationReplayAndImmutableAnswer(t *testing.T) {
	s, a, project := fixture(t)
	g := goal(t, s, a, project)
	in := clarificationInput(g, "first")
	batch := must(t, s, a, "clarifications.propose", in)
	if replay := must(t, s, a, "clarifications.propose", in); replay.ID != batch.ID {
		t.Fatal("proposal retry created another batch")
	}
	question := Obj(Items(batch.Data["questions"])[0])
	answer := M{"id": batch.ID, "questionId": S(question, "id"), "expectedRequirementRevision": 1, "key": "answer", "answer": "Only authenticated customers may cancel their own booking."}
	must(t, s, a, "clarifications.answer", answer)
	if replay := must(t, s, a, "clarifications.answer", answer); replay.ID != batch.ID || replay.Status != "answered" {
		t.Fatal("answer retry lost the confirmed decision", replay)
	}
	answer["answer"] = "Changed decision"
	_, err := s.Command(context.Background(), a, "clarifications.answer", answer)
	clarificationFault(t, err, 409)
	answer["key"] = "different-request"
	_, err = s.Command(context.Background(), a, "clarifications.answer", answer)
	clarificationFault(t, err, 409)
	changed := clarificationInput(g, "first")
	changed["questions"] = []any{M{"question": "A different question"}}
	_, err = s.Command(context.Background(), a, "clarifications.propose", changed)
	clarificationFault(t, err, 409)
	duplicate := clarificationInput(g, "duplicate-spelling")
	duplicate["questions"] = []any{M{"question": " WHO may  cancel a confirmed booking? "}}
	_, err = s.Command(context.Background(), a, "clarifications.propose", duplicate)
	clarificationFault(t, err, 409)
}

// Old answers remain historical; they cannot answer or approve a new scope.
func TestClarificationRevisionIsolationAndStaleApproval(t *testing.T) {
	s, a, project := fixture(t)
	g := goal(t, s, a, project)
	batch := must(t, s, a, "clarifications.propose", clarificationInput(g, "old"))
	current := must(t, s, a, "goals.revise", M{"id": g.ID, "reason": "Change cancellation policy", "outcome": "New business scope"})
	question := Obj(Items(batch.Data["questions"])[0])
	_, err := s.Command(context.Background(), a, "clarifications.answer", M{"id": batch.ID, "questionId": S(question, "id"), "expectedRequirementRevision": 1, "key": "old-reply", "answer": "Answer for obsolete scope"})
	clarificationFault(t, err, 409)
	_, err = s.Command(context.Background(), a, "goals.approve", M{"id": g.ID, "expectedRequirementRevision": 1})
	clarificationFault(t, err, 409)
	must(t, s, a, "goals.approve", M{"id": current.ID, "expectedRequirementRevision": 2})
	historical, _ := s.Store.Get(batch.ID)
	if historical.Status != "open" || N(historical.Data, "requirementRevision") != 1 {
		t.Fatal("new baseline erased or adopted old questions", historical)
	}
}

// Agents can raise a question but cannot impersonate the business decision maker.
func TestClarificationUsesCurrentHumanAuthority(t *testing.T) {
	s, admin, project, owner, member := membershipFixture(t)
	g := goal(t, s, admin, project)
	agent := Actor{ID: "analysis-agent", Kind: "agent", ProjectID: project.ID, Roles: []string{"developer"}}
	batch := must(t, s, agent, "clarifications.propose", clarificationInput(g, "agent-question"))
	question := Obj(Items(batch.Data["questions"])[0])
	in := M{"id": batch.ID, "questionId": S(question, "id"), "expectedRequirementRevision": 1, "key": "decision", "answer": "Authenticated customers only"}
	for _, kind := range []string{"agent", "worker"} {
		actor := Actor{ID: "non-human-" + kind, Kind: kind, ProjectID: project.ID, Roles: []string{"owner"}}
		_, err := s.Command(context.Background(), actor, "clarifications.answer", in)
		clarificationFault(t, err, 403)
	}
	worker := Actor{ID: "unleased-worker", Kind: "worker", ProjectID: project.ID, Roles: []string{"owner"}}
	_, err := s.Command(context.Background(), worker, "clarifications.propose", clarificationInput(g, "worker"))
	clarificationFault(t, err, 403)
	must(t, s, admin, "members.grant", M{"projectId": project.ID, "identityId": owner.ID, "roles": []string{"viewer"}, "expectedRevision": member.Revision})
	owner.Roles = []string{"owner"}
	_, err = s.Command(context.Background(), owner, "clarifications.answer", in)
	clarificationFault(t, err, 403)
	_, err = s.Command(context.Background(), owner, "clarifications.propose", clarificationInput(g, "revoked"))
	clarificationFault(t, err, 403)
	foreign := Actor{ID: "foreign-owner", Kind: "human", ProjectID: "other-project", Roles: []string{"owner"}}
	_, err = s.Command(context.Background(), foreign, "clarifications.answer", in)
	clarificationFault(t, err, 403)
}

func TestClarificationRejectsMalformedAndExcessiveQuestions(t *testing.T) {
	cases := []struct {
		name  string
		value any
	}{
		{"no-questions", []any{}}, {"wrong-type", "question"}, {"mixed", []any{"text"}},
		{"empty-question", []any{M{"question": " "}}}, {"numeric-question", []any{M{"question": 12}}},
		{"long-question", []any{M{"question": strings.Repeat("x", 2001)}}},
		{"long-context", []any{M{"question": "Who?", "context": strings.Repeat("x", 2001)}}},
		{"wrong-context", []any{M{"question": "Who?", "context": false}}},
		{"forged-answer", []any{M{"question": "Who?", "answer": "forged"}}},
		{"duplicate", []any{M{"question": "Who?"}, M{"question": "who?"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, a, project := fixture(t)
			g := goal(t, s, a, project)
			in := clarificationInput(g, tc.name)
			in["questions"] = tc.value
			_, err := s.Command(context.Background(), a, "clarifications.propose", in)
			clarificationFault(t, err, 422)
			batches, _ := s.Store.List(project.ID, "clarification", g.ID)
			if len(batches) != 0 {
				t.Fatal("malformed input partially wrote questions")
			}
		})
	}
}

func TestClarificationAuditFailureRollsBackLedgerAndRetry(t *testing.T) {
	s, a, project := fixture(t)
	g := goal(t, s, a, project)
	if _, err := s.Store.DB.Exec(`CREATE TRIGGER fail_clarification_audit BEFORE INSERT ON audit BEGIN SELECT RAISE(ABORT, 'synthetic audit failure'); END`); err != nil {
		t.Fatal(err)
	}
	_, err := s.Command(context.Background(), a, "clarifications.propose", clarificationInput(g, "atomic"))
	if err == nil {
		t.Fatal("proposal succeeded without audit")
	}
	items, _ := s.Store.List(project.ID, "clarification", g.ID)
	var remembered int
	if err := s.Store.DB.QueryRow(`SELECT COUNT(*) FROM idempotency`).Scan(&remembered); err != nil || remembered != 0 || len(items) != 0 {
		t.Fatal("audit failure left partial questions or retry state", remembered, items, err)
	}
}

func TestClarificationCapsBatchAndCurrentRevisionVolume(t *testing.T) {
	s, a, project := fixture(t)
	g := goal(t, s, a, project)
	questions := func(start, count int) []any {
		out := []any{}
		for i := start; i < start+count; i++ {
			out = append(out, M{"question": fmt.Sprintf("Business decision number %d?", i)})
		}
		return out
	}
	large := clarificationInput(g, "large")
	large["questions"] = questions(0, 11)
	_, err := s.Command(context.Background(), a, "clarifications.propose", large)
	clarificationFault(t, err, 422)
	for i := 0; i < 10; i++ {
		in := clarificationInput(g, fmt.Sprintf("batch-%d", i))
		in["questions"] = questions(i*10, 10)
		must(t, s, a, "clarifications.propose", in)
	}
	_, err = s.Command(context.Background(), a, "clarifications.propose", clarificationInput(g, "over-cap"))
	clarificationFault(t, err, 409)
	current := must(t, s, a, "goals.revise", M{"id": g.ID, "reason": "New scope with a new discovery budget"})
	must(t, s, a, "clarifications.propose", clarificationInput(current, "new-revision"))
}

func TestClarificationAnswerAuditFailureRollsBackDecision(t *testing.T) {
	s, a, project := fixture(t)
	g := goal(t, s, a, project)
	batch := must(t, s, a, "clarifications.propose", clarificationInput(g, "ask"))
	question := Obj(Items(batch.Data["questions"])[0])
	if _, err := s.Store.DB.Exec(`CREATE TRIGGER fail_answer_audit BEFORE INSERT ON audit WHEN json_extract(NEW.body,'$.action')='clarifications.answer' BEGIN SELECT RAISE(ABORT,'synthetic answer audit failure'); END`); err != nil {
		t.Fatal(err)
	}
	_, err := s.Command(context.Background(), a, "clarifications.answer", M{"id": batch.ID, "questionId": S(question, "id"), "expectedRequirementRevision": 1, "key": "answer", "answer": "Only the assigned owner."})
	if err == nil {
		t.Fatal("answer succeeded without its audit")
	}
	current, _ := s.Store.Get(batch.ID)
	var retries int
	if err := s.Store.DB.QueryRow(`SELECT COUNT(*) FROM idempotency`).Scan(&retries); err != nil || retries != 1 || current.Status != "open" || S(Obj(Items(current.Data["questions"])[0]), "answer") != "" {
		t.Fatal("audit failure partially confirmed the answer", current, retries, err)
	}
}

func TestClarificationApprovedContextRejectsTamperedOrMalformedSnapshot(t *testing.T) {
	for _, mode := range []string{"digest", "missing-snapshot", "foreign-batch", "unanswered", "conflicting-approval"} {
		t.Run(mode, func(t *testing.T) {
			s, a, project := fixture(t)
			g := goal(t, s, a, project)
			batch := must(t, s, a, "clarifications.propose", clarificationInput(g, "ask"))
			q := Obj(Items(batch.Data["questions"])[0])
			must(t, s, a, "clarifications.answer", M{"id": batch.ID, "questionId": S(q, "id"), "expectedRequirementRevision": 1, "key": "answer", "answer": "Confirmed human decision"})
			g = must(t, s, a, "goals.approve", M{"id": g.ID})
			approvals, _ := s.Store.List(project.ID, "approval", g.ID)
			approval := approvals[0]
			snapshot := Items(approval.Data["clarifications"])
			switch mode {
			case "digest":
				approval.Data["clarificationsDigest"] = "wrong"
			case "missing-snapshot":
				delete(approval.Data, "clarifications")
			case "foreign-batch":
				Obj(snapshot[0])["projectId"] = "other-project"
				approval.Data["clarificationsDigest"] = Hash(snapshot)
			case "unanswered":
				Obj(Items(Obj(Obj(snapshot[0])["data"])["questions"])[0])["status"] = "open"
				approval.Data["clarificationsDigest"] = Hash(snapshot)
			case "conflicting-approval":
				other := approval
				other.ID = NewID("approval")
				other.Data = copyM(approval.Data)
				other.Data["clarifications"] = []any{}
				other.Data["clarificationsDigest"] = Hash([]any{})
				approvals = append(approvals, other)
			}
			approvals[0] = approval
			_, err := ApprovedClarifications(g, approvals)
			clarificationFault(t, err, 409)
		})
	}
}

package platform

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
	"time"
)

func expenseInput(g Resource, reference string) M {
	return M{"goalId": g.ID, "title": "Isolated build allocation", "category": "build", "amountCents": 1234, "currency": "USD", "incurredAt": "2026-01-02T03:04:05Z", "reference": reference, "note": "Synthetic invoice line for accounting tests", "key": reference}
}

func expectCostFault(t *testing.T, err error, status int) {
	t.Helper()
	var fault *Fault
	if !errors.As(err, &fault) || fault.Status != status {
		t.Fatalf("expected governed error %d, got %v", status, err)
	}
}

// Catch mutable amounts, replay double-counting, missing correction provenance,
// and accidentally altering the model budget when recording an external cost.
func TestDeliveryExpenseRecordReplayVoidAndCorrection(t *testing.T) {
	s, a, project := fixture(t)
	g := goal(t, s, a, project)
	baseline, err := s.Store.Get(g.ID)
	if err != nil {
		t.Fatal(err)
	}
	in := expenseInput(g, "invoice-line-1")
	first := must(t, s, a, "expenses.record", in)
	if first.Status != "recorded" || first.GoalID != g.ID || S(first.Data, "recordedBy") != a.ID || N(first.Data, "requirementRevision") != 1 || N(first.Data, "amountCents") != 1234 {
		t.Fatal("expense lost its amount, owner or requirement binding", first)
	}
	replay := must(t, s, a, "expenses.record", in)
	if replay.ID != first.ID {
		t.Fatal("network retry created a second cost")
	}
	changed := copyM(in)
	changed["amountCents"] = 900
	_, err = s.Command(context.Background(), a, "expenses.record", changed)
	expectCostFault(t, err, 409)
	voidInput := M{"id": first.ID, "key": "void-1", "reason": "Synthetic invoice was allocated to the wrong amount"}
	voided := must(t, s, a, "expenses.void", voidInput)
	if voided.Status != "voided" || S(voided.Data, "voidedBy") != a.ID || N(voided.Data, "amountCents") != 1234 || voided.Revision != 2 {
		t.Fatal("void removed original financial provenance", voided)
	}
	if must(t, s, a, "expenses.void", voidInput).ID != first.ID {
		t.Fatal("void retry lost its identity")
	}
	voidInput["key"] = "another-void"
	_, err = s.Command(context.Background(), a, "expenses.void", voidInput)
	expectCostFault(t, err, 409)
	changed["key"] = "corrected-1"
	correction := must(t, s, a, "expenses.record", changed)
	if correction.ID == first.ID || S(correction.Data, "replacesExpenseId") != first.ID || N(correction.Data, "amountCents") != 900 {
		t.Fatal("correction overwrote history or failed to link the old record", correction)
	}
	after, _ := s.Store.Get(g.ID)
	if Hash(after) != Hash(baseline) {
		t.Fatal("external cost changed model budget, spent amount or goal state")
	}
	old := must(t, s, a, "expenses.record", in)
	if old.ID != first.ID || old.Status != "voided" {
		t.Fatal("replay of original recording resurrected a voided expense")
	}
	all, _ := s.Store.List(project.ID, "expense", g.ID)
	if len(all) != 2 {
		t.Fatal("ledger lost history or duplicated a replay", all)
	}
}

func TestDeliveryExpenseSourceIsUniqueAcrossActorsAndGoals(t *testing.T) {
	s, a, project := fixture(t)
	g := goal(t, s, a, project)
	otherGoal := goal(t, s, a, project)
	must(t, s, a, "expenses.record", expenseInput(g, "shared-line"))
	other := Actor{ID: "other-accountant", Kind: "human", Admin: true}
	in := expenseInput(otherGoal, "shared-line")
	in["key"] = "different-person-and-key"
	_, err := s.Command(context.Background(), other, "expenses.record", in)
	expectCostFault(t, err, 409)
	otherProject := must(t, s, a, "projects.create", M{"title": "Independent account"})
	independent := goal(t, s, a, otherProject)
	must(t, s, a, "expenses.record", expenseInput(independent, "shared-line"))
}

func TestDeliveryExpenseRejectsInvalidFinancialAndSourceInputs(t *testing.T) {
	s, a, project := fixture(t)
	g := goal(t, s, a, project)
	cases := []struct {
		name, field string
		value       any
	}{
		{"missing-amount", "amountCents", nil}, {"fraction", "amountCents", 1.5},
		{"string-amount", "amountCents", "1234"}, {"boolean", "amountCents", true},
		{"negative", "amountCents", -1}, {"too-large", "amountCents", 100000001},
		{"nan", "amountCents", math.NaN()}, {"infinity", "amountCents", math.Inf(1)},
		{"wrong-currency", "currency", "CNY"}, {"missing-currency", "currency", nil},
		{"unknown-category", "category", "model"}, {"no-reference", "reference", " "},
		{"no-note", "note", " "}, {"no-key", "key", ""},
		{"long-reference", "reference", strings.Repeat("a", 161)},
		{"long-note", "note", strings.Repeat("a", 2001)},
		{"bad-date", "incurredAt", "not-a-date"},
		{"future", "incurredAt", time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := expenseInput(g, tc.name)
			in[tc.field] = tc.value
			_, err := s.Command(context.Background(), a, "expenses.record", in)
			expectCostFault(t, err, 422)
		})
	}
	zero := expenseInput(g, "explicit-zero")
	zero["amountCents"] = json.Number("0")
	zero["note"] = "The synthetic invoice explicitly allocates no build charge"
	must(t, s, a, "expenses.record", zero)
	all, _ := s.Store.List(project.ID, "expense", g.ID)
	if len(all) != 1 || N(all[0].Data, "amountCents") != 0 {
		t.Fatal("invalid entries persisted, or explicit zero was lost", all)
	}
}

func TestDeliveryExpenseProofScopeAndHistoricalRevision(t *testing.T) {
	s, a, project := fixture(t)
	g := goal(t, s, a, project)
	otherGoal := goal(t, s, a, project)
	proof := resource("artifact", "Synthetic cost evidence", project.ID, g.ID, M{"type": "report", "digest": strings.Repeat("a", 64), "size": 42})
	wrongGoal := resource("artifact", "Another allocation", project.ID, otherGoal.ID, M{"type": "report", "digest": strings.Repeat("b", 64)})
	packageProof := resource("artifact", "Package is not a bill", project.ID, g.ID, M{"type": "package"})
	if err := s.Store.Update(context.Background(), func(tx *Tx) error {
		for _, r := range []Resource{proof, wrongGoal, packageProof} {
			if err := tx.Put(r); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, badID := range []string{wrongGoal.ID, packageProof.ID, "missing-proof"} {
		in := expenseInput(g, badID)
		in["proofArtifactId"] = badID
		if _, err := s.Command(context.Background(), a, "expenses.record", in); err == nil {
			t.Fatal("invalid proof accepted", badID)
		}
	}
	in := expenseInput(g, "backed-line")
	in["proofArtifactId"] = proof.ID
	entry := must(t, s, a, "expenses.record", in)
	if S(entry.Data, "proofDigest") != strings.Repeat("a", 64) {
		t.Fatal("expense did not bind the exact proof bytes", entry)
	}
	must(t, s, a, "goals.revise", M{"id": g.ID, "reason": "New product iteration"})
	after, _ := s.Store.Get(entry.ID)
	if after.Status != "recorded" || N(after.Data, "requirementRevision") != 1 {
		t.Fatal("scope revision erased historical incurred cost", after)
	}
}

func TestDeliveryExpenseUsesCurrentHumanOwnerAuthority(t *testing.T) {
	s, admin, project, owner, member := membershipFixture(t)
	g := goal(t, s, admin, project)
	in := expenseInput(g, "owner-only")
	entry := must(t, s, owner, "expenses.record", in)
	for _, actor := range []Actor{
		{ID: "agent-admin", Kind: "agent", Admin: true},
		{ID: "viewer", Kind: "human", ProjectID: project.ID, Roles: []string{"viewer"}},
		{ID: "developer", Kind: "human", ProjectID: project.ID, Roles: []string{"developer"}},
	} {
		if _, err := s.Command(context.Background(), actor, "expenses.record", expenseInput(g, actor.ID)); err == nil {
			t.Fatal("non-owner or non-human recorded a cost", actor)
		}
	}
	must(t, s, admin, "members.grant", M{"projectId": project.ID, "identityId": owner.ID, "roles": []string{"viewer"}, "expectedRevision": member.Revision})
	owner.Roles = []string{"owner"}
	_, err := s.Command(context.Background(), owner, "expenses.record", in)
	expectCostFault(t, err, 403)
	_, err = s.Command(context.Background(), owner, "expenses.void", M{"id": entry.ID, "key": "stale-owner", "reason": "Cannot use cached owner role"})
	expectCostFault(t, err, 403)
}

func TestDeliveryExpenseAuditFailureRollsBackRecordAndIdempotency(t *testing.T) {
	s, a, project := fixture(t)
	g := goal(t, s, a, project)
	if _, err := s.Store.DB.Exec(`CREATE TRIGGER fail_expense_audit BEFORE INSERT ON audit BEGIN SELECT RAISE(ABORT, 'synthetic audit failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Command(context.Background(), a, "expenses.record", expenseInput(g, "atomic")); err == nil {
		t.Fatal("expense acknowledged without durable audit")
	}
	all, _ := s.Store.List(project.ID, "expense", g.ID)
	var remembered int
	if err := s.Store.DB.QueryRow(`SELECT COUNT(*) FROM idempotency`).Scan(&remembered); err != nil || len(all) != 0 || remembered != 0 {
		t.Fatal("failed audit left a cost or replay key", err, all, remembered)
	}
}

func TestDeliveryExpenseRequiresHumanEvenWithOwnerRole(t *testing.T) {
	s, a, project := fixture(t)
	g := goal(t, s, a, project)
	for _, kind := range []string{"agent", "worker"} {
		actor := Actor{ID: "delegated-" + kind, Kind: kind, ProjectID: project.ID, Roles: []string{"owner"}}
		if err := s.CanAccess(context.Background(), actor, project.ID, "owner"); err != nil {
			t.Fatal("test did not reach the human decision boundary", err)
		}
		_, err := s.Command(context.Background(), actor, "expenses.record", expenseInput(g, kind))
		expectCostFault(t, err, 403)
	}
}

func TestModelReconciliationRejectsNonIntegerOrMissingAmount(t *testing.T) {
	for _, amount := range []any{nil, "0", true, 0.5, json.Number("1.5"), -1, 100000001, math.NaN(), math.Inf(1)} {
		t.Run(strings.ReplaceAll(strings.TrimSpace(string(mustJSONCostName(amount))), "/", "_"), func(t *testing.T) {
			s, a, project := fixture(t)
			g := goal(t, s, a, project)
			charge := resource("charge", "Uncertain synthetic model call", project.ID, g.ID, M{"chargedCents": 100, "reservedCents": 100})
			charge.Status = "uncertain"
			g.Data["spentCents"] = 100
			if err := s.Store.Update(context.Background(), func(tx *Tx) error {
				if err := tx.Put(g); err != nil {
					return err
				}
				return tx.Put(charge)
			}); err != nil {
				t.Fatal(err)
			}
			_, err := s.Command(context.Background(), a, "charges.reconcile", M{"id": charge.ID, "chargedCents": amount, "reason": "Synthetic provider statement"})
			expectCostFault(t, err, 422)
			after, _ := s.Store.Get(charge.ID)
			current, _ := s.Store.Get(g.ID)
			if after.Status != "uncertain" || N(current.Data, "spentCents") != 100 {
				t.Fatal("invalid reconciliation changed accounting")
			}
		})
	}
}

func mustJSONCostName(value any) []byte {
	body, err := json.Marshal(value)
	if err != nil {
		return []byte("non-finite")
	}
	return body
}

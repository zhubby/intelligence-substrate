package platform

import (
	"encoding/json"
	"math"
	"time"
)

var expenseCategories = []string{"build", "device", "storage", "channel", "labor", "other"}

// A missing amount is unknown, not an explicitly recorded zero. Restrict both
// the type and precision before money can reach any ledger or budget field.
func costCents(value any) (int64, error) {
	var amount float64
	switch n := value.(type) {
	case int:
		amount = float64(n)
	case int64:
		amount = float64(n)
	case float64:
		amount = n
	case json.Number:
		var err error
		amount, err = n.Float64()
		if err != nil {
			return 0, invalid("费用必须为 0–100000000 的整数美分")
		}
	default:
		return 0, invalid("费用必须为显式的整数美分")
	}
	if math.IsNaN(amount) || math.IsInf(amount, 0) || amount != math.Trunc(amount) || amount < 0 || amount > 100000000 {
		return 0, invalid("费用必须为 0–100000000 的整数美分")
	}
	return int64(amount), nil
}

func (s *Service) expenseCommand(t *Tx, a Actor, cmd string, in M) (Resource, error) {
	if cmd == "expenses.void" {
		return s.voidExpense(t, a, in)
	}
	g, err := s.scoped(t, a, S(in, "goalId"), "goal", "owner")
	if err != nil {
		return g, err
	}
	if err = human(a); err != nil {
		return Resource{}, err
	}
	for _, field := range []struct {
		key, title string
		max        int
	}{
		{"title", "费用名称", 200}, {"reference", "费用来源标识", 160},
		{"note", "费用登记依据", 2000}, {"key", "幂等键", 160},
	} {
		if err = validateText(S(in, field.key), field.title, field.max); err != nil {
			return Resource{}, err
		}
	}
	amount, err := costCents(in["amountCents"])
	if err != nil {
		return Resource{}, err
	}
	if S(in, "currency") != "USD" || !Has(expenseCategories, S(in, "category")) {
		return Resource{}, invalid("费用需要 USD 币种和受支持的费用类别")
	}
	incurred, err := time.Parse(time.RFC3339Nano, S(in, "incurredAt"))
	if err != nil || incurred.After(time.Now().Add(5*time.Minute)) {
		return Resource{}, invalid("费用发生时间无效或超出允许的未来时钟偏差")
	}
	scope := "expense-record:" + g.ProjectID + ":" + a.ID
	if previous, ok, err := t.Once(scope, S(in, "key"), in); err != nil || ok {
		return previous, err
	}
	all, err := t.List(g.ProjectID, "expense", "")
	if err != nil {
		return Resource{}, err
	}
	replaces := ""
	var latest time.Time
	for _, old := range all {
		if S(old.Data, "reference") != S(in, "reference") {
			continue
		}
		if old.Status != "voided" {
			return Resource{}, conflict("同一项目的费用来源已经登记，请核对原记录")
		}
		at, err := time.Parse(time.RFC3339Nano, S(old.Data, "voidedAt"))
		if err != nil {
			return Resource{}, conflict("费用来源的作废历史不完整，需要核对原记录")
		}
		if replaces == "" || at.After(latest) || (at.Equal(latest) && old.ID > replaces) {
			replaces, latest = old.ID, at
		}
	}
	data := M{"category": S(in, "category"), "amountCents": amount, "currency": "USD", "incurredAt": incurred.UTC().Format(time.RFC3339Nano), "reference": S(in, "reference"), "note": S(in, "note"), "requirementRevision": g.Revision, "recordedBy": a.ID, "recordedName": a.Name, "basis": "human-recorded"}
	if replaces != "" {
		data["replacesExpenseId"] = replaces
	}
	if value, exists := in["proofArtifactId"]; exists {
		if _, valid := value.(string); !valid {
			return Resource{}, invalid("依据附件必须是报告制品 ID")
		}
	}
	if id := S(in, "proofArtifactId"); id != "" {
		proof, err := s.scoped(t, a, id, "artifact", "owner")
		if err != nil {
			return Resource{}, err
		}
		if proof.GoalID != g.ID || proof.ProjectID != g.ProjectID || S(proof.Data, "type") != "report" || len(S(proof.Data, "digest")) != 64 {
			return Resource{}, invalid("依据附件必须为同项目和目标的报告制品")
		}
		data["proofArtifactId"], data["proofDigest"] = proof.ID, S(proof.Data, "digest")
	}
	expense := resource("expense", S(in, "title"), g.ProjectID, g.ID, data)
	expense.Status = "recorded"
	if err = t.Put(expense); err != nil {
		return expense, err
	}
	return expense, t.Remember(scope, S(in, "key"), in, expense)
}

func (s *Service) voidExpense(t *Tx, a Actor, in M) (Resource, error) {
	expense, err := s.scoped(t, a, S(in, "id"), "expense", "owner")
	if err != nil {
		return expense, err
	}
	if err = human(a); err != nil {
		return expense, err
	}
	if err = validateText(S(in, "key"), "幂等键", 160); err != nil {
		return expense, err
	}
	if err = validateText(S(in, "reason"), "作废原因", 2000); err != nil {
		return expense, err
	}
	scope := "expense-void:" + expense.ID + ":" + a.ID
	if previous, ok, err := t.Once(scope, S(in, "key"), in); err != nil || ok {
		return previous, err
	}
	if expense.Status != "recorded" {
		return expense, conflict("费用记录已作废，请刷新费用状态")
	}
	expense.Status = "voided"
	expense.Revision++
	expense.Data["voidedBy"], expense.Data["voidedName"] = a.ID, a.Name
	expense.Data["voidedAt"], expense.Data["voidReason"] = now(), S(in, "reason")
	if err = t.Put(expense); err != nil {
		return expense, err
	}
	return expense, t.Remember(scope, S(in, "key"), in, expense)
}

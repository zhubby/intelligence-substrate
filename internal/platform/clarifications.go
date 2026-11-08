package platform

import (
	"encoding/json"
	"math"
	"strings"
	"time"
)

func clarificationRevision(g Resource, value any) error {
	switch value.(type) {
	case int, int64, float64, json.Number:
	default:
		return invalid("需要明确的需求版本 expectedRequirementRevision")
	}
	n := N(M{"revision": value}, "revision")
	if math.IsNaN(n) || math.IsInf(n, 0) || n < 1 || n != math.Trunc(n) {
		return invalid("需求版本必须为正整数")
	}
	if n != float64(g.Revision) {
		return conflict("需求版本已变化，请读取当前基线后处理澄清")
	}
	return nil
}

func clarificationDraft(g Resource, expected any) error {
	if err := clarificationRevision(g, expected); err != nil {
		return err
	}
	if N(g.Data, "approvedRevision") == float64(g.Revision) || g.Status != "awaiting_baseline" {
		return conflict("当前需求已批准，新增或更正澄清须先变更需求基线")
	}
	return nil
}

func clarificationFields(in M, allowed ...string) error {
	for key := range in {
		if !Has(allowed, key) {
			return invalid("澄清包含不支持的字段：" + key)
		}
	}
	return nil
}

func normalizedQuestion(question string) string {
	return strings.ToLower(strings.Join(strings.Fields(question), " "))
}

func validateClarificationQuestions(value any) ([]any, error) {
	questions, ok := value.([]any)
	if !ok || len(questions) < 1 || len(questions) > 10 {
		return nil, invalid("每次需要 1–10 项澄清问题")
	}
	out := []any{}
	seen := map[string]bool{}
	for _, item := range questions {
		question, ok := item.(map[string]any)
		if !ok {
			return nil, invalid("澄清问题必须为结构化对象")
		}
		if err := clarificationFields(question, "question", "context"); err != nil {
			return nil, err
		}
		if err := validateText(S(question, "question"), "澄清问题", 2000); err != nil {
			return nil, err
		}
		if context, exists := question["context"]; exists {
			if text, ok := context.(string); !ok || len(text) > 2000 {
				return nil, invalid("问题背景须为不超过 2000 字节的文本")
			}
		}
		normalized := normalizedQuestion(S(question, "question"))
		if seen[normalized] {
			return nil, invalid("同一批澄清问题不能重复")
		}
		seen[normalized] = true
		out = append(out, M{"id": NewID("question"), "question": S(question, "question"), "context": S(question, "context"), "status": "open"})
	}
	return out, nil
}

// The caller owns a current, unapproved goal and is already authorized.
func createClarificationBatch(t *Tx, a Actor, g Resource, value any, origin, generationID string) (Resource, error) {
	questions, err := validateClarificationQuestions(value)
	if err != nil {
		return Resource{}, err
	}
	batches, err := t.List(g.ProjectID, "clarification", g.ID)
	if err != nil {
		return Resource{}, err
	}
	count := len(questions)
	seen := map[string]bool{}
	for _, batch := range batches {
		if revision(batch) != g.Revision {
			continue
		}
		for _, item := range Items(batch.Data["questions"]) {
			count++
			seen[normalizedQuestion(S(Obj(item), "question"))] = true
		}
	}
	if count > 100 {
		return Resource{}, conflict("当前需求版本已达到 100 项澄清问题限制")
	}
	for _, item := range questions {
		if seen[normalizedQuestion(S(Obj(item), "question"))] {
			return Resource{}, conflict("当前需求版本已包含相同澄清问题")
		}
	}
	batch := resource("clarification", "需求澄清", g.ProjectID, g.ID, M{"requirementRevision": g.Revision, "questions": questions, "origin": origin, "proposedBy": a.ID, "proposedName": a.Name, "proposedKind": a.Kind})
	batch.Status = "open"
	if generationID != "" {
		batch.Data["generationId"] = generationID
	}
	return batch, t.Put(batch)
}

func (s *Service) clarificationCommand(t *Tx, a Actor, cmd string, in M) (Resource, error) {
	if a.Kind == "worker" {
		return Resource{}, denied()
	}
	if cmd == "clarifications.answer" {
		return s.answerClarification(t, a, in)
	}
	g, err := s.scoped(t, a, S(in, "goalId"), "goal", "product", "architect", "developer", "analysis", "design")
	if err != nil {
		return g, err
	}
	if err = clarificationFields(in, "goalId", "expectedRequirementRevision", "key", "questions"); err != nil {
		return Resource{}, err
	}
	if err = validateText(S(in, "key"), "幂等键", 160); err != nil {
		return Resource{}, err
	}
	scope := "clarifications-propose:" + g.ID + ":" + a.ID
	if previous, ok, err := t.Once(scope, S(in, "key"), in); err != nil || ok {
		return previous, err
	}
	if err = clarificationDraft(g, in["expectedRequirementRevision"]); err != nil {
		return Resource{}, err
	}
	batch, err := createClarificationBatch(t, a, g, in["questions"], "manual", "")
	if err != nil {
		return batch, err
	}
	return batch, t.Remember(scope, S(in, "key"), in, batch)
}

func (s *Service) answerClarification(t *Tx, a Actor, in M) (Resource, error) {
	batch, err := s.scoped(t, a, S(in, "id"), "clarification", "product")
	if err != nil {
		return batch, err
	}
	if err = human(a); err != nil {
		return Resource{}, err
	}
	if err = clarificationFields(in, "id", "questionId", "expectedRequirementRevision", "key", "answer"); err != nil {
		return Resource{}, err
	}
	for _, field := range []struct {
		key, title string
		max        int
	}{{"key", "幂等键", 160}, {"questionId", "问题 ID", 96}, {"answer", "需求答复", 12000}} {
		if err = validateText(S(in, field.key), field.title, field.max); err != nil {
			return Resource{}, err
		}
	}
	scope := "clarifications-answer:" + batch.ID + ":" + a.ID + ":" + S(in, "questionId")
	if previous, ok, err := t.Once(scope, S(in, "key"), in); err != nil || ok {
		return previous, err
	}
	g, err := t.Get(batch.GoalID)
	if err != nil {
		return Resource{}, err
	}
	if err = clarificationDraft(g, in["expectedRequirementRevision"]); err != nil {
		return Resource{}, err
	}
	if revision(batch) != g.Revision {
		return Resource{}, conflict("旧需求版本的问题只能查看，不能继续回答")
	}
	questions := Items(batch.Data["questions"])
	found, open := false, 0
	for _, item := range questions {
		q := Obj(item)
		if S(q, "id") == S(in, "questionId") {
			found = true
			if S(q, "status") != "open" {
				return Resource{}, conflict("该问题已有确认答复；更正须创建新需求版本")
			}
			q["status"], q["answer"] = "answered", S(in, "answer")
			q["answeredBy"], q["answeredName"], q["answeredAt"] = a.ID, a.Name, now()
		}
		if S(q, "status") != "answered" {
			open++
		}
	}
	if !found {
		return Resource{}, missing()
	}
	batch.Revision++
	if open == 0 {
		batch.Status = "answered"
	}
	if err = t.Put(batch); err != nil {
		return batch, err
	}
	return batch, t.Remember(scope, S(in, "key"), in, batch)
}

// Exact question/answer records travel with the approval, beyond the existing
// criteria digest. Draft or malformed decisions fail closed.
func clarificationSnapshot(t *Tx, g Resource) ([]Resource, error) {
	batches, err := t.List(g.ProjectID, "clarification", g.ID)
	if err != nil {
		return nil, err
	}
	current := []Resource{}
	for _, batch := range batches {
		if revision(batch) != g.Revision {
			continue
		}
		questions := Items(batch.Data["questions"])
		if batch.Status != "answered" || len(questions) == 0 {
			return nil, conflict("仍有未确认的需求澄清，请先完成人工答复")
		}
		for _, item := range questions {
			q := Obj(item)
			_, err := time.Parse(time.RFC3339Nano, S(q, "answeredAt"))
			if S(q, "status") != "answered" || S(q, "answer") == "" || S(q, "answeredBy") == "" || err != nil {
				return nil, conflict("需求答复记录不完整，请核对后再批准")
			}
		}
		current = append(current, batch)
	}
	return current, nil
}

// ApprovedClarifications selects the frozen business decisions for this exact
// baseline. Live ledger rows are deliberately not execution authority. Legacy
// approvals without this optional snapshot continue to have no clarification.
func ApprovedClarifications(goal Resource, resources []Resource) ([]any, error) {
	result := []any{}
	digest := ""
	for _, approval := range resources {
		if approval.Kind != "approval" || approval.ProjectID != goal.ProjectID || approval.GoalID != goal.ID || S(approval.Data, "type") != "baseline" || N(approval.Data, "requirementRevision") != float64(goal.Revision) || approval.Status == "stale" || S(approval.Data, "decision") != "approved" {
			continue
		}
		records, exists := approval.Data["clarifications"]
		if !exists {
			if _, hasDigest := approval.Data["clarificationsDigest"]; hasDigest {
				return nil, &Fault{Code: "invalid_baseline", Message: "需求澄清批准快照缺失，需要核对基线", Status: 409}
			}
			continue
		}
		invalidSnapshot := func() ([]any, error) {
			return nil, &Fault{Code: "invalid_baseline", Message: "需求澄清批准记录不完整或摘要不一致，需要核对基线", Status: 409}
		}
		items, ok := records.([]any)
		if !ok || Hash(records) != S(approval.Data, "clarificationsDigest") || (digest != "" && digest != Hash(records)) {
			return invalidSnapshot()
		}
		for _, record := range items {
			batch := Obj(record)
			data := Obj(batch["data"])
			if S(batch, "kind") != "clarification" || S(batch, "projectId") != goal.ProjectID || S(batch, "goalId") != goal.ID || N(data, "requirementRevision") != float64(goal.Revision) || S(batch, "status") != "answered" || len(Items(data["questions"])) == 0 {
				return invalidSnapshot()
			}
			for _, value := range Items(data["questions"]) {
				question := Obj(value)
				_, err := time.Parse(time.RFC3339Nano, S(question, "answeredAt"))
				if S(question, "status") != "answered" || S(question, "answer") == "" || S(question, "answeredBy") == "" || err != nil {
					return invalidSnapshot()
				}
			}
		}
		result, digest = items, Hash(records)
	}
	return result, nil
}

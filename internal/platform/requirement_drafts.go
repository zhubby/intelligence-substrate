package platform

// Requirement content has no policy, permission, answer or evidence fields.
// Every proposed or human-edited criterion receives a new server-owned ID.
func validateRequirementContent(value any) (M, error) {
	in, ok := value.(map[string]any)
	if !ok {
		return nil, invalid("需求稿必须是结构化内容")
	}
	if err := clarificationFields(in, "outcome", "brief", "criteria"); err != nil {
		return nil, err
	}
	for _, field := range []struct {
		key, title string
		max        int
	}{{"outcome", "业务结果", 12000}, {"brief", "需求说明", 16000}} {
		if err := validateText(S(in, field.key), field.title, field.max); err != nil {
			return nil, err
		}
	}
	items, ok := in["criteria"].([]any)
	if !ok || len(items) == 0 || len(items) > 100 {
		return nil, invalid("需求稿需要 1–100 项验收标准")
	}
	seen := map[string]bool{}
	for _, item := range items {
		criterion, ok := item.(map[string]any)
		if !ok {
			return nil, invalid("验收标准必须为结构化对象")
		}
		if err := clarificationFields(criterion, "text", "method"); err != nil {
			return nil, err
		}
		if _, ok := criterion["method"].(string); !ok || !Has([]string{"automated", "manual", "device", "simulator"}, S(criterion, "method")) {
			return nil, invalid("需求稿须明确有效的验证方式")
		}
		normalized := normalizedQuestion(S(criterion, "text"))
		if seen[normalized] {
			return nil, invalid("需求稿验收标准不能重复")
		}
		seen[normalized] = true
	}
	criteria, err := validateCriteria(items)
	if err != nil {
		return nil, err
	}
	return M{"outcome": S(in, "outcome"), "brief": S(in, "brief"), "criteria": criteria}, nil
}

// Canonical JSON maps ensure snapshots hash identically before and after SQL
// persistence/backup. Spend is excluded because inference changes it itself.
func requirementDraftContext(t *Tx, g Resource) (M, error) {
	clarifications, err := clarificationSnapshot(t, g)
	if err != nil {
		return nil, err
	}
	items, err := t.List(g.ProjectID, "knowledge", "")
	if err != nil {
		return nil, err
	}
	knowledge := []any{}
	for _, item := range items {
		if item.Status != "superseded" {
			knowledge = append(knowledge, M{"id": item.ID, "revision": item.Revision, "title": item.Title, "content": item.Data["content"], "source": item.Data["source"]})
		}
	}
	return copyM(M{"outcome": g.Data["outcome"], "criteria": g.Data["criteria"], "brief": g.Data["requirementBrief"], "targets": g.Data["targets"], "budgetCents": g.Data["budgetCents"], "sources": g.Data["sources"], "clarifications": clarifications, "knowledge": knowledge}), nil
}

func createRequirementProposal(t *Tx, a Actor, g Resource, value any, input M, origin, generationID string) (Resource, error) {
	content, err := validateRequirementContent(value)
	if err != nil {
		return Resource{}, err
	}
	drafts, err := t.List(g.ProjectID, "requirement_proposal", g.ID)
	if err != nil {
		return Resource{}, err
	}
	if len(drafts) >= 200 {
		return Resource{}, conflict("当前目标已达到 200 份需求稿限制")
	}
	draft := resource("requirement_proposal", "待审阅需求稿", g.ProjectID, g.ID, M{"requirementRevision": g.Revision, "content": content, "contentDigest": Hash(content), "input": input, "inputDigest": Hash(input), "origin": origin, "proposedBy": a.ID, "proposedName": a.Name, "proposedKind": a.Kind})
	draft.Status = "proposed"
	if generationID != "" {
		draft.Data["generationId"] = generationID
	}
	return draft, t.Put(draft)
}

func (s *Service) requirementDraftCommand(t *Tx, a Actor, cmd string, in M) (Resource, error) {
	if a.Kind == "worker" {
		return Resource{}, denied()
	}
	if cmd == "requirements.apply" {
		return s.applyRequirementDraft(t, a, in)
	}
	g, err := s.scoped(t, a, S(in, "goalId"), "goal", "product", "architect", "analysis", "design", "developer")
	if err != nil {
		return g, err
	}
	if err = clarificationFields(in, "goalId", "expectedRequirementRevision", "key", "content"); err != nil {
		return Resource{}, err
	}
	if err = validateText(S(in, "key"), "幂等键", 160); err != nil {
		return Resource{}, err
	}
	scope := "requirements-propose:" + g.ID + ":" + a.ID
	if previous, ok, err := t.Once(scope, S(in, "key"), in); err != nil || ok {
		return previous, err
	}
	if err = clarificationDraft(g, in["expectedRequirementRevision"]); err != nil {
		return Resource{}, err
	}
	input, err := requirementDraftContext(t, g)
	if err != nil {
		return Resource{}, err
	}
	draft, err := createRequirementProposal(t, a, g, in["content"], input, "manual", "")
	if err != nil {
		return draft, err
	}
	return draft, t.Remember(scope, S(in, "key"), in, draft)
}

func (s *Service) applyRequirementDraft(t *Tx, a Actor, in M) (Resource, error) {
	draft, err := s.scoped(t, a, S(in, "id"), "requirement_proposal", "product")
	if err != nil {
		return draft, err
	}
	if err = human(a); err != nil {
		return Resource{}, err
	}
	if err = clarificationFields(in, "id", "expectedRequirementRevision", "key", "reason", "content"); err != nil {
		return Resource{}, err
	}
	if err = validateText(S(in, "key"), "幂等键", 160); err != nil {
		return Resource{}, err
	}
	if err = validateText(S(in, "reason"), "采纳原因", 2000); err != nil {
		return Resource{}, err
	}
	scope := "requirements-apply:" + draft.ID + ":" + a.ID
	if previous, ok, err := t.Once(scope, S(in, "key"), in); err != nil || ok {
		return previous, err
	}
	g, err := t.Get(draft.GoalID)
	if err != nil {
		return Resource{}, err
	}
	if err = clarificationDraft(g, in["expectedRequirementRevision"]); err != nil {
		return Resource{}, err
	}
	if draft.Status != "proposed" || revision(draft) != g.Revision {
		return Resource{}, conflict("需求稿已处理或属于旧需求版本")
	}
	input, err := requirementDraftContext(t, g)
	if err != nil {
		return Resource{}, err
	}
	if Hash(input) != S(draft.Data, "inputDigest") || Hash(draft.Data["input"]) != Hash(input) || Hash(draft.Data["content"]) != S(draft.Data, "contentDigest") {
		return Resource{}, conflict("需求稿上下文或摘要已变化，请重新整理并审阅")
	}
	content, err := validateRequirementContent(in["content"])
	if err != nil {
		return Resource{}, err
	}
	// Only this trusted call can bind an adopted document to the new version.
	current, err := s.goalCommand(t, a, "goals.revise", M{"id": g.ID, "reason": S(in, "reason"), "outcome": content["outcome"], "criteria": content["criteria"]}, M{"requirementBrief": content["brief"], "requirementProposalId": draft.ID, "requirementContentDigest": Hash(content)})
	if err != nil {
		return Resource{}, err
	}
	for _, value := range Items(input["clarifications"]) {
		original := Obj(value)
		data := copyM(Obj(original["data"]))
		data["requirementRevision"], data["sourceBatchId"], data["sourceRequirementRevision"], data["adoptedBy"], data["proposalId"] = current.Revision, S(original, "id"), g.Revision, a.ID, draft.ID
		carried := resource("clarification", "采纳的需求决定", g.ProjectID, g.ID, data)
		carried.Status = "answered"
		if err = t.Put(carried); err != nil {
			return Resource{}, err
		}
	}
	draft.Status, draft.Revision = "applied", draft.Revision+1
	draft.Data["appliedBy"], draft.Data["appliedName"], draft.Data["appliedAt"], draft.Data["appliedContent"], draft.Data["appliedDigest"], draft.Data["resultRevision"], draft.Data["reason"] = a.ID, a.Name, now(), content, Hash(content), current.Revision, S(in, "reason")
	if err = t.Put(draft); err != nil {
		return Resource{}, err
	}
	return current, t.Remember(scope, S(in, "key"), in, current)
}

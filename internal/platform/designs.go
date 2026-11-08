package platform

// Designs are immutable proposals. The plan approval, not this live row, is
// authority to implement a particular design.
func designFields(in M, allowed ...string) error {
	for key := range in {
		if !Has(allowed, key) {
			return invalid("设计包含不支持的字段：" + key)
		}
	}
	return nil
}

func designCriteria(g Resource, value any) ([]any, error) {
	items, ok := value.([]any)
	if !ok || len(items) < 1 || len(items) > 100 {
		return nil, invalid("设计需要关联 1–100 项当前验收标准")
	}
	known, seen := map[string]bool{}, map[string]bool{}
	for _, item := range Items(g.Data["criteria"]) {
		known[S(Obj(item), "id")] = true
	}
	out := []any{}
	for _, item := range items {
		id, ok := item.(string)
		if !ok || !known[id] || seen[id] {
			return nil, invalid("设计验收标准必须唯一且属于当前需求")
		}
		seen[id] = true
		out = append(out, id)
	}
	return out, nil
}

func prototypeReference(artifact Resource) M {
	return M{"artifactId": artifact.ID, "digest": artifact.Data["digest"], "size": artifact.Data["size"], "repositoryId": artifact.Data["repositoryId"], "commit": artifact.Data["commit"], "target": artifact.Data["target"], "name": artifact.Title}
}

func (s *Service) designPrototypes(t *Tx, g Resource, value any) ([]any, error) {
	if value == nil {
		value = []any{}
	}
	ids, ok := value.([]any)
	if !ok || len(ids) > 6 {
		return nil, invalid("设计最多关联 6 个独立 HTML 原型")
	}
	out, seen := []any{}, map[string]bool{}
	for _, item := range ids {
		id, ok := item.(string)
		if !ok || id == "" || seen[id] {
			return nil, invalid("原型 ID 必须为不重复的非空字符串")
		}
		seen[id] = true
		artifact, err := t.Get(id)
		if err != nil {
			return nil, err
		}
		if artifact.Kind != "artifact" || artifact.ProjectID != g.ProjectID || artifact.GoalID != g.ID {
			return nil, denied()
		}
		if revision(artifact) != g.Revision || S(artifact.Data, "type") != "prototype" || N(artifact.Data, "size") < 1 || N(artifact.Data, "size") > 2<<20 || !Has(Strings(g.Data["targets"]), S(artifact.Data, "target")) {
			return nil, conflict("原型必须属于当前需求版本且为不超过 2 MiB 的独立 HTML")
		}
		if err = s.VerifyArtifact(artifact); err != nil {
			return nil, err
		}
		out = append(out, prototypeReference(artifact))
	}
	return out, nil
}

func (s *Service) designCommand(t *Tx, a Actor, in M) (Resource, error) {
	g, err := s.scoped(t, a, S(in, "goalId"), "goal", "product", "architect", "developer", "design")
	if err != nil {
		return Resource{}, err
	}
	if !Has([]string{"human", "agent", "worker"}, a.Kind) {
		return Resource{}, denied()
	}
	if err = designFields(in, "goalId", "expectedRequirementRevision", "key", "title", "summary", "decisions", "criteria", "prototypeArtifactIds", "reason", "runId", "leaseToken"); err != nil {
		return Resource{}, err
	}
	if err = noSecrets(in); err != nil {
		return Resource{}, err
	}
	for _, field := range []struct {
		key string
		max int
	}{{"key", 160}, {"title", 200}, {"summary", 12000}, {"decisions", 16000}, {"reason", 2000}} {
		if err = validateText(S(in, field.key), field.key, field.max); err != nil {
			return Resource{}, err
		}
	}
	if a.Kind == "worker" {
		run, err := t.Get(S(in, "runId"))
		if err != nil {
			return Resource{}, err
		}
		task, err := t.Get(S(run.Data, "taskId"))
		if err != nil || S(task.Data, "kind") != "design" || run.GoalID != g.ID {
			return Resource{}, denied()
		}
	}
	scope := "designs-propose:" + g.ID + ":" + a.ID
	if previous, exists, err := t.Once(scope, S(in, "key"), in); err != nil || exists {
		return previous, err
	}
	if err = clarificationRevision(g, in["expectedRequirementRevision"]); err != nil {
		return Resource{}, err
	}
	if err = approved(t, g); err != nil {
		return Resource{}, err
	}
	criteria, err := designCriteria(g, in["criteria"])
	if err != nil {
		return Resource{}, err
	}
	prototypes, err := s.designPrototypes(t, g, in["prototypeArtifactIds"])
	if err != nil {
		return Resource{}, err
	}
	previous, err := t.List(g.ProjectID, "design", g.ID)
	if err != nil {
		return Resource{}, err
	}
	if len(previous) >= 200 {
		return Resource{}, conflict("当前目标已达到 200 个设计版本限制")
	}
	design := resource("design", S(in, "title"), g.ProjectID, g.ID, M{"requirementRevision": g.Revision, "summary": S(in, "summary"), "decisions": S(in, "decisions"), "criteria": criteria, "prototypes": prototypes, "reason": S(in, "reason"), "authorId": a.ID, "authorName": a.Name, "authorKind": a.Kind})
	design.Status, design.Revision = "proposed", len(previous)+1
	if a.Kind == "worker" {
		design.Data["runId"] = S(in, "runId")
	}
	// The title is business content too, so it must be covered by the binding.
	design.Data["title"] = design.Title
	design.Data["binding"] = Hash(design.Data)
	if err = t.Put(design); err != nil {
		return design, err
	}
	return design, t.Remember(scope, S(in, "key"), in, design)
}

func designSnapshot(design Resource) M {
	return copyM(M{"id": design.ID, "kind": design.Kind, "projectId": design.ProjectID, "goalId": design.GoalID, "title": design.Title, "revision": design.Revision, "data": design.Data})
}

func validDesignSnapshot(g Resource, snapshot M) error {
	data := Obj(snapshot["data"])
	bound := copyM(data)
	delete(bound, "binding")
	if S(snapshot, "id") == "" || S(snapshot, "kind") != "design" || S(snapshot, "projectId") != g.ProjectID || S(snapshot, "goalId") != g.ID || N(snapshot, "revision") < 1 || N(data, "requirementRevision") != float64(g.Revision) || S(snapshot, "title") == "" || S(snapshot, "title") != S(data, "title") || Hash(bound) != S(data, "binding") {
		return conflict("设计批准快照不完整或摘要不一致，需要核对")
	}
	if _, err := designCriteria(g, data["criteria"]); err != nil {
		return conflict("设计验收标准与当前需求不一致")
	}
	prototypes, ok := data["prototypes"].([]any)
	if !ok || len(prototypes) > 6 || S(data, "summary") == "" || S(data, "decisions") == "" || S(data, "authorId") == "" {
		return conflict("设计内容或原型快照不完整")
	}
	return nil
}

// ApprovedDesign reads the frozen plan approval. A mutable proposal row can
// never replace the design a human approved for this exact plan and baseline.
func ApprovedDesign(g Resource, planID string, resources []Resource) (M, error) {
	if planID == "" {
		return nil, nil
	}
	var plan Resource
	for _, r := range resources {
		if r.ID == planID && r.Kind == "plan" && r.ProjectID == g.ProjectID && r.GoalID == g.ID {
			plan = r
		}
	}
	if plan.ID == "" || plan.Status != "active" || revision(plan) != g.Revision || S(g.Data, "activePlanId") != plan.ID {
		return nil, conflict("设计上下文需要当前已批准方案")
	}
	design, exists := plan.Data["design"]
	if !exists {
		for _, r := range resources {
			if r.Kind == "approval" && r.ProjectID == g.ProjectID && r.GoalID == g.ID && S(r.Data, "type") == "plan" && S(r.Data, "planId") == planID && revision(r) == g.Revision && r.Status != "stale" {
				if _, hasDesign := r.Data["design"]; hasDesign || S(r.Data, "designDigest") != "" {
					return nil, conflict("方案缺少人工已批准的设计快照，需要核对")
				}
			}
		}
		return nil, nil // Plans created before design support remain valid.
	}
	snapshot := Obj(design)
	if err := validDesignSnapshot(g, snapshot); err != nil {
		return nil, err
	}
	matches := 0
	for _, r := range resources {
		if r.Kind != "approval" || r.ProjectID != g.ProjectID || r.GoalID != g.ID || S(r.Data, "type") != "plan" || S(r.Data, "planId") != planID || revision(r) != g.Revision || r.Status == "stale" {
			continue
		}
		if S(r.Data, "binding") != S(plan.Data, "binding") || S(r.Data, "designDigest") != Hash(snapshot) || Hash(r.Data["design"]) != Hash(snapshot) || S(r.Data, "actorId") == "" {
			return nil, conflict("设计与人工方案批准记录不一致，需要核对")
		}
		matches++
	}
	if matches != 1 {
		return nil, conflict("缺少唯一有效的人工设计批准记录")
	}
	return copyM(snapshot), nil
}

func planDesign(t *Tx, g Resource, in M) (M, error) {
	if id := S(in, "designId"); id != "" {
		design, err := t.Get(id)
		if err != nil {
			return nil, err
		}
		if design.Kind != "design" || design.ProjectID != g.ProjectID || design.GoalID != g.ID {
			return nil, denied()
		}
		if S(in, "expectedDesignBinding") == "" {
			return nil, invalid("选择设计需要 expectedDesignBinding")
		}
		snapshot := designSnapshot(design)
		if design.Status != "proposed" || S(in, "expectedDesignBinding") != S(design.Data, "binding") {
			return nil, conflict("设计版本或摘要已变化，请重新审阅")
		}
		return snapshot, validDesignSnapshot(g, snapshot)
	}
	if S(in, "expectedDesignBinding") != "" {
		return nil, invalid("设计摘要需要明确的 designId")
	}
	resources, err := t.List(g.ProjectID, "", g.ID)
	if err != nil {
		return nil, err
	}
	for _, r := range resources {
		if r.ID == S(g.Data, "activePlanId") && r.Kind == "plan" && revision(r) != g.Revision {
			return nil, nil // A revised baseline never inherits the old design.
		}
	}
	return ApprovedDesign(g, S(g.Data, "activePlanId"), resources)
}

func (s *Service) validatePlanDesign(t *Tx, g Resource, value any) error {
	if value == nil {
		return nil
	}
	snapshot := Obj(value)
	if err := validDesignSnapshot(g, snapshot); err != nil {
		return err
	}
	design, err := t.Get(S(snapshot, "id"))
	if err != nil {
		return err
	}
	if design.Status != "proposed" || Hash(designSnapshot(design)) != Hash(snapshot) {
		return conflict("设计提案已变化，请重新审阅当前记录")
	}
	ids := []any{}
	for _, item := range Items(Obj(snapshot["data"])["prototypes"]) {
		ids = append(ids, S(Obj(item), "artifactId"))
	}
	prototypes, err := s.designPrototypes(t, g, ids)
	if err != nil {
		return err
	}
	if Hash(prototypes) != Hash(Obj(snapshot["data"])["prototypes"]) {
		return conflict("原型元数据与设计快照不一致")
	}
	return nil
}

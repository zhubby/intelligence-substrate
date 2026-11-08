package platform

import "sort"

func qualityFields(in M, allowed ...string) error {
	for key := range in {
		if !Has(allowed, key) {
			return invalid("自动验证包含不支持的字段：" + key)
		}
	}
	return nil
}

func qualityCriteria(value any) ([]string, error) {
	var values []string
	switch v := value.(type) {
	case []string:
		values = append([]string{}, v...)
	case []any:
		values = []string{}
		for _, item := range v {
			id, ok := item.(string)
			if !ok {
				return nil, invalid("验证标准必须为字符串数组")
			}
			values = append(values, id)
		}
	default:
		return nil, invalid("需要明确的验证标准数组")
	}
	if len(values) > 100 {
		return nil, invalid("验证标准不能超过100项")
	}
	sort.Strings(values)
	for i, id := range values {
		if !IDPattern.MatchString(id) || (i > 0 && id == values[i-1]) {
			return nil, invalid("验证标准ID无效或重复")
		}
	}
	return values, nil
}

func (s *Service) qualityCommand(t *Tx, a Actor, command string, in M) (Resource, error) {
	if err := human(a); err != nil {
		return Resource{}, err
	}
	if command == "quality.revoke" {
		return s.revokeQualityPolicy(t, a, in)
	}
	g, err := s.scoped(t, a, S(in, "goalId"), "goal", "quality")
	if err != nil {
		return g, err
	}
	if err = qualityFields(in, "goalId", "expectedRequirementRevision", "expectedPlanId", "pipelineId", "target", "criteria", "budgetCents", "key"); err != nil {
		return g, err
	}
	if err = validateText(S(in, "key"), "幂等键", 160); err != nil {
		return g, err
	}
	scope := "quality.bind:" + g.ID + ":" + a.ID
	previous, exists, err := t.Once(scope, S(in, "key"), in)
	if err != nil {
		return g, err
	}
	if exists {
		return previous, nil
	}
	if err = clarificationRevision(g, in["expectedRequirementRevision"]); err != nil {
		return g, err
	}
	if err = approved(t, g); err != nil {
		return g, err
	}
	plan, err := t.Get(S(in, "expectedPlanId"))
	if err != nil || plan.Kind != "plan" || plan.ProjectID != g.ProjectID || plan.GoalID != g.ID || plan.Status != "active" || plan.ID != S(g.Data, "activePlanId") || revision(plan) != g.Revision {
		return g, conflict("需要当前已批准的准确方案")
	}
	pipeline, err := s.scoped(t, a, S(in, "pipelineId"), "pipeline", "quality")
	if err != nil {
		return g, err
	}
	if pipeline.ProjectID != g.ProjectID {
		return g, denied()
	}
	target := S(in, "target")
	if !Has(Strings(g.Data["targets"]), target) {
		return g, invalid("自动验证目标不属于需求基线")
	}
	criteria, err := qualityCriteria(in["criteria"])
	if err != nil {
		return g, err
	}
	environment := S(pipeline.Data, "verificationEnvironment")
	if environment != "ci" && environment != "simulator" && environment != targetEnvironment(target) {
		return g, invalid("验证策略实际环境与目标不匹配")
	}
	if len(criteria) == 0 && environment != targetEnvironment(target) {
		return g, invalid("未关联业务标准的策略必须验证准确目标环境")
	}
	for _, id := range criteria {
		found := false
		for _, x := range Items(g.Data["criteria"]) {
			c := Obj(x)
			if S(c, "id") == id && S(c, "method") == S(pipeline.Data, "method") {
				found = true
			}
		}
		if !found {
			return g, invalid("验证策略与验收标准方式不符")
		}
	}
	budget, err := costCents(in["budgetCents"])
	if err != nil {
		return g, err
	}
	if float64(budget) > N(g.Data, "budgetCents") {
		return g, invalid("单次验证预算不能超过目标预算")
	}
	policies, err := t.List(g.ProjectID, "quality_policy", g.ID)
	if err != nil {
		return g, err
	}
	if len(policies) >= 200 {
		return g, conflict("当前目标已达到200条质量绑定限制")
	}
	definition := Hash(M{"planId": plan.ID, "requirementRevision": g.Revision, "pipelineId": pipeline.ID, "target": target, "criteria": criteria})
	active := 0
	for _, policy := range policies {
		if policy.Status == "active" && revision(policy) == g.Revision && S(policy.Data, "planId") == plan.ID {
			active++
			if S(policy.Data, "definitionDigest") == definition {
				return g, conflict("已有相同活动验证绑定，请先撤销旧绑定")
			}
		}
	}
	if active >= 32 {
		return g, conflict("每个目标最多32条活动质量绑定")
	}
	policy := resource("quality_policy", pipeline.Title+" · 自动验证", g.ProjectID, g.ID, M{"requirementRevision": g.Revision, "planId": plan.ID, "planBinding": S(plan.Data, "binding"), "baselineDigest": Hash(M{"criteria": g.Data["criteria"], "targets": g.Data["targets"]}), "pipelineId": pipeline.ID, "pipelineRevision": pipeline.Revision, "pipelineDigest": Hash(pipeline.Data), "runtime": S(pipeline.Data, "runtime"), "method": S(pipeline.Data, "method"), "verificationEnvironment": environment, "target": target, "criteria": criteria, "budgetCents": budget, "boundBy": a.ID, "definitionDigest": definition})
	policy.Data["binding"] = Hash(policy.Data)
	policy.Status = "active"
	if err = t.Put(policy); err != nil {
		return policy, err
	}
	return policy, t.Remember(scope, S(in, "key"), in, policy)
}

func (s *Service) revokeQualityPolicy(t *Tx, a Actor, in M) (Resource, error) {
	policy, err := s.scoped(t, a, S(in, "id"), "quality_policy", "quality")
	if err != nil {
		return policy, err
	}
	if err = qualityFields(in, "id", "reason", "key"); err != nil {
		return policy, err
	}
	if err = validateText(S(in, "key"), "幂等键", 160); err != nil {
		return policy, err
	}
	if err = validateText(S(in, "reason"), "撤销原因", 2000); err != nil {
		return policy, err
	}
	scope := "quality.revoke:" + policy.ID + ":" + a.ID
	if previous, exists, e := t.Once(scope, S(in, "key"), in); e != nil || exists {
		return previous, e
	}
	if policy.Status != "active" {
		return policy, conflict("只有活动质量绑定可撤销")
	}
	policy.Status = "revoked"
	policy.Data["revokedBy"] = a.ID
	policy.Data["revocationReason"] = S(in, "reason")
	all, err := t.List(policy.ProjectID, "", policy.GoalID)
	if err != nil {
		return policy, err
	}
	for _, r := range all {
		if r.Kind == "task" && S(r.Data, "qualityPolicyId") == policy.ID && Has([]string{"pending", "running", "blocked", "waiting"}, r.Status) {
			r.Status = "cancelled"
			r.Data["reason"] = "独立质量绑定已撤销"
			if err = t.Put(r); err != nil {
				return policy, err
			}
		}
		if r.Kind == "run" && r.Status == "running" && S(Obj(r.Data["task"]), "qualityPolicyId") == policy.ID {
			r.Status = "cancelled"
			delete(r.Data, "leaseToken")
			if err = t.Put(r); err != nil {
				return policy, err
			}
		}
	}
	if err = t.Put(policy); err != nil {
		return policy, err
	}
	return policy, t.Remember(scope, S(in, "key"), in, policy)
}

func (s *Service) validQualityPolicy(t *Tx, policy, g Resource) (Resource, error) {
	if policy.Kind != "quality_policy" || policy.Status != "active" || policy.ProjectID != g.ProjectID || policy.GoalID != g.ID || revision(policy) != g.Revision || S(policy.Data, "planId") != S(g.Data, "activePlanId") {
		return Resource{}, conflict("质量绑定不再属于当前批准基线和方案")
	}
	data := copyM(policy.Data)
	delete(data, "binding")
	if Hash(data) != S(policy.Data, "binding") {
		return Resource{}, conflict("质量绑定内容摘要已变化")
	}
	if err := approved(t, g); err != nil {
		return Resource{}, err
	}
	if Hash(M{"criteria": g.Data["criteria"], "targets": g.Data["targets"]}) != S(policy.Data, "baselineDigest") {
		return Resource{}, conflict("质量绑定的验收基线已变化")
	}
	plan, err := t.Get(S(policy.Data, "planId"))
	if err != nil || plan.Status != "active" || plan.Kind != "plan" || plan.ProjectID != g.ProjectID || plan.GoalID != g.ID || S(plan.Data, "binding") != S(policy.Data, "planBinding") {
		return Resource{}, conflict("质量绑定的交付方案已变化")
	}
	pipeline, err := t.Get(S(policy.Data, "pipelineId"))
	if err != nil || pipeline.Kind != "pipeline" || pipeline.ProjectID != g.ProjectID || pipeline.Revision != int(N(policy.Data, "pipelineRevision")) || Hash(pipeline.Data) != S(policy.Data, "pipelineDigest") {
		return Resource{}, conflict("受保护验证策略已变化，需要重新绑定")
	}
	return pipeline, nil
}

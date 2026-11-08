package platform

import "fmt"

func validateCriteria(v any) ([]any, error) {
	items := Items(v)
	if len(items) == 0 || len(items) > 100 {
		return nil, invalid("需要 1–100 项验收标准")
	}
	seen := map[string]bool{}
	out := []any{}
	for _, x := range items {
		m := Obj(x)
		id := S(m, "id")
		if id == "" {
			id = NewID("criterion")
		}
		if !IDPattern.MatchString(id) || seen[id] {
			return nil, invalid("验收标准 ID 无效或重复")
		}
		seen[id] = true
		if e := validateText(S(m, "text"), "验收标准", 2000); e != nil {
			return nil, e
		}
		method := S(m, "method")
		if method == "" {
			method = "automated"
		}
		if !Has([]string{"automated", "manual", "device", "simulator"}, method) {
			return nil, invalid("未知验证方式")
		}
		out = append(out, M{"id": id, "text": S(m, "text"), "method": method})
	}
	return out, nil
}
func validateTargets(v any) ([]string, error) {
	targets := Strings(v)
	if len(targets) == 0 {
		return nil, invalid("至少选择一个交付目标")
	}
	seen := map[string]bool{}
	for _, x := range targets {
		if !Has(Targets, x) || seen[x] {
			return nil, invalid("交付目标无效或重复")
		}
		seen[x] = true
	}
	return targets, nil
}
func (s *Service) goalCommand(t *Tx, a Actor, cmd string, in M, adopted ...M) (Resource, error) {
	if cmd == "goals.create" {
		p := S(in, "projectId")
		if e := s.allowed(t, a, p, "product"); e != nil {
			return Resource{}, e
		}
		if e := validateText(S(in, "title"), "交付目标", 200); e != nil {
			return Resource{}, e
		}
		if e := validateText(S(in, "outcome"), "业务结果", 12000); e != nil {
			return Resource{}, e
		}
		criteria := []any{}
		if value, exists := in["criteria"]; exists {
			var err error
			criteria, err = validateCriteria(value)
			if err != nil {
				return Resource{}, err
			}
		}
		targets, e := validateTargets(in["targets"])
		if e != nil {
			return Resource{}, e
		}
		budget, e := money(in, "budget", 50)
		if e != nil {
			return Resource{}, e
		}
		g := resource("goal", S(in, "title"), p, "", M{"outcome": S(in, "outcome"), "criteria": criteria, "targets": targets, "budgetCents": budget, "spentCents": 0, "approvedRevision": 0, "sources": Items(in["sources"]), "createdBy": a.ID})
		g.GoalID = g.ID
		g.Status = "awaiting_baseline"
		if e = t.Put(g); e != nil {
			return g, e
		}
		return g, s.saveRequirement(t, g, a)
	}
	g, e := s.scoped(t, a, S(in, "id"), "goal", "product")
	if e != nil {
		return g, e
	}
	if cmd == "goals.approve" {
		if e = human(a); e != nil {
			return g, e
		}
		if _, err := validateCriteria(g.Data["criteria"]); err != nil {
			return g, err
		}
		if value, exists := in["expectedRequirementRevision"]; exists {
			if e = clarificationRevision(g, value); e != nil {
				return g, e
			}
		}
		clarifications, err := clarificationSnapshot(t, g)
		if err != nil {
			return g, err
		}
		g.Data["approvedRevision"] = g.Revision
		g.Status = "planning"
		// Canonical JSON maps keep the digest stable after persistence/restore.
		clarificationData := copyM(M{"items": clarifications})["items"]
		approval := resource("approval", "需求基线批准", g.ProjectID, g.ID, M{"type": "baseline", "requirementRevision": g.Revision, "actorId": a.ID, "binding": Hash(g.Data["criteria"]), "decision": "approved", "clarifications": clarificationData, "clarificationsDigest": Hash(clarificationData)})
		if e = t.Put(approval); e != nil {
			return g, e
		}
		return g, t.Put(g)
	}
	if value, exists := in["expectedRequirementRevision"]; exists {
		if err := clarificationRevision(g, value); err != nil {
			return g, err
		}
	}
	if e = validateText(S(in, "reason"), "变更原因", 2000); e != nil {
		return g, e
	}
	if v, ok := in["outcome"]; ok {
		if e = validateText(S(in, "outcome"), "业务结果", 12000); e != nil {
			return g, e
		}
		g.Data["outcome"] = v
	}
	if v, ok := in["criteria"]; ok {
		c, err := validateCriteria(v)
		if err != nil {
			return g, err
		}
		g.Data["criteria"] = c
	}
	if v, ok := in["targets"]; ok {
		ts, err := validateTargets(v)
		if err != nil {
			return g, err
		}
		g.Data["targets"] = ts
	}
	if _, ok := in["budget"]; ok {
		budget, err := money(in, "budget", 50)
		if err != nil {
			return g, err
		}
		if budget < int64(N(g.Data, "spentCents")) {
			return g, invalid("预算不能低于已发生费用")
		}
		g.Data["budgetCents"] = budget
	}
	if len(adopted) == 1 {
		for key, value := range adopted[0] {
			g.Data[key] = value
		}
	} else if _, outcomeChanged := in["outcome"]; outcomeChanged || in["criteria"] != nil || in["requirementBrief"] != nil {
		// Public manual edits cannot keep claiming an earlier adopted document.
		delete(g.Data, "requirementBrief")
		delete(g.Data, "requirementProposalId")
		delete(g.Data, "requirementContentDigest")
		if value, exists := in["requirementBrief"]; exists {
			brief, ok := value.(string)
			if !ok || len(brief) > 16000 {
				return g, invalid("需求说明须为不超过 16000 字节的文本")
			}
			g.Data["requirementBrief"] = brief
		}
	}
	g.Revision++
	g.Status = "awaiting_baseline"
	g.Data["approvedRevision"] = 0
	g.Data["changeReason"] = S(in, "reason")
	all, e := t.List(g.ProjectID, "", g.ID)
	if e != nil {
		return g, e
	}
	for _, r := range all {
		if r.Kind == "preview" && previewView(r).Status != "active" {
			// Finished previews retain the reason and state of their closure.
			continue
		}
		if (r.Kind == "approval" && S(r.Data, "type") == "runtime_rollback") || (r.Kind == "evidence" && S(r.Data, "purpose") == "runtime") {
			// Future product scope cannot invalidate an approved recovery or the
			// independent operational proof of software still in service.
			continue
		}
		if Has([]string{"plan", "design", "approval", "evidence", "release", "quality_policy", "preview", "store_revision"}, r.Kind) {
			r.Status = "stale"
			if e = t.Put(r); e != nil {
				return g, e
			}
		}
		if r.Kind == "task" && !operationalTask(r) && !Has([]string{"succeeded", "failed", "cancelled"}, r.Status) {
			r.Status = "cancelled"
			r.Data["reason"] = "需求版本变化"
			if e = t.Put(r); e != nil {
				return g, e
			}
		}
		if r.Kind == "run" && r.Status == "running" {
			if operationalTask(Resource{Data: Obj(r.Data["task"])}) {
				continue
			}
			r.Status = "cancelled"
			if e = t.Put(r); e != nil {
				return g, e
			}
		}
	}
	if e = t.Put(g); e != nil {
		return g, e
	}
	return g, s.saveRequirement(t, g, a)
}
func (s *Service) saveRequirement(t *Tx, g Resource, a Actor) error {
	r := resource("requirement", fmt.Sprintf("需求基线 v%d", g.Revision), g.ProjectID, g.ID, copyM(g.Data))
	r.Revision = g.Revision
	r.Data["actorId"] = a.ID
	return t.Put(r)
}
func validateTasks(v any, internal ...bool) ([]any, error) {
	items := Items(v)
	if len(items) == 0 || len(items) > 100 {
		return nil, invalid("计划需要 1–100 个工作项")
	}
	seen := map[string]M{}
	out := []any{}
	for _, x := range items {
		m := copyM(Obj(x))
		delete(m, "commandText")
		if _, exists := m["reportPath"]; exists {
			return nil, invalid("报告路径只能由受保护验证策略声明")
		}
		if e := validateSigningTask(m); e != nil {
			return nil, e
		}
		if len(internal) == 0 {
			for _, key := range []string{"messageWaitId", "messageWaitCount", "messageStepsUsed", "messageContinuation", "blueprintSeed", "agentProgressId", "agentResume", "agentProgressVersion"} {
				if _, ok := m[key]; ok {
					return nil, invalid("计划包含控制面保留字段：" + key)
				}
			}
			for _, key := range []string{"planId", "requirementRevision", "logicalId", "attempts", "maxAttempts", "defectId", "repairRecipeId", "parentId", "children", "releaseId", "operationKey", "assignedWorkerId", "requiresArtifactFrom", "pipelineRevision", "qualityPolicyId", "qualityPolicyDigest", "qualityPipelineDigest", "qualityDispatchId", "sourceBinding", "sourceConflict", "sourceRecoveryId", "sourceRecoveryIds", "integrationId", "integrationParents", "integrationWorkspaceCommit", "integrationConflicts", "integrationResolutions", "integrationResolutionDigest", "signingInput", "signingInputDigest", "signingRepairId", "signingPhase", "signingPolls", "signingNextCheckAt", "signingDeadline", "signingOperation", "signingActiveSeconds", "notarizationId"} {
				if _, ok := m[key]; ok {
					return nil, invalid("计划包含控制面保留字段：" + key)
				}
			}
		}
		id := S(m, "id")
		if !IDPattern.MatchString(id) || seen[id] != nil {
			return nil, invalid("任务 ID 无效或重复")
		}
		if e := validateText(S(m, "title"), "任务名称", 200); e != nil {
			return nil, e
		}
		if !Has(AgentRoles, S(m, "role")) {
			return nil, invalid("未知 Agent 角色")
		}
		if !Has([]string{"linux", "darwin", "windows", "device", "manual"}, S(m, "runtime")) {
			return nil, invalid("未知执行环境")
		}
		if !Has([]string{"analysis", "design", "code", "test", "build", "security", "publish", "manual"}, S(m, "kind")) {
			return nil, invalid("未知任务类型")
		}
		if S(m, "kind") == "publish" {
			return nil, invalid("发布工作项只能由已批准的发布候选创建")
		}
		if paths, e := taskWritePaths(m); e != nil {
			return nil, e
		} else if paths != nil {
			m["writePaths"] = paths
		}
		if value, exists := m["sourceMode"]; exists {
			mode, ok := value.(string)
			if !ok || mode != "integrate" || S(m, "kind") != "code" {
				return nil, invalid("只有开发工作项可以声明 sourceMode=integrate")
			}
		}
		if v, ok := m["sourceFrom"]; ok {
			selectors, e := sourceSelectors(v)
			if e != nil {
				return nil, e
			}
			m["sourceFrom"] = selectors
		}
		for _, key := range []string{"branch", "commit", "repositoryId"} {
			if value, ok := m[key]; ok {
				if _, valid := value.(string); !valid {
					return nil, invalid("任务源码字段必须为字符串：" + key)
				}
			}
		}
		if branch := S(m, "branch"); branch != "" && (!validBranch(branch) || (S(m, "kind") == "code" && branch == "main")) {
			return nil, invalid("任务源码分支无效")
		}
		if commit := S(m, "commit"); commit != "" && !CommitPattern.MatchString(commit) {
			return nil, invalid("任务源码需要完整提交 ID")
		}
		if N(m, "budgetCents") < 0 || N(m, "budgetCents") > 100000000 || N(m, "timeoutSeconds") < 0 || N(m, "timeoutSeconds") > 86400 {
			return nil, invalid("任务预算与超时范围无效")
		}
		if len(Strings(m["command"])) > 50 {
			return nil, invalid("命令参数超过限制")
		}
		if e := noSecrets(m); e != nil {
			return nil, e
		}
		seen[id] = m
		out = append(out, m)
	}
	color := map[string]int{}
	var visit func(string) error
	visit = func(id string) error {
		if seen[id] == nil {
			return invalid("任务依赖不存在")
		}
		if color[id] == 1 {
			return invalid("任务依赖包含循环")
		}
		if color[id] == 2 {
			return nil
		}
		color[id] = 1
		for _, dep := range Strings(seen[id]["dependsOn"]) {
			if e := visit(dep); e != nil {
				return e
			}
		}
		color[id] = 2
		return nil
	}
	for id := range seen {
		if e := visit(id); e != nil {
			return nil, e
		}
	}
	for id, m := range seen {
		reachable := map[string]bool{}
		var walk func(string)
		walk = func(dep string) {
			if reachable[dep] {
				return
			}
			reachable[dep] = true
			for _, ancestor := range Strings(seen[dep]["dependsOn"]) {
				walk(ancestor)
			}
		}
		for _, dep := range Strings(m["dependsOn"]) {
			walk(dep)
		}
		for _, selected := range Strings(m["sourceFrom"]) {
			if selected == id || !reachable[selected] {
				return nil, invalid("sourceFrom 必须选择工作项的依赖祖先")
			}
		}
		if controlledSigningTask(Resource{Data: m}) {
			selected := S(m, "inputArtifactFrom")
			if !reachable[selected] || S(seen[selected], "artifactStage") != "unsigned" || S(seen[selected], "target") != S(m, "target") {
				return nil, invalid("签名输入必须选择同一目标的无签名构建祖先")
			}
		}
	}
	return out, validateBlueprintDependencies(out)
}
func (s *Service) planCommand(t *Tx, a Actor, cmd string, in M) (Resource, error) {
	if cmd == "plans.propose" {
		for _, key := range []string{"design", "designDigest", "approvedDesign", "blueprint", "blueprintDigest", "blueprintSeed", "releaseIssues"} {
			if _, exists := in[key]; exists {
				return Resource{}, invalid("设计快照由控制面从 designId 派生")
			}
		}
		g, e := s.scoped(t, a, S(in, "goalId"), "goal", "product", "architect", "developer")
		if e != nil {
			return g, e
		}
		if e = approved(t, g); e != nil {
			return g, e
		}
		if _, exists := in["expectedRequirementRevision"]; exists && N(in, "expectedRequirementRevision") != float64(g.Revision) {
			return g, conflict("需求基线已变更，请按当前版本重新生成方案")
		}
		if e = validateText(S(in, "summary"), "方案说明", 12000); e != nil {
			return g, e
		}
		releaseIssues, e := releaseIssueSnapshot(t, g)
		if e != nil {
			return g, e
		}
		if e = s.validatePlanReleaseIssues(t, g, releaseIssues); e != nil {
			return g, e
		}
		design, e := planDesign(t, g, in)
		if e != nil {
			return g, e
		}
		if design != nil {
			if e = s.validatePlanDesign(t, g, design); e != nil {
				return g, e
			}
		}
		tasks, e := validateTasks(in["tasks"])
		if e != nil {
			return g, e
		}
		blueprint, e := planBlueprint(t, g, in)
		if e != nil {
			return g, e
		}
		if blueprint != nil {
			if e = s.validatePlanBlueprint(t, g, blueprint); e != nil {
				return g, e
			}
		}
		for _, x := range tasks {
			m := Obj(x)
			for _, binding := range []struct{ key, kind string }{{"repositoryId", "repository"}, {"pipelineId", "pipeline"}, {"artifactId", "artifact"}} {
				if id := S(m, binding.key); id != "" {
					r, e := s.scoped(t, a, id, binding.kind)
					if e != nil {
						return g, e
					}
					if r.ProjectID != g.ProjectID {
						return g, denied()
					}
				}
			}
			if output := S(m, "outputPath"); output != "" {
				if _, e = SafePath("/unused", output); e != nil {
					return g, e
				}
			}
			if target := S(m, "target"); target != "" && !Has(Strings(g.Data["targets"]), target) {
				return g, invalid("任务交付目标不属于需求基线")
			}
		}
		tasks, e = prepareBlueprintTasks(g, blueprint, B(in, "initializeBlueprint"), tasks)
		if e != nil {
			return g, e
		}
		plans, e := t.List(g.ProjectID, "plan", g.ID)
		if e != nil {
			return g, e
		}
		r := resource("plan", "交付方案", g.ProjectID, g.ID, M{"summary": S(in, "summary"), "risk": S(in, "risk"), "tasks": tasks, "requirementRevision": g.Revision, "authorId": a.ID, "reason": S(in, "reason")})
		r.Status = "proposed"
		r.Revision = len(plans) + 1
		if len(releaseIssues) > 0 {
			r.Data["releaseIssues"] = releaseIssues
		}
		if design != nil {
			r.Data["design"] = design
		}
		if blueprint != nil {
			r.Data["blueprint"], r.Data["initializeBlueprint"] = blueprint, B(in, "initializeBlueprint")
		}
		r.Data["binding"] = Hash(r.Data)
		return r, t.Put(r)
	}
	r, e := s.scoped(t, a, S(in, "id"), "plan", "architect")
	if e != nil {
		return r, e
	}
	if e = human(a); e != nil {
		return r, e
	}
	g, e := t.Get(r.GoalID)
	if e != nil {
		return r, e
	}
	if e = checkRevision(g, r); e != nil {
		return r, e
	}
	if e = approved(t, g); e != nil {
		return r, e
	}
	if r.Status != "proposed" {
		return r, conflict("方案已处理")
	}
	if e = s.validatePlanReleaseIssues(t, g, r.Data["releaseIssues"]); e != nil {
		return r, e
	}
	if len(Items(r.Data["releaseIssues"])) > 0 {
		bound := copyM(r.Data)
		delete(bound, "binding")
		if Hash(bound) != S(r.Data, "binding") {
			return r, conflict("方案与拒审问题绑定摘要不一致")
		}
	}
	if blueprint, exists := r.Data["blueprint"]; exists {
		bound := copyM(r.Data)
		delete(bound, "binding")
		if Hash(bound) != S(r.Data, "binding") {
			return r, conflict("方案与蓝图绑定摘要不一致")
		}
		if e = s.validatePlanBlueprint(t, g, blueprint); e != nil {
			return r, e
		}
		if e = validateBlueprintDependencies(Items(r.Data["tasks"])); e != nil {
			return r, e
		}
	} else {
		current, err := planBlueprint(t, g, M{})
		if err != nil {
			return r, err
		}
		if current != nil {
			return r, conflict("当前方案已有批准蓝图，请基于它重新提交方案")
		}
	}
	if design, exists := r.Data["design"]; exists {
		bound := copyM(r.Data)
		delete(bound, "binding")
		if Hash(bound) != S(r.Data, "binding") {
			return r, conflict("方案与设计绑定摘要不一致")
		}
		if e = s.validatePlanDesign(t, g, design); e != nil {
			return r, e
		}
	} else {
		current, err := planDesign(t, g, M{})
		if err != nil {
			return r, err
		}
		if current != nil {
			return r, conflict("当前方案已有批准设计，请基于它重新提交方案")
		}
	}
	all, e := t.List(r.ProjectID, "", r.GoalID)
	if e != nil {
		return r, e
	}
	for _, old := range all {
		if old.Kind == "quality_policy" && old.Status == "active" {
			old.Status = "stale"
			if e = t.Put(old); e != nil {
				return r, e
			}
		}
		if old.Kind == "plan" && old.Status == "active" {
			old.Status = "superseded"
			if e = t.Put(old); e != nil {
				return r, e
			}
		}
		if old.Kind == "task" && !Has([]string{"succeeded", "failed", "cancelled"}, old.Status) {
			old.Status = "cancelled"
			if e = t.Put(old); e != nil {
				return r, e
			}
		}
		if old.Kind == "run" && old.Status == "running" {
			old.Status = "cancelled"
			if e = t.Put(old); e != nil {
				return r, e
			}
		}
		if old.Kind == "release" {
			old.Status = "stale"
			if e = t.Put(old); e != nil {
				return r, e
			}
		}
	}
	r.Status = "active"
	r.Data["approvedBy"] = a.ID
	tasks := Items(r.Data["tasks"])
	for _, x := range tasks {
		m := copyM(Obj(x))
		m["planId"] = r.ID
		m["requirementRevision"] = g.Revision
		m["logicalId"] = S(m, "id")
		m["attempts"] = 0
		m["maxAttempts"] = 3
		if N(m, "timeoutSeconds") == 0 {
			m["timeoutSeconds"] = 900
		}
		if N(m, "budgetCents") == 0 {
			m["budgetCents"] = int64(N(g.Data, "budgetCents")) / int64(len(tasks))
		}
		task := resource("task", S(m, "title"), r.ProjectID, r.GoalID, m)
		task.Status = "pending"
		if e = t.Put(task); e != nil {
			return r, e
		}
	}
	approval := resource("approval", "交付方案批准", r.ProjectID, r.GoalID, M{"type": "plan", "planId": r.ID, "requirementRevision": g.Revision, "binding": S(r.Data, "binding"), "actorId": a.ID})
	if issues := Items(r.Data["releaseIssues"]); len(issues) > 0 {
		approval.Data["releaseIssues"] = issues
		for _, entry := range issues {
			issue, err := t.Get(S(Obj(entry), "id"))
			if err != nil {
				return r, err
			}
			issue.Status, issue.Data["planId"] = "planned", r.ID
			if err = t.Put(issue); err != nil {
				return r, err
			}
		}
	}
	if design, exists := r.Data["design"]; exists {
		approval.Data["design"], approval.Data["designDigest"] = design, Hash(design)
	}
	if blueprint, exists := r.Data["blueprint"]; exists {
		approval.Data["blueprint"], approval.Data["blueprintDigest"] = blueprint, Hash(blueprint)
	}
	if e = t.Put(approval); e != nil {
		return r, e
	}
	g.Data["activePlanId"] = r.ID
	g.Status = "developing"
	if e = t.Put(g); e != nil {
		return r, e
	}
	return r, t.Put(r)
}

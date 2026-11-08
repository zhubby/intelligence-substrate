package platform

// Adaptive changes may only rearrange pending approved work or split it within
// its existing role, runtime, command and total budget. They cannot change the
// requirement, quality policy, permission envelope or production approval.
func (s *Service) adaptiveCommand(t *Tx, a Actor, command string, in M) (Resource, error) {
	var g Resource
	var parent Resource
	var e error
	if command == "tasks.split" {
		parent, e = s.scoped(t, a, S(in, "id"), "task", "architect", "developer")
		if e != nil {
			return parent, e
		}
		if _, seed := parent.Data["blueprintSeed"]; seed {
			return parent, conflict("蓝图初始化必须保持单一批准工作项，不能拆分")
		}
		if S(parent.Data, "signingRepairId") != "" {
			return parent, conflict("签名修复配方由权威记录保护，不能通用拆分")
		}
		if S(parent.Data, "messageWaitId") != "" {
			return parent, conflict("已有协作等待的工作项不能拆分，需要新批准的工作")
		}
		if _, bound := parent.Data["sourceBinding"]; bound {
			return parent, conflict("已绑定源码的工作项不能拆分，需要新批准的工作")
		}
		if S(parent.Data, "sourceMode") == "integrate" {
			return parent, conflict("集成任务不能拆分为重复集成，请批准分阶段的集成方案")
		}
		g, e = t.Get(parent.GoalID)
	} else {
		g, e = s.scoped(t, a, S(in, "goalId"), "goal", "architect", "developer")
	}
	if e != nil {
		return g, e
	}
	if e = approved(t, g); e != nil {
		return g, e
	}
	if S(g.Data, "activePlanId") == "" {
		return g, conflict("需要已批准方案")
	}
	if e = validateText(S(in, "reason"), "调整原因", 2000); e != nil {
		return g, e
	}
	if e = validateText(S(in, "impact"), "产物、成本及验收影响", 4000); e != nil {
		return g, e
	}
	change := resource("plan_change", "执行计划调整", g.ProjectID, g.ID, M{"planId": S(g.Data, "activePlanId"), "requirementRevision": g.Revision, "actorId": a.ID, "reason": S(in, "reason"), "impact": S(in, "impact"), "type": command})
	if command == "tasks.split" {
		if parent.Status != "pending" || S(parent.Data, "planId") != S(g.Data, "activePlanId") || !Has([]string{"analysis", "design", "code"}, S(parent.Data, "kind")) {
			return parent, conflict("只能拆分当前方案内待执行的分析、设计或开发工作项")
		}
		children := Items(in["children"])
		if len(children) < 2 || len(children) > 8 {
			return parent, invalid("每次拆分需要 2–8 个子工作项")
		}
		ids, logical := []string{}, []string{}
		budget := int64(N(parent.Data, "budgetCents"))
		per := budget / int64(len(children))
		for i, x := range children {
			child := Obj(x)
			if _, exists := child["writePaths"]; exists {
				return parent, invalid("拆分工作项继承批准的 writePaths，不能覆盖修改范围")
			}
			if e = validateText(S(child, "title"), "子工作项名称", 200); e != nil {
				return parent, e
			}
			if e = validateText(S(child, "instructions"), "执行说明", 8000); e != nil {
				return parent, e
			}
			data := copyM(parent.Data)
			id := NewID("part")
			data["logicalId"] = id
			data["id"] = id
			data["parentId"] = parent.ID
			data["title"] = S(child, "title")
			data["instructions"] = S(child, "instructions")
			data["budgetCents"] = per
			if i == len(children)-1 {
				data["budgetCents"] = budget - per*int64(i)
			}
			r := resource("task", S(child, "title"), g.ProjectID, g.ID, data)
			r.Status = "pending"
			if e = t.Put(r); e != nil {
				return parent, e
			}
			ids = append(ids, r.ID)
			logical = append(logical, id)
		}
		parent.Status = "waiting"
		parent.Data["children"] = ids
		parent.Data["dependsOn"] = logical
		parent.Data["reason"] = "等待拆分后的子工作项"
		if e = t.Put(parent); e != nil {
			return parent, e
		}
		change.Data["parentId"] = parent.ID
		change.Data["children"] = ids
	} else {
		tasks, e := t.List(g.ProjectID, "task", g.ID)
		if e != nil {
			return g, e
		}
		byID := map[string]Resource{}
		for _, task := range tasks {
			if S(task.Data, "planId") == S(g.Data, "activePlanId") && !Has([]string{"cancelled"}, task.Status) {
				byID[S(task.Data, "logicalId")] = task
			}
		}
		for _, x := range Items(in["tasks"]) {
			update := Obj(x)
			if _, exists := update["writePaths"]; exists {
				return g, invalid("重排只能改变依赖，不能覆盖批准的 writePaths")
			}
			task, ok := byID[S(update, "id")]
			if !ok || task.Status != "pending" {
				return g, conflict("只能调整待执行工作项的依赖")
			}
			if S(task.Data, "signingRepairId") != "" {
				return g, conflict("签名修复依赖由权威记录保护，不能通用重排")
			}
			if S(task.Data, "messageWaitId") != "" {
				return g, conflict("已有协作等待的工作项不能修改依赖，需要新批准的工作")
			}
			if S(task.Data, "qualityPolicyId") != "" {
				return g, conflict("独立质量工作项的依赖由受保护调度控制")
			}
			if _, bound := task.Data["sourceBinding"]; bound {
				return g, conflict("已绑定源码的工作项不能修改依赖，需要新批准的工作")
			}
			task.Data["dependsOn"] = Strings(update["dependsOn"])
			byID[S(update, "id")] = task
		}
		graph := []any{}
		for id, task := range byID {
			data := copyM(task.Data)
			data["id"] = id
			data["title"] = task.Title
			graph = append(graph, data)
		}
		if _, e = validateTasks(graph, true); e != nil {
			return g, e
		}
		for _, task := range byID {
			if e = t.Put(task); e != nil {
				return g, e
			}
		}
		change.Data["dependencies"] = in["tasks"]
	}
	return change, t.Put(change)
}

func (s *Service) aggregateChildren(t *Tx, project string) error {
	tasks, e := t.List(project, "task", "")
	if e != nil {
		return e
	}
	byID := map[string]Resource{}
	for _, task := range tasks {
		byID[task.ID] = task
	}
	for _, task := range tasks {
		if !Has([]string{"waiting", "blocked"}, task.Status) {
			continue
		}
		children := Strings(task.Data["children"])
		if len(children) == 0 {
			continue
		}
		done, blocked := true, false
		for _, id := range children {
			child, ok := byID[id]
			if !ok || child.Status != "succeeded" {
				done = false
			}
			if Has([]string{"failed", "blocked", "cancelled"}, child.Status) {
				blocked = true
			}
		}
		if done {
			task.Status = "succeeded"
			task.Data["reason"] = "全部子工作项完成"
		} else if blocked {
			task.Status = "blocked"
			task.Data["reason"] = "子工作项需要处理"
		} else {
			task.Status = "waiting"
		}
		if e = t.Put(task); e != nil {
			return e
		}
	}
	return nil
}

func (s *Service) cancelTaskTree(t *Tx, task Resource) error {
	for _, id := range Strings(task.Data["children"]) {
		child, e := t.Get(id)
		if e != nil {
			return e
		}
		if !Has([]string{"succeeded", "cancelled", "stale"}, child.Status) {
			if e = s.cancelTaskTree(t, child); e != nil {
				return e
			}
		}
	}
	runs, e := t.List(task.ProjectID, "run", task.GoalID)
	if e != nil {
		return e
	}
	for _, run := range runs {
		if S(run.Data, "taskId") == task.ID && run.Status == "running" {
			run.Status = "cancelled"
			delete(run.Data, "leaseToken")
			if e = t.Put(run); e != nil {
				return e
			}
		}
	}
	task.Status = "cancelled"
	return t.Put(task)
}

package platform

func (s *Service) engineeringCommand(t *Tx, a Actor, cmd string, in M) (Resource, error) {
	if cmd == "knowledge.revise" {
		old, e := s.scoped(t, a, S(in, "id"), "knowledge", "product", "architect", "developer", "analysis", "design")
		if e != nil {
			return old, e
		}
		if old.Status == "superseded" {
			return old, conflict("请修订最新的知识版本")
		}
		if e = validateText(S(in, "content"), "知识内容", 64000); e != nil {
			return old, e
		}
		if e = validateText(S(in, "reason"), "修订原因", 2000); e != nil {
			return old, e
		}
		if e = noSecrets(in); e != nil {
			return old, e
		}
		data := copyM(old.Data)
		data["previousId"] = old.ID
		data["content"] = S(in, "content")
		data["authorId"] = a.ID
		data["reason"] = S(in, "reason")
		if source := S(in, "source"); source != "" {
			data["source"] = source
		}
		next := resource("knowledge", old.Title, old.ProjectID, old.GoalID, data)
		next.Revision = old.Revision + 1
		old.Status = "superseded"
		if e = t.Put(old); e != nil {
			return old, e
		}
		return next, t.Put(next)
	}
	if cmd == "charges.reconcile" {
		charge, e := s.scoped(t, a, S(in, "id"), "charge", "owner")
		if e != nil {
			return charge, e
		}
		if e = human(a); e != nil {
			return charge, e
		}
		if !Has([]string{"reserved", "uncertain"}, charge.Status) {
			return charge, conflict("仅能核对待确认费用")
		}
		if e = validateText(S(in, "reason"), "核对依据", 2000); e != nil {
			return charge, e
		}
		amount, e := costCents(in["chargedCents"])
		if e != nil {
			return charge, e
		}
		g, e := t.Get(charge.GoalID)
		if e != nil {
			return charge, e
		}
		difference := float64(amount) - N(charge.Data, "chargedCents")
		g.Data["spentCents"] = N(g.Data, "spentCents") + difference
		charge.Status = "settled"
		charge.Data["chargedCents"] = amount
		charge.Data["reconciledBy"] = a.ID
		charge.Data["reason"] = S(in, "reason")
		if runID := S(charge.Data, "runId"); runID != "" {
			run, e := t.Get(runID)
			if e != nil {
				return charge, e
			}
			run.Data["spentCents"] = N(run.Data, "spentCents") + difference
			if e = t.Put(run); e != nil {
				return charge, e
			}
		}
		if e = t.Put(g); e != nil {
			return charge, e
		}
		return charge, t.Put(charge)
	}
	if cmd == "projects.policy" {
		project, e := s.scoped(t, a, S(in, "id"), "project", "owner")
		if e != nil {
			return project, e
		}
		if e = human(a); e != nil {
			return project, e
		}
		project.Data["allowExternalModels"] = B(in, "allowExternalModels")
		project.Revision++
		return project, t.Put(project)
	}
	if cmd == "pipelines.run" {
		if e := human(a); e != nil {
			return Resource{}, e
		}
		pipeline, e := s.scoped(t, a, S(in, "pipelineId"), "pipeline", "quality")
		if e != nil {
			return pipeline, e
		}
		artifact, e := s.scoped(t, a, S(in, "artifactId"), "artifact", "quality")
		if e != nil {
			return artifact, e
		}
		if pipeline.ProjectID != artifact.ProjectID {
			return artifact, denied()
		}
		if S(artifact.Data, "type") != "package" {
			return artifact, invalid("验证任务必须绑定应用制品")
		}
		if e = s.validateSigningArtifact(t, artifact); e != nil {
			return artifact, e
		}
		g, e := t.Get(artifact.GoalID)
		if e != nil {
			return g, e
		}
		if e = checkRevision(g, artifact); e != nil {
			return g, e
		}
		if e = approved(t, g); e != nil {
			return g, e
		}
		if S(g.Data, "activePlanId") == "" {
			return g, conflict("需要已批准方案")
		}
		criteria := Strings(in["criteria"])
		if len(criteria) == 0 {
			return g, invalid("需要选择验收标准")
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
		task := resource("task", pipeline.Title, g.ProjectID, g.ID, M{"kind": "test", "role": "quality", "runtime": S(pipeline.Data, "runtime"), "pipelineId": pipeline.ID, "pipelineRevision": pipeline.Revision, "artifactId": artifact.ID, "repositoryId": S(artifact.Data, "repositoryId"), "commit": S(artifact.Data, "commit"), "criteria": criteria, "requirementRevision": g.Revision, "planId": S(g.Data, "activePlanId"), "logicalId": NewID("verify"), "attempts": 0, "maxAttempts": 3, "timeoutSeconds": N(pipeline.Data, "timeoutSeconds"), "budgetCents": int64(N(g.Data, "budgetCents")) / 10})
		task.Status = "pending"
		task.Data["target"] = S(artifact.Data, "target")
		return task, t.Put(task)
	}
	p := S(in, "projectId")
	roles := []string{"quality"}
	if cmd == "knowledge.create" {
		roles = []string{"product", "architect", "developer", "analysis", "design"}
	}
	if e := s.allowed(t, a, p, roles...); e != nil {
		return Resource{}, e
	}
	if e := validateText(S(in, "title"), "名称", 200); e != nil {
		return Resource{}, e
	}
	if cmd == "knowledge.create" {
		if e := validateText(S(in, "content"), "知识内容", 64000); e != nil {
			return Resource{}, e
		}
		if e := noSecrets(in); e != nil {
			return Resource{}, e
		}
		r := resource("knowledge", S(in, "title"), p, "", M{"content": S(in, "content"), "source": S(in, "source"), "authorId": a.ID})
		return r, t.Put(r)
	}
	if e := human(a); e != nil {
		return Resource{}, e
	}
	argv := Strings(in["command"])
	if len(argv) == 0 || len(argv) > 50 {
		return Resource{}, invalid("测试策略需要明确的命令参数")
	}
	if e := noSecrets(in); e != nil {
		return Resource{}, e
	}
	method := S(in, "method")
	if method == "" {
		method = "automated"
	}
	if !Has([]string{"automated", "simulator", "device"}, method) {
		return Resource{}, invalid("流水线验证方式无效")
	}
	runtime := S(in, "runtime")
	if !Has([]string{"linux", "darwin", "windows", "device"}, runtime) {
		return Resource{}, invalid("流水线执行环境无效")
	}
	environment := S(in, "verificationEnvironment")
	if environment == "" {
		environment = "ci"
	}
	if e := validateVerificationEnvironment(environment, method, runtime); e != nil {
		return Resource{}, e
	}
	r := resource("pipeline", S(in, "title"), p, "", M{"command": argv, "runtime": runtime, "method": method, "verificationEnvironment": environment, "createdBy": a.ID, "timeoutSeconds": 900})
	if value, exists := in["reportPath"]; exists {
		name, ok := value.(string)
		if !ok {
			return r, invalid("报告路径必须为字符串")
		}
		if name != "" {
			if err := ValidateReportPath(name); err != nil {
				return r, err
			}
			r.Data["reportPath"] = name
		}
	}
	return r, t.Put(r)
}

func validateVerificationEnvironment(environment, method, runtime string) error {
	if !Has([]string{"ci", "browser", "windows", "macos", "ios-device", "android-device", "simulator"}, environment) {
		return invalid("目标验证环境无效")
	}
	if Has([]string{"ios-device", "android-device"}, environment) && method != "device" {
		return invalid("移动端真机证据必须使用 device 验证方式")
	}
	if method == "simulator" && environment != "simulator" {
		return invalid("模拟器验证不能标记为真实目标环境")
	}
	if runtime != "" {
		if environment == "windows" && runtime != "windows" || environment == "macos" && runtime != "darwin" || environment == "ios-device" && !Has([]string{"darwin", "device"}, runtime) {
			return invalid("目标验证环境与执行节点系统不匹配")
		}
	}
	return nil
}

func targetEnvironment(target string) string {
	if target == "web" {
		return "browser"
	}
	if target == "ios-appstore" {
		return "ios-device"
	}
	if target == "windows" || target == "macos" {
		return target
	}
	return "android-device"
}

func environmentEvidence(ev, artifact Resource) bool {
	if Has([]string{"ios-device", "android-device"}, S(ev.Data, "verificationEnvironment")) && S(ev.Data, "method") != "device" {
		return false
	}
	return ev.Status == "passed" && S(ev.Data, "artifactId") == artifact.ID && S(ev.Data, "digest") == S(artifact.Data, "digest") && S(ev.Data, "commit") == S(artifact.Data, "commit") && revision(ev) == revision(artifact) && S(ev.Data, "verificationEnvironment") == targetEnvironment(S(artifact.Data, "target")) && S(ev.Data, "actorId") != S(artifact.Data, "producerId")
}

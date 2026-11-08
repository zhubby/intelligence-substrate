package platform

import "fmt"

func (s *Service) recordTestOutcome(t *Tx, run, task Resource) error {
	if S(task.Data, "kind") != "test" {
		return nil
	}
	if run.Status == "succeeded" {
		if id := S(task.Data, "defectId"); id != "" {
			defect, e := t.Get(id)
			if e != nil {
				return e
			}
			defect.Status = "closed"
			defect.Data["verifiedRunId"] = run.ID
			defect.Data["evidenceId"] = S(run.Data, "evidenceId")
			return t.Put(defect)
		}
		return nil
	}
	if run.Status != "failed" {
		return nil
	}
	var defect Resource
	var e error
	if id := S(task.Data, "defectId"); id != "" {
		defect, e = t.Get(id)
		if e != nil {
			return e
		}
	} else {
		defect = resource("defect", "验证失败："+task.Title, run.ProjectID, run.GoalID, M{"requirementRevision": revision(run), "testTaskId": task.ID, "artifactId": S(task.Data, "artifactId"), "criteria": Strings(task.Data["criteria"]), "repairAttempts": 0})
	}
	defect.Status = "open"
	defect.Data["failedRunId"] = run.ID
	defect.Data["failedTestTaskId"] = task.ID
	defect.Data["log"] = S(run.Data, "log")
	defect.Data["artifactId"] = S(task.Data, "artifactId")
	defect.Data["failedRunIds"] = append(Strings(defect.Data["failedRunIds"]), run.ID)
	if N(defect.Data, "repairAttempts") >= 2 {
		defect.Status = "blocked"
		defect.Data["reason"] = "自动修复达到两次上限，需要人工处理"
		return t.Put(defect)
	}
	g, e := t.Get(run.GoalID)
	if e != nil {
		return e
	}
	plan, e := t.Get(S(g.Data, "activePlanId"))
	if e != nil {
		return e
	}
	artifact, e := t.Get(S(task.Data, "artifactId"))
	if e != nil {
		return e
	}
	var recipe M
	preferred := S(defect.Data, "repairRecipeId")
	if preferred == "" {
		origins := map[string]bool{}
		seen := map[string]bool{}
		var walk func(Resource)
		walk = func(producer Resource) {
			if seen[producer.ID] || producer.ProjectID != run.ProjectID || producer.GoalID != run.GoalID || revision(producer) != revision(run) || S(producer.Data, "planId") != plan.ID {
				return
			}
			seen[producer.ID] = true
			if S(producer.Data, "kind") == "code" {
				id := S(producer.Data, "repairRecipeId")
				if id == "" {
					id = S(producer.Data, "logicalId")
				}
				origins[id] = true
				return
			}
			for _, x := range Items(Obj(producer.Data["sourceBinding"])["inputs"]) {
				if input, err := t.Get(S(Obj(x), "taskId")); err == nil {
					walk(input)
				}
			}
		}
		if buildRun, err := t.Get(S(artifact.Data, "runId")); err == nil {
			if build, err := t.Get(S(buildRun.Data, "taskId")); err == nil {
				walk(build)
			}
		}
		if len(origins) == 1 {
			for id := range origins {
				preferred = id
			}
		}
	}
	candidates := []M{}
	for _, x := range Items(plan.Data["tasks"]) {
		candidate := Obj(x)
		if S(candidate, "kind") == "code" && S(candidate, "role") == "developer" && (S(candidate, "repositoryId") == "" || S(candidate, "repositoryId") == S(artifact.Data, "repositoryId")) {
			candidates = append(candidates, candidate)
			if S(candidate, "id") == preferred {
				recipe = copyM(candidate)
			}
		}
	}
	if recipe == nil && preferred == "" && len(candidates) == 1 {
		recipe = copyM(candidates[0])
	}
	if recipe == nil {
		defect.Status = "blocked"
		defect.Data["reason"] = "当前批准方案没有唯一匹配源码来源的开发工作项，请人工处理"
		return t.Put(defect)
	}
	remaining := N(g.Data, "budgetCents") - N(g.Data, "spentCents")
	if remaining < 4 {
		defect.Status = "blocked"
		defect.Data["reason"] = "修复预算不足，需要人工调整"
		return t.Put(defect)
	}
	defect.Data["repairRecipeId"] = S(recipe, "id")
	recipe["repairRecipeId"] = S(recipe, "id")
	recipe["id"] = NewID("repair")
	recipe["logicalId"] = recipe["id"]
	recipe["repositoryId"] = S(artifact.Data, "repositoryId")
	recipe["commit"] = S(artifact.Data, "commit")
	recipe["planId"] = plan.ID
	recipe["requirementRevision"] = g.Revision
	recipe["defectId"] = defect.ID
	recipe["kind"] = "code"
	recipe["dependsOn"] = []string{}
	delete(recipe, "sourceBinding")
	delete(recipe, "sourceConflict")
	delete(recipe, "sourceFrom")
	delete(recipe, "sourceMode")
	for _, key := range []string{"integrationId", "integrationWorkspaceCommit", "integrationConflicts", "integrationResolutions", "integrationResolutionDigest", "integrationParents"} {
		delete(recipe, key)
	}
	recipe["branch"] = fmt.Sprintf("repair/%s/%d", defect.ID, int(N(defect.Data, "repairAttempts"))+1)
	recipe["attempts"] = 0
	recipe["maxAttempts"] = 2
	recipe["budgetCents"] = int64(remaining / 4)
	recipe["instructions"] = "修复独立验证发现的问题，保持已批准需求及质量门槛。失败执行：" + run.ID + "。查看对应验证策略和日志，不得修改受保护的验证命令。"
	if N(recipe, "timeoutSeconds") == 0 {
		recipe["timeoutSeconds"] = 900
	}
	repair := resource("task", "修复："+task.Title, run.ProjectID, run.GoalID, recipe)
	repair.Status = "pending"
	if e = s.startSigningRepair(t, run, task, artifact, plan, &repair, &defect); e != nil {
		defect.Status, defect.Data["reason"] = "blocked", e.Error()
		return t.Put(defect)
	}
	defect.Status = "repairing"
	defect.Data["repairTaskId"] = repair.ID
	defect.Data["repairAttempts"] = N(defect.Data, "repairAttempts") + 1
	if e = t.Put(repair); e != nil {
		return e
	}
	return t.Put(defect)
}

func (s *Service) afterRepair(t *Tx, run, task Resource, result M) error {
	id := S(task.Data, "defectId")
	if id == "" || S(task.Data, "kind") != "code" {
		return nil
	}
	defect, e := t.Get(id)
	if e != nil {
		return e
	}
	if !Has([]string{"succeeded", "recovered"}, run.Status) {
		defect.Status = "blocked"
		defect.Data["reason"] = "开发修复没有完成，需要人工处理"
		return t.Put(defect)
	}
	if run.Status == "recovered" {
		if e = sourceRecoveryProof(t, run, task, result); e != nil {
			return e
		}
	}
	if S(task.Data, "signingRepairId") != "" {
		if e = s.validateSigningRepair(t, task); e != nil {
			return e
		}
		return s.afterSigningRepair(t, run, task, result, &defect)
	}
	testID := S(defect.Data, "failedTestTaskId")
	if testID == "" {
		testID = S(defect.Data, "testTaskId")
	}
	original, e := t.Get(testID)
	if e != nil {
		return e
	}
	artifact, e := t.Get(S(original.Data, "artifactId"))
	if e != nil {
		return e
	}
	buildRun, e := t.Get(S(artifact.Data, "runId"))
	if e != nil {
		defect.Status = "blocked"
		defect.Data["reason"] = "修复提交已生成；原制品缺少可复用的构建记录，请人工构建后验证"
		defect.Data["repairCommit"] = S(result, "commit")
		return t.Put(defect)
	}
	build, e := t.Get(S(buildRun.Data, "taskId"))
	if e != nil {
		return e
	}
	if S(build.Data, "kind") != "build" || len(Strings(build.Data["command"])) == 0 || S(build.Data, "outputPath") == "" || !CommitPattern.MatchString(S(result, "commit")) {
		defect.Status = "blocked"
		defect.Data["reason"] = "需要已批准的构建命令、输出路径及修复提交"
		return t.Put(defect)
	}
	data := copyM(build.Data)
	delete(data, "sourceBinding")
	delete(data, "sourceConflict")
	delete(data, "sourceFrom")
	logical := NewID("repairbuild")
	data["id"] = logical
	data["logicalId"] = logical
	data["commit"] = S(result, "commit")
	data["branch"] = S(result, "branch")
	data["dependsOn"] = []string{S(task.Data, "logicalId")}
	data["attempts"] = 0
	data["defectId"] = id
	rebuilt := resource("task", "重新构建："+build.Title, run.ProjectID, run.GoalID, data)
	rebuilt.Status = "pending"
	if e = t.Put(rebuilt); e != nil {
		return e
	}
	verify := copyM(original.Data)
	delete(verify, "qualityDispatchId")
	delete(verify, "sourceBinding")
	delete(verify, "sourceConflict")
	delete(verify, "sourceFrom")
	logical = NewID("reverify")
	verify["id"] = logical
	verify["logicalId"] = logical
	verify["artifactId"] = ""
	verify["commit"] = ""
	verify["requiresArtifactFrom"] = rebuilt.ID
	verify["dependsOn"] = []string{S(rebuilt.Data, "logicalId")}
	verify["attempts"] = 0
	verify["defectId"] = id
	recheck := resource("task", "重新验证："+original.Title, run.ProjectID, run.GoalID, verify)
	recheck.Status = "pending"
	if e = t.Put(recheck); e != nil {
		return e
	}
	defect.Status = "awaiting_verification"
	defect.Data["repairCommit"] = S(result, "commit")
	defect.Data["buildTaskId"] = rebuilt.ID
	defect.Data["verificationTaskId"] = recheck.ID
	return t.Put(defect)
}

func (s *Service) resolveTestArtifact(t *Tx, task *Resource) error {
	source := S(task.Data, "requiresArtifactFrom")
	if source == "" || S(task.Data, "artifactId") != "" {
		return nil
	}
	build, e := t.Get(source)
	if e != nil {
		return e
	}
	if build.Status != "succeeded" {
		return conflict("等待修复制品构建完成")
	}
	runs, e := t.List(task.ProjectID, "run", task.GoalID)
	if e != nil {
		return e
	}
	for _, run := range runs {
		if S(run.Data, "taskId") == source && run.Status == "succeeded" {
			artifact, e := t.Get(S(Obj(run.Data["result"]), "artifactId"))
			if e != nil {
				return e
			}
			if artifact.GoalID != task.GoalID || revision(artifact) != revision(*task) {
				return conflict("修复制品版本不匹配")
			}
			task.Data["artifactId"] = artifact.ID
			task.Data["commit"] = S(artifact.Data, "commit")
			task.Data["repositoryId"] = S(artifact.Data, "repositoryId")
			return t.Put(*task)
		}
	}
	return conflict("构建没有登记实际制品")
}

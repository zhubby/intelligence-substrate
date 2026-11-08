package platform

func prepareBlueprintTasks(g Resource, snapshot M, initialize bool, tasks []any) ([]any, error) {
	if !initialize {
		return tasks, nil
	}
	if len(snapshot) == 0 || len(tasks) >= 100 {
		return nil, invalid("蓝图初始化需要具体提案，并为初始化工作项保留一个位置")
	}
	runtime := ""
	repoID := S(Obj(snapshot["data"]), "repositoryId")
	for _, item := range tasks {
		task := Obj(item)
		if S(task, "id") == blueprintInitializer {
			return nil, invalid("blueprint-initialize 是蓝图初始化保留标识")
		}
		if runtime == "" && sourceTask(Resource{Data: task}) {
			runtime = S(task, "runtime")
		}
	}
	if runtime == "" {
		runtime = "linux"
	}
	out := []any{M{"id": blueprintInitializer, "title": "从已选蓝图初始化应用源码", "kind": "code", "role": "developer", "runtime": runtime,
		"repositoryId": repoID, "dependsOn": []string{}, "blueprintSeed": snapshot, "timeoutSeconds": 300}}
	for _, item := range tasks {
		task := copyM(Obj(item))
		if sourceTask(Resource{Data: task}) {
			if !Has([]string{"linux", "darwin", "windows"}, S(task, "runtime")) || S(task, "commit") != "" || (S(task, "repositoryId") != "" && S(task, "repositoryId") != repoID) {
				return nil, invalid("蓝图初始化方案不能改用其他仓库、固定旧提交或设备执行环境")
			}
			task["repositoryId"] = repoID
			deps := Strings(task["dependsOn"])
			if !Has(deps, blueprintInitializer) {
				task["dependsOn"] = append(deps, blueprintInitializer)
			}
			if _, present := task["sourceFrom"]; present {
				selected := Strings(task["sourceFrom"])
				if !Has(selected, blueprintInitializer) {
					task["sourceFrom"] = append(selected, blueprintInitializer)
				}
			}
		}
		out = append(out, task)
	}
	return out, nil
}

// validateBlueprintDependencies also checks adapted task graphs: a source task
// cannot be reordered ahead of the governed initializer.
func validateBlueprintDependencies(tasks []any) error {
	byID := map[string]M{}
	initializer := ""
	for _, item := range tasks {
		task := Obj(item)
		byID[S(task, "id")] = task
		if _, present := task["blueprintSeed"]; present {
			if initializer != "" || S(task, "id") != blueprintInitializer || S(task, "kind") != "code" || S(task, "role") != "developer" || len(Items(task["command"])) != 0 || S(task, "sourceMode") != "" || len(Items(task["dependsOn"])) != 0 || len(Items(task["sourceFrom"])) != 0 {
				return conflict("蓝图初始化任务不能替换、拆分或添加执行命令和依赖")
			}
			initializer = S(task, "id")
		}
	}
	if initializer == "" {
		return nil
	}
	var reaches func(string, map[string]bool) bool
	reaches = func(id string, seen map[string]bool) bool {
		if id == initializer {
			return true
		}
		if seen[id] {
			return false
		}
		seen[id] = true
		for _, dep := range Strings(byID[id]["dependsOn"]) {
			if reaches(dep, seen) {
				return true
			}
		}
		return false
	}
	for id, task := range byID {
		if sourceTask(Resource{Data: task}) && !reaches(id, map[string]bool{}) {
			return conflict("源码工作项必须保留对蓝图初始化的依赖")
		}
	}
	return nil
}

func (s *Service) validateBlueprintSeed(tx *Tx, task Resource) (M, error) {
	snapshot, present := task.Data["blueprintSeed"]
	if !present {
		return nil, conflict("此工作项不是蓝图初始化")
	}
	g, err := tx.Get(task.GoalID)
	if err != nil {
		return nil, err
	}
	if err = approved(tx, g); err != nil {
		return nil, err
	}
	resources, err := tx.List(task.ProjectID, "", task.GoalID)
	if err != nil {
		return nil, err
	}
	frozen, err := ApprovedBlueprint(g, S(task.Data, "planId"), resources)
	if err != nil {
		return nil, err
	}
	if len(frozen) == 0 || Hash(frozen) != Hash(snapshot) || S(task.Data, "logicalId") != blueprintInitializer || S(task.Data, "kind") != "code" || S(task.Data, "role") != "developer" || S(task.Data, "sourceMode") != "" || len(Items(task.Data["command"])) != 0 || len(Items(task.Data["dependsOn"])) != 0 || len(Items(task.Data["sourceFrom"])) != 0 {
		return nil, conflict("蓝图工作项与批准的初始化快照不一致")
	}
	plan, err := tx.Get(S(task.Data, "planId"))
	if err != nil || !B(plan.Data, "initializeBlueprint") {
		return nil, conflict("当前方案未批准蓝图初始化")
	}
	if err = s.validatePlanBlueprint(tx, g, frozen); err != nil {
		return nil, err
	}
	return frozen, nil
}

func (s *Service) validateBlueprintArchive(tx *Tx, a Actor, in M, digest string) error {
	if a.Kind != "worker" {
		return nil
	}
	run, err := tx.Get(S(in, "runId"))
	if err != nil {
		return err
	}
	task, err := tx.Get(S(run.Data, "taskId"))
	if err != nil {
		return err
	}
	if _, present := task.Data["blueprintSeed"]; !present {
		return nil
	}
	snapshot, err := s.validateBlueprintSeed(tx, task)
	if err != nil {
		return err
	}
	if S(Obj(snapshot["data"]), "digest") != digest || Hash(Obj(run.Data["task"])["blueprintSeed"]) != Hash(snapshot) {
		return conflict("初始化源码归档必须与批准蓝图逐字节一致")
	}
	return nil
}

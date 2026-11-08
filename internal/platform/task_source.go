package platform

import "sort"

func sourceTask(task Resource) bool {
	return Has([]string{"code", "build", "security"}, S(task.Data, "kind"))
}

// A binding belongs to one approved work item. Its digest protects accidental
// mutation; only the control plane may create it, so it is not a signature.
func taskSourceBinding(task Resource) (M, error) {
	b := Obj(task.Data["sourceBinding"])
	content := copyM(b)
	delete(content, "digest")
	if N(b, "version") != 1 || S(b, "digest") != Hash(content) ||
		S(b, "taskId") != task.ID || S(b, "projectId") != task.ProjectID || S(b, "goalId") != task.GoalID ||
		S(b, "planId") != S(task.Data, "planId") || N(b, "requirementRevision") != float64(revision(task)) ||
		S(b, "logicalId") != S(task.Data, "logicalId") || S(b, "kind") != S(task.Data, "kind") ||
		S(b, "sourceMode") != S(task.Data, "sourceMode") ||
		S(b, "repositoryId") != S(task.Data, "repositoryId") || S(b, "commit") != S(task.Data, "commit") ||
		S(b, "declaredBranch") != S(task.Data, "branch") || S(b, "selectionDigest") != sourceSelectionDigest(task.Data) || !sameWritePaths(b, task.Data) {
		return nil, conflict("源码绑定已变化，需要新批准的工作项")
	}
	return b, nil
}

func sourceSelectionDigest(data M) string {
	selection := M{"dependsOn": Strings(data["dependsOn"]), "sourceFrom": Strings(data["sourceFrom"])}
	// Preserve exact historical digests for tasks without this optional field.
	if paths, exists := data["writePaths"]; exists {
		selection["writePaths"] = paths
	}
	return Hash(selection)
}

// bindTaskSource runs inside the claim transaction, before reserving budget or
// consuming an attempt. Source conflicts are durable work-item state, not leases.
func (s *Service) bindTaskSource(t *Tx, a Actor, task *Resource, tasks, runs []Resource) error {
	if !sourceTask(*task) {
		return nil
	}
	binding, inputs, e := s.resolveTaskSource(t, a, *task, tasks, runs)
	if e != nil {
		if _, ok := e.(*Fault); !ok {
			return e
		}
		task.Status = "blocked"
		task.Data["reason"] = e.Error()
		task.Data["sourceConflict"] = M{"code": e.(*Fault).Code, "message": e.Error(), "inputs": inputs}
		if e = t.Put(*task); e != nil {
			return e
		}
		return t.Audit(a, "tasks.source.blocked", *task, task.Data["sourceConflict"].(M))
	}
	task.Data["sourceBinding"] = binding
	task.Data["repositoryId"] = S(binding, "repositoryId")
	task.Data["commit"] = S(binding, "commit")
	delete(task.Data, "sourceConflict")
	return nil
}

func (s *Service) resolveTaskSource(t *Tx, a Actor, task Resource, tasks, runs []Resource) (M, []any, error) {
	inputs := []any{}
	if _, e := taskWritePaths(task.Data); e != nil {
		return nil, inputs, e
	}
	if _, seed := task.Data["blueprintSeed"]; seed {
		if _, e := s.validateBlueprintSeed(t, task); e != nil {
			return nil, inputs, e
		}
	}
	if _, exists := task.Data["sourceBinding"]; exists {
		binding, e := taskSourceBinding(task)
		if e != nil {
			return nil, inputs, e
		}
		inputs = Items(binding["inputs"])
		repo, e := t.Get(S(binding, "repositoryId"))
		if e != nil || repo.Kind != "repository" || repo.ProjectID != task.ProjectID {
			return nil, inputs, conflict("绑定的源码仓库不可用")
		}
		if e = s.sourceCommitExists(repo, S(binding, "commit")); e != nil {
			return nil, inputs, e
		}
		if _, seed := task.Data["blueprintSeed"]; seed {
			if e = s.emptyBlueprintSource(repo, S(binding, "commit")); e != nil {
				return nil, inputs, e
			}
		}
		if S(task.Data, "kind") == "code" {
			if e = s.sourceTargetUnchanged(repo, binding); e != nil {
				return nil, inputs, e
			}
		}
		return binding, inputs, nil
	}

	byLogical, byID := map[string]Resource{}, map[string]Resource{}
	for _, other := range tasks {
		if other.ProjectID == task.ProjectID && other.GoalID == task.GoalID && S(other.Data, "planId") == S(task.Data, "planId") && revision(other) == revision(task) {
			logical := S(other.Data, "logicalId")
			if _, duplicate := byLogical[logical]; duplicate {
				return nil, inputs, conflict("源码依赖存在重复的工作项标识")
			}
			byLogical[logical], byID[other.ID] = other, other
		}
	}
	// Expand all ancestors for selector validation, even when a nearer code
	// result would normally hide its predecessors from the source frontier.
	reachable, visiting := map[string]bool{}, map[string]bool{}
	var ancestors func(string) error
	ancestors = func(id string) error {
		if visiting[id] {
			return conflict("源码依赖包含循环")
		}
		if reachable[id] {
			return nil
		}
		other, ok := byLogical[id]
		if !ok || other.ID == task.ID {
			return conflict("源码依赖不属于当前目标、方案和需求版本")
		}
		visiting[id] = true
		for _, dep := range Strings(other.Data["dependsOn"]) {
			if e := ancestors(dep); e != nil {
				return e
			}
		}
		visiting[id] = false
		reachable[id] = true
		return nil
	}
	for _, id := range Strings(task.Data["dependsOn"]) {
		if e := ancestors(id); e != nil {
			return nil, inputs, e
		}
	}
	selected := Strings(task.Data["dependsOn"])
	if v, ok := task.Data["sourceFrom"]; ok {
		var e error
		selected, e = sourceSelectors(v)
		if e != nil {
			return nil, inputs, e
		}
		for _, id := range selected {
			if !reachable[id] {
				return nil, inputs, conflict("sourceFrom 必须选择当前工作项的依赖祖先")
			}
		}
	}
	seen := map[string]bool{}
	var frontier func(string) error
	frontier = func(id string) error {
		if seen[id] {
			return nil
		}
		seen[id] = true
		other, ok := byLogical[id]
		if !ok || other.Status != "succeeded" {
			return conflict("源码依赖尚未成功完成")
		}
		if children := Strings(other.Data["children"]); len(children) > 0 {
			for _, childID := range children {
				child, ok := byID[childID]
				if !ok || S(child.Data, "parentId") != other.ID {
					return conflict("拆分任务缺少有效的子工作项")
				}
				if e := frontier(S(child.Data, "logicalId")); e != nil {
					return e
				}
			}
			return nil
		}
		if Has([]string{"code", "build"}, S(other.Data, "kind")) {
			input, e := s.sourceProducer(t, other, runs)
			if e != nil {
				return e
			}
			inputs = append(inputs, input)
			return nil
		}
		for _, dep := range Strings(other.Data["dependsOn"]) {
			if e := frontier(dep); e != nil {
				return e
			}
		}
		return nil
	}
	for _, id := range selected {
		if e := frontier(id); e != nil {
			return nil, inputs, e
		}
	}
	if repoID := S(task.Data, "repositoryId"); repoID != "" {
		filtered := []any{}
		for _, x := range inputs {
			if S(Obj(x), "repositoryId") == repoID {
				filtered = append(filtered, x)
			}
		}
		// A declared repository can intentionally have only non-source deps.
		// If code/build sources exist but none matches, don't guess a branch.
		if len(inputs) > 0 && len(filtered) == 0 {
			return nil, inputs, conflict("选定仓库没有匹配的依赖源码结果")
		}
		inputs = filtered
	}
	sort.Slice(inputs, func(i, j int) bool { return S(Obj(inputs[i]), "taskId") < S(Obj(inputs[j]), "taskId") })
	repoID := S(task.Data, "repositoryId")
	for _, input := range inputs {
		id := S(Obj(input), "repositoryId")
		if repoID != "" && repoID != id {
			return nil, inputs, conflict("源码依赖涉及多个仓库，请在批准方案中选择仓库")
		}
		repoID = id
	}
	if repoID == "" {
		repos, e := t.List(task.ProjectID, "repository", "")
		if e != nil {
			return nil, inputs, e
		}
		if len(repos) > 1 {
			return nil, inputs, conflict("项目包含多个源码仓库，需要在批准方案中明确选择")
		}
		if len(repos) == 1 {
			repoID = repos[0].ID
		}
		if len(repos) == 0 && S(task.Data, "kind") == "code" {
			repo, e := s.gitCommand(t, a, "repositories.create", M{"projectId": task.ProjectID, "title": "application", "description": "Source for delivery goal " + task.GoalID})
			if e != nil {
				return nil, inputs, e
			}
			if e = t.Audit(a, "repositories.create", repo, M{"taskId": task.ID}); e != nil {
				return nil, inputs, e
			}
			repoID = repo.ID
		}
	}
	repo, e := t.Get(repoID)
	if e != nil || repo.Kind != "repository" || repo.ProjectID != task.ProjectID {
		return nil, inputs, conflict("任务需要可访问的实际源码仓库")
	}
	commit := S(task.Data, "commit")
	declaredCommit := commit
	integrate := S(task.Data, "sourceMode") == "integrate"
	if integrate && (S(task.Data, "kind") != "code" || len(inputs) == 0) {
		return nil, inputs, conflict("集成任务需要成功的依赖源码结果")
	}
	if commit != "" {
		if e = s.sourceCommitExists(repo, commit); e != nil {
			return nil, inputs, e
		}
	}
	if len(inputs) > 0 && !integrate {
		containsAll := func(candidate string) bool {
			for _, input := range inputs {
				if _, err := s.Git(repo, "merge-base", "--is-ancestor", S(Obj(input), "commit"), candidate); err != nil {
					return false
				}
			}
			return true
		}
		if commit != "" {
			if !containsAll(commit) {
				return nil, inputs, conflict("指定提交尚未包含全部选定源码结果，需要先集成")
			}
		} else {
			for _, input := range inputs {
				candidate := S(Obj(input), "commit")
				if containsAll(candidate) {
					commit = candidate
					break
				}
			}
			if commit == "" {
				return nil, inputs, conflict("依赖源码尚未集成，请集成列出的提交后创建新的批准工作项")
			}
		}
	}
	branch := S(task.Data, "branch")
	target, expected := "", ""
	if S(task.Data, "kind") == "code" {
		target = branch
		if target == "" {
			target = "agent/" + task.GoalID
			if integrate {
				target = "integration/" + task.GoalID + "/" + S(task.Data, "logicalId")
			}
		}
		if !validBranch(target) || target == "main" {
			return nil, inputs, conflict("开发任务需要有效的非主分支")
		}
		expected, _ = s.Git(repo, "rev-parse", "--verify", "refs/heads/"+target)
	}
	var parents []string
	if integrate {
		parents = []string{}
		for _, input := range inputs {
			sha := S(Obj(input), "commit")
			if !Has(parents, sha) {
				parents = append(parents, sha)
			}
		}
		if commit == "" {
			commit = parents[0]
			if Has(parents, expected) {
				commit = expected
			}
		}
		// A separately declared base is also an approved input, so its history
		// must survive in the final commit rather than only in a private tree.
		if !Has(parents, commit) {
			parents = append(parents, commit)
		}
		if len(parents) > maxIntegrationParents {
			return nil, inputs, conflict("集成最多选择八个不同提交，请分阶段集成")
		}
		for _, parent := range parents {
			if _, e = s.Git(repo, "merge-base", commit, parent); e != nil {
				return nil, inputs, conflict("集成输入没有共同历史")
			}
		}
	}
	if commit == "" {
		sourceBranch := branch
		if sourceBranch == "" {
			sourceBranch = S(repo.Data, "defaultBranch")
			if sourceBranch == "" {
				sourceBranch = "main"
			}
		}
		if target != "" {
			if expected != "" {
				sourceBranch = target
			} else {
				sourceBranch = S(repo.Data, "defaultBranch")
				if sourceBranch == "" {
					sourceBranch = "main"
				}
			}
		}
		if !validBranch(sourceBranch) {
			return nil, inputs, conflict("源码分支无效")
		}
		commit, e = s.Git(repo, "rev-parse", "--verify", "refs/heads/"+sourceBranch)
		if e != nil {
			return nil, inputs, conflict("指定源码分支不存在")
		}
	}
	if e = s.sourceCommitExists(repo, commit); e != nil {
		return nil, inputs, e
	}
	if _, seed := task.Data["blueprintSeed"]; seed {
		if e = s.emptyBlueprintSource(repo, commit); e != nil {
			return nil, inputs, e
		}
	}
	if target != "" && expected != "" && expected != commit {
		return nil, inputs, conflict("开发目标分支已偏离选定源码，需要重新集成或批准工作")
	}
	binding := M{"version": 1, "taskId": task.ID, "logicalId": S(task.Data, "logicalId"), "projectId": task.ProjectID, "goalId": task.GoalID, "planId": S(task.Data, "planId"), "requirementRevision": revision(task), "kind": S(task.Data, "kind"), "repositoryId": repo.ID, "commit": commit, "declaredCommit": declaredCommit, "declaredBranch": branch, "targetBranch": target, "expectedHead": expected, "selectionDigest": sourceSelectionDigest(task.Data), "inputs": inputs}
	if paths, exists := task.Data["writePaths"]; exists {
		binding["writePaths"] = paths
	}
	if integrate {
		binding["sourceMode"], binding["integrationParents"] = "integrate", parents
	}
	binding["digest"] = Hash(binding)
	return binding, inputs, nil
}

func (s *Service) sourceCommitExists(repo Resource, commit string) error {
	if !CommitPattern.MatchString(commit) {
		return conflict("源码结果缺少完整提交")
	}
	actual, e := s.Git(repo, "rev-parse", "--verify", commit+"^{commit}")
	if e != nil || actual != commit {
		return conflict("源码提交不存在或不是完整提交")
	}
	return nil
}
func (s *Service) sourceTargetUnchanged(repo Resource, binding M) error {
	head, _ := s.Git(repo, "rev-parse", "--verify", "refs/heads/"+S(binding, "targetBranch"))
	if head != S(binding, "expectedHead") {
		return conflict("开发目标分支已变化；保留原源码绑定，需核对已有结果或重新规划")
	}
	return nil
}

func (s *Service) sourceProducer(t *Tx, task Resource, runs []Resource) (M, error) {
	var successes []Resource
	for _, run := range runs {
		if S(run.Data, "taskId") == task.ID && Has([]string{"succeeded", "recovered"}, run.Status) {
			successes = append(successes, run)
		}
	}
	if len(successes) != 1 {
		return nil, conflict("源码生产任务缺少唯一成功执行：" + S(task.Data, "logicalId"))
	}
	run := successes[0]
	snapshot := Obj(run.Data["task"])
	if run.ProjectID != task.ProjectID || run.GoalID != task.GoalID || S(run.Data, "planId") != S(task.Data, "planId") || revision(run) != revision(task) ||
		S(snapshot, "logicalId") != S(task.Data, "logicalId") || S(snapshot, "planId") != S(task.Data, "planId") || N(snapshot, "requirementRevision") != float64(revision(task)) || S(snapshot, "kind") != S(task.Data, "kind") {
		return nil, conflict("源码生产记录不属于当前工作项、方案或需求版本")
	}
	result := Obj(run.Data["result"])
	worker, e := t.Get(S(run.Data, "workerId"))
	if e != nil || worker.Kind != "worker" || worker.ProjectID != task.ProjectID {
		return nil, conflict("源码生产记录缺少可信执行身份")
	}
	if e = s.validateTaskResult(t, Actor{ID: "worker:" + worker.ID, Kind: "worker", WorkerID: worker.ID}, run, task, result); e != nil {
		return nil, e
	}
	return M{"taskId": task.ID, "logicalId": S(task.Data, "logicalId"), "runId": run.ID, "repositoryId": S(result, "repositoryId"), "commit": S(result, "commit")}, nil
}

func sourceSelectors(v any) ([]string, error) {
	switch v.(type) {
	case []any, []string:
	default:
		return nil, invalid("sourceFrom 必须为依赖工作项 ID 数组")
	}
	items := Items(v)
	if len(items) == 0 || len(items) > 100 {
		return nil, invalid("sourceFrom 需要 1–100 个依赖工作项 ID")
	}
	out, seen := []string{}, map[string]bool{}
	for _, item := range items {
		id, ok := item.(string)
		if !ok || !IDPattern.MatchString(id) || seen[id] {
			return nil, invalid("sourceFrom 包含无效或重复的工作项 ID")
		}
		seen[id] = true
		out = append(out, id)
	}
	return out, nil
}

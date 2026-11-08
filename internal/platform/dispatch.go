package platform

func (s *Service) dispatchRelease(t *Tx, a Actor, r Resource, in M) (Resource, error) {
	if e := human(a); e != nil {
		return r, e
	}
	if e := s.allowed(t, a, r.ProjectID, "release"); e != nil {
		return r, e
	}
	scope := "publish:" + r.ID
	if task, ok, e := t.Once(scope, S(in, "key"), in); e != nil || ok {
		return task, e
	}
	target := S(in, "target")
	if target == "web" {
		operations, err := t.List(r.ProjectID, "rollback", r.GoalID)
		if err != nil {
			return r, err
		}
		for _, operation := range operations {
			if Has([]string{"approved", "running", "reconciliation_required"}, operation.Status) {
				return r, conflict("已有运行回滚正在执行或等待核对")
			}
		}
	}
	var current M
	for _, x := range Items(r.Data["targets"]) {
		m := Obj(x)
		if S(m, "target") == target {
			current = m
		}
	}
	if current == nil || S(current, "status") != "ready" {
		return r, conflict("只有准备就绪的目标可以创建发布操作")
	}
	worker, e := s.scoped(t, a, S(in, "workerId"), "worker", "release")
	if e != nil {
		return r, e
	}
	if worker.ProjectID != r.ProjectID || worker.Status == "disabled" || !Has(Strings(worker.Data["roles"]), "release") || !Has(Strings(worker.Data["capabilities"]), "publish") {
		return r, invalid("需要可信发布节点")
	}
	if target == "web" {
		deployments, err := t.List(r.ProjectID, "deployment", r.GoalID)
		if err != nil {
			return r, err
		}
		for _, deployment := range deployments {
			if deployment.Status == "active" && S(deployment.Data, "workerId") != worker.ID {
				return r, conflict("现有应用必须由原运行节点发布；节点迁移需要独立运维流程")
			}
		}
	}
	tasks, e := t.List(r.ProjectID, "task", r.GoalID)
	if e != nil {
		return r, e
	}
	for _, task := range tasks {
		if S(task.Data, "releaseId") == r.ID && S(task.Data, "target") == target && Has([]string{"pending", "running", "blocked"}, task.Status) {
			return r, conflict("此目标已有发布操作，需要等待或核对")
		}
	}
	artifact, e := t.Get(S(current, "artifactId"))
	if e != nil {
		return r, e
	}
	task := resource("task", "发布 "+target, r.ProjectID, r.GoalID, M{"kind": "publish", "role": "release", "runtime": S(worker.Data, "runtime"), "assignedWorkerId": worker.ID, "releaseId": r.ID, "artifactId": artifact.ID, "artifactDigest": S(artifact.Data, "digest"), "artifactSize": N(artifact.Data, "size"), "version": S(r.Data, "version"), "repositoryId": S(artifact.Data, "repositoryId"), "target": target, "commit": S(r.Data, "commit"), "requirementRevision": revision(r), "binding": S(r.Data, "binding"), "operationKey": Hash(M{"release": r.ID, "target": target, "key": S(in, "key")}), "maxAttempts": 1, "attempts": 0, "timeoutSeconds": 1800, "budgetCents": 0})
	task.Status = "pending"
	if digest := releaseMetadataDigest(r, target); digest != "" {
		task.Data["metadataDigest"] = digest
	}
	if e = t.Put(task); e != nil {
		return task, e
	}
	return task, t.Remember(scope, S(in, "key"), in, task)
}

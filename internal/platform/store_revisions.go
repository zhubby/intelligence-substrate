package platform

// A store revision has its own approval. The release's immutable candidate
// binding, package and other channels are never rewritten.
func (s *Service) storeRevisionCommand(tx *Tx, a Actor, cmd string, in M) (Resource, error) {
	allowed := []string{"id", "expectedBinding"}
	if cmd == "store_revisions.propose" {
		allowed = []string{"issueId", "metadataArtifactId", "key"}
	}
	if cmd == "store_revisions.dispatch" {
		allowed = []string{"id", "workerId", "key"}
	}
	for key := range in {
		if !Has(allowed, key) {
			return Resource{}, invalid("资料修订包含不支持的字段：" + key)
		}
	}
	var rev, r, issue Resource
	var err error
	if cmd == "store_revisions.propose" {
		issue, err = s.scoped(tx, a, S(in, "issueId"), "release_issue", "release", "developer")
		if err != nil {
			return rev, err
		}
		r, err = tx.Get(S(Obj(issue.Data["source"]), "releaseId"))
		if err != nil {
			return rev, err
		}
	} else {
		if err = human(a); err != nil {
			return rev, err
		}
		rev, err = s.scoped(tx, a, S(in, "id"), "store_revision", "release")
		if err != nil {
			return rev, err
		}
		r, err = tx.Get(S(Obj(rev.Data["spec"]), "releaseId"))
		if err != nil {
			return rev, err
		}
		issue, err = tx.Get(S(Obj(rev.Data["spec"]), "issueId"))
		if err != nil {
			return rev, err
		}
	}
	if cmd == "store_revisions.propose" {
		if err = validateText(S(in, "key"), "幂等键", 160); err != nil {
			return rev, err
		}
		scope := "store-revision-propose:" + issue.ID + ":" + a.ID
		if old, ok, err := tx.Once(scope, S(in, "key"), in); err != nil || ok {
			return old, err
		}
		if err = s.storeRevisionSourceGate(tx, r, issue); err != nil {
			return rev, err
		}
		source := Obj(issue.Data["source"])
		target := releaseTarget(r, "ios-appstore")
		if S(target, "status") != "rejected" || S(target, "operationKey") != S(source, "operationKey") || S(target, "rejectionIssueId") != issue.ID {
			return rev, conflict("原拒审渠道已变化或已有资料修订")
		}
		g, err := tx.Get(r.GoalID)
		if err != nil {
			return rev, err
		}
		materials, err := s.releaseMaterials(tx, g, S(r.Data, "commit"), Items(r.Data["artifacts"]), []any{S(in, "metadataArtifactId")})
		if err != nil {
			return rev, err
		}
		material := Obj(materials[0])
		if S(material, "digest") == S(source, "metadataDigest") {
			return rev, conflict("资料修订必须提供新的批准资料字节")
		}
		spec := copyM(M{"releaseId": r.ID, "releaseBinding": S(r.Data, "binding"), "issueId": issue.ID, "issueBinding": S(issue.Data, "binding"), "issueRevision": issue.Revision, "triageBinding": S(issue.Data, "triageBinding"), "requirementRevision": revision(r), "planId": S(r.Data, "planId"), "target": "ios-appstore", "artifactId": S(source, "artifactId"), "artifactDigest": S(source, "artifactDigest"), "commit": S(source, "commit"), "externalId": S(source, "externalId"), "sourceOperationKey": S(source, "operationKey"), "sourcePublicationBinding": effectivePublicationBinding(r, target), "sourceMetadataDigest": S(source, "metadataDigest"), "sourceMaterialRevisionId": S(source, "materialRevisionId"), "metadataArtifactId": S(material, "id"), "metadataDigest": S(material, "digest"), "createdBy": a.ID})
		title := "App Store 文字资料修订"
		if S(Obj(issue.Data["triage"]), "strategy") == "store-media" {
			spec["materialScope"], title = "screenshots", "App Store 截图与资料修订"
		}
		rev = resource("store_revision", title, r.ProjectID, r.GoalID, M{"spec": spec, "binding": Hash(spec), "requirementRevision": revision(r)})
		rev.Status = "awaiting_approval"
		if err = tx.Put(rev); err != nil {
			return rev, err
		}
		return rev, tx.Remember(scope, S(in, "key"), in, rev)
	}
	spec := Obj(rev.Data["spec"])
	if cmd == "store_revisions.approve" {
		if err = s.storeRevisionGate(tx, rev, r); err != nil {
			return rev, err
		}
		if S(in, "expectedBinding") != S(rev.Data, "binding") {
			return rev, conflict("资料修订已变化，请重新读取")
		}
		if rev.Status == "approved" {
			return rev, nil
		}
		if rev.Status != "awaiting_approval" {
			return rev, conflict("资料修订不可审批")
		}
		material, err := tx.Get(S(spec, "metadataArtifactId"))
		if err != nil {
			return rev, err
		}
		if S(spec, "createdBy") == a.ID || S(material.Data, "producerId") == a.ID {
			return rev, conflict("资料生产者或提案人不能批准自己的修订")
		}
		rev.Status = "approved"
		rev.Data["approvedBinding"], rev.Data["approvedBy"] = S(rev.Data, "binding"), a.ID
		approval := resource("approval", "商店资料修订批准", r.ProjectID, r.GoalID, M{"type": "store-revision", "storeRevisionId": rev.ID, "releaseId": r.ID, "binding": S(rev.Data, "binding"), "requirementRevision": revision(r), "actorId": a.ID})
		if err = tx.Put(approval); err != nil {
			return rev, err
		}
		return rev, tx.Put(rev)
	}
	if err = validateText(S(in, "key"), "幂等键", 160); err != nil {
		return rev, err
	}
	scope := "store-revision-dispatch:" + rev.ID
	if old, ok, err := tx.Once(scope, S(in, "key"), in); err != nil || ok {
		return old, err
	}
	if err = s.storeRevisionGate(tx, rev, r); err != nil {
		return rev, err
	}
	if rev.Status != "approved" || S(rev.Data, "approvedBinding") != S(rev.Data, "binding") {
		return rev, conflict("需要这份新资料的独立发布批准")
	}
	target := releaseTarget(r, "ios-appstore")
	if S(target, "status") != "rejected" || S(target, "operationKey") != S(spec, "sourceOperationKey") || S(target, "rejectionIssueId") != issue.ID {
		return rev, conflict("原资料操作或拒审状态已变化")
	}
	worker, err := s.scoped(tx, a, S(in, "workerId"), "worker", "release")
	if err != nil {
		return rev, err
	}
	if worker.ID != S(target, "publisherWorkerId") || worker.Status == "disabled" || !Has(Strings(worker.Data["roles"]), "release") || !Has(Strings(worker.Data["capabilities"]), "publish") {
		return rev, conflict("资料修订必须使用持有原操作日志的可信发布节点")
	}
	tasks, err := tx.List(r.ProjectID, "task", r.GoalID)
	if err != nil {
		return rev, err
	}
	for _, task := range tasks {
		if publicationTask(task) && S(task.Data, "releaseId") == r.ID && S(task.Data, "target") == "ios-appstore" && Has([]string{"pending", "running", "blocked"}, task.Status) {
			return rev, conflict("渠道已有执行中或待核对操作")
		}
	}
	pkg, err := tx.Get(S(spec, "artifactId"))
	if err != nil {
		return rev, err
	}
	data := M{"kind": "publish_materials", "role": "release", "runtime": S(worker.Data, "runtime"), "assignedWorkerId": worker.ID, "releaseId": r.ID, "artifactId": pkg.ID, "repositoryId": S(pkg.Data, "repositoryId"), "artifactDigest": S(pkg.Data, "digest"), "artifactSize": N(pkg.Data, "size"), "target": "ios-appstore", "version": S(r.Data, "version"), "commit": S(r.Data, "commit"), "requirementRevision": revision(r), "binding": S(r.Data, "binding"), "publicationBinding": S(rev.Data, "binding"), "materialRevisionId": rev.ID, "metadataDigest": S(spec, "metadataDigest"), "sourceOperationKey": S(spec, "sourceOperationKey"), "sourcePublicationBinding": S(spec, "sourcePublicationBinding"), "sourceMetadataDigest": S(spec, "sourceMetadataDigest"), "operationKey": Hash(M{"storeRevisionId": rev.ID, "binding": S(rev.Data, "binding")}), "externalId": S(spec, "externalId"), "maxAttempts": 1, "attempts": 0, "timeoutSeconds": 900, "budgetCents": 0}
	if scope := S(spec, "materialScope"); scope != "" {
		data["materialScope"] = scope
	}
	task := resource("task", "修订 App Store 文字资料", r.ProjectID, r.GoalID, data)
	task.Status = "pending"
	for _, key := range []string{"publicationBinding", "materialRevisionId", "metadataDigest", "sourceOperationKey", "sourcePublicationBinding", "sourceMetadataDigest", "operationKey"} {
		target[key] = data[key]
	}
	delete(target, "materialScope")
	if scope := S(spec, "materialScope"); scope != "" {
		target["materialScope"] = scope
	}
	target["continueRequired"], target["nextCheckAt"] = false, ""
	rev.Status = "dispatched"
	rev.Data["taskId"] = task.ID
	if err = tx.Put(task); err != nil {
		return rev, err
	}
	if err = tx.Put(rev); err != nil {
		return rev, err
	}
	if err = tx.Put(r); err != nil {
		return rev, err
	}
	return task, tx.Remember(scope, S(in, "key"), in, task)
}

func effectivePublicationBinding(r Resource, target M) string {
	if value := S(target, "publicationBinding"); value != "" {
		return value
	}
	return S(r.Data, "binding")
}

func (s *Service) storeRevisionSourceGate(tx *Tx, r, issue Resource) error {
	if err := s.validateReleaseIssueSource(tx, issue); err != nil {
		return err
	}
	triage := Obj(issue.Data["triage"])
	if issue.Status == "resolved" || !Has([]string{"store-text", "store-media"}, S(triage, "strategy")) || S(issue.Data, "triageBinding") != Hash(triage) || int(N(triage, "revision")) != issue.Revision || int(N(triage, "requirementRevision")) != revision(r) {
		return conflict("需要当前拒审问题的人工资料修复决定")
	}
	if !Has([]string{"approved", "publishing", "blocked"}, r.Status) || S(r.Data, "approvedBinding") != S(r.Data, "binding") {
		return conflict("原候选发布批准不可用")
	}
	return s.releaseGate(tx, r)
}

func (s *Service) storeRevisionGate(tx *Tx, rev, r Resource) error {
	spec := Obj(rev.Data["spec"])
	if rev.Kind != "store_revision" || rev.ProjectID != r.ProjectID || rev.GoalID != r.GoalID || S(rev.Data, "binding") != Hash(spec) || S(spec, "releaseBinding") != S(r.Data, "binding") || S(spec, "releaseId") != r.ID || S(spec, "planId") != S(r.Data, "planId") || revision(rev) != revision(r) {
		return conflict("资料修订来源绑定无效")
	}
	issue, err := tx.Get(S(spec, "issueId"))
	if err != nil {
		return err
	}
	if S(spec, "issueBinding") != S(issue.Data, "binding") || int(N(spec, "issueRevision")) != issue.Revision || S(spec, "triageBinding") != S(issue.Data, "triageBinding") {
		return conflict("拒审修复要求已变化，需要重新提案和批准资料")
	}
	if err = s.storeRevisionSourceGate(tx, r, issue); err != nil {
		return err
	}
	expectedScope := ""
	if S(Obj(issue.Data["triage"]), "strategy") == "store-media" {
		expectedScope = "screenshots"
	}
	if S(spec, "materialScope") != expectedScope {
		return conflict("资料修订范围与人工批准的修复决定不一致")
	}
	g, err := tx.Get(r.GoalID)
	if err != nil {
		return err
	}
	materials, err := s.releaseMaterials(tx, g, S(r.Data, "commit"), Items(r.Data["artifacts"]), []any{S(spec, "metadataArtifactId")})
	if err != nil {
		return err
	}
	if S(Obj(materials[0]), "digest") != S(spec, "metadataDigest") {
		return conflict("资料清单字节已变化")
	}
	return nil
}

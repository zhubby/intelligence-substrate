package platform

func copyPublicationFields(target, data M) {
	for _, key := range []string{"publicationBinding", "materialRevisionId", "metadataDigest", "sourceOperationKey", "sourcePublicationBinding", "sourceMetadataDigest", "materialScope"} {
		if value := S(target, key); value != "" {
			data[key] = value
		}
	}
}

func publicationEpochMatches(r Resource, target M, task Resource) bool {
	if S(target, "materialRevisionId") != S(task.Data, "materialRevisionId") {
		return false
	}
	if S(target, "materialRevisionId") == "" {
		return true
	}
	if effectivePublicationBinding(r, target) != S(task.Data, "publicationBinding") || S(target, "operationKey") != S(task.Data, "operationKey") {
		return false
	}
	for _, key := range []string{"metadataDigest", "sourceOperationKey", "sourcePublicationBinding", "sourceMetadataDigest", "materialScope"} {
		if S(target, key) != S(task.Data, key) {
			return false
		}
	}
	return true
}

func (s *Service) storeRevisionTaskGate(tx *Tx, r, task Resource) error {
	target := releaseTarget(r, S(task.Data, "target"))
	if !publicationEpochMatches(r, target, task) {
		return conflict("渠道资料批准已变化，旧任务不能覆盖新资料")
	}
	if S(task.Data, "materialRevisionId") == "" {
		return nil
	}
	if !Has([]string{"publish_materials", "publish_status"}, S(task.Data, "kind")) && !(S(task.Data, "kind") == "publish_continue" && S(task.Data, "materialScope") == "screenshots") {
		return conflict("资料修订不能复用普通发布或继续提审")
	}
	rev, err := tx.Get(S(task.Data, "materialRevisionId"))
	if err != nil {
		return err
	}
	if rev.Status != "dispatched" || S(rev.Data, "approvedBinding") != S(rev.Data, "binding") || S(rev.Data, "binding") != S(task.Data, "publicationBinding") || S(rev.Data, "taskId") == "" {
		return conflict("资料修订缺少当前批准与派发记录")
	}
	if S(Obj(rev.Data["spec"]), "materialScope") != S(task.Data, "materialScope") {
		return conflict("工作项的资料范围与独立批准不一致")
	}
	return s.storeRevisionGate(tx, rev, r)
}

func (s *Service) resolveStoreRevisions(tx *Tx, r Resource, target string, ev Resource) error {
	id := S(releaseTarget(r, target), "materialRevisionId")
	seen := map[string]bool{}
	for id != "" {
		if seen[id] || len(seen) > 100 {
			return conflict("资料修订来源链无效")
		}
		seen[id] = true
		rev, err := tx.Get(id)
		if err != nil {
			return err
		}
		spec := Obj(rev.Data["spec"])
		if rev.ProjectID != r.ProjectID || rev.GoalID != r.GoalID || S(spec, "releaseId") != r.ID || S(rev.Data, "binding") != Hash(spec) || S(rev.Data, "approvedBinding") != S(rev.Data, "binding") {
			return conflict("资料修订链缺少准确批准")
		}
		issue, err := tx.Get(S(spec, "issueId"))
		if err != nil {
			return err
		}
		if issue.Status != "resolved" {
			if S(issue.Data, "binding") != S(spec, "issueBinding") || issue.Revision != int(N(spec, "issueRevision")) || S(issue.Data, "triageBinding") != S(spec, "triageBinding") {
				return conflict("资料修复要求已变化，旧修订不能解决问题")
			}
			issue.Status = "resolved"
			issue.Data["resolution"] = M{"releaseId": r.ID, "releaseBinding": S(r.Data, "binding"), "materialRevisionId": S(releaseTarget(r, target), "materialRevisionId"), "publicationBinding": S(ev.Data, "publicationBinding"), "metadataDigest": S(ev.Data, "metadataDigest"), "artifactId": S(spec, "artifactId"), "artifactDigest": S(spec, "artifactDigest"), "commit": S(spec, "commit"), "evidenceId": ev.ID, "externalId": S(ev.Data, "externalId"), "actorId": S(ev.Data, "actorId"), "at": now()}
			if err = tx.Put(issue); err != nil {
				return err
			}
			if err = tx.Audit(Actor{ID: S(ev.Data, "actorId")}, "release_issues.resolved", issue, Obj(issue.Data["resolution"])); err != nil {
				return err
			}
		}
		rev.Status = "verified"
		rev.Data["evidenceId"] = ev.ID
		if err = tx.Put(rev); err != nil {
			return err
		}
		id = S(spec, "sourceMaterialRevisionId")
	}
	return nil
}

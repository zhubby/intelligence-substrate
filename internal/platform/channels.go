package platform

import "time"

func releaseTarget(r Resource, target string) M {
	for _, value := range Items(r.Data["targets"]) {
		m := Obj(value)
		if S(m, "target") == target {
			return m
		}
	}
	return nil
}

func publicationTask(task Resource) bool {
	return Has([]string{"publish", "publish_status", "publish_continue", "publish_materials"}, S(task.Data, "kind"))
}

func mutatingPublicationTask(task Resource) bool {
	return Has([]string{"publish", "publish_continue", "publish_materials"}, S(task.Data, "kind"))
}

func waitingChannel(state string) bool {
	return state == "processing" || state == "awaiting_review" || state == "awaiting_release"
}

// A status query is a short read-only job. Review waiting never holds a lease.
func (s *Service) refreshRelease(t *Tx, a Actor, r Resource, in M) (Resource, error) {
	if err := human(a); err != nil {
		return r, err
	}
	if err := s.allowed(t, a, r.ProjectID, "release"); err != nil {
		return r, err
	}
	scope := "channel-query:" + r.ID
	if previous, ok, err := t.Once(scope, S(in, "key"), in); err != nil || ok {
		return previous, err
	}
	target := releaseTarget(r, S(in, "target"))
	if target == nil || !Has([]string{"processing", "awaiting_review", "awaiting_release", "published"}, S(target, "status")) || S(target, "externalId") == "" {
		return r, conflict("只有已知外部版本的待审或已发布目标可以查询；未知发布结果仍须人工核对")
	}
	workerID := S(in, "workerId")
	if workerID == "" {
		workerID = S(target, "publisherWorkerId")
	}
	worker, err := s.scoped(t, a, workerID, "worker", "release")
	if err != nil {
		return r, err
	}
	if worker.ProjectID != r.ProjectID || worker.Status == "disabled" || !Has(Strings(worker.Data["roles"]), "release") || !Has(Strings(worker.Data["capabilities"]), "publish") {
		return r, invalid("需要可信发布节点查询渠道")
	}
	if waitingChannel(S(target, "status")) && S(target, "pollUntil") <= now() {
		target["pollUntil"] = time.Now().Add(30 * 24 * time.Hour).UTC().Format(time.RFC3339Nano)
		if err = t.Put(r); err != nil {
			return r, err
		}
	}
	query, err := s.enqueueChannelQuery(t, r, target, worker)
	if err != nil {
		return r, err
	}
	return query, t.Remember(scope, S(in, "key"), in, query)
}

func (s *Service) enqueueChannelQuery(t *Tx, r Resource, target M, worker Resource) (Resource, error) {
	tasks, err := t.List(r.ProjectID, "task", r.GoalID)
	if err != nil {
		return r, err
	}
	for _, task := range tasks {
		if publicationTask(task) && S(task.Data, "releaseId") == r.ID && S(task.Data, "target") == S(target, "target") && Has([]string{"pending", "running"}, task.Status) {
			if S(task.Data, "kind") == "publish_status" {
				return task, nil
			}
			return r, conflict("发布操作仍在进行，暂不能查询")
		}
	}
	artifact, err := t.Get(S(target, "artifactId"))
	if err != nil {
		return r, err
	}
	data := M{"kind": "publish_status", "role": "release", "runtime": S(worker.Data, "runtime"), "assignedWorkerId": worker.ID, "releaseId": r.ID, "artifactId": artifact.ID, "repositoryId": S(artifact.Data, "repositoryId"), "artifactDigest": S(artifact.Data, "digest"), "artifactSize": N(artifact.Data, "size"), "target": S(target, "target"), "version": S(r.Data, "version"), "commit": S(r.Data, "commit"), "requirementRevision": revision(r), "binding": S(r.Data, "binding"), "operationKey": S(target, "operationKey"), "externalId": S(target, "externalId"), "expectedState": S(target, "status"), "maxAttempts": 3, "attempts": 0, "timeoutSeconds": 120, "budgetCents": 0}
	query := resource("task", "查询渠道 "+S(target, "target"), r.ProjectID, r.GoalID, data)
	copyPublicationFields(target, query.Data)
	if digest := releaseMetadataDigest(r, S(target, "target")); digest != "" {
		query.Data["metadataDigest"] = digest
	}
	query.Status = "pending"
	if err = t.Put(query); err != nil {
		return query, err
	}
	target["nextCheckAt"] = ""
	return query, t.Put(r)
}

func (s *Service) cancelChannelQueries(t *Tx, r Resource, target string) error {
	tasks, err := t.List(r.ProjectID, "task", r.GoalID)
	if err != nil {
		return err
	}
	for _, task := range tasks {
		if !Has([]string{"publish_status", "publish_continue", "publish_materials"}, S(task.Data, "kind")) || S(task.Data, "releaseId") != r.ID || S(task.Data, "target") != target || !Has([]string{"pending", "running", "blocked"}, task.Status) {
			continue
		}
		task.Status = "cancelled"
		if err = t.Put(task); err != nil {
			return err
		}
		runs, err := t.List(r.ProjectID, "run", r.GoalID)
		if err != nil {
			return err
		}
		for _, run := range runs {
			if S(run.Data, "taskId") == task.ID && run.Status == "running" {
				run.Status = "cancelled"
				delete(run.Data, "leaseToken")
				if err = t.Put(run); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (s *Service) scheduleChannelQueries(t *Tx, a Actor, worker Resource) error {
	if !Has(Strings(worker.Data["roles"]), "release") || !Has(Strings(worker.Data["capabilities"]), "publish") {
		return nil
	}
	releases, err := t.List(worker.ProjectID, "release", "")
	if err != nil {
		return err
	}
	for _, r := range releases {
		if !Has([]string{"publishing", "blocked"}, r.Status) || S(r.Data, "approvedBinding") != S(r.Data, "binding") {
			continue
		}
		g, err := t.Get(r.GoalID)
		if err != nil {
			return err
		}
		if checkRevision(g, r) != nil || approved(t, g) != nil || S(g.Data, "activePlanId") != S(r.Data, "planId") {
			continue
		}
		for _, value := range Items(r.Data["targets"]) {
			target := Obj(value)
			if !waitingChannel(S(target, "status")) || S(target, "publisherWorkerId") != worker.ID {
				continue
			}
			if B(target, "continueRequired") && S(target, "status") == "processing" {
				if s.releaseGate(t, r) != nil {
					continue
				}
				if err = s.enqueueChannelContinuation(t, r, target, worker); err != nil {
					return err
				}
				continue
			}
			if S(target, "nextCheckAt") == "" || S(target, "nextCheckAt") > now() {
				continue
			}
			if S(target, "pollUntil") <= now() {
				target["nextCheckAt"] = ""
				target["queryError"] = "自动查询已达 30 天上限，请人工检查渠道并按需继续查询"
				if err = t.Put(r); err != nil {
					return err
				}
				continue
			}
			query, err := s.enqueueChannelQuery(t, r, target, worker)
			if err != nil {
				return err
			}
			if err = t.Audit(a, "channels.query.scheduled", query, M{"releaseId": r.ID, "target": S(target, "target")}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) recordChannelQuery(t *Tx, a Actor, run, task Resource, succeeded bool, result M) error {
	r, err := t.Get(S(task.Data, "releaseId"))
	if err != nil {
		return err
	}
	target := releaseTarget(r, S(task.Data, "target"))
	if target == nil || S(r.Data, "binding") != S(task.Data, "binding") || !publicationEpochMatches(r, target, task) || S(target, "externalId") != S(task.Data, "externalId") || S(target, "status") != S(task.Data, "expectedState") {
		return conflict("渠道版本或状态已变化，旧查询不能覆盖新的结论")
	}
	target["lastCheckedAt"] = now()
	target["nextCheckAt"] = ""
	if succeeded {
		if S(result, "externalId") != S(task.Data, "externalId") || !Has([]string{"processing", "awaiting_review", "awaiting_release", "published", "rejected", "halted", "unchanged"}, S(result, "state")) {
			return invalid("渠道查询必须返回相同外部版本和明确状态，不能替代独立验收")
		}
		if len(S(result, "reason")) > 2000 || len(S(result, "upstreamState")) > 200 {
			return invalid("渠道响应超出长度限制")
		}
		if S(result, "state") != "unchanged" && S(target, "status") != S(result, "state") {
			if err = s.setTarget(t, &r, S(target, "target"), S(result, "state"), S(result, "externalId"), S(result, "reason"), a.ID); err != nil {
				return err
			}
		}
		target["queryError"] = ""
		target["queryFailures"] = 0
		target["upstreamState"] = S(result, "upstreamState")
		target["queryNote"] = S(result, "reason")
		if B(result, "continueRequired") && (S(result, "state") != "processing" || S(target, "status") != "processing") {
			return invalid("只有处理中的准确版本可以继续提审")
		}
		target["continueRequired"] = B(result, "continueRequired")
	} else {
		target["queryFailures"] = N(target, "queryFailures") + 1
		target["queryError"] = "渠道查询失败，保留上次已知状态；详情见执行记录"
	}
	if waitingChannel(S(target, "status")) && S(target, "pollUntil") > now() && S(target, "upstreamState") != "MANUAL_VERIFICATION_REQUIRED" {
		delay := 5 * time.Minute
		if N(target, "queryFailures") > 0 {
			delay = 30 * time.Minute
		}
		target["nextCheckAt"] = time.Now().Add(delay).UTC().Format(time.RFC3339Nano)
	}
	target["lastQueryRunId"] = run.ID
	return t.Put(r)
}

// Continuation is a separate bounded mutation under the original release approval.
// Only a trusted observation of the exact uploaded operation can request it.
func (s *Service) enqueueChannelContinuation(t *Tx, r Resource, target M, worker Resource) error {
	tasks, err := t.List(r.ProjectID, "task", r.GoalID)
	if err != nil {
		return err
	}
	for _, task := range tasks {
		if publicationTask(task) && S(task.Data, "releaseId") == r.ID && S(task.Data, "target") == S(target, "target") && Has([]string{"pending", "running", "blocked"}, task.Status) {
			return nil
		}
	}
	artifact, err := t.Get(S(target, "artifactId"))
	if err != nil {
		return err
	}
	data := M{"kind": "publish_continue", "role": "release", "runtime": S(worker.Data, "runtime"), "assignedWorkerId": worker.ID, "releaseId": r.ID, "artifactId": artifact.ID, "repositoryId": S(artifact.Data, "repositoryId"), "artifactDigest": S(artifact.Data, "digest"), "artifactSize": N(artifact.Data, "size"), "target": S(target, "target"), "version": S(r.Data, "version"), "commit": S(r.Data, "commit"), "requirementRevision": revision(r), "binding": S(r.Data, "binding"), "operationKey": S(target, "operationKey"), "externalId": S(target, "externalId"), "expectedState": "processing", "maxAttempts": 1, "attempts": 0, "timeoutSeconds": 900, "budgetCents": 0}
	task := resource("task", "继续渠道提审 "+S(target, "target"), r.ProjectID, r.GoalID, data)
	copyPublicationFields(target, task.Data)
	if digest := releaseMetadataDigest(r, S(target, "target")); digest != "" {
		task.Data["metadataDigest"] = digest
	}
	task.Status = "pending"
	if err = t.Put(task); err != nil {
		return err
	}
	target["continueRequired"] = false
	target["nextCheckAt"] = ""
	return t.Put(r)
}

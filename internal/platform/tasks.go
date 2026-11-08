package platform

import (
	"fmt"
	"net/url"
	"time"
)

func (s *Service) taskCommand(t *Tx, a Actor, cmd string, in M) (Resource, error) {
	if cmd == "workers.disable" {
		w, e := s.scoped(t, a, S(in, "id"), "worker", "owner")
		if e != nil {
			return w, e
		}
		if e = human(a); e != nil {
			return w, e
		}
		w.Status = "disabled"
		return w, t.Put(w)
	}
	if cmd == "workers.register" || cmd == "workers.update" {
		if e := human(a); e != nil {
			return Resource{}, e
		}
		p := S(in, "projectId")
		if e := s.allowed(t, a, p, "owner"); e != nil {
			return Resource{}, e
		}
		roles := Strings(in["roles"])
		if len(roles) == 0 {
			return Resource{}, invalid("必须指定执行角色")
		}
		for _, role := range roles {
			if !Has(AgentRoles, role) {
				return Resource{}, invalid("执行角色无效")
			}
		}
		capabilities := Strings(in["capabilities"])
		if len(capabilities) == 0 {
			return Resource{}, invalid("至少指定一种任务能力")
		}
		for _, capability := range capabilities {
			if !Has([]string{"analysis", "design", "code", "build", "test", "security", "publish"}, capability) {
				return Resource{}, invalid("未知节点能力")
			}
		}
		if B(in, "trustedQuality") {
			for _, role := range roles {
				if !Has([]string{"quality", "security"}, role) {
					return Resource{}, invalid("独立质量身份不能兼任开发或发布角色")
				}
			}
			for _, c := range capabilities {
				if !Has([]string{"test", "security"}, c) {
					return Resource{}, invalid("独立质量节点只能运行验证能力")
				}
			}
		}
		runtime := S(in, "runtime")
		if !Has([]string{"linux", "darwin", "windows", "device"}, runtime) {
			return Resource{}, invalid("执行节点环境无效")
		}
		if e := validateText(S(in, "title"), "节点名称", 120); e != nil {
			return Resource{}, e
		}
		r := resource("worker", S(in, "title"), p, "", M{"roles": roles, "runtime": runtime, "capabilities": Strings(in["capabilities"]), "trustedQuality": B(in, "trustedQuality"), "registeredBy": a.ID, "lastSeen": ""})
		r.Status = "offline"
		if cmd == "workers.update" {
			old, e := s.scoped(t, a, S(in, "id"), "worker", "owner")
			if e != nil {
				return old, e
			}
			r.ID = old.ID
			r.Revision = old.Revision + 1
			r.CreatedAt = old.CreatedAt
		}
		return r, t.Put(r)
	}
	if cmd == "tasks.cancel" || cmd == "tasks.retry" {
		r, e := s.scoped(t, a, S(in, "id"), "task")
		if e != nil {
			return r, e
		}
		roles := []string{"architect", "developer"}
		if S(r.Data, "qualityPolicyId") != "" {
			roles = []string{"quality"}
		}
		if e = s.allowed(t, a, r.ProjectID, roles...); e != nil {
			return r, e
		}
		if e = human(a); e != nil {
			return r, e
		}
		if cmd == "tasks.retry" {
			if e = s.AgentProgressRunGate(t, r.ProjectID, r.GoalID, ""); e != nil {
				return r, e
			}
			if e = s.AgentProgressGate(t, r); e != nil {
				return r, e
			}
			if e = s.SandboxRunGate(t, r.ProjectID, r.GoalID, ""); e != nil {
				return r, e
			}
			if controlledSigningTask(r) {
				return r, conflict("签名执行需要先核对持久意图；不能直接重试，请批准新的工作项")
			}
			if S(r.Data, "qualityPolicyId") != "" {
				if e = s.validateQualityTask(t, r); e != nil {
					return r, e
				}
			}
			if ops, err := s.unsettledTaskSources(t, r); err != nil {
				return r, err
			} else if len(ops) > 0 {
				return r, conflict("源码候选需要负责人核对，不能直接重试")
			}
			if !Has([]string{"failed", "blocked"}, r.Status) {
				return r, conflict("只能重试失败或阻塞工作项")
			}
			if publicationTask(r) || operationalTask(r) {
				return r, conflict("发布结果需先核对，不能直接重试")
			}
			if N(r.Data, "attempts") >= N(r.Data, "maxAttempts") {
				return r, conflict("已达到重试次数，需要重新规划")
			}
			g, e := t.Get(r.GoalID)
			if e != nil {
				return r, e
			}
			if e = checkRevision(g, r); e != nil {
				return r, e
			}
			if governedAgentTask(r) {
				if _, e = agentTaskAllowance(t, r, ""); e != nil {
					return r, e
				}
			}
			r.Status = "pending"
			if len(Strings(r.Data["children"])) > 0 {
				r.Status = "waiting"
			}
		} else {
			if publicationTask(r) || operationalTask(r) {
				return r, conflict("发布操作必须核对外部结果后处理")
			}
			if Has([]string{"succeeded", "cancelled", "stale"}, r.Status) {
				return r, conflict("此工作项已结束")
			}
			if e = s.cancelTaskTree(t, r); e != nil {
				return r, e
			}
			r.Status = "cancelled"
		}
		return r, t.Put(r)
	}
	if a.Kind != "worker" || a.WorkerID == "" {
		return Resource{}, denied()
	}
	worker, e := s.scoped(t, a, a.WorkerID, "worker")
	if e != nil {
		return worker, e
	}
	if worker.Status == "disabled" || a.ID != "worker:"+worker.ID {
		return Resource{}, denied()
	}
	worker.Data["lastSeen"] = now()
	worker.Status = "online"
	if e = t.Put(worker); e != nil {
		return worker, e
	}
	if cmd == "tasks.claim" {
		return s.claimTask(t, a, worker)
	}
	run, e := s.scoped(t, a, S(in, "id"), "run")
	if e != nil {
		return run, e
	}
	if S(run.Data, "workerId") != worker.ID {
		return run, denied()
	}
	if S(run.Data, "sourceRecoveryId") != "" {
		return run, conflict("此执行已由负责人核对，原租约不能继续操作")
	}
	digest := Hash(in)
	if cmd == "tasks.complete" && S(run.Data, "completionDigest") != "" {
		if S(run.Data, "completionDigest") == digest {
			return run, nil
		}
		return run, conflict("执行已经以不同结果完成")
	}
	if run.Status != "running" || S(in, "leaseToken") != S(run.Data, "leaseToken") || S(run.Data, "leaseUntil") < now() {
		return run, conflict("执行租约已失效")
	}
	task, e := t.Get(S(run.Data, "taskId"))
	if e != nil {
		return run, e
	}
	if !Has(Strings(worker.Data["roles"]), S(task.Data, "role")) {
		return run, denied()
	}
	if S(task.Data, "qualityPolicyId") != "" && (!B(worker.Data, "trustedQuality") || !Has(Strings(worker.Data["roles"]), "quality") || !Has(Strings(worker.Data["capabilities"]), "test") || S(worker.Data, "runtime") != S(task.Data, "runtime")) {
		return run, denied()
	}
	g, e := t.Get(run.GoalID)
	if e != nil {
		return run, e
	}
	if e = checkRevision(g, run); e != nil && !operationalTask(task) {
		return run, e
	}
	if e = s.validateSigningRepair(t, task); e != nil {
		return run, e
	}
	if S(task.Data, "signingRepairId") != "" && signingRepairRecipe(Obj(run.Data["task"])) != signingRepairRecipe(task.Data) {
		return run, conflict("签名修复执行配方与领取时不一致")
	}
	if cmd == "tasks.progress" {
		message := S(in, "message")
		if len(message) > 4000 {
			return run, invalid("进度信息过长")
		}
		run.Data["progress"] = message
		lease := time.Now().Add(90 * time.Second)
		deadline, e := time.Parse(time.RFC3339Nano, S(run.Data, "deadline"))
		if e != nil {
			return run, e
		}
		if time.Now().After(deadline) {
			run.Status = "failed"
			task.Status = "failed"
			task.Data["reason"] = "执行超时"
			if controlledSigningTask(task) {
				run.Status, task.Status = "blocked", "blocked"
				task.Data["reason"] = "签名执行超时，需要核对可信程序的持久意图"
				recordMacOSSigningTime(&task, run)
			}
			if operationalTask(task) {
				task.Status = "blocked"
				if e = s.rollbackUncertain(t, task); e != nil {
					return run, e
				}
			}
			if S(task.Data, "kind") == "publish_status" {
				if err := s.recordChannelQuery(t, a, run, task, false, M{}); err != nil {
					return run, err
				}
			}
			if mutatingPublicationTask(task) {
				task.Status = "blocked"
				task.Data["reason"] = "发布执行超时，需要核对外部结果"
				release, err := t.Get(S(task.Data, "releaseId"))
				if err != nil {
					return run, err
				}
				if err = s.setTarget(t, &release, S(task.Data, "target"), "reconciliation_required", S(task.Data, "externalId"), S(task.Data, "reason"), a.ID); err != nil {
					return run, err
				}
				if err = t.Put(release); err != nil {
					return run, err
				}
			}
			delete(run.Data, "leaseToken")
			if _, e = s.blockUnsettledSource(t, &task); e != nil {
				return run, e
			}
			if e = t.Put(task); e != nil {
				return run, e
			}
			return run, t.Put(run)
		}
		if lease.After(deadline) {
			lease = deadline
		}
		run.Data["leaseUntil"] = lease.UTC().Format(time.RFC3339Nano)
		return run, t.Put(run)
	}
	if e = s.validateQualityTask(t, task); e != nil {
		return run, e
	}
	if controlledSigningTask(task) {
		if _, e = s.validateSigningRun(t, run, task); e != nil {
			return run, e
		}
	}
	if _, ok := in["exitCode"]; !ok {
		return run, invalid("需要实际退出码")
	}
	if N(in, "exitCode") == 0 {
		if e = s.AgentProgressGate(t, task); e != nil {
			return run, e
		}
		if e = s.SandboxRunGate(t, run.ProjectID, run.GoalID, run.ID); e != nil {
			return run, e
		}
	}
	if e = noSecrets(in["result"]); e != nil {
		return run, e
	}
	result := Obj(in["result"])
	var qualityReport Resource
	if S(task.Data, "kind") == "test" {
		artifact, err := t.Get(S(task.Data, "artifactId"))
		if err != nil {
			return run, err
		}
		if err = s.validateSigningArtifact(t, artifact); err != nil {
			return run, err
		}
		pipeline, err := t.Get(S(task.Data, "pipelineId"))
		if err != nil {
			return run, conflict("独立验证缺少受保护的测试策略")
		}
		required := N(in, "exitCode") == 0 && !B(result, "blocked") && S(pipeline.Data, "reportPath") != ""
		qualityReport, err = s.validateQualityReport(t, a, run, task, result, required)
		if err != nil {
			return run, err
		}
	}
	run.Data["result"] = result
	run.Data["exitCode"] = N(in, "exitCode")
	run.Data["completionDigest"] = digest
	run.Data["completedAt"] = now()
	if N(in, "exitCode") == 0 {
		run.Status = "succeeded"
		task.Status = "succeeded"
	} else {
		run.Status = "failed"
		task.Status = "failed"
	}
	if B(result, "blocked") {
		run.Status = "blocked"
		task.Status = "blocked"
		task.Data["reason"] = S(result, "reason")
	}
	if controlledSigningTask(task) && run.Status == "failed" {
		run.Status, task.Status = "blocked", "blocked"
		task.Data["reason"] = "签名未确认完成，需要核对可信程序的持久意图"
	}
	if macOSSigningTask(task) {
		recordMacOSSigningTime(&task, run)
	}
	if _, exists := result["signingPending"]; exists {
		if run.Status != "succeeded" {
			return run, invalid("失败或阻塞执行不能声明公证等待")
		}
		if e = s.waitMacOSSignature(t, &run, &task, result); e != nil {
			return run, e
		}
	}
	if run.Status == "succeeded" {
		if e = s.validateTaskResult(t, a, run, task, result); e != nil {
			return run, e
		}
	}
	if log := S(in, "log"); len(log) > 128000 {
		return run, invalid("执行日志超过 128 KB")
	} else {
		run.Data["log"] = log
	}
	if run.Status == "succeeded" && S(task.Data, "kind") == "test" && B(worker.Data, "trustedQuality") {
		pipeline, e := t.Get(S(task.Data, "pipelineId"))
		if e != nil {
			return run, conflict("独立验证缺少受保护的测试策略")
		}
		if pipeline.Kind != "pipeline" || pipeline.ProjectID != run.ProjectID {
			return run, denied()
		}
		if pipeline.Revision != int(N(task.Data, "pipelineRevision")) || S(pipeline.Data, "runtime") != S(worker.Data, "runtime") {
			return run, conflict("验证策略或执行环境已变化")
		}
		artifact, e := t.Get(S(task.Data, "artifactId"))
		if e != nil {
			return run, conflict("独立验证必须绑定实际制品")
		}
		if artifact.Kind != "artifact" || artifact.GoalID != run.GoalID || revision(artifact) != g.Revision {
			return run, conflict("测试制品绑定失效")
		}
		if S(artifact.Data, "producerId") == a.ID || S(artifact.Data, "type") != "package" {
			return run, conflict("质量节点不能验证自己生产的制品")
		}
		if e = s.VerifyArtifact(artifact); e != nil {
			return run, e
		}
		ev := resource("evidence", task.Title, run.ProjectID, run.GoalID, M{"requirementRevision": g.Revision, "runId": run.ID, "workerId": worker.ID, "pipelineId": pipeline.ID, "pipelineRevision": pipeline.Revision, "artifactId": artifact.ID, "digest": S(artifact.Data, "digest"), "commit": S(artifact.Data, "commit"), "target": S(artifact.Data, "target"), "criteria": Strings(task.Data["criteria"]), "method": S(pipeline.Data, "method"), "source": "trusted-worker", "actorId": a.ID, "result": "passed"})
		ev.Status = "passed"
		ev.Data["verificationEnvironment"] = S(pipeline.Data, "verificationEnvironment")
		ev.Data["runtime"] = S(worker.Data, "runtime")
		if qualityReport.ID != "" {
			ev.Data["proofArtifactId"] = qualityReport.ID
			ev.Data["proofDigest"] = S(qualityReport.Data, "digest")
		}
		if S(task.Data, "qualityPolicyId") != "" {
			for _, key := range []string{"qualityPolicyId", "qualityPolicyDigest", "qualityDispatchId"} {
				ev.Data[key] = task.Data[key]
			}
		}
		if e = t.Put(ev); e != nil {
			return run, e
		}
		run.Data["evidenceId"] = ev.ID
	}
	if mutatingPublicationTask(task) {
		release, e := t.Get(S(task.Data, "releaseId"))
		if e != nil {
			return run, e
		}
		target := S(task.Data, "target")
		if run.Status == "succeeded" {
			external := S(result, "externalId")
			if external == "" {
				return run, invalid("发布操作必须返回外部操作标识")
			}
			state := S(result, "state")
			if !Has([]string{"processing", "awaiting_review", "published"}, state) {
				return run, invalid("发布节点必须明确返回实际渠道状态")
			}
			if S(task.Data, "kind") == "publish_materials" && state == "processing" && S(task.Data, "materialScope") != "screenshots" {
				return run, invalid("只有批准的截图资料修订可以等待图片处理")
			}
			if Has([]string{"publish_continue", "publish_materials"}, S(task.Data, "kind")) && external != S(task.Data, "externalId") {
				return run, invalid("继续提审必须返回原上传的准确版本")
			}
			if S(releaseTarget(release, target), "status") != state {
				if e = s.setTarget(t, &release, target, state, external, S(result, "reason"), a.ID); e != nil {
					return run, e
				}
			}
			current := releaseTarget(release, target)
			if link := S(result, "url"); link != "" {
				u, err := url.Parse(link)
				if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || len(link) > 2000 {
					return run, invalid("分发地址必须为不含凭据的 HTTPS URL")
				}
				current["publicationUrl"] = link
			}
			current["publisherWorkerId"] = worker.ID
			current["operationKey"] = S(task.Data, "operationKey")
			current["continueRequired"] = false
			current["upstreamState"] = S(result, "upstreamState")
			if waitingChannel(state) {
				current["nextCheckAt"] = time.Now().Add(5 * time.Minute).UTC().Format(time.RFC3339Nano)
				current["pollUntil"] = time.Now().Add(30 * 24 * time.Hour).UTC().Format(time.RFC3339Nano)
			}
		} else {
			external := S(result, "externalId")
			if external == "" {
				external = S(task.Data, "externalId")
			}
			if e = s.setTarget(t, &release, target, "reconciliation_required", external, "发布执行失败，需要核对外部结果", a.ID); e != nil {
				return run, e
			}
		}
		if e = t.Put(release); e != nil {
			return run, e
		}
		if run.Status == "succeeded" {
			if e = s.recordRuntimeDeployment(t, a, release, result); e != nil {
				return run, e
			}
		}
	}
	if operationalTask(task) {
		if e = s.finishRuntimeTask(t, task, run.Status == "succeeded", result); e != nil {
			return run, e
		}
	}
	if S(task.Data, "kind") == "publish_status" {
		if e = s.recordChannelQuery(t, a, run, task, run.Status == "succeeded", result); e != nil {
			return run, e
		}
	}
	delete(run.Data, "leaseToken")
	if run.Status != "succeeded" {
		if _, e = s.blockUnsettledSource(t, &task); e != nil {
			return run, e
		}
	}
	if e = t.Put(task); e != nil {
		return run, e
	}
	if e = s.recordTestOutcome(t, run, task); e != nil {
		return run, e
	}
	if e = s.afterRepair(t, run, task, result); e != nil {
		return run, e
	}
	if e = s.aggregateChildren(t, run.ProjectID); e != nil {
		return run, e
	}
	return run, t.Put(run)
}

func (s *Service) validateTaskResult(t *Tx, a Actor, run, task Resource, result M) error {
	if e := s.validateSigningRepair(t, task); e != nil {
		return e
	}
	if run.Status == "recovered" {
		if e := sourceRecoveryProof(t, run, task, result); e != nil {
			return e
		}
	}
	var sourceBinding M
	if _, bound := task.Data["sourceBinding"]; bound {
		var e error
		sourceBinding, e = taskSourceBinding(task)
		if e != nil || Hash(Obj(run.Data["task"])["sourceBinding"]) != Hash(sourceBinding) {
			return conflict("执行结果的源码绑定与领取时不一致")
		}
		if S(task.Data, "kind") == "security" && (S(result, "repositoryId") != S(sourceBinding, "repositoryId") || S(result, "commit") != S(sourceBinding, "commit")) {
			return conflict("安全验证结果必须使用领取时绑定的源码")
		}
	}
	if mutatingPublicationTask(task) {
		r, err := t.Get(S(task.Data, "releaseId"))
		if err != nil {
			return err
		}
		current := releaseTarget(r, S(task.Data, "target"))
		expected := "submitting"
		if S(task.Data, "kind") == "publish_continue" {
			expected = "processing"
		}
		if S(task.Data, "kind") == "publish_materials" {
			expected = "revising_materials"
		}
		if current == nil || S(current, "status") != expected || S(r.Data, "binding") != S(task.Data, "binding") || S(r.Data, "approvedBinding") != S(task.Data, "binding") {
			return conflict("发布审批或渠道状态已变化，需要核对外部结果")
		}
		if err = s.releaseGate(t, r); err != nil {
			return err
		}
		if err = s.storeRevisionTaskGate(t, r, task); err != nil {
			return err
		}
	}
	if S(task.Data, "kind") == "code" {
		if _, seed := task.Data["blueprintSeed"]; seed {
			snapshot, err := s.validateBlueprintSeed(t, task)
			if err != nil {
				return err
			}
			op, err := t.Get(S(result, "sourceOperationId"))
			if err != nil || op.Kind != "source_commit" || op.Status != "committed" || op.ProjectID != task.ProjectID || op.GoalID != task.GoalID || S(op.Data, "runId") != run.ID || S(op.Data, "commit") != S(result, "commit") || S(op.Data, "digest") != S(Obj(snapshot["data"]), "digest") {
				return conflict("蓝图完成需要当前执行提交的完整批准源码归档")
			}
		}
		commit := S(result, "commit")
		repo, e := t.Get(S(result, "repositoryId"))
		if e != nil || repo.Kind != "repository" || repo.ProjectID != run.ProjectID || !CommitPattern.MatchString(commit) {
			return conflict("开发完成需要实际仓库与完整提交")
		}
		if expected := S(task.Data, "repositoryId"); expected != "" && repo.ID != expected {
			return denied()
		}
		if _, e = s.Git(repo, "cat-file", "-e", commit+"^{commit}"); e != nil {
			return e
		}
		if len(sourceBinding) > 0 {
			if _, e = s.Git(repo, "merge-base", "--is-ancestor", S(sourceBinding, "commit"), commit); e != nil {
				return conflict("开发结果必须包含领取时绑定的源码")
			}
			if S(sourceBinding, "sourceMode") == "integrate" {
				for _, parent := range Strings(sourceBinding["integrationParents"]) {
					if _, e = s.Git(repo, "merge-base", "--is-ancestor", parent, commit); e != nil {
						return conflict("集成结果必须包含所有选定输入")
					}
				}
				op, err := t.Get(S(result, "sourceOperationId"))
				if err != nil || op.Kind != "source_commit" || op.Status != "committed" || op.ProjectID != run.ProjectID || op.GoalID != run.GoalID ||
					S(op.Data, "runId") != run.ID || S(op.Data, "repositoryId") != repo.ID || S(op.Data, "commit") != commit ||
					S(op.Data, "integrationId") != S(run.Data, "integrationId") || S(op.Data, "resolutionDigest") != S(run.Data, "integrationResolutionDigest") ||
					Hash(op.Data["parents"]) != Hash(sourceBinding["integrationParents"]) {
					return conflict("集成完成需要当前执行确认的源码快照")
				}
			}
		}
		changes, e := t.List(run.ProjectID, "change", "")
		if e != nil {
			return e
		}
		for _, change := range changes {
			if change.Status == "prepared" {
				continue
			}
			if S(change.Data, "repositoryId") == repo.ID && S(change.Data, "commit") == commit && S(change.Data, "authorId") == a.ID && (len(sourceBinding) == 0 || S(change.Data, "runId") == run.ID) {
				return nil
			}
		}
		return conflict("开发提交必须来自当前可信开发身份")
	}
	if S(task.Data, "kind") == "build" {
		if len(sourceBinding) > 0 && (S(result, "commit") != S(sourceBinding, "commit") || S(result, "repositoryId") != S(sourceBinding, "repositoryId")) {
			return conflict("构建结果必须使用领取时绑定的源码")
		}
		artifact, e := t.Get(S(result, "artifactId"))
		artifactType := "package"
		if S(task.Data, "artifactStage") == "unsigned" {
			artifactType = "unsigned-package"
		}
		if e != nil || artifact.Kind != "artifact" || artifact.ProjectID != run.ProjectID || artifact.GoalID != run.GoalID || revision(artifact) != revision(run) || S(artifact.Data, "runId") != run.ID || S(artifact.Data, "type") != artifactType || S(artifact.Data, "producerId") != a.ID {
			return conflict("构建完成需要当前执行生产的实际应用制品")
		}
		if S(artifact.Data, "commit") != S(result, "commit") || S(artifact.Data, "repositoryId") != S(result, "repositoryId") || S(artifact.Data, "target") != S(task.Data, "target") {
			return conflict("构建结果与制品绑定不一致")
		}
		if controlledSigningTask(task) {
			if e = s.validateSignedResult(t, run, task, artifact, result); e != nil {
				return e
			}
		}
		return s.VerifyArtifact(artifact)
	}
	return nil
}

func (s *Service) claimTask(t *Tx, a Actor, worker Resource) (Resource, error) {
	if e := s.refreshMacOSSignatures(t, worker.ProjectID); e != nil {
		return Resource{}, e
	}
	if e := s.refreshMessageWaits(t, worker.ProjectID); e != nil {
		return Resource{}, e
	}
	if e := s.aggregateChildren(t, worker.ProjectID); e != nil {
		return Resource{}, e
	}
	tasks, e := t.List(worker.ProjectID, "task", "")
	if e != nil {
		return Resource{}, e
	}
	runs, e := t.List(worker.ProjectID, "run", "")
	if e != nil {
		return Resource{}, e
	}
	for _, run := range runs {
		if run.Status != "running" {
			continue
		}
		if S(run.Data, "leaseUntil") >= now() {
			if S(run.Data, "workerId") == worker.ID {
				return Resource{}, conflict("节点已有正在执行的工作项")
			}
			continue
		}
		task, e := t.Get(S(run.Data, "taskId"))
		if e != nil {
			return run, e
		}
		run.Status = "expired"
		delete(run.Data, "leaseToken")
		task.Status = "pending"
		if controlledSigningTask(task) {
			task.Status = "blocked"
			task.Data["reason"] = "签名租约中断，需要核对可信程序的持久意图，不能自动重签"
		}
		if operationalTask(task) {
			task.Status = "blocked"
			if e = s.rollbackUncertain(t, task); e != nil {
				return run, e
			}
		}
		if mutatingPublicationTask(task) {
			task.Status = "blocked"
			task.Data["reason"] = "发布租约中断，外部结果需要核对"
			release, e := t.Get(S(task.Data, "releaseId"))
			if e == nil {
				_ = s.setTarget(t, &release, S(task.Data, "target"), "reconciliation_required", S(task.Data, "externalId"), "执行中断，不能自动重试", a.ID)
				if e = t.Put(release); e != nil {
					return run, e
				}
			}
		}
		if N(task.Data, "attempts") >= N(task.Data, "maxAttempts") {
			task.Status = "blocked"
			task.Data["reason"] = "达到重试上限"
			if S(task.Data, "kind") == "publish_status" {
				if e = s.recordChannelQuery(t, a, run, task, false, M{}); e != nil {
					return run, e
				}
			}
		}
		if _, e = s.blockUnsettledSource(t, &task); e != nil {
			return run, e
		}
		if sandboxErr := s.SandboxRunGate(t, task.ProjectID, task.GoalID, ""); sandboxErr != nil {
			task.Status, task.Data["reason"] = "blocked", sandboxErr.Error()
		}
		if progressErr := s.AgentProgressGate(t, task); progressErr != nil {
			task.Status, task.Data["reason"] = "blocked", progressErr.Error()
		}
		if e = t.Put(task); e != nil {
			return run, e
		}
		if e = t.Put(run); e != nil {
			return run, e
		}
	}
	if e = s.scheduleChannelQueries(t, a, worker); e != nil {
		return Resource{}, e
	}
	if e = s.scheduleQuality(t, a, worker); e != nil {
		return Resource{}, e
	}
	tasks, e = t.List(worker.ProjectID, "task", "")
	if e != nil {
		return Resource{}, e
	}
	runs, e = t.List(worker.ProjectID, "run", "")
	if e != nil {
		return Resource{}, e
	}
	for _, task := range tasks {
		if signingOnlyWorker(worker) && !controlledSigningTask(task) {
			continue
		}
		if task.Status != "pending" || !Has(Strings(worker.Data["roles"]), S(task.Data, "role")) || S(task.Data, "runtime") != S(worker.Data, "runtime") {
			continue
		}
		capability := S(task.Data, "kind")
		if capability == "publish_status" || capability == "publish_continue" || capability == "publish_materials" || operationalTask(task) {
			capability = "publish"
		}
		if !Has(Strings(worker.Data["capabilities"]), capability) {
			continue
		}
		if assigned := S(task.Data, "assignedWorkerId"); assigned != "" && assigned != worker.ID {
			continue
		}
		if controlledSigningTask(task) && !signingWorker(worker, task) {
			continue
		}
		g, e := t.Get(task.GoalID)
		if e != nil {
			return task, e
		}
		if progressErr := s.AgentProgressRunGate(t, task.ProjectID, task.GoalID, ""); progressErr != nil {
			task.Status, task.Data["reason"] = "blocked", progressErr.Error()
			if e = t.Put(task); e != nil {
				return task, e
			}
			continue
		}
		if e = s.validateSigningRepair(t, task); e != nil {
			task.Status, task.Data["reason"] = "blocked", e.Error()
			if e = t.Put(task); e != nil {
				return task, e
			}
			continue
		}
		if !operationalTask(task) && (checkRevision(g, task) != nil || approved(t, g) != nil) {
			continue
		}
		if !publicationTask(task) && !operationalTask(task) && S(task.Data, "planId") != S(g.Data, "activePlanId") {
			continue
		}
		if sandboxErr := s.SandboxRunGate(t, task.ProjectID, task.GoalID, ""); sandboxErr != nil {
			task.Data["reason"] = sandboxErr.Error()
			if e = t.Put(task); e != nil {
				return task, e
			}
			continue
		}
		ready := true
		for _, dep := range Strings(task.Data["dependsOn"]) {
			found := false
			for _, other := range tasks {
				if S(other.Data, "logicalId") == dep && S(other.Data, "planId") == S(task.Data, "planId") && other.Status == "succeeded" {
					found = true
				}
			}
			if !found {
				ready = false
				break
			}
		}
		if !ready {
			continue
		}
		if S(task.Data, "kind") == "test" {
			if e = s.resolveTestArtifact(t, &task); e != nil {
				task.Status = "blocked"
				task.Data["reason"] = e.Error()
				if e = t.Put(task); e != nil {
					return task, e
				}
				continue
			}
			if e = s.validateQualityTask(t, task); e != nil {
				task.Status = "blocked"
				task.Data["reason"] = e.Error()
				if e = t.Put(task); e != nil {
					return task, e
				}
				continue
			}
			pipeline, err := t.Get(S(task.Data, "pipelineId"))
			if err != nil || pipeline.Kind != "pipeline" || pipeline.ProjectID != task.ProjectID || !B(worker.Data, "trustedQuality") || pipeline.Revision != int(N(task.Data, "pipelineRevision")) || S(pipeline.Data, "runtime") != S(worker.Data, "runtime") {
				task.Data["reason"] = "需要独立质量节点和受保护测试策略"
				if e = t.Put(task); e != nil {
					return task, e
				}
				continue
			}
		}
		if publicationTask(task) {
			release, err := t.Get(S(task.Data, "releaseId"))
			if err != nil || !Has([]string{"approved", "publishing", "blocked"}, release.Status) || S(release.Data, "approvedBinding") != S(release.Data, "binding") {
				continue
			}
			if mutatingPublicationTask(task) {
				current := releaseTarget(release, S(task.Data, "target"))
				expected := "ready"
				if S(task.Data, "kind") == "publish_continue" {
					expected = "processing"
				}
				if S(task.Data, "kind") == "publish_materials" {
					expected = "rejected"
				}
				if current == nil || S(current, "status") != expected || (expected == "processing" && (S(current, "externalId") != S(task.Data, "externalId") || S(current, "operationKey") != S(task.Data, "operationKey"))) {
					continue
				}
			}
			if mutatingPublicationTask(task) && s.releaseGate(t, release) != nil {
				continue
			}
			if mutatingPublicationTask(task) && s.storeRevisionTaskGate(t, release, task) != nil {
				continue
			}
			if S(task.Data, "kind") == "publish_status" {
				current := releaseTarget(release, S(task.Data, "target"))
				if current == nil || !publicationEpochMatches(release, current, task) || S(current, "externalId") != S(task.Data, "externalId") || S(current, "status") != S(task.Data, "expectedState") {
					task.Status = "cancelled"
					if err = t.Put(task); err != nil {
						return task, err
					}
					continue
				}
			}
		}
		if operationalTask(task) && !s.validRuntimeTask(t, task, worker) {
			continue
		}
		budget := N(task.Data, "budgetCents")
		timeout := N(task.Data, "timeoutSeconds")
		if timeout <= 0 || timeout > 86400 {
			timeout = 900
		}
		remaining := time.Duration(timeout) * time.Second
		if macOSSigningTask(task) {
			if S(task.Data, "signingDeadline") == "" {
				task.Data["signingDeadline"] = time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339Nano)
			}
			remaining, e = macOSSigningAllowance(task)
			if e != nil {
				task.Status, task.Data["reason"] = "blocked", e.Error()
				if e = t.Put(task); e != nil {
					return task, e
				}
				continue
			}
		}
		var continuation Resource
		if S(task.Data, "messageWaitId") != "" {
			continuation, e = t.Get(S(task.Data, "messageWaitId"))
			if e != nil {
				return task, e
			}
			if e = s.validMessageWait(t, continuation, task); e == nil {
				budget, remaining, e = messageTaskAllowance(t, task, "")
			}
			if e != nil || continuation.Status == "waiting" {
				task.Status = "blocked"
				task.Data["reason"] = "协作继续条件无效，需要人工重新规划"
				if e != nil {
					task.Data["reason"] = e.Error()
				}
				if e = t.Put(task); e != nil {
					return task, e
				}
				continue
			}
		}
		var agentBudget agentAllowance
		if governedAgentTask(task) {
			agentBudget, e = agentTaskAllowance(t, task, "")
			if e != nil {
				task.Status, task.Data["reason"] = "blocked", e.Error()
				if e = t.Put(task); e != nil {
					return task, e
				}
				continue
			}
			budget, remaining = agentBudget.budget, agentBudget.remaining
		}
		reserved := float64(0)
		for _, r := range runs {
			if r.GoalID == g.ID && r.Status == "running" {
				reserved += N(r.Data, "reservedCents")
			}
		}
		if S(task.Data, "kind") != "publish_status" && !operationalTask(task) && N(g.Data, "spentCents")+reserved+budget > N(g.Data, "budgetCents") {
			task.Status = "blocked"
			// No external action has started; keep the continuation claimable
			// after other reservations are released, without permitting generic retry.
			if Has([]string{"publish_continue", "publish_materials"}, S(task.Data, "kind")) {
				task.Status = "pending"
			}
			task.Data["reason"] = "目标预算不足，需要人工调整"
			if e = t.Put(task); e != nil {
				return task, e
			}
			continue
		}
		if blocked, err := s.blockUnsettledSource(t, &task); err != nil {
			return task, err
		} else if blocked {
			continue
		}
		if e = s.bindTaskSource(t, a, &task, tasks, runs); e != nil {
			return task, e
		}
		if task.Status == "blocked" {
			continue
		}
		resume, resumeErr := s.agentResume(t, task)
		if resumeErr != nil {
			task.Status, task.Data["reason"] = "blocked", resumeErr.Error()
			if e = t.Put(task); e != nil {
				return task, e
			}
			continue
		}
		if e = s.bindSigningInput(t, &task); e != nil {
			task.Status, task.Data["reason"] = "blocked", e.Error()
			if e = t.Put(task); e != nil {
				return task, e
			}
			continue
		}
		deadline := time.Now().Add(remaining)
		lease := time.Now().Add(90 * time.Second)
		if lease.After(deadline) {
			lease = deadline
		}
		run := resource("run", task.Title, task.ProjectID, task.GoalID, M{"taskId": task.ID, "workerId": worker.ID, "requirementRevision": g.Revision, "planId": S(task.Data, "planId"), "leaseToken": Secret(), "leaseUntil": lease.UTC().Format(time.RFC3339Nano), "deadline": deadline.UTC().Format(time.RFC3339Nano), "reservedCents": budget, "spentCents": 0, "task": copyM(task.Data)})
		if governedAgentTask(task) {
			run.Data["agentAllowance"] = agentBudget.snapshot(task)
			run.Data["agentProgressVersion"] = 1
			if resume != nil {
				run.Data["agentResume"] = resume
			}
		}
		run.Status = "running"
		task.Status = "running"
		if continuation.ID != "" {
			data := M{"waitId": continuation.ID, "priorWaitStatus": continuation.Status}
			for _, key := range []string{"summary", "source", "requestMessageId", "replyMessageId", "reason", "checkpointId", "checkpointDigest"} {
				data[key] = continuation.Data[key]
			}
			run.Data["messageContinuation"] = data
			if e = s.continueWorkspace(t, continuation, task, &run); e != nil {
				return task, e
			}
		}
		if resume != nil {
			if e = s.continueSavedWorkspace(t, Obj(resume["integration"]), task, &run); e != nil {
				return task, e
			}
		}
		if continuation.Status == "ready" {
			continuation.Status, continuation.Data["resumedRunId"], continuation.Data["resumedAt"] = "resumed", run.ID, now()
			if e = t.Put(continuation); e != nil {
				return run, e
			}
		} else if macOSSigningTask(task) && S(task.Data, "signingPhase") == "status" {
			task.Data["signingPolls"] = N(task.Data, "signingPolls") + 1
		} else {
			task.Data["attempts"] = N(task.Data, "attempts") + 1
		}
		task.Data["reason"] = ""
		if operationalTask(task) {
			rollback, err := t.Get(S(task.Data, "rollbackId"))
			if err != nil {
				return run, err
			}
			rollback.Status = "running"
			if err = t.Put(rollback); err != nil {
				return run, err
			}
		}
		if e = t.Put(task); e != nil {
			return run, e
		}
		if Has([]string{"publish", "publish_materials"}, S(task.Data, "kind")) {
			release, e := t.Get(S(task.Data, "releaseId"))
			if e != nil {
				return run, e
			}
			state, external := "submitting", ""
			if S(task.Data, "kind") == "publish_materials" {
				state, external = "revising_materials", S(task.Data, "externalId")
			}
			if e = s.setTarget(t, &release, S(task.Data, "target"), state, external, "发布节点开始执行", a.ID); e != nil {
				return run, e
			}
			if e = t.Put(release); e != nil {
				return run, e
			}
		}
		return run, t.Put(run)
	}
	worker.Data["idle"] = true
	worker.Data["message"] = fmt.Sprintf("%s 暂无可执行工作项", worker.Title)
	return worker, nil
}

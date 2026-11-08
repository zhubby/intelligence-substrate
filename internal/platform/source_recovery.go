package platform

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"time"
)

type sourceRecoveryContext struct {
	op, repo, change, run, task, worker Resource
}

// The caller has already scoped the operation. Related resources must still be
// checked: a recovery must never manufacture provenance from unrelated records.
func (s *Service) sourceRecoveryContext(t *Tx, op Resource) (c sourceRecoveryContext, err error) {
	c.op = op
	c.repo, err = t.Get(S(op.Data, "repositoryId"))
	if err != nil || c.repo.Kind != "repository" || c.repo.ProjectID != op.ProjectID {
		return c, conflict("源码仓库与原操作不一致")
	}
	c.change, err = t.Get(S(op.Data, "changeId"))
	if err != nil || c.change.Kind != "change" || c.change.ProjectID != op.ProjectID || c.change.GoalID != op.GoalID ||
		S(c.change.Data, "sourceOperationId") != op.ID || S(c.change.Data, "repositoryId") != c.repo.ID ||
		S(c.change.Data, "commit") != S(op.Data, "commit") || S(c.change.Data, "authorId") != S(op.Data, "authorId") ||
		S(c.change.Data, "runId") != S(op.Data, "runId") || c.change.Status != op.Status {
		return c, conflict("源码操作缺少一致的原生产者记录")
	}
	if S(op.Data, "runId") == "" {
		if op.GoalID != "" || strings.HasPrefix(S(op.Data, "authorId"), "worker:") {
			return c, conflict("源码操作缺少原执行记录")
		}
		return c, nil
	}
	c.run, err = t.Get(S(op.Data, "runId"))
	if err != nil || c.run.Kind != "run" || c.run.ProjectID != op.ProjectID || c.run.GoalID != op.GoalID ||
		S(op.Data, "authorId") != "worker:"+S(c.run.Data, "workerId") || revision(op) != revision(c.run) {
		return c, conflict("源码候选不属于原执行身份或需求版本")
	}
	c.worker, err = t.Get(S(c.run.Data, "workerId"))
	if err != nil || c.worker.Kind != "worker" || c.worker.ProjectID != op.ProjectID {
		return c, conflict("源码候选缺少原可信执行节点身份")
	}
	c.task, err = t.Get(S(c.run.Data, "taskId"))
	if err != nil || c.task.Kind != "task" || c.task.ProjectID != op.ProjectID || c.task.GoalID != op.GoalID || S(c.task.Data, "kind") != "code" {
		return c, conflict("源码候选缺少原开发工作项")
	}
	return c, nil
}

func (s *Service) recoveryGit(ctx context.Context, repo Resource, args ...string) (string, error) {
	b, e := s.rawGit(ctx, repo, "", nil, append([]string{"--no-replace-objects"}, args...)...)
	return strings.TrimSpace(string(b)), e
}

func (s *Service) sourceRecoveryHead(ctx context.Context, c sourceRecoveryContext) (string, error) {
	branch := S(c.op.Data, "branch")
	if !validBranch(branch) || branch == "main" || !CommitPattern.MatchString(S(c.op.Data, "commit")) {
		return "", conflict("原源码候选或工作分支无效")
	}
	head, e := s.recoveryGit(ctx, c.repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	var exit *exec.ExitError
	if errors.As(e, &exit) && exit.ExitCode() == 1 {
		return "", nil // Missing branch is distinct from an unreadable repository.
	}
	return head, e
}

func activeSourceLease(run Resource) bool {
	if run.Status != "running" {
		return false
	}
	until, e := time.Parse(time.RFC3339Nano, S(run.Data, "leaseUntil"))
	// An invalid lease is not evidence that a running execution has stopped.
	return e != nil || !time.Now().After(until)
}

func (s *Service) sourceRecoveryIdle(t *Tx, c sourceRecoveryContext) error {
	if !Has([]string{"prepared", "committed"}, c.op.Status) || S(c.op.Data, "sourceRecoveryId") != "" {
		return conflict("源码操作已有决定或不需要核对")
	}
	if c.run.ID == "" {
		if c.op.Status != "prepared" {
			return conflict("人工源码操作已经完成")
		}
		return nil
	}
	if !Has([]string{"running", "expired", "failed", "blocked", "stale", "cancelled"}, c.run.Status) || activeSourceLease(c.run) {
		return conflict("原执行仍持有效租约或已正常完成")
	}
	runs, e := t.List(c.op.ProjectID, "run", c.op.GoalID)
	if e != nil {
		return e
	}
	for _, run := range runs {
		if S(run.Data, "taskId") != c.task.ID || run.ID == c.run.ID {
			continue
		}
		if activeSourceLease(run) || Has([]string{"succeeded", "recovered"}, run.Status) {
			return conflict("工作项另有有效租约或已采用的结果")
		}
	}
	return nil
}

func (s *Service) sourceRecoveryConfirmable(ctx context.Context, t *Tx, c sourceRecoveryContext, head string) error {
	if e := s.sourceRecoveryIdle(t, c); e != nil {
		return e
	}
	candidate := S(c.op.Data, "commit")
	retained, e := s.recoveryGit(ctx, c.repo, "rev-parse", "--verify", "refs/intelligence/source/"+c.op.ID)
	if e != nil || retained != candidate {
		return conflict("控制面保留的候选引用已变化，需要人工调查")
	}
	if head == "" {
		return conflict("目标分支不存在，不能确认候选已落地")
	}
	if _, e = s.recoveryGit(ctx, c.repo, "merge-base", "--is-ancestor", candidate, head); e != nil {
		return conflict("当前分支不包含原候选，不能确认；请刷新核对或停止采用")
	}
	parents := []string{S(c.op.Data, "baseCommit")}
	if c.run.ID != "" {
		if !Has([]string{"running", "pending", "failed", "blocked"}, c.task.Status) {
			return conflict("原工作项已取消、过期或完成，需要新批准的工作")
		}
		g, e := t.Get(c.op.GoalID)
		if e != nil {
			return e
		}
		if e = approved(t, g); e != nil {
			return e
		}
		if e = checkRevision(g, c.run); e != nil {
			return e
		}
		if e = checkRevision(g, c.task); e != nil {
			return e
		}
		plan, e := t.Get(S(c.task.Data, "planId"))
		if e != nil || plan.Kind != "plan" || plan.Status != "active" || S(plan.Data, "approvedBy") == "" || plan.ProjectID != c.op.ProjectID || plan.GoalID != g.ID ||
			S(g.Data, "activePlanId") != plan.ID || S(c.run.Data, "planId") != plan.ID || revision(plan) != g.Revision {
			return conflict("原批准方案已变化，需要新批准的工作")
		}
		binding, e := taskSourceBinding(c.task)
		if e != nil {
			return e
		}
		if Hash(Obj(c.run.Data["task"])["sourceBinding"]) != Hash(binding) || !sameWritePaths(c.task.Data, Obj(c.run.Data["task"])) ||
			S(binding, "repositoryId") != c.repo.ID || S(binding, "targetBranch") != S(c.op.Data, "branch") ||
			S(binding, "commit") != S(c.op.Data, "baseCommit") || S(binding, "expectedHead") != S(c.op.Data, "expectedHead") {
			return conflict("原候选与领取时固定的源码绑定不一致")
		}
		ops, e := s.unsettledTaskSources(t, c.task)
		if e != nil {
			return e
		}
		for _, op := range ops {
			if op.ID != c.op.ID {
				return conflict("原工作项还有其他候选，请先逐项核对")
			}
		}
		if S(binding, "sourceMode") == "integrate" {
			if _, e = integrationPreparation(t, c.run, binding); e != nil {
				return e
			}
			if S(c.op.Data, "integrationId") != S(c.run.Data, "integrationId") ||
				S(c.op.Data, "resolutionDigest") != S(c.run.Data, "integrationResolutionDigest") || S(c.op.Data, "resolutionDigest") != Hash(Obj(c.run.Data["integrationResolutions"])) ||
				Hash(c.op.Data["parents"]) != Hash(binding["integrationParents"]) {
				return conflict("集成候选与原冲突决定或父输入不一致")
			}
			parents = Strings(binding["integrationParents"])
		} else if S(c.op.Data, "integrationId") != "" {
			return conflict("普通源码候选包含意外集成状态")
		}
		if e = s.sourceWriteScope(ctx, c.repo, c.task.Data, S(binding, "commit"), candidate); e != nil {
			return e
		}
	}
	actual, e := s.recoveryGit(ctx, c.repo, "show", "-s", "--format=%P", candidate)
	if e != nil || Hash(strings.Fields(actual)) != Hash(parents) {
		return conflict("候选提交的父历史与批准输入不一致")
	}
	return nil
}

// InspectSourceOperation is read-only and deliberately returns no run/lease body.
// Owners still re-submit the observed head; an inspection does not authorize a
// later mutation after the branch, plan, requirement or execution changes.
func (s *Service) InspectSourceOperation(ctx context.Context, a Actor, id string) (result M, err error) {
	err = s.Store.Update(ctx, func(t *Tx) error {
		op, e := s.scoped(t, a, id, "source_commit")
		if e != nil {
			return e
		}
		c, e := s.sourceRecoveryContext(t, op)
		if e != nil {
			return e
		}
		head, e := s.sourceRecoveryHead(ctx, c)
		if e != nil {
			return e
		}
		result = M{"operation": op, "observedHead": head, "runStatus": c.run.Status, "workerStatus": c.worker.Status, "taskId": c.task.ID, "canConfirm": false, "canAbandon": false}
		if recoveryID := S(op.Data, "sourceRecoveryId"); recoveryID != "" {
			decision, e := t.Get(recoveryID)
			if e != nil {
				return e
			}
			result["recovery"] = decision
			result["reason"] = "此源码操作已记录人工决定"
			return nil
		}
		permission := human(a)
		if permission == nil {
			permission = s.allowed(t, a, op.ProjectID, "owner")
		}
		if permission != nil {
			result["reason"] = "仅项目负责人可以核对并决定采用源码"
			return nil
		}
		idle := s.sourceRecoveryIdle(t, c)
		result["canAbandon"] = idle == nil
		if e = s.sourceRecoveryConfirmable(ctx, t, c, head); e != nil {
			result["reason"] = e.Error()
		} else {
			result["canConfirm"] = true
			result["reason"] = "可确认原候选；后续仍需独立构建与质量验证"
		}
		return nil
	})
	return result, err
}

func (s *Service) reconcileSource(ctx context.Context, t *Tx, a Actor, in M) (Resource, error) {
	if e := human(a); e != nil {
		return Resource{}, e
	}
	op, e := s.scoped(t, a, S(in, "id"), "source_commit", "owner")
	if e != nil {
		return Resource{}, e
	}
	for key, value := range in {
		if !Has([]string{"id", "key", "commit", "observedHead", "decision", "reason"}, key) {
			return Resource{}, invalid("源码核对包含未知字段")
		}
		if _, ok := value.(string); !ok {
			return Resource{}, invalid("源码核对参数必须为字符串")
		}
	}
	if len(in) != 6 || !IDPattern.MatchString(S(in, "key")) || !CommitPattern.MatchString(S(in, "commit")) ||
		(S(in, "observedHead") != "" && !CommitPattern.MatchString(S(in, "observedHead"))) || !Has([]string{"confirm", "abandon"}, S(in, "decision")) {
		return Resource{}, invalid("需要有效幂等键、完整候选与观察提交、决定和原因")
	}
	if e = validateText(S(in, "reason"), "核对原因", 2000); e != nil {
		return Resource{}, e
	}
	scope := "source-recovery:" + a.ID + ":" + op.ID
	previous, found, e := t.Once(scope, S(in, "key"), in)
	if e != nil || found {
		return previous, e
	}
	c, e := s.sourceRecoveryContext(t, op)
	if e != nil {
		return Resource{}, e
	}
	if S(op.Data, "commit") != S(in, "commit") {
		return Resource{}, conflict("核对候选与原操作不一致")
	}
	if e = s.sourceRecoveryIdle(t, c); e != nil {
		return Resource{}, e
	}
	head, e := s.sourceRecoveryHead(ctx, c)
	if e != nil {
		return Resource{}, e
	}
	if head != S(in, "observedHead") {
		return Resource{}, conflict("目标分支已变化，请刷新核对后重新决定")
	}
	if S(in, "decision") == "confirm" {
		if e = s.sourceRecoveryConfirmable(ctx, t, c, head); e != nil {
			return Resource{}, e
		}
	}
	decision := resource("source_recovery", op.Title, op.ProjectID, op.GoalID, M{
		"sourceOperationId": op.ID, "repositoryId": c.repo.ID, "branch": S(op.Data, "branch"), "commit": S(op.Data, "commit"),
		"observedHead": head, "decision": S(in, "decision"), "reason": S(in, "reason"), "actorId": a.ID, "originalAuthorId": S(op.Data, "authorId"),
		"runId": c.run.ID, "taskId": c.task.ID, "requirementRevision": revision(op), "planId": S(c.run.Data, "planId"),
		"sourceBindingDigest": S(Obj(c.task.Data["sourceBinding"]), "digest"), "priorOperationStatus": op.Status, "priorTaskStatus": c.task.Status,
		"priorRunState": M{"status": c.run.Status, "result": c.run.Data["result"], "exitCode": c.run.Data["exitCode"], "completionDigest": c.run.Data["completionDigest"], "completedAt": c.run.Data["completedAt"]},
	})
	decision.Status = "abandoned"
	op.Status, c.change.Status = "abandoned", "abandoned"
	if S(in, "decision") == "confirm" {
		decision.Status = "confirmed"
		op.Status, c.change.Status = "committed", "committed"
	}
	op.Data["sourceRecoveryId"], c.change.Data["sourceRecoveryId"] = decision.ID, decision.ID
	if e = t.Put(decision); e != nil {
		return Resource{}, e
	}
	if e = t.Put(op); e != nil {
		return Resource{}, e
	}
	if e = t.Put(c.change); e != nil {
		return Resource{}, e
	}
	if c.run.ID != "" {
		c.run.Data["sourceRecoveryId"], c.run.Data["recoveredAt"] = decision.ID, now()
		delete(c.run.Data, "leaseToken")
		if decision.Status == "confirmed" {
			c.run.Status = "recovered"
			c.run.Data["result"] = M{"repositoryId": c.repo.ID, "commit": S(op.Data, "commit"), "sourceOperationId": op.ID}
			c.task.Status = "succeeded"
			c.task.Data["sourceRecoveryId"] = decision.ID
			delete(c.task.Data, "sourceConflict")
			delete(c.task.Data, "sourceRecoveryIds")
			c.task.Data["reason"] = "负责人已确认原源码候选；后续需独立验证"
		} else {
			if !Has([]string{"stale", "cancelled"}, c.run.Status) {
				c.run.Status = "abandoned"
			}
			if !Has([]string{"stale", "cancelled", "succeeded"}, c.task.Status) {
				c.task.Status = "blocked"
				c.task.Data["reason"] = "已停止采用源码候选；继续开发前需核对分支或重新规划"
				delete(c.task.Data, "sourceRecoveryIds")
			}
		}
		if e = t.Put(c.run); e != nil {
			return Resource{}, e
		}
		// Retire other expired attempts so a later claim sweep cannot undo the
		// accepted task. Their actual logs/results and identities are preserved.
		runs, e := t.List(op.ProjectID, "run", op.GoalID)
		if e != nil {
			return Resource{}, e
		}
		for _, run := range runs {
			if run.ID != c.run.ID && S(run.Data, "taskId") == c.task.ID && run.Status == "running" {
				run.Status = "expired"
				delete(run.Data, "leaseToken")
				if e = t.Put(run); e != nil {
					return Resource{}, e
				}
			}
		}
		if e = t.Put(c.task); e != nil {
			return Resource{}, e
		}
		if decision.Status == "confirmed" {
			if e = s.afterRepair(t, c.run, c.task, Obj(c.run.Data["result"])); e != nil {
				return Resource{}, e
			}
		}
		if e = s.aggregateChildren(t, op.ProjectID); e != nil {
			return Resource{}, e
		}
	}
	if e = t.Remember(scope, S(in, "key"), in, decision); e != nil {
		return Resource{}, e
	}
	if e = t.Audit(a, "sources.recovery."+decision.Status, op, M{"recoveryId": decision.ID, "commit": S(op.Data, "commit"), "observedHead": head, "originalAuthorId": S(op.Data, "authorId"), "runId": c.run.ID}); e != nil {
		return Resource{}, e
	}
	return decision, nil
}

func (s *Service) unsettledTaskSources(t *Tx, task Resource) ([]Resource, error) {
	result := []Resource{}
	if S(task.Data, "kind") != "code" {
		return result, nil
	}
	ops, e := t.List(task.ProjectID, "source_commit", task.GoalID)
	if e != nil {
		return nil, e
	}
	for _, op := range ops {
		if !Has([]string{"prepared", "committed"}, op.Status) || S(op.Data, "sourceRecoveryId") != "" || S(op.Data, "runId") == "" {
			continue
		}
		run, e := t.Get(S(op.Data, "runId"))
		if e != nil {
			return nil, e
		}
		if S(run.Data, "taskId") == task.ID && !Has([]string{"succeeded", "recovered"}, run.Status) {
			result = append(result, op)
		}
	}
	return result, nil
}

func (s *Service) blockUnsettledSource(t *Tx, task *Resource) (bool, error) {
	ops, e := s.unsettledTaskSources(t, *task)
	if e != nil || len(ops) == 0 {
		return false, e
	}
	ids := []string{}
	for _, op := range ops {
		ids = append(ids, op.ID)
	}
	task.Status = "blocked"
	task.Data["sourceRecoveryIds"] = ids
	task.Data["reason"] = "源码候选已持久化，执行中断后需要负责人核对"
	delete(task.Data, "sourceConflict")
	return true, t.Put(*task)
}

func sourceRecoveryProof(t *Tx, run, task Resource, result M) error {
	decision, e := t.Get(S(run.Data, "sourceRecoveryId"))
	if e != nil || decision.Kind != "source_recovery" || decision.Status != "confirmed" || decision.ProjectID != run.ProjectID || decision.GoalID != run.GoalID ||
		S(decision.Data, "decision") != "confirm" || S(decision.Data, "actorId") == "" || S(decision.Data, "runId") != run.ID || S(decision.Data, "taskId") != task.ID ||
		S(decision.Data, "originalAuthorId") != "worker:"+S(run.Data, "workerId") || revision(decision) != revision(run) ||
		S(decision.Data, "planId") != S(run.Data, "planId") || S(decision.Data, "sourceBindingDigest") != S(Obj(task.Data["sourceBinding"]), "digest") ||
		S(decision.Data, "repositoryId") != S(result, "repositoryId") || S(decision.Data, "commit") != S(result, "commit") ||
		S(decision.Data, "sourceOperationId") != S(result, "sourceOperationId") || S(task.Data, "sourceRecoveryId") != decision.ID {
		return conflict("人工恢复来源缺少一致的负责人决定")
	}
	op, e := t.Get(S(result, "sourceOperationId"))
	if e != nil || op.Kind != "source_commit" || op.Status != "committed" || op.ProjectID != run.ProjectID || op.GoalID != run.GoalID ||
		S(op.Data, "sourceRecoveryId") != decision.ID || S(op.Data, "runId") != run.ID || S(op.Data, "commit") != S(result, "commit") || S(op.Data, "repositoryId") != S(result, "repositoryId") {
		return conflict("人工恢复来源缺少原已确认源码操作")
	}
	return nil
}

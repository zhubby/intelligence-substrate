package platform

import (
	"context"
	"math"
	"sort"
	"strings"
	"time"
)

func messageTaskRecipe(data M) string {
	data = copyM(data)
	for _, key := range []string{"attempts", "reason", "messageWaitId", "messageWaitCount", "messageStepsUsed", "agentProgressId"} {
		delete(data, key)
	}
	return Hash(data)
}

func (s *Service) messageWaitCommand(t *Tx, a Actor, in M) (Resource, error) {
	if a.Kind != "worker" {
		return Resource{}, denied()
	}
	run, e := s.scoped(t, a, S(in, "id"), "run")
	if e != nil {
		return run, e
	}
	worker, e := t.Get(a.WorkerID)
	if e != nil || worker.Status == "disabled" || a.ID != "worker:"+worker.ID || S(run.Data, "workerId") != worker.ID {
		return Resource{}, denied()
	}
	// Replays authenticate the current caller but do not revive the old lease.
	if S(run.Data, "messageWaitDigest") != "" {
		if S(run.Data, "messageWaitDigest") != Hash(in) {
			return run, conflict("等待请求已以不同内容提交")
		}
		return t.Get(S(run.Data, "messageWaitId"))
	}
	run, task, e := s.messageRun(t, a, M{"runId": run.ID, "leaseToken": S(in, "leaseToken")})
	if e != nil {
		return run, e
	}
	if !Has([]string{"analysis", "code", "design"}, S(task.Data, "kind")) || len(Items(task.Data["command"])) != 0 || len(Strings(task.Data["children"])) != 0 || messageTaskRecipe(task.Data) != messageTaskRecipe(Obj(run.Data["task"])) {
		return Resource{}, conflict("等待仅适用于当前未变更的无固定命令分析、开发或设计工作项")
	}
	if N(task.Data, "messageWaitCount") >= 3 {
		return Resource{}, conflict("工作项已达到三次协作等待上限，需要重新规划")
	}
	if e = s.AgentProgressGate(t, task); e != nil {
		return run, e
	}
	for key, bounds := range map[string][2]float64{"timeoutSeconds": {60, 86400}, "steps": {1, 23}} {
		n := N(in, key)
		if math.IsNaN(n) || math.IsInf(n, 0) || n != math.Trunc(n) || n < bounds[0] || n > bounds[1] {
			return Resource{}, invalid("等待时间或步数超出允许的整数范围")
		}
	}
	if N(in, "steps") <= N(task.Data, "messageStepsUsed") {
		return Resource{}, conflict("等待步数必须推进当前工作项")
	}
	for key, limit := range map[string]int{"reason": 2000, "summary": 8000} {
		if e = validateText(S(in, key), "等待说明", limit); e != nil {
			return Resource{}, e
		}
	}
	if e = noSecrets(in); e != nil {
		return Resource{}, e
	}
	request, e := t.Get(S(in, "requestMessageId"))
	if e != nil || request.Kind != "message" || request.ProjectID != run.ProjectID || request.GoalID != run.GoalID || revision(request) != revision(run) || S(request.Data, "sender") != a.ID || S(request.Data, "senderRunId") != run.ID || !Has([]string{"request", "clarification", "review"}, S(request.Data, "type")) || S(request.Data, "recipientTaskId") == task.ID {
		return Resource{}, conflict("等待需要当前执行发给其他协作者的准确请求")
	}
	if e = s.messageRecipient(t, run.ProjectID, S(request.Data, "recipient")); e != nil {
		return Resource{}, e
	}
	source, ok := in["source"].(map[string]any)
	if !ok || len(source) != 2 {
		return Resource{}, invalid("等待需要准确的只读源码上下文")
	}
	for _, key := range []string{"repositoryId", "commit"} {
		if _, ok := source[key].(string); !ok {
			return Resource{}, invalid("源码上下文字段必须为字符串")
		}
	}
	if e = s.validateWaitSource(t, run.ProjectID, source); e != nil {
		return Resource{}, e
	}
	for _, key := range []string{"repositoryId", "commit"} {
		if expected := S(task.Data, key); expected != "" && S(source, key) != expected {
			return Resource{}, conflict("等待必须保留批准工作项声明的只读源码")
		}
	}
	if prior := S(task.Data, "messageWaitId"); prior != "" {
		previous, err := t.Get(prior)
		if err != nil || Hash(previous.Data["source"]) != Hash(source) {
			return Resource{}, conflict("继续分析不能切换原等待的只读源码")
		}
	}
	var checkpoint Resource
	if Has([]string{"code", "design"}, S(task.Data, "kind")) {
		if _, _, e = s.checkpointAuthority(t, a, run.ID, M{"leaseToken": S(in, "leaseToken"), "repositoryId": S(source, "repositoryId"), "commit": S(source, "commit")}); e != nil {
			return Resource{}, e
		}
		checkpoint, e = t.Get(S(in, "checkpointId"))
		if e != nil || S(checkpoint.Data, "runId") != run.ID {
			return Resource{}, conflict("开发或设计等待需要当前执行保存的准确工作区")
		}
		if e = s.checkpointSource(t, task, run, source); e != nil {
			return Resource{}, e
		}
		if e = s.validateWorkspaceCheckpoint(t, checkpoint, task, source); e != nil {
			return Resource{}, e
		}
	} else if _, present := in["checkpointId"]; present {
		return Resource{}, invalid("分析等待不能引入工作区")
	}
	_, _, e = messageTaskAllowance(t, task, run.ID)
	if e != nil {
		return Resource{}, e
	}
	wait := resource("message_wait", task.Title, run.ProjectID, run.GoalID, M{"version": 1, "taskId": task.ID, "runId": run.ID, "planId": S(run.Data, "planId"), "requirementRevision": revision(run), "taskRecipe": messageTaskRecipe(task.Data), "requestMessageId": request.ID, "requester": a.ID, "recipient": S(request.Data, "recipient"), "recipientTaskId": S(request.Data, "recipientTaskId"), "reason": S(in, "reason"), "summary": S(in, "summary"), "source": copyM(source), "steps": N(in, "steps"), "expiresAt": time.Now().Add(time.Duration(N(in, "timeoutSeconds")) * time.Second).UTC().Format(time.RFC3339Nano)})
	wait.ID = "message_wait_" + Hash([]string{run.ID})
	wait.Status = "waiting"
	if checkpoint.ID != "" {
		wait.Data["checkpointId"], wait.Data["checkpointDigest"] = checkpoint.ID, S(checkpoint.Data, "digest")
	}
	run.Status = "suspended"
	run.Data["suspendedAt"], run.Data["messageWaitId"], run.Data["messageWaitDigest"] = now(), wait.ID, Hash(in)
	delete(run.Data, "leaseToken")
	task.Status = "waiting_message"
	task.Data["messageWaitId"], task.Data["messageWaitCount"], task.Data["messageStepsUsed"], task.Data["reason"] = wait.ID, N(task.Data, "messageWaitCount")+1, N(in, "steps"), S(in, "reason")
	delete(task.Data, "agentProgressId")
	for _, r := range []Resource{run, task, wait} {
		if e = t.Put(r); e != nil {
			return Resource{}, e
		}
	}
	messages, e := t.List(run.ProjectID, "message", run.GoalID)
	if e != nil {
		return Resource{}, e
	}
	sort.Slice(messages, func(i, j int) bool {
		if messages[i].CreatedAt == messages[j].CreatedAt {
			return messages[i].ID < messages[j].ID
		}
		return messages[i].CreatedAt < messages[j].CreatedAt
	})
	for _, message := range messages {
		if matchesWaitReply(t, wait, message) {
			if e = s.wakeMessageWait(t, &wait, message); e != nil {
				return Resource{}, e
			}
			break
		}
	}
	return wait, nil
}

func (s *Service) validateWaitSource(t *Tx, project string, source M) error {
	repoID, commit := S(source, "repositoryId"), S(source, "commit")
	if repoID == "" && commit == "" {
		return nil
	}
	repo, e := t.Get(repoID)
	if e != nil || repo.Kind != "repository" || repo.ProjectID != project {
		return conflict("等待的只读源码仓库不可用")
	}
	return s.sourceCommitExists(repo, commit)
}

func matchesWaitReply(t *Tx, wait, message Resource) bool {
	if wait.Status != "waiting" || message.Kind != "message" || message.ProjectID != wait.ProjectID || message.GoalID != wait.GoalID || revision(message) != revision(wait) || !Has([]string{"pending", "delivered"}, message.Status) || !Has([]string{"result", "clarification", "review"}, S(message.Data, "type")) || S(message.Data, "sender") != S(wait.Data, "recipient") || S(message.Data, "recipient") != S(wait.Data, "requester") || S(message.Data, "recipientTaskId") != S(wait.Data, "taskId") || S(message.Data, "causationId") != S(wait.Data, "requestMessageId") {
		return false
	}
	if strings.HasPrefix(S(message.Data, "sender"), "worker:") {
		run, e := t.Get(S(message.Data, "senderRunId"))
		return e == nil && run.Kind == "run" && S(run.Data, "taskId") == S(wait.Data, "recipientTaskId") && S(run.Data, "planId") == S(wait.Data, "planId") && run.ProjectID == wait.ProjectID && run.GoalID == wait.GoalID && revision(run) == revision(wait)
	}
	return true
}

func (s *Service) validMessageWait(t *Tx, wait Resource, task Resource) error {
	g, e := t.Get(wait.GoalID)
	if e != nil {
		return e
	}
	plan, e := t.Get(S(wait.Data, "planId"))
	if e != nil || approved(t, g) != nil || checkRevision(g, wait) != nil || plan.Kind != "plan" || plan.Status != "active" || S(plan.Data, "approvedBy") == "" || plan.GoalID != g.ID || plan.ProjectID != g.ProjectID || revision(plan) != g.Revision || S(g.Data, "activePlanId") != plan.ID || task.Kind != "task" || task.GoalID != g.ID || task.ProjectID != g.ProjectID || revision(task) != g.Revision || S(task.Data, "planId") != plan.ID || S(task.Data, "messageWaitId") != wait.ID || messageTaskRecipe(task.Data) != S(wait.Data, "taskRecipe") {
		return conflict("协作等待的需求、批准方案或工作项已变化")
	}
	if e = s.validateWaitSource(t, g.ProjectID, Obj(wait.Data["source"])); e != nil {
		return e
	}
	if Has([]string{"code", "design"}, S(task.Data, "kind")) {
		checkpoint, err := t.Get(S(wait.Data, "checkpointId"))
		if err != nil || S(checkpoint.Data, "runId") != S(wait.Data, "runId") || S(checkpoint.Data, "digest") != S(wait.Data, "checkpointDigest") {
			return conflict("协作等待缺少准确工作区")
		}
		return s.validateWorkspaceCheckpoint(t, checkpoint, task, Obj(wait.Data["source"]))
	}
	return nil
}

func (s *Service) closeMessageWait(t *Tx, wait *Resource, task *Resource, status, reason string) error {
	wait.Status, wait.Data["closedAt"], wait.Data["closeReason"] = status, now(), reason
	if Has([]string{"waiting_message", "pending"}, task.Status) {
		task.Status, task.Data["reason"] = "blocked", reason
		if e := t.Put(*task); e != nil {
			return e
		}
	}
	if e := t.Put(*wait); e != nil {
		return e
	}
	return t.Audit(Actor{ID: "system:collaboration", Kind: "system"}, "tasks.wait."+status, *wait, M{"reason": reason})
}

func (s *Service) wakeMessageWait(t *Tx, wait *Resource, message Resource) error {
	task, e := t.Get(S(wait.Data, "taskId"))
	if e != nil {
		return e
	}
	if message.Status != "delivered" && N(message.Data, "attempts") >= 3 {
		return s.closeMessageWait(t, wait, &task, "blocked", "回复已达到消息投递次数上限；保留原消息，需要人工重新规划")
	}
	run, e := t.Get(S(wait.Data, "runId"))
	if e != nil {
		return e
	}
	receipts, e := taskMessageReceipts(t, run)
	if e != nil {
		return e
	}
	receipt, fits, e := recordTaskMessage(t, run, message, receipts)
	if e != nil {
		return e
	}
	if !fits {
		return s.closeMessageWait(t, wait, &task, "blocked", "协作上下文达到 32 条或 64 KiB 上限；回复保留，需要人工重新规划")
	}
	wait.Status, wait.Data["replyMessageId"], wait.Data["receiptId"], wait.Data["readyAt"] = "ready", message.ID, receipt.ID, now()
	task.Status, task.Data["reason"] = "pending", "已收到指定协作者回复，等待可用节点继续"
	if e = t.Put(task); e != nil {
		return e
	}
	if e = t.Put(*wait); e != nil {
		return e
	}
	return t.Audit(Actor{ID: "system:collaboration", Kind: "system"}, "tasks.wait.ready", *wait, M{"replyMessageId": message.ID, "receiptId": receipt.ID})
}

// Only the exact reply to a live wait may bypass a disabled original requester
// identity. It enters task context atomically, never another Worker's mailbox.
func (s *Service) waitingReply(t *Tx, message Resource) (Resource, bool, error) {
	waits, e := t.List(message.ProjectID, "message_wait", message.GoalID)
	if e != nil {
		return Resource{}, false, e
	}
	for _, wait := range waits {
		if matchesWaitReply(t, wait, message) {
			return wait, true, nil
		}
	}
	return Resource{}, false, nil
}

func (s *Service) refreshMessageWaits(t *Tx, project string) error {
	waits, e := t.List(project, "message_wait", "")
	if e != nil {
		return e
	}
	for _, wait := range waits {
		if !Has([]string{"waiting", "ready"}, wait.Status) {
			continue
		}
		task, e := t.Get(S(wait.Data, "taskId"))
		if e != nil {
			return e
		}
		status, reason := "", ""
		if Has([]string{"cancelled", "stale"}, task.Status) {
			status, reason = "cancelled", "工作项已取消或替换"
		} else if e = s.validMessageWait(t, wait, task); e != nil {
			status, reason = "stale", e.Error()
		} else if wait.Status == "waiting" {
			until, err := time.Parse(time.RFC3339Nano, S(wait.Data, "expiresAt"))
			if err != nil || !time.Now().Before(until) {
				status, reason = "expired", "协作等待超时；需要人工重试或重新规划"
			}
		}
		if status != "" {
			if e = s.closeMessageWait(t, &wait, &task, status, reason); e != nil {
				return e
			}
		}
	}
	return nil
}

func (s *Service) ReconcileMessageWaits(ctx context.Context) error {
	return s.Store.Update(ctx, func(t *Tx) error { return s.refreshMessageWaits(t, "") })
}

// A suspension chain spends one approved task envelope, even across Workers.
// Reserved model calls must be reconciled before a new run can consume money.
func messageTaskAllowance(t *Tx, task Resource, currentRunID string) (float64, time.Duration, error) {
	budget, remaining, e := messageTaskRemaining(t, task, currentRunID)
	if e != nil {
		return 0, 0, e
	}
	if budget <= 0 {
		return 0, 0, conflict("工作项的累计预算或执行时间已用尽，需要重新规划")
	}
	return budget, remaining, nil
}

// Zero model budget still permits the last admitted tool result to be saved.
// New inference, claims and explicit waits require positive remaining allowance.
func messageTaskRemaining(t *Tx, task Resource, currentRunID string) (float64, time.Duration, error) {
	runs, e := t.List(task.ProjectID, "run", task.GoalID)
	if e != nil {
		return 0, 0, e
	}
	ids := map[string]bool{}
	spent, elapsed := 0.0, time.Duration(0)
	for _, run := range runs {
		if S(run.Data, "taskId") != task.ID {
			continue
		}
		ids[run.ID] = true
		spent += N(run.Data, "spentCents")
		start, err := time.Parse(time.RFC3339Nano, run.CreatedAt)
		if err != nil {
			return 0, 0, conflict("执行时间记录不可用")
		}
		end := time.Now()
		key := "completedAt"
		if run.Status == "suspended" {
			key = "suspendedAt"
		}
		if Has([]string{"expired", "cancelled"}, run.Status) {
			key = "leaseUntil"
		}
		if run.Status != "running" {
			end, err = time.Parse(time.RFC3339Nano, S(run.Data, key))
			if err != nil {
				return 0, 0, conflict("执行结束时间不可用，需要核对")
			}
		}
		deadline, err := time.Parse(time.RFC3339Nano, S(run.Data, "deadline"))
		if err != nil {
			return 0, 0, conflict("执行截止时间不可用")
		}
		if end.After(deadline) {
			end = deadline
		}
		if end.After(start) {
			elapsed += end.Sub(start)
		}
		if run.Status == "running" && run.ID != currentRunID {
			return 0, 0, conflict("工作项仍有活动执行")
		}
	}
	charges, e := t.List(task.ProjectID, "charge", task.GoalID)
	if e != nil {
		return 0, 0, e
	}
	for _, charge := range charges {
		if ids[S(charge.Data, "runId")] && charge.Status == "reserved" {
			return 0, 0, conflict("工作项有结果未明的模型费用，需要先核对")
		}
	}
	budget, remaining := N(task.Data, "budgetCents")-spent, taskTimeout(task)-elapsed
	if budget < 0 || remaining <= 0 {
		return 0, 0, conflict("工作项的累计预算或执行时间已用尽，需要重新规划")
	}
	return budget, remaining, nil
}

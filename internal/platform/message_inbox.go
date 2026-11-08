package platform

import (
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"
)

func (s *Service) messageRun(t *Tx, a Actor, in M) (Resource, Resource, error) {
	if e := s.validWorkerRun(t, a, "messages.receive", in); e != nil {
		return Resource{}, Resource{}, e
	}
	run, e := t.Get(S(in, "runId"))
	if e != nil {
		return run, Resource{}, e
	}
	for _, key := range []string{"leaseUntil", "deadline"} {
		until, e := time.Parse(time.RFC3339Nano, S(run.Data, key))
		if e != nil || !time.Now().Before(until) {
			return run, Resource{}, conflict("消息操作的执行租约或截止时间已失效")
		}
	}
	task, e := t.Get(S(run.Data, "taskId"))
	if e != nil {
		return run, task, e
	}
	g, e := t.Get(run.GoalID)
	if e != nil {
		return run, task, e
	}
	plan, e := t.Get(S(run.Data, "planId"))
	if e != nil || task.Status != "running" || task.Kind != "task" || task.ProjectID != run.ProjectID || task.GoalID != run.GoalID ||
		S(task.Data, "planId") != plan.ID || S(g.Data, "activePlanId") != plan.ID || plan.Kind != "plan" || plan.Status != "active" ||
		plan.ProjectID != run.ProjectID || plan.GoalID != run.GoalID || S(plan.Data, "approvedBy") == "" || revision(plan) != g.Revision || revision(task) != g.Revision || S(run.Data, "sourceRecoveryId") != "" {
		return run, task, conflict("消息操作需要当前批准方案中的活动工作项")
	}
	return run, task, nil
}

func (s *Service) messageRecipient(t *Tx, project, recipient string) error {
	if strings.HasPrefix(recipient, "worker:") {
		worker, e := t.Get(strings.TrimPrefix(recipient, "worker:"))
		if e != nil || worker.Kind != "worker" || worker.ProjectID != project || worker.Status == "disabled" {
			return invalid("收件节点不属于此项目或已停用")
		}
		return nil
	}
	roles, e := t.ProjectRoles(Actor{ID: recipient, Kind: "human"}, project)
	if e != nil {
		return e
	}
	if len(roles) > 0 {
		var identity string
		e := t.tx.QueryRow(`SELECT body FROM identities WHERE id=$1`, recipient).Scan(&identity)
		if e == nil {
			var data M
			if json.Unmarshal([]byte(identity), &data) != nil || B(data, "disabled") {
				return denied()
			}
		} else if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		return nil
	}
	rows, e := t.tx.Query(`SELECT c.body,i.body FROM credentials c JOIN identities i ON i.id=c.identity_id WHERE c.kind='token' AND c.expires_at>$1`, now())
	if e != nil {
		return e
	}
	defer rows.Close()
	for rows.Next() {
		var body, issuerBody string
		if e = rows.Scan(&body, &issuerBody); e != nil {
			return e
		}
		var actor Actor
		var issuer M
		if json.Unmarshal([]byte(body), &actor) == nil && json.Unmarshal([]byte(issuerBody), &issuer) == nil &&
			actor.ID == recipient && actor.ProjectID == project && actor.Kind == "agent" && !B(issuer, "disabled") {
			return nil
		}
	}
	if e = rows.Err(); e != nil {
		return e
	}
	return invalid("收件人不是此项目的有效协作者")
}

// An explicit task prevents messages for a busy worker from entering a different
// later task. Omission is only unambiguous with one active target run.
func (s *Service) messageRecipientTask(t *Tx, in M, requiredRevision int) (string, error) {
	if !strings.HasPrefix(S(in, "recipient"), "worker:") || S(in, "goalId") == "" {
		if S(in, "recipientTaskId") != "" {
			return "", invalid("只有目标内的节点消息可以指定收件工作项")
		}
		return "", nil
	}
	id := S(in, "recipientTaskId")
	if id == "" {
		runs, e := t.List(S(in, "projectId"), "run", S(in, "goalId"))
		if e != nil {
			return "", e
		}
		for _, run := range runs {
			if S(run.Data, "workerId") == strings.TrimPrefix(S(in, "recipient"), "worker:") && activeSourceLease(run) && revision(run) == requiredRevision {
				if id != "" {
					return "", invalid("收件节点有多个活动执行，请指定 recipientTaskId")
				}
				id = S(run.Data, "taskId")
			}
		}
	}
	task, e := t.Get(id)
	if e != nil || task.Kind != "task" || task.ProjectID != S(in, "projectId") || task.GoalID != S(in, "goalId") || revision(task) != requiredRevision || Has([]string{"cancelled", "stale", "succeeded"}, task.Status) {
		return "", invalid("需要当前目标内有效的 recipientTaskId")
	}
	g, e := t.Get(task.GoalID)
	if e != nil || S(task.Data, "planId") != S(g.Data, "activePlanId") {
		return "", conflict("收件工作项不属于当前方案")
	}
	return id, nil
}

func (s *Service) receiveMessages(t *Tx, run Resource, a Actor) (Resource, error) {
	receipts, e := taskMessageReceipts(t, run)
	if e != nil {
		return Resource{}, e
	}
	seen := map[string]bool{}
	for _, receipt := range receipts {
		seen[S(receipt.Data, "messageId")] = true
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
	received, blocked, deferred := 0, 0, false
	for _, message := range messages {
		if S(message.Data, "recipient") != a.ID || S(message.Data, "recipientTaskId") != S(run.Data, "taskId") || revision(message) != revision(run) || seen[message.ID] {
			continue
		}
		if message.Status != "pending" && !(message.Status == "leased" && S(message.Data, "leaseUntil") < now()) {
			continue
		}
		if N(message.Data, "attempts") >= 3 {
			message.Status = "blocked"
			message.Data["reason"] = "消息投递超过重试限制"
			delete(message.Data, "leaseToken")
			if e = t.Put(message); e != nil {
				return Resource{}, e
			}
			blocked++
			continue
		}
		receipt, fits, err := recordTaskMessage(t, run, message, receipts)
		if err != nil {
			return Resource{}, err
		}
		if !fits {
			deferred = true
			continue
		}
		receipts = append(receipts, receipt)
		seen[message.ID] = true
		received++
	}
	sort.Slice(receipts, func(i, j int) bool {
		if receipts[i].CreatedAt == receipts[j].CreatedAt {
			return receipts[i].ID < receipts[j].ID
		}
		return receipts[i].CreatedAt < receipts[j].CreatedAt
	})
	return Resource{ID: run.ID, Kind: "message-inbox", ProjectID: run.ProjectID, GoalID: run.GoalID,
		Data: M{"items": copyM(M{"items": receipts})["items"], "deferred": deferred, "receivedCount": received, "blockedCount": blocked}}, nil
}

func taskMessageReceipts(t *Tx, run Resource) ([]Resource, error) {
	all, e := t.List(run.ProjectID, "message_receipt", run.GoalID)
	if e != nil {
		return nil, e
	}
	out := []Resource{}
	for _, receipt := range all {
		if S(receipt.Data, "taskId") == S(run.Data, "taskId") && S(receipt.Data, "planId") == S(run.Data, "planId") && revision(receipt) == revision(run) {
			out = append(out, receipt)
		}
	}
	return out, nil
}

// The caller commits the receipt and its workflow transition in one transaction.
// The same bounded context applies to running inbox delivery and wait wake-up.
func recordTaskMessage(t *Tx, run, message Resource, receipts []Resource) (Resource, bool, error) {
	for _, receipt := range receipts {
		if S(receipt.Data, "messageId") == message.ID {
			return receipt, true, nil
		}
	}
	snapshot := M{"id": message.ID, "sender": S(message.Data, "sender"), "recipient": S(message.Data, "recipient"), "recipientTaskId": S(message.Data, "recipientTaskId"), "type": S(message.Data, "type"), "payload": message.Data["payload"], "causationId": S(message.Data, "causationId"), "senderRunId": S(message.Data, "senderRunId"), "createdAt": message.CreatedAt, "requirementRevision": revision(message)}
	receipt := resource("message_receipt", message.Title, run.ProjectID, run.GoalID, M{"taskId": S(run.Data, "taskId"), "planId": S(run.Data, "planId"), "requirementRevision": revision(run), "firstRunId": run.ID, "workerId": S(run.Data, "workerId"), "messageId": message.ID, "message": snapshot})
	receipt.ID = "message_receipt_" + Hash([]string{S(run.Data, "taskId"), message.ID})
	receipt.Status = "recorded"
	body, e := json.Marshal(append(append([]Resource{}, receipts...), receipt))
	if e != nil {
		return Resource{}, false, e
	}
	if len(receipts) >= 32 || len(body) > 64<<10 {
		return Resource{}, false, nil
	}
	if e = t.Put(receipt); e != nil {
		return Resource{}, false, e
	}
	message.Status = "delivered"
	message.Data["receivedTaskId"], message.Data["receiptId"] = S(run.Data, "taskId"), receipt.ID
	message.Data["attempts"] = N(message.Data, "attempts") + 1
	delete(message.Data, "leaseToken")
	return receipt, true, t.Put(message)
}

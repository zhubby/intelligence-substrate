package platform

import (
	"encoding/json"
	"time"
)

func (s *Service) messageCommand(t *Tx, a Actor, cmd string, in M) (Resource, error) {
	var run Resource
	if a.Kind == "worker" {
		if cmd != "messages.send" && cmd != "messages.receive" {
			return Resource{}, conflict("执行节点必须使用当前工作项的持久收件上下文")
		}
		var e error
		run, _, e = s.messageRun(t, a, in)
		if e != nil {
			return Resource{}, e
		}
	}
	if cmd == "messages.receive" {
		if a.Kind != "worker" {
			return Resource{}, denied()
		}
		return s.receiveMessages(t, run, a)
	}
	if cmd == "messages.send" {
		p := S(in, "projectId")
		if e := s.allowed(t, a, p); e != nil {
			return Resource{}, e
		}
		goalRevision := 0
		if a.Kind == "worker" && S(in, "goalId") != run.GoalID {
			return Resource{}, denied()
		}
		if S(in, "goalId") != "" {
			g, e := s.scoped(t, a, S(in, "goalId"), "goal")
			if e != nil {
				return g, e
			}
			if g.ProjectID != p {
				return g, denied()
			}
			goalRevision = g.Revision
		}
		if !Has([]string{"request", "progress", "result", "blocked", "clarification", "review", "cancel"}, S(in, "type")) {
			return Resource{}, invalid("未知消息类型")
		}
		if e := validateText(S(in, "recipient"), "收件人", 100); e != nil {
			return Resource{}, e
		}
		if e := noSecrets(in["payload"]); e != nil {
			return Resource{}, e
		}
		payload, ok := in["payload"].(map[string]any)
		body, e := json.Marshal(payload)
		if !ok || e != nil || len(body) > 8<<10 {
			return Resource{}, invalid("消息 payload 必须为不超过 8 KiB 的对象")
		}
		scope := p + ":" + a.ID + ":message"
		if r, ok, e := t.Once(scope, S(in, "key"), in); e != nil || ok {
			return r, e
		}
		if e = s.refreshMessageWaits(t, p); e != nil {
			return Resource{}, e
		}
		r := resource("message", S(in, "type"), p, S(in, "goalId"), M{"sender": a.ID, "recipient": S(in, "recipient"), "type": S(in, "type"), "payload": payload, "causationId": S(in, "causationId"), "attempts": 0, "requirementRevision": goalRevision, "senderRunId": run.ID, "recipientTaskId": S(in, "recipientTaskId")})
		r.Status = "pending"
		wait, waiting, e := s.waitingReply(t, r)
		if e != nil {
			return Resource{}, e
		}
		if !waiting {
			if e = s.messageRecipient(t, p, S(in, "recipient")); e != nil {
				return Resource{}, e
			}
		}
		if cause := S(in, "causationId"); cause != "" {
			previous, e := t.Get(cause)
			if e != nil || previous.Kind != "message" || previous.ProjectID != p || previous.GoalID != S(in, "goalId") || revision(previous) != goalRevision {
				return Resource{}, conflict("消息因果引用必须属于相同项目、目标和需求版本")
			}
		}
		recipientTask, e := s.messageRecipientTask(t, in, goalRevision)
		if e != nil {
			return Resource{}, e
		}
		r.Data["recipientTaskId"] = recipientTask
		if e := t.Put(r); e != nil {
			return r, e
		}
		if waiting {
			if e = s.wakeMessageWait(t, &wait, r); e != nil {
				return Resource{}, e
			}
		}
		if e = t.Remember(scope, S(in, "key"), in, r); e != nil {
			return Resource{}, e
		}
		return t.Get(r.ID)
	}
	if cmd == "messages.claim" {
		p := S(in, "projectId")
		if e := s.allowed(t, a, p); e != nil {
			return Resource{}, e
		}
		all, e := t.List(p, "message", "")
		if e != nil {
			return Resource{}, e
		}
		for _, r := range all {
			if S(r.Data, "recipient") != a.ID {
				continue
			}
			expired := S(r.Data, "leaseUntil") < now()
			if r.Status != "pending" && !(r.Status == "leased" && expired) {
				continue
			}
			if N(r.Data, "attempts") >= 3 {
				r.Status = "blocked"
				r.Data["reason"] = "消息投递超过重试限制"
				if e = t.Put(r); e != nil {
					return r, e
				}
				continue
			}
			r.Status = "leased"
			r.Data["leaseToken"] = Secret()
			r.Data["leaseUntil"] = time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano)
			r.Data["attempts"] = N(r.Data, "attempts") + 1
			return r, t.Put(r)
		}
		return Resource{Kind: "message-poll", ProjectID: p, Data: M{"idle": true}}, nil
	}
	r, e := s.scoped(t, a, S(in, "id"), "message")
	if e != nil {
		return r, e
	}
	if S(r.Data, "recipient") != a.ID {
		return r, denied()
	}
	if r.Status == "delivered" {
		return r, nil
	}
	if r.Status != "leased" || S(in, "leaseToken") != S(r.Data, "leaseToken") || S(r.Data, "leaseUntil") < now() {
		return r, conflict("消息租约已失效")
	}
	r.Status = "delivered"
	delete(r.Data, "leaseToken")
	return r, t.Put(r)
}

package worker

import (
	"context"
	"fmt"

	p "github.com/intelligence-substrate/platform/internal/platform"
)

// Delivery means that context is durable on the task, not that the model has
// understood it. A failed provider request or replacement worker can replay it.
func (r *Runner) receiveMessages(ctx context.Context, run p.Resource, seen map[string]bool) ([]any, error) {
	inbox, e := r.Client.Command(ctx, "messages.receive", p.M{"runId": run.ID, "leaseToken": p.S(run.Data, "leaseToken")})
	if e != nil {
		return nil, e
	}
	if inbox.Kind != "message-inbox" || inbox.ID != run.ID || inbox.GoalID != run.GoalID || inbox.ProjectID != run.ProjectID {
		return nil, fmt.Errorf("collaboration inbox does not belong to this execution")
	}
	if p.B(inbox.Data, "deferred") {
		return nil, fmt.Errorf("task collaboration context reached its 32-message or 64 KiB limit; pending messages are preserved for human replanning")
	}
	out := []any{}
	for _, item := range p.Items(inbox.Data["items"]) {
		receipt := p.Obj(item)
		data := p.Obj(receipt["data"])
		message := p.Obj(data["message"])
		id := p.S(receipt, "id")
		if id == "" || p.S(receipt, "kind") != "message_receipt" || p.S(receipt, "status") != "recorded" ||
			p.S(data, "taskId") != p.S(run.Data, "taskId") || p.S(data, "planId") != p.S(run.Data, "planId") ||
			p.N(data, "requirementRevision") != p.N(run.Data, "requirementRevision") || p.S(message, "id") != p.S(data, "messageId") {
			return nil, fmt.Errorf("collaboration receipt does not match the approved task")
		}
		if !seen[id] {
			out = append(out, p.M{"receiptId": id, "firstRunId": p.S(data, "firstRunId"), "message": message})
			seen[id] = true
		}
	}
	return out, nil
}

package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	p "github.com/intelligence-substrate/platform/internal/platform"
)

func TestWorkerReceivesFrozenClarificationAnswers(t *testing.T) {
	f := newInboxHarness(t, false)
	f.goal = f.command(t, f.owner, "goals.revise", p.M{"id": f.goal.ID, "reason": "Confirm the business decision before development"})
	batch := f.command(t, f.owner, "clarifications.propose", p.M{"goalId": f.goal.ID, "expectedRequirementRevision": f.goal.Revision, "key": "ask", "questions": []any{p.M{"question": "Who confirms the UTC contract?"}}})
	question := p.Obj(p.Items(batch.Data["questions"])[0])
	batch = f.command(t, f.owner, "clarifications.answer", p.M{"id": batch.ID, "questionId": p.S(question, "id"), "expectedRequirementRevision": f.goal.Revision, "key": "answer", "answer": "FROZEN-HUMAN: the assigned project owner confirms UTC."})
	draft := f.command(t, f.owner, "requirements.propose", p.M{"goalId": f.goal.ID, "expectedRequirementRevision": f.goal.Revision, "key": "draft", "content": p.M{"outcome": "Implement confirmed UTC behavior", "brief": "REVIEWED-BRIEF: Owner confirms UTC; local-time reinterpretation is excluded.", "criteria": []any{p.M{"text": "Store and return confirmed UTC values", "method": "automated"}}}})
	content := p.Obj(draft.Data["content"])
	for _, item := range p.Items(content["criteria"]) {
		delete(p.Obj(item), "id")
	}
	f.goal = f.command(t, f.owner, "requirements.apply", p.M{"id": draft.ID, "expectedRequirementRevision": f.goal.Revision, "key": "adopt", "reason": "Review UTC contract and exclusions", "content": content})
	batches, _ := f.svc.Store.List(f.project.ID, "clarification", f.goal.ID)
	for _, current := range batches {
		if p.N(current.Data, "requirementRevision") == float64(f.goal.Revision) {
			batch = current
		}
	}
	f.command(t, f.owner, "goals.approve", p.M{"id": f.goal.ID})
	plan := f.command(t, f.owner, "plans.propose", p.M{"goalId": f.goal.ID, "summary": "Implement the confirmed decision", "tasks": []any{p.M{"id": "confirmed", "title": "Document the confirmed contract", "kind": "analysis", "role": "developer", "runtime": runtime.GOOS, "budgetCents": 100}}})
	f.command(t, f.owner, "plans.approve", p.M{"id": plan.ID})
	// A changed live row must not replace the approved snapshot. Foreign and
	// historical approvals are visible to the selector but are not authority.
	p.Obj(p.Items(batch.Data["questions"])[0])["answer"] = "UNAPPROVED-LIVE-ANSWER"
	if err := f.svc.Store.Update(context.Background(), func(tx *p.Tx) error {
		if err := tx.Put(batch); err != nil {
			return err
		}
		for _, foreign := range []p.Resource{
			{ID: "foreign-clarification-approval", Kind: "approval", ProjectID: "other-project", GoalID: f.goal.ID, Data: p.M{"type": "baseline", "requirementRevision": f.goal.Revision, "clarifications": []any{"FOREIGN-ANSWER"}}},
			{ID: "old-clarification-approval", Kind: "approval", ProjectID: f.project.ID, GoalID: f.goal.ID, Data: p.M{"type": "baseline", "requirementRevision": 1, "clarifications": []any{"OLD-ANSWER"}}},
		} {
			if err := tx.Put(foreign); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	runner, _ := f.runner(t, "developer")
	calls := 0
	f.h.ModelCall = func(ctx context.Context, actor p.Actor, in p.M) (p.M, error) {
		calls++
		body, _ := json.Marshal(in["messages"])
		text := string(body)
		if !strings.Contains(text, "REVIEWED-BRIEF") || !strings.Contains(text, "FROZEN-HUMAN") || strings.Contains(text, "UNAPPROVED-LIVE-ANSWER") || strings.Contains(text, "FOREIGN-ANSWER") || strings.Contains(text, "OLD-ANSWER") {
			return nil, fmt.Errorf("worker did not receive only the frozen current human decision: %s", text)
		}
		return actionResponse(p.M{"action": "finish", "summary": "Confirmed decision received; independent testing is still required."}), nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if worked, err := runner.RunOnce(ctx); err != nil || !worked {
		t.Fatal(worked, err)
	}
	if calls != 1 {
		t.Fatal("worker never reached the actual model callback", calls)
	}
	tasks, _ := f.svc.Store.List(f.project.ID, "task", f.goal.ID)
	for _, task := range tasks {
		if p.S(task.Data, "logicalId") == "confirmed" && task.Status != "succeeded" {
			t.Fatal("confirmed task failed", task)
		}
	}
}

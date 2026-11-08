package platform

import "time"

func governedAgentTask(task Resource) bool {
	_, blueprint := task.Data["blueprintSeed"]
	return Has([]string{"analysis", "code", "design"}, S(task.Data, "kind")) && len(Items(task.Data["command"])) == 0 && !blueprint && len(Strings(task.Data["children"])) == 0
}

type agentAllowance struct {
	budget    float64
	remaining time.Duration
	calls     int
	spent     float64
}

func (a agentAllowance) snapshot(task Resource) M {
	return M{"usedModelCalls": a.calls, "spentCents": a.spent, "activeSeconds": taskTimeout(task).Seconds() - a.remaining.Seconds(), "remainingCents": a.budget, "remainingSeconds": a.remaining.Seconds()}
}

func taskTimeout(task Resource) time.Duration {
	timeout := N(task.Data, "timeoutSeconds")
	if timeout <= 0 || timeout > 86400 {
		timeout = 900
	}
	return time.Duration(timeout) * time.Second
}

// Derive authority from committed runs and charge reservations, never a Worker
// report. A failed response still used a model call, including a zero-cost call.
func agentTaskAllowance(t *Tx, task Resource, currentRunID string) (agentAllowance, error) {
	var result agentAllowance
	budget, remaining, e := messageTaskAllowance(t, task, currentRunID)
	if e != nil {
		return result, e
	}
	runs, e := t.List(task.ProjectID, "run", task.GoalID)
	if e != nil {
		return result, e
	}
	byID := map[string]Resource{}
	counts := map[string]int{}
	for _, run := range runs {
		if S(run.Data, "taskId") == task.ID {
			byID[run.ID] = run
		}
	}
	charges, e := t.List(task.ProjectID, "charge", task.GoalID)
	if e != nil {
		return result, e
	}
	for _, charge := range charges {
		id := S(charge.Data, "runId")
		if _, ok := byID[id]; !ok {
			continue
		}
		if Has([]string{"reserved", "uncertain"}, charge.Status) {
			return result, conflict("工作项有结果未明的模型费用，需要先核对")
		}
		counts[id]++
		result.calls++
	}
	// Old/manual waits may record a higher step floor than historical charge
	// counts. Carry that floor through every later run, including failed retries.
	if floor := int(N(task.Data, "messageStepsUsed")); floor > result.calls {
		result.calls = floor
	}
	for id, run := range byID {
		if floor := int(N(Obj(run.Data["agentAllowance"]), "usedModelCalls")) + counts[id]; floor > result.calls {
			result.calls = floor
		}
	}
	if result.calls >= 24 {
		return result, conflict("工作项累计模型调用已达 24 次，需要重新规划")
	}
	result.budget, result.remaining, result.spent = budget, remaining, N(task.Data, "budgetCents")-budget
	return result, nil
}

// ValidateAgentModelAdmission runs inside the same transaction that reserves a
// model charge. Claims are snapshots: reconciliation, revocation or concurrent
// inference may have changed authority since the run was created.
func (s *Service) ValidateAgentModelAdmission(t *Tx, a Actor, in M, reserved float64) error {
	run, task, e := s.messageRun(t, a, in)
	if e != nil {
		return e
	}
	if !governedAgentTask(task) || messageTaskRecipe(task.Data) != messageTaskRecipe(Obj(run.Data["task"])) {
		return conflict("模型调用需要当前无固定命令的 Agent 工作项")
	}
	if e = workspaceWorker(t, a, task); e != nil {
		return e
	}
	if e = s.AgentProgressGate(t, task); e != nil {
		return e
	}
	allowance, e := agentTaskAllowance(t, task, run.ID)
	if e != nil {
		return e
	}
	if reserved > allowance.budget {
		return conflict("工作项累计模型预算不足")
	}
	return nil
}

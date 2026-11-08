package platform

import (
	"encoding/json"
	"fmt"
	"math"
)

// Live sibling work may proceed in parallel. An inactive or disabled origin
// with an unconfirmed action fences the whole goal, including replacement plans.
func (s *Service) AgentProgressRunGate(t *Tx, project, goal, runID string) error {
	steps, e := t.List(project, "agent_step", goal)
	if e != nil {
		return e
	}
	for _, step := range steps {
		if step.Status != "prepared" {
			continue
		}
		origin, e := t.Get(S(step.Data, "runId"))
		if e != nil {
			return e
		}
		worker, e := t.Get(S(step.Data, "workerId"))
		if e != nil {
			return e
		}
		if activeSourceLease(origin) && worker.Status != "disabled" {
			continue
		}
		return conflict("原方案 Agent 操作 " + step.ID + " 结果未明，需要先停止原进程并核对外部副作用")
	}
	return nil
}

// AgentProgressGate never infers that lease expiry stopped a tool process.
func (s *Service) AgentProgressGate(t *Tx, task Resource) error {
	steps, e := t.List(task.ProjectID, "agent_step", task.GoalID)
	if e != nil {
		return e
	}
	for _, step := range steps {
		if S(step.Data, "taskId") == task.ID && step.Status == "prepared" {
			return conflict("Agent 操作 " + step.ID + " 结果未确认，必须先核对原执行与外部副作用")
		}
	}
	return nil
}

func (s *Service) agentProgressAuthority(t *Tx, a Actor, in M) (Resource, Resource, error) {
	current, e := t.CurrentActor(a)
	if e != nil {
		return Resource{}, Resource{}, e
	}
	a = current
	if a.Kind != "worker" {
		return Resource{}, Resource{}, denied()
	}
	run, task, e := s.messageRun(t, a, in)
	if e != nil {
		return run, task, e
	}
	if !governedAgentTask(task) || messageTaskRecipe(task.Data) != messageTaskRecipe(Obj(run.Data["task"])) {
		return run, task, conflict("进度只属于当前批准的普通 Agent 工作项")
	}
	if e = workspaceWorker(t, a, task); e != nil {
		return run, task, e
	}
	_, _, e = messageTaskRemaining(t, task, run.ID)
	return run, task, e
}

func (s *Service) agentStepCommand(t *Tx, a Actor, command string, in M) (Resource, error) {
	if command == "agent_steps.reconcile" {
		return s.reconcileAgentStep(t, a, in)
	}
	if command == "agent_steps.complete" {
		return s.completeAgentStep(t, a, in)
	}
	for key := range in {
		if !Has([]string{"runId", "leaseToken", "step", "action", "source"}, key) {
			return Resource{}, invalid("操作意图包含未知字段")
		}
	}
	run, task, e := s.agentProgressAuthority(t, a, in)
	if e != nil {
		return run, e
	}
	n := N(in, "step")
	if math.IsNaN(n) || math.IsInf(n, 0) || n != math.Trunc(n) || n < 1 || n > 24 {
		return Resource{}, invalid("操作序号必须为 1–24 的整数")
	}
	action := Obj(in["action"])
	fields := map[string][]string{
		"read": {"path"}, "read_prototype": {"artifactId", "offset"},
		"write": {"path", "content", "executable"}, "delete": {"path"}, "restore_base": {"path"},
		"run": {"command"}, "read_input": {"commit", "path"}, "take_input": {"commit", "path"}, "resolve": {"paths"},
		"split": {"taskId", "children", "reason", "impact"}, "reorder": {"tasks", "reason", "impact"},
		"message": {"recipient", "recipientTaskId", "type", "payload", "causationId"},
	}
	allowed, ok := fields[S(action, "action")]
	if !ok {
		return Resource{}, invalid("只有非终结工具操作可以登记意图")
	}
	for key := range action {
		if key != "action" && !Has(allowed, key) {
			return Resource{}, invalid("操作包含未知或会话内部字段")
		}
	}
	body, e := json.Marshal(action)
	if e != nil || len(body) > 64000 {
		return Resource{}, invalid("操作意图超过 64 KB")
	}
	input := Obj(in["source"])
	if len(input) != 2 {
		return Resource{}, invalid("需要准确源码输入")
	}
	for _, key := range []string{"repositoryId", "commit"} {
		if _, ok := input[key].(string); !ok {
			return Resource{}, invalid("源码输入必须为字符串")
		}
	}
	if e = s.checkpointSource(t, task, run, input); e != nil {
		return Resource{}, e
	}
	// Exact replay authenticates the live authority, but returns the existing
	// state. A Worker must never execute a replayed prepared/completed intent.
	scope, key := "agent-step:"+run.ID, fmt.Sprint(int(n))
	previous, found, e := t.Once(scope, key, in)
	if e != nil || found {
		return previous, e
	}
	if e = s.AgentProgressGate(t, task); e != nil {
		return Resource{}, e
	}
	steps, e := t.List(task.ProjectID, "agent_step", task.GoalID)
	if e != nil {
		return Resource{}, e
	}
	for _, prior := range steps {
		if S(prior.Data, "runId") == run.ID && N(prior.Data, "step") >= n {
			return Resource{}, conflict("操作序号不能倒退")
		}
	}
	step := resource("agent_step", task.Title, run.ProjectID, run.GoalID, M{"version": 1, "runId": run.ID, "taskId": task.ID, "workerId": a.WorkerID, "planId": S(run.Data, "planId"), "requirementRevision": revision(run), "taskRecipe": messageTaskRecipe(task.Data), "source": copyM(input), "sourceBinding": copyM(Obj(task.Data["sourceBinding"])), "step": n, "action": copyM(action), "actionDigest": Hash(action), "previousId": S(task.Data, "agentProgressId")})
	step.ID = "agent_step_" + Hash([]string{run.ID, key})
	step.Status = "prepared"
	if e = t.Put(step); e != nil {
		return step, e
	}
	if e = t.Remember(scope, key, in, step); e != nil {
		return step, e
	}
	return step, nil
}

func (s *Service) completeAgentStep(t *Tx, a Actor, in M) (Resource, error) {
	for key := range in {
		if !Has([]string{"id", "runId", "leaseToken", "observation", "checkpointId"}, key) {
			return Resource{}, invalid("操作结果包含未知字段")
		}
	}
	run, task, e := s.agentProgressAuthority(t, a, in)
	if e != nil {
		return run, e
	}
	step, e := t.Get(S(in, "id"))
	if e != nil {
		return step, e
	}
	if step.Kind != "agent_step" || step.ProjectID != task.ProjectID || step.GoalID != task.GoalID || S(step.Data, "runId") != run.ID || S(step.Data, "taskRecipe") != messageTaskRecipe(task.Data) {
		return step, denied()
	}
	observation, ok := in["observation"].(string)
	if !ok || len(observation) > 16000 {
		return step, invalid("工具观察必须为不超过 16 KB 的字符串")
	}
	if step.Status == "completed" {
		if S(step.Data, "completionDigest") != Hash(in) {
			return step, conflict("操作已有不同结果")
		}
		return step, nil
	}
	if step.Status != "prepared" || S(task.Data, "agentProgressId") != S(step.Data, "previousId") {
		return step, conflict("操作进度已经变化")
	}
	if e = s.checkpointSource(t, task, run, Obj(step.Data["source"])); e != nil {
		return step, e
	}
	checkpointID := S(in, "checkpointId")
	if S(task.Data, "kind") == "analysis" {
		if checkpointID != "" {
			return step, invalid("分析结果不能包含工作区归档")
		}
	} else {
		checkpoint, err := t.Get(checkpointID)
		if err != nil {
			return step, err
		}
		if S(checkpoint.Data, "agentStepId") != step.ID {
			return step, conflict("工作区不是此准确操作的结果")
		}
		if e = s.validateAgentCheckpoint(t, checkpoint, task, Obj(step.Data["source"])); e != nil {
			return step, e
		}
		step.Data["checkpointId"], step.Data["checkpointDigest"] = checkpoint.ID, S(checkpoint.Data, "digest")
	}
	step.Status = "completed"
	step.Data["observation"], step.Data["completedAt"], step.Data["completionDigest"] = observation, now(), Hash(in)
	step.Data["binding"] = Hash(step.Data)
	task.Data["agentProgressId"] = step.ID
	if e = t.Put(step); e != nil {
		return step, e
	}
	return step, t.Put(task)
}

// Resume contains public tool evidence, never provider reasoning or an old
// system/user transcript. All restored observations remain untrusted data.
func (s *Service) agentResume(t *Tx, task Resource) (M, error) {
	id := S(task.Data, "agentProgressId")
	if id == "" {
		return nil, nil
	}
	if !governedAgentTask(task) {
		return nil, conflict("普通进度不能转为其他执行配方")
	}
	if e := s.AgentProgressGate(t, task); e != nil {
		return nil, e
	}
	latest, e := t.Get(id)
	if e != nil {
		return nil, e
	}
	input := Obj(latest.Data["source"])
	trace := []any{}
	seen := map[string]bool{}
	for current := latest; ; {
		data := copyM(current.Data)
		delete(data, "binding")
		if seen[current.ID] || current.Kind != "agent_step" || current.Status != "completed" || current.ProjectID != task.ProjectID || current.GoalID != task.GoalID || S(data, "taskId") != task.ID || S(data, "planId") != S(task.Data, "planId") || revision(current) != revision(task) || S(data, "taskRecipe") != messageTaskRecipe(task.Data) || Hash(data["source"]) != Hash(input) || Hash(data["sourceBinding"]) != Hash(Obj(task.Data["sourceBinding"])) || S(current.Data, "binding") != Hash(data) || S(data, "actionDigest") != Hash(data["action"]) {
			return nil, conflict("已确认 Agent 进度与当前工作项不一致")
		}
		seen[current.ID] = true
		origin, err := t.Get(S(data, "runId"))
		if err != nil || origin.Kind != "run" || S(origin.Data, "taskId") != task.ID || origin.ProjectID != task.ProjectID || origin.GoalID != task.GoalID || revision(origin) != revision(task) || messageTaskRecipe(Obj(origin.Data["task"])) != messageTaskRecipe(task.Data) {
			return nil, conflict("Agent 进度缺少准确原执行")
		}
		if e = s.checkpointSource(t, task, origin, input); e != nil {
			return nil, e
		}
		observation := S(data, "observation")
		if len(observation) > 8000 {
			observation = observation[len(observation)-8000:]
		}
		entry := M{"id": current.ID, "runId": origin.ID, "step": data["step"], "action": S(Obj(data["action"]), "action"), "actionDigest": data["actionDigest"], "observation": observation}
		candidate := append([]any{entry}, trace...)
		encoded, _ := json.Marshal(candidate)
		if len(encoded) <= 32000 {
			trace = candidate
		}
		previous := S(data, "previousId")
		if previous == "" {
			break
		}
		if len(seen) >= 24 {
			return nil, conflict("Agent 进度链超出任务上限")
		}
		current, e = t.Get(previous)
		if e != nil {
			return nil, e
		}
	}
	resume := M{"version": 1, "stepId": latest.ID, "source": copyM(input), "trace": trace, "checkpointId": S(latest.Data, "checkpointId"), "checkpointDigest": S(latest.Data, "checkpointDigest")}
	if S(task.Data, "kind") != "analysis" {
		checkpoint, err := t.Get(S(latest.Data, "checkpointId"))
		if err != nil {
			return nil, err
		}
		if S(checkpoint.Data, "agentStepId") != latest.ID || S(checkpoint.Data, "digest") != S(latest.Data, "checkpointDigest") {
			return nil, conflict("进度缺少准确文件字节")
		}
		if e = s.validateAgentCheckpoint(t, checkpoint, task, input); e != nil {
			return nil, e
		}
		resume["integration"] = checkpoint.Data["integration"]
	}
	return resume, nil
}

func (s *Service) reconcileAgentStep(t *Tx, a Actor, in M) (Resource, error) {
	if e := human(a); e != nil {
		return Resource{}, e
	}
	step, e := s.scoped(t, a, S(in, "id"), "agent_step", "owner")
	if e != nil {
		return step, e
	}
	for key := range in {
		if !Has([]string{"id", "key", "actionDigest", "externalReconciled", "reason"}, key) {
			return step, invalid("操作核对包含未知字段")
		}
	}
	if !IDPattern.MatchString(S(in, "key")) || S(in, "actionDigest") != S(step.Data, "actionDigest") || !B(in, "externalReconciled") {
		return step, invalid("必须核对准确操作及其进程和外部副作用")
	}
	if e = validateText(S(in, "reason"), "核对依据", 2000); e != nil {
		return step, e
	}
	scope := "agent-step-reconcile:" + step.ID
	previous, found, e := t.Once(scope, S(in, "key"), in)
	if e != nil || found {
		return previous, e
	}
	run, e := t.Get(S(step.Data, "runId"))
	if e != nil {
		return step, e
	}
	worker, e := t.Get(S(step.Data, "workerId"))
	if e != nil || worker.Kind != "worker" || worker.ProjectID != step.ProjectID || worker.Status != "disabled" || activeSourceLease(run) {
		return step, conflict("请先独立停止并停用原节点，等待原租约失效")
	}
	if step.Status != "prepared" {
		return step, conflict("操作已确认或核对")
	}
	step.Status = "abandoned"
	step.Data["reconciledBy"], step.Data["reconciledAt"], step.Data["reason"], step.Data["externalReconciled"] = a.ID, now(), S(in, "reason"), true
	if e = t.Put(step); e != nil {
		return step, e
	}
	return step, t.Remember(scope, S(in, "key"), in, step)
}

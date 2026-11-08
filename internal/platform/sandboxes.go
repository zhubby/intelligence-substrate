package platform

import (
	"fmt"
	"strings"
	"time"
)

// SandboxRunGate fences result publication and replacement work. A live sibling
// may run in parallel; an unknown or expired sibling may still have side effects.
func (s *Service) SandboxRunGate(t *Tx, project, goal, runID string) error {
	items, e := t.List(project, "sandbox", goal)
	if e != nil {
		return e
	}
	for _, item := range items {
		if item.Status == "stopped" {
			continue
		}
		run, e := t.Get(S(item.Data, "runId"))
		if e != nil {
			return e
		}
		if run.ID != runID && item.Status == "prepared" && run.Status == "running" {
			lease, leaseErr := time.Parse(time.RFC3339Nano, S(run.Data, "leaseUntil"))
			deadline, deadlineErr := time.Parse(time.RFC3339Nano, S(run.Data, "deadline"))
			worker, workerErr := t.Get(S(run.Data, "workerId"))
			if workerErr != nil {
				return workerErr
			}
			if leaseErr == nil && deadlineErr == nil && time.Now().Before(lease) && time.Now().Before(deadline) && worker.Status != "disabled" {
				continue
			}
		}
		return conflict(fmt.Sprintf("AX 客体 %s 尚未确认停止，需要核对原节点与资源 %s", item.ID, S(item.Data, "name")))
	}
	return nil
}

func (s *Service) sandboxCommand(t *Tx, a Actor, command string, in M) (Resource, error) {
	if command == "sandboxes.prepare" {
		return s.prepareSandbox(t, a, in)
	}
	remote, e := s.scoped(t, a, S(in, "id"), "sandbox")
	if e != nil {
		return remote, e
	}
	run, e := t.Get(S(remote.Data, "runId"))
	if e != nil || run.Kind != "run" || run.ProjectID != remote.ProjectID || run.GoalID != remote.GoalID || S(run.Data, "workerId") != S(remote.Data, "workerId") {
		return remote, conflict("客体缺少一致的原执行记录")
	}
	if S(in, "name") != S(remote.Data, "name") || S(in, "backendDigest") != S(remote.Data, "backendDigest") {
		return remote, conflict("必须核对准确的原客体和执行后端")
	}
	if e = validateText(S(in, "observation"), "客体清理依据", 2000); e != nil {
		return remote, e
	}
	if e = noSecrets(in); e != nil {
		return remote, e
	}
	if command == "sandboxes.finish" {
		for key := range in {
			if !Has([]string{"id", "leaseToken", "name", "backendDigest", "stopped", "observation"}, key) {
				return remote, invalid("客体回执包含未知字段")
			}
		}
		stopped, ok := in["stopped"].(bool)
		if !ok || a.Kind != "worker" || a.WorkerID != S(remote.Data, "workerId") || a.ID != "worker:"+a.WorkerID || S(in, "leaseToken") == "" || Hash(S(in, "leaseToken")) != S(remote.Data, "leaseHash") {
			return remote, denied()
		}
		worker, err := t.Get(a.WorkerID)
		if err != nil || worker.Status == "disabled" {
			return remote, denied()
		}
		if digest := S(remote.Data, "finishDigest"); digest != "" {
			if digest != Hash(in) {
				return remote, conflict("客体已经提交不同的清理回执")
			}
			return remote, nil
		}
		if remote.Status != "prepared" {
			return remote, conflict("客体已有人工核对结果")
		}
		remote.Status = "unresolved"
		if stopped {
			remote.Status = "stopped"
		}
		remote.Data["finishDigest"], remote.Data["observation"], remote.Data["reportedBy"], remote.Data["reportedAt"] = Hash(in), S(in, "observation"), a.ID, now()
		return remote, t.Put(remote)
	}
	if e = human(a); e != nil {
		return remote, e
	}
	if e = s.allowed(t, a, remote.ProjectID, "owner"); e != nil {
		return remote, e
	}
	for key := range in {
		if !Has([]string{"id", "key", "name", "backendDigest", "observation"}, key) {
			return remote, invalid("客体核对包含未知字段")
		}
	}
	if !IDPattern.MatchString(S(in, "key")) {
		return remote, invalid("需要有效核对幂等键")
	}
	scope := "sandbox-reconcile:" + remote.ID
	previous, found, e := t.Once(scope, S(in, "key"), in)
	if e != nil || found {
		return previous, e
	}
	worker, e := t.Get(S(remote.Data, "workerId"))
	if e != nil || worker.Kind != "worker" || worker.ProjectID != remote.ProjectID || worker.Status != "disabled" || activeSourceLease(run) {
		return remote, conflict("请先停止并停用原执行节点，等待原租约失效后核对")
	}
	if remote.Status != "prepared" && remote.Status != "unresolved" {
		return remote, conflict("客体已确认停止")
	}
	remote.Status = "stopped"
	remote.Data["reconciledBy"], remote.Data["reconciledAt"], remote.Data["observation"] = a.ID, now(), S(in, "observation")
	if e = t.Put(remote); e != nil {
		return remote, e
	}
	return remote, t.Remember(scope, S(in, "key"), in, remote)
}

func (s *Service) prepareSandbox(t *Tx, a Actor, in M) (Resource, error) {
	if a.Kind != "worker" {
		return Resource{}, denied()
	}
	for key := range in {
		if !Has([]string{"runId", "leaseToken", "key", "backend", "command"}, key) {
			return Resource{}, invalid("客体准备包含未知字段")
		}
	}
	if !IDPattern.MatchString(S(in, "key")) {
		return Resource{}, invalid("需要有效客体幂等键")
	}
	run, e := s.scoped(t, a, S(in, "runId"), "run")
	if e != nil {
		return run, e
	}
	if a.ID != "worker:"+a.WorkerID || S(run.Data, "workerId") != a.WorkerID || run.Status != "running" || S(in, "leaseToken") == "" || S(run.Data, "leaseToken") != S(in, "leaseToken") {
		return Resource{}, denied()
	}
	for _, key := range []string{"leaseUntil", "deadline"} {
		until, err := time.Parse(time.RFC3339Nano, S(run.Data, key))
		if err != nil || !time.Now().Before(until) {
			return Resource{}, conflict("客体创建需要有效租约和截止时间")
		}
	}
	if e = s.validWorkerRunAuthority(t, a, "sandboxes.prepare", in); e != nil {
		return Resource{}, e
	}
	task, e := t.Get(S(run.Data, "taskId"))
	if e != nil {
		return Resource{}, e
	}
	if S(task.Data, "runtime") != "linux" || publicationTask(task) || controlledSigningTask(task) || operationalTask(task) || task.Status != "running" {
		return Resource{}, denied()
	}
	if e = workspaceWorker(t, a, task); e != nil {
		return Resource{}, e
	}
	g, e := t.Get(run.GoalID)
	if e != nil {
		return Resource{}, e
	}
	if S(task.Data, "planId") != "" && (S(task.Data, "planId") != S(g.Data, "activePlanId") || S(run.Data, "planId") != S(task.Data, "planId")) {
		return Resource{}, conflict("客体需要原工作项的当前批准方案")
	}
	if e = s.validateQualityTask(t, task); e != nil {
		return Resource{}, e
	}
	expected := Strings(task.Data["command"])
	if S(task.Data, "kind") == "test" {
		pipeline, err := t.Get(S(task.Data, "pipelineId"))
		if err != nil || pipeline.Kind != "pipeline" || pipeline.Revision != int(N(task.Data, "pipelineRevision")) {
			return Resource{}, conflict("独立质量管线已变化")
		}
		expected = Strings(pipeline.Data["command"])
	}
	if len(expected) > 0 && Hash(expected) != Hash(Strings(in["command"])) {
		return Resource{}, conflict("AX 命令必须保留工作项或独立质量管线批准的 argv")
	}
	backend := Obj(in["backend"])
	if e = ValidateAXBackend(backend); e != nil {
		return Resource{}, e
	}
	args := Strings(in["command"])
	if len(args) == 0 || len(args) > 128 || len(args) != len(Items(in["command"])) || args[0] == "" || strings.HasPrefix(args[0], "-") {
		return Resource{}, invalid("需要有效命令 argv")
	}
	total := 0
	for _, arg := range args {
		total += len(arg)
		if strings.ContainsRune(arg, 0) {
			return Resource{}, invalid("命令不能包含 NUL")
		}
	}
	if total > 16000 {
		return Resource{}, invalid("命令过长")
	}
	scope := "sandbox-prepare:" + run.ID
	previous, found, e := t.Once(scope, S(in, "key"), in)
	if e != nil {
		return previous, e
	}
	if found {
		previous.Data = copyM(previous.Data)
		previous.Data["replayed"] = true
		return previous, nil
	}
	if e = s.SandboxRunGate(t, run.ProjectID, run.GoalID, run.ID); e != nil {
		return Resource{}, e
	}
	remote := resource("sandbox", "AX command", run.ProjectID, run.GoalID, M{"version": 1, "runId": run.ID, "taskId": task.ID, "workerId": a.WorkerID, "planId": S(run.Data, "planId"), "requirementRevision": revision(run), "leaseHash": Hash(S(in, "leaseToken")), "backend": copyM(backend), "backendDigest": Hash(backend), "commandDigest": Hash(args)})
	remote.Status = "prepared"
	remote.Data["name"] = "is-" + strings.ReplaceAll(remote.ID, "_", "-")
	if e = t.Put(remote); e != nil {
		return remote, e
	}
	if e = t.Remember(scope, S(in, "key"), in, remote); e != nil {
		return remote, e
	}
	remote.Data = copyM(remote.Data)
	remote.Data["replayed"] = false
	return remote, nil
}

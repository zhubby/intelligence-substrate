package platform

import (
	"strconv"
	"strings"
	"time"
)

func operationalTask(task Resource) bool { return S(task.Data, "kind") == "runtime_rollback" }

func (s *Service) recordRuntimeDeployment(t *Tx, a Actor, release Resource, result M) error {
	info := Obj(result["runtime"])
	if len(info) == 0 {
		return nil
	}
	external := S(result, "externalId")
	app := S(info, "application")
	if S(info, "driver") != "docker" || !IDPattern.MatchString(app) || !strings.HasPrefix(external, "runtime:"+app+":") || !digestPattern.MatchString(strings.TrimPrefix(external, "runtime:"+app+":")) || S(info, "activationId") != external || !digestPattern.MatchString(strings.TrimPrefix(S(info, "imageId"), "sha256:")) || !strings.HasPrefix(S(info, "imageId"), "sha256:") || !IDPattern.MatchString(S(info, "dataContract")) {
		return invalid("运行节点必须返回准确的应用、激活版本与镜像摘要")
	}
	target := releaseTarget(release, "web")
	if target == nil || S(target, "externalId") != external || S(target, "status") != "published" || external != "runtime:"+app+":"+S(target, "operationKey") {
		return invalid("运行部署必须绑定 Web 发布目标")
	}
	existing, e := t.List(release.ProjectID, "deployment", release.GoalID)
	if e != nil {
		return e
	}
	for _, old := range existing {
		if S(old.Data, "externalId") == external {
			return conflict("运行激活标识已被其他发布使用")
		}
		if old.Status == "active" {
			if S(old.Data, "application") != app || S(old.Data, "workerId") != a.WorkerID || S(old.Data, "dataContract") != S(info, "dataContract") || S(old.Data, "url") != S(result, "url") {
				return conflict("运行发布不能静默改变应用、节点、数据契约或公开入口")
			}
			old.Status = "superseded"
			if e = t.Put(old); e != nil {
				return e
			}
		}
	}
	artifact, e := t.Get(S(target, "artifactId"))
	if e != nil {
		return e
	}
	d := resource("deployment", release.Title, release.ProjectID, release.GoalID, M{"releaseId": release.ID, "version": S(release.Data, "version"), "artifactId": artifact.ID, "artifactDigest": S(artifact.Data, "digest"), "artifactSize": N(artifact.Data, "size"), "commit": S(release.Data, "commit"), "binding": S(release.Data, "binding"), "externalId": external, "operationKey": S(target, "operationKey"), "workerId": a.WorkerID, "application": app, "imageId": S(info, "imageId"), "dataContract": S(info, "dataContract"), "url": S(result, "url"), "health": "unknown", "verificationStatus": "required", "activatedBy": a.ID, "approvedBy": S(release.Data, "approvedBy"), "activatedAt": now(), "lastSequence": 0})
	return t.Put(d)
}
func runtimeView(r Resource) Resource {
	deadline, err := time.Parse(time.RFC3339Nano, S(r.Data, "validUntil"))
	if r.Kind == "deployment" && r.Status == "active" && (err != nil || !deadline.After(time.Now())) {
		r.Data = copyM(r.Data)
		r.Data["health"] = "unknown"
		r.Data["signalExpired"] = true
	}
	return r
}
func (s *Service) runtimeCommand(t *Tx, a Actor, cmd string, in M) (Resource, error) {
	if cmd == "runtime.report" {
		return s.reportRuntime(t, a, in)
	}
	if cmd == "runtime.rollback.propose" {
		from, e := s.scoped(t, a, S(in, "id"), "deployment", "release", "developer", "architect")
		if e != nil {
			return from, e
		}
		to, e := s.scoped(t, a, S(in, "destinationId"), "deployment", "release", "developer", "architect")
		if e != nil {
			return to, e
		}
		if from.Status != "active" || to.Status != "superseded" || from.ProjectID != to.ProjectID || from.GoalID != to.GoalID || S(from.Data, "application") != S(to.Data, "application") || S(from.Data, "workerId") != S(to.Data, "workerId") || S(from.Data, "dataContract") != S(to.Data, "dataContract") {
			return from, conflict("需要同一应用、节点和数据契约的当前版本与历史版本")
		}
		if e = validateText(S(in, "reason"), "回滚原因", 2000); e != nil {
			return from, e
		}
		scope := "rollback:" + from.ID
		if previous, ok, e := t.Once(scope, S(in, "key"), in); e != nil || ok {
			return previous, e
		}
		if e = s.runtimeSwitchAvailable(t, from.ProjectID, from.GoalID, ""); e != nil {
			return from, e
		}
		r := resource("rollback", "回滚至 "+to.Title, from.ProjectID, from.GoalID, M{"sourceId": from.ID, "destinationId": to.ID, "expectedActive": S(from.Data, "externalId"), "rollbackTo": S(to.Data, "externalId"), "reason": S(in, "reason"), "workerId": S(from.Data, "workerId"), "proposedBy": a.ID})
		r.Status = "awaiting_approval"
		r.Data["approvalBinding"] = Hash(M{"source": from.ID, "expectedActive": S(from.Data, "externalId"), "destination": to.ID, "artifact": S(to.Data, "artifactDigest"), "image": S(to.Data, "imageId"), "workerId": S(from.Data, "workerId"), "reason": S(in, "reason")})
		if e = t.Put(r); e != nil {
			return r, e
		}
		return r, t.Remember(scope, S(in, "key"), in, r)
	}
	if cmd == "runtime.verify" {
		d, e := s.scoped(t, a, S(in, "id"), "deployment", "quality")
		if e != nil {
			return d, e
		}
		if e = human(a); e != nil {
			return d, e
		}
		if d.Status != "active" || a.ID == S(d.Data, "activatedBy") || a.ID == S(d.Data, "approvedBy") {
			return d, conflict("需要当前部署的独立运行验证")
		}
		artifact, e := t.Get(S(d.Data, "artifactId"))
		if e != nil {
			return d, e
		}
		if S(artifact.Data, "producerId") == a.ID {
			return d, conflict("制品生产者不能独立验收自己的部署")
		}
		if e = validateText(S(in, "statement"), "独立运行验收记录", 12000); e != nil {
			return d, e
		}
		proof, e := s.runtimeProof(t, a, d, in)
		if e != nil {
			return d, e
		}
		ev := resource("evidence", "运行版本独立验收", d.ProjectID, d.GoalID, M{"purpose": "runtime", "deploymentId": d.ID, "artifactId": artifact.ID, "digest": S(artifact.Data, "digest"), "commit": S(d.Data, "commit"), "externalId": S(d.Data, "externalId"), "proofArtifactId": proof.ID, "actorId": a.ID, "method": "manual", "verificationEnvironment": "browser", "source": "human-attested", "statement": S(in, "statement")})
		ev.Status = "passed"
		if e = t.Put(ev); e != nil {
			return d, e
		}
		d.Data["verificationStatus"] = "verified"
		d.Data["verificationEvidenceId"] = ev.ID
		return d, t.Put(d)
	}
	r, e := s.scoped(t, a, S(in, "id"), "rollback", "release")
	if e != nil {
		return r, e
	}
	if e = human(a); e != nil {
		return r, e
	}
	from, e := t.Get(S(r.Data, "sourceId"))
	if e != nil {
		return r, e
	}
	to, e := t.Get(S(r.Data, "destinationId"))
	if e != nil {
		return r, e
	}
	if from.Status != "active" || S(from.Data, "externalId") != S(r.Data, "expectedActive") {
		return r, conflict("当前部署已经变化，旧回滚决定不能执行")
	}
	if cmd == "runtime.rollback.approve" {
		if r.Status != "awaiting_approval" {
			return r, conflict("回滚当前不可审批")
		}
		artifact, err := t.Get(S(to.Data, "artifactId"))
		if err != nil {
			return r, err
		}
		if S(artifact.Data, "producerId") == a.ID {
			return r, conflict("回滚批准人不能是目标制品生产者")
		}
		if e = s.runtimeSwitchAvailable(t, r.ProjectID, r.GoalID, r.ID); e != nil {
			return r, e
		}
		worker, e := s.scoped(t, a, S(r.Data, "workerId"), "worker", "release")
		if e != nil {
			return r, e
		}
		if worker.Status == "disabled" || !Has(Strings(worker.Data["roles"]), "release") || !Has(Strings(worker.Data["capabilities"]), "publish") {
			return r, conflict("需要仍获授权的可信发布节点")
		}
		operation := Hash(M{"rollback": r.ID, "approvalBinding": S(r.Data, "approvalBinding")})
		external := S(from.Data, "externalId")
		prefix := external[:strings.LastIndex(external, ":")+1]
		data := copyM(to.Data)
		data["kind"] = "runtime_rollback"
		data["role"] = "release"
		data["runtime"] = S(worker.Data, "runtime")
		data["assignedWorkerId"] = worker.ID
		data["rollbackId"] = r.ID
		data["expectedActive"] = S(r.Data, "expectedActive")
		data["rollbackTo"] = S(r.Data, "rollbackTo")
		data["operationKey"] = operation
		data["expectedExternalId"] = prefix + operation
		data["target"] = "web"
		data["timeoutSeconds"] = 180
		data["maxAttempts"] = 1
		data["attempts"] = 0
		data["budgetCents"] = 0
		task := resource("task", r.Title, r.ProjectID, r.GoalID, data)
		task.Status = "pending"
		if e = t.Put(task); e != nil {
			return r, e
		}
		r.Status = "approved"
		r.Data["approvedBy"] = a.ID
		r.Data["approvedAt"] = now()
		r.Data["taskId"] = task.ID
		r.Data["operationKey"] = operation
		r.Data["expectedExternalId"] = prefix + operation
		approval := resource("approval", "生产回滚批准", r.ProjectID, r.GoalID, M{"type": "runtime_rollback", "rollbackId": r.ID, "binding": S(r.Data, "approvalBinding"), "actorId": a.ID})
		if e = t.Put(approval); e != nil {
			return r, e
		}
		return r, t.Put(r)
	}
	if cmd != "runtime.rollback.reconcile" || r.Status != "reconciliation_required" {
		return r, conflict("回滚结果不明时才可显式核对")
	}
	if e = validateText(S(in, "reason"), "核对结论", 2000); e != nil {
		return r, e
	}
	if !Has([]string{"succeeded", "failed"}, S(in, "state")) {
		return r, invalid("核对必须确认目标激活成功或原版本仍在运行")
	}
	proofDeployment := to
	if S(in, "state") == "failed" {
		proofDeployment = from
	}
	proof, proofErr := s.runtimeProof(t, a, proofDeployment, in)
	if proofErr != nil {
		return r, proofErr
	}
	if proof.CreatedAt < S(r.Data, "approvedAt") {
		return r, conflict("核对附件必须在此次回滚批准之后采集")
	}
	task, e := t.Get(S(r.Data, "taskId"))
	if e != nil {
		return r, e
	}
	if S(in, "state") == "succeeded" {
		result := M{"externalId": S(r.Data, "expectedExternalId"), "state": "published"}
		if e = s.activateRollback(t, r, task, result); e != nil {
			return r, e
		}
	}
	runs, e := t.List(r.ProjectID, "run", r.GoalID)
	if e != nil {
		return r, e
	}
	for _, run := range runs {
		if S(run.Data, "taskId") == task.ID && run.Status == "running" {
			run.Status = "cancelled"
			delete(run.Data, "leaseToken")
			if e = t.Put(run); e != nil {
				return r, e
			}
		}
	}
	task.Status = "cancelled"
	if e = t.Put(task); e != nil {
		return r, e
	}
	r.Status = S(in, "state")
	r.Data["reconciledBy"] = a.ID
	r.Data["reconciliationReason"] = S(in, "reason")
	r.Data["proofArtifactId"] = S(in, "proofArtifactId")
	return r, t.Put(r)
}
func (s *Service) runtimeProof(t *Tx, a Actor, d Resource, in M) (Resource, error) {
	proof, e := s.scoped(t, a, S(in, "proofArtifactId"), "artifact")
	if e != nil {
		return proof, e
	}
	if S(proof.Data, "purpose") == "release-feedback" || proof.GoalID != d.GoalID || S(proof.Data, "type") != "report" || S(proof.Data, "commit") != S(d.Data, "commit") || S(proof.Data, "target") != "web" || proof.CreatedAt < S(d.Data, "activatedAt") {
		return proof, conflict("需要准确提交和目标的实际运行报告附件")
	}
	if claimed := S(proof.Data, "deploymentId"); claimed != "" && claimed != d.ID {
		return proof, conflict("运行报告属于其他激活版本")
	}
	return proof, s.VerifyArtifact(proof)
}
func (s *Service) runtimeSwitchAvailable(t *Tx, project, goal, except string) error {
	operations, e := t.List(project, "rollback", goal)
	if e != nil {
		return e
	}
	for _, r := range operations {
		if r.ID != except && Has([]string{"approved", "running", "reconciliation_required"}, r.Status) {
			return conflict("已有回滚正在执行或等待核对")
		}
	}
	tasks, e := t.List(project, "task", goal)
	if e != nil {
		return e
	}
	for _, task := range tasks {
		if S(task.Data, "kind") == "publish" && Has([]string{"pending", "running", "blocked"}, task.Status) {
			return conflict("已有发布操作未完成核对")
		}
	}
	return nil
}
func (s *Service) reportRuntime(t *Tx, a Actor, in M) (Resource, error) {
	if a.Kind != "worker" || a.ID != "worker:"+a.WorkerID {
		return Resource{}, denied()
	}
	worker, e := s.scoped(t, a, a.WorkerID, "worker", "release")
	if e != nil {
		return worker, e
	}
	if worker.Status == "disabled" || !Has(Strings(worker.Data["roles"]), "release") || !Has(Strings(worker.Data["capabilities"]), "publish") {
		return worker, denied()
	}
	deployments, e := t.List(worker.ProjectID, "deployment", "")
	if e != nil {
		return worker, e
	}
	var d Resource
	for _, value := range deployments {
		if value.Status == "active" && S(value.Data, "externalId") == S(in, "externalId") && S(value.Data, "workerId") == worker.ID {
			d = value
			break
		}
	}
	if d.ID == "" || S(d.Data, "releaseId") != S(in, "releaseId") || S(d.Data, "binding") != S(in, "binding") {
		return d, conflict("运行观察与当前激活版本不匹配")
	}
	sequence := N(in, "sequence")
	if sequence < 1 || sequence != float64(int64(sequence)) || sequence > 9007199254740991 {
		return d, invalid("运行观察需要递增的持久序号")
	}
	scope := "runtime-report:" + d.ID
	key := S(in, "externalId") + ":" + strconv.FormatInt(int64(sequence), 10)
	if old, ok, e := t.Once(scope, key, in); e != nil || ok {
		return old, e
	}
	checked, e := time.Parse(time.RFC3339Nano, S(in, "checkedAt"))
	received := time.Now()
	if e != nil || checked.After(received.Add(30*time.Second)) || !checked.Add(runtimeSignalTTL).After(received) || S(in, "checkedAt") < S(d.Data, "activatedAt") {
		return d, invalid("运行观察必须来自当前激活后的近期真实检查")
	}
	if sequence <= N(d.Data, "lastSequence") {
		return d, conflict("旧运行观察不能覆盖较新结果")
	}
	if previous, err := time.Parse(time.RFC3339Nano, S(d.Data, "lastCheckedAt")); err == nil && !checked.After(previous) {
		return d, conflict("运行检查时间必须晚于上一次已接收检查")
	}
	health := S(in, "health")
	code := N(in, "statusCode")
	if !Has([]string{"healthy", "unhealthy"}, health) || code < 0 || code > 599 || N(in, "latencyMs") < 0 || N(in, "latencyMs") > 60000 || (health == "healthy" && (code < 200 || code >= 300)) {
		return d, invalid("运行检查结果无效")
	}
	if e = detectRuntimeSilence(t, d, checked); e != nil {
		return d, e
	}
	if previousExpiry, err := time.Parse(time.RFC3339Nano, S(d.Data, "validUntil")); err != nil || !checked.Before(previousExpiry) {
		// An observation gap cannot establish continuous failure or recovery.
		d.Data["failures"] = 0
		d.Data["successes"] = 0
	}
	d.Data["health"] = health
	d.Data["statusCode"] = code
	d.Data["latencyMs"] = N(in, "latencyMs")
	d.Data["lastCheckedAt"] = S(in, "checkedAt")
	d.Data["validUntil"] = checked.Add(runtimeSignalTTL).UTC().Format(time.RFC3339Nano)
	d.Data["lastSequence"] = sequence
	if health == "healthy" {
		d.Data["successes"] = N(d.Data, "successes") + 1
		d.Data["failures"] = 0
	} else {
		d.Data["failures"] = N(d.Data, "failures") + 1
		d.Data["successes"] = 0
	}
	if e = resolveRuntimeIncidents(t, d, "monitor_signal_lost", "signal_resumed", checked); e != nil {
		return d, e
	}
	if N(d.Data, "failures") >= 3 {
		if _, e = openRuntimeIncident(t, d, "application_unhealthy", checked, M{"statusCode": code, "latencyMs": N(in, "latencyMs")}); e != nil {
			return d, e
		}
	}
	if N(d.Data, "successes") >= 2 {
		if e = resolveRuntimeIncidents(t, d, "application_unhealthy", "healthy_probes", checked); e != nil {
			return d, e
		}
	}
	if e = t.Put(d); e != nil {
		return d, e
	}
	return d, t.Remember(scope, key, in, d)
}
func (s *Service) validRuntimeTask(t *Tx, task, worker Resource) bool {
	r, e := t.Get(S(task.Data, "rollbackId"))
	if e != nil || !Has([]string{"approved", "running"}, r.Status) || S(r.Data, "taskId") != task.ID || S(r.Data, "workerId") != worker.ID {
		return false
	}
	source, e := t.Get(S(r.Data, "sourceId"))
	return e == nil && source.Status == "active" && S(source.Data, "externalId") == S(task.Data, "expectedActive")
}
func (s *Service) rollbackUncertain(t *Tx, task Resource) error {
	r, e := t.Get(S(task.Data, "rollbackId"))
	if e != nil {
		return e
	}
	r.Status = "reconciliation_required"
	r.Data["reasonForReconciliation"] = "执行失败、中断或超时，先核对实际激活版本；不能自动重试"
	return t.Put(r)
}
func (s *Service) activateRollback(t *Tx, r, task Resource, result M) error {
	if S(result, "state") != "published" || S(result, "externalId") != S(task.Data, "expectedExternalId") {
		return invalid("回滚必须返回批准操作对应的准确激活标识")
	}
	from, e := t.Get(S(r.Data, "sourceId"))
	if e != nil {
		return e
	}
	to, e := t.Get(S(r.Data, "destinationId"))
	if e != nil {
		return e
	}
	if from.Status != "active" || S(from.Data, "externalId") != S(task.Data, "expectedActive") {
		return conflict("运行版本已变化，旧回滚结果不能覆盖新部署")
	}
	from.Status = "superseded"
	if e = t.Put(from); e != nil {
		return e
	}
	data := copyM(to.Data)
	for _, key := range []string{"verificationEvidenceId", "lastCheckedAt", "validUntil", "signalExpired"} {
		delete(data, key)
	}
	data["externalId"] = S(result, "externalId")
	data["operationKey"] = S(task.Data, "operationKey")
	data["activatedBy"] = S(r.Data, "approvedBy")
	data["approvedBy"] = S(r.Data, "approvedBy")
	data["activatedAt"] = now()
	data["sourceDeploymentId"] = to.ID
	data["rollbackId"] = r.ID
	data["health"] = "unknown"
	data["verificationStatus"] = "required"
	data["lastSequence"] = 0
	data["failures"] = 0
	data["successes"] = 0
	d := resource("deployment", to.Title+"（回滚）", to.ProjectID, to.GoalID, data)
	return t.Put(d)
}
func (s *Service) finishRuntimeTask(t *Tx, task Resource, succeeded bool, result M) error {
	r, e := t.Get(S(task.Data, "rollbackId"))
	if e != nil {
		return e
	}
	if !succeeded {
		return s.rollbackUncertain(t, task)
	}
	if e = s.activateRollback(t, r, task, result); e != nil {
		return e
	}
	r.Status = "succeeded"
	r.Data["completedAt"] = now()
	return t.Put(r)
}

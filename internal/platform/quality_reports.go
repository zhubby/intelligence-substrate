package platform

import (
	"path"
	"regexp"
	"strings"
)

var reportSegment = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
var windowsDevice = regexp.MustCompile(`^(CON|PRN|AUX|NUL|COM[1-9]|LPT[1-9])$`)

// ValidateReportPath keeps collection portable and away from controller inputs.
// SafePath separately checks the actual workspace for symlinks at execution time.
func ValidateReportPath(name string) error {
	if name == "" || len(name) > 512 || path.Clean(name) != name || strings.HasPrefix(name, "/") {
		return invalid("报告路径必须是工作区内的普通相对文件路径")
	}
	for _, part := range strings.Split(name, "/") {
		lower := strings.ToLower(part)
		if !reportSegment.MatchString(part) || len(part) > 128 || part == "." || part == ".." || strings.HasSuffix(part, ".") || lower == ".git" || strings.HasPrefix(lower, ".intelligence-") || windowsDevice.MatchString(strings.ToUpper(strings.SplitN(part, ".", 2)[0])) {
			return invalid("报告路径包含不安全或保留的文件名")
		}
	}
	return nil
}

func (s *Service) validateQualityReport(t *Tx, a Actor, run, task Resource, result M, required bool) (Resource, error) {
	id := S(result, "reportArtifactId")
	if id == "" {
		if required {
			return Resource{}, conflict("测试成功但缺少策略要求的本次报告")
		}
		return Resource{}, nil
	}
	worker, err := t.Get(S(run.Data, "workerId"))
	if err != nil || worker.Status == "disabled" || a.ID != "worker:"+worker.ID || !B(worker.Data, "trustedQuality") || !Has(Strings(worker.Data["roles"]), "quality") || !Has(Strings(worker.Data["capabilities"]), "test") || S(worker.Data, "runtime") != S(task.Data, "runtime") {
		return Resource{}, denied()
	}
	artifact, err := t.Get(S(task.Data, "artifactId"))
	if err != nil || artifact.Kind != "artifact" || S(artifact.Data, "type") != "package" {
		return Resource{}, conflict("质量报告缺少准确的被测制品")
	}
	report, err := t.Get(id)
	if err != nil || report.Kind != "artifact" || S(report.Data, "type") != "report" || report.ProjectID != run.ProjectID || report.GoalID != run.GoalID || revision(report) != revision(run) || S(report.Data, "producerId") != a.ID || S(report.Data, "runId") != run.ID || S(report.Data, "testedArtifactId") != artifact.ID {
		return Resource{}, conflict("质量报告必须来自本次独立验证和准确制品")
	}
	for _, key := range []string{"repositoryId", "commit", "target"} {
		if S(report.Data, key) != S(artifact.Data, key) || (S(task.Data, key) != "" && S(report.Data, key) != S(task.Data, key)) {
			return Resource{}, conflict("质量报告的源码或目标绑定失效")
		}
	}
	if artifact.ProjectID != run.ProjectID || artifact.GoalID != run.GoalID || revision(artifact) != revision(run) {
		return Resource{}, conflict("质量报告的需求版本绑定失效")
	}
	return report, s.VerifyArtifact(report)
}

// Recheck the persisted completion and exact report bytes when evidence is used.
// A missing proof field cannot turn a configured-report pipeline into a log-only one.
func (s *Service) qualityEvidenceReport(t *Tx, ev Resource) error {
	if S(ev.Data, "source") != "trusted-worker" {
		if S(ev.Data, "source") != "human-attested" {
			return conflict("质量证据来源无效")
		}
		if !Has([]string{"manual", "device"}, S(ev.Data, "method")) {
			return conflict("人工证据的验证方式无效")
		}
		artifact, err := t.Get(S(ev.Data, "artifactId"))
		if err != nil || artifact.Kind != "artifact" || S(artifact.Data, "type") != "package" || artifact.ProjectID != ev.ProjectID || artifact.GoalID != ev.GoalID || revision(artifact) != revision(ev) {
			return conflict("人工验收缺少准确的被测制品")
		}
		report, err := t.Get(S(ev.Data, "proofArtifactId"))
		if err != nil || S(report.Data, "purpose") == "release-feedback" || report.Kind != "artifact" || S(report.Data, "type") != "report" || report.ProjectID != ev.ProjectID || report.GoalID != ev.GoalID || revision(report) != revision(ev) {
			return conflict("人工验收缺少准确报告附件")
		}
		for _, key := range []string{"repositoryId", "commit", "target"} {
			if S(report.Data, key) != S(artifact.Data, key) {
				return conflict("人工验收报告的源码或目标绑定失效")
			}
		}
		return s.VerifyArtifact(report)
	}
	pipeline, err := t.Get(S(ev.Data, "pipelineId"))
	if err != nil || pipeline.Kind != "pipeline" || pipeline.ProjectID != ev.ProjectID || pipeline.Revision != int(N(ev.Data, "pipelineRevision")) {
		return conflict("质量证据的验证策略已失效")
	}
	run, err := t.Get(S(ev.Data, "runId"))
	if err != nil || run.Kind != "run" || run.ProjectID != ev.ProjectID || run.GoalID != ev.GoalID || revision(run) != revision(ev) || run.Status != "succeeded" || S(run.Data, "evidenceId") != ev.ID {
		return conflict("质量报告缺少原始通过记录")
	}
	task, err := t.Get(S(run.Data, "taskId"))
	if err != nil || task.Kind != "task" || task.ProjectID != ev.ProjectID || task.GoalID != ev.GoalID || revision(task) != revision(ev) || task.Status != "succeeded" || S(task.Data, "kind") != "test" || S(task.Data, "artifactId") != S(ev.Data, "artifactId") || S(task.Data, "pipelineId") != pipeline.ID || S(ev.Data, "method") != S(pipeline.Data, "method") || S(ev.Data, "verificationEnvironment") != S(pipeline.Data, "verificationEnvironment") || Hash(Strings(ev.Data["criteria"])) != Hash(Strings(task.Data["criteria"])) {
		return conflict("质量报告的任务绑定失效")
	}
	worker, err := t.Get(S(run.Data, "workerId"))
	if err != nil || worker.Kind != "worker" || worker.ProjectID != ev.ProjectID || worker.Status == "disabled" || S(ev.Data, "actorId") != "worker:"+worker.ID || !B(worker.Data, "trustedQuality") || !Has(Strings(worker.Data["roles"]), "quality") || !Has(Strings(worker.Data["capabilities"]), "test") || S(worker.Data, "runtime") != S(task.Data, "runtime") {
		return conflict("质量证据的独立执行节点已失效")
	}
	if S(pipeline.Data, "reportPath") == "" && S(ev.Data, "proofArtifactId") == "" {
		return nil
	}
	result := Obj(run.Data["result"])
	if S(ev.Data, "proofArtifactId") == "" || S(result, "reportArtifactId") != S(ev.Data, "proofArtifactId") {
		return conflict("质量证据缺少准确报告附件")
	}
	report, err := s.validateQualityReport(t, Actor{ID: S(ev.Data, "actorId")}, run, task, result, true)
	if err != nil {
		return err
	}
	if S(ev.Data, "proofDigest") != S(report.Data, "digest") {
		return conflict("质量报告摘要已变化")
	}
	return nil
}

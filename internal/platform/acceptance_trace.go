package platform

import (
	"context"
	"sort"
)

// These read models deliberately contain no arbitrary task/run payloads. Reading
// traceability must not expose leases, provider responses or worker credentials.
type AcceptanceTrace struct {
	ProjectID           string           `json:"projectId"`
	GoalID              string           `json:"goalId"`
	Title               string           `json:"title"`
	Outcome             string           `json:"outcome"`
	RequirementRevision int              `json:"requirementRevision"`
	CurrentRevision     int              `json:"currentRevision"`
	Revisions           []int            `json:"revisions"`
	Current             bool             `json:"current"`
	BaselineApproved    bool             `json:"baselineApproved"`
	BaselineApprovalIDs []string         `json:"baselineApprovalIds"`
	GeneratedAt         string           `json:"generatedAt"`
	Criteria            []TraceCriterion `json:"criteria"`
	Artifacts           []TraceArtifact  `json:"artifacts"`
	Releases            []TraceRelease   `json:"releases"`
}

type TraceCriterion struct {
	ID      string          `json:"id"`
	Text    string          `json:"text"`
	Method  string          `json:"method"`
	Targets []TraceCoverage `json:"targets"`
}

// Covered means this criterion has exact independent evidence for at least one
// package. It is not an assertion of environment coverage or release readiness.
type TraceCoverage struct {
	Target      string   `json:"target"`
	Covered     bool     `json:"covered"`
	ArtifactIDs []string `json:"artifactIds"`
	EvidenceIDs []string `json:"evidenceIds"`
	Gaps        []string `json:"gaps"`
}

type TraceArtifact struct {
	ID                  string          `json:"id"`
	Title               string          `json:"title"`
	Target              string          `json:"target"`
	Digest              string          `json:"digest"`
	RepositoryID        string          `json:"repositoryId"`
	Commit              string          `json:"commit"`
	ProducerID          string          `json:"producerId"`
	IntegrityVerified   bool            `json:"integrityVerified"`
	SourceVerified      bool            `json:"sourceVerified"`
	RequiredEnvironment string          `json:"requiredEnvironment"`
	EnvironmentVerified bool            `json:"environmentVerified"`
	Issues              []string        `json:"issues"`
	Build               *TraceBuild     `json:"build"`
	Evidence            []TraceEvidence `json:"evidence"`
}

type TraceBuild struct {
	RunID        string   `json:"runId"`
	TaskID       string   `json:"taskId"`
	WorkerID     string   `json:"workerId"`
	PlanID       string   `json:"planId"`
	ApprovalID   string   `json:"approvalId,omitempty"`
	DesignID     string   `json:"designId,omitempty"`
	DesignDigest string   `json:"designDigest,omitempty"`
	PrototypeIDs []string `json:"prototypeIds"`
	Verified     bool     `json:"verified"`
	Issues       []string `json:"issues"`
}

type TraceEvidence struct {
	ID                      string   `json:"id"`
	Status                  string   `json:"status"`
	Source                  string   `json:"source"`
	Method                  string   `json:"method"`
	Purpose                 string   `json:"purpose"`
	ActorID                 string   `json:"actorId"`
	Criteria                []string `json:"criteria"`
	VerificationEnvironment string   `json:"verificationEnvironment"`
	RunID                   string   `json:"runId,omitempty"`
	TaskID                  string   `json:"taskId,omitempty"`
	PipelineID              string   `json:"pipelineId,omitempty"`
	QualityPolicyID         string   `json:"qualityPolicyId,omitempty"`
	ReportID                string   `json:"reportId,omitempty"`
	ReportDigest            string   `json:"reportDigest,omitempty"`
	ReportVerified          bool     `json:"reportVerified"`
	Usable                  bool     `json:"usable"`
	Issues                  []string `json:"issues"`
}

type TraceRelease struct {
	ID         string               `json:"id"`
	Version    string               `json:"version"`
	Status     string               `json:"status"`
	Commit     string               `json:"commit"`
	PlanID     string               `json:"planId"`
	Binding    string               `json:"binding"`
	Approved   bool                 `json:"approved"`
	ApprovalID string               `json:"approvalId,omitempty"`
	GatePassed bool                 `json:"gatePassed"`
	Issues     []string             `json:"issues"`
	Targets    []TraceReleaseTarget `json:"targets"`
}

type TraceReleaseTarget struct {
	Target     string `json:"target"`
	Status     string `json:"status"`
	ArtifactID string `json:"artifactId"`
	ExternalID string `json:"externalId"`
	Reason     string `json:"reason"`
}

func (s *Service) AcceptanceTrace(ctx context.Context, actor Actor, projectID, goalID string, requestedRevision int) (AcceptanceTrace, error) {
	report := AcceptanceTrace{ProjectID: projectID, GoalID: goalID, Revisions: []int{}, BaselineApprovalIDs: []string{}, Criteria: []TraceCriterion{}, Artifacts: []TraceArtifact{}, Releases: []TraceRelease{}}
	err := s.Store.Update(ctx, func(tx *Tx) error {
		currentActor, err := tx.CurrentActor(actor)
		if err != nil {
			return err
		}
		if err = s.allowed(tx, currentActor, projectID); err != nil {
			return err
		}
		project, err := tx.Get(projectID)
		if err != nil {
			return err
		}
		if project.Kind != "project" {
			return missing()
		}
		g, err := tx.Get(goalID)
		if err != nil {
			return err
		}
		if g.Kind != "goal" || g.ProjectID != projectID {
			return denied()
		}
		if requestedRevision < 0 {
			return invalid("需求版本必须为正整数")
		}
		if requestedRevision == 0 {
			requestedRevision = g.Revision
		}
		items, err := tx.List(projectID, "", goalID)
		if err != nil {
			return err
		}
		var baseline Resource
		for _, r := range items {
			if r.Kind == "requirement" {
				report.Revisions = append(report.Revisions, r.Revision)
				if r.Revision == requestedRevision {
					baseline = r
				}
			}
		}
		if baseline.ID == "" {
			return missing()
		}
		sort.Ints(report.Revisions)
		report.RequirementRevision, report.CurrentRevision = requestedRevision, g.Revision
		report.Current = requestedRevision == g.Revision
		report.Title, report.Outcome = g.Title, S(baseline.Data, "outcome")
		report.BaselineApproved = report.Current && approved(tx, g) == nil
		for _, r := range items {
			if r.Kind == "approval" && S(r.Data, "type") == "baseline" && revision(r) == requestedRevision {
				report.BaselineApprovalIDs = append(report.BaselineApprovalIDs, r.ID)
			}
			if r.Kind == "artifact" && S(r.Data, "type") == "package" && revision(r) == requestedRevision {
				report.Artifacts = append(report.Artifacts, s.traceArtifact(tx, g, r, items, report.Current && report.BaselineApproved))
			}
		}
		for _, entry := range Items(baseline.Data["criteria"]) {
			criterion := Obj(entry)
			row := TraceCriterion{ID: S(criterion, "id"), Text: S(criterion, "text"), Method: S(criterion, "method"), Targets: []TraceCoverage{}}
			for _, target := range Strings(baseline.Data["targets"]) {
				coverage := TraceCoverage{Target: target, ArtifactIDs: []string{}, EvidenceIDs: []string{}, Gaps: []string{}}
				hasPackage := false
				for _, artifact := range report.Artifacts {
					if artifact.Target != target {
						continue
					}
					hasPackage = true
					matched := false
					for _, evidence := range artifact.Evidence {
						if evidence.Usable && evidence.Purpose != "publication" && evidence.Method == row.Method && Has(evidence.Criteria, row.ID) {
							coverage.EvidenceIDs = append(coverage.EvidenceIDs, evidence.ID)
							matched = true
						}
					}
					if matched {
						coverage.ArtifactIDs = append(coverage.ArtifactIDs, artifact.ID)
					}
				}
				coverage.Covered = len(coverage.EvidenceIDs) > 0
				switch {
				case !report.Current:
					coverage.Gaps = append(coverage.Gaps, "历史记录不计入当前验收")
				case !report.BaselineApproved:
					coverage.Gaps = append(coverage.Gaps, "当前需求基线尚未批准")
				}
				if !hasPackage {
					coverage.Gaps = append(coverage.Gaps, "尚无此目标的候选制品")
				} else if !coverage.Covered && report.Current {
					coverage.Gaps = append(coverage.Gaps, "缺少匹配当前标准与方法的独立通过证据")
				}
				row.Targets = append(row.Targets, coverage)
			}
			report.Criteria = append(report.Criteria, row)
		}
		for _, r := range items {
			if r.Kind == "release" && revision(r) == requestedRevision {
				report.Releases = append(report.Releases, s.traceRelease(tx, g, r, items, report.Current))
			}
		}
		report.GeneratedAt = now()
		return nil
	})
	return report, err
}

func (s *Service) traceArtifact(tx *Tx, g, artifact Resource, items []Resource, current bool) TraceArtifact {
	r := TraceArtifact{ID: artifact.ID, Title: artifact.Title, Target: S(artifact.Data, "target"), Digest: S(artifact.Data, "digest"), RepositoryID: S(artifact.Data, "repositoryId"), Commit: S(artifact.Data, "commit"), ProducerID: S(artifact.Data, "producerId"), RequiredEnvironment: targetEnvironment(S(artifact.Data, "target")), Issues: []string{}, Evidence: []TraceEvidence{}}
	if err := s.VerifyArtifact(artifact); err != nil {
		r.Issues = append(r.Issues, err.Error())
	} else {
		r.IntegrityVerified = true
	}
	if err := s.validateSigningArtifact(tx, artifact); err != nil {
		r.Issues = append(r.Issues, err.Error())
	}
	repo, err := tx.Get(r.RepositoryID)
	if err == nil && repo.Kind == "repository" && repo.ProjectID == artifact.ProjectID && CommitPattern.MatchString(r.Commit) {
		_, err = s.Git(repo, "cat-file", "-e", r.Commit+"^{commit}")
		r.SourceVerified = err == nil
	}
	if !r.SourceVerified {
		r.Issues = append(r.Issues, "制品源码仓库或准确提交无法核验")
	}
	if S(artifact.Data, "runId") != "" {
		r.Build = traceBuild(tx, artifact, items)
	}
	for _, ev := range items {
		if ev.Kind != "evidence" || S(ev.Data, "artifactId") != artifact.ID {
			continue
		}
		entry := s.traceEvidence(tx, artifact, ev, current && len(r.Issues) == 0)
		if entry.Usable && entry.Purpose != "publication" && environmentEvidence(ev, artifact) {
			r.EnvironmentVerified = true
		}
		r.Evidence = append(r.Evidence, entry)
	}
	return r
}

func (s *Service) traceEvidence(tx *Tx, artifact, ev Resource, current bool) TraceEvidence {
	r := TraceEvidence{ID: ev.ID, Status: ev.Status, Source: S(ev.Data, "source"), Method: S(ev.Data, "method"), Purpose: S(ev.Data, "purpose"), ActorID: S(ev.Data, "actorId"), Criteria: Strings(ev.Data["criteria"]), VerificationEnvironment: S(ev.Data, "verificationEnvironment"), RunID: S(ev.Data, "runId"), PipelineID: S(ev.Data, "pipelineId"), QualityPolicyID: S(ev.Data, "qualityPolicyId"), ReportID: S(ev.Data, "proofArtifactId"), Issues: []string{}}
	if r.Criteria == nil {
		r.Criteria = []string{}
	}
	if !current {
		r.Issues = append(r.Issues, "不计入当前验收：历史版本、未批准基线或制品校验未通过")
	}
	if ev.Status != "passed" {
		r.Issues = append(r.Issues, "当前保存的证据状态不是通过")
	}
	if ev.ProjectID != artifact.ProjectID || ev.GoalID != artifact.GoalID || revision(ev) != revision(artifact) || S(ev.Data, "digest") != S(artifact.Data, "digest") || S(ev.Data, "commit") != S(artifact.Data, "commit") || S(ev.Data, "target") != S(artifact.Data, "target") {
		r.Issues = append(r.Issues, "证据与准确制品、源码、目标或需求版本不匹配")
	}
	if r.ActorID == "" || r.ActorID == S(artifact.Data, "producerId") {
		r.Issues = append(r.Issues, "缺少独立于制品生产者的验证人")
	}
	if err := s.qualityEvidenceReport(tx, ev); err != nil {
		r.Issues = append(r.Issues, err.Error())
	} else if r.ReportID != "" {
		report, err := tx.Get(r.ReportID)
		if err == nil {
			r.ReportDigest, r.ReportVerified = S(report.Data, "digest"), true
		}
	}
	if r.RunID != "" {
		run, err := tx.Get(r.RunID)
		if err == nil && run.Kind == "run" && run.ProjectID == ev.ProjectID && run.GoalID == ev.GoalID {
			r.TaskID = S(run.Data, "taskId")
		}
	}
	r.Usable = len(r.Issues) == 0
	return r
}

func traceBuild(tx *Tx, artifact Resource, items []Resource) *TraceBuild {
	r := &TraceBuild{RunID: S(artifact.Data, "runId"), PrototypeIDs: []string{}, Issues: []string{}}
	run, err := tx.Get(r.RunID)
	if err != nil || run.Kind != "run" || run.ProjectID != artifact.ProjectID || run.GoalID != artifact.GoalID || revision(run) != revision(artifact) {
		r.Issues = append(r.Issues, "构建执行关联无效")
		return r
	}
	r.TaskID, r.WorkerID, r.PlanID = S(run.Data, "taskId"), S(run.Data, "workerId"), S(run.Data, "planId")
	task, err := tx.Get(r.TaskID)
	result := Obj(run.Data["result"])
	if err != nil || task.Kind != "task" || task.ProjectID != artifact.ProjectID || task.GoalID != artifact.GoalID || revision(task) != revision(artifact) || task.Status != "succeeded" || run.Status != "succeeded" || S(task.Data, "kind") != "build" || S(task.Data, "planId") != r.PlanID || S(result, "artifactId") != artifact.ID || S(result, "commit") != S(artifact.Data, "commit") || S(result, "repositoryId") != S(artifact.Data, "repositoryId") || "worker:"+r.WorkerID != S(artifact.Data, "producerId") {
		r.Issues = append(r.Issues, "未找到与此制品准确绑定的成功构建")
		return r
	}
	plan, err := tx.Get(r.PlanID)
	if err != nil || plan.Kind != "plan" || plan.ProjectID != artifact.ProjectID || plan.GoalID != artifact.GoalID || revision(plan) != revision(artifact) {
		r.Issues = append(r.Issues, "构建方案关联无效")
		return r
	}
	for _, approval := range items {
		if approval.Kind != "approval" || S(approval.Data, "type") != "plan" || S(approval.Data, "planId") != plan.ID || revision(approval) != revision(artifact) || S(approval.Data, "binding") != S(plan.Data, "binding") {
			continue
		}
		r.ApprovalID = approval.ID
		if snapshot := Obj(approval.Data["design"]); len(snapshot) > 0 && S(approval.Data, "designDigest") == Hash(snapshot) {
			r.DesignID, r.DesignDigest = S(snapshot, "id"), S(approval.Data, "designDigest")
			for _, prototype := range Items(Obj(snapshot["data"])["prototypes"]) {
				r.PrototypeIDs = append(r.PrototypeIDs, S(Obj(prototype), "artifactId"))
			}
		}
		break
	}
	if r.ApprovalID == "" {
		r.Issues = append(r.Issues, "未找到构建方案的准确人工批准记录")
	}
	r.Verified = len(r.Issues) == 0
	return r
}

func (s *Service) traceRelease(tx *Tx, g, release Resource, items []Resource, current bool) TraceRelease {
	r := TraceRelease{ID: release.ID, Version: S(release.Data, "version"), Status: release.Status, Commit: S(release.Data, "commit"), PlanID: S(release.Data, "planId"), Binding: S(release.Data, "binding"), Issues: []string{}, Targets: []TraceReleaseTarget{}}
	for _, target := range Items(release.Data["targets"]) {
		m := Obj(target)
		r.Targets = append(r.Targets, TraceReleaseTarget{S(m, "target"), S(m, "status"), S(m, "artifactId"), S(m, "externalId"), S(m, "reason")})
	}
	if !current || release.Status == "stale" {
		r.Issues = append(r.Issues, "历史或已失效候选不具有当前发布权限")
	} else if err := s.releaseGate(tx, release); err != nil {
		r.Issues = append(r.Issues, err.Error())
	} else {
		r.GatePassed = true
	}
	for _, a := range items {
		if a.Kind == "approval" && S(a.Data, "type") == "release" && S(a.Data, "releaseId") == release.ID && S(a.Data, "binding") == r.Binding && revision(a) == revision(release) {
			r.ApprovalID = a.ID
			r.Approved = current && release.Status != "stale" && a.Status != "stale" && r.Binding != "" && S(release.Data, "approvedBinding") == r.Binding
		}
	}
	return r
}

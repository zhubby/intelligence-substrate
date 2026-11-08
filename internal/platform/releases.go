package platform

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
)

var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func artifactRoles(meta M) []string {
	if S(meta, "type") == "prototype" {
		return []string{"developer", "product", "architect", "design", "quality", "release"}
	}
	return []string{"developer", "quality", "release"}
}

func (s *Service) ArtifactPath(digest string) string {
	if !digestPattern.MatchString(digest) {
		return ""
	}
	return filepath.Join(s.Root, "artifacts", digest[:2], digest)
}
func (s *Service) Upload(ctx context.Context, a Actor, meta M, body io.Reader) (Resource, error) {
	g, e := s.GetResource(ctx, a, S(meta, "goalId"))
	if e != nil || g.Kind != "goal" {
		return Resource{}, missing()
	}
	if e = s.CanAccess(ctx, a, g.ProjectID, artifactRoles(meta)...); e != nil {
		return Resource{}, e
	}
	limit := int64(512 << 20)
	if S(meta, "type") == "prototype" {
		limit = 2 << 20
	}
	digest, size, e := s.uploadContent(body, limit)
	if e != nil {
		return Resource{}, e
	}
	meta = copyM(meta)
	meta["digest"] = digest
	meta["size"] = size
	return s.Command(ctx, a, "artifacts.register", meta)
}
func (s *Service) VerifyArtifact(r Resource) error {
	path := s.ArtifactPath(S(r.Data, "digest"))
	if path == "" {
		return invalid("制品摘要无效")
	}
	f, e := os.Open(path)
	if e != nil {
		return conflict("制品内容不存在")
	}
	defer f.Close()
	h := sha256.New()
	n, e := io.Copy(h, f)
	if e != nil {
		return e
	}
	if hex.EncodeToString(h.Sum(nil)) != S(r.Data, "digest") || n != int64(N(r.Data, "size")) {
		return conflict("制品完整性验证失败")
	}
	return nil
}
func (s *Service) releaseCommand(t *Tx, a Actor, cmd string, in M) (Resource, error) {
	if cmd == "artifacts.register" {
		g, e := s.scoped(t, a, S(in, "goalId"), "goal", artifactRoles(in)...)
		if e != nil {
			return g, e
		}
		target := S(in, "target")
		if !Has(Strings(g.Data["targets"]), target) {
			// A future product baseline can remove a client while its historical
			// deployment still needs operational proof. This exception accepts
			// reports only, bound to an existing deployed artifact and repository.
			d, err := s.scoped(t, a, S(in, "deploymentId"), "deployment", "quality", "release")
			if err != nil || S(in, "type") != "report" || d.GoalID != g.ID || target != "web" || S(d.Data, "commit") != S(in, "commit") {
				return g, invalid("制品目标不属于需求基线")
			}
			artifact, err := t.Get(S(d.Data, "artifactId"))
			if err != nil || S(artifact.Data, "repositoryId") != S(in, "repositoryId") {
				return g, invalid("运行报告必须绑定历史部署的准确仓库")
			}
		}
		commit := S(in, "commit")
		if !CommitPattern.MatchString(commit) {
			return g, invalid("需要完整的源提交 SHA")
		}
		repo, e := s.scoped(t, a, S(in, "repositoryId"), "repository")
		if e != nil {
			return g, e
		}
		if repo.ProjectID != g.ProjectID {
			return g, denied()
		}
		if _, e = s.Git(repo, "cat-file", "-e", commit+"^{commit}"); e != nil {
			return g, invalid("源提交在项目仓库中不存在")
		}
		kind := S(in, "type")
		if kind == "" {
			kind = "package"
		}
		if !Has([]string{"package", "unsigned-package", "report", "prototype"}, kind) {
			return g, invalid("制品类型无效")
		}
		name := S(in, "name")
		if e = validateText(name, "制品名称", 200); e != nil {
			return g, e
		}
		r := resource("artifact", name, g.ProjectID, g.ID, M{"digest": S(in, "digest"), "size": N(in, "size"), "commit": commit, "repositoryId": repo.ID, "target": target, "type": kind, "requirementRevision": g.Revision, "producerId": a.ID, "runId": S(in, "runId"), "mediaType": S(in, "mediaType")})
		if e = s.bindAndroidArtifact(t, a, in, &r); e != nil {
			return r, e
		}
		if a.Kind == "worker" && kind == "report" {
			run, err := t.Get(S(in, "runId"))
			if err != nil {
				return r, err
			}
			task, err := t.Get(S(run.Data, "taskId"))
			if err != nil {
				return r, err
			}
			if S(task.Data, "kind") == "test" {
				// Derived from the lease, never from upload metadata or report text.
				r.Data["testedArtifactId"] = S(task.Data, "artifactId")
				if err = t.Put(r); err != nil {
					return r, err
				}
				if _, err = s.validateQualityReport(t, a, run, task, M{"reportArtifactId": r.ID}, true); err != nil {
					return r, err
				}
			}
		}
		if S(in, "deploymentId") != "" {
			r.Data["deploymentId"] = S(in, "deploymentId")
		}
		if e = s.VerifyArtifact(r); e != nil {
			return r, e
		}
		return r, t.Put(r)
	}
	if cmd == "evidence.record" {
		artifact, e := s.scoped(t, a, S(in, "artifactId"), "artifact", "quality")
		if e != nil {
			return artifact, e
		}
		if e = human(a); e != nil {
			return artifact, e
		}
		if e = s.validateSigningArtifact(t, artifact); e != nil {
			return artifact, e
		}
		if S(artifact.Data, "producerId") == a.ID {
			return artifact, conflict("独立验证人不能是制品生产者")
		}
		g, e := t.Get(artifact.GoalID)
		if e != nil {
			return g, e
		}
		if e = checkRevision(g, artifact); e != nil {
			return g, e
		}
		method := S(in, "method")
		if !Has([]string{"manual", "device"}, method) {
			return g, denied()
		}
		purpose := S(in, "purpose")
		if purpose == "" {
			purpose = "acceptance"
		}
		if !Has([]string{"acceptance", "environment", "publication"}, purpose) {
			return g, invalid("验证目的无效")
		}
		environment := S(in, "verificationEnvironment")
		if environment != "" {
			if e = validateVerificationEnvironment(environment, method, ""); e != nil {
				return g, e
			}
			if environment != targetEnvironment(S(artifact.Data, "target")) && !Has([]string{"ci", "simulator"}, environment) {
				return g, invalid("目标验证环境与制品端不匹配")
			}
		}
		if purpose == "environment" && environment == "" {
			return g, invalid("环境验收需要明确实际目标环境")
		}
		if e = validateText(S(in, "statement"), "验收记录", 12000); e != nil {
			return g, e
		}
		proof, e := s.scoped(t, a, S(in, "proofArtifactId"), "artifact", "quality")
		if e != nil {
			return proof, e
		}
		if S(proof.Data, "purpose") == "release-feedback" || S(artifact.Data, "type") != "package" || proof.GoalID != g.ID || S(proof.Data, "type") != "report" || S(proof.Data, "commit") != S(artifact.Data, "commit") || S(proof.Data, "target") != S(artifact.Data, "target") || revision(proof) != g.Revision || S(proof.Data, "repositoryId") != S(artifact.Data, "repositoryId") {
			return proof, conflict("验收附件必须为同一目标和提交的实际报告")
		}
		if e = s.VerifyArtifact(proof); e != nil {
			return proof, e
		}
		criteria := Strings(in["criteria"])
		if purpose == "acceptance" {
			if len(criteria) == 0 {
				return g, invalid("需要关联验收标准")
			}
			for _, id := range criteria {
				found := false
				for _, c := range Items(g.Data["criteria"]) {
					m := Obj(c)
					if S(m, "id") == id && S(m, "method") == method {
						found = true
					}
				}
				if !found {
					return g, invalid("验证方式与需求验收标准不符")
				}
			}
		}
		r := resource("evidence", "独立验收记录", g.ProjectID, g.ID, M{"artifactId": artifact.ID, "proofArtifactId": proof.ID, "digest": S(artifact.Data, "digest"), "commit": S(artifact.Data, "commit"), "target": S(artifact.Data, "target"), "requirementRevision": g.Revision, "criteria": criteria, "method": method, "source": "human-attested", "statement": S(in, "statement"), "actorId": a.ID, "purpose": S(in, "purpose"), "externalId": S(in, "externalId")})
		r.Status = "passed"
		r.Data["purpose"] = purpose
		r.Data["verificationEnvironment"] = environment
		if purpose == "publication" && S(in, "publicationBinding") != "" {
			release, err := s.scoped(t, a, S(in, "releaseId"), "release", "quality")
			if err != nil {
				return r, err
			}
			target := releaseTarget(release, S(artifact.Data, "target"))
			if release.GoalID != g.ID || S(target, "artifactId") != artifact.ID || S(target, "externalId") != S(in, "externalId") || S(target, "status") != "published" || S(in, "publicationBinding") != effectivePublicationBinding(release, target) {
				return r, conflict("发布证据必须关联准确候选与当前资料批准")
			}
			r.Data["releaseId"], r.Data["publicationBinding"], r.Data["metadataDigest"], r.Data["materialRevisionId"] = release.ID, S(in, "publicationBinding"), releaseMetadataDigest(release, S(target, "target")), S(target, "materialRevisionId")
		}
		return r, t.Put(r)
	}
	if cmd == "releases.create" {
		if _, exists := in["releaseIssues"]; exists {
			return Resource{}, invalid("拒审问题快照由当前批准方案派生")
		}
		g, e := s.scoped(t, a, S(in, "goalId"), "goal", "release", "developer")
		if e != nil {
			return g, e
		}
		if e = validateText(S(in, "version"), "版本号", 80); e != nil {
			return g, e
		}
		commit := S(in, "commit")
		if !CommitPattern.MatchString(commit) {
			return g, invalid("需要完整的源提交 SHA")
		}
		ids := Strings(in["artifactIds"])
		artifacts := []any{}
		targets := []any{}
		seen := map[string]bool{}
		for _, id := range ids {
			ar, e := t.Get(id)
			if e != nil {
				return g, e
			}
			target := S(ar.Data, "target")
			if ar.Kind != "artifact" || ar.GoalID != g.ID || S(ar.Data, "commit") != commit || revision(ar) != g.Revision || S(ar.Data, "type") != "package" || seen[target] {
				return g, invalid("候选制品绑定无效或目标重复")
			}
			if e = s.VerifyArtifact(ar); e != nil {
				return g, e
			}
			seen[target] = true
			artifacts = append(artifacts, M{"id": ar.ID, "target": target, "digest": S(ar.Data, "digest")})
			targets = append(targets, M{"target": target, "status": "ready", "artifactId": ar.ID, "externalId": "", "reason": ""})
		}
		for _, target := range Strings(g.Data["targets"]) {
			if !seen[target] {
				return g, conflict("缺少必选目标的候选制品：" + target)
			}
		}
		r := resource("release", S(in, "version"), g.ProjectID, g.ID, M{"version": S(in, "version"), "commit": commit, "requirementRevision": g.Revision, "planId": S(g.Data, "activePlanId"), "artifacts": artifacts, "targets": targets, "createdBy": a.ID})
		for _, entry := range targets {
			Obj(entry)["observedHistory"] = []any{M{"from": "", "to": "ready", "at": r.CreatedAt, "actorId": a.ID}}
		}
		r.Status = "awaiting_approval"
		materials, e := s.releaseMaterials(t, g, commit, artifacts, in["storeMetadataArtifactIds"])
		if e != nil {
			return r, e
		}
		binding := M{"goalId": g.ID, "revision": g.Revision, "commit": commit, "artifacts": artifacts, "planId": S(g.Data, "activePlanId")}
		qualityPolicies, e := s.qualityPolicySnapshot(t, g)
		if e != nil {
			return r, e
		}
		if len(qualityPolicies) > 0 {
			r.Data["qualityPolicies"] = qualityPolicies
			binding["qualityPolicies"] = qualityPolicies
		}
		if len(materials) > 0 {
			r.Data["storeMetadata"] = materials
			binding["storeMetadata"] = materials
		}
		r.Data["binding"] = Hash(binding)
		plan, e := t.Get(S(r.Data, "planId"))
		if e != nil {
			return r, e
		}
		issues, e := candidateReleaseIssues(t, plan)
		if e != nil {
			return r, e
		}
		if len(issues) > 0 {
			r.Data["releaseIssues"] = issues
			r.Data["binding"] = Hash(releaseBinding(r))
		}
		if e = s.releaseGate(t, r); e != nil {
			return r, e
		}
		return r, t.Put(r)
	}
	r, e := s.scoped(t, a, S(in, "id"), "release")
	if e != nil {
		return r, e
	}
	g, e := t.Get(r.GoalID)
	if e != nil {
		return r, e
	}
	if e = checkRevision(g, r); e != nil {
		return r, e
	}
	if r.Status == "stale" {
		return r, conflict("发布候选已失效")
	}
	if cmd == "releases.approve" {
		if e = s.allowed(t, a, r.ProjectID, "release"); e != nil {
			return r, e
		}
		if e = human(a); e != nil {
			return r, e
		}
		if r.Status != "awaiting_approval" {
			return r, conflict("候选版本当前不可审批")
		}
		if e = s.releaseGate(t, r); e != nil {
			return r, e
		}
		for _, x := range append(Items(r.Data["artifacts"]), Items(r.Data["storeMetadata"])...) {
			ar, e := t.Get(S(Obj(x), "id"))
			if e != nil {
				return r, e
			}
			if S(ar.Data, "producerId") == a.ID {
				return r, conflict("发布批准人不能是候选制品生产者")
			}
		}
		r.Status = "approved"
		r.Data["approvedBinding"] = S(r.Data, "binding")
		r.Data["approvedBy"] = a.ID
		approval := resource("approval", "生产发布批准", r.ProjectID, r.GoalID, M{"type": "release", "releaseId": r.ID, "binding": S(r.Data, "binding"), "requirementRevision": g.Revision, "actorId": a.ID})
		if e = t.Put(approval); e != nil {
			return r, e
		}
		return r, t.Put(r)
	}
	if !Has([]string{"approved", "publishing", "delivered", "blocked"}, r.Status) || S(r.Data, "approvedBinding") != S(r.Data, "binding") {
		return r, conflict("需要当前版本的发布批准")
	}
	if cmd == "releases.refresh" {
		return s.refreshRelease(t, a, r, in)
	}
	if e = s.releaseGate(t, r); e != nil {
		return r, e
	}
	if cmd == "releases.dispatch" {
		return s.dispatchRelease(t, a, r, in)
	}
	if cmd == "releases.verify" {
		if e = s.allowed(t, a, r.ProjectID, "quality"); e != nil {
			return r, e
		}
		ev, e := s.scoped(t, a, S(in, "evidenceId"), "evidence", "quality")
		if e != nil {
			return r, e
		}
		target := S(in, "target")
		var current M
		for _, x := range Items(r.Data["targets"]) {
			m := Obj(x)
			if S(m, "target") == target {
				current = m
			}
		}
		if current == nil || S(current, "status") != "published" {
			return r, conflict("目标尚未实际发布")
		}
		if ev.Status != "passed" || revision(ev) != g.Revision || ev.GoalID != g.ID || S(ev.Data, "purpose") != "publication" || S(ev.Data, "artifactId") != S(current, "artifactId") || S(ev.Data, "externalId") != S(current, "externalId") || S(ev.Data, "actorId") == S(current, "updatedBy") || ev.CreatedAt < S(current, "updatedAt") {
			return r, conflict("需要与渠道版本和制品绑定的独立发布验证")
		}
		artifact, e := t.Get(S(current, "artifactId"))
		if e != nil {
			return r, e
		}
		if !environmentEvidence(ev, artifact) {
			return r, conflict("发布验证必须来自实际目标环境")
		}
		if S(current, "materialRevisionId") != "" {
			if S(ev.Data, "releaseId") != r.ID || S(ev.Data, "publicationBinding") != effectivePublicationBinding(r, current) || S(ev.Data, "metadataDigest") != releaseMetadataDigest(r, target) || S(ev.Data, "materialRevisionId") != S(current, "materialRevisionId") {
				return r, conflict("需要绑定当前商店资料批准的独立发布验证")
			}
			rev, err := t.Get(S(current, "materialRevisionId"))
			if err != nil {
				return r, err
			}
			if err = s.storeRevisionGate(t, rev, r); err != nil {
				return r, err
			}
		}
		if e = s.cancelChannelQueries(t, r, target); e != nil {
			return r, e
		}
		if e = s.setTarget(t, &r, target, "verified", S(current, "externalId"), "", a.ID); e != nil {
			return r, e
		}
		if e = s.resolveReleaseIssues(t, r, target, ev); e != nil {
			return r, e
		}
		if e = s.resolveStoreRevisions(t, r, target, ev); e != nil {
			return r, e
		}
		if e = t.Put(r); e != nil {
			return r, e
		}
		if r.Status == "delivered" {
			g.Status = "delivered"
		} else {
			g.Status = "publishing"
		}
		return r, t.Put(g)
	}
	if e = s.allowed(t, a, r.ProjectID, "release"); e != nil {
		return r, e
	}
	if e = human(a); e != nil {
		return r, e
	}
	target := S(in, "target")
	state := S(in, "state")
	if e = s.cancelChannelQueries(t, r, target); e != nil {
		return r, e
	}
	if state == "verified" {
		return r, denied()
	}
	if !Has([]string{"submitting", "processing", "awaiting_review", "awaiting_release", "rejected", "published", "halted", "reconciliation_required", "ready"}, state) {
		return r, invalid("渠道状态无效")
	}
	if state == "ready" && cmd != "releases.reconcile" {
		return r, invalid("结果待核对时需要显式核对")
	}
	if cmd == "releases.reconcile" {
		if e = human(a); e != nil {
			return r, e
		}
		if e = validateText(S(in, "reason"), "核对结论", 2000); e != nil {
			return r, e
		}
		tasks, e := t.List(r.ProjectID, "task", r.GoalID)
		if e != nil {
			return r, e
		}
		for _, task := range tasks {
			if S(task.Data, "releaseId") == r.ID && S(task.Data, "target") == target && Has([]string{"pending", "running", "blocked"}, task.Status) {
				task.Status = "cancelled"
				if e = t.Put(task); e != nil {
					return r, e
				}
				runs, err := t.List(r.ProjectID, "run", r.GoalID)
				if err != nil {
					return r, err
				}
				for _, run := range runs {
					if S(run.Data, "taskId") == task.ID && run.Status == "running" {
						run.Status = "cancelled"
						delete(run.Data, "leaseToken")
						if err = t.Put(run); err != nil {
							return r, err
						}
					}
				}
			}
		}
	}
	if Has([]string{"processing", "awaiting_review", "awaiting_release", "published"}, state) && S(in, "externalId") == "" {
		return r, invalid("需要实际外部版本或审核标识")
	}
	if a.Kind == "human" && Has([]string{"processing", "awaiting_review", "awaiting_release", "published"}, state) {
		proof, e := s.scoped(t, a, S(in, "proofArtifactId"), "artifact", "release")
		if e != nil {
			return r, e
		}
		if S(proof.Data, "purpose") == "release-feedback" || proof.GoalID != g.ID || S(proof.Data, "type") != "report" || S(proof.Data, "commit") != S(r.Data, "commit") || S(proof.Data, "target") != target || revision(proof) != g.Revision {
			return r, invalid("需要渠道操作的实际报告附件")
		}
		if e = s.VerifyArtifact(proof); e != nil {
			return r, e
		}
	}
	for _, x := range Items(r.Data["targets"]) {
		m := Obj(x)
		if S(m, "target") == target && S(m, "status") == "reconciliation_required" && cmd != "releases.reconcile" {
			return r, conflict("外部结果不明，必须先显式核对")
		}
	}
	if e = s.setTarget(t, &r, target, state, S(in, "externalId"), S(in, "reason"), a.ID); e != nil {
		return r, e
	}
	g.Status = "publishing"
	if e = t.Put(g); e != nil {
		return r, e
	}
	return r, t.Put(r)
}

func (s *Service) releaseGate(t *Tx, r Resource) error {
	g, e := t.Get(r.GoalID)
	if e != nil {
		return e
	}
	if e = approved(t, g); e != nil {
		return e
	}
	if e = checkRevision(g, r); e != nil {
		return e
	}
	if e = s.releaseMaterialGate(t, r, g); e != nil {
		return e
	}
	if S(r.Data, "planId") == "" || S(r.Data, "planId") != S(g.Data, "activePlanId") {
		return conflict("当前方案未批准或已变化")
	}
	plan, e := t.Get(S(r.Data, "planId"))
	if e != nil || plan.Status != "active" {
		return conflict("需要有效的交付方案")
	}
	if S(r.Data, "binding") != Hash(releaseBinding(r)) {
		return conflict("候选内容与发布绑定不一致")
	}
	if e = s.releaseIssueGate(t, r, g, plan); e != nil {
		return e
	}
	evidence, e := t.List(g.ProjectID, "evidence", g.ID)
	if e != nil {
		return e
	}
	if e = s.releaseQualityGate(t, r, g, evidence); e != nil {
		return e
	}
	for _, x := range Items(r.Data["artifacts"]) {
		a, e := t.Get(S(Obj(x), "id"))
		if e != nil {
			return e
		}
		if e = s.VerifyArtifact(a); e != nil {
			return e
		}
		if e = s.validateSigningArtifact(t, a); e != nil {
			return e
		}
		for _, c := range Items(g.Data["criteria"]) {
			criterion := Obj(c)
			found := false
			for _, ev := range evidence {
				if ev.Status == "passed" && S(ev.Data, "purpose") != "publication" && revision(ev) == g.Revision && S(ev.Data, "artifactId") == a.ID && S(ev.Data, "digest") == S(a.Data, "digest") && S(ev.Data, "commit") == S(r.Data, "commit") && S(ev.Data, "target") == S(a.Data, "target") && S(ev.Data, "method") == S(criterion, "method") && Has(Strings(ev.Data["criteria"]), S(criterion, "id")) && S(ev.Data, "actorId") != S(a.Data, "producerId") {
					if err := s.qualityEvidenceReport(t, ev); err != nil {
						continue
					}
					found = true
					break
				}
			}
			if !found {
				return conflict(fmt.Sprintf("%s 缺少验收标准「%s」的独立验证", S(a.Data, "target"), S(criterion, "text")))
			}
		}
		environmentVerified := false
		for _, ev := range evidence {
			if S(ev.Data, "purpose") != "publication" && environmentEvidence(ev, a) {
				if err := s.qualityEvidenceReport(t, ev); err != nil {
					continue
				}
				environmentVerified = true
				break
			}
		}
		if !environmentVerified {
			return conflict(fmt.Sprintf("%s 缺少目标环境 %s 的独立验证证据", S(a.Data, "target"), targetEnvironment(S(a.Data, "target"))))
		}
	}
	if len(Items(r.Data["artifacts"])) != len(Strings(g.Data["targets"])) {
		return conflict("候选制品未覆盖全部必选目标")
	}
	return nil
}

func (s *Service) setTarget(t *Tx, r *Resource, target, state, external, reason, actor string) error {
	allowed := map[string][]string{"ready": {"submitting", "processing", "awaiting_review", "awaiting_release", "published", "halted", "reconciliation_required"}, "processing": {"awaiting_review", "awaiting_release", "published", "rejected", "halted", "reconciliation_required"}, "submitting": {"processing", "awaiting_review", "awaiting_release", "published", "rejected", "reconciliation_required"}, "awaiting_release": {"published", "rejected", "halted", "reconciliation_required"}, "awaiting_review": {"awaiting_release", "published", "rejected", "halted", "reconciliation_required"}, "rejected": {"ready", "submitting", "halted", "revising_materials"}, "revising_materials": {"processing", "awaiting_review", "published", "rejected", "reconciliation_required"}, "published": {"verified", "halted", "reconciliation_required"}, "verified": {"halted", "reconciliation_required"}, "halted": {"ready"}, "reconciliation_required": {"ready", "processing", "awaiting_review", "awaiting_release", "published", "rejected", "halted"}}
	found := false
	allVerified := true
	blocked := false
	for _, x := range Items(r.Data["targets"]) {
		m := Obj(x)
		if S(m, "target") == target {
			found = true
			rejectedBefore := S(m, "status") == "rejected" || S(m, "rejectionIssueId") != ""
			for _, entry := range Items(m["observedHistory"]) {
				rejectedBefore = rejectedBefore || S(Obj(entry), "to") == "rejected"
			}
			if rejectedBefore && Has([]string{"ready", "submitting"}, state) {
				return conflict("拒审候选不能复用旧批准；请确认修复要求并创建新的候选版本")
			}
			if !Has(allowed[S(m, "status")], state) {
				return conflict("不允许的渠道状态转换")
			}
			observedAt := now()
			if state == "rejected" && external == "" {
				external = S(m, "externalId")
			}
			observation := M{"from": S(m, "status"), "to": state, "at": observedAt, "actorId": actor, "externalId": external, "reason": reason}
			copyPublicationFields(m, observation)
			m["observedHistory"] = append(Items(m["observedHistory"]), observation)
			m["status"] = state
			m["externalId"] = external
			m["reason"] = reason
			m["updatedBy"] = actor
			m["updatedAt"] = observedAt
			if state == "rejected" {
				if err := s.recordReleaseRejection(t, *r, m, actor); err != nil {
					return err
				}
			}
		}
		if S(m, "status") != "verified" {
			allVerified = false
		}
		if Has([]string{"rejected", "reconciliation_required", "halted"}, S(m, "status")) {
			blocked = true
		}
	}
	if !found {
		return invalid("渠道不属于此版本")
	}
	r.Status = "publishing"
	if blocked {
		r.Status = "blocked"
	}
	if allVerified {
		r.Status = "delivered"
	}
	return nil
}

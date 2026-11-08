package platform

import (
	"context"
	"io"
)

const ReleaseReportLimit int64 = 16 << 20

func (s *Service) feedbackAuthority(tx *Tx, a Actor, in M) (Resource, Resource, error) {
	current, err := tx.CurrentActor(a)
	if err != nil {
		return Resource{}, Resource{}, err
	}
	if err = human(current); err != nil {
		return Resource{}, Resource{}, err
	}
	r, err := s.scoped(tx, current, S(in, "releaseId"), "release", "product", "quality")
	if err != nil {
		return r, Resource{}, err
	}
	if err = validateText(S(in, "name"), "报告名称", 200); err != nil {
		return r, Resource{}, err
	}
	if err = validateText(S(in, "key"), "幂等键", 160); err != nil {
		return r, Resource{}, err
	}
	pkg, err := s.releaseFeedbackSource(tx, r, S(in, "artifactId"))
	return r, pkg, err
}

// ReleaseFeedback never calls releaseGate: future requirements must not revoke
// the ability to report on software that is still deployed under an old scope.
func (s *Service) releaseFeedbackSource(tx *Tx, r Resource, artifactID string) (Resource, error) {
	if r.Kind != "release" || S(r.Data, "binding") != Hash(releaseBinding(r)) {
		return Resource{}, conflict("问题报告的候选绑定无法核验")
	}
	g, err := tx.Get(r.GoalID)
	if err != nil || g.Kind != "goal" || g.ProjectID != r.ProjectID {
		return Resource{}, conflict("问题报告的交付目标无法核验")
	}
	if _, err = measurementBaseline(tx, g, revision(r)); err != nil {
		return Resource{}, err
	}
	pkg, err := tx.Get(artifactID)
	if err != nil {
		return Resource{}, err
	}
	if pkg.Kind != "artifact" || pkg.ProjectID != r.ProjectID || pkg.GoalID != r.GoalID || revision(pkg) != revision(r) || S(pkg.Data, "type") != "package" || S(pkg.Data, "commit") != S(r.Data, "commit") {
		return Resource{}, conflict("问题报告必须选择候选内的准确制品")
	}
	found := false
	for _, x := range Items(r.Data["artifacts"]) {
		entry := Obj(x)
		if S(entry, "id") == pkg.ID && S(entry, "target") == S(pkg.Data, "target") && S(entry, "digest") == S(pkg.Data, "digest") {
			found = true
		}
	}
	if !found {
		return Resource{}, conflict("问题报告制品不属于此候选")
	}
	if !releaseTargetWasPublished(r, S(pkg.Data, "target"), pkg.ID) {
		return Resource{}, conflict("问题报告需要该准确目标已有发布观测")
	}
	repo, err := tx.Get(S(pkg.Data, "repositoryId"))
	if err != nil || repo.Kind != "repository" || repo.ProjectID != r.ProjectID || !CommitPattern.MatchString(S(pkg.Data, "commit")) {
		return Resource{}, conflict("问题报告的源码绑定无法核验")
	}
	if _, err = s.Git(repo, "cat-file", "-e", S(pkg.Data, "commit")+"^{commit}"); err != nil {
		return Resource{}, conflict("问题报告的原始提交不存在")
	}
	return pkg, s.VerifyArtifact(pkg)
}

func releaseTargetWasPublished(r Resource, target, artifactID string) bool {
	for _, x := range Items(r.Data["targets"]) {
		entry := Obj(x)
		if S(entry, "target") != target || (artifactID != "" && S(entry, "artifactId") != artifactID) {
			continue
		}
		if r.Status == "delivered" || Has([]string{"published", "verified"}, S(entry, "status")) {
			return true
		}
		for _, event := range Items(entry["observedHistory"]) {
			if Has([]string{"published", "verified"}, S(Obj(event), "to")) {
				return true
			}
		}
	}
	return false
}

func (s *Service) UploadReleaseReport(ctx context.Context, a Actor, in M, body io.Reader) (Resource, error) {
	in = copyM(in)
	if err := s.Store.Update(ctx, func(tx *Tx) error { _, _, err := s.feedbackAuthority(tx, a, in); return err }); err != nil {
		return Resource{}, err
	}
	digest, size, err := s.uploadContent(body, ReleaseReportLimit)
	if err != nil {
		return Resource{}, err
	}
	in["digest"], in["size"] = digest, size
	return s.Command(ctx, a, "reports.attach", in)
}

func (s *Service) attachReleaseReport(tx *Tx, a Actor, in M) (Resource, error) {
	r, pkg, err := s.feedbackAuthority(tx, a, in)
	if err != nil {
		return Resource{}, err
	}
	canonical := M{"releaseId": r.ID, "releaseBinding": S(r.Data, "binding"), "artifactId": pkg.ID, "artifactDigest": S(pkg.Data, "digest"), "name": S(in, "name"), "digest": S(in, "digest"), "size": N(in, "size")}
	scope := "release-report:" + r.ProjectID + ":" + a.ID
	if old, ok, err := tx.Once(scope, S(in, "key"), canonical); err != nil || ok {
		if err == nil {
			err = s.VerifyArtifact(old)
		}
		return old, err
	}
	size := N(in, "size")
	if size < 1 || size > float64(ReleaseReportLimit) || size != float64(int64(size)) {
		return Resource{}, invalid("问题报告必须为 1 B–16 MiB 的普通文件")
	}
	data := M{"type": "report", "purpose": "release-feedback", "digest": S(in, "digest"), "size": size, "producerId": a.ID, "requirementRevision": revision(r), "releaseId": r.ID, "releaseBinding": S(r.Data, "binding"), "releaseArtifactId": pkg.ID, "releaseArtifactDigest": S(pkg.Data, "digest"), "commit": S(pkg.Data, "commit"), "repositoryId": S(pkg.Data, "repositoryId"), "target": S(pkg.Data, "target")}
	proof := resource("artifact", S(in, "name"), r.ProjectID, r.GoalID, data)
	if err = s.VerifyArtifact(proof); err != nil {
		return proof, err
	}
	if err = tx.Put(proof); err != nil {
		return proof, err
	}
	return proof, tx.Remember(scope, S(in, "key"), canonical, proof)
}

func (s *Service) validateReleaseFeedbackReport(tx *Tx, proof, r Resource) error {
	if S(proof.Data, "purpose") != "release-feedback" {
		return nil
	}
	pkg, err := s.releaseFeedbackSource(tx, r, S(proof.Data, "releaseArtifactId"))
	if err != nil {
		return err
	}
	if proof.ProjectID != r.ProjectID || proof.GoalID != r.GoalID || revision(proof) != revision(r) || S(proof.Data, "releaseId") != r.ID || S(proof.Data, "releaseBinding") != S(r.Data, "binding") || S(proof.Data, "releaseArtifactDigest") != S(pkg.Data, "digest") {
		return conflict("问题报告不属于准确候选及制品")
	}
	for _, key := range []string{"commit", "repositoryId", "target"} {
		if S(proof.Data, key) != S(pkg.Data, key) {
			return conflict("问题报告的源码或目标绑定失效")
		}
	}
	return s.VerifyArtifact(proof)
}

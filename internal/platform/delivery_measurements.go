package platform

import "time"

var measurementTypes = []string{"preview", "business_acceptance", "human_effort", "production_defect"}

func (s *Service) measurementCommand(tx *Tx, a Actor, command string, in M) (Resource, error) {
	if command == "measurements.void" {
		return s.voidMeasurement(tx, a, in)
	}
	g, err := s.scoped(tx, a, S(in, "goalId"), "goal", "product", "quality")
	if err != nil {
		return Resource{}, err
	}
	if err = human(a); err != nil {
		return Resource{}, err
	}
	for _, field := range []struct {
		key, label string
		max        int
	}{{"reference", "观测来源标识", 160}, {"key", "幂等键", 160}, {"note", "观测依据", 2000}} {
		if err = validateText(S(in, field.key), field.label, field.max); err != nil {
			return Resource{}, err
		}
	}
	kind := S(in, "type")
	if !Has(measurementTypes, kind) {
		return Resource{}, invalid("观测类型无效")
	}
	scope := "measurement-record:" + g.ProjectID + ":" + a.ID
	if old, ok, err := tx.Once(scope, S(in, "key"), in); err != nil || ok {
		return old, err
	}
	data := M{"type": kind, "reference": S(in, "reference"), "note": S(in, "note"), "requirementRevision": g.Revision, "recordedBy": a.ID, "recordedName": a.Name, "basis": "human-attested"}
	switch kind {
	case "human_effort":
		err = s.measureEffort(tx, a, g, in, data)
	case "preview":
		err = s.measurePreview(tx, g, in, data)
	default:
		err = s.measureRelease(tx, g, in, data)
	}
	if err != nil {
		return Resource{}, err
	}
	if value, present := in["requirementRevision"]; present {
		n, err := costCents(value)
		if err != nil || n != int64(N(data, "requirementRevision")) {
			return Resource{}, conflict("需求版本与观测来源不一致，请刷新对应版本")
		}
	}
	rows, err := tx.List(g.ProjectID, "measurement", "")
	if err != nil {
		return Resource{}, err
	}
	var replaced Resource
	for _, old := range rows {
		if S(old.Data, "reference") != S(in, "reference") {
			continue
		}
		if old.Status != "voided" {
			return Resource{}, conflict("同一项目的观测来源已经登记，请核对原记录")
		}
		if S(old.Data, "voidedAt") == "" {
			return Resource{}, conflict("原观测的作废历史不完整")
		}
		if replaced.ID == "" || S(old.Data, "voidedAt") > S(replaced.Data, "voidedAt") {
			replaced = old
		}
	}
	if replaced.ID != "" {
		data["replacesMeasurementId"] = replaced.ID
	}
	r := resource("measurement", kind, g.ProjectID, g.ID, data)
	r.Status = "recorded"
	if err = tx.Put(r); err != nil {
		return r, err
	}
	return r, tx.Remember(scope, S(in, "key"), in, r)
}

func measurementBaseline(tx *Tx, g Resource, rev int) (Resource, error) {
	rows, err := tx.List(g.ProjectID, "requirement", g.ID)
	if err != nil {
		return Resource{}, err
	}
	for _, r := range rows {
		if r.Revision == rev {
			return r, nil
		}
	}
	return Resource{}, missing()
}

func (s *Service) measureEffort(tx *Tx, a Actor, g Resource, in, data M) error {
	if id := S(in, "personId"); id != "" && id != a.ID {
		return invalid("只能登记本人实际投入")
	}
	start, e1 := time.Parse(time.RFC3339Nano, S(in, "startedAt"))
	end, e2 := time.Parse(time.RFC3339Nano, S(in, "endedAt"))
	baseline, err := measurementBaseline(tx, g, g.Revision)
	if err != nil {
		return err
	}
	origin, e3 := time.Parse(time.RFC3339Nano, baseline.CreatedAt)
	if e1 != nil || e2 != nil || e3 != nil || !end.After(start) || start.Before(origin) || end.After(time.Now()) || end.Sub(start) > 24*time.Hour {
		return invalid("投入区间必须在本需求版本创建后、现时之前，且为不超过 24 小时的正区间")
	}
	rows, err := tx.List("", "measurement", "")
	if err != nil {
		return err
	}
	for _, r := range rows {
		if r.Status != "recorded" || S(r.Data, "type") != "human_effort" || S(r.Data, "personId") != a.ID {
			continue
		}
		x, e1 := time.Parse(time.RFC3339Nano, S(r.Data, "startedAt"))
		y, e2 := time.Parse(time.RFC3339Nano, S(r.Data, "endedAt"))
		if e1 != nil || e2 != nil {
			return conflict("本人已有投入记录的时间无效，需要核对")
		}
		if start.Before(y) && end.After(x) {
			return conflict("本人投入区间与已有记录重叠，不能重复计时")
		}
	}
	data["personId"], data["startedAt"], data["endedAt"] = a.ID, start.UTC().Format(time.RFC3339Nano), end.UTC().Format(time.RFC3339Nano)
	data["durationSeconds"] = end.Sub(start).Seconds()
	return nil
}

func (s *Service) measurePreview(tx *Tx, g Resource, in, data M) error {
	if err := approved(tx, g); err != nil {
		return err
	}
	artifact, err := tx.Get(S(in, "artifactId"))
	if err != nil {
		return err
	}
	if artifact.Kind != "artifact" || artifact.ProjectID != g.ProjectID || artifact.GoalID != g.ID || revision(artifact) != g.Revision || S(artifact.Data, "type") != "package" {
		return conflict("预览需要当前需求的准确可运行制品")
	}
	ev, err := tx.Get(S(in, "evidenceId"))
	if err != nil {
		return err
	}
	// Trusted Worker acceptance evidence predates the explicit purpose field.
	// Its source and complete execution/report binding are rechecked below.
	if ev.Kind != "evidence" || S(ev.Data, "artifactId") != artifact.ID || !Has([]string{"", "acceptance", "environment"}, S(ev.Data, "purpose")) || !Has([]string{"browser", "simulator", "ios-device", "android-device", "windows", "macos"}, S(ev.Data, "verificationEnvironment")) {
		return conflict("预览需要明确实际运行环境的独立通过证据，不能使用 CI 或发布证明")
	}
	if err = s.VerifyArtifact(artifact); err != nil {
		return err
	}
	entry := s.traceEvidence(tx, artifact, ev, true)
	if !entry.Usable {
		return conflict("预览独立证据或报告绑定无法核验")
	}
	proof, err := tx.Get(S(ev.Data, "proofArtifactId"))
	if err != nil {
		return conflict("预览确认需要实际报告附件")
	}
	if id := S(in, "proofArtifactId"); id != "" && id != proof.ID {
		return conflict("预览报告必须与独立证据一致")
	}
	data["artifactId"], data["artifactDigest"], data["commit"] = artifact.ID, S(artifact.Data, "digest"), S(artifact.Data, "commit")
	data["evidenceId"], data["verificationEnvironment"] = ev.ID, S(ev.Data, "verificationEnvironment")
	data["proofArtifactId"], data["proofDigest"] = proof.ID, S(proof.Data, "digest")
	return nil
}

func releaseWasPublished(r Resource) bool {
	if r.Status == "delivered" {
		return true
	}
	for _, x := range Items(r.Data["targets"]) {
		target := Obj(x)
		if Has([]string{"published", "verified"}, S(target, "status")) {
			return true
		}
		for _, event := range Items(target["observedHistory"]) {
			if Has([]string{"published", "verified"}, S(Obj(event), "to")) {
				return true
			}
		}
	}
	return false
}

func (s *Service) measureRelease(tx *Tx, g Resource, in, data M) error {
	r, err := tx.Get(S(in, "releaseId"))
	if err != nil {
		return err
	}
	if r.Kind != "release" || r.ProjectID != g.ProjectID || r.GoalID != g.ID || S(r.Data, "binding") == "" {
		return conflict("观测必须关联本目标的准确候选版本")
	}
	if S(data, "type") == "business_acceptance" {
		if err = approved(tx, g); err != nil {
			return err
		}
		if revision(r) != g.Revision || r.Status == "stale" {
			return conflict("业务验收必须关联当前需求版本")
		}
		if !Has([]string{"passed", "failed"}, S(in, "result")) {
			return invalid("业务验收需要显式的 passed 或 failed 结果")
		}
		data["result"] = S(in, "result")
	} else if !releaseWasPublished(r) {
		return conflict("上线缺陷只能关联已有真实发布观测的版本")
	}
	proof, err := tx.Get(S(in, "proofArtifactId"))
	if err != nil {
		return err
	}
	if proof.Kind != "artifact" || proof.ProjectID != r.ProjectID || proof.GoalID != r.GoalID || revision(proof) != revision(r) || S(proof.Data, "type") != "report" || S(proof.Data, "commit") != S(r.Data, "commit") {
		return conflict("观测报告必须绑定准确需求版本和候选提交")
	}
	if S(data, "type") == "business_acceptance" && S(proof.Data, "purpose") == "release-feedback" {
		return conflict("上线问题报告不能作为业务验收依据")
	}
	if S(data, "type") == "production_defect" && !releaseTargetWasPublished(r, S(proof.Data, "target"), S(proof.Data, "releaseArtifactId")) {
		return conflict("上线缺陷需要报告对应目标已有发布观测")
	}
	if err = s.validateReleaseFeedbackReport(tx, proof, r); err != nil {
		return err
	}
	if err = s.VerifyArtifact(proof); err != nil {
		return err
	}
	data["releaseId"], data["releaseBinding"], data["commit"] = r.ID, S(r.Data, "binding"), S(r.Data, "commit")
	data["proofArtifactId"], data["proofDigest"] = proof.ID, S(proof.Data, "digest")
	data["requirementRevision"] = revision(r)
	return nil
}

func (s *Service) voidMeasurement(tx *Tx, a Actor, in M) (Resource, error) {
	r, err := s.scoped(tx, a, S(in, "id"), "measurement", "product", "quality")
	if err != nil {
		return r, err
	}
	if err = human(a); err != nil {
		return r, err
	}
	if err = validateText(S(in, "key"), "幂等键", 160); err != nil {
		return r, err
	}
	if err = validateText(S(in, "reason"), "作废原因", 2000); err != nil {
		return r, err
	}
	scope := "measurement-void:" + r.ID + ":" + a.ID
	if previous, ok, err := tx.Once(scope, S(in, "key"), in); err != nil || ok {
		return previous, err
	}
	if r.Status != "recorded" {
		return r, conflict("观测记录已作废，请刷新后核对")
	}
	r.Status = "voided"
	r.Revision++
	r.Data["voidedBy"], r.Data["voidedName"], r.Data["voidedAt"], r.Data["voidReason"] = a.ID, a.Name, now(), S(in, "reason")
	if err = tx.Put(r); err != nil {
		return r, err
	}
	return r, tx.Remember(scope, S(in, "key"), in, r)
}

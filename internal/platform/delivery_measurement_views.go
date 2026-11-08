package platform

import "time"

func (s *Service) measurementView(tx *Tx, r Resource) MeasurementView {
	d := r.Data
	v := MeasurementView{ID: r.ID, GoalID: r.GoalID, RequirementRevision: revision(r), Type: S(d, "type"), Status: r.Status, Reference: S(d, "reference"), Note: S(d, "note"), RecordedBy: S(d, "recordedBy"), RecordedAt: r.CreatedAt, Result: S(d, "result"), ArtifactID: S(d, "artifactId"), EvidenceID: S(d, "evidenceId"), ReleaseID: S(d, "releaseId"), ProofArtifactID: S(d, "proofArtifactId"), ProofDigest: S(d, "proofDigest"), Commit: S(d, "commit"), VerificationEnvironment: S(d, "verificationEnvironment"), StartedAt: S(d, "startedAt"), EndedAt: S(d, "endedAt"), ReplacesMeasurementID: S(d, "replacesMeasurementId"), VoidReason: S(d, "voidReason"), VoidedBy: S(d, "voidedBy"), VoidedAt: S(d, "voidedAt"), Issues: []string{}}
	if r.Status == "voided" {
		return v
	}
	if r.Status != "recorded" || !Has(measurementTypes, v.Type) {
		v.Issues = append(v.Issues, "观测状态或类型无法核验")
		return v
	}
	if v.Type == "human_effort" {
		start, e1 := time.Parse(time.RFC3339Nano, v.StartedAt)
		end, e2 := time.Parse(time.RFC3339Nano, v.EndedAt)
		if e1 != nil || e2 != nil || !end.After(start) || end.Sub(start) > 24*time.Hour || S(d, "personId") != v.RecordedBy {
			v.Issues = append(v.Issues, "本人投入区间无法核验")
		} else {
			v.DurationSeconds = end.Sub(start).Seconds()
		}
	} else {
		proof, err := tx.Get(v.ProofArtifactID)
		if err != nil || proof.Kind != "artifact" || proof.ProjectID != r.ProjectID || proof.GoalID != r.GoalID || revision(proof) != revision(r) || S(proof.Data, "type") != "report" || S(proof.Data, "digest") != v.ProofDigest || S(proof.Data, "commit") != v.Commit {
			v.Issues = append(v.Issues, "观测报告的准确绑定失效")
		} else if err = s.VerifyArtifact(proof); err != nil {
			v.Issues = append(v.Issues, "观测报告的字节无法核验")
		}
		if v.Type == "preview" {
			artifact, err := tx.Get(v.ArtifactID)
			if err != nil || artifact.Kind != "artifact" || artifact.ProjectID != r.ProjectID || artifact.GoalID != r.GoalID || revision(artifact) != revision(r) || S(artifact.Data, "type") != "package" || S(artifact.Data, "digest") != S(d, "artifactDigest") || S(artifact.Data, "commit") != v.Commit {
				v.Issues = append(v.Issues, "预览制品绑定失效")
			} else if err = s.VerifyArtifact(artifact); err != nil {
				v.Issues = append(v.Issues, "预览制品的字节无法核验")
			}
		} else {
			release, err := tx.Get(v.ReleaseID)
			if err != nil || release.Kind != "release" || release.ProjectID != r.ProjectID || release.GoalID != r.GoalID || revision(release) != revision(r) || S(release.Data, "binding") != S(d, "releaseBinding") || S(release.Data, "commit") != v.Commit {
				v.Issues = append(v.Issues, "观测版本的准确绑定失效")
			} else if S(proof.Data, "purpose") == "release-feedback" {
				if err = s.validateReleaseFeedbackReport(tx, proof, release); err != nil {
					v.Issues = append(v.Issues, "上线问题报告的候选或制品绑定失效")
				}
			}
			if v.Type == "business_acceptance" && !Has([]string{"passed", "failed"}, v.Result) {
				v.Issues = append(v.Issues, "业务验收结果无法核验")
			}
		}
	}
	v.Usable = len(v.Issues) == 0
	return v
}

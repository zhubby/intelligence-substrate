package platform

func (s *Service) qualityPolicySnapshot(t *Tx, g Resource) ([]any, error) {
	policies, err := t.List(g.ProjectID, "quality_policy", g.ID)
	if err != nil {
		return nil, err
	}
	current := []any{}
	for _, policy := range policies {
		if policy.Status != "active" || revision(policy) != g.Revision || S(policy.Data, "planId") != S(g.Data, "activePlanId") {
			continue
		}
		if _, err = s.validQualityPolicy(t, policy, g); err != nil {
			return nil, err
		}
		current = append(current, M{"id": policy.ID, "binding": S(policy.Data, "binding"), "target": S(policy.Data, "target")})
	}
	return current, nil
}

func (s *Service) releaseQualityGate(t *Tx, release, g Resource, evidence []Resource) error {
	current, err := s.qualityPolicySnapshot(t, g)
	if err != nil {
		return err
	}
	previous := Items(release.Data["qualityPolicies"])
	if (len(current) > 0 || len(previous) > 0) && Hash(current) != Hash(previous) {
		return conflict("质量绑定已变化，需要重新创建候选和批准")
	}
	for _, x := range current {
		entry := Obj(x)
		policy, err := t.Get(S(entry, "id"))
		if err != nil {
			return err
		}
		for _, v := range Items(release.Data["artifacts"]) {
			artifact, err := t.Get(S(Obj(v), "id"))
			if err != nil {
				return err
			}
			if S(artifact.Data, "target") != S(policy.Data, "target") {
				continue
			}
			found := false
			for _, ev := range evidence {
				if ev.Status != "passed" || ev.ProjectID != g.ProjectID || ev.GoalID != g.ID || revision(ev) != g.Revision || S(ev.Data, "source") != "trusted-worker" || S(ev.Data, "qualityPolicyId") != policy.ID || S(ev.Data, "qualityPolicyDigest") != S(policy.Data, "binding") || S(ev.Data, "artifactId") != artifact.ID || S(ev.Data, "digest") != S(artifact.Data, "digest") || S(ev.Data, "commit") != S(artifact.Data, "commit") || S(ev.Data, "actorId") == S(artifact.Data, "producerId") {
					continue
				}
				run, err := t.Get(S(ev.Data, "runId"))
				if err != nil || run.Status != "succeeded" || S(run.Data, "evidenceId") != ev.ID {
					continue
				}
				task, err := t.Get(S(run.Data, "taskId"))
				if err != nil || task.Status != "succeeded" || S(task.Data, "qualityDispatchId") != S(ev.Data, "qualityDispatchId") || s.validateQualityTask(t, task) != nil {
					continue
				}
				if err = s.qualityEvidenceReport(t, ev); err != nil {
					continue
				}
				found = true
				break
			}
			if !found {
				return conflict("制品缺少准确自动质量绑定的独立通过证据：" + policy.Title)
			}
		}
	}
	return nil
}

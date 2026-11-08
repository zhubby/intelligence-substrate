package platform

// Scheduling shares the claim transaction: a restart cannot commit a task
// without its policy/artifact idempotency record, or create a second one.
func (s *Service) scheduleQuality(t *Tx, a Actor, worker Resource) error {
	if !B(worker.Data, "trustedQuality") || !Has(Strings(worker.Data["roles"]), "quality") || !Has(Strings(worker.Data["capabilities"]), "test") {
		return nil
	}
	policies, err := t.List(worker.ProjectID, "quality_policy", "")
	if err != nil {
		return err
	}
	runs, err := t.List(worker.ProjectID, "run", "")
	if err != nil {
		return err
	}
	created := 0
	for _, policy := range policies {
		if policy.Status != "active" || S(policy.Data, "runtime") != S(worker.Data, "runtime") {
			continue
		}
		g, err := t.Get(policy.GoalID)
		if err != nil {
			return err
		}
		if revision(policy) != g.Revision || S(policy.Data, "planId") != S(g.Data, "activePlanId") {
			continue
		}
		pipeline, policyErr := s.validQualityPolicy(t, policy, g)
		for _, run := range runs {
			if run.Status != "succeeded" || run.GoalID != g.ID || revision(run) != g.Revision || S(run.Data, "planId") != S(policy.Data, "planId") {
				continue
			}
			build, err := t.Get(S(run.Data, "taskId"))
			if err != nil || build.Status != "succeeded" || S(build.Data, "kind") != "build" || build.ProjectID != g.ProjectID || build.GoalID != g.ID || revision(build) != g.Revision || S(build.Data, "planId") != S(policy.Data, "planId") {
				continue
			}
			result := Obj(run.Data["result"])
			artifact, err := t.Get(S(result, "artifactId"))
			if err != nil || artifact.Kind != "artifact" || artifact.ProjectID != g.ProjectID || artifact.GoalID != g.ID || revision(artifact) != g.Revision || S(artifact.Data, "target") != S(policy.Data, "target") || S(artifact.Data, "target") != S(build.Data, "target") || S(artifact.Data, "type") != "package" || S(artifact.Data, "runId") != run.ID || S(artifact.Data, "producerId") != "worker:"+S(run.Data, "workerId") || S(artifact.Data, "commit") != S(result, "commit") || S(artifact.Data, "repositoryId") != S(result, "repositoryId") {
				continue
			}
			input := M{"policyId": policy.ID, "policyDigest": S(policy.Data, "binding"), "artifactId": artifact.ID, "artifactDigest": S(artifact.Data, "digest"), "buildRunId": run.ID}
			scope := "quality.schedule:" + policy.ID
			if _, exists, err := t.Once(scope, artifact.ID, input); err != nil {
				return err
			} else if exists {
				continue
			}
			dispatch := resource("quality_dispatch", policy.Title, g.ProjectID, g.ID, M{"requirementRevision": g.Revision, "planId": S(policy.Data, "planId"), "policyId": policy.ID, "policyDigest": S(policy.Data, "binding"), "pipelineId": S(policy.Data, "pipelineId"), "artifactId": artifact.ID, "artifactDigest": S(artifact.Data, "digest"), "commit": S(artifact.Data, "commit"), "repositoryId": S(artifact.Data, "repositoryId"), "target": S(artifact.Data, "target"), "buildRunId": run.ID, "buildTaskId": build.ID})
			dispatch.Status = "blocked"
			validationErr := policyErr
			if validationErr == nil {
				validationErr = s.VerifyArtifact(artifact)
			}
			if validationErr == nil {
				validationErr = s.validateSigningArtifact(t, artifact)
			}
			if validationErr != nil {
				dispatch.Data["reason"] = validationErr.Error()
			} else {
				task, err := s.qualityVerificationTask(t, policy, pipeline, artifact, build, dispatch.ID)
				if err != nil {
					return err
				}
				dispatch.Status = "scheduled"
				dispatch.Data["taskId"] = task.ID
			}
			if err = t.Put(dispatch); err != nil {
				return err
			}
			if err = t.Remember(scope, artifact.ID, input, dispatch); err != nil {
				return err
			}
			if err = t.Audit(a, "quality.schedule", dispatch, M{"status": dispatch.Status, "policyId": policy.ID, "artifactId": artifact.ID, "taskId": S(dispatch.Data, "taskId"), "reason": S(dispatch.Data, "reason")}); err != nil {
				return err
			}
			created++
			if created >= 64 {
				return nil
			}
		}
	}
	return nil
}

func (s *Service) qualityVerificationTask(t *Tx, policy, pipeline, artifact, build Resource, dispatchID string) (Resource, error) {
	tasks, err := t.List(policy.ProjectID, "task", policy.GoalID)
	if err != nil {
		return Resource{}, err
	}
	// afterRepair has already created the original policy's recheck. Bind that
	// same task to the newly built artifact rather than dispatching it twice.
	for _, task := range tasks {
		if task.Status == "pending" && S(task.Data, "requiresArtifactFrom") == build.ID && S(task.Data, "qualityPolicyId") == policy.ID && S(task.Data, "qualityPolicyDigest") == S(policy.Data, "binding") && S(task.Data, "pipelineId") == pipeline.ID && Hash(Strings(task.Data["criteria"])) == Hash(Strings(policy.Data["criteria"])) {
			task.Data["qualityDispatchId"] = dispatchID
			task.Data["artifactId"] = artifact.ID
			task.Data["commit"] = S(artifact.Data, "commit")
			task.Data["repositoryId"] = S(artifact.Data, "repositoryId")
			return task, t.Put(task)
		}
	}
	task := resource("task", pipeline.Title, policy.ProjectID, policy.GoalID, M{"kind": "test", "role": "quality", "runtime": S(pipeline.Data, "runtime"), "pipelineId": pipeline.ID, "pipelineRevision": pipeline.Revision, "qualityPipelineDigest": S(policy.Data, "pipelineDigest"), "qualityPolicyId": policy.ID, "qualityPolicyDigest": S(policy.Data, "binding"), "qualityDispatchId": dispatchID, "artifactId": artifact.ID, "repositoryId": S(artifact.Data, "repositoryId"), "commit": S(artifact.Data, "commit"), "target": S(artifact.Data, "target"), "criteria": Strings(policy.Data["criteria"]), "requirementRevision": revision(policy), "planId": S(policy.Data, "planId"), "logicalId": NewID("verify"), "attempts": 0, "maxAttempts": 3, "timeoutSeconds": N(pipeline.Data, "timeoutSeconds"), "budgetCents": N(policy.Data, "budgetCents")})
	task.Status = "pending"
	return task, t.Put(task)
}

func (s *Service) validateQualityTask(t *Tx, task Resource) error {
	id := S(task.Data, "qualityPolicyId")
	if id == "" {
		return nil
	}
	policy, err := t.Get(id)
	if err != nil {
		return err
	}
	g, err := t.Get(task.GoalID)
	if err != nil {
		return err
	}
	pipeline, err := s.validQualityPolicy(t, policy, g)
	if err != nil {
		return err
	}
	if task.ProjectID != policy.ProjectID || task.GoalID != policy.GoalID || S(task.Data, "kind") != "test" || S(task.Data, "role") != "quality" || revision(task) != revision(policy) || S(task.Data, "planId") != S(policy.Data, "planId") || S(task.Data, "qualityPolicyDigest") != S(policy.Data, "binding") || S(task.Data, "pipelineId") != pipeline.ID || N(task.Data, "pipelineRevision") != float64(pipeline.Revision) || S(task.Data, "qualityPipelineDigest") != S(policy.Data, "pipelineDigest") || S(task.Data, "runtime") != S(policy.Data, "runtime") || Hash(Strings(task.Data["criteria"])) != Hash(Strings(policy.Data["criteria"])) || N(task.Data, "budgetCents") != N(policy.Data, "budgetCents") {
		return conflict("质量工作项与批准绑定不一致")
	}
	artifact, err := t.Get(S(task.Data, "artifactId"))
	if err != nil {
		return err
	}
	if err = s.validateSigningArtifact(t, artifact); err != nil {
		return err
	}
	dispatch, err := t.Get(S(task.Data, "qualityDispatchId"))
	if err != nil || dispatch.Kind != "quality_dispatch" || dispatch.Status != "scheduled" || dispatch.ProjectID != task.ProjectID || dispatch.GoalID != task.GoalID || S(dispatch.Data, "taskId") != task.ID || S(dispatch.Data, "policyId") != policy.ID || S(dispatch.Data, "policyDigest") != S(policy.Data, "binding") || S(dispatch.Data, "artifactId") != artifact.ID || S(dispatch.Data, "artifactDigest") != S(artifact.Data, "digest") || S(dispatch.Data, "commit") != S(artifact.Data, "commit") || S(dispatch.Data, "repositoryId") != S(artifact.Data, "repositoryId") || S(artifact.Data, "target") != S(policy.Data, "target") || S(task.Data, "commit") != S(artifact.Data, "commit") || S(task.Data, "repositoryId") != S(artifact.Data, "repositoryId") {
		return conflict("质量调度与准确制品绑定不一致")
	}
	return nil
}

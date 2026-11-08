package platform

import "context"

func planGenerationFields(in M, allowed ...string) error {
	for key := range in {
		if !Has(allowed, key) {
			return invalid("方案生成包含不支持的字段：" + key)
		}
	}
	return nil
}

func planGenerator(a Actor) error {
	if !Has([]string{"human", "agent"}, a.Kind) {
		return denied()
	}
	return nil
}

// Capture stable planning inputs in the same transaction as the durable intent.
// Spend, heartbeat times and task progress are not changes to a planning basis.
func planGenerationContext(tx *Tx, g Resource, selection M) (M, error) {
	if err := approved(tx, g); err != nil {
		return nil, err
	}
	resources, err := tx.List(g.ProjectID, "", "")
	if err != nil {
		return nil, err
	}
	clarifications, err := ApprovedClarifications(g, resources)
	if err != nil {
		return nil, err
	}
	design, err := planDesign(tx, g, selection)
	if err != nil {
		return nil, err
	}
	project, err := tx.Get(g.ProjectID)
	if err != nil {
		return nil, err
	}
	knowledge, repositories, workers, approvals := []any{}, []any{}, []any{}, []any{}
	var activePlan any
	for _, r := range resources {
		switch r.Kind {
		case "knowledge":
			if r.Status != "superseded" {
				knowledge = append(knowledge, M{"id": r.ID, "revision": r.Revision, "title": r.Title, "content": r.Data["content"], "source": r.Data["source"]})
			}
		case "repository":
			repositories = append(repositories, M{"id": r.ID, "title": r.Title, "defaultBranch": r.Data["defaultBranch"], "head": r.Data["head"]})
		case "worker":
			if r.Status != "disabled" {
				workers = append(workers, M{"id": r.ID, "title": r.Title, "runtime": r.Data["runtime"], "roles": r.Data["roles"], "capabilities": r.Data["capabilities"]})
			}
		case "approval":
			if r.GoalID == g.ID && S(r.Data, "type") == "baseline" && revision(r) == g.Revision && r.Status != "stale" && S(r.Data, "decision") == "approved" {
				approvals = append(approvals, M{"id": r.ID, "binding": r.Data["binding"], "clarificationsDigest": r.Data["clarificationsDigest"], "actorId": r.Data["actorId"]})
			}
		case "plan":
			if r.GoalID == g.ID && r.ID == S(g.Data, "activePlanId") {
				activePlan = M{"id": r.ID, "revision": r.Revision, "status": r.Status, "binding": r.Data["binding"], "requirementRevision": r.Data["requirementRevision"], "summary": r.Data["summary"], "tasks": r.Data["tasks"]}
			}
		}
	}
	// copyM normalizes nested maps/slices through JSON before hashing/persisting.
	input := M{"goalId": g.ID, "requirementRevision": g.Revision, "title": g.Title, "outcome": g.Data["outcome"], "requirementBrief": g.Data["requirementBrief"], "requirementProposalId": g.Data["requirementProposalId"], "requirementContentDigest": g.Data["requirementContentDigest"], "criteria": g.Data["criteria"], "targets": g.Data["targets"], "budgetCents": g.Data["budgetCents"], "sources": g.Data["sources"], "allowExternalModels": B(project.Data, "allowExternalModels"), "baselineApprovals": approvals, "activePlan": activePlan, "knowledge": knowledge, "repositories": repositories, "workers": workers, "clarifications": clarifications}
	if design != nil {
		input["selectedDesign"] = design
	}
	blueprint, err := planBlueprint(tx, g, selection)
	if err != nil {
		return nil, err
	}
	if blueprint != nil {
		input["selectedBlueprint"], input["initializeBlueprint"] = blueprint, B(selection, "initializeBlueprint")
	}
	issues, err := releaseIssueSnapshot(tx, g)
	if err != nil {
		return nil, err
	}
	if len(issues) > 0 {
		input["releaseIssues"] = issues
	}
	return copyM(input), nil
}

func generationBlueprintSelection(request Resource) M {
	blueprint := Obj(Obj(request.Data["input"])["selectedBlueprint"])
	if len(blueprint) == 0 {
		return M{}
	}
	return M{"blueprintId": S(blueprint, "id"), "expectedBlueprintBinding": S(Obj(blueprint["data"]), "binding"), "initializeBlueprint": B(Obj(request.Data["input"]), "initializeBlueprint")}
}

func generationDesignSelection(request Resource) M {
	design := Obj(Obj(request.Data["input"])["selectedDesign"])
	if len(design) == 0 {
		return M{}
	}
	return M{"designId": S(design, "id"), "expectedDesignBinding": S(Obj(design["data"]), "binding")}
}

func ValidatePlanGenerationContext(tx *Tx, g, request Resource) error {
	if request.Kind != "plan_generation" || request.GoalID != g.ID || request.ProjectID != g.ProjectID || request.Status != "running" {
		return conflict("方案生成意图不可用")
	}
	if err := clarificationRevision(g, request.Data["requirementRevision"]); err != nil {
		return err
	}
	selection := generationDesignSelection(request)
	for key, value := range generationBlueprintSelection(request) {
		selection[key] = value
	}
	input, err := planGenerationContext(tx, g, selection)
	if err != nil {
		return err
	}
	if Hash(input) != S(request.Data, "inputDigest") || Hash(request.Data["input"]) != Hash(input) {
		return conflict("方案规划上下文已变化，请读取当前基线与项目输入后重新规划")
	}
	return nil
}

func (s *Service) BeginPlanGeneration(ctx context.Context, actor Actor, in M) (Resource, bool, error) {
	var request Resource
	started := false
	err := s.Store.Update(ctx, func(tx *Tx) error {
		a, err := tx.CurrentActor(actor)
		if err != nil {
			return err
		}
		g, err := s.scoped(tx, a, S(in, "goalId"), "goal", "product", "architect", "developer")
		if err != nil {
			return err
		}
		if err = planGenerator(a); err != nil {
			return err
		}
		if err = planGenerationFields(in, "goalId", "expectedRequirementRevision", "key", "designId", "expectedDesignBinding", "blueprintId", "expectedBlueprintBinding", "initializeBlueprint"); err != nil {
			return err
		}
		if err = validateText(S(in, "key"), "幂等键", 160); err != nil {
			return err
		}
		scope := "plans-generate:" + g.ID + ":" + a.ID
		previous, exists, err := tx.Once(scope, S(in, "key"), in)
		if err != nil {
			return err
		}
		if exists {
			request = previous
			return nil
		}
		if err = clarificationRevision(g, in["expectedRequirementRevision"]); err != nil {
			return err
		}
		input, err := planGenerationContext(tx, g, in)
		if err != nil {
			return err
		}
		if design := input["selectedDesign"]; design != nil {
			if err = s.validatePlanDesign(tx, g, design); err != nil {
				return err
			}
		}
		if err = s.validatePlanBlueprint(tx, g, input["selectedBlueprint"]); err != nil {
			return err
		}
		if err = s.validatePlanReleaseIssues(tx, g, input["releaseIssues"]); err != nil {
			return err
		}
		requests, err := tx.List(g.ProjectID, "plan_generation", g.ID)
		if err != nil {
			return err
		}
		if len(requests) >= 200 {
			return conflict("当前目标已达到 200 次方案生成限制")
		}
		request = resource("plan_generation", "模型生成交付方案", g.ProjectID, g.ID, M{"requirementRevision": g.Revision, "requestedBy": a.ID, "requestedName": a.Name, "requestedKind": a.Kind, "input": input, "inputDigest": Hash(input)})
		request.Status = "running"
		if err = tx.Put(request); err != nil {
			return err
		}
		if err = tx.Remember(scope, S(in, "key"), in, request); err != nil {
			return err
		}
		if err = tx.Audit(a, "plans.generate.start", request, M{"requirementRevision": g.Revision, "inputDigest": S(request.Data, "inputDigest")}); err != nil {
			return err
		}
		started = true
		return nil
	})
	return request, started && err == nil, err
}

func planGenerationCharge(tx *Tx, request Resource) (Resource, error) {
	charges, err := tx.List(request.ProjectID, "charge", request.GoalID)
	if err != nil {
		return Resource{}, err
	}
	var found Resource
	for _, charge := range charges {
		if S(charge.Data, "planRequestId") != request.ID {
			continue
		}
		if found.ID != "" || S(charge.Data, "actorId") != S(request.Data, "requestedBy") {
			return Resource{}, conflict("方案生成费用关联不唯一或请求人不符，需要核对")
		}
		found = charge
	}
	return found, nil
}

// Lifecycle methods are trusted integration calls, not public commands or MCP.
func (s *Service) CompletePlanGeneration(ctx context.Context, actor Actor, requestID string, content M) (Resource, error) {
	var plan Resource
	err := s.Store.Update(ctx, func(tx *Tx) error {
		a, err := tx.CurrentActor(actor)
		if err != nil {
			return err
		}
		request, err := s.scoped(tx, a, requestID, "plan_generation", "product", "architect", "developer")
		if err != nil {
			return err
		}
		if err = planGenerator(a); err != nil {
			return err
		}
		if S(request.Data, "requestedBy") != a.ID || S(request.Data, "requestedKind") != a.Kind {
			return denied()
		}
		g, err := tx.Get(request.GoalID)
		if err != nil {
			return err
		}
		if err = ValidatePlanGenerationContext(tx, g, request); err != nil {
			return err
		}
		charge, err := planGenerationCharge(tx, request)
		if err != nil {
			return err
		}
		if charge.ID == "" || charge.Status != "settled" || S(charge.Data, "outcome") != "completed" {
			return conflict("方案模型结果或费用尚未确认")
		}
		if content == nil {
			return invalid("模型方案必须为包含说明和工作项的对象")
		}
		if err = planGenerationFields(content, "summary", "risk", "tasks"); err != nil {
			return err
		}
		if value, exists := content["risk"]; exists {
			text, ok := value.(string)
			if !ok || len(text) > 12000 {
				return invalid("方案风险须为不超过 12000 字节的文本")
			}
		}
		in := copyM(content)
		in["goalId"], in["expectedRequirementRevision"] = g.ID, g.Revision
		for key, value := range generationDesignSelection(request) {
			in[key] = value
		}
		for key, value := range generationBlueprintSelection(request) {
			in[key] = value
		}
		plan, err = s.planCommand(tx, a, "plans.propose", in)
		if err != nil {
			return err
		}
		plan.Data["generationId"], plan.Data["inputDigest"], plan.Data["chargeId"], plan.Data["origin"] = request.ID, request.Data["inputDigest"], charge.ID, "model"
		delete(plan.Data, "binding")
		plan.Data["binding"] = Hash(plan.Data)
		if err = tx.Put(plan); err != nil {
			return err
		}
		request.Status, request.Revision = "succeeded", request.Revision+1
		request.Data["resultId"], request.Data["chargeId"], request.Data["completedAt"] = plan.ID, charge.ID, now()
		if err = tx.Put(request); err != nil {
			return err
		}
		if err = tx.Audit(a, "plans.propose", plan, M{"generationId": request.ID}); err != nil {
			return err
		}
		return tx.Audit(a, "plans.generate.complete", plan, M{"generationId": request.ID, "chargeId": charge.ID, "inputDigest": request.Data["inputDigest"], "requirementRevision": g.Revision})
	})
	return plan, err
}

func (s *Service) FailPlanGeneration(ctx context.Context, requestID, reason string) error {
	return s.Store.Update(ctx, func(tx *Tx) error {
		request, err := tx.Get(requestID)
		if err != nil {
			return err
		}
		if request.Kind != "plan_generation" {
			return missing()
		}
		if request.Status != "running" {
			return nil
		}
		charge, err := planGenerationCharge(tx, request)
		if err != nil {
			return err
		}
		request.Status, request.Revision = "failed", request.Revision+1
		request.Data["errorCode"], request.Data["completedAt"] = reason, now()
		if charge.ID != "" {
			request.Data["chargeId"] = charge.ID
		}
		if err = tx.Put(request); err != nil {
			return err
		}
		return tx.Audit(Actor{ID: "system:plans", Kind: "system"}, "plans.generate.failed", request, M{"reason": reason, "chargeId": charge.ID})
	})
}

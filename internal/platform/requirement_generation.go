package platform

import "context"

// ValidateRequirementGenerationContext is used at model admission and completion.
// Changes within the same requirement revision (new decisions/knowledge) matter.
func ValidateRequirementGenerationContext(tx *Tx, g, request Resource) error {
	if request.Kind != "requirement_generation" || request.GoalID != g.ID || request.ProjectID != g.ProjectID || request.Status != "running" {
		return conflict("需求稿生成意图不可用")
	}
	if err := clarificationDraft(g, request.Data["requirementRevision"]); err != nil {
		return err
	}
	input, err := requirementDraftContext(tx, g)
	if err != nil {
		return err
	}
	if Hash(input) != S(request.Data, "inputDigest") || Hash(request.Data["input"]) != Hash(input) {
		return conflict("需求上下文已变化，请重新整理需求稿")
	}
	return nil
}

func (s *Service) BeginRequirementGeneration(ctx context.Context, actor Actor, in M) (Resource, bool, error) {
	var request Resource
	started := false
	err := s.Store.Update(ctx, func(tx *Tx) error {
		a, err := tx.CurrentActor(actor)
		if err != nil {
			return err
		}
		g, err := s.scoped(tx, a, S(in, "goalId"), "goal", "product")
		if err != nil {
			return err
		}
		if err = human(a); err != nil {
			return err
		}
		if err = clarificationFields(in, "goalId", "expectedRequirementRevision", "key"); err != nil {
			return err
		}
		if err = validateText(S(in, "key"), "幂等键", 160); err != nil {
			return err
		}
		scope := "requirements-generate:" + g.ID + ":" + a.ID
		previous, exists, err := tx.Once(scope, S(in, "key"), in)
		if err != nil {
			return err
		}
		if exists {
			request = previous
			return nil
		}
		if err = clarificationDraft(g, in["expectedRequirementRevision"]); err != nil {
			return err
		}
		input, err := requirementDraftContext(tx, g)
		if err != nil {
			return err
		}
		requests, err := tx.List(g.ProjectID, "requirement_generation", g.ID)
		if err != nil {
			return err
		}
		if len(requests) >= 200 {
			return conflict("当前目标已达到 200 次需求稿生成限制")
		}
		request = resource("requirement_generation", "模型整理需求稿", g.ProjectID, g.ID, M{"requirementRevision": g.Revision, "requestedBy": a.ID, "requestedName": a.Name, "input": input, "inputDigest": Hash(input)})
		request.Status = "running"
		if err = tx.Put(request); err != nil {
			return err
		}
		if err = tx.Remember(scope, S(in, "key"), in, request); err != nil {
			return err
		}
		if err = tx.Audit(a, "requirements.generate.start", request, M{"requirementRevision": g.Revision, "inputDigest": S(request.Data, "inputDigest")}); err != nil {
			return err
		}
		started = true
		return nil
	})
	return request, started && err == nil, err
}

func requirementGenerationCharge(tx *Tx, request Resource) (Resource, error) {
	charges, err := tx.List(request.ProjectID, "charge", request.GoalID)
	if err != nil {
		return Resource{}, err
	}
	var found Resource
	for _, charge := range charges {
		if S(charge.Data, "requirementRequestId") != request.ID {
			continue
		}
		if found.ID != "" {
			return Resource{}, conflict("需求稿生成关联多条费用，需要核对")
		}
		found = charge
	}
	return found, nil
}

// These lifecycle methods are trusted integration entry points, never commands.
func (s *Service) CompleteRequirementGeneration(ctx context.Context, actor Actor, requestID string, content any) (Resource, error) {
	var draft Resource
	err := s.Store.Update(ctx, func(tx *Tx) error {
		a, err := tx.CurrentActor(actor)
		if err != nil {
			return err
		}
		request, err := s.scoped(tx, a, requestID, "requirement_generation", "product")
		if err != nil {
			return err
		}
		if err = human(a); err != nil {
			return err
		}
		if S(request.Data, "requestedBy") != a.ID {
			return denied()
		}
		g, err := tx.Get(request.GoalID)
		if err != nil {
			return err
		}
		if err = ValidateRequirementGenerationContext(tx, g, request); err != nil {
			return err
		}
		charge, err := requirementGenerationCharge(tx, request)
		if err != nil {
			return err
		}
		if charge.ID == "" || charge.Status != "settled" || S(charge.Data, "outcome") != "completed" {
			return conflict("需求稿模型结果或费用尚未确认")
		}
		draft, err = createRequirementProposal(tx, a, g, content, Obj(request.Data["input"]), "model", request.ID)
		if err != nil {
			return err
		}
		draft.Data["chargeId"] = charge.ID
		if err = tx.Put(draft); err != nil {
			return err
		}
		request.Status, request.Revision = "succeeded", request.Revision+1
		request.Data["resultId"], request.Data["chargeId"], request.Data["completedAt"] = draft.ID, charge.ID, now()
		if err = tx.Put(request); err != nil {
			return err
		}
		return tx.Audit(a, "requirements.generate.complete", draft, M{"generationId": request.ID, "chargeId": charge.ID, "requirementRevision": g.Revision})
	})
	return draft, err
}

func (s *Service) FailRequirementGeneration(ctx context.Context, requestID, reason string) error {
	return s.Store.Update(ctx, func(tx *Tx) error {
		request, err := tx.Get(requestID)
		if err != nil {
			return err
		}
		if request.Kind != "requirement_generation" {
			return missing()
		}
		if request.Status != "running" {
			return nil
		}
		charge, err := requirementGenerationCharge(tx, request)
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
		return tx.Audit(Actor{ID: "system:requirements", Kind: "system"}, "requirements.generate.failed", request, M{"reason": reason, "chargeId": charge.ID})
	})
}

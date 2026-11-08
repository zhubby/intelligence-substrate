package platform

import "context"

// Begin commits the intent before inference. Replays never authorize another
// provider attempt, even when a process died with a running intent.
func (s *Service) BeginClarificationGeneration(ctx context.Context, actor Actor, in M) (Resource, bool, error) {
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
		scope := "clarifications-generate:" + g.ID + ":" + a.ID
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
		items, err := tx.List(g.ProjectID, "", "")
		if err != nil {
			return err
		}
		knowledge, questions := []any{}, []any{}
		generations := 0
		for _, item := range items {
			if item.Kind == "knowledge" && item.Status != "superseded" {
				knowledge = append(knowledge, M{"id": item.ID, "revision": item.Revision, "title": item.Title, "content": item.Data["content"], "source": item.Data["source"]})
			}
			if item.GoalID != g.ID {
				continue
			}
			if item.Kind == "clarification_generation" {
				generations++
			}
			if item.Kind == "clarification" && revision(item) == g.Revision {
				for _, value := range Items(item.Data["questions"]) {
					q := Obj(value)
					questions = append(questions, M{"question": q["question"], "context": q["context"], "status": q["status"], "answer": q["answer"]})
				}
			}
		}
		if len(questions) >= 100 || generations >= 200 {
			return conflict("当前目标达到澄清问题或生成请求限制")
		}
		input := M{"outcome": g.Data["outcome"], "criteria": g.Data["criteria"], "targets": g.Data["targets"], "knowledge": knowledge, "clarifications": questions}
		request = resource("clarification_generation", "模型需求澄清", g.ProjectID, g.ID, M{"requirementRevision": g.Revision, "requestedBy": a.ID, "requestedName": a.Name, "input": input, "inputDigest": Hash(input)})
		request.Status = "running"
		if err = tx.Put(request); err != nil {
			return err
		}
		if err = tx.Remember(scope, S(in, "key"), in, request); err != nil {
			return err
		}
		if err = tx.Audit(a, "clarifications.generate.start", request, M{"requirementRevision": g.Revision, "inputDigest": S(request.Data, "inputDigest")}); err != nil {
			return err
		}
		started = true
		return nil
	})
	return request, started && err == nil, err
}

func generationCharge(tx *Tx, request Resource) (string, error) {
	charges, err := tx.List(request.ProjectID, "charge", request.GoalID)
	if err != nil {
		return "", err
	}
	id := ""
	for _, charge := range charges {
		if S(charge.Data, "clarificationRequestId") != request.ID {
			continue
		}
		if id != "" {
			return "", conflict("需求澄清生成有多条费用记录，需要核对")
		}
		id = charge.ID
	}
	return id, nil
}

// Completion is a trusted integration entry point, never a public command.
// Questions, cost linkage, audit and the completed intent commit together.
func (s *Service) CompleteClarificationGeneration(ctx context.Context, actor Actor, requestID string, questions any) (Resource, error) {
	var batch Resource
	err := s.Store.Update(ctx, func(tx *Tx) error {
		a, err := tx.CurrentActor(actor)
		if err != nil {
			return err
		}
		request, err := s.scoped(tx, a, requestID, "clarification_generation", "product")
		if err != nil {
			return err
		}
		if err = human(a); err != nil {
			return err
		}
		if request.Status != "running" || S(request.Data, "requestedBy") != a.ID {
			return conflict("需求澄清生成请求已处理或不属于当前人")
		}
		g, err := tx.Get(request.GoalID)
		if err != nil {
			return err
		}
		if err = clarificationDraft(g, request.Data["requirementRevision"]); err != nil {
			return err
		}
		chargeID, err := generationCharge(tx, request)
		if err != nil {
			return err
		}
		if chargeID == "" {
			return conflict("需求澄清缺少模型费用来源")
		}
		charge, err := tx.Get(chargeID)
		if err != nil {
			return err
		}
		if charge.Status != "settled" || S(charge.Data, "outcome") != "completed" {
			return conflict("需求澄清生成结果或费用尚未确认")
		}
		batch, err = createClarificationBatch(tx, a, g, questions, "model", request.ID)
		if err != nil {
			return err
		}
		batch.Data["chargeId"] = chargeID
		if err = tx.Put(batch); err != nil {
			return err
		}
		request.Status = "succeeded"
		request.Revision++
		request.Data["resultId"], request.Data["chargeId"], request.Data["completedAt"] = batch.ID, chargeID, now()
		if err = tx.Put(request); err != nil {
			return err
		}
		return tx.Audit(a, "clarifications.generate.complete", batch, M{"generationId": request.ID, "chargeId": chargeID, "requirementRevision": g.Revision})
	})
	return batch, err
}

// Even if the requester was revoked while the provider worked, its existing
// intent must record failure. This internal path can neither create questions
// nor refund/retry a possibly billable call.
func (s *Service) FailClarificationGeneration(ctx context.Context, requestID, reason string) error {
	return s.Store.Update(ctx, func(tx *Tx) error {
		request, err := tx.Get(requestID)
		if err != nil {
			return err
		}
		if request.Kind != "clarification_generation" {
			return missing()
		}
		if request.Status != "running" {
			return nil
		}
		chargeID, err := generationCharge(tx, request)
		if err != nil {
			return err
		}
		request.Status = "failed"
		request.Revision++
		request.Data["errorCode"], request.Data["completedAt"] = reason, now()
		if chargeID != "" {
			request.Data["chargeId"] = chargeID
		}
		if err = tx.Put(request); err != nil {
			return err
		}
		return tx.Audit(Actor{ID: "system:clarifications", Kind: "system"}, "clarifications.generate.failed", request, M{"reason": reason, "chargeId": chargeID})
	})
}

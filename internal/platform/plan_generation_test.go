package platform

import (
	"context"
	"testing"
)

func planGenerationInput(g Resource, key string) M {
	return M{"goalId": g.ID, "expectedRequirementRevision": g.Revision, "key": key}
}

func generatedPlanContent() M {
	return M{"summary": "Implement the approved support workflow", "risk": "Independent quality verification is required", "tasks": []any{M{"id": "analysis", "title": "Analyze the support workflow", "kind": "analysis", "role": "analysis", "runtime": "linux"}}}
}

func putPlanGenerationCharge(t *testing.T, s *Service, request Resource) Resource {
	t.Helper()
	charge := resource("charge", "Synthetic completed inference", request.ProjectID, request.GoalID, M{"planRequestId": request.ID, "actorId": request.Data["requestedBy"], "outcome": "completed", "chargedCents": 1})
	charge.Status = "settled"
	if err := s.Store.Update(context.Background(), func(tx *Tx) error { return tx.Put(charge) }); err != nil {
		t.Fatal(err)
	}
	return charge
}

func TestPlanGenerationIntentReplayAndTransactionalCompletion(t *testing.T) {
	s, a, project := fixture(t)
	g := goal(t, s, a, project)
	answeredDraftQuestion(t, s, a, g)
	must(t, s, a, "goals.approve", M{"id": g.ID})
	in := planGenerationInput(g, "one-provider-attempt")
	request, started, err := s.BeginPlanGeneration(context.Background(), a, in)
	if err != nil || !started || request.Status != "running" || S(request.Data, "inputDigest") != Hash(request.Data["input"]) {
		t.Fatal(request, started, err)
	}
	if len(Items(Obj(request.Data["input"])["clarifications"])) != 1 {
		t.Fatal("missing frozen human decisions", request)
	}
	if replay, newAttempt, err := s.BeginPlanGeneration(context.Background(), a, in); err != nil || newAttempt || replay.ID != request.ID {
		t.Fatal("running replay authorized inference", replay, newAttempt, err)
	}
	_, err = s.CompletePlanGeneration(context.Background(), a, request.ID, generatedPlanContent())
	clarificationFault(t, err, 409)
	charge := putPlanGenerationCharge(t, s, request)
	plan, err := s.CompletePlanGeneration(context.Background(), a, request.ID, generatedPlanContent())
	if err != nil || plan.Status != "proposed" || S(plan.Data, "generationId") != request.ID || S(plan.Data, "chargeId") != charge.ID || S(plan.Data, "inputDigest") != S(request.Data, "inputDigest") {
		t.Fatal(plan, err)
	}
	bound := copyM(plan.Data)
	delete(bound, "binding")
	if Hash(bound) != S(plan.Data, "binding") {
		t.Fatal("approval binding omitted model provenance")
	}
	if tasks, _ := s.Store.List(project.ID, "task", g.ID); len(tasks) != 0 {
		t.Fatal("generation activated execution", tasks)
	}
	if approvals, _ := s.Store.List(project.ID, "approval", g.ID); len(approvals) != 1 {
		t.Fatal("generation approved its own plan", approvals)
	}
	if replay, newAttempt, err := s.BeginPlanGeneration(context.Background(), a, in); err != nil || newAttempt || replay.Status != "succeeded" || S(replay.Data, "resultId") != plan.ID {
		t.Fatal(replay, newAttempt, err)
	}
	_, _, err = s.BeginPlanGeneration(context.Background(), a, M{"goalId": g.ID, "expectedRequirementRevision": 2, "key": "one-provider-attempt"})
	clarificationFault(t, err, 409)
	_, err = s.CompletePlanGeneration(context.Background(), a, request.ID, generatedPlanContent())
	clarificationFault(t, err, 409)
}

func TestPlanGenerationStrictInputAndCurrentAuthority(t *testing.T) {
	s, a, project := fixture(t)
	g := goal(t, s, a, project)
	_, _, err := s.BeginPlanGeneration(context.Background(), a, planGenerationInput(g, "unapproved"))
	clarificationFault(t, err, 409)
	must(t, s, a, "goals.approve", M{"id": g.ID})
	for _, in := range []M{
		{"goalId": g.ID, "key": "no-revision"},
		{"goalId": g.ID, "expectedRequirementRevision": "1", "key": "string-revision"},
		{"goalId": g.ID, "expectedRequirementRevision": 1.5, "key": "fraction-revision"},
		{"goalId": g.ID, "expectedRequirementRevision": 1},
		{"goalId": g.ID, "expectedRequirementRevision": 1, "key": "", "approve": true},
	} {
		_, _, err := s.BeginPlanGeneration(context.Background(), a, in)
		clarificationFault(t, err, 422)
	}
	for _, actor := range []Actor{
		{ID: "worker", Kind: "worker", ProjectID: project.ID, Roles: []string{"owner"}},
		{ID: "viewer", Kind: "human", ProjectID: project.ID, Roles: []string{"viewer"}},
		{ID: "foreign", Kind: "agent", ProjectID: "foreign-project", Roles: []string{"architect"}},
	} {
		_, _, err := s.BeginPlanGeneration(context.Background(), actor, planGenerationInput(g, actor.ID))
		clarificationFault(t, err, 403)
	}
	agent := Actor{ID: "architect-agent", Kind: "agent", ProjectID: project.ID, Roles: []string{"architect"}}
	r, started, err := s.BeginPlanGeneration(context.Background(), agent, planGenerationInput(g, "agent"))
	if err != nil || !started || S(r.Data, "requestedKind") != "agent" {
		t.Fatal(r, started, err)
	}
	putPlanGenerationCharge(t, s, r)
	plan, err := s.CompletePlanGeneration(context.Background(), agent, r.ID, generatedPlanContent())
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Command(context.Background(), agent, "plans.approve", M{"id": plan.ID})
	clarificationFault(t, err, 403)
}

func TestPlanGenerationRejectsChangedSnapshotBeforeCompletion(t *testing.T) {
	for _, mode := range []string{"revision", "knowledge", "repository", "worker", "active-plan", "approval", "tamper-input", "tamper-digest"} {
		t.Run(mode, func(t *testing.T) {
			s, a, project := fixture(t)
			g := goal(t, s, a, project)
			must(t, s, a, "goals.approve", M{"id": g.ID})
			repository := must(t, s, a, "repositories.create", M{"projectId": project.ID, "title": "Current repository"})
			worker := must(t, s, a, "workers.register", M{"projectId": project.ID, "title": "Linux build", "runtime": "linux", "roles": []string{"developer"}, "capabilities": []string{"build"}})
			r, _, err := s.BeginPlanGeneration(context.Background(), a, planGenerationInput(g, "snapshot"))
			if err != nil {
				t.Fatal(err)
			}
			putPlanGenerationCharge(t, s, r)
			switch mode {
			case "revision":
				must(t, s, a, "goals.revise", M{"id": g.ID, "reason": "Business scope changed"})
			case "knowledge":
				must(t, s, a, "knowledge.create", M{"projectId": project.ID, "title": "Business constraint", "content": "New policy", "source": "Human review"})
			case "repository":
				if err := s.Store.Update(context.Background(), func(tx *Tx) error {
					current, err := tx.Get(repository.ID)
					if err != nil {
						return err
					}
					current.Data["head"] = "1111111111111111111111111111111111111111"
					return tx.Put(current)
				}); err != nil {
					t.Fatal(err)
				}
			case "worker":
				worker.Status = "disabled"
				if err := s.Store.Update(context.Background(), func(tx *Tx) error { return tx.Put(worker) }); err != nil {
					t.Fatal(err)
				}
			case "active-plan":
				content := generatedPlanContent()
				content["goalId"] = g.ID
				plan := must(t, s, a, "plans.propose", content)
				must(t, s, a, "plans.approve", M{"id": plan.ID})
			case "approval":
				must(t, s, a, "goals.approve", M{"id": g.ID})
			case "tamper-input", "tamper-digest":
				if mode == "tamper-input" {
					Obj(r.Data["input"])["outcome"] = "Unapproved replacement"
				} else {
					r.Data["inputDigest"] = "incorrect"
				}
				if err := s.Store.Update(context.Background(), func(tx *Tx) error { return tx.Put(r) }); err != nil {
					t.Fatal(err)
				}
			}
			_, err = s.CompletePlanGeneration(context.Background(), a, r.ID, generatedPlanContent())
			clarificationFault(t, err, 409)
			plans, _ := s.Store.List(project.ID, "plan", g.ID)
			expected := 0
			if mode == "active-plan" {
				expected = 1
			}
			if len(plans) != expected {
				t.Fatal("stale result became a proposal", plans)
			}
		})
	}
}

func TestPlanGenerationIgnoresInferenceSpendHeartbeatAndForeignKnowledge(t *testing.T) {
	s, a, project := fixture(t)
	g := goal(t, s, a, project)
	must(t, s, a, "goals.approve", M{"id": g.ID})
	worker := must(t, s, a, "workers.register", M{"projectId": project.ID, "title": "Developer", "runtime": "linux", "roles": []string{"developer"}, "capabilities": []string{"code"}})
	r, _, err := s.BeginPlanGeneration(context.Background(), a, planGenerationInput(g, "unrelated"))
	if err != nil {
		t.Fatal(err)
	}
	other := must(t, s, a, "projects.create", M{"title": "Other project"})
	must(t, s, a, "knowledge.create", M{"projectId": other.ID, "title": "Private", "content": "Unrelated", "source": "Human"})
	if err := s.Store.Update(context.Background(), func(tx *Tx) error {
		current, _ := tx.Get(g.ID)
		current.Data["spentCents"] = 3
		worker.Data["lastSeen"] = now()
		if err := tx.Put(worker); err != nil {
			return err
		}
		return tx.Put(current)
	}); err != nil {
		t.Fatal(err)
	}
	putPlanGenerationCharge(t, s, r)
	if _, err := s.CompletePlanGeneration(context.Background(), a, r.ID, generatedPlanContent()); err != nil {
		t.Fatal("volatile progress invalidated stable planning input", err)
	}
}

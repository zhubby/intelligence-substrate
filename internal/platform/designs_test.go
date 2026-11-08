package platform

import (
	"bytes"
	"context"
	"os"
	"testing"
)

func designFixture(t *testing.T) (*Service, Actor, Resource, Resource, Resource) {
	t.Helper()
	s, owner, project := fixture(t)
	g := goal(t, s, owner, project)
	must(t, s, owner, "goals.approve", M{"id": g.ID})
	repo := must(t, s, owner, "repositories.create", M{"projectId": project.ID, "title": "Design source"})
	repo = must(t, s, owner, "repositories.commit", M{"id": repo.ID, "branch": "design", "message": "Prototype source", "files": M{"prototype.html": "<!doctype html><button>Submit a request</button>"}})
	artifact, err := s.Upload(context.Background(), owner, M{"goalId": g.ID, "repositoryId": repo.ID, "commit": repo.Data["head"], "target": "web", "type": "prototype", "name": "prototype.html"}, bytes.NewBufferString("<!doctype html><button>Submit a request</button>"))
	if err != nil {
		t.Fatal(err)
	}
	return s, owner, project, g, artifact
}

func designInput(g, prototype Resource, key string) M {
	return M{"goalId": g.ID, "expectedRequirementRevision": g.Revision, "key": key, "title": "Request workflow", "summary": "A single request form and status list", "decisions": "CONFIRMED-DESIGN: submit once, show status, no payment.", "criteria": []any{"ticket"}, "prototypeArtifactIds": []any{prototype.ID}, "reason": "Review the primary customer journey"}
}

func designPlan(g, design Resource) M {
	in := generatedPlanContent()
	in["goalId"], in["expectedRequirementRevision"] = g.ID, g.Revision
	in["designId"], in["expectedDesignBinding"] = design.ID, design.Data["binding"]
	return in
}

// Removing immutable version creation or storing caller-supplied prototype
// metadata would lose the exact reviewed input and its replay guarantee.
func TestDesignProposalsVersionAndBindRealPrototype(t *testing.T) {
	s, owner, project, g, prototype := designFixture(t)
	in := designInput(g, prototype, "one-design")
	design := must(t, s, owner, "designs.propose", in)
	if design.Kind != "design" || design.Revision != 1 || design.Status != "proposed" || revision(design) != 1 {
		t.Fatal(design)
	}
	items := Items(design.Data["prototypes"])
	if len(items) != 1 || S(Obj(items[0]), "artifactId") != prototype.ID || S(Obj(items[0]), "digest") != S(prototype.Data, "digest") || S(Obj(items[0]), "commit") != S(prototype.Data, "commit") {
		t.Fatal("lost exact prototype/source binding", design)
	}
	first, _ := s.Store.Get(design.ID)
	if replay := must(t, s, owner, "designs.propose", in); replay.ID != design.ID {
		t.Fatal("duplicate proposal", replay)
	}
	changed := copyM(in)
	changed["summary"] = "A different customer journey"
	_, err := s.Command(context.Background(), owner, "designs.propose", changed)
	clarificationFault(t, err, 409)
	changed["key"] = "new-version"
	second := must(t, s, owner, "designs.propose", changed)
	after, _ := s.Store.Get(first.ID)
	if second.Revision != 2 || Hash(after) != Hash(first) {
		t.Fatal("new proposal changed old design", after, first, second)
	}
	if tasks, _ := s.Store.List(project.ID, "task", g.ID); len(tasks) != 0 {
		t.Fatal("proposal created execution authority", tasks)
	}
}

// Plan approval must bind exactly the design the architect reviewed, and must
// not authorize work if the underlying prototype bytes changed meanwhile.
func TestDesignPlanApprovalFreezesSnapshotAndRechecksBytes(t *testing.T) {
	s, owner, project, g, prototype := designFixture(t)
	design := must(t, s, owner, "designs.propose", designInput(g, prototype, "selected"))
	plan := must(t, s, owner, "plans.propose", designPlan(g, design))
	snapshot := Obj(plan.Data["design"])
	if S(snapshot, "id") != design.ID || S(Obj(snapshot["data"]), "decisions") != "CONFIRMED-DESIGN: submit once, show status, no payment." {
		t.Fatal("selected design missing from plan", plan)
	}
	path := s.ArtifactPath(S(prototype.Data, "digest"))
	body, _ := os.ReadFile(path)
	if err := os.WriteFile(path, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := s.Command(context.Background(), owner, "plans.approve", M{"id": plan.ID})
	clarificationFault(t, err, 409)
	if tasks, _ := s.Store.List(project.ID, "task", g.ID); len(tasks) != 0 {
		t.Fatal("failed approval leaked tasks", tasks)
	}
	if err = os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	must(t, s, owner, "plans.approve", M{"id": plan.ID})
	approvals, _ := s.Store.List(project.ID, "approval", g.ID)
	matched := 0
	for _, approval := range approvals {
		if S(approval.Data, "planId") == plan.ID {
			matched++
			if Hash(approval.Data["design"]) != Hash(snapshot) || S(approval.Data, "designDigest") != Hash(snapshot) {
				t.Fatal("approval did not freeze exact design", approval)
			}
		}
	}
	if matched != 1 {
		t.Fatal("missing unique human approval", approvals)
	}
	inherited := generatedPlanContent()
	inherited["goalId"] = g.ID
	newPlan := must(t, s, owner, "plans.propose", inherited)
	if Hash(newPlan.Data["design"]) != Hash(snapshot) {
		t.Fatal("omitted selection silently dropped approved design", newPlan)
	}
}

func TestDesignRejectsInvalidScopeFieldsAndSelection(t *testing.T) {
	s, owner, project, g, prototype := designFixture(t)
	for _, change := range []M{
		{"expectedRequirementRevision": nil}, {"expectedRequirementRevision": "1"}, {"expectedRequirementRevision": 1.5},
		{"criteria": []any{}}, {"criteria": []any{"unknown"}}, {"criteria": []any{"ticket", "ticket"}}, {"criteria": []any{7}},
		{"prototypeArtifactIds": "not-an-array"}, {"prototypeArtifactIds": []any{prototype.ID, prototype.ID}},
		{"summary": ""}, {"decisions": ""}, {"reason": ""}, {"approvedBy": owner.ID},
	} {
		in := designInput(g, prototype, NewID("invalid"))
		for key, value := range change {
			in[key] = value
		}
		_, err := s.Command(context.Background(), owner, "designs.propose", in)
		clarificationFault(t, err, 422)
	}
	for _, actor := range []Actor{
		{ID: "viewer", Kind: "agent", ProjectID: project.ID, Roles: []string{"viewer"}},
		{ID: "foreign", Kind: "agent", ProjectID: "other", Roles: []string{"design"}},
	} {
		_, err := s.Command(context.Background(), actor, "designs.propose", designInput(g, prototype, actor.ID))
		clarificationFault(t, err, 403)
	}
	agent := Actor{ID: "design-agent", Kind: "agent", ProjectID: project.ID, Roles: []string{"design"}}
	design := must(t, s, agent, "designs.propose", designInput(g, prototype, "agent-design"))
	for _, change := range []M{{"expectedDesignBinding": "wrong"}, {"designId": prototype.ID}, {"expectedDesignBinding": nil}} {
		in := designPlan(g, design)
		for key, value := range change {
			in[key] = value
		}
		if _, err := s.Command(context.Background(), owner, "plans.propose", in); err == nil {
			t.Fatal("invalid design selection accepted", change)
		}
	}
	forged := generatedPlanContent()
	forged["goalId"], forged["design"] = g.ID, M{"decisions": "A caller-controlled approval"}
	_, err := s.Command(context.Background(), owner, "plans.propose", forged)
	clarificationFault(t, err, 422)
}

func TestDesignCannotDropNewlyApprovedDecisionThroughOlderProposal(t *testing.T) {
	s, owner, _, g, prototype := designFixture(t)
	oldInput := generatedPlanContent()
	oldInput["goalId"] = g.ID
	old := must(t, s, owner, "plans.propose", oldInput)
	design := must(t, s, owner, "designs.propose", designInput(g, prototype, "first"))
	current := must(t, s, owner, "plans.propose", designPlan(g, design))
	must(t, s, owner, "plans.approve", M{"id": current.ID})
	_, err := s.Command(context.Background(), owner, "plans.approve", M{"id": old.ID})
	clarificationFault(t, err, 409)
	after, _ := s.Store.Get(g.ID)
	if S(after.Data, "activePlanId") != current.ID {
		t.Fatal("old unbound plan discarded the reviewed design", after)
	}
}

func TestDesignRequirementChangeStartsNewBasis(t *testing.T) {
	s, owner, _, g, prototype := designFixture(t)
	design := must(t, s, owner, "designs.propose", designInput(g, prototype, "first"))
	plan := must(t, s, owner, "plans.propose", designPlan(g, design))
	must(t, s, owner, "plans.approve", M{"id": plan.ID})
	g = must(t, s, owner, "goals.revise", M{"id": g.ID, "reason": "Change business scope"})
	current, _ := s.Store.Get(design.ID)
	if current.Status != "stale" {
		t.Fatal("design survived a requirement revision", current)
	}
	must(t, s, owner, "goals.approve", M{"id": g.ID})
	in := generatedPlanContent()
	in["goalId"] = g.ID
	fresh := must(t, s, owner, "plans.propose", in)
	if fresh.Data["design"] != nil {
		t.Fatal("old design leaked into new requirement basis", fresh)
	}
	_, err := s.Command(context.Background(), owner, "designs.propose", designInput(g, prototype, "stale-prototype"))
	clarificationFault(t, err, 409)
}

func TestDesignWorkerContextUsesApprovalAndRejectsDamagedSnapshot(t *testing.T) {
	s, owner, project, g, prototype := designFixture(t)
	design := must(t, s, owner, "designs.propose", designInput(g, prototype, "selected"))
	plan := must(t, s, owner, "plans.propose", designPlan(g, design))
	must(t, s, owner, "plans.approve", M{"id": plan.ID})
	g, _ = s.Store.Get(g.ID)
	design.Data["decisions"] = "UNAPPROVED-LIVE-DESIGN"
	if err := s.Store.Update(context.Background(), func(tx *Tx) error { return tx.Put(design) }); err != nil {
		t.Fatal(err)
	}
	resources, _ := s.Store.List(project.ID, "", g.ID)
	snapshot, err := ApprovedDesign(g, plan.ID, resources)
	if err != nil || S(Obj(snapshot["data"]), "decisions") != "CONFIRMED-DESIGN: submit once, show status, no payment." {
		t.Fatal("live proposal replaced frozen approval", snapshot, err)
	}
	for _, mode := range []string{"missing", "digest", "content", "plan-binding", "missing-plan-design"} {
		t.Run(mode, func(t *testing.T) {
			copy := []Resource{}
			for _, r := range resources {
				r.Data = copyM(r.Data)
				if mode == "missing-plan-design" && r.ID == plan.ID {
					delete(r.Data, "design")
				}
				if r.Kind == "approval" && S(r.Data, "planId") == plan.ID {
					switch mode {
					case "missing":
						continue
					case "digest":
						r.Data["designDigest"] = "incorrect"
					case "content":
						Obj(Obj(r.Data["design"])["data"])["decisions"] = "forged"
					case "plan-binding":
						r.Data["binding"] = "different-plan"
					}
				}
				copy = append(copy, r)
			}
			_, err := ApprovedDesign(g, plan.ID, copy)
			clarificationFault(t, err, 409)
		})
	}
}

func TestDesignReplacementOnlyInvalidatesWorkAtHumanApproval(t *testing.T) {
	s, owner, project, g, prototype := designFixture(t)
	one := must(t, s, owner, "designs.propose", designInput(g, prototype, "first"))
	first := must(t, s, owner, "plans.propose", designPlan(g, one))
	must(t, s, owner, "plans.approve", M{"id": first.ID})
	tasks, _ := s.Store.List(project.ID, "task", g.ID)
	if len(tasks) != 1 {
		t.Fatal(tasks)
	}
	oldTask := tasks[0]
	policy := resource("quality_policy", "Historical protected policy", project.ID, g.ID, M{"planId": first.ID, "requirementRevision": 1})
	release := resource("release", "Historical candidate", project.ID, g.ID, M{"planId": first.ID, "requirementRevision": 1})
	if err := s.Store.Update(context.Background(), func(tx *Tx) error {
		if err := tx.Put(policy); err != nil {
			return err
		}
		return tx.Put(release)
	}); err != nil {
		t.Fatal(err)
	}
	in := designInput(g, prototype, "replacement")
	in["decisions"], in["reason"] = "Show request status first", "Reduce navigation complexity"
	two := must(t, s, owner, "designs.propose", in)
	second := must(t, s, owner, "plans.propose", designPlan(g, two))
	before, _ := s.Store.Get(oldTask.ID)
	if before.Status != "pending" {
		t.Fatal("proposal alone cancelled approved work", before)
	}
	must(t, s, owner, "plans.approve", M{"id": second.ID})
	for id, want := range map[string]string{oldTask.ID: "cancelled", policy.ID: "stale", release.ID: "stale"} {
		r, err := s.Store.Get(id)
		if err != nil || r.Status != want {
			t.Fatal(id, want, r, err)
		}
	}
}

func TestDesignPlanGenerationPinsSelectionAndRejectsSelectedDrift(t *testing.T) {
	for _, mode := range []string{"selected-changed", "unselected-new", "model-substitution"} {
		t.Run(mode, func(t *testing.T) {
			s, owner, _, g, prototype := designFixture(t)
			design := must(t, s, owner, "designs.propose", designInput(g, prototype, "first"))
			in := planGenerationInput(g, "planning")
			in["designId"], in["expectedDesignBinding"] = design.ID, design.Data["binding"]
			request, started, err := s.BeginPlanGeneration(context.Background(), owner, in)
			if err != nil || !started || S(Obj(Obj(request.Data["input"])["selectedDesign"]), "id") != design.ID {
				t.Fatal("selected design not pinned in durable inference input", request, started, err)
			}
			putPlanGenerationCharge(t, s, request)
			content := generatedPlanContent()
			switch mode {
			case "selected-changed":
				design.Data["decisions"] = "tampered while inference was running"
				if err := s.Store.Update(context.Background(), func(tx *Tx) error { return tx.Put(design) }); err != nil {
					t.Fatal(err)
				}
			case "unselected-new":
				newInput := designInput(g, prototype, "unselected")
				newInput["decisions"] = "UNSELECTED-DESIGN"
				must(t, s, owner, "designs.propose", newInput)
			case "model-substitution":
				content["designId"] = "invented-by-model"
			}
			plan, err := s.CompletePlanGeneration(context.Background(), owner, request.ID, content)
			if mode == "unselected-new" {
				if err != nil || S(Obj(plan.Data["design"]), "id") != design.ID || plan.Status != "proposed" {
					t.Fatal("unrelated proposal replaced the chosen design", plan, err)
				}
			} else if mode == "selected-changed" {
				clarificationFault(t, err, 409)
			} else {
				clarificationFault(t, err, 422)
			}
		})
	}
}

func TestDesignWorkerRequiresDesignTaskAndCurrentLease(t *testing.T) {
	for _, kind := range []string{"design", "analysis"} {
		t.Run(kind, func(t *testing.T) {
			s, owner, project, g, prototype := designFixture(t)
			planInput := generatedPlanContent()
			planInput["goalId"], planInput["tasks"] = g.ID, []any{M{"id": "task", "title": "Prepare a design", "kind": kind, "role": "design", "runtime": "linux"}}
			plan := must(t, s, owner, "plans.propose", planInput)
			must(t, s, owner, "plans.approve", M{"id": plan.ID})
			worker := must(t, s, owner, "workers.register", M{"projectId": project.ID, "title": "Design node", "roles": []any{"design"}, "runtime": "linux", "capabilities": []any{kind}})
			actor := Actor{ID: "worker:" + worker.ID, Kind: "worker", ProjectID: project.ID, WorkerID: worker.ID, Roles: []string{"design"}}
			run := must(t, s, actor, "tasks.claim", M{})
			in := designInput(g, prototype, "worker-result")
			in["runId"], in["leaseToken"] = run.ID, run.Data["leaseToken"]
			if kind == "analysis" {
				_, err := s.Command(context.Background(), actor, "designs.propose", in)
				clarificationFault(t, err, 403)
				return
			}
			wrong := copyM(in)
			wrong["leaseToken"] = "wrong"
			_, err := s.Command(context.Background(), actor, "designs.propose", wrong)
			clarificationFault(t, err, 409)
			foreign := goal(t, s, owner, project)
			wrong = copyM(in)
			wrong["goalId"] = foreign.ID
			_, err = s.Command(context.Background(), actor, "designs.propose", wrong)
			clarificationFault(t, err, 403)
			design := must(t, s, actor, "designs.propose", in)
			if S(design.Data, "runId") != run.ID {
				t.Fatal("lost lease provenance", design)
			}
			meta := M{"goalId": g.ID, "repositoryId": prototype.Data["repositoryId"], "commit": prototype.Data["commit"], "target": "web", "type": "package", "name": "forged-package", "runId": run.ID, "leaseToken": run.Data["leaseToken"]}
			_, err = s.Upload(context.Background(), actor, meta, bytes.NewBufferString("not authorized"))
			clarificationFault(t, err, 403)
			meta["type"] = "prototype"
			_, err = s.Upload(context.Background(), actor, meta, bytes.NewReader(bytes.Repeat([]byte{'x'}, (2<<20)+1)))
			clarificationFault(t, err, 422)
			must(t, s, actor, "tasks.complete", M{"id": run.ID, "leaseToken": run.Data["leaseToken"], "exitCode": 0, "result": M{"designId": design.ID}})
			_, err = s.Command(context.Background(), actor, "designs.propose", in)
			clarificationFault(t, err, 409)
		})
	}
}

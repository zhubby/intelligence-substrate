package platform

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/intelligence-substrate/platform/internal/blueprint"
	"github.com/intelligence-substrate/platform/internal/source"
)

func TestBlueprintProposalRequiresCurrentBaseline(t *testing.T) {
	s, a, project := fixture(t)
	g := goal(t, s, a, project)
	_, err := s.Command(context.Background(), a, "blueprints.propose", M{"goalId": g.ID, "expectedRequirementRevision": g.Revision, "key": "blueprint-proposal", "blueprintId": "flutter-go-v1", "applicationName": "support_portal", "organization": "com.example", "mode": "feedback", "reason": "Use the standard reviewed multi-platform starting point"})
	clarificationFault(t, err, 409)
}

func enableBlueprints(t *testing.T, s *Service) {
	t.Helper()
	var err error
	s.Blueprints, err = blueprint.Load("../..")
	if err != nil {
		t.Fatal(err)
	}
}

func blueprintInput(s *Service, g Resource, key string) M {
	return M{"goalId": g.ID, "expectedRequirementRevision": g.Revision, "key": key, "blueprintId": "flutter-go-v1", "expectedCatalogDigest": s.Blueprints.List()[0].Digest, "applicationName": "enterprise_app", "organization": "com.enterprise", "mode": "feedback", "reason": "Use the standard application starting source"}
}

func blueprintPlan(g, selection Resource) M {
	return M{"goalId": g.ID, "expectedRequirementRevision": g.Revision, "summary": "Initialize and implement the approved business scope", "blueprintId": selection.ID, "expectedBlueprintBinding": selection.Data["binding"], "initializeBlueprint": true,
		"tasks": []any{M{"id": "implement", "title": "Implement the approved scope", "kind": "code", "role": "developer", "runtime": "linux"}}}
}

func blueprintFixture(t *testing.T) (*Service, Actor, Resource, Resource, Resource) {
	t.Helper()
	s, a, project := fixture(t)
	enableBlueprints(t, s)
	g := goal(t, s, a, project)
	must(t, s, a, "goals.approve", M{"id": g.ID})
	selection := must(t, s, a, "blueprints.propose", blueprintInput(s, g, "selection"))
	return s, a, project, g, selection
}

func TestBlueprintOptionalKeepsLegacyPlanLifecycle(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		name := "disabled"
		if enabled {
			name = "enabled"
		}
		t.Run(name, func(t *testing.T) {
			s, a, project := fixture(t)
			if enabled {
				enableBlueprints(t, s)
			}
			g := goal(t, s, a, project)
			must(t, s, a, "goals.approve", M{"id": g.ID})
			for _, summary := range []string{"Existing plan without blueprint", "Continue existing project"} {
				plan := must(t, s, a, "plans.propose", M{"goalId": g.ID, "summary": summary,
					"tasks": []any{M{"id": "analysis", "title": "Analyze approved scope", "kind": "analysis", "role": "analysis", "runtime": "linux"}}})
				if _, exists := plan.Data["blueprint"]; exists || len(Items(plan.Data["tasks"])) != 1 {
					t.Fatal("optional catalog changed legacy plan", plan)
				}
				must(t, s, a, "plans.approve", M{"id": plan.ID})
				current, _ := s.Store.Get(g.ID)
				resources, _ := s.Store.List(project.ID, "", g.ID)
				if frozen, err := ApprovedBlueprint(current, plan.ID, resources); err != nil || frozen != nil {
					t.Fatal("legacy approval acquired blueprint authority", frozen, err)
				}
			}
		})
	}
}

func TestBlueprintProposalFreezesSourceAndReplaysWithoutCatalog(t *testing.T) {
	s, a, project, g, selection := blueprintFixture(t)
	if selection.Status != "proposed" || revision(selection) != g.Revision || s.VerifyArtifact(selection) != nil {
		t.Fatal(selection)
	}
	if len(selection.Data["digest"].(string)) != 64 || N(selection.Data, "fileCount") < 20 {
		t.Fatal("source was not frozen", selection)
	}
	for _, kind := range []string{"task", "repository", "artifact", "evidence"} {
		rows, err := s.Store.List(project.ID, kind, g.ID)
		if err != nil || len(rows) != 0 {
			t.Fatal("proposal created execution or quality authority", kind, rows, err)
		}
	}
	in := blueprintInput(s, g, "selection")
	s.Blueprints = nil
	replay := must(t, s, a, "blueprints.propose", in)
	if replay.ID != selection.ID {
		t.Fatal("catalog disable lost proposal idempotency")
	}
	in["mode"] = "booking"
	_, err := s.Command(context.Background(), a, "blueprints.propose", in)
	clarificationFault(t, err, 409)
	if list, err := s.BlueprintCatalog(context.Background(), a, project.ID); err != nil || len(list) != 0 {
		t.Fatal(list, err)
	}
}

func TestBlueprintProposalRejectsUntrustedInputsAndCrossProjectRepository(t *testing.T) {
	s, a, project, g, _ := blueprintFixture(t)
	for _, patch := range []M{{"expectedRequirementRevision": 0}, {"expectedRequirementRevision": "1"}, {"applicationName": "../escape"}, {"organization": "com.example;env"}, {"mode": "arbitrary"}, {"expectedCatalogDigest": "old"}, {"blueprintId": "https://example.test/template"}, {"path": "/etc"}, {"command": []any{"env"}}, {"binding": "forged"}} {
		in := blueprintInput(s, g, NewID("invalid"))
		for k, v := range patch {
			in[k] = v
		}
		if _, err := s.Command(context.Background(), a, "blueprints.propose", in); err == nil {
			t.Fatal("invalid blueprint input accepted", patch)
		}
	}
	foreign := must(t, s, a, "projects.create", M{"title": "Foreign project"})
	repo := must(t, s, a, "repositories.create", M{"projectId": foreign.ID, "title": "private"})
	in := blueprintInput(s, g, "foreign-repo")
	in["repositoryId"] = repo.ID
	_, err := s.Command(context.Background(), a, "blueprints.propose", in)
	clarificationFault(t, err, 403)
	for _, actor := range []Actor{{ID: "foreign-agent", Kind: "agent", ProjectID: foreign.ID, Roles: []string{"developer"}}, {ID: "viewer", Kind: "human", ProjectID: project.ID, Roles: []string{"viewer"}}, {ID: "worker:x", Kind: "worker", ProjectID: project.ID, Roles: []string{"developer"}}} {
		_, err = s.Command(context.Background(), actor, "blueprints.propose", blueprintInput(s, g, "actor"))
		clarificationFault(t, err, 403)
	}
}

func TestBlueprintPlanApprovalAndContinuationFreezeExactSelection(t *testing.T) {
	s, a, project, g, selection := blueprintFixture(t)
	plan := must(t, s, a, "plans.propose", blueprintPlan(g, selection))
	if plan.Status != "proposed" || len(Items(plan.Data["tasks"])) != 2 {
		t.Fatal(plan)
	}
	if rows, _ := s.Store.List(project.ID, "task", g.ID); len(rows) != 0 {
		t.Fatal("proposal started work")
	}
	items := Items(plan.Data["tasks"])
	if Hash(Obj(items[0])["blueprintSeed"]) != Hash(plan.Data["blueprint"]) || !Has(Strings(Obj(items[1])["dependsOn"]), blueprintInitializer) {
		t.Fatal("initializer is not wired into the approved graph", items)
	}
	agent := Actor{ID: "architect-agent", Kind: "agent", ProjectID: project.ID, Roles: []string{"architect"}}
	_, err := s.Command(context.Background(), agent, "plans.approve", M{"id": plan.ID})
	clarificationFault(t, err, 403)
	must(t, s, a, "plans.approve", M{"id": plan.ID})
	current, _ := s.Store.Get(g.ID)
	resources, _ := s.Store.List(project.ID, "", g.ID)
	frozen, err := ApprovedBlueprint(current, plan.ID, resources)
	if err != nil || S(frozen, "id") != selection.ID {
		t.Fatal("approved snapshot is unusable", frozen, err)
	}
	continuation := must(t, s, a, "plans.propose", M{"goalId": g.ID, "summary": "Continue without repeating initialization", "tasks": []any{M{"id": "analysis", "title": "Analyze next work", "kind": "analysis", "role": "analysis", "runtime": "linux"}}})
	if Hash(continuation.Data["blueprint"]) != Hash(frozen) || B(continuation.Data, "initializeBlueprint") || len(Items(continuation.Data["tasks"])) != 1 {
		t.Fatal("continuation reinitialized or lost blueprint", continuation)
	}
	must(t, s, a, "goals.revise", M{"id": g.ID, "reason": "New business scope"})
	_, err = s.Command(context.Background(), a, "plans.approve", M{"id": continuation.ID})
	clarificationFault(t, err, 409)
	g, _ = s.Store.Get(g.ID)
	must(t, s, a, "goals.approve", M{"id": g.ID})
	_, err = s.Command(context.Background(), a, "plans.propose", blueprintPlan(g, selection))
	clarificationFault(t, err, 409)
}

func TestBlueprintGenerationCapturesAndRejectsChangedSelection(t *testing.T) {
	for _, change := range []string{"complete", "snapshot", "requirement", "approval"} {
		t.Run(change, func(t *testing.T) {
			s, a, project, g, selection := blueprintFixture(t)
			in := planGenerationInput(g, "model-blueprint")
			for _, key := range []string{"blueprintId", "expectedBlueprintBinding", "initializeBlueprint"} {
				in[key] = blueprintPlan(g, selection)[key]
			}
			request, started, err := s.BeginPlanGeneration(context.Background(), a, in)
			if err != nil || !started || S(Obj(Obj(request.Data["input"])["selectedBlueprint"]), "id") != selection.ID {
				t.Fatal(request, err)
			}
			putPlanGenerationCharge(t, s, request)
			switch change {
			case "snapshot":
				selection.Data["mode"] = "booking"
				if err = s.Store.Update(context.Background(), func(tx *Tx) error { return tx.Put(selection) }); err != nil {
					t.Fatal(err)
				}
			case "requirement":
				must(t, s, a, "goals.revise", M{"id": g.ID, "reason": "Scope changed"})
			case "approval":
				must(t, s, a, "goals.approve", M{"id": g.ID})
			}
			plan, err := s.CompletePlanGeneration(context.Background(), a, request.ID, generatedPlanContent())
			if change != "complete" {
				clarificationFault(t, err, 409)
				return
			}
			if err != nil || S(Obj(plan.Data["blueprint"]), "id") != selection.ID || len(Items(plan.Data["tasks"])) != 2 {
				t.Fatal(plan, err)
			}
			if rows, _ := s.Store.List(project.ID, "task", g.ID); len(rows) != 0 {
				t.Fatal("model activated its own blueprint")
			}
		})
	}
}

func TestBlueprintInitializerCannotBeForgedSplitOrReorderedAway(t *testing.T) {
	s, a, project, g, selection := blueprintFixture(t)
	for _, patch := range []M{{"blueprint": blueprintSnapshot(selection)}, {"initializeBlueprint": "true"}, {"expectedBlueprintBinding": "changed"}} {
		in := blueprintPlan(g, selection)
		for k, v := range patch {
			in[k] = v
		}
		if _, err := s.Command(context.Background(), a, "plans.propose", in); err == nil {
			t.Fatal("untrusted selection accepted", patch)
		}
	}
	in := blueprintPlan(g, selection)
	Obj(Items(in["tasks"])[0])["blueprintSeed"] = blueprintSnapshot(selection)
	_, err := s.Command(context.Background(), a, "plans.propose", in)
	clarificationFault(t, err, 422)
	plan := must(t, s, a, "plans.propose", blueprintPlan(g, selection))
	must(t, s, a, "plans.approve", M{"id": plan.ID})
	tasks, _ := s.Store.List(project.ID, "task", g.ID)
	for _, task := range tasks {
		if S(task.Data, "logicalId") == blueprintInitializer {
			_, err = s.Command(context.Background(), a, "tasks.split", M{"id": task.ID, "reason": "Parallelize", "impact": "Preserve scope", "children": []any{M{"title": "One", "instructions": "One"}, M{"title": "Two", "instructions": "Two"}}})
			clarificationFault(t, err, 409)
		}
	}
	for _, updates := range [][]any{{M{"id": "implement", "dependsOn": []any{}}}, {M{"id": blueprintInitializer, "dependsOn": []any{"implement"}}}} {
		_, err = s.Command(context.Background(), a, "plans.reorder", M{"goalId": g.ID, "reason": "Adjust work", "impact": "Preserve scope", "tasks": updates})
		if err == nil {
			t.Fatal("removed protected initializer dependency")
		}
	}
}

func TestBlueprintReservedTaskIDRejectedAfterSourceTask(t *testing.T) {
	s, a, _, g, selection := blueprintFixture(t)
	in := blueprintPlan(g, selection)
	in["tasks"] = append(Items(in["tasks"]), M{"id": blueprintInitializer, "title": "Duplicate initializer identity", "kind": "analysis", "role": "analysis", "runtime": "linux"})
	_, err := s.Command(context.Background(), a, "plans.propose", in)
	clarificationFault(t, err, 422)
}

func claimBlueprint(t *testing.T, s *Service, a Actor, project, g Resource) (Actor, Resource) {
	t.Helper()
	w := must(t, s, a, "workers.register", M{"projectId": project.ID, "title": "Blueprint worker", "runtime": "linux", "roles": []any{"developer"}, "capabilities": []any{"code"}})
	actor := Actor{ID: "worker:" + w.ID, Kind: "worker", WorkerID: w.ID, ProjectID: project.ID, Roles: []string{"developer"}}
	return actor, must(t, s, actor, "tasks.claim", M{})
}

func TestBlueprintInitializerBlocksNonemptySourceBeforeAttempt(t *testing.T) {
	s, a, project, g, selection := blueprintFixture(t)
	repo := must(t, s, a, "repositories.create", M{"projectId": project.ID, "title": "application"})
	plan := must(t, s, a, "plans.propose", blueprintPlan(g, selection))
	must(t, s, a, "plans.approve", M{"id": plan.ID})
	must(t, s, a, "repositories.commit", M{"id": repo.ID, "branch": "agent/" + g.ID, "message": "Existing user code", "files": M{"user.txt": "preserve me"}})
	_, run := claimBlueprint(t, s, a, project, g)
	if run.Kind == "run" {
		t.Fatal("initializer leased existing source", run)
	}
	tasks, _ := s.Store.List(project.ID, "task", g.ID)
	blocked := false
	for _, task := range tasks {
		if S(task.Data, "logicalId") == blueprintInitializer {
			blocked = task.Status == "blocked" && N(task.Data, "attempts") == 0 && task.Data["sourceConflict"] != nil
		}
	}
	if !blocked {
		t.Fatal("nonempty source was not a durable pre-attempt block", tasks)
	}
	content, err := s.Git(repo, "show", "refs/heads/agent/"+g.ID+":user.txt")
	if err != nil || content != "preserve me" {
		t.Fatal(content, err)
	}
}

func TestBlueprintSeedArchiveEnforcedByControlPlaneAndLease(t *testing.T) {
	s, a, project, g, selection := blueprintFixture(t)
	plan := must(t, s, a, "plans.propose", blueprintPlan(g, selection))
	must(t, s, a, "plans.approve", M{"id": plan.ID})
	worker, run := claimBlueprint(t, s, a, project, g)
	if run.Kind != "run" {
		t.Fatal(run)
	}
	for _, actor := range []Actor{a, {ID: "worker:foreign", Kind: "worker", WorkerID: "foreign", ProjectID: project.ID, Roles: []string{"developer"}}} {
		if file, err := s.OpenBlueprintSeed(context.Background(), actor, run.ID, M{"leaseToken": S(run.Data, "leaseToken")}); err == nil {
			file.Close()
			t.Fatal("foreign actor read private seed")
		}
	}
	file, err := s.OpenBlueprintSeed(context.Background(), worker, run.ID, M{"leaseToken": S(run.Data, "leaseToken")})
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(file)
	file.Close()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	entries, _, err := source.Extract(bytes.NewReader(body), root)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "application.json")
	original, _ := os.ReadFile(path)
	changed := bytes.ReplaceAll(original, []byte("enterprise_app"), []byte("unapproved_app"))
	if err = os.WriteFile(path, changed, 0600); err != nil {
		t.Fatal(err)
	}
	for i := range entries {
		if entries[i].Name == "application.json" {
			entries[i].Size = int64(len(changed))
		}
	}
	var archive bytes.Buffer
	if err = source.Write(&archive, root, entries); err != nil {
		t.Fatal(err)
	}
	binding := Obj(Obj(run.Data["task"])["sourceBinding"])
	meta := M{"key": run.ID, "runId": run.ID, "leaseToken": S(run.Data, "leaseToken"), "branch": S(binding, "targetBranch"), "expectedHead": S(binding, "expectedHead"), "baseCommit": S(binding, "commit"), "message": "Initialize from blueprint"}
	_, err = s.CommitSnapshot(context.Background(), worker, S(binding, "repositoryId"), meta, &archive)
	clarificationFault(t, err, 409)
	_, err = s.Command(context.Background(), worker, "repositories.commit", M{"id": S(binding, "repositoryId"), "runId": run.ID, "leaseToken": S(run.Data, "leaseToken"), "branch": S(binding, "targetBranch"), "message": "Bypass seed", "files": M{"unapproved.txt": "text"}})
	clarificationFault(t, err, 409)
	op, err := s.CommitSnapshot(context.Background(), worker, S(binding, "repositoryId"), meta, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	complete := M{"id": run.ID, "leaseToken": S(run.Data, "leaseToken"), "exitCode": 0, "result": M{"repositoryId": S(binding, "repositoryId"), "commit": op.Data["commit"], "sourceOperationId": op.ID}}
	must(t, s, worker, "tasks.complete", complete)
	if file, err = s.OpenBlueprintSeed(context.Background(), worker, run.ID, M{"leaseToken": S(run.Data, "leaseToken")}); err == nil {
		file.Close()
		t.Fatal("completed lease read seed")
	}
}

func TestBlueprintArchiveRestoreSurvivesDisabledCatalogAndRejectsCorruption(t *testing.T) {
	s, a, project, g, selection := blueprintFixture(t)
	in := blueprintInput(s, g, "selection")
	plan := must(t, s, a, "plans.propose", blueprintPlan(g, selection))
	must(t, s, a, "plans.approve", M{"id": plan.ID})
	archive := filepath.Join(t.TempDir(), "blueprint.tar.gz")
	if err := s.Backup(context.Background(), archive); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "restored")
	if err := Restore(context.Background(), archive, destination, ""); err != nil {
		t.Fatal(err)
	}
	store, err := Open(filepath.Join(destination, "platform.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	restored := &Service{Store: store, Root: destination}
	rows, _ := s.Store.List(project.ID, "", g.ID)
	for _, before := range rows {
		after, err := store.Get(before.ID)
		if err != nil || Hash(before) != Hash(after) {
			t.Fatal("restore changed immutable blueprint state", before.ID, err)
		}
	}
	if replay := must(t, restored, a, "blueprints.propose", in); replay.ID != selection.ID {
		t.Fatal("restored proposal duplicated")
	}
	_, run := claimBlueprint(t, restored, a, project, g)
	if run.Kind != "run" {
		t.Fatal("frozen seed could not run without catalog", run)
	}
	if err = restored.VerifyIntegrity(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(restored.ArtifactPath(S(selection.Data, "digest")), []byte("corrupted"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = restored.VerifyIntegrity(context.Background()); err == nil {
		t.Fatal("corrupt seed passed integrity check")
	}
}

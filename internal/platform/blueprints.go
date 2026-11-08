package platform

import (
	"bytes"
	"context"
	"io"
	"os"

	"github.com/intelligence-substrate/platform/internal/blueprint"
	"github.com/intelligence-substrate/platform/internal/source"
)

const blueprintInitializer = "blueprint-initialize"

func (s *Service) BlueprintCatalog(ctx context.Context, a Actor, project string) ([]blueprint.Descriptor, error) {
	if err := s.CanAccess(ctx, a, project); err != nil {
		return nil, err
	}
	return s.Blueprints.List(), nil
}

func (s *Service) blueprintCommand(tx *Tx, a Actor, in M) (Resource, error) {
	g, err := s.scoped(tx, a, S(in, "goalId"), "goal", "product", "architect", "developer")
	if err != nil {
		return Resource{}, err
	}
	if !Has([]string{"human", "agent"}, a.Kind) {
		return Resource{}, denied()
	}
	if err = approved(tx, g); err != nil {
		return Resource{}, err
	}
	if err = planGenerationFields(in, "goalId", "expectedRequirementRevision", "key", "blueprintId", "expectedCatalogDigest", "applicationName", "organization", "mode", "repositoryId", "reason"); err != nil {
		return Resource{}, err
	}
	if err = noSecrets(in); err != nil {
		return Resource{}, err
	}
	for _, field := range []struct {
		key string
		max int
	}{{"key", 160}, {"reason", 2000}} {
		if err = validateText(S(in, field.key), field.key, field.max); err != nil {
			return Resource{}, err
		}
	}
	scope := "blueprints-propose:" + g.ID + ":" + a.ID
	if previous, exists, err := tx.Once(scope, S(in, "key"), in); err != nil || exists {
		return previous, err
	}
	if err = clarificationRevision(g, in["expectedRequirementRevision"]); err != nil {
		return Resource{}, err
	}
	if s.Blueprints == nil {
		return Resource{}, conflict("管理员尚未启用应用蓝图目录")
	}
	options := blueprint.Options{Name: S(in, "applicationName"), Organization: S(in, "organization"), Mode: S(in, "mode")}
	if err = blueprint.ValidateOptions(options); err != nil {
		return Resource{}, invalid(err.Error())
	}
	archive, fileCount, err := s.Blueprints.Generate(S(in, "blueprintId"), S(in, "expectedCatalogDigest"), options)
	if err != nil {
		return Resource{}, conflict(err.Error())
	}
	repoID := S(in, "repositoryId")
	if repoID == "" {
		repos, err := tx.List(g.ProjectID, "repository", "")
		if err != nil {
			return Resource{}, err
		}
		if len(repos) > 1 {
			return Resource{}, invalid("项目包含多个仓库，请明确选择空应用仓库")
		}
		if len(repos) == 1 {
			repoID = repos[0].ID
		}
	}
	if repoID != "" {
		repo, err := s.scoped(tx, a, repoID, "repository")
		if err != nil || repo.ProjectID != g.ProjectID {
			return Resource{}, denied()
		}
		head, err := s.Git(repo, "rev-parse", "refs/heads/main")
		if err != nil {
			return Resource{}, err
		}
		if err = s.emptyBlueprintSource(repo, head); err != nil {
			return Resource{}, err
		}
	}
	previous, err := tx.List(g.ProjectID, "blueprint", g.ID)
	if err != nil {
		return Resource{}, err
	}
	if len(previous) >= 100 {
		return Resource{}, conflict("当前目标已达到 100 个蓝图提案限制")
	}
	digest, size, err := s.uploadContent(bytes.NewReader(archive), source.MaxBytes)
	if err != nil {
		return Resource{}, err
	}
	r := resource("blueprint", "Flutter + Go · "+options.Name, g.ProjectID, g.ID, M{"requirementRevision": g.Revision,
		"blueprintId": S(in, "blueprintId"), "blueprintVersion": 1, "catalogDigest": S(in, "expectedCatalogDigest"), "applicationName": options.Name,
		"organization": options.Organization, "mode": options.Mode, "repositoryId": repoID, "digest": digest, "size": size, "fileCount": fileCount,
		"authorId": a.ID, "reason": S(in, "reason"), "limitations": s.Blueprints.List()[0].Limitations})
	r.Status, r.Revision = "proposed", len(previous)+1
	r.Data["binding"] = Hash(r.Data)
	if err = tx.Put(r); err != nil {
		return Resource{}, err
	}
	return r, tx.Remember(scope, S(in, "key"), in, r)
}

func blueprintSnapshot(r Resource) M {
	return copyM(M{"id": r.ID, "kind": r.Kind, "projectId": r.ProjectID, "goalId": r.GoalID, "title": r.Title, "revision": r.Revision, "data": r.Data})
}

func validBlueprintSnapshot(g Resource, snapshot M) error {
	d := Obj(snapshot["data"])
	bound := copyM(d)
	delete(bound, "binding")
	if S(snapshot, "kind") != "blueprint" || S(snapshot, "id") == "" || S(snapshot, "projectId") != g.ProjectID || S(snapshot, "goalId") != g.ID ||
		N(d, "requirementRevision") != float64(g.Revision) || Hash(bound) != S(d, "binding") || S(d, "blueprintId") != "flutter-go-v1" ||
		!digestPattern.MatchString(S(d, "digest")) || !digestPattern.MatchString(S(d, "catalogDigest")) || N(d, "size") < 1024 || N(d, "size") > float64(source.MaxBytes) || N(d, "fileCount") < 1 || N(d, "fileCount") > 1000 {
		return conflict("蓝图快照与当前需求或归档摘要不一致")
	}
	if err := blueprint.ValidateOptions(blueprint.Options{Name: S(d, "applicationName"), Organization: S(d, "organization"), Mode: S(d, "mode")}); err != nil {
		return conflict("蓝图应用标识或业务模式无效")
	}
	return nil
}

// ApprovedBlueprint uses the human approval's frozen selection. The current
// installed catalog is deliberately irrelevant to previously approved work.
func ApprovedBlueprint(g Resource, planID string, resources []Resource) (M, error) {
	if planID == "" {
		return nil, nil
	}
	var plan Resource
	for _, r := range resources {
		if r.ID == planID && r.Kind == "plan" && r.ProjectID == g.ProjectID && r.GoalID == g.ID {
			plan = r
		}
	}
	if plan.ID == "" || plan.Status != "active" || revision(plan) != g.Revision || S(g.Data, "activePlanId") != plan.ID {
		return nil, conflict("蓝图上下文需要当前批准方案")
	}
	snapshot := Obj(plan.Data["blueprint"])
	matches := 0
	for _, r := range resources {
		if r.Kind != "approval" || r.ProjectID != g.ProjectID || r.GoalID != g.ID || S(r.Data, "type") != "plan" || S(r.Data, "planId") != planID || revision(r) != g.Revision || r.Status == "stale" {
			continue
		}
		if len(snapshot) == 0 {
			if r.Data["blueprint"] != nil || S(r.Data, "blueprintDigest") != "" {
				return nil, conflict("方案缺少已批准蓝图快照")
			}
			continue
		}
		bound := copyM(plan.Data)
		delete(bound, "binding")
		delete(bound, "approvedBy")
		if Hash(bound) != S(plan.Data, "binding") || S(r.Data, "binding") != S(plan.Data, "binding") || S(r.Data, "blueprintDigest") != Hash(snapshot) || Hash(r.Data["blueprint"]) != Hash(snapshot) || S(r.Data, "actorId") == "" {
			return nil, conflict("蓝图与人工方案批准记录不一致")
		}
		matches++
	}
	if len(snapshot) == 0 {
		return nil, nil
	}
	if matches != 1 {
		return nil, conflict("缺少唯一有效的蓝图方案批准记录")
	}
	if err := validBlueprintSnapshot(g, snapshot); err != nil {
		return nil, err
	}
	return copyM(snapshot), nil
}

func planBlueprint(tx *Tx, g Resource, in M) (M, error) {
	if value, exists := in["initializeBlueprint"]; exists {
		if _, ok := value.(bool); !ok {
			return nil, invalid("initializeBlueprint 必须为布尔值")
		}
	}
	if id := S(in, "blueprintId"); id != "" {
		r, err := tx.Get(id)
		if err != nil {
			return nil, err
		}
		if r.Kind != "blueprint" || r.ProjectID != g.ProjectID || r.GoalID != g.ID {
			return nil, denied()
		}
		if S(in, "expectedBlueprintBinding") == "" {
			return nil, invalid("选择蓝图需要 expectedBlueprintBinding")
		}
		if r.Status != "proposed" || S(in, "expectedBlueprintBinding") != S(r.Data, "binding") {
			return nil, conflict("蓝图提案已变化，请重新审阅")
		}
		snapshot := blueprintSnapshot(r)
		return snapshot, validBlueprintSnapshot(g, snapshot)
	}
	if S(in, "expectedBlueprintBinding") != "" || B(in, "initializeBlueprint") {
		return nil, invalid("蓝图摘要或初始化需要明确的 blueprintId")
	}
	resources, err := tx.List(g.ProjectID, "", g.ID)
	if err != nil {
		return nil, err
	}
	for _, r := range resources {
		if r.Kind == "plan" && r.ID == S(g.Data, "activePlanId") && revision(r) != g.Revision {
			return nil, nil
		}
	}
	return ApprovedBlueprint(g, S(g.Data, "activePlanId"), resources)
}

func (s *Service) validatePlanBlueprint(tx *Tx, g Resource, value any) error {
	if value == nil {
		return nil
	}
	snapshot := Obj(value)
	if err := validBlueprintSnapshot(g, snapshot); err != nil {
		return err
	}
	r, err := tx.Get(S(snapshot, "id"))
	if err != nil {
		return err
	}
	if r.Status != "proposed" || Hash(blueprintSnapshot(r)) != Hash(snapshot) {
		return conflict("蓝图提案内容已变化，请重新审阅")
	}
	return s.VerifyArtifact(r)
}

func (s *Service) emptyBlueprintSource(repo Resource, commit string) error {
	tree, err := s.Git(repo, "ls-tree", "-r", "--name-only", commit)
	if err != nil {
		return err
	}
	if tree != "" {
		return conflict("蓝图初始化只允许空源码，已有代码需要独立审阅和迁移方案")
	}
	return nil
}

func (s *Service) OpenBlueprintSeed(ctx context.Context, actor Actor, runID string, in M) (io.ReadCloser, error) {
	var snapshot M
	err := s.Store.Update(ctx, func(tx *Tx) error {
		a, err := tx.CurrentActor(actor)
		if err != nil {
			return err
		}
		if a.Kind != "worker" {
			return denied()
		}
		if err = planGenerationFields(in, "leaseToken"); err != nil {
			return err
		}
		run, err := tx.Get(runID)
		if err != nil {
			return err
		}
		if err = s.validWorkerRun(tx, a, "repositories.commit", M{"runId": run.ID, "leaseToken": S(in, "leaseToken")}); err != nil {
			return err
		}
		task, err := tx.Get(S(run.Data, "taskId"))
		if err != nil {
			return err
		}
		if Hash(Obj(run.Data["task"])["blueprintSeed"]) != Hash(task.Data["blueprintSeed"]) {
			return conflict("执行的蓝图快照已变化")
		}
		snapshot, err = s.validateBlueprintSeed(tx, task)
		return err
	})
	if err != nil {
		return nil, err
	}
	return os.Open(s.ArtifactPath(S(Obj(snapshot["data"]), "digest")))
}

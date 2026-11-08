package platform

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/intelligence-substrate/platform/internal/blueprint"
)

type Service struct {
	Store                   *Store
	Root                    string
	PreviewRuntimeAvailable bool               // Startup configuration; never set by Agent input.
	Blueprints              *blueprint.Catalog // Administrator-installed immutable assets.
}

func NewService(s *Store) *Service {
	root := os.Getenv("IS_DATA_DIR")
	if root == "" {
		if s.Postgres {
			root = ".data"
		} else {
			root = filepath.Dir(s.DSN)
		}
	}
	return &Service{Store: s, Root: root}
}
func (s *Service) Command(ctx context.Context, a Actor, command string, in M) (Resource, error) {
	var result Resource
	if a.ID == "" {
		return result, denied()
	}
	if in == nil {
		in = M{}
	}
	err := s.Store.Update(ctx, func(t *Tx) error {
		var e error
		current, e := t.CurrentActor(a)
		if e != nil {
			return e
		}
		a = current
		if a.Kind == "worker" && Has([]string{"repositories.create", "repositories.commit", "knowledge.create", "knowledge.revise", "plans.propose", "designs.propose", "artifacts.register", "reviews.create", "tasks.split", "plans.reorder"}, command) {
			if e = s.validWorkerRun(t, a, command, in); e != nil {
				return e
			}
			if command == "repositories.commit" {
				if e = s.workerSourceBinding(t, in); e != nil {
					return e
				}
			}
		}
		switch command {
		case "agent_steps.prepare", "agent_steps.complete", "agent_steps.reconcile":
			result, e = s.agentStepCommand(t, a, command, in)
		case "sandboxes.prepare", "sandboxes.finish", "sandboxes.reconcile":
			result, e = s.sandboxCommand(t, a, command, in)
		case "store_revisions.propose", "store_revisions.approve", "store_revisions.dispatch":
			result, e = s.storeRevisionCommand(t, a, command, in)
		case "release_issues.triage":
			result, e = s.triageReleaseIssue(t, a, in)
		case "blueprints.propose":
			result, e = s.blueprintCommand(t, a, in)
		case "previews.create", "previews.stop":
			result, e = s.previewCommand(t, a, command, in)
		case "teams.create", "teams.update", "teams.disable", "teams.enable", "teams.grant", "teams.revoke":
			result, e = s.teamCommand(t, a, command, in)
		case "reports.attach":
			result, e = s.attachReleaseReport(t, a, in)
		case "measurements.record", "measurements.void":
			result, e = s.measurementCommand(t, a, command, in)
		case "tasks.wait":
			result, e = s.messageWaitCommand(t, a, in)
		case "designs.propose":
			result, e = s.designCommand(t, a, in)
		case "quality.bind", "quality.revoke":
			result, e = s.qualityCommand(t, a, command, in)
		case "requirements.propose", "requirements.apply":
			result, e = s.requirementDraftCommand(t, a, command, in)
		case "clarifications.propose", "clarifications.answer":
			result, e = s.clarificationCommand(t, a, command, in)
		case "expenses.record", "expenses.void":
			result, e = s.expenseCommand(t, a, command, in)
		case "sources.reconcile":
			result, e = s.reconcileSource(ctx, t, a, in)
		case "sources.prepare", "sources.resolve":
			result, e = s.integrationCommand(ctx, t, a, command, in)
		case "runtime.incident.acknowledge":
			result, e = s.acknowledgeRuntimeIncident(t, a, in)
		case "runtime.report", "runtime.rollback.propose", "runtime.rollback.approve", "runtime.rollback.reconcile", "runtime.verify":
			result, e = s.runtimeCommand(t, a, command, in)
		case "tasks.split", "plans.reorder":
			result, e = s.adaptiveCommand(t, a, command, in)
		case "projects.create":
			result, e = s.createProject(t, a, in)
		case "members.grant":
			result, e = s.grantMember(t, a, in)
		case "members.revoke":
			result, e = s.revokeMember(t, a, in)
		case "goals.create", "goals.revise", "goals.approve":
			result, e = s.goalCommand(t, a, command, in)
		case "plans.propose", "plans.approve":
			result, e = s.planCommand(t, a, command, in)
		case "messages.send", "messages.claim", "messages.ack", "messages.receive":
			result, e = s.messageCommand(t, a, command, in)
		case "workers.register", "workers.update", "workers.disable", "tasks.claim", "tasks.progress", "tasks.complete", "tasks.cancel", "tasks.retry":
			result, e = s.taskCommand(t, a, command, in)
		case "artifacts.register", "evidence.record", "releases.create", "releases.approve", "releases.transition", "releases.verify", "releases.reconcile", "releases.dispatch", "releases.refresh":
			result, e = s.releaseCommand(t, a, command, in)
		case "repositories.create", "repositories.commit", "reviews.create", "reviews.approve", "reviews.merge":
			result, e = s.gitCommand(t, a, command, in)
		case "pipelines.create", "pipelines.run", "knowledge.create", "knowledge.revise", "charges.reconcile", "projects.policy":
			result, e = s.engineeringCommand(t, a, command, in)
		default:
			return fail("unknown_command", "未知操作", 404)
		}
		if e != nil {
			return e
		}
		if Has([]string{"tasks.cancel", "goals.revise", "plans.approve"}, command) {
			if e = s.refreshMessageWaits(t, result.ProjectID); e != nil {
				return e
			}
		}
		if (command == "tasks.claim" && result.Kind != "run") || (command == "messages.claim" && result.Kind != "message") {
			return nil
		}
		if command == "messages.receive" && N(result.Data, "receivedCount") == 0 && N(result.Data, "blockedCount") == 0 {
			return nil
		}
		return t.Audit(a, command, result, M{"status": result.Status, "revision": result.Revision})
	})
	if err != nil {
		var f *Fault
		if errors.As(err, &f) {
			_ = s.Store.Update(context.Background(), func(t *Tx) error {
				project := a.ProjectID
				if project == "" {
					project = S(in, "projectId")
					for _, key := range []string{"id", "goalId", "pipelineId", "artifactId", "repositoryId"} {
						if r, e := t.Get(S(in, key)); e == nil {
							project = r.ProjectID
							break
						}
					}
				}
				return t.Audit(a, "denied:"+command, Resource{ID: S(in, "id"), ProjectID: project}, M{"code": f.Code})
			})
		}
		return Resource{}, err
	}
	return result, nil
}
func (s *Service) validWorkerRun(t *Tx, a Actor, command string, in M) error {
	if e := s.validWorkerRunAuthority(t, a, command, in); e != nil {
		return e
	}
	run, e := t.Get(S(in, "runId"))
	if e != nil {
		return e
	}
	if e = s.AgentProgressRunGate(t, run.ProjectID, run.GoalID, run.ID); e != nil {
		return e
	}
	return s.SandboxRunGate(t, run.ProjectID, run.GoalID, run.ID)
}

func (s *Service) validWorkerRunAuthority(t *Tx, a Actor, command string, in M) error {
	run, e := t.Get(S(in, "runId"))
	if e != nil || a.ID != "worker:"+a.WorkerID || run.Kind != "run" || run.Status != "running" || run.ProjectID != a.ProjectID || S(run.Data, "workerId") != a.WorkerID || S(run.Data, "leaseToken") != S(in, "leaseToken") || S(run.Data, "leaseUntil") < now() {
		return conflict("此操作需要当前有效执行租约")
	}
	worker, e := t.Get(a.WorkerID)
	if e != nil || worker.Status == "disabled" {
		return denied()
	}
	for key, expected := range map[string]string{"projectId": run.ProjectID, "goalId": run.GoalID} {
		if actual := S(in, key); actual != "" && actual != expected {
			return denied()
		}
	}
	task, e := t.Get(S(run.Data, "taskId"))
	if e != nil {
		return e
	}
	if e = s.validateSigningRepair(t, task); e != nil {
		return e
	}
	if !Has(Strings(worker.Data["roles"]), S(task.Data, "role")) {
		return denied()
	}
	if command == "artifacts.register" && S(in, "type") == "prototype" && S(task.Data, "kind") != "design" {
		return denied()
	}
	repositoryID := S(in, "repositoryId")
	if command == "repositories.commit" {
		repositoryID = S(in, "id")
	}
	if expected := S(task.Data, "repositoryId"); expected != "" && repositoryID != "" && expected != repositoryID {
		return denied()
	}
	if command == "artifacts.register" && S(task.Data, "commit") != "" && S(task.Data, "commit") != S(in, "commit") {
		return conflict("制品提交必须与当前执行绑定一致")
	}
	if command == "tasks.split" {
		parent, err := t.Get(S(in, "id"))
		if err != nil || parent.GoalID != run.GoalID {
			return denied()
		}
	}
	g, e := t.Get(run.GoalID)
	if e != nil {
		return e
	}
	if e = approved(t, g); e != nil {
		return e
	}
	if e = checkRevision(g, run); e != nil {
		return e
	}
	return nil
}
func (s *Service) allowed(t *Tx, a Actor, project string, roles ...string) error {
	return t.RequireProjectAccess(a, project, roles...)
}
func (s *Service) scoped(t *Tx, a Actor, id, kind string, roles ...string) (Resource, error) {
	r, e := t.Get(id)
	if e != nil {
		return r, e
	}
	if r.Kind != kind {
		return Resource{}, missing()
	}
	if e = s.allowed(t, a, r.ProjectID, roles...); e != nil {
		return Resource{}, e
	}
	return r, nil
}
func human(a Actor) error {
	if a.Kind != "human" {
		return fail("human_required", "此决策需要有权限的人审批", 403)
	}
	return nil
}
func (s *Service) createProject(t *Tx, a Actor, in M) (Resource, error) {
	if e := human(a); e != nil {
		return Resource{}, e
	}
	if !a.Admin {
		return Resource{}, denied()
	}
	if e := validateText(S(in, "title"), "项目名称", 160); e != nil {
		return Resource{}, e
	}
	r := resource("project", S(in, "title"), "", "", M{"description": S(in, "description"), "allowExternalModels": B(in, "allowExternalModels")})
	r.ProjectID = r.ID
	if e := t.Put(r); e != nil {
		return r, e
	}
	m := resource("member", a.Name, r.ID, "", M{"identityId": a.ID, "roles": []string{"owner"}})
	return r, t.Put(m)
}
func (s *Service) CanAccess(ctx context.Context, a Actor, project string, roles ...string) error {
	return s.Store.Update(ctx, func(t *Tx) error { return s.allowed(t, a, project, roles...) })
}
func (s *Service) Projects(ctx context.Context, a Actor) ([]Resource, error) {
	all, e := s.Store.List("", "project", "")
	if e != nil {
		return nil, e
	}
	out := []Resource{}
	for _, p := range all {
		if s.CanAccess(ctx, a, p.ID) == nil {
			out = append(out, p)
		}
	}
	return out, nil
}
func (s *Service) Workspace(ctx context.Context, a Actor, p string) ([]Resource, error) {
	var all []Resource
	e := s.Store.Update(ctx, func(t *Tx) error {
		var e error
		a, e = t.CurrentActor(a)
		if e != nil {
			return e
		}
		if e = s.allowed(t, a, p); e != nil {
			return e
		}
		all, e = t.List(p, "", "")
		if e != nil {
			return e
		}
		roles, e := t.ProjectRoles(a, p)
		if e != nil {
			return e
		}
		for i := range all {
			all[i] = previewView(runtimeView(all[i]))
			if all[i].Kind == "project" {
				all[i].Data["effectiveRoles"] = roles
			}
			if all[i].Kind == "team_grant" {
				all[i], e = t.projectTeamView(all[i], a)
				if e != nil {
					return e
				}
			}
		}
		return nil
	})
	return all, e
}
func (s *Service) GetResource(ctx context.Context, a Actor, id string) (Resource, error) {
	var r Resource
	e := s.Store.Update(ctx, func(t *Tx) error {
		var e error
		a, e = t.CurrentActor(a)
		if e != nil {
			return e
		}
		r, e = t.Get(id)
		if e != nil {
			return e
		}
		if Has([]string{"team", "team_revision"}, r.Kind) {
			return teamAdministrator(a)
		}
		return s.allowed(t, a, r.ProjectID)
	})
	return previewView(runtimeView(r)), e
}
func checkRevision(g Resource, r Resource) error {
	if revision(r) != g.Revision {
		return conflict("需求版本已变化，需要重新规划、验证和审批")
	}
	return nil
}
func approved(t *Tx, g Resource) error {
	if int(N(g.Data, "approvedRevision")) != g.Revision {
		return conflict("需要先批准当前需求基线")
	}
	return nil
}
func money(in M, key string, defaultValue float64) (int64, error) {
	v := N(in, key)
	if _, ok := in[key]; !ok {
		v = defaultValue
	}
	if v <= 0 || v > 1000000 {
		return 0, invalid(fmt.Sprintf("%s 必须在 0 与 1000000 之间", key))
	}
	return cents(v), nil
}

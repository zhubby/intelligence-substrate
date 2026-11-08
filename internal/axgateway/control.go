package axgateway

import (
	"context"
	ax "github.com/google/ax/pkg/apis/v1alpha1"
	"google.golang.org/protobuf/proto"
)

type controlProxy struct {
	ax.UnimplementedAXServer
	g *Gateway
}

func (c *controlProxy) GetTask(ctx context.Context, r *ax.GetTaskRequest) (*ax.Task, error) {
	return controlCall(c.g, ctx, "task.get", r.GetAtespace(), r.GetName(), r, namePattern.MatchString(r.GetName()), func(ctx context.Context) (*ax.Task, error) { return c.g.control.GetTask(ctx, r) }, func(v *ax.Task, p Principal) bool {
		return metadataMatches(v.GetMetadata(), p.Atespace, r.GetName()) && taskSpecAllowed(v, p)
	})
}
func (c *controlProxy) ListTasks(ctx context.Context, r *ax.ListTasksRequest) (*ax.ListTasksResponse, error) {
	query := proto.Clone(r).(*ax.ListTasksRequest)
	if query.Limit == 0 {
		query.Limit = 100
	}
	return controlCall(c.g, ctx, "tasks.list", r.GetAtespace(), "", r, (r.GetLimit() == 0 || r.GetLimit() == 100) && r.GetOffset() >= 0 && r.GetOffset() <= 9900 && r.GetOffset()%100 == 0, func(ctx context.Context) (*ax.ListTasksResponse, error) { return c.g.control.ListTasks(ctx, query) }, func(v *ax.ListTasksResponse, p Principal) bool {
		if len(v.Tasks) > 100 {
			return false
		}
		seen := map[string]bool{}
		for _, task := range v.Tasks {
			if !metadataMatches(task.GetMetadata(), p.Atespace, "") || !taskSpecAllowed(task, p) || seen[task.GetMetadata().GetName()] {
				return false
			}
			seen[task.Metadata.Name] = true
		}
		return true
	})
}
func (c *controlProxy) CreateTask(ctx context.Context, r *ax.CreateTaskRequest) (*ax.Task, error) {
	task := r.GetTask()
	m := task.GetMetadata()
	name, space := m.GetName(), m.GetAtespace()
	valid := namePattern.MatchString(name) && task.GetApiVersion() == "ax.io/v1alpha1" && task.GetKind() == "Task" && m.GetCreateTime() == nil && task.GetStatus() == nil
	return controlCall(c.g, ctx, "task.create", space, name, r, valid, func(ctx context.Context) (*ax.Task, error) {
		return c.g.control.CreateTask(ctx, r)
	}, func(v *ax.Task, p Principal) bool {
		return metadataMatches(v.GetMetadata(), p.Atespace, name) && taskSpecAllowed(v, p) && proto.Equal(v.GetSpec(), task.GetSpec())
	}, func(p Principal) bool { return taskSpecAllowed(task, p) })
}
func (c *controlProxy) DeleteTask(ctx context.Context, r *ax.DeleteTaskRequest) (*ax.DeleteTaskResponse, error) {
	return controlCall(c.g, ctx, "task.delete", r.GetAtespace(), r.GetName(), r, namePattern.MatchString(r.GetName()), func(ctx context.Context) (*ax.DeleteTaskResponse, error) { return c.g.control.DeleteTask(ctx, r) }, func(*ax.DeleteTaskResponse, Principal) bool { return true })
}
func (c *controlProxy) SuspendTask(ctx context.Context, r *ax.SuspendTaskRequest) (*ax.Task, error) {
	return controlCall(c.g, ctx, "task.suspend", r.GetAtespace(), r.GetName(), r, namePattern.MatchString(r.GetName()), func(ctx context.Context) (*ax.Task, error) { return c.g.control.SuspendTask(ctx, r) }, func(v *ax.Task, p Principal) bool {
		return metadataMatches(v.GetMetadata(), p.Atespace, r.GetName()) && taskSpecAllowed(v, p)
	})
}
func (c *controlProxy) ResumeTask(ctx context.Context, r *ax.ResumeTaskRequest) (*ax.Task, error) {
	return controlCall(c.g, ctx, "task.resume", r.GetAtespace(), r.GetName(), r, namePattern.MatchString(r.GetName()), func(ctx context.Context) (*ax.Task, error) { return c.g.control.ResumeTask(ctx, r) }, func(v *ax.Task, p Principal) bool {
		return metadataMatches(v.GetMetadata(), p.Atespace, r.GetName()) && taskSpecAllowed(v, p)
	})
}
func (c *controlProxy) GetWorkspace(ctx context.Context, r *ax.GetWorkspaceRequest) (*ax.Workspace, error) {
	return controlCall(c.g, ctx, "workspace.get", r.GetAtespace(), r.GetName(), r, namePattern.MatchString(r.GetName()), func(ctx context.Context) (*ax.Workspace, error) { return c.g.control.GetWorkspace(ctx, r) }, func(v *ax.Workspace, p Principal) bool {
		return metadataMatches(v.GetMetadata(), p.Atespace, r.GetName()) && proto.Size(v.GetSpec()) == 0
	})
}
func (c *controlProxy) ListWorkspaces(ctx context.Context, r *ax.ListWorkspacesRequest) (*ax.ListWorkspacesResponse, error) {
	return controlCall(c.g, ctx, "workspaces.list", r.GetAtespace(), "", r, true, func(ctx context.Context) (*ax.ListWorkspacesResponse, error) {
		return c.g.control.ListWorkspaces(ctx, r)
	}, func(v *ax.ListWorkspacesResponse, p Principal) bool {
		seen := map[string]bool{}
		for _, w := range v.Workspaces {
			if !metadataMatches(w.GetMetadata(), p.Atespace, "") || seen[w.GetMetadata().GetName()] || proto.Size(w.GetSpec()) != 0 {
				return false
			}
			seen[w.Metadata.Name] = true
		}
		return true
	})
}
func (c *controlProxy) UpdateWorkspace(ctx context.Context, r *ax.UpdateWorkspaceRequest) (*ax.Workspace, error) {
	w := r.GetWorkspace()
	m := w.GetMetadata()
	name, space := m.GetName(), m.GetAtespace()
	valid := namePattern.MatchString(name) && w.GetApiVersion() == "ax.io/v1alpha1" && w.GetKind() == "Workspace" && m.GetCreateTime() == nil && proto.Size(w.GetSpec()) == 0
	return controlCall(c.g, ctx, "workspace.update", space, name, r, valid, func(ctx context.Context) (*ax.Workspace, error) { return c.g.control.UpdateWorkspace(ctx, r) }, func(v *ax.Workspace, p Principal) bool {
		return metadataMatches(v.GetMetadata(), p.Atespace, name) && proto.Size(v.GetSpec()) == 0
	})
}
func (c *controlProxy) DeleteWorkspace(ctx context.Context, r *ax.DeleteWorkspaceRequest) (*ax.DeleteWorkspaceResponse, error) {
	return controlCall(c.g, ctx, "workspace.delete", r.GetAtespace(), r.GetName(), r, namePattern.MatchString(r.GetName()), func(ctx context.Context) (*ax.DeleteWorkspaceResponse, error) {
		return c.g.control.DeleteWorkspace(ctx, r)
	}, func(*ax.DeleteWorkspaceResponse, Principal) bool { return true })
}

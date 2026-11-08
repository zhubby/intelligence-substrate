package integrations

import (
	"context"
	"errors"
	"fmt"

	ax "github.com/google/ax/pkg/apis/v1alpha1"
	p "github.com/intelligence-substrate/platform/internal/platform"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"gopkg.in/yaml.v3"
)

func (a AX) secureControl(ctx context.Context) (*axSecureSession, func(), error) {
	prepared, _, close, e := a.Open(ctx)
	return prepared.session, close, e
}
func (a AX) submitSecure(ctx context.Context, run p.Resource, repositoryURL string) (string, error) {
	if repositoryURL != "" {
		return "", fmt.Errorf("secure AX imports only the controlled local snapshot")
	}
	s, close, e := a.secureControl(ctx)
	if e != nil {
		return "", e
	}
	defer close()
	name := "is-" + stringsReplaceID(run.ID)
	meta := &ax.ObjectMeta{Name: name, Atespace: a.Atespace}
	w := &ax.Workspace{ApiVersion: "ax.io/v1alpha1", Kind: "Workspace", Metadata: meta, Spec: &ax.WorkspaceSpec{}}
	// From the first mutation onward the exact identity remains unresolved until
	// both deletion and complete absence observations have succeeded.
	if _, e = s.control.UpdateWorkspace(ctx, &ax.UpdateWorkspaceRequest{Workspace: w}); e != nil {
		return name, fmt.Errorf("AX workspace creation unconfirmed: %s", publicRPCCode(e))
	}
	task := &ax.Task{ApiVersion: "ax.io/v1alpha1", Kind: "Task", Metadata: meta, Spec: &ax.TaskSpec{Image: a.Image, Command: []string{"/bin/true"}, Debug: true, Workspace: &ax.WorkspaceRef{Name: name, Path: "/workspace"}, Resources: &ax.ResourcesSpec{Limits: &ax.ResourceList{Cpu: "2", Memory: "4Gi"}}}}
	if _, e = s.control.CreateTask(ctx, &ax.CreateTaskRequest{Task: task}); e != nil {
		return name, fmt.Errorf("AX task creation unconfirmed: %s", publicRPCCode(e))
	}
	if _, e = s.control.ResumeTask(ctx, &ax.ResumeTaskRequest{Name: name, Atespace: a.Atespace}); e != nil {
		return name, fmt.Errorf("AX task resume unconfirmed: %s", publicRPCCode(e))
	}
	return name, nil
}

func (a AX) describeSecure(ctx context.Context, name string) (string, error) {
	s, close, e := a.secureControl(ctx)
	if e != nil {
		return "", e
	}
	defer close()
	task, e := s.control.GetTask(ctx, &ax.GetTaskRequest{Name: name, Atespace: a.Atespace})
	if e != nil {
		return "", fmt.Errorf("AX task read failed: %s", publicRPCCode(e))
	}
	if e = a.exactTask(task, name); e != nil {
		return "", e
	}
	body, e := protojson.Marshal(task)
	if e != nil {
		return "", e
	}
	var data any
	if e = yaml.Unmarshal(body, &data); e != nil {
		return "", e
	}
	body, e = yaml.Marshal(data)
	return string(body), e
}
func (a AX) exactTask(task *ax.Task, name string) error {
	if task == nil || task.GetMetadata().GetName() != name || task.GetMetadata().GetAtespace() != a.Atespace {
		return fmt.Errorf("AX control returned a different task identity")
	}
	return nil
}
func (a AX) cancelSecure(ctx context.Context, name string) error {
	s, close, e := a.secureControl(ctx)
	if e != nil {
		return e
	}
	defer close()
	_, te := s.control.DeleteTask(ctx, &ax.DeleteTaskRequest{Name: name, Atespace: a.Atespace})
	_, we := s.control.DeleteWorkspace(ctx, &ax.DeleteWorkspaceRequest{Name: name, Atespace: a.Atespace})
	var result error
	if te != nil {
		result = errors.Join(result, fmt.Errorf("AX task deletion unconfirmed: %s", publicRPCCode(te)))
	}
	if we != nil {
		result = errors.Join(result, fmt.Errorf("AX workspace deletion unconfirmed: %s", publicRPCCode(we)))
	}
	return result
}
func (a AX) stoppedSecure(ctx context.Context, name string) error {
	s, close, e := a.secureControl(ctx)
	if e != nil {
		return e
	}
	defer close()
	var result error
	// The pinned AX server defaults to50 tasks, so a default first page is not
	// an absence observation. Enumerate bounded explicit pages and reject
	// duplicates/overlong pages; an exact point read below also covers shifting
	// pagination under concurrent writes.
	seenTasks := map[string]bool{}
	for offset := int64(0); ; offset += 100 {
		tasks, te := s.control.ListTasks(ctx, &ax.ListTasksRequest{Atespace: a.Atespace, Limit: 100, Offset: offset})
		if te != nil || tasks == nil || len(tasks.Tasks) > 100 {
			result = errors.Join(result, fmt.Errorf("AX task absence unconfirmed"))
			break
		}
		for _, t := range tasks.Tasks {
			n := t.GetMetadata().GetName()
			if n == "" || seenTasks[n] || t.GetMetadata().GetAtespace() != a.Atespace || n == name {
				result = errors.Join(result, fmt.Errorf("AX task listing invalid or original task retained"))
			}
			seenTasks[n] = true
		}
		if len(tasks.Tasks) < 100 {
			break
		}
		if offset >= 9900 {
			result = errors.Join(result, fmt.Errorf("AX task inventory exceeds bounded complete observation"))
			break
		}
	}
	workspaces, we := s.control.ListWorkspaces(ctx, &ax.ListWorkspacesRequest{Atespace: a.Atespace})
	if we != nil || workspaces == nil {
		result = errors.Join(result, fmt.Errorf("AX workspace absence unconfirmed"))
	} else {
		seen := map[string]bool{}
		for _, w := range workspaces.Workspaces {
			n := w.GetMetadata().GetName()
			if n == "" || seen[n] || w.GetMetadata().GetAtespace() != a.Atespace || n == name {
				result = errors.Join(result, fmt.Errorf("AX workspace listing invalid or original workspace retained"))
			}
			seen[n] = true
		}
	}
	if _, e := s.control.GetTask(ctx, &ax.GetTaskRequest{Atespace: a.Atespace, Name: name}); status.Code(e) != codes.NotFound {
		result = errors.Join(result, fmt.Errorf("AX exact task absence unconfirmed"))
	}
	if _, e := s.control.GetWorkspace(ctx, &ax.GetWorkspaceRequest{Atespace: a.Atespace, Name: name}); status.Code(e) != codes.NotFound {
		result = errors.Join(result, fmt.Errorf("AX exact workspace absence unconfirmed"))
	}
	return result
}

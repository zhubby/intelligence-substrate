package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"

	axapi "github.com/google/ax/pkg/apis/v1alpha1"
	p "github.com/intelligence-substrate/platform/internal/platform"
	"github.com/intelligence-substrate/platform/internal/process"
)

type AX struct {
	Binary         string
	Context        string
	Namespace      string
	Atespace       string
	Image          string
	Secure         *AXSecureConfig `json:"secure,omitempty"`
	AllowLegacyCLI bool            `json:"allowLegacyCLI,omitempty"`
	session        *axSecureSession
}

func AXFromEnv() AX {
	binary := os.Getenv("IS_AX_BINARY")
	if binary == "" {
		binary = "ax"
	}
	ns := os.Getenv("IS_AX_NAMESPACE")
	if ns == "" {
		ns = "ax-system"
	}
	space := os.Getenv("IS_AX_ATESPACE")
	if space == "" {
		space = "intelligence-substrate"
	}
	return AX{Binary: binary, Context: os.Getenv("IS_AX_CONTEXT"), Namespace: ns, Atespace: space, Image: os.Getenv("IS_AX_RUNNER_IMAGE"), AllowLegacyCLI: os.Getenv("IS_AX_ALLOW_LEGACY_CLI") == "true"}
}
func (a AX) args(args ...string) []string {
	out := []string{"--namespace=" + a.Namespace, "--atespace=" + a.Atespace}
	if a.Context != "" {
		out = append(out, "--context="+a.Context)
	}
	return append(out, args...)
}
func (a AX) call(ctx context.Context, args ...string) (string, error) {
	if a.Secure != nil || !a.AllowLegacyCLI {
		return "", fmt.Errorf("AX CLI routing requires explicit allowLegacyCLI and no secure configuration")
	}
	cmd := exec.CommandContext(ctx, a.Binary, a.args(args...)...)
	process.Protect(cmd)
	out, e := cmd.CombinedOutput()
	if e != nil {
		detail := strings.TrimSpace(string(out))
		if len(detail) > 4096 {
			detail = detail[:4096] + "…"
		}
		return "", fmt.Errorf("AX request failed: %w: %s", e, detail)
	}
	return strings.TrimSpace(string(out)), nil
}
func (a AX) Manifest(run p.Resource, repositoryURL string) ([]byte, error) {
	if !regexp.MustCompile(`^[^\s]+@sha256:[a-f0-9]{64}$`).MatchString(a.Image) {
		return nil, fmt.Errorf("IS_AX_RUNNER_IMAGE must be pinned by digest")
	}
	name := "is-" + strings.ToLower(strings.ReplaceAll(run.ID, "_", "-"))
	task := p.Obj(run.Data["task"])
	workspaceSpec := p.M{}
	if repositoryURL != "" {
		workspaceSpec["git"] = []any{p.M{"name": "source", "repo": repositoryURL, "branch": p.S(task, "branch")}}
	}
	workspace := p.M{"apiVersion": "ax.io/v1alpha1", "kind": "Workspace", "metadata": p.M{"name": name, "atespace": a.Atespace}, "spec": workspaceSpec}
	manifest := p.M{"apiVersion": "ax.io/v1alpha1", "kind": "Task", "metadata": p.M{"name": name, "atespace": a.Atespace}, "spec": p.M{"image": a.Image, "workspace": p.M{"name": name, "path": "/workspace"}, "command": []string{"/bin/true"}, "debug": true, "resources": p.M{"limits": p.M{"cpu": "2", "memory": "4Gi"}}}}
	x, _ := json.Marshal(workspace)
	y, _ := json.Marshal(manifest)
	return append(append(x, []byte("\n---\n")...), y...), nil
}
func (a AX) Submit(ctx context.Context, run p.Resource, repositoryURL string) (string, error) {
	if a.Secure != nil || a.session != nil {
		return a.submitSecure(ctx, run, repositoryURL)
	}
	body, e := a.Manifest(run, repositoryURL)
	if e != nil {
		return "", e
	}
	f, e := os.CreateTemp("", "intelligence-ax-*.yaml")
	if e != nil {
		return "", e
	}
	defer os.Remove(f.Name())
	if _, e = f.Write(body); e != nil {
		f.Close()
		return "", e
	}
	if e = f.Close(); e != nil {
		return "", e
	}
	name := "is-" + strings.ToLower(strings.ReplaceAll(run.ID, "_", "-"))
	if _, e = a.call(ctx, "apply", "-f", f.Name()); e != nil {
		return name, e
	}
	if _, e = a.call(ctx, "resume", "task", name); e != nil {
		return name, e
	}
	return name, nil
}
func (a AX) Suspend(ctx context.Context, name string) error {
	if a.Secure != nil || a.session != nil {
		s, close, e := a.secureControl(ctx)
		if e != nil {
			return e
		}
		defer close()
		_, e = s.control.SuspendTask(ctx, &axapi.SuspendTaskRequest{Name: name, Atespace: a.Atespace})
		if e != nil {
			return fmt.Errorf("AX task suspension unconfirmed: %s", publicRPCCode(e))
		}
		return nil
	}
	_, e := a.call(ctx, "suspend", "task", name)
	return e
}
func (a AX) Resume(ctx context.Context, name string) error {
	if a.Secure != nil || a.session != nil {
		s, close, e := a.secureControl(ctx)
		if e != nil {
			return e
		}
		defer close()
		_, e = s.control.ResumeTask(ctx, &axapi.ResumeTaskRequest{Name: name, Atespace: a.Atespace})
		if e != nil {
			return fmt.Errorf("AX task resume unconfirmed: %s", publicRPCCode(e))
		}
		return nil
	}
	_, e := a.call(ctx, "resume", "task", name)
	return e
}
func (a AX) Cancel(ctx context.Context, name string) error {
	if a.Secure != nil || a.session != nil {
		return a.cancelSecure(ctx, name)
	}
	_, taskErr := a.call(ctx, "delete", "task", name)
	_, workspaceErr := a.call(ctx, "delete", "workspace", name)
	return errors.Join(taskErr, workspaceErr)
}
func (a AX) Describe(ctx context.Context, name string) (string, error) {
	if a.Secure != nil || a.session != nil {
		return a.describeSecure(ctx, name)
	}
	return a.call(ctx, "get", "task", name)
}
func (a AX) Exec(ctx context.Context, name string, command []string, output io.Writer) (int, error) {
	if a.Secure != nil || a.session != nil {
		return a.execSecure(ctx, name, command, output)
	}
	if a.Secure != nil || !a.AllowLegacyCLI {
		return 1, fmt.Errorf("AX CLI routing requires explicit allowLegacyCLI and no secure configuration")
	}
	args := []string{"ssh", name, "--"}
	args = append(args, command...)
	cmd := exec.CommandContext(ctx, a.Binary, a.args(args...)...)
	process.Protect(cmd)
	cmd.Stdout = output
	cmd.Stderr = output
	e := cmd.Run()
	if e == nil {
		return 0, nil
	}
	if exit, ok := e.(*exec.ExitError); ok {
		return exit.ExitCode(), nil
	}
	return 1, e
}
func (a AX) Capability() p.M {
	status := "unconfigured"
	reason := "配置 AX 控制面、WorkerPool、固定镜像和出网策略后启用"
	if a.Image != "" && (a.Secure != nil || a.AllowLegacyCLI) {
		status = "configured"
		reason = "已配置镜像，仍需真实集群执行验证"
	}
	if _, e := exec.LookPath(a.Binary); e != nil && a.Secure == nil && a.AllowLegacyCLI {
		reason = "AX CLI 未安装"
	}
	return p.M{"id": "ax", "title": "AX / Substrate", "status": status, "reason": reason, "runtime": "linux", "namespace": a.Namespace, "verified": false}
}
func Capabilities(modelReady bool) []p.M {
	out := []p.M{AXFromEnv().Capability(), {"id": "model", "title": "模型网关", "status": "unconfigured", "reason": "配置模型地址、模型标识、凭据及价格后启用", "verified": false}, {"id": "native-macos", "title": "macOS / iOS 执行节点", "status": "worker_required", "reason": "注册隔离原生节点并完成实际构建验证", "verified": false}, {"id": "native-windows", "title": "Windows 执行节点", "status": "worker_required", "reason": "注册 Windows 节点并完成实际构建验证", "verified": false}}
	if modelReady {
		out[1]["status"] = "configured"
		out[1]["reason"] = "可调用；实际模型响应按执行记录验证"
	}
	for _, target := range p.Targets {
		out = append(out, p.M{"id": "channel-" + target, "title": target, "status": "credentials_required", "reason": "需要渠道账户、可信发布执行节点和独立发布验证", "verified": false})
	}
	return out
}

// Package worker contains the trusted job controller. Repository code never
// receives the platform token; only this controller can submit artifacts/results.
package worker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/intelligence-substrate/platform/internal/integrations"
	p "github.com/intelligence-substrate/platform/internal/platform"
	"github.com/intelligence-substrate/platform/internal/process"
	"github.com/intelligence-substrate/platform/internal/source"
)

type Config struct {
	Driver                string              `json:"driver"`
	Image                 string              `json:"image"`
	AllowNative           bool                `json:"allowNative"`
	StateDir              string              `json:"stateDir"`
	Publishers            map[string][]string `json:"publishers"`
	Signers               map[string][]string `json:"signers"`
	ContinuationProviders map[string][]string `json:"continuationProviders"`
	MaterialProviders     map[string][]string `json:"materialProviders"`
	StatusProviders       map[string][]string `json:"statusProviders"`
	RollbackProviders     map[string][]string `json:"rollbackProviders"`
	PublishEnvironment    []string            `json:"publishEnvironment"`
	AX                    integrations.AX     `json:"ax"`
}
type Runner struct {
	Client *Client
	Config Config
}
type completion struct {
	Operation string `json:"operation,omitempty"`
	ID        string `json:"id"`
	Lease     string `json:"leaseToken"`
	Exit      int    `json:"exitCode"`
	Log       string `json:"log"`
	Result    p.M    `json:"result"`
}
type cappedLog struct {
	mu sync.Mutex
	b  []byte
}

func (l *cappedLog) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.b = append(l.b, b...)
	if len(l.b) > 120000 {
		l.b = l.b[len(l.b)-120000:]
	}
	return len(b), nil
}
func (l *cappedLog) String() string { l.mu.Lock(); defer l.mu.Unlock(); return string(l.b) }

func (r *Runner) RunOnce(ctx context.Context) (bool, error) {
	if r.Config.StateDir == "" {
		return false, fmt.Errorf("worker state directory is required")
	}
	if e := os.MkdirAll(r.Config.StateDir, 0700); e != nil {
		return false, e
	}
	if e := r.flush(ctx); e != nil {
		return false, e
	}
	run, e := r.Client.Command(ctx, "tasks.claim", p.M{})
	if e != nil {
		return false, e
	}
	if run.Kind != "run" {
		return false, nil
	}
	deadline, e := time.Parse(time.RFC3339Nano, p.S(run.Data, "deadline"))
	if e != nil {
		return true, e
	}
	job, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	stopped, heartbeatDone := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		timer := time.NewTicker(20 * time.Second)
		defer timer.Stop()
		failures := 0
		for {
			select {
			case <-stopped:
				return
			case <-job.Done():
				return
			case <-timer.C:
				beat, err := r.Client.Command(job, "tasks.progress", p.M{"id": run.ID, "leaseToken": p.S(run.Data, "leaseToken"), "message": "执行节点正在运行工作项"})
				if err != nil {
					failures++
					var api *APIError
					if errors.As(err, &api) && api.Status < 500 || failures >= 2 {
						cancel()
						return
					}
				} else if beat.Status != "running" {
					cancel()
					return
				} else {
					failures = 0
				}
			}
		}
	}()
	log := &cappedLog{}
	result, exit, execErr := r.execute(job, run, log)
	close(stopped)
	<-heartbeatDone
	if execErr != nil {
		if result == nil {
			result = p.M{}
		}
		result["reason"] = execErr.Error()
		if exit == 0 {
			exit = 1
		}
		if p.B(result, "blocked") {
			fmt.Fprintln(log, "BLOCKED:", execErr)
		} else {
			fmt.Fprintln(log, "FAILED:", execErr)
		}
	}
	c := completion{ID: run.ID, Lease: p.S(run.Data, "leaseToken"), Exit: exit, Log: log.String(), Result: result}
	if execErr == nil && exit == 0 && len(p.Obj(result["messageWait"])) > 0 {
		c.Operation, c.Result = "wait", p.Obj(result["messageWait"])
	}
	b, _ := json.Marshal(c)
	file := filepath.Join(r.Config.StateDir, run.ID+".json")
	if e = saveCompletion(file, b); e != nil {
		return true, e
	}
	ack, cancelAck := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	defer cancelAck()
	return true, r.send(ack, file, c)
}
func saveCompletion(path string, body []byte) error {
	f, e := os.CreateTemp(filepath.Dir(path), ".completion-")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if _, e = f.Write(body); e != nil {
		f.Close()
		return e
	}
	if e = f.Sync(); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	if e = os.Rename(f.Name(), path); e != nil {
		return e
	}
	if directory, e := os.Open(filepath.Dir(path)); e == nil {
		_ = directory.Sync()
		directory.Close()
	}
	return nil
}
func (r *Runner) flush(ctx context.Context) error {
	files, e := filepath.Glob(filepath.Join(r.Config.StateDir, "run_*.json"))
	if e != nil {
		return e
	}
	for _, file := range files {
		body, e := os.ReadFile(file)
		if e != nil {
			return e
		}
		var c completion
		if e = json.Unmarshal(body, &c); e != nil {
			return e
		}
		if e = r.send(ctx, file, c); e != nil {
			return e
		}
	}
	return nil
}
func (r *Runner) send(ctx context.Context, file string, c completion) error {
	b, _ := json.Marshal(c)
	var in p.M
	_ = json.Unmarshal(b, &in)
	command := "tasks.complete"
	if c.Operation == "wait" {
		command = "tasks.wait"
		in = p.Obj(p.Obj(in)["result"])
		in["id"], in["leaseToken"] = c.ID, c.Lease
	} else if c.Operation != "" {
		return fmt.Errorf("unknown journal operation %q; preserved for reconciliation", c.Operation)
	}
	_, e := r.Client.Command(ctx, command, in)
	if e == nil {
		if c.Operation == "wait" && p.S(in, "checkpointId") != "" {
			if cleanupErr := os.Remove(r.checkpointPath(c.ID)); cleanupErr != nil && !os.IsNotExist(cleanupErr) {
				return cleanupErr
			}
		}
		return os.Remove(file)
	}
	var api *APIError
	if errors.As(e, &api) && (api.Status == 409 || api.Status == 403) {
		if renameErr := os.Rename(file, file+".unresolved"); renameErr != nil {
			return renameErr
		}
		return fmt.Errorf("completion requires reconciliation; preserved in %s: %w", filepath.Base(file)+".unresolved", e)
	}
	return e
}

func (r *Runner) execute(ctx context.Context, run p.Resource, log io.Writer) (p.M, int, error) {
	task := p.Obj(run.Data["task"])
	kind := p.S(task, "kind")
	if p.S(task, "signingMode") != "" {
		if p.S(task, "signingMode") == "windows-msix" {
			return r.signWindows(ctx, run)
		}
		if p.S(task, "signingMode") == "ios-ipa" {
			return r.signIOS(ctx, run)
		}
		if p.S(task, "signingMode") == "macos-pkg" {
			return r.signMacOS(ctx, run)
		}
		return r.signAndroid(ctx, run)
	}
	if len(r.Config.Signers) != 0 {
		return p.M{"blocked": true}, 1, fmt.Errorf("a signing worker cannot execute repository or publication tasks")
	}
	if kind == "publish" || kind == "publish_status" || kind == "publish_continue" || kind == "publish_materials" || kind == "runtime_rollback" {
		return r.publish(ctx, run, log)
	}
	if r.Config.Driver == "native" && !r.Config.AllowNative {
		return p.M{"blocked": true}, 1, fmt.Errorf("native execution requires explicit allowNative on a disposable node")
	}
	if r.Config.Driver != "native" && r.Config.Driver != "container" && r.Config.Driver != "ax" {
		return p.M{"blocked": true}, 1, fmt.Errorf("unsupported execution driver %q", r.Config.Driver)
	}
	if r.Config.Driver == "native" && p.S(task, "runtime") != runtime.GOOS {
		return p.M{"blocked": true}, 1, fmt.Errorf("task requires %s; this native node is %s", p.S(task, "runtime"), runtime.GOOS)
	}
	dir, e := os.MkdirTemp("", "is-job-")
	if e != nil {
		return nil, 1, e
	}
	defer os.RemoveAll(dir)
	var resources []p.Resource
	if e = r.Client.JSON(ctx, "GET", "projects/"+run.ProjectID+"/workspace", nil, &resources); e != nil {
		return nil, 1, e
	}
	continuation := p.Obj(run.Data["messageContinuation"])
	hasContinuation := len(continuation) > 0
	progress := p.Obj(run.Data["agentResume"])
	hasProgress := len(progress) > 0
	repoID := p.S(task, "repositoryId")
	if hasContinuation {
		if !p.Has([]string{"analysis", "code", "design"}, kind) || len(p.Items(task["command"])) > 0 || p.S(continuation, "waitId") == "" || kind != "analysis" && (p.S(continuation, "checkpointId") == "" || len(p.S(continuation, "checkpointDigest")) != 64) {
			return p.M{"blocked": true}, 1, fmt.Errorf("invalid task continuation")
		}
		repoID = p.S(p.Obj(continuation["source"]), "repositoryId")
	}
	if hasProgress {
		if hasContinuation && p.Hash(progress["source"]) != p.Hash(continuation["source"]) {
			return p.M{"blocked": true}, 1, fmt.Errorf("Agent progress and wait source differ")
		}
		repoID = p.S(p.Obj(progress["source"]), "repositoryId")
	}
	binding := p.Obj(task["sourceBinding"])
	for _, v := range resources {
		if v.Kind == "repository" && repoID == "" && !hasContinuation && !hasProgress {
			repoID = v.ID
		}
	}
	if repoID == "" && kind == "code" {
		repo, e := r.Client.Command(ctx, "repositories.create", p.M{"projectId": run.ProjectID, "runId": run.ID, "leaseToken": p.S(run.Data, "leaseToken"), "title": "application", "description": "Source for delivery goal " + run.GoalID})
		if e != nil {
			return nil, 1, e
		}
		repoID = repo.ID
	}
	commit := p.S(task, "commit")
	if hasContinuation {
		commit = p.S(p.Obj(continuation["source"]), "commit")
	}
	if hasProgress {
		commit = p.S(p.Obj(progress["source"]), "commit")
	}
	branch := p.S(task, "branch")
	if branch == "" {
		branch = "agent/" + run.GoalID
	}
	expected := ""
	if len(binding) > 0 {
		if p.S(binding, "taskId") != p.S(run.Data, "taskId") || p.S(binding, "repositoryId") != repoID || p.S(binding, "commit") != commit || !p.CommitPattern.MatchString(commit) {
			return p.M{"blocked": true}, 1, fmt.Errorf("invalid authoritative source binding")
		}
		if kind == "code" {
			branch, expected = p.S(binding, "targetBranch"), p.S(binding, "expectedHead")
			var refs p.M
			if e = r.Client.JSON(ctx, "GET", "repositories/"+repoID+"/refs", nil, &refs); e != nil {
				return p.M{"blocked": true}, 1, e
			}
			if p.S(refs, "repositoryId") != repoID || p.S(p.Obj(refs["heads"]), branch) != expected {
				return p.M{"blocked": true}, 1, fmt.Errorf("code target changed after source binding; reconcile or approve new work")
			}
		}
	}
	var sourceEntries []source.Entry
	var integration *integrationWorkspace
	if kind == "code" && p.S(task, "sourceMode") == "integrate" {
		integration, e = r.prepareSourceIntegration(ctx, run)
		if e != nil {
			return p.M{"blocked": true}, 1, e
		}
	}
	if repoID != "" {
		// Older in-flight code runs still need their legacy target lookup.
		// Every task with a pinned SHA can fetch it without branch discovery.
		if commit == "" || (kind == "code" && len(binding) == 0) {
			var view p.M
			e = r.Client.JSON(ctx, "GET", "repositories/"+repoID+"?branch="+urlQuery(branch), nil, &view)
			if e != nil {
				e = r.Client.JSON(ctx, "GET", "repositories/"+repoID+"?branch=main", nil, &view)
			} else {
				expected = strings.Split(p.S(view, "log"), "\t")[0]
			}
			if e != nil {
				return nil, 1, e
			}
			if commit == "" {
				commit = strings.Split(p.S(view, "log"), "\t")[0]
			}
		}
		workspaceCommit := commit
		if integration != nil {
			workspaceCommit = integration.workspace
		}
		response, e := r.Client.request(ctx, "GET", "repositories/"+repoID+"/archive?commit="+workspaceCommit, nil)
		if e != nil {
			return nil, 1, e
		}
		sourceEntries, _, e = source.Extract(response.Body, dir)
		response.Body.Close()
		if e != nil {
			return nil, 1, e
		}
	}
	var sourceState *sourceWorkspace
	if kind == "code" || kind == "design" {
		sourceState, e = newSourceWorkspace(sourceEntries)
		if e != nil {
			return nil, 1, e
		}
		defer sourceState.close()
		sourceState.integration = integration
	}
	if hasContinuation && kind != "analysis" {
		if e = r.restoreWorkspaceCheckpoint(ctx, run, sourceState, dir); e != nil {
			return p.M{"blocked": true}, 1, e
		}
	}
	if resume := p.Obj(run.Data["agentResume"]); len(resume) > 0 {
		if p.N(run.Data, "agentProgressVersion") != 1 || !p.Has([]string{"analysis", "code", "design"}, kind) || len(p.Items(task["command"])) != 0 || p.S(resume, "stepId") == "" || p.Hash(resume["source"]) != p.Hash(p.M{"repositoryId": repoID, "commit": commit}) {
			return p.M{"blocked": true}, 1, fmt.Errorf("invalid confirmed Agent progress")
		}
		if kind != "analysis" {
			if len(p.S(resume, "checkpointDigest")) != 64 || p.S(resume, "checkpointId") == "" {
				return p.M{"blocked": true}, 1, fmt.Errorf("Agent progress has no exact file checkpoint")
			}
			if e = r.restorePrivateWorkspaceCheckpoint(ctx, run, sourceState, dir, true); e != nil {
				return p.M{"blocked": true}, 1, e
			}
		}
	}
	if _, seed := task["blueprintSeed"]; seed {
		return r.initializeBlueprint(ctx, run, sourceState, dir, repoID, commit, branch, expected, sourceEntries)
	}
	argv := p.Strings(task["command"])
	if integration != nil && len(integration.conflicts) == 0 && len(argv) == 0 && !hasContinuation {
		op, err := r.commitSource(ctx, run, sourceState, dir, repoID, commit, branch, expected)
		if err != nil {
			return p.M{"blocked": true}, 1, err
		}
		return p.M{"commit": p.S(op.Data, "commit"), "repositoryId": repoID, "branch": branch, "sourceOperationId": op.ID, "integrationId": integration.id, "source": "controller-integration"}, 0, nil
	}
	var reportPath string
	if kind == "test" {
		pipelineID := p.S(task, "pipelineId")
		var pipeline p.Resource
		if e = r.Client.JSON(ctx, "GET", "resources/"+pipelineID, nil, &pipeline); e != nil {
			return nil, 1, e
		}
		if pipeline.Revision != int(p.N(task, "pipelineRevision")) {
			return p.M{"blocked": true}, 1, fmt.Errorf("pipeline changed")
		}
		if p.S(task, "qualityPolicyId") != "" && p.Hash(pipeline.Data) != p.S(task, "qualityPipelineDigest") {
			return p.M{"blocked": true}, 1, fmt.Errorf("protected quality pipeline content changed")
		}
		argv = p.Strings(pipeline.Data["command"])
		reportPath = p.S(pipeline.Data, "reportPath")
		if reportPath != "" {
			if e = p.ValidateReportPath(reportPath); e != nil {
				return p.M{"blocked": true}, 1, e
			}
			name, err := p.SafePath(dir, reportPath)
			if err != nil {
				return p.M{"blocked": true}, 1, err
			}
			// A report shipped in source cannot stand in for this execution.
			if info, err := os.Lstat(name); err == nil {
				if !info.Mode().IsRegular() {
					return p.M{"blocked": true}, 1, fmt.Errorf("report path is not a regular file")
				}
				if err = os.Remove(name); err != nil {
					return p.M{"blocked": true}, 1, err
				}
			} else if !os.IsNotExist(err) {
				return p.M{"blocked": true}, 1, err
			}
		}
		if e = r.download(ctx, p.S(task, "artifactId"), filepath.Join(dir, ".intelligence-artifact")); e != nil {
			return nil, 1, e
		}
	}
	if len(argv) > 0 {
		exit, e := r.execRun(ctx, run, dir, argv, log)
		result := p.M{"commit": commit, "repositoryId": repoID, "command": argv, "environment": r.Config.Driver}
		if kind == "test" && reportPath != "" {
			name, err := p.SafePath(dir, reportPath)
			if err != nil {
				result["blocked"] = true
				return result, exit, err
			}
			if _, err = os.Lstat(name); os.IsNotExist(err) && (e != nil || exit != 0) {
				// Preserve the real failure and logs even when no report was produced.
				return result, exit, e
			}
			a, err := r.upload(ctx, run, dir, reportPath, repoID, commit, p.S(task, "target"), "report")
			if err != nil {
				result["blocked"] = true
				return result, exit, fmt.Errorf("required quality report: %w", err)
			}
			result["reportArtifactId"] = a.ID
		}
		if e == nil && exit == 0 && kind == "code" {
			if integration != nil && len(integration.conflicts) > 0 {
				if err := r.resolveIntegration(ctx, run, sourceState, dir, integration.conflicts); err != nil {
					return p.M{"blocked": true}, 1, err
				}
			}
			op, err := r.commitSource(ctx, run, sourceState, dir, repoID, commit, branch, expected)
			if err != nil {
				return p.M{"blocked": true}, 1, err
			}
			result["commit"], result["branch"], result["sourceOperationId"] = p.S(op.Data, "commit"), branch, op.ID
		}
		if e == nil && exit == 0 && kind == "build" {
			if output := p.S(task, "outputPath"); output != "" {
				artifactType := "package"
				if p.S(task, "artifactStage") == "unsigned" {
					artifactType = "unsigned-package"
				}
				a, e := r.upload(ctx, run, dir, output, repoID, commit, p.S(task, "target"), artifactType)
				if e != nil {
					return nil, 1, e
				}
				result["artifactId"] = a.ID
			}
		}
		return result, exit, e
	}
	if kind == "build" || kind == "test" || kind == "security" {
		return p.M{"blocked": true}, 1, fmt.Errorf("task needs an approved command or protected validation pipeline")
	}
	if kind == "code" {
		sourceState.inputDigest, e = sourceState.fingerprint(ctx, dir, io.Discard)
		if e != nil {
			return nil, 1, e
		}
	}
	return r.agent(ctx, run, resources, sourceState, dir, repoID, commit, branch, expected, log)
}

func urlQuery(s string) string {
	return strings.NewReplacer("%", "%25", "/", "%2F", " ", "%20", "+", "%2B", "?", "%3F", "&", "%26", "#", "%23").Replace(s)
}
func extractSource(dir string, body io.Reader) error {
	_, _, e := source.Extract(body, dir)
	return e
}
func (r *Runner) exec(ctx context.Context, dir string, argv []string, output io.Writer) (int, error) {
	if len(argv) == 0 || strings.HasPrefix(argv[0], "-") {
		return 1, fmt.Errorf("invalid command")
	}
	var cmd *exec.Cmd
	if r.Config.Driver == "ax" {
		return 1, fmt.Errorf("AX commands require a durable run sandbox")
	}
	if r.Config.Driver == "container" {
		if !regexp.MustCompile(`^[^\s]+@sha256:[a-f0-9]{64}$`).MatchString(r.Config.Image) {
			return 1, fmt.Errorf("container toolchain image must be pinned to sha256 digest")
		}
		name := "is-job-" + strings.ToLower(p.NewID(""))
		args := []string{"run", "--rm", "--pull=never", "--name", name, "--network=none", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--pids-limit=256", "--memory=4g", "--cpus=2", "--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()), "--tmpfs", "/tmp:rw,nosuid,size=512m", "--mount", "type=bind,source=" + dir + ",target=/workspace", "-w", "/workspace", "-e", "HOME=/tmp", "-e", "IS_ARTIFACT_PATH=/workspace/.intelligence-artifact", r.Config.Image}
		args = append(args, argv...)
		cmd = exec.CommandContext(ctx, "docker", args...)
		defer func() {
			c, stop := context.WithTimeout(context.Background(), 10*time.Second)
			defer stop()
			_ = exec.CommandContext(c, "docker", "rm", "-f", name).Run()
		}()
	} else {
		cmd = exec.CommandContext(ctx, argv[0], argv[1:]...)
		cmd.Dir = dir
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "TMPDIR=" + os.TempDir(), "LANG=C.UTF-8", "IS_ARTIFACT_PATH=" + filepath.Join(dir, ".intelligence-artifact")}
		if runtime.GOOS == "windows" {
			cmd.Env = append(cmd.Env, "SystemRoot="+os.Getenv("SystemRoot"), "TEMP="+os.TempDir())
		}
	}
	cmd.Stdout = output
	cmd.Stderr = output
	process.Protect(cmd)
	cmd.WaitDelay = 5 * time.Second
	e := cmd.Run()
	if e == nil {
		return 0, nil
	}
	var exit *exec.ExitError
	if errors.As(e, &exit) {
		return exit.ExitCode(), nil
	}
	return 1, e
}
func (r *Runner) download(ctx context.Context, id, dest string) error {
	response, e := r.Client.request(ctx, "GET", "artifacts/"+id+"/content", nil)
	if e != nil {
		return e
	}
	defer response.Body.Close()
	file, e := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	_, e = io.Copy(file, io.LimitReader(response.Body, 512<<20+1))
	closeErr := file.Close()
	if e != nil {
		return e
	}
	return closeErr
}
func (r *Runner) upload(ctx context.Context, run p.Resource, dir, name, repo, commit, target, kind string) (p.Resource, error) {
	path, e := p.SafePath(dir, name)
	if e != nil {
		return p.Resource{}, e
	}
	file, e := os.Open(path)
	if e != nil {
		return p.Resource{}, e
	}
	defer file.Close()
	stat, e := file.Stat()
	if e != nil || !stat.Mode().IsRegular() {
		return p.Resource{}, fmt.Errorf("artifact must be a regular file")
	}
	query := urlValues(p.M{"goalId": run.GoalID, "repositoryId": repo, "commit": commit, "target": target, "type": kind, "name": filepath.Base(path), "runId": run.ID, "leaseToken": p.S(run.Data, "leaseToken")})
	response, e := r.Client.request(ctx, "POST", "artifacts/upload?"+query, file)
	if e != nil {
		return p.Resource{}, e
	}
	defer response.Body.Close()
	var out p.Resource
	e = json.NewDecoder(response.Body).Decode(&out)
	return out, e
}
func urlValues(in p.M) string {
	parts := []string{}
	for key, value := range in {
		parts = append(parts, urlQuery(key)+"="+urlQuery(fmt.Sprint(value)))
	}
	return strings.Join(parts, "&")
}

func (r *Runner) publish(ctx context.Context, run p.Resource, log io.Writer) (p.M, int, error) {
	task := p.Obj(run.Data["task"])
	target := p.S(task, "target")
	argv := r.Config.Publishers[target]
	action := "publish"
	if p.S(task, "kind") == "publish_status" {
		action = "status"
		argv = r.Config.StatusProviders[target]
		if len(argv) == 0 {
			return p.M{"blocked": true}, 1, fmt.Errorf("no explicit read-only status provider configured for %s; the mutating publisher is never used as fallback", target)
		}
	}
	if p.S(task, "kind") == "publish_continue" {
		action = "continue"
		argv = r.Config.ContinuationProviders[target]
		if len(argv) == 0 {
			return p.M{"blocked": true}, 1, fmt.Errorf("no explicit continuation provider configured for %s", target)
		}
	}
	if p.S(task, "kind") == "publish_materials" {
		action = "materials"
		argv = r.Config.MaterialProviders[target]
		if len(argv) == 0 {
			return p.M{"blocked": true}, 1, fmt.Errorf("no explicit material revision provider configured for %s", target)
		}
	}
	if p.S(task, "kind") == "runtime_rollback" {
		action = "rollback"
		argv = r.Config.RollbackProviders[target]
		if len(argv) == 0 {
			return p.M{"blocked": true}, 1, fmt.Errorf("no explicit trusted rollback provider configured for %s; publisher fallback is forbidden", target)
		}
	}
	if len(argv) == 0 {
		return p.M{"blocked": true}, 1, fmt.Errorf("no trusted publisher configured for %s; external result must be reconciled before retry", target)
	}
	if !filepath.IsAbs(argv[0]) {
		return p.M{"blocked": true}, 1, fmt.Errorf("publisher executable must be an absolute administrator-controlled path")
	}
	dir, e := os.MkdirTemp("", "is-publish-")
	if e != nil {
		return nil, 1, e
	}
	defer os.RemoveAll(dir)
	artifactPath := ""
	if action == "publish" {
		artifactPath = filepath.Join(dir, "package")
		var artifact p.Resource
		if e = r.Client.JSON(ctx, "GET", "resources/"+p.S(task, "artifactId"), nil, &artifact); e != nil {
			return nil, 1, e
		}
		if artifact.Kind != "artifact" || artifact.ProjectID != run.ProjectID || artifact.GoalID != run.GoalID || p.S(artifact.Data, "commit") != p.S(task, "commit") || p.S(artifact.Data, "target") != target {
			return nil, 1, fmt.Errorf("publication artifact differs from approved task binding")
		}
		if expected := p.S(task, "artifactDigest"); expected != "" && expected != p.S(artifact.Data, "digest") {
			return nil, 1, fmt.Errorf("publication digest differs from approved task")
		}
		task["artifactDigest"], task["artifactSize"] = artifact.Data["digest"], artifact.Data["size"]
		if e = r.download(ctx, p.S(task, "artifactId"), artifactPath); e != nil {
			return nil, 1, e
		}
		file, err := os.Open(artifactPath)
		if err != nil {
			return nil, 1, err
		}
		h := sha256.New()
		size, err := io.Copy(h, file)
		file.Close()
		if err != nil || size != int64(p.N(task, "artifactSize")) || hex.EncodeToString(h.Sum(nil)) != p.S(task, "artifactDigest") {
			return nil, 1, fmt.Errorf("downloaded publication artifact failed integrity verification")
		}
	}
	payload := p.M{"protocolVersion": "1", "action": action, "projectId": run.ProjectID, "goalId": run.GoalID, "operationKey": p.S(task, "operationKey"), "runId": run.ID, "releaseId": p.S(task, "releaseId"), "target": target, "version": p.S(task, "version"), "artifactPath": artifactPath, "artifactId": p.S(task, "artifactId"), "artifactDigest": p.S(task, "artifactDigest"), "artifactSize": p.N(task, "artifactSize"), "commit": p.S(task, "commit"), "binding": p.S(task, "binding"), "externalId": p.S(task, "externalId")}
	if digest := p.S(task, "metadataDigest"); digest != "" {
		payload["metadataDigest"] = digest
	}
	if p.S(task, "materialRevisionId") != "" {
		payload["binding"] = p.S(task, "publicationBinding")
		payload["sourceBinding"] = p.S(task, "sourcePublicationBinding")
		for _, key := range []string{"materialRevisionId", "sourceOperationKey", "sourceMetadataDigest"} {
			payload[key] = p.S(task, key)
		}
		if scope := p.S(task, "materialScope"); scope != "" {
			payload["materialScope"] = scope
		}
	}
	if action == "rollback" {
		payload["expectedActive"] = p.S(task, "expectedActive")
		payload["rollbackTo"] = p.S(task, "rollbackTo")
	}
	input, _ := json.Marshal(payload)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	process.Protect(cmd)
	cmd.Dir = dir
	cmd.Stdin = bytes.NewReader(input)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "TMPDIR=" + os.TempDir()}
	if runtime.GOOS == "windows" {
		cmd.Env = append(cmd.Env, "SystemRoot="+os.Getenv("SystemRoot"), "TEMP="+os.TempDir())
	}
	for _, key := range r.Config.PublishEnvironment {
		if strings.HasPrefix(key, "IS_") {
			return nil, 1, fmt.Errorf("publisher cannot receive platform environment variables")
		}
		if value, ok := os.LookupEnv(key); ok {
			cmd.Env = append(cmd.Env, key+"="+value)
		}
	}
	output := &cappedLog{}
	cmd.Stdout = output
	cmd.Stderr = log
	cmd.WaitDelay = 5 * time.Second
	if e = cmd.Run(); e != nil {
		return p.M{}, 1, e
	}
	var result p.M
	if e = json.Unmarshal([]byte(output.String()), &result); e != nil {
		return p.M{}, 1, fmt.Errorf("publisher result must be a JSON object; inspect external operation by operationKey")
	}
	states := []string{"processing", "awaiting_review", "published"}
	if action == "status" {
		states = append(states, "awaiting_release", "rejected", "halted", "unchanged")
	}
	if p.S(result, "externalId") == "" || !p.Has(states, p.S(result, "state")) || ((action == "status" || action == "continue" || action == "materials") && p.S(result, "externalId") != p.S(task, "externalId")) {
		return result, 1, fmt.Errorf("publisher must return actual externalId and state")
	}
	if action == "rollback" && (p.S(result, "state") != "published" || p.S(result, "externalId") != p.S(task, "expectedExternalId")) {
		return result, 1, fmt.Errorf("rollback provider must return the exact approved activation")
	}
	return result, 0, nil
}

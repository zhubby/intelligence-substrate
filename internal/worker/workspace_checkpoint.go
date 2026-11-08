package worker

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	p "github.com/intelligence-substrate/platform/internal/platform"
	"github.com/intelligence-substrate/platform/internal/source"
)

func (r *Runner) checkpointPath(run string) string {
	return filepath.Join(r.Config.StateDir, "workspace_"+run+".tar")
}

func (r *Runner) saveWorkspaceCheckpoint(ctx context.Context, run p.Resource, state *sourceWorkspace, root, repo, base string) (p.Resource, error) {
	return r.savePrivateWorkspaceCheckpoint(ctx, run, state, root, repo, base, "")
}

func (r *Runner) savePrivateWorkspaceCheckpoint(ctx context.Context, run p.Resource, state *sourceWorkspace, root, repo, base, stepID string) (p.Resource, error) {
	if state == nil || state.commitTried && !state.commitRejected {
		return p.Resource{}, fmt.Errorf("workspace cannot be saved after source submission")
	}
	// A failed/uncertain suspension must never adopt incomplete source via the
	// stopped-source fallback. Keep the actual archive on this node for review.
	if stepID == "" {
		state.checkpointTried = true
	}
	entries, e := state.selectEntries(ctx, root)
	if e != nil {
		return p.Resource{}, e
	}
	if e = os.MkdirAll(r.Config.StateDir, 0700); e != nil {
		return p.Resource{}, e
	}
	path := r.checkpointPath(run.ID)
	if stepID != "" {
		path = r.agentCheckpointPath(stepID)
	}
	file, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if e != nil {
		return p.Resource{}, e
	}
	defer file.Close()
	complete := false
	defer func() {
		if !complete {
			_ = os.Remove(path)
		}
	}()
	hash := sha256.New()
	if e = source.Write(io.MultiWriter(file, hash), root, entries); e != nil {
		return p.Resource{}, e
	}
	if e = file.Sync(); e != nil {
		return p.Resource{}, e
	}
	if dir, e := os.Open(r.Config.StateDir); e == nil {
		_ = dir.Sync()
		dir.Close()
	}
	complete = true
	digest := hex.EncodeToString(hash.Sum(nil))
	info, e := file.Stat()
	if e != nil {
		return p.Resource{}, e
	}
	meta := p.M{"leaseToken": p.S(run.Data, "leaseToken"), "repositoryId": repo, "commit": base}
	raw, _ := json.Marshal(meta)
	endpoint, kind := "runs/"+run.ID+"/checkpoint", "workspace_checkpoint"
	headers := map[string]string{"Content-Type": "application/x-tar", "X-IS-Checkpoint": base64.StdEncoding.EncodeToString(raw)}
	if stepID != "" {
		endpoint, kind = "agent-steps/"+stepID+"/checkpoint", "agent_checkpoint"
		headers = map[string]string{"Content-Type": "application/x-tar", "X-IS-Lease": p.S(run.Data, "leaseToken")}
	}
	for attempt := 0; attempt < 3; attempt++ {
		if _, e = file.Seek(0, io.SeekStart); e != nil {
			return p.Resource{}, e
		}
		response, err := r.Client.requestHeaders(ctx, "POST", endpoint, struct{ io.Reader }{file}, headers)
		e = err
		if e == nil {
			var saved p.Resource
			e = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&saved)
			response.Body.Close()
			if e == nil {
				if saved.Kind != kind || saved.Status != "saved" || p.S(saved.Data, "agentStepId") != stepID || p.S(saved.Data, "runId") != run.ID || p.S(saved.Data, "digest") != digest || p.N(saved.Data, "size") != float64(info.Size()) || p.S(p.Obj(saved.Data["source"]), "repositoryId") != repo || p.S(p.Obj(saved.Data["source"]), "commit") != base {
					return p.Resource{}, fmt.Errorf("checkpoint response differs from exact workspace; local archive retained in %s", path)
				}
				return saved, nil
			}
		}
		var api *APIError
		if ctx.Err() != nil || errors.As(e, &api) && api.Status < 500 {
			break
		}
	}
	return p.Resource{}, fmt.Errorf("workspace save requires reconciliation; local archive retained in %s: %w", path, e)
}

func (r *Runner) restoreWorkspaceCheckpoint(ctx context.Context, run p.Resource, state *sourceWorkspace, root string) error {
	return r.restorePrivateWorkspaceCheckpoint(ctx, run, state, root, false)
}

func (r *Runner) restorePrivateWorkspaceCheckpoint(ctx context.Context, run p.Resource, state *sourceWorkspace, root string, progress bool) error {
	if state == nil {
		return fmt.Errorf("workspace continuation has no source controller")
	}
	continuation := p.Obj(run.Data["messageContinuation"])
	endpoint := "runs/" + run.ID + "/checkpoint"
	if progress {
		continuation, endpoint = p.Obj(run.Data["agentResume"]), "runs/"+run.ID+"/agent-checkpoint"
	}
	restored, e := os.MkdirTemp(state.private, "restore-")
	if e != nil {
		return e
	}
	response, e := r.Client.requestHeaders(ctx, "GET", endpoint, nil, map[string]string{"X-IS-Lease": p.S(run.Data, "leaseToken")})
	if e != nil {
		return e
	}
	entries, digest, e := source.Extract(response.Body, restored)
	response.Body.Close()
	if e != nil {
		return fmt.Errorf("invalid checkpoint archive: %w", e)
	}
	if digest != p.S(continuation, "checkpointDigest") {
		return fmt.Errorf("checkpoint bytes differ from continuation")
	}
	// Replace the complete disposable tree: overlay extraction would resurrect
	// deletions. Original tracked paths survive in state.tracked; restored paths
	// become tracked too, including explicit modes on Windows.
	files, e := os.ReadDir(root)
	if e != nil {
		return e
	}
	for _, file := range files {
		if e = os.RemoveAll(filepath.Join(root, file.Name())); e != nil {
			return e
		}
	}
	files, e = os.ReadDir(restored)
	if e != nil {
		return e
	}
	for _, file := range files {
		if e = os.Rename(filepath.Join(restored, file.Name()), filepath.Join(root, file.Name())); e != nil {
			return e
		}
	}
	for _, entry := range entries {
		state.tracked[entry.Name], state.modes[entry.Name] = entry.Executable, entry.Executable
	}
	return nil
}

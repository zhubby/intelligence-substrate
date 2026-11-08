package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	p "github.com/intelligence-substrate/platform/internal/platform"
	"github.com/intelligence-substrate/platform/internal/source"
)

// The fingerprint covers the same canonical tar (including modes and deletions)
// as an ordinary full-source submission. It never includes controller state.
func (s *sourceWorkspace) fingerprint(ctx context.Context, root string, output io.Writer) (string, error) {
	entries, err := s.selectEntries(ctx, root)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	if err = source.Write(io.MultiWriter(output, hash), root, entries); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// A controlled stop is not finish. Save actual bytes outside the disposable
// workspace first; the original lease/baseline still governs remote registration.
// There is no model/command replay and no automatic source-adoption authority.
func (r *Runner) preserveStoppedSource(ctx context.Context, run p.Resource, state *sourceWorkspace, root, repo, base, branch, expected string) (p.M, error) {
	if state.inputDigest == "" {
		return nil, fmt.Errorf("original source fingerprint is unavailable")
	}
	local, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	dir, err := os.MkdirTemp(r.Config.StateDir, ".stopped-source-")
	if err != nil {
		return nil, err
	}
	retained := false
	defer func() {
		if !retained {
			_ = os.RemoveAll(dir)
		}
	}()
	archive := filepath.Join(dir, "source.tar")
	file, err := os.OpenFile(archive, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, err
	}
	digest, writeErr := state.fingerprint(local, root, file)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr != nil {
		return nil, writeErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if digest == state.inputDigest {
		return nil, nil
	}
	info, err := os.Stat(archive)
	if err != nil {
		return nil, err
	}
	metadata := p.M{"kind": "incomplete-agent-source", "incomplete": true, "savedAt": time.Now().UTC().Format(time.RFC3339Nano), "runId": run.ID, "taskId": p.S(run.Data, "taskId"), "workerId": p.S(run.Data, "workerId"), "projectId": run.ProjectID, "goalId": run.GoalID, "repositoryId": repo, "baseCommit": base, "branch": branch, "expectedHead": expected, "inputDigest": state.inputDigest, "digest": digest, "size": info.Size(), "requirementRevision": run.Data["requirementRevision"], "planId": run.Data["planId"], "sourceBindingDigest": p.S(p.Obj(p.Obj(run.Data["task"])["sourceBinding"]), "digest")}
	if state.integration != nil {
		metadata["integrationId"], metadata["resolutionDigest"] = state.integration.id, state.integration.digest
	}
	body, err := json.Marshal(metadata)
	if err != nil {
		return nil, err
	}
	if err = saveCompletion(filepath.Join(dir, "context.json"), append(body, '\n')); err != nil {
		return nil, err
	}
	if parent, err := os.Open(r.Config.StateDir); err == nil {
		_ = parent.Sync()
		_ = parent.Close()
	}
	retained = true
	saved := p.M{"archive": filepath.Join(filepath.Base(dir), "source.tar"), "digest": digest, "size": info.Size(), "status": "local_only", "incomplete": true}
	if ctx.Err() != nil {
		saved["synchronizationError"] = "original execution context ended; retained locally without extending its lease"
		return saved, nil
	}
	if state.commitTried {
		saved["synchronizationError"] = "final source submission already attempted; inspect its original operation without creating a second one"
		return saved, nil
	}
	checkpointRun := run
	checkpointRun.Title = "Incomplete source: " + run.Title
	input, err := os.Open(archive)
	if err != nil {
		saved["synchronizationError"] = err.Error()
		return saved, nil
	}
	defer input.Close()
	op, err := r.submitSourceArchive(ctx, checkpointRun, state, input, repo, base, branch, expected)
	if err != nil {
		saved["synchronizationError"] = err.Error()
		return saved, nil
	}
	if op.Kind != "source_commit" || op.Status != "committed" || p.S(op.Data, "runId") != run.ID || p.S(op.Data, "repositoryId") != repo || p.S(op.Data, "digest") != digest || !p.CommitPattern.MatchString(p.S(op.Data, "commit")) {
		saved["synchronizationError"] = "source acknowledgement does not identify the original committed candidate; inspect original operation"
		return saved, nil
	}
	saved["status"], saved["sourceOperationId"], saved["commit"] = "candidate_saved", op.ID, p.S(op.Data, "commit")
	return saved, nil
}

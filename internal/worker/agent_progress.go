package worker

import (
	"context"
	"fmt"
	"path/filepath"

	p "github.com/intelligence-substrate/platform/internal/platform"
)

func (r *Runner) agentCheckpointPath(id string) string {
	return filepath.Join(r.Config.StateDir, "agent_"+id+".tar")
}

func (r *Runner) prepareAgentProgress(ctx context.Context, run p.Resource, step int, action p.M, repo, base string) (p.Resource, error) {
	intent, e := r.Client.Command(ctx, "agent_steps.prepare", p.M{"runId": run.ID, "leaseToken": p.S(run.Data, "leaseToken"), "step": step, "action": action, "source": p.M{"repositoryId": repo, "commit": base}})
	if e != nil {
		return intent, e
	}
	if intent.Kind != "agent_step" || intent.Status != "prepared" || p.S(intent.Data, "runId") != run.ID || p.S(intent.Data, "taskId") != p.S(run.Data, "taskId") || p.N(intent.Data, "step") != float64(step) || p.S(intent.Data, "actionDigest") != p.Hash(action) || p.Hash(intent.Data["source"]) != p.Hash(p.M{"repositoryId": repo, "commit": base}) || !p.IDPattern.MatchString(intent.ID) {
		return intent, fmt.Errorf("action intent acknowledgement differs from original operation")
	}
	return intent, nil
}

func (r *Runner) confirmAgentProgress(ctx context.Context, run, intent p.Resource, state *sourceWorkspace, root, repo, base, observation string) error {
	checkpointID := ""
	if p.S(p.Obj(run.Data["task"]), "kind") != "analysis" {
		checkpoint, e := r.savePrivateWorkspaceCheckpoint(ctx, run, state, root, repo, base, intent.ID)
		if e != nil {
			return e
		}
		checkpointID = checkpoint.ID
	}
	input := p.M{"id": intent.ID, "runId": run.ID, "leaseToken": p.S(run.Data, "leaseToken"), "observation": observation, "checkpointId": checkpointID}
	confirmed, e := r.Client.Command(ctx, "agent_steps.complete", input)
	if e != nil {
		return e
	}
	if confirmed.ID != intent.ID || confirmed.Kind != "agent_step" || confirmed.Status != "completed" || p.S(confirmed.Data, "completionDigest") != p.Hash(input) || p.S(confirmed.Data, "actionDigest") != p.S(intent.Data, "actionDigest") || p.S(confirmed.Data, "checkpointId") != checkpointID {
		return fmt.Errorf("action completion acknowledgement differs from original result")
	}
	return nil
}

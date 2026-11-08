package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	p "github.com/intelligence-substrate/platform/internal/platform"
)

func (r *Runner) execRun(ctx context.Context, run p.Resource, dir string, argv []string, output io.Writer) (int, error) {
	if r.Config.Driver != "ax" {
		return r.exec(ctx, dir, argv, output)
	}
	ax := r.Config.AX
	ax, backend, close, e := ax.Open(ctx)
	if e != nil {
		return 1, fmt.Errorf("AX connection was not authenticated; no sandbox created: %w", e)
	}
	defer close()
	remote, e := r.Client.Command(ctx, "sandboxes.prepare", p.M{"runId": run.ID, "leaseToken": p.S(run.Data, "leaseToken"), "key": p.NewID("command"), "backend": backend, "command": argv})
	if e != nil {
		return 1, fmt.Errorf("AX sandbox preparation unconfirmed; command not issued: %w", e)
	}
	replayed, hasReplay := remote.Data["replayed"].(bool)
	if !hasReplay || replayed || remote.Kind != "sandbox" || remote.Status != "prepared" || remote.ProjectID != run.ProjectID || remote.GoalID != run.GoalID || p.S(remote.Data, "runId") != run.ID || p.S(remote.Data, "workerId") != p.S(run.Data, "workerId") || p.S(remote.Data, "name") != "is-"+strings.ReplaceAll(remote.ID, "_", "-") || p.S(remote.Data, "backendDigest") != p.Hash(backend) || p.S(remote.Data, "commandDigest") != p.Hash(argv) {
		return 1, fmt.Errorf("AX sandbox preparation did not authorize a new exact command; preserved for reconciliation")
	}
	exit, stopped, executeErr := ax.RunOwnedCommand(ctx, remote.ID, dir, argv, output)
	observation := "Command controller confirmed exact Task and Workspace cleanup, or issued no external creation request"
	if !stopped {
		observation = "Exact AX cleanup remains unconfirmed; inspect the original controller log and remote Task, Workspace and Actor before reconciliation"
	}
	ack, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	defer cancel()
	_, finishErr := r.Client.Command(ack, "sandboxes.finish", p.M{"id": remote.ID, "leaseToken": p.S(run.Data, "leaseToken"), "name": remote.Data["name"], "backendDigest": remote.Data["backendDigest"], "stopped": stopped, "observation": observation})
	if finishErr != nil {
		executeErr = errors.Join(executeErr, fmt.Errorf("AX cleanup receipt unconfirmed for %s: %w", remote.ID, finishErr))
	}
	if !stopped && executeErr == nil {
		executeErr = fmt.Errorf("AX sandbox %s still requires cleanup reconciliation", remote.ID)
	}
	if executeErr != nil && exit == 0 {
		exit = 1
	}
	return exit, executeErr
}

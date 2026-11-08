package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	p "github.com/intelligence-substrate/platform/internal/platform"
	"github.com/intelligence-substrate/platform/internal/source"
)

func (r *Runner) initializeBlueprint(ctx context.Context, run p.Resource, state *sourceWorkspace, dir, repo, base, branch, expected string, entries []source.Entry) (p.M, int, error) {
	task := p.Obj(run.Data["task"])
	snapshot := p.Obj(task["blueprintSeed"])
	data := p.Obj(snapshot["data"])
	if len(entries) != 0 || p.S(task, "kind") != "code" || p.S(task, "logicalId") != "blueprint-initialize" || len(p.Items(task["command"])) != 0 || p.S(task, "sourceMode") != "" || len(p.Obj(run.Data["messageContinuation"])) != 0 {
		return p.M{"blocked": true}, 1, fmt.Errorf("blueprint initialization requires an empty fixed source and the approved controller task")
	}
	body, err := json.Marshal(p.M{"leaseToken": p.S(run.Data, "leaseToken")})
	if err != nil {
		return nil, 1, err
	}
	response, err := r.Client.request(ctx, "POST", "runs/"+run.ID+"/blueprint", bytes.NewReader(body))
	if err != nil {
		return p.M{"blocked": true}, 1, err
	}
	seedEntries, digest, err := source.Extract(response.Body, dir)
	response.Body.Close()
	if err != nil {
		return p.M{"blocked": true}, 1, err
	}
	if digest != p.S(data, "digest") || len(seedEntries) != int(p.N(data, "fileCount")) {
		return p.M{"blocked": true}, 1, fmt.Errorf("blueprint archive differs from the approved frozen source")
	}
	for _, entry := range seedEntries {
		// Every frozen file is selected explicitly, even when its own ignore
		// rules would hide it. Windows also preserves the approved mode bits.
		state.tracked[entry.Name] = entry.Executable
		state.modes[entry.Name] = entry.Executable
	}
	op, err := r.commitSource(ctx, run, state, dir, repo, base, branch, expected)
	if err != nil {
		return p.M{"blocked": true}, 1, err
	}
	return p.M{"commit": p.S(op.Data, "commit"), "repositoryId": repo, "branch": branch, "sourceOperationId": op.ID,
		"blueprintId": p.S(snapshot, "id"), "blueprintDigest": p.S(data, "digest"), "source": "controller-blueprint"}, 0, nil
}

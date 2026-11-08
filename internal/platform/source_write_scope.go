package platform

import (
	"context"
	"strings"

	"github.com/intelligence-substrate/platform/internal/source"
)

type sourceWriteScopeViolation struct{ paths []string }

func (e *sourceWriteScopeViolation) Error() string {
	return "源码变更超出批准的 writePaths：" + strings.Join(e.paths, "、") + "。扩大范围需要批准新方案"
}

func (e *sourceWriteScopeViolation) Unwrap() error { return conflict(e.Error()) }

func taskWritePaths(data M) ([]string, error) {
	value, exists := data["writePaths"]
	if !exists {
		return nil, nil
	}
	if S(data, "kind") != "code" {
		return nil, invalid("只有开发工作项可以声明 writePaths")
	}
	paths, err := source.ParseWritePaths(value)
	if err != nil {
		return nil, invalid(err.Error())
	}
	return paths, nil
}

func sameWritePaths(a, b M) bool {
	av, aok := a["writePaths"]
	bv, bok := b["writePaths"]
	return aok == bok && Hash(av) == Hash(bv)
}

// Compare actual Git trees, including modes and both sides of renames. This is
// an output admission gate, not a filesystem sandbox for arbitrary commands.
func (s *Service) sourceWriteScope(ctx context.Context, repo Resource, task M, base, tree string) error {
	paths, err := taskWritePaths(task)
	if err != nil || paths == nil {
		return err
	}
	changed, err := s.rawGit(ctx, repo, "", nil, "--no-replace-objects", "diff-tree", "--no-commit-id", "--name-only", "-r", "-z", "--no-renames", base, tree, "--")
	if err != nil {
		return err
	}
	outside := []string{}
	for _, name := range strings.Split(string(changed), "\x00") {
		if name != "" && !source.AllowsWrite(paths, name) {
			outside = append(outside, name)
			if len(outside) == 8 {
				break
			}
		}
	}
	if len(outside) != 0 {
		return &sourceWriteScopeViolation{paths: outside}
	}
	return nil
}

func (s *Service) snapshotWriteScope(ctx context.Context, t *Tx, a Actor, repo Resource, in M, tree string) error {
	if a.Kind != "worker" {
		return nil
	}
	run, err := t.Get(S(in, "runId"))
	if err != nil {
		return err
	}
	task, err := t.Get(S(run.Data, "taskId"))
	if err != nil {
		return err
	}
	return s.sourceWriteScope(ctx, repo, task.Data, S(in, "baseCommit"), tree)
}

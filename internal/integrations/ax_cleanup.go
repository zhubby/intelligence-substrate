package integrations

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// The pinned CLI prints these complete list headers. Only a successful,
// well-formed scoped listing can prove absence; NotFound-like stderr cannot.
func (a AX) confirmStopped(ctx context.Context, name string) error {
	if a.Secure != nil || a.session != nil {
		return a.stoppedSecure(ctx, name)
	}
	var result error
	for _, kind := range []string{"tasks", "workspaces"} {
		body, e := a.call(ctx, "get", kind)
		if e != nil {
			result = errors.Join(result, e)
			continue
		}
		lines := strings.Split(body, "\n")
		header := "NAME ATESPACE PHASE ACTOR WORKER-IP AGE"
		if kind == "workspaces" {
			header = "NAME ATESPACE GIT-REPOS MCP-SERVERS"
		}
		fields := strings.Fields(lines[0])
		if strings.Join(fields, " ") != header {
			result = errors.Join(result, fmt.Errorf("AX %s cleanup listing is invalid", kind))
			continue
		}
		for _, line := range lines[1:] {
			row := strings.Fields(line)
			if len(row) != len(fields) || row[1] != a.Atespace {
				result = errors.Join(result, fmt.Errorf("AX %s cleanup listing has unexpected scope or shape", kind))
				break
			}
			if row[0] == name {
				result = errors.Join(result, fmt.Errorf("AX %s resource %s still exists", kind, name))
				break
			}
		}
	}
	return result
}

func (a AX) cleanupCommand(ctx context.Context, name string) error {
	deletion := a.Cancel(ctx, name)
	observation := a.confirmStopped(ctx, name)
	return errors.Join(deletion, observation)
}

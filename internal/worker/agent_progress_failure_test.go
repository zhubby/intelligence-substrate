package worker

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	p "github.com/intelligence-substrate/platform/internal/platform"
)

func TestAgentProgressLostAcknowledgementNeverReplaysUnknownAction(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("actual POSIX command fixture")
	}
	for _, mode := range []string{"prepare-lost", "checkpoint-unresolved", "complete-lost"} {
		t.Run(mode, func(t *testing.T) {
			f, _, _ := workspaceHarness(t, "code")
			runner, actor := workspaceRunner(t, f)
			marker := filepath.Join(t.TempDir(), "effect")
			calls := 0
			f.h.ModelCall = func(_ context.Context, _ p.Actor, _ p.M) (p.M, error) {
				calls++
				if calls > 1 {
					return nil, fmt.Errorf("unexpected inference after unknown acknowledgement")
				}
				return actionResponse(p.M{"action": "run", "command": []string{"/bin/sh", "-c", "printf once >> \"$1\"; printf saved > draft.txt", "fixture", marker}}), nil
			}
			lost := 0
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				intercept := mode == "prepare-lost" && strings.HasSuffix(req.URL.Path, "/commands/agent_steps.prepare") || mode == "complete-lost" && strings.HasSuffix(req.URL.Path, "/commands/agent_steps.complete") || mode == "checkpoint-unresolved" && strings.HasPrefix(req.URL.Path, "/api/v1/agent-steps/") && strings.HasSuffix(req.URL.Path, "/checkpoint")
				if intercept {
					response := httptest.NewRecorder()
					f.h.ServeHTTP(response, req)
					if response.Code != 200 {
						t.Error("fault fixture rejected operation", response.Code, response.Body.String())
					}
					lost++
					w.WriteHeader(503)
					return
				}
				f.h.ServeHTTP(w, req)
			}))
			defer proxy.Close()
			runner.Client.URL = proxy.URL
			if worked, e := runner.RunOnce(context.Background()); !worked || e != nil {
				t.Fatal(worked, e)
			}
			if calls != 1 || lost < 1 {
				t.Fatal("retried model or missed fault", calls, lost)
			}
			actual, e := os.ReadFile(marker)
			if mode == "prepare-lost" {
				if !os.IsNotExist(e) {
					t.Fatal("tool ran without acknowledged intent", string(actual), e)
				}
			} else if e != nil || string(actual) != "once" {
				t.Fatal("tool was repeated", string(actual), e)
			}
			steps, _ := f.svc.Store.List(f.project.ID, "agent_step", f.goal.ID)
			ops, _ := f.svc.Store.List(f.project.ID, "source_commit", f.goal.ID)
			if len(steps) != 1 || len(ops) != 0 {
				t.Fatal("lost acknowledgement duplicated or adopted source", steps, ops)
			}
			task := workspaceTask(t, f, "left")
			if mode != "complete-lost" {
				if steps[0].Status != "prepared" || p.S(task.Data, "agentProgressId") != "" {
					t.Fatal("unconfirmed checkpoint became restorable", steps[0])
				}
				if _, e = f.svc.Command(context.Background(), f.owner, "tasks.retry", p.M{"id": task.ID}); e == nil {
					t.Fatal("unknown operation retried")
				}
				return
			}
			if steps[0].Status != "completed" || p.S(task.Data, "agentProgressId") != steps[0].ID {
				t.Fatal("lost completed response erased committed progress")
			}
			f.command(t, f.owner, "workers.disable", p.M{"id": actor.WorkerID})
			f.command(t, f.owner, "tasks.retry", p.M{"id": task.ID})
			f.h.ModelCall = func(_ context.Context, _ p.Actor, _ p.M) (p.M, error) {
				return actionResponse(p.M{"action": "finish", "summary": "Continue from exact confirmed source"}), nil
			}
			replacement, _ := workspaceRunner(t, f)
			if worked, e := replacement.RunOnce(context.Background()); !worked || e != nil {
				t.Fatal(worked, e)
			}
			actual, _ = os.ReadFile(marker)
			if workspaceTask(t, f, "left").Status != "succeeded" || string(actual) != "once" {
				t.Fatal("completed action was replayed on replacement")
			}
		})
	}
}

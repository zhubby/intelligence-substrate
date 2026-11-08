package worker

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	p "github.com/intelligence-substrate/platform/internal/platform"
)

func TestWorkspaceWaitLostAcknowledgementsNeverAdoptOrRerunSource(t *testing.T) {
	for _, mode := range []string{"upload-lost-once", "wait-lost", "upload-unresolved"} {
		t.Run(mode, func(t *testing.T) {
			f, _, _ := workspaceHarness(t, "code")
			runner, _ := workspaceRunner(t, f)
			calls, uploads := 0, 0
			loseWait := true
			var original []byte
			f.h.ModelCall = func(_ context.Context, a p.Actor, _ p.M) (p.M, error) {
				calls++
				switch calls {
				case 1:
					return actionResponse(p.M{"action": "write", "path": "draft.txt", "content": "Durable saved code"}), nil
				case 2:
					return actionResponse(p.M{"action": "message", "recipient": f.owner.ID, "type": "request", "payload": p.M{"question": "Confirm saved source?"}}), nil
				case 3:
					return actionResponse(workspaceWaitAction(t, f, a)), nil
				}
				return nil, fmt.Errorf("checkpoint retry resent inference")
			}
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "POST" && strings.HasPrefix(r.URL.Path, "/api/v1/runs/") && strings.HasSuffix(r.URL.Path, "/checkpoint") {
					uploads++
					body, _ := io.ReadAll(r.Body)
					r.Body.Close()
					r.Body = io.NopCloser(bytes.NewReader(body))
					if uploads == 1 {
						original = append([]byte(nil), body...)
					} else if !bytes.Equal(body, original) {
						t.Error("retry changed raw checkpoint bytes")
					}
					response := httptest.NewRecorder()
					f.h.ServeHTTP(response, r)
					if response.Code != 200 {
						t.Error("fixture registration failed", response.Code, response.Body.String())
					}
					if mode == "upload-unresolved" || mode == "upload-lost-once" && uploads == 1 {
						w.WriteHeader(503)
						return
					}
					for k, v := range response.Header() {
						w.Header()[k] = v
					}
					w.WriteHeader(response.Code)
					_, _ = w.Write(response.Body.Bytes())
					return
				}
				if mode == "wait-lost" && loseWait && strings.HasSuffix(r.URL.Path, "/commands/tasks.wait") {
					response := httptest.NewRecorder()
					f.h.ServeHTTP(response, r)
					if response.Code != 200 {
						t.Error("fixture wait failed", response.Body.String())
					}
					w.WriteHeader(503)
					return
				}
				f.h.ServeHTTP(w, r)
			}))
			defer proxy.Close()
			runner.Client.URL = proxy.URL
			worked, err := runner.RunOnce(context.Background())
			if !worked || mode == "wait-lost" && err == nil || mode != "wait-lost" && err != nil {
				t.Fatal(worked, err)
			}
			if calls != 3 {
				t.Fatal("inference was repeated", calls)
			}
			checkpoints, _ := f.svc.Store.List(f.project.ID, "workspace_checkpoint", f.goal.ID)
			if len(checkpoints) != 1 {
				t.Fatal("upload duplicated state", len(checkpoints))
			}
			ops, _ := f.svc.Store.List(f.project.ID, "source_commit", f.goal.ID)
			if len(ops) != 0 {
				t.Fatal("suspension adopted stopped source", ops)
			}
			saved, _ := filepath.Glob(filepath.Join(runner.Config.StateDir, "workspace_*.tar"))
			if mode == "upload-unresolved" {
				if uploads != 3 || len(saved) != 1 || workspaceTask(t, f, "left").Status != "blocked" {
					t.Fatal("uncertain checkpoint lost review state", uploads, saved, workspaceTask(t, f, "left"))
				}
				actual, e := os.ReadFile(saved[0])
				if e != nil || !bytes.Equal(actual, original) {
					t.Fatal("local exact archive lost", e)
				}
			} else if mode == "wait-lost" {
				if len(saved) != 1 || workspaceTask(t, f, "left").Status != "waiting_message" {
					t.Fatal("lost wait response lost durable archive", saved)
				}
				loseWait = false
				if e := runner.flush(context.Background()); e != nil {
					t.Fatal(e)
				}
				saved, _ = filepath.Glob(filepath.Join(runner.Config.StateDir, "workspace_*.tar"))
				if len(saved) != 0 || calls != 3 {
					t.Fatal("exact wait journal replay failed", saved, calls)
				}
			} else if uploads != 2 || len(saved) != 0 || workspaceTask(t, f, "left").Status != "waiting_message" {
				t.Fatal("exact upload replay failed", uploads, saved)
			}
		})
	}
}

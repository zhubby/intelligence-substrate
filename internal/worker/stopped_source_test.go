package worker

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/intelligence-substrate/platform/internal/httpapi"
	"github.com/intelligence-substrate/platform/internal/integrations"
	p "github.com/intelligence-substrate/platform/internal/platform"
)

type stoppedSourceFixture struct {
	t                   *testing.T
	store               *p.Store
	svc                 *p.Service
	handler             *httpapi.Handler
	owner               p.Actor
	project, goal, repo p.Resource
	server              *httptest.Server
}

func newStoppedSourceFixture(t *testing.T, kind string) *stoppedSourceFixture {
	t.Helper()
	store, err := p.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	svc := p.NewService(store)
	handler := httpapi.New(svc, false)
	if err = handler.Auth.Bootstrap("stopped@example.test", "stopped-source-password-123"); err != nil {
		t.Fatal(err)
	}
	identity, err := handler.Auth.IdentityByEmail("stopped@example.test")
	if err != nil {
		t.Fatal(err)
	}
	f := &stoppedSourceFixture{t: t, store: store, svc: svc, handler: handler, owner: p.Actor{ID: identity.ID, Kind: "human", Admin: true}}
	f.project = f.command(f.owner, "projects.create", p.M{"title": "Stopped source acceptance"})
	f.goal = f.command(f.owner, "goals.create", p.M{"projectId": f.project.ID, "title": "Keep generated source", "outcome": "Preserve actual output after budget stop, then verify independently", "targets": []any{"web"}, "criteria": []any{p.M{"id": "saved", "text": "Package contains the preserved source", "method": "automated"}}, "budget": 1})
	f.command(f.owner, "goals.approve", p.M{"id": f.goal.ID})
	f.repo = f.command(f.owner, "repositories.create", p.M{"projectId": f.project.ID, "title": "source"})
	f.repo = f.command(f.owner, "repositories.commit", p.M{"id": f.repo.ID, "branch": "seed/input", "message": "Known input", "files": p.M{"remove.txt": "remove later\n", "launch.sh": "#!/bin/sh\n"}})
	tasks := []any{p.M{"id": "code", "title": "Generate source then exhaust the original work budget", "kind": kind, "role": "developer", "runtime": runtime.GOOS, "repositoryId": f.repo.ID, "commit": p.S(f.repo.Data, "lastCommit"), "branch": "feature/stopped", "budgetCents": 1}}
	if kind == "code" {
		tasks = append(tasks, p.M{"id": "build", "title": "Build the adopted source", "kind": "build", "role": "developer", "runtime": runtime.GOOS, "repositoryId": f.repo.ID, "dependsOn": []any{"code"}, "command": []any{"/bin/sh", "-c", "cat source.txt > package.txt"}, "outputPath": "package.txt", "target": "web", "budgetCents": 20})
	}
	plan := f.command(f.owner, "plans.propose", p.M{"goalId": f.goal.ID, "summary": "Preserve incomplete source without claiming success", "tasks": tasks})
	f.command(f.owner, "plans.approve", p.M{"id": plan.ID})
	f.server = httptest.NewServer(handler)
	t.Cleanup(f.server.Close)
	return f
}

func (f *stoppedSourceFixture) command(actor p.Actor, name string, input p.M) p.Resource {
	f.t.Helper()
	r, err := f.svc.Command(context.Background(), actor, name, input)
	if err != nil {
		f.t.Fatalf("%s: %v", name, err)
	}
	return r
}

func (f *stoppedSourceFixture) runner(role string, capabilities []any, quality bool) (*Runner, p.Actor) {
	f.t.Helper()
	w := f.command(f.owner, "workers.register", p.M{"projectId": f.project.ID, "title": role + " node", "runtime": runtime.GOOS, "roles": []any{role}, "capabilities": capabilities, "trustedQuality": quality})
	token, _, err := f.handler.Auth.IssueToken(context.Background(), f.owner, p.M{"projectId": f.project.ID, "workerId": w.ID})
	if err != nil {
		f.t.Fatal(err)
	}
	client, err := NewClient(f.server.URL, token, true)
	if err != nil {
		f.t.Fatal(err)
	}
	return &Runner{Client: client, Config: Config{Driver: "native", AllowNative: true, StateDir: f.t.TempDir()}}, p.Actor{ID: "worker:" + w.ID, Kind: "worker", WorkerID: w.ID, ProjectID: f.project.ID, Roles: []string{role}}
}

func (f *stoppedSourceFixture) list(kind string) []p.Resource {
	f.t.Helper()
	rows, err := f.store.List(f.project.ID, kind, f.goal.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	return rows
}

// Removing abnormal-exit preservation loses this exact source despite one paid
// (fixture-provider) write action; real gateway accounting rejects the next call.
func TestStoppedAgentSourceBudgetStopPreservesCandidateAndRequiresHumanAdoption(t *testing.T) {
	f := newStoppedSourceFixture(t, "code")
	calls := 0
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = json.NewEncoder(w).Encode(p.M{"id": "measured-write", "model": "fixture-model", "choices": []any{p.M{"finish_reason": "stop", "message": p.M{"content": `{"action":"write","path":"source.txt","content":"saved-before-stop\n"}`}}}, "usage": p.M{"prompt_tokens": 10, "completion_tokens": 32}})
	}))
	defer provider.Close()
	model := &integrations.Model{Service: f.svc, BaseURL: provider.URL, Name: "fixture-model", InputUSD: 0, OutputUSD: 1, DefaultMaxTokens: 64, Client: provider.Client()}
	f.handler.ModelCall = model.Chat
	dev, actor := f.runner("developer", []any{"code", "build"}, false)
	if worked, err := dev.RunOnce(context.Background()); err != nil || !worked {
		t.Fatalf("budget-stop cycle worked=%v: %v", worked, err)
	}
	runs := f.list("run")
	if calls != 1 || len(runs) != 1 || runs[0].Status != "blocked" || p.N(runs[0].Data, "exitCode") != 1 || !strings.Contains(p.S(p.Obj(runs[0].Data["result"]), "reason"), "预算") {
		t.Fatalf("did not exercise the real gateway budget stop: calls=%d runs=%+v", calls, runs)
	}
	charges := f.list("charge")
	if len(charges) != 1 || charges[0].Status != "settled" || p.N(charges[0].Data, "chargedCents") != 1 || len(f.list("evidence")) != 0 {
		t.Fatal("source preservation changed accounting or fabricated quality")
	}
	ops := f.list("source_commit")
	if len(ops) != 1 || ops[0].Status != "committed" || p.S(ops[0].Data, "runId") != runs[0].ID {
		t.Fatalf("generated source was discarded before finish; no persisted stopped candidate: %+v", ops)
	}
	stopped := p.Obj(p.Obj(runs[0].Data["result"])["stoppedSource"])
	if p.S(stopped, "sourceOperationId") != ops[0].ID || p.S(stopped, "status") != "candidate_saved" || !p.B(stopped, "incomplete") || p.S(stopped, "digest") != p.S(ops[0].Data, "digest") {
		t.Fatal("stopped source was presented as completed work", stopped)
	}
	archivePath := filepath.Join(dev.Config.StateDir, p.S(stopped, "archive"))
	body, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal("private source archive was not retained", err)
	}
	digest := sha256.Sum256(body)
	if hex.EncodeToString(digest[:]) != p.S(stopped, "digest") {
		t.Fatal("private source digest mismatch")
	}
	file, err := os.Open(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(file)
	found := false
	for {
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
		if h.Name == "source.txt" {
			got, e := io.ReadAll(tr)
			if e != nil || string(got) != "saved-before-stop\n" {
				t.Fatal("private source bytes changed", e)
			}
			found = true
		}
	}
	file.Close()
	if !found {
		t.Fatal("private archive omitted the generated file")
	}
	inspect, err := f.svc.InspectSourceOperation(context.Background(), f.owner, ops[0].ID)
	if err != nil || !p.B(inspect, "canConfirm") {
		t.Fatal("owner cannot inspect the stopped candidate", err, inspect)
	}
	input := p.M{"id": ops[0].ID, "key": "adopt-stopped-source", "commit": p.S(ops[0].Data, "commit"), "observedHead": p.S(inspect, "observedHead"), "decision": "confirm", "reason": "Reviewed the exact generated file; continue with independent build and QA"}
	if _, err = f.svc.Command(context.Background(), actor, "sources.reconcile", input); err == nil {
		t.Fatal("the producing worker adopted its own interrupted output")
	}
	if worked, err := dev.RunOnce(context.Background()); err != nil || worked {
		t.Fatal("dependent build ran before human adoption", worked, err)
	}
	f.command(f.owner, "sources.reconcile", input)
	recovered, err := f.store.Get(runs[0].ID)
	if err != nil || recovered.Status != "recovered" || p.N(recovered.Data, "exitCode") != 1 || p.S(recovered.Data, "workerId") != actor.WorkerID {
		t.Fatal("adoption lost the original error, exit or producer", err, recovered)
	}
	if worked, err := dev.RunOnce(context.Background()); err != nil || !worked {
		t.Fatal("adopted source did not build", worked, err)
	}
	artifacts := f.list("artifact")
	if len(artifacts) != 1 || p.S(artifacts[0].Data, "commit") != p.S(ops[0].Data, "commit") {
		t.Fatal("build did not use the exact adopted candidate", artifacts)
	}
	pipeline := f.command(f.owner, "pipelines.create", p.M{"projectId": f.project.ID, "title": "Read the actual preserved package", "runtime": runtime.GOOS, "method": "automated", "command": []any{"/bin/sh", "-c", "test \"$(cat \"$IS_ARTIFACT_PATH\")\" = saved-before-stop"}})
	f.command(f.owner, "pipelines.run", p.M{"pipelineId": pipeline.ID, "artifactId": artifacts[0].ID, "criteria": []any{"saved"}})
	qa, _ := f.runner("quality", []any{"test"}, true)
	if worked, err := qa.RunOnce(context.Background()); err != nil || !worked {
		t.Fatal("independent QA failed", worked, err)
	}
	evidence := f.list("evidence")
	if len(evidence) != 1 || evidence[0].Status != "passed" || p.S(evidence[0].Data, "digest") != p.S(artifacts[0].Data, "digest") {
		t.Fatal("actual independent verification was not recorded", evidence)
	}
	if _, err = f.svc.Command(context.Background(), f.owner, "releases.create", p.M{"goalId": f.goal.ID, "version": "0.1.0", "commit": p.S(artifacts[0].Data, "commit"), "artifactIds": []any{artifacts[0].ID}}); err == nil || !strings.Contains(err.Error(), "目标环境") {
		t.Fatal("source recovery bypassed the actual browser gate", err)
	}
}

func TestStoppedAgentSourceWithoutChangesDoesNotCreateCandidates(t *testing.T) {
	for _, kind := range []string{"code", "analysis"} {
		t.Run(kind, func(t *testing.T) {
			f := newStoppedSourceFixture(t, kind)
			calls := 0
			f.handler.ModelCall = func(_ context.Context, _ p.Actor, _ p.M) (p.M, error) {
				calls++
				if calls == 1 {
					action := `{"action":"read","path":"launch.sh"}`
					if kind == "analysis" {
						action = `{"action":"write","path":"forbidden.txt","content":"not allowed"}`
					}
					return p.M{"choices": []any{p.M{"message": p.M{"content": action}}}}, nil
				}
				return nil, &p.Fault{Code: "budget_exceeded", Message: "fixture budget stop", Status: 409}
			}
			runner, _ := f.runner("developer", []any{kind}, false)
			if worked, err := runner.RunOnce(context.Background()); err != nil || !worked {
				t.Fatal(worked, err)
			}
			local, err := filepath.Glob(filepath.Join(runner.Config.StateDir, ".stopped-source-*"))
			if err != nil || len(local) != 0 || len(f.list("source_commit")) != 0 {
				t.Fatal("unchanged or read-only work created a misleading source candidate", err, local)
			}
		})
	}
}

func TestStoppedAgentSourceKeepsToolBytesDeletionModesAndPrivateProvenance(t *testing.T) {
	f := newStoppedSourceFixture(t, "code")
	calls, lease := 0, ""
	f.handler.ModelCall = func(_ context.Context, _ p.Actor, in p.M) (p.M, error) {
		calls++
		lease = p.S(in, "leaseToken")
		if calls == 1 {
			action, _ := json.Marshal(p.M{"action": "run", "command": []any{"python3", "-c", "from pathlib import Path; import os; Path('payload.bin').write_bytes(bytes([0,255,128,1])); Path('remove.txt').unlink(); Path('launch.sh').write_text('#!/bin/sh\\necho kept\\n'); os.chmod('launch.sh', 0o700); Path('.intelligence-artifact').write_text('controller-artifact-must-stay-out')"}})
			return p.M{"choices": []any{p.M{"message": p.M{"content": string(action), "reasoning_content": "opaque-reasoning-must-not-be-a-source-checkpoint"}}}}, nil
		}
		return nil, &p.Fault{Code: "model_failed", Message: "provider result unconfirmed", Status: 502}
	}
	runner, _ := f.runner("developer", []any{"code"}, false)
	if worked, err := runner.RunOnce(context.Background()); err != nil || !worked {
		t.Fatal(worked, err)
	}
	ops := f.list("source_commit")
	if len(ops) != 1 {
		t.Fatal("tool-generated source was discarded", ops)
	}
	git := func(args ...string) string {
		t.Helper()
		out, err := f.svc.Git(f.repo, args...)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	commit := p.S(ops[0].Data, "commit")
	if !bytes.Equal([]byte(git("show", commit+":payload.bin")), []byte{0, 255, 128, 1}) || !strings.HasPrefix(git("ls-tree", commit, "launch.sh"), "100755 ") {
		t.Fatal("binary or executable mode changed during stop preservation")
	}
	if _, err := f.svc.Git(f.repo, "cat-file", "-e", commit+":remove.txt"); err == nil {
		t.Fatal("deleted source reappeared")
	}
	if _, err := f.svc.Git(f.repo, "cat-file", "-e", commit+":.intelligence-artifact"); err == nil {
		t.Fatal("quality artifact entered the source candidate")
	}
	runs := f.list("run")
	saved := p.Obj(p.Obj(runs[0].Data["result"])["stoppedSource"])
	dir := filepath.Join(runner.Config.StateDir, filepath.Dir(p.S(saved, "archive")))
	for _, name := range []string{"source.tar", "context.json"} {
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || bytes.Contains(body, []byte(runner.Client.Token)) || bytes.Contains(body, []byte(lease)) || bytes.Contains(body, []byte("opaque-reasoning-must-not-be-a-source-checkpoint")) {
			t.Fatal("private checkpoint missing or included controller/model secrets", name, err)
		}
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil || info.Mode().Perm()&0077 != 0 {
			t.Fatal("private source checkpoint was publicly readable", name, err)
		}
	}
	metadata, err := os.ReadFile(filepath.Join(dir, "context.json"))
	var origin p.M
	if err != nil || json.Unmarshal(metadata, &origin) != nil || p.S(origin, "runId") != runs[0].ID || p.S(origin, "baseCommit") != p.S(f.repo.Data, "lastCommit") || p.S(origin, "digest") != p.S(ops[0].Data, "digest") {
		t.Fatal("retained source has no accurate durable provenance", err, origin)
	}
}

func TestStoppedAgentSourceRetainsLocalCopyOnRejectedOrUnconfirmedUpload(t *testing.T) {
	for _, mode := range []string{"rejected-upload", "lost-ack", "final-commit-lost-ack", "cancelled", "changed-baseline"} {
		t.Run(mode, func(t *testing.T) {
			f := newStoppedSourceFixture(t, "code")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls, uploads := 0, 0
			f.handler.ModelCall = func(_ context.Context, _ p.Actor, _ p.M) (p.M, error) {
				calls++
				if calls == 1 {
					return p.M{"choices": []any{p.M{"message": p.M{"content": `{"action":"write","path":"source.txt","content":"retain locally\n"}`}}}}, nil
				}
				if mode == "final-commit-lost-ack" {
					return p.M{"choices": []any{p.M{"message": p.M{"content": `{"action":"finish","summary":"Source ready for independent validation"}`}}}}, nil
				}
				if mode == "cancelled" {
					cancel()
				}
				if mode == "changed-baseline" {
					if _, err := f.svc.Command(context.Background(), f.owner, "goals.revise", p.M{"id": f.goal.ID, "reason": "Baseline changed during development", "outcome": "A different approved outcome is required"}); err != nil {
						t.Error(err)
					}
				}
				return nil, &p.Fault{Code: "budget_exceeded", Message: "budget stop", Status: 409}
			}
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				if strings.HasSuffix(request.URL.Path, "/snapshot") {
					uploads++
					if mode == "lost-ack" || mode == "final-commit-lost-ack" {
						recorder := httptest.NewRecorder()
						f.handler.ServeHTTP(recorder, request)
						if recorder.Code != 200 {
							t.Errorf("original snapshot was not accepted: %d %s", recorder.Code, recorder.Body.String())
						}
						w.WriteHeader(503)
						_, _ = w.Write([]byte(`{"error":{"code":"ack_lost","message":"source acknowledgement unavailable"}}`))
						return
					}
					if mode == "rejected-upload" {
						w.WriteHeader(403)
						_, _ = w.Write([]byte(`{"error":{"code":"forbidden","message":"snapshot not authorized"}}`))
						return
					}
				}
				f.handler.ServeHTTP(w, request)
			}))
			defer proxy.Close()
			runner, _ := f.runner("developer", []any{"code"}, false)
			runner.Client.URL = proxy.URL
			worked, err := runner.RunOnce(ctx)
			if !worked || mode != "changed-baseline" && err != nil {
				t.Fatal("stopped cycle did not preserve its completion", worked, err)
			}
			files, err := filepath.Glob(filepath.Join(runner.Config.StateDir, ".stopped-source-*", "source.tar"))
			if err != nil || len(files) != 1 {
				t.Fatal("unconfirmed source has no private retained copy", files, err)
			}
			metadata, err := os.ReadFile(filepath.Join(filepath.Dir(files[0]), "context.json"))
			if err != nil || !bytes.Contains(metadata, []byte(`"incomplete":true`)) {
				t.Fatal("retained source falsely presented as completed", err)
			}
			ops := f.list("source_commit")
			wantOps, wantUploads := 0, 1
			if mode == "lost-ack" || mode == "final-commit-lost-ack" {
				wantOps, wantUploads = 1, 3
			}
			if mode == "cancelled" {
				wantUploads = 0
			}
			if len(ops) != wantOps || uploads != wantUploads {
				t.Fatalf("lease/context bypass or duplicate source operation: ops=%d uploads=%d", len(ops), uploads)
			}
			for _, run := range f.list("run") {
				if run.Status == "succeeded" || run.Status == "recovered" {
					t.Fatal("unknown/invalid source registration became completed work")
				}
				if mode != "changed-baseline" {
					saved := p.Obj(p.Obj(run.Data["result"])["stoppedSource"])
					if p.S(saved, "status") != "local_only" || p.S(saved, "sourceOperationId") != "" || p.S(saved, "synchronizationError") == "" {
						t.Fatal("unacknowledged upload falsely claimed a saved candidate", saved)
					}
				}
			}
		})
	}
}

func TestStoppedAgentSourceRejectsUnsafeOrOversizeOutputWithoutClaimingRetention(t *testing.T) {
	for _, mode := range []string{"symlink", "oversize"} {
		t.Run(mode, func(t *testing.T) {
			f := newStoppedSourceFixture(t, "code")
			calls := 0
			f.handler.ModelCall = func(_ context.Context, _ p.Actor, _ p.M) (p.M, error) {
				calls++
				if calls == 1 {
					program := "import os; os.symlink('/etc/passwd','unsafe-link')"
					if mode == "oversize" {
						program = "f=open('oversized.bin','wb'); f.truncate(64*1024*1024+1); f.close()"
					}
					action, _ := json.Marshal(p.M{"action": "run", "command": []any{"python3", "-c", program}})
					return p.M{"choices": []any{p.M{"message": p.M{"content": string(action)}}}}, nil
				}
				return nil, &p.Fault{Code: "model_failed", Message: "provider result unavailable", Status: 502}
			}
			runner, _ := f.runner("developer", []any{"code"}, false)
			if worked, err := runner.RunOnce(context.Background()); err != nil || !worked {
				t.Fatal(worked, err)
			}
			for _, run := range f.list("run") {
				result := p.Obj(run.Data["result"])
				if result["stoppedSource"] != nil || p.S(result, "reason") == "" || run.Status == "succeeded" {
					t.Fatal("unsafe/incomplete bytes claimed to be recoverable source", result)
				}
			}
			steps := f.list("agent_step")
			if calls != 1 || len(steps) != 1 || steps[0].Status != "prepared" || len(f.list("agent_checkpoint")) != 0 {
				t.Fatal("unsafe output lost its unconfirmed-action fence", calls, steps)
			}
			local, _ := filepath.Glob(filepath.Join(runner.Config.StateDir, ".stopped-source-*"))
			if len(local) != 0 || len(f.list("source_commit")) != 0 {
				t.Fatal("unsafe output produced a retained or remote source snapshot")
			}
		})
	}
}

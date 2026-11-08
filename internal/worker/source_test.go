package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/intelligence-substrate/platform/internal/httpapi"
	p "github.com/intelligence-substrate/platform/internal/platform"
	"github.com/intelligence-substrate/platform/internal/source"
)

func TestWorkspaceSelectsTrackedIgnoredAndToolGeneratedFilesAfterReplacement(t *testing.T) {
	dir := t.TempDir()
	state, e := newSourceWorkspace([]source.Entry{{Name: "tracked.txt"}, {Name: "deleted.txt"}, {Name: "bin/start.sh", Executable: true}})
	if e != nil {
		t.Fatal(e)
	}
	defer state.close()
	// AX can replace the entire guest source tree; trusted metadata must survive.
	if e = os.RemoveAll(dir); e != nil {
		t.Fatal(e)
	}
	if e = os.MkdirAll(filepath.Join(dir, "bin"), 0700); e != nil {
		t.Fatal(e)
	}
	for name, body := range map[string]string{".gitignore": "tracked.txt\ncache/\n", "tracked.txt": "updated", "bin/start.sh": "#!/bin/sh\n", "generated.txt": "tool output", ".intelligence-artifact": "not source", "cache/download": "ignored"} {
		path := filepath.Join(dir, name)
		if e = os.MkdirAll(filepath.Dir(path), 0700); e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(path, []byte(body), 0600); e != nil {
			t.Fatal(e)
		}
	}
	entries, e := state.selectEntries(context.Background(), dir)
	if e != nil {
		t.Fatal(e)
	}
	names := []string{}
	for _, entry := range entries {
		names = append(names, entry.Name)
	}
	if strings.Join(names, ",") != ".gitignore,bin/start.sh,generated.txt,tracked.txt" {
		t.Fatalf("wrong source selection: %v", names)
	}
	if !state.executable("bin/start.sh", false, true) || state.executable("generated.txt", true, true) {
		t.Fatal("Windows inferred unrepresentable Git mode")
	}
	state.modes["bin/start.sh"] = false
	state.modes["generated.txt"] = true
	if state.executable("bin/start.sh", true, true) || !state.executable("generated.txt", false, true) {
		t.Fatal("explicit mode override lost")
	}
	if runtime.GOOS != "windows" {
		if e = os.Symlink("/etc/passwd", filepath.Join(dir, "bad-link")); e != nil {
			t.Fatal(e)
		}
		if _, e = state.selectEntries(context.Background(), dir); e == nil {
			t.Fatal("selected source symlink was accepted")
		}
	}
}

func TestSnapshotUploadReplaysExactBytesAfterLostAcknowledgement(t *testing.T) {
	for _, status := range []int{500, 409} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			dir := t.TempDir()
			if e := os.WriteFile(filepath.Join(dir, "file.bin"), []byte{0, 255, 128}, 0600); e != nil {
				t.Fatal(e)
			}
			state, e := newSourceWorkspace(nil)
			if e != nil {
				t.Fatal(e)
			}
			defer state.close()
			calls := 0
			var original []byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				calls++
				body, e := io.ReadAll(req.Body)
				if e != nil {
					t.Error(e)
				}
				if calls == 1 {
					original = body
				} else if !bytes.Equal(body, original) {
					t.Error("retry changed source archive")
				}
				if req.URL.RawQuery != "" || req.Header.Get("X-IS-Source") == "" || req.Header.Get("Content-Type") != "application/x-tar" {
					t.Error("snapshot metadata or credentials entered wrong transport")
				}
				if calls < 3 {
					w.WriteHeader(status)
					_, _ = w.Write([]byte(`{"error":{"code":"injected","message":"injected acknowledgement failure"}}`))
					return
				}
				_ = json.NewEncoder(w).Encode(p.Resource{ID: "source-operation", Status: "committed", Data: p.M{"commit": strings.Repeat("a", 40)}})
			}))
			defer server.Close()
			client, e := NewClient(server.URL, "test-token", true)
			if e != nil {
				t.Fatal(e)
			}
			runner := &Runner{Client: client}
			op, e := runner.commitSource(context.Background(), p.Resource{ID: "run-test", Title: "Commit source", Data: p.M{"leaseToken": "test-lease"}}, state, dir, "repo-test", strings.Repeat("b", 40), "feature", "")
			if status == 500 {
				if e != nil || calls != 3 || op.Status != "committed" {
					t.Fatalf("exact retry failed calls=%d: %v", calls, e)
				}
			} else if e == nil || calls != 1 {
				t.Fatalf("rejected lease was retried: %d %v", calls, e)
			}
		})
	}
}

func TestWorkspaceWriteDeleteRespectModesAndRegularFileBoundary(t *testing.T) {
	root := t.TempDir()
	state, e := newSourceWorkspace(nil)
	if e != nil {
		t.Fatal(e)
	}
	defer state.close()
	if e = state.write(root, p.M{"path": "bin/run.sh", "content": "#!/bin/sh\n", "executable": true}); e != nil {
		t.Fatal(e)
	}
	if e = state.write(root, p.M{"path": "bin/run.sh", "content": "#!/bin/sh\necho ready\n"}); e != nil {
		t.Fatal(e)
	}
	if !state.modes["bin/run.sh"] {
		t.Fatal("text update cleared explicit executable mode")
	}
	if e = state.write(root, p.M{"path": "bin/run.sh", "content": "text", "executable": false}); e != nil || state.modes["bin/run.sh"] {
		t.Fatal("explicit mode could not be cleared", e)
	}
	if e = state.write(root, p.M{"path": "other", "content": "text", "executable": "true"}); e == nil {
		t.Fatal("nonboolean mode accepted")
	}
	for _, name := range []string{"../escape", "bin"} {
		if e = state.delete(root, name); e == nil {
			t.Fatal("delete escaped or removed a directory", name)
		}
	}
	if e = state.delete(root, "bin/run.sh"); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(filepath.Join(root, "bin/run.sh")); !os.IsNotExist(e) {
		t.Fatal("regular source file was not deleted")
	}
	if e = state.write(root, p.M{"path": "bin", "content": "invalid directory write", "executable": true}); e == nil {
		t.Fatal("write replaced directory")
	}
	if _, exists := state.modes["bin"]; exists {
		t.Fatal("failed write changed trusted mode metadata")
	}
}

func TestWorkspaceCapturesToolModeChangesAfterExplicitWrite(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX chmod acceptance")
	}
	root := t.TempDir()
	state, e := newSourceWorkspace(nil)
	if e != nil {
		t.Fatal(e)
	}
	defer state.close()
	if e = state.write(root, p.M{"path": "run.sh", "content": "#!/bin/sh\n", "executable": true}); e != nil {
		t.Fatal(e)
	}
	if e = os.Chmod(filepath.Join(root, "run.sh"), 0600); e != nil {
		t.Fatal(e)
	}
	if e = state.write(root, p.M{"path": "run.sh", "content": "updated source"}); e != nil {
		t.Fatal(e)
	}
	entries, e := state.selectEntries(context.Background(), root)
	if e != nil || len(entries) != 1 || entries[0].Executable {
		t.Fatal("later tool mode change was overwritten by an earlier model action", e, entries)
	}
}

// Only model responses are deterministic. HTTP, native commands, source bytes,
// Git, build artifact and independent quality execution are real.
func TestWorkerCommitsCompleteToolSourceAndIndependentQuality(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("acceptance uses POSIX tools; Windows worker is cross-compiled separately")
	}
	for _, withModel := range []bool{true, false} {
		name := "approved-command"
		if withModel {
			name = "deterministic-model"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			store, e := p.Open(filepath.Join(t.TempDir(), "control.db"))
			if e != nil {
				t.Fatal(e)
			}
			defer store.Close()
			svc := p.NewService(store)
			h := httpapi.New(svc, false)
			if e = h.Auth.Bootstrap("source@example.test", "source-test-password-123"); e != nil {
				t.Fatal(e)
			}
			identity, _ := h.Auth.IdentityByEmail("source@example.test")
			owner := p.Actor{ID: identity.ID, Kind: "human", Admin: true}
			command := func(a p.Actor, name string, in p.M) p.Resource {
				t.Helper()
				v, e := svc.Command(ctx, a, name, in)
				if e != nil {
					t.Fatalf("%s: %v", name, e)
				}
				return v
			}
			project := command(owner, "projects.create", p.M{"title": "Complete source acceptance"})
			goal := command(owner, "goals.create", p.M{"projectId": project.ID, "title": "Tool-generated application", "outcome": "Retain the actual generated application", "targets": []any{"web"}, "criteria": []any{p.M{"id": "asset", "text": "Package preserves binary source", "method": "automated"}}, "budget": 100})
			command(owner, "goals.approve", p.M{"id": goal.ID})
			repo := command(owner, "repositories.create", p.M{"projectId": project.ID, "title": "source"})
			seed := t.TempDir()
			files := map[string]string{".gitignore": "tracked.txt\ncache/\n", "tracked.txt": "original\n", "removed.txt": "obsolete\n", "bin/start.sh": "#!/bin/sh\necho ready\n"}
			entries := []source.Entry{}
			for name, body := range files {
				path := filepath.Join(seed, name)
				_ = os.MkdirAll(filepath.Dir(path), 0700)
				if e = os.WriteFile(path, []byte(body), 0600); e != nil {
					t.Fatal(e)
				}
				entries = append(entries, source.Entry{Name: name, Size: int64(len(body))})
			}
			var tar bytes.Buffer
			if e = source.Write(&tar, seed, entries); e != nil {
				t.Fatal(e)
			}
			op, e := svc.CommitSnapshot(ctx, owner, repo.ID, p.M{"key": "seed", "branch": "feature", "baseCommit": p.S(repo.Data, "head"), "message": "Initial tree"}, &tar)
			if e != nil {
				t.Fatal(e)
			}
			base := p.S(op.Data, "commit")
			script := `test -z "$IS_WORKER_TOKEN" && mkdir -p assets cache && dd if=/dev/zero of=assets/font.bin bs=1024 count=1100 2>/dev/null && printf '\377\200\r\n' >> assets/font.bin && printf ignored > cache/generated && printf generated > generated.txt && chmod +x bin/start.sh`
			task := p.M{"id": "code", "title": "Generate actual source", "kind": "code", "role": "developer", "runtime": runtime.GOOS, "repositoryId": repo.ID, "branch": "feature", "commit": base}
			if !withModel {
				task["command"] = []any{"/bin/sh", "-c", script + ` && printf 'updated\n' > tracked.txt && rm removed.txt`}
			}
			plan := command(owner, "plans.propose", p.M{"goalId": goal.ID, "summary": "Complete source, build and independent check", "tasks": []any{task, p.M{"id": "build", "title": "Package exact asset", "kind": "build", "role": "developer", "runtime": runtime.GOOS, "repositoryId": repo.ID, "branch": "feature", "dependsOn": []any{"code"}, "command": []any{"/bin/sh", "-c", "cat assets/font.bin > package.bin"}, "outputPath": "package.bin", "target": "web"}}})
			command(owner, "plans.approve", p.M{"id": plan.ID})
			step := 0
			h.ModelCall = func(_ context.Context, _ p.Actor, in p.M) (p.M, error) {
				actions := []p.M{{"action": "write", "path": "tracked.txt", "content": "updated\n"}, {"action": "delete", "path": "removed.txt"}, {"action": "write", "path": "bin/start.sh", "content": "#!/bin/sh\necho ready\n", "executable": true}, {"action": "run", "command": []any{"/bin/sh", "-c", script}}, {"action": "finish", "summary": "Generated the approved source"}}
				if !withModel || step >= len(actions) {
					t.Error("unexpected model request")
					return nil, io.EOF
				}
				b, _ := json.Marshal(actions[step])
				step++
				return p.M{"choices": []any{p.M{"message": p.M{"content": string(b)}}}}, nil
			}
			branchReads, targetChecks := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if strings.HasSuffix(req.URL.Path, "/refs") {
					targetChecks++
				}
				if req.Method == "GET" && strings.HasPrefix(req.URL.Path, "/api/v1/repositories/") && req.URL.Query().Has("branch") {
					branchReads++
					http.Error(w, "branch discovery is unavailable for pinned work", http.StatusServiceUnavailable)
					return
				}
				h.ServeHTTP(w, req)
			}))
			defer server.Close()
			runner := func(role string, caps []any, quality bool) *Runner {
				t.Helper()
				w := command(owner, "workers.register", p.M{"projectId": project.ID, "title": role, "runtime": runtime.GOOS, "roles": []any{role}, "capabilities": caps, "trustedQuality": quality})
				token, _, err := h.Auth.IssueToken(ctx, owner, p.M{"projectId": project.ID, "workerId": w.ID})
				if err != nil {
					t.Fatal(err)
				}
				client, err := NewClient(server.URL, token, true)
				if err != nil {
					t.Fatal(err)
				}
				return &Runner{Client: client, Config: Config{Driver: "native", AllowNative: true, StateDir: t.TempDir()}}
			}
			run := func(r *Runner) {
				t.Helper()
				if worked, e := r.RunOnce(ctx); e != nil || !worked {
					t.Fatalf("worked=%v: %v", worked, e)
				}
			}
			dev := runner("developer", []any{"code", "build"}, false)
			t.Setenv("IS_WORKER_TOKEN", "must-not-reach-command")
			run(dev)
			repo, _ = store.Get(repo.ID)
			commit := p.S(repo.Data, "lastCommit")
			if commit == base {
				runs, _ := store.List(project.ID, "run", goal.ID)
				t.Fatalf("no actual source commit: %+v", runs)
			}
			raw := exec.Command("git", "--git-dir", svc.RepoPath(repo), "show", commit+":assets/font.bin")
			asset, e := raw.Output()
			expected := append(make([]byte, 1100*1024), 255, 128, 13, 10)
			if e != nil || !bytes.Equal(asset, expected) {
				t.Fatal("actual tool binary was discarded or changed", e)
			}
			tree, e := svc.Git(repo, "ls-tree", "-r", commit)
			if e != nil || strings.Contains(tree, "removed.txt") || strings.Contains(tree, "cache/generated") || !strings.Contains(tree, "100755 blob") || !strings.Contains(tree, "generated.txt") {
				t.Fatal("incomplete source tree", e, tree)
			}
			if withModel && step != 5 {
				t.Fatal("model steps were not exercised", step)
			}
			// A later human edit must not silently replace the approved producer's
			// source while the dependent build is waiting for a node.
			advanced := command(owner, "repositories.commit", p.M{"id": repo.ID, "branch": "feature", "expectedHead": commit, "message": "Unrelated later branch edit", "files": p.M{"tracked.txt": "wrong-version\n"}})
			if p.S(advanced.Data, "lastCommit") == commit {
				t.Fatal("branch advance acceptance was not exercised")
			}
			run(dev)
			artifacts, e := store.List(project.ID, "artifact", goal.ID)
			if e != nil || len(artifacts) != 1 {
				runs, _ := store.List(project.ID, "run", goal.ID)
				t.Fatalf("source cannot build: %v %+v runs=%+v", e, artifacts, runs)
			}
			if p.S(artifacts[0].Data, "commit") != commit {
				t.Fatal("build used a mutable branch instead of its producer's source", artifacts[0].Data)
			}
			pipeline := command(owner, "pipelines.create", p.M{"projectId": project.ID, "title": "Check exact source and artifact", "command": []any{"/bin/sh", "-c", `test -x bin/start.sh && test ! -e removed.txt && test ! -e cache/generated && grep -qx updated tracked.txt && cmp assets/font.bin "$IS_ARTIFACT_PATH"`}, "method": "automated", "runtime": runtime.GOOS})
			command(owner, "pipelines.run", p.M{"pipelineId": pipeline.ID, "artifactId": artifacts[0].ID, "criteria": []any{"asset"}})
			qa := runner("quality", []any{"test"}, true)
			run(qa)
			evidence, e := store.List(project.ID, "evidence", goal.ID)
			if e != nil || len(evidence) != 1 || evidence[0].Status != "passed" || p.S(evidence[0].Data, "commit") != commit {
				t.Fatalf("no independent source-bound evidence: %v %+v", e, evidence)
			}
			if branchReads != 0 || targetChecks != 1 {
				t.Fatal("pinned source used branch discovery or skipped code target preflight", branchReads, targetChecks)
			}
		})
	}
}

func TestWorkerBlocksChangedCodeTargetBeforeRunningAnyTool(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX command acceptance")
	}
	ctx := context.Background()
	store, e := p.Open(filepath.Join(t.TempDir(), "control.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	svc := p.NewService(store)
	h := httpapi.New(svc, false)
	if e = h.Auth.Bootstrap("preflight@example.test", "preflight-password-123"); e != nil {
		t.Fatal(e)
	}
	identity, _ := h.Auth.IdentityByEmail("preflight@example.test")
	owner := p.Actor{ID: identity.ID, Kind: "human", Admin: true}
	command := func(a p.Actor, name string, in p.M) p.Resource {
		t.Helper()
		r, e := svc.Command(ctx, a, name, in)
		if e != nil {
			t.Fatalf("%s: %v", name, e)
		}
		return r
	}
	project := command(owner, "projects.create", p.M{"title": "Preflight"})
	repo := command(owner, "repositories.create", p.M{"projectId": project.ID, "title": "source"})
	goal := command(owner, "goals.create", p.M{"projectId": project.ID, "title": "Preflight", "outcome": "Do not execute against changed source", "criteria": []any{p.M{"id": "safe", "text": "Reject changed branch"}}, "targets": []any{"web"}, "budget": 100})
	command(owner, "goals.approve", p.M{"id": goal.ID})
	marker := filepath.Join(t.TempDir(), "must-not-exist")
	plan := command(owner, "plans.propose", p.M{"goalId": goal.ID, "summary": "Check source before tools", "tasks": []any{p.M{"id": "code", "title": "Code", "kind": "code", "role": "developer", "runtime": runtime.GOOS, "repositoryId": repo.ID, "branch": "feature", "command": []any{"/usr/bin/touch", marker}}}})
	command(owner, "plans.approve", p.M{"id": plan.ID})
	w := command(owner, "workers.register", p.M{"projectId": project.ID, "title": "Developer", "roles": []any{"developer"}, "capabilities": []any{"code"}, "runtime": runtime.GOOS})
	actor := p.Actor{ID: "worker:" + w.ID, Kind: "worker", WorkerID: w.ID, ProjectID: project.ID, Roles: []string{"developer"}}
	run := command(actor, "tasks.claim", p.M{})
	command(owner, "repositories.commit", p.M{"id": repo.ID, "branch": "feature", "message": "Concurrent change", "files": p.M{"other.txt": "new input"}})
	token, _, e := h.Auth.IssueToken(ctx, owner, p.M{"projectId": project.ID, "workerId": w.ID})
	if e != nil {
		t.Fatal(e)
	}
	server := httptest.NewServer(h)
	defer server.Close()
	client, e := NewClient(server.URL, token, true)
	if e != nil {
		t.Fatal(e)
	}
	runner := &Runner{Client: client, Config: Config{Driver: "native", AllowNative: true, StateDir: t.TempDir()}}
	result, exit, e := runner.execute(ctx, run, io.Discard)
	if e == nil || exit == 0 || !p.B(result, "blocked") {
		t.Fatal("changed branch was not blocked", result, exit, e)
	}
	if _, e = os.Stat(marker); !os.IsNotExist(e) {
		t.Fatal("tool ran before checking immutable target", e)
	}
}

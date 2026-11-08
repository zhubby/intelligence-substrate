package worker

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/intelligence-substrate/platform/internal/httpapi"
	"github.com/intelligence-substrate/platform/internal/integrations"
	p "github.com/intelligence-substrate/platform/internal/platform"
)

func TestWorkerExecutesRealBuildAndIndependentPipeline(t *testing.T) {
	ctx := context.Background()
	store, e := p.Open(filepath.Join(t.TempDir(), "control.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	svc := p.NewService(store)
	handler := httpapi.New(svc, false)
	if e = handler.Auth.Bootstrap("owner@example.com", "test-worker-password-123"); e != nil {
		t.Fatal(e)
	}
	user, e := handler.Auth.IdentityByEmail("owner@example.com")
	if e != nil {
		t.Fatal(e)
	}
	owner := p.Actor{ID: user.ID, Kind: "human", Admin: true}
	command := func(a p.Actor, name string, in p.M) p.Resource {
		t.Helper()
		v, e := svc.Command(ctx, a, name, in)
		if e != nil {
			t.Fatalf("%s: %v", name, e)
		}
		return v
	}
	project := command(owner, "projects.create", p.M{"title": "Worker integration"})
	goal := command(owner, "goals.create", p.M{"projectId": project.ID, "title": "Actual build", "outcome": "Produce and verify the package", "targets": []any{"web"}, "criteria": []any{p.M{"id": "output", "text": "Package contains expected content", "method": "automated"}}, "budget": 100})
	command(owner, "goals.approve", p.M{"id": goal.ID})
	repo := command(owner, "repositories.create", p.M{"projectId": project.ID, "title": "source"})
	repo = command(owner, "repositories.commit", p.M{"id": repo.ID, "branch": "feature", "message": "Add source", "files": p.M{"source.txt": "expected\n"}})
	plan := command(owner, "plans.propose", p.M{"goalId": goal.ID, "summary": "Build source", "tasks": []any{p.M{"id": "build", "title": "Build a real package", "role": "developer", "kind": "build", "runtime": runtime.GOOS, "repositoryId": repo.ID, "commit": p.S(repo.Data, "lastCommit"), "command": []any{"/bin/sh", "-c", "test -z \"$IS_WORKER_TOKEN\" && cat source.txt > package.txt"}, "outputPath": "package.txt", "target": "web"}}})
	command(owner, "plans.approve", p.M{"id": plan.ID})
	server := httptest.NewServer(handler)
	defer server.Close()
	runnerFor := func(role, capability string, quality bool) *Runner {
		t.Helper()
		w := command(owner, "workers.register", p.M{"projectId": project.ID, "title": role + " runner", "runtime": runtime.GOOS, "roles": []any{role}, "capabilities": []any{capability}, "trustedQuality": quality})
		token, _, e := handler.Auth.IssueToken(ctx, owner, p.M{"projectId": project.ID, "workerId": w.ID})
		if e != nil {
			t.Fatal(e)
		}
		client, e := NewClient(server.URL, token, true)
		if e != nil {
			t.Fatal(e)
		}
		return &Runner{Client: client, Config: Config{Driver: "native", AllowNative: true, StateDir: t.TempDir()}}
	}
	developer := runnerFor("developer", "build", false)
	t.Setenv("IS_WORKER_TOKEN", "must-not-reach-job")
	if worked, e := developer.RunOnce(ctx); e != nil || !worked {
		t.Fatalf("build cycle worked=%v: %v", worked, e)
	}
	artifacts, e := store.List(project.ID, "artifact", goal.ID)
	if e != nil || len(artifacts) != 1 {
		runs, _ := store.List(project.ID, "run", goal.ID)
		t.Fatalf("actual output not registered: %v %+v; runs=%+v", e, artifacts, runs)
	}
	if e = svc.VerifyArtifact(artifacts[0]); e != nil {
		t.Fatal(e)
	}
	pipeline := command(owner, "pipelines.create", p.M{"projectId": project.ID, "title": "Verify actual content", "command": []any{"/bin/sh", "-c", "test -z \"$IS_WORKER_TOKEN\" && grep -qx expected \"$IS_ARTIFACT_PATH\""}, "method": "automated", "runtime": runtime.GOOS})
	command(owner, "pipelines.run", p.M{"pipelineId": pipeline.ID, "artifactId": artifacts[0].ID, "criteria": []any{"output"}})
	qa := runnerFor("quality", "test", true)
	if worked, e := qa.RunOnce(ctx); e != nil || !worked {
		t.Fatalf("quality cycle worked=%v: %v", worked, e)
	}
	evidence, _ := store.List(project.ID, "evidence", goal.ID)
	if len(evidence) != 1 || evidence[0].Status != "passed" {
		t.Fatalf("independent pipeline produced no evidence: %+v", evidence)
	}
	if _, err := svc.Command(ctx, owner, "releases.create", p.M{"goalId": goal.ID, "version": "1.0.0", "commit": p.S(artifacts[0].Data, "commit"), "artifactIds": []any{artifacts[0].ID}}); err == nil || !strings.Contains(err.Error(), "目标环境") {
		t.Fatalf("API-only test incorrectly satisfied real-browser release gate: %v", err)
	}
	failing := command(owner, "pipelines.create", p.M{"projectId": project.ID, "title": "Failing check", "command": []any{"/bin/sh", "-c", "exit 7"}, "method": "automated", "runtime": runtime.GOOS})
	command(owner, "pipelines.run", p.M{"pipelineId": failing.ID, "artifactId": artifacts[0].ID, "criteria": []any{"output"}})
	if _, e = qa.RunOnce(ctx); e != nil {
		t.Fatal(e)
	}
	evidence, _ = store.List(project.ID, "evidence", goal.ID)
	if len(evidence) != 1 {
		t.Fatal("nonzero exit forged passing evidence")
	}
	tasks, _ := store.List(project.ID, "task", goal.ID)
	found := false
	for _, task := range tasks {
		if task.Title == "Failing check" && task.Status == "failed" {
			found = true
		}
	}
	if !found {
		t.Fatal("real nonzero exit was lost")
	}
}

func TestSourceArchiveRejectsEscapeAndSymlinks(t *testing.T) {
	for _, header := range []*tar.Header{{Name: "../escape", Mode: 0600, Size: 1, Typeflag: tar.TypeReg}, {Name: "link", Linkname: "/tmp", Typeflag: tar.TypeSymlink}} {
		var b bytes.Buffer
		w := tar.NewWriter(&b)
		_ = w.WriteHeader(header)
		if header.Size > 0 {
			_, _ = w.Write([]byte("x"))
		}
		w.Close()
		if e := extractSource(t.TempDir(), &b); e == nil {
			t.Fatalf("unsafe source accepted: %+v", header)
		}
	}
}

// The model is deterministic; Git, command execution, artifacts, failures and
// independent verification all run through the real HTTP/worker path.
func TestIndependentFailureRepairsRebuildsAndRetests(t *testing.T) {
	runIndependentRepairFlow(t, false)
}

func TestRecoveredRepairRebuildsAndIndependentlyRetests(t *testing.T) {
	runIndependentRepairFlow(t, true)
}

func runIndependentRepairFlow(t *testing.T, recoverSource bool) {
	t.Helper()
	ctx := context.Background()
	store, e := p.Open(filepath.Join(t.TempDir(), "control.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	svc := p.NewService(store)
	h := httpapi.New(svc, false)
	if e = h.Auth.Bootstrap("repair@example.test", "test-repair-password-123"); e != nil {
		t.Fatal(e)
	}
	identity, _ := h.Auth.IdentityByEmail("repair@example.test")
	owner := p.Actor{ID: identity.ID, Kind: "human", Admin: true}
	command := func(a p.Actor, name string, in p.M) p.Resource {
		t.Helper()
		r, err := svc.Command(ctx, a, name, in)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return r
	}
	project := command(owner, "projects.create", p.M{"title": "Repair acceptance"})
	goal := command(owner, "goals.create", p.M{"projectId": project.ID, "title": "Feedback package", "outcome": "Produce a verified package", "targets": []any{"web"}, "criteria": []any{p.M{"id": "content", "text": "Package has expected content", "method": "automated"}}, "budget": 100})
	command(owner, "goals.approve", p.M{"id": goal.ID})
	repo := command(owner, "repositories.create", p.M{"projectId": project.ID, "title": "repair-source"})
	previous := command(owner, "plans.propose", p.M{"goalId": goal.ID, "summary": "Earlier superseded scope", "tasks": []any{p.M{"id": "obsolete", "title": "Superseded task", "kind": "code", "role": "developer", "runtime": runtime.GOOS, "repositoryId": repo.ID, "instructions": "stale-plan-instructions-should-not-enter-current-context"}}})
	command(owner, "plans.approve", p.M{"id": previous.ID})
	plan := command(owner, "plans.propose", p.M{"goalId": goal.ID, "summary": "Implement, build, independently verify", "tasks": []any{
		p.M{"id": "code", "title": "Implement source", "kind": "code", "role": "developer", "runtime": runtime.GOOS, "repositoryId": repo.ID, "instructions": "current-complete-scope-marker"},
		p.M{"id": "build", "title": "Package source", "kind": "build", "role": "developer", "runtime": runtime.GOOS, "repositoryId": repo.ID, "instructions": "pending-build-scope-marker", "dependsOn": []any{"code"}, "command": []any{"/bin/sh", "-c", "cat source.txt > package.txt"}, "outputPath": "package.txt", "target": "web"},
	}})
	command(owner, "plans.approve", p.M{"id": plan.ID})
	step := 0
	respond := func(_ context.Context, _ p.Actor, in p.M) (p.M, error) {
		step++
		var initial p.M
		if err := json.Unmarshal([]byte(p.S(p.Obj(p.Items(in["messages"])[1]), "content")), &initial); err != nil {
			t.Fatal(err)
		}
		if nested := p.Obj(initial["approvedTaskContext"]); len(nested) > 0 {
			initial = nested
		}
		if step <= 2 && p.S(p.Obj(initial["task"]), "instructions") != "current-complete-scope-marker" {
			t.Error("context compaction removed the current task's complete instructions")
		}
		for _, value := range p.Items(initial["approvedWork"]) {
			item := p.Obj(value)
			data := p.Obj(item["data"])
			if _, exists := data["sourceBinding"]; exists {
				t.Error("coordination index repeats the full source binding in every model turn")
			}
			if p.S(item, "status") != "pending" && p.S(data, "instructions") != "" {
				t.Error("coordination index repeats instructions of already assigned or completed work")
			}
			if p.S(item, "id") == "" || p.S(data, "logicalId") == "" || p.S(data, "kind") == "" || p.S(data, "role") == "" {
				t.Error("coordination index lost task identities or scope")
			}
			if p.S(data, "logicalId") == "build" && p.S(item, "status") == "pending" && (p.S(data, "instructions") != "pending-build-scope-marker" || len(p.Items(data["command"])) == 0) {
				t.Error("pending work lost the scope or command needed for governed coordination")
			}
		}
		contextBody, _ := json.Marshal(in["messages"])
		if strings.Contains(string(contextBody), "stale-plan-instructions-should-not-enter-current-context") {
			t.Error("current Agent received cancelled work from a superseded plan")
		}
		if step%2 == 0 {
			preserved := false
			for _, item := range p.Items(in["messages"]) {
				message := p.Obj(item)
				if p.S(message, "role") == "assistant" && p.S(message, "reasoning_content") == "opaque-provider-continuation" {
					preserved = true
				}
			}
			if !preserved {
				t.Error("multi-step agent dropped provider continuation context")
			}
		}
		action := p.M{"action": "finish", "summary": "Implemented the approved source change"}
		if step%2 == 1 {
			content := "broken\n"
			if step > 2 {
				b, _ := json.Marshal(in["messages"])
				if !strings.Contains(string(b), "independentFailure") {
					t.Error("repair lost independent failure context")
				}
				content = "expected\n"
			}
			action = p.M{"action": "write", "path": "source.txt", "content": content}
		}
		b, _ := json.Marshal(action)
		return p.M{"choices": []any{p.M{"message": p.M{"content": string(b), "reasoning_content": "opaque-provider-continuation"}}}}, nil
	}
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in p.M
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			t.Error(err)
		}
		if p.N(in, "max_tokens") != 8192 {
			t.Errorf("Agent ignored the configured model output budget: %v", in["max_tokens"])
		}
		out, _ := respond(r.Context(), p.Actor{}, in)
		choice := p.Obj(p.Items(out["choices"])[0])
		choice["finish_reason"] = "stop"
		out["usage"] = p.M{"prompt_tokens": 10, "completion_tokens": 30}
		_ = json.NewEncoder(w).Encode(out)
	}))
	defer provider.Close()
	for key, value := range map[string]string{"IS_MODEL_BASE_URL": provider.URL, "IS_MODEL_NAME": "test", "IS_MODEL_INPUT_USD_PER_MILLION": "1", "IS_MODEL_OUTPUT_USD_PER_MILLION": "1", "IS_MODEL_DEFAULT_MAX_TOKENS": "8192", "IS_MODEL_EXTERNAL": "false"} {
		t.Setenv(key, value)
	}
	model := integrations.ModelFromEnv(svc)
	if model == nil {
		t.Fatal("valid model configuration rejected")
	}
	model.Client = provider.Client()
	h.ModelCall = model.Chat
	server := httptest.NewServer(h)
	defer server.Close()
	runner := func(role string, capabilities []any, quality bool) *Runner {
		t.Helper()
		w := command(owner, "workers.register", p.M{"projectId": project.ID, "title": role, "runtime": runtime.GOOS, "roles": []any{role}, "capabilities": capabilities, "trustedQuality": quality})
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
	dev := runner("developer", []any{"code", "build"}, false)
	qa := runner("quality", []any{"test"}, true)
	run := func(r *Runner) {
		t.Helper()
		if worked, err := r.RunOnce(ctx); err != nil || !worked {
			t.Fatalf("run worked=%v: %v", worked, err)
		}
	}
	run(dev)
	run(dev)
	artifacts, _ := store.List(project.ID, "artifact", goal.ID)
	if len(artifacts) != 1 {
		t.Fatalf("initial build artifacts: %d", len(artifacts))
	}
	initialCommit := p.S(artifacts[0].Data, "commit")
	initialArtifact := artifacts[0].ID
	pipeline := command(owner, "pipelines.create", p.M{"projectId": project.ID, "title": "Protected content check", "command": []any{"/bin/sh", "-c", "grep -qx expected \"$IS_ARTIFACT_PATH\""}, "method": "automated", "runtime": runtime.GOOS})
	command(owner, "pipelines.run", p.M{"pipelineId": pipeline.ID, "artifactId": artifacts[0].ID, "criteria": []any{"content"}})
	run(qa)
	defects, _ := store.List(project.ID, "defect", goal.ID)
	if len(defects) != 1 || defects[0].Status != "repairing" {
		t.Fatalf("failure did not queue repair: %+v", defects)
	}
	evidence, _ := store.List(project.ID, "evidence", goal.ID)
	if len(evidence) != 0 {
		t.Fatal("failed test created passing evidence")
	}
	if recoverSource {
		// Execute the real repair and snapshot, then model a controller crash
		// before tasks.complete. No successful completion is fabricated.
		repair, e := dev.Client.Command(ctx, "tasks.claim", p.M{})
		if e != nil || repair.Kind != "run" || p.S(p.Obj(repair.Data["task"]), "defectId") == "" {
			t.Fatal("repair did not claim", e, repair)
		}
		var log bytes.Buffer
		result, exit, e := dev.execute(ctx, repair, &log)
		if e != nil || exit != 0 {
			t.Fatal("actual repair source failed", e, exit, log.String())
		}
		repair.Data["leaseUntil"] = "2000-01-01T00:00:00Z"
		if e = store.Update(ctx, func(tx *p.Tx) error { return tx.Put(repair) }); e != nil {
			t.Fatal(e)
		}
		op, e := store.Get(p.S(result, "sourceOperationId"))
		if e != nil {
			t.Fatal(e)
		}
		command(owner, "sources.reconcile", p.M{"id": op.ID, "key": "recover-real-repair", "commit": p.S(op.Data, "commit"), "observedHead": p.S(op.Data, "commit"), "decision": "confirm", "reason": "Confirm actual interrupted repair, then rebuild and independently retest"})
		restoredDefect, _ := store.Get(defects[0].ID)
		if restoredDefect.Status != "awaiting_verification" {
			t.Fatal("recovered repair did not schedule rebuilding and retesting", restoredDefect)
		}
	} else {
		run(dev)
	}
	run(dev)
	run(qa)
	defect, _ := store.Get(defects[0].ID)
	evidence, _ = store.List(project.ID, "evidence", goal.ID)
	artifacts, _ = store.List(project.ID, "artifact", goal.ID)
	if defect.Status != "closed" || len(evidence) != 1 || len(artifacts) != 2 {
		t.Fatalf("repair not independently verified: defect=%+v, evidence=%d artifacts=%d", defect, len(evidence), len(artifacts))
	}
	if p.S(evidence[0].Data, "commit") == initialCommit || p.S(evidence[0].Data, "artifactId") == initialArtifact {
		t.Fatal("repair reused stale source or artifact evidence")
	}
	verified, err := store.Get(p.S(evidence[0].Data, "artifactId"))
	if err != nil || p.S(verified.Data, "commit") != p.S(evidence[0].Data, "commit") {
		t.Fatal("verification does not bind the repaired artifact")
	}
	if step != 4 {
		t.Fatalf("unexpected model action count: %d", step)
	}
}

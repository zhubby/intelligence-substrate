package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/intelligence-substrate/platform/internal/integrations"
	p "github.com/intelligence-substrate/platform/internal/platform"
)

func workspaceHarness(t *testing.T, kind string) (*inboxHarness, p.Resource, string) {
	t.Helper()
	f := newInboxHarness(t, false)
	repo := f.command(t, f.owner, "repositories.create", p.M{"projectId": f.project.ID, "title": "checkpoint-source"})
	seed := f.command(t, f.owner, "repositories.commit", p.M{"id": repo.ID, "branch": "seed", "message": "Original approved files", "files": p.M{".gitignore": "cache/\n", "remove.txt": "remove me", "readme.txt": "original"}})
	base := p.S(seed.Data, "lastCommit")
	plan := f.command(t, f.owner, "plans.propose", p.M{"goalId": f.goal.ID, "summary": "Preserve work while asking the exact collaborator", "tasks": []any{p.M{"id": "left", "title": "Implement UTC", "kind": kind, "role": "developer", "runtime": runtime.GOOS, "repositoryId": repo.ID, "commit": base, "branch": "checkpoint-work", "budgetCents": 100}}})
	f.command(t, f.owner, "plans.approve", p.M{"id": plan.ID})
	return f, repo, base
}

func workspaceRunner(t *testing.T, f *inboxHarness) (*Runner, p.Actor) {
	t.Helper()
	w := f.command(t, f.owner, "workers.register", p.M{"projectId": f.project.ID, "title": "Workspace collaborator", "roles": []any{"developer"}, "runtime": runtime.GOOS, "capabilities": []any{"code", "design"}})
	token, actor, e := f.h.Auth.IssueToken(context.Background(), f.owner, p.M{"projectId": f.project.ID, "workerId": w.ID})
	if e != nil {
		t.Fatal(e)
	}
	client, e := NewClient(f.server.URL, token, true)
	if e != nil {
		t.Fatal(e)
	}
	return &Runner{Client: client, Config: Config{Driver: "native", AllowNative: true, StateDir: t.TempDir()}}, actor
}

func workspaceTask(t *testing.T, f *inboxHarness, logical string) p.Resource {
	t.Helper()
	goal, e := f.svc.Store.Get(f.goal.ID)
	if e != nil {
		t.Fatal(e)
	}
	tasks, e := f.svc.Store.List(f.project.ID, "task", f.goal.ID)
	if e != nil {
		t.Fatal(e)
	}
	for _, task := range tasks {
		if p.S(task.Data, "logicalId") == logical && p.S(task.Data, "planId") == p.S(goal.Data, "activePlanId") {
			return task
		}
	}
	t.Fatal("no active plan task", logical)
	return p.Resource{}
}

func workspaceReply(t *testing.T, f *inboxHarness) p.Resource {
	t.Helper()
	waits, e := f.svc.Store.List(f.project.ID, "message_wait", f.goal.ID)
	if e != nil {
		t.Fatal(e)
	}
	for _, wait := range waits {
		if wait.Status != "waiting" {
			continue
		}
		return f.command(t, f.owner, "messages.send", p.M{"projectId": f.project.ID, "goalId": f.goal.ID, "recipient": wait.Data["requester"], "recipientTaskId": wait.Data["taskId"], "type": "result", "key": "reply:" + wait.ID, "causationId": wait.Data["requestMessageId"], "payload": p.M{"answer": "UTC confirmed; preserve the saved work. This grants no approval."}})
	}
	t.Fatal("no waiting workspace task")
	return p.Resource{}
}

func workspaceWaitAction(t *testing.T, f *inboxHarness, actor p.Actor) p.M {
	t.Helper()
	messages, e := f.svc.Store.List(f.project.ID, "message", f.goal.ID)
	if e != nil {
		t.Fatal(e)
	}
	for _, m := range messages {
		if p.S(m.Data, "sender") == actor.ID {
			return p.M{"action": "wait", "requestMessageId": m.ID, "reason": "Need the exact collaborator response", "summary": "Saved draft files; continue from their actual bytes after the reply.", "timeoutSeconds": 600}
		}
	}
	t.Fatal("request was not sent")
	return nil
}

// Catches lost binary/deletion/mode state, accidental intermediate source
// adoption, ignore-rule loss after restore, transcript/command replay and
// resetting the original source or task envelope across two replacement nodes.
func TestWorkspaceWaitCodeRestoresExactSourceAcrossReplacementWorkers(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX tools acceptance; Windows modes are verified separately")
	}
	f, repo, base := workspaceHarness(t, "code")
	runner, actor := workspaceRunner(t, f)
	phase, calls := 0, 0
	scripted := func(_ context.Context, a p.Actor, in p.M) (p.M, error) {
		calls++
		messages := p.Items(in["messages"])
		var initial p.M
		_ = json.Unmarshal([]byte(p.S(p.Obj(messages[1]), "content")), &initial)
		if phase > 0 && calls == 1 {
			body, _ := json.Marshal(messages)
			if len(messages) != 3 || !strings.Contains(string(body), "UTC confirmed") || strings.Contains(string(body), "Tool observation:") || p.S(initial, "commit") != base || len(p.Obj(initial["untrustedWorkspaceContinuation"])) == 0 {
				return nil, fmt.Errorf("workspace continuation lost its fresh exact context: %s", body)
			}
		}
		actions := []p.M{}
		switch phase {
		case 0:
			actions = []p.M{
				{"action": "write", "path": "draft.txt", "content": "UTC draft"},
				{"action": "run", "command": []string{"/bin/sh", "-c", "printf '\\000\\377\\001' > asset.bin; printf '#!/bin/sh\\necho saved\\n' > tool.sh; chmod +x tool.sh; rm remove.txt; mkdir -p cache; printf excluded > cache/no.txt"}},
				{"action": "message", "recipient": f.owner.ID, "type": "clarification", "payload": p.M{"question": "Confirm UTC after reviewing saved code?"}},
			}
		case 1:
			actions = []p.M{
				{"action": "run", "command": []string{"/bin/sh", "-c", "test ! -e remove.txt && test ! -e cache/no.txt && test -x tool.sh && test \"$(od -An -tx1 asset.bin | tr -d ' \\n')\" = 00ff01 && test \"$(cat draft.txt)\" = 'UTC draft' && printf checkpoint-ok"}},
				{"action": "write", "path": ".gitignore", "content": "cache/\nasset.bin\n"},
				{"action": "message", "recipient": f.owner.ID, "type": "review", "payload": p.M{"question": "Confirm the saved binary still belongs to this source?"}},
			}
		case 2:
			actions = []p.M{
				{"action": "run", "command": []string{"/bin/sh", "-c", "test ! -e remove.txt && test -x tool.sh && test \"$(od -An -tx1 asset.bin | tr -d ' \\n')\" = 00ff01 && printf checkpoint-ok"}},
				{"action": "finish", "summary": "Preserved the exact source and completed the approved implementation; independent quality still required."},
			}
		}
		if calls == 2 && phase > 0 {
			body, _ := json.Marshal(messages)
			if !strings.Contains(string(body), "exitCode=0\\ncheckpoint-ok") {
				return nil, fmt.Errorf("actual restored tool checks failed: %s", body)
			}
		}
		if calls <= len(actions) {
			return actionResponse(actions[calls-1]), nil
		}
		if calls == len(actions)+1 && phase < 2 {
			return actionResponse(workspaceWaitAction(t, f, a)), nil
		}
		return nil, fmt.Errorf("unexpected inference phase=%d call=%d", phase, calls)
	}
	providerCalls := 0
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var in p.M
		if e := json.NewDecoder(request.Body).Decode(&in); e != nil {
			t.Error(e)
			w.WriteHeader(400)
			return
		}
		if request.URL.Path != "/v1/chat/completions" || p.S(in, "model") != "local-workspace-fixture" {
			t.Error("unexpected local provider request")
			w.WriteHeader(400)
			return
		}
		providerCalls++
		out, e := scripted(request.Context(), actor, in)
		if e != nil {
			t.Error(e)
			w.WriteHeader(503)
			return
		}
		out["id"], out["model"], out["usage"] = "local-workspace-response", "local-workspace-fixture", p.M{"prompt_tokens": 10, "completion_tokens": 30}
		p.Obj(p.Items(out["choices"])[0])["finish_reason"] = "stop"
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	}))
	defer provider.Close()
	model := &integrations.Model{Service: f.svc, BaseURL: provider.URL + "/v1", Name: "local-workspace-fixture", InputUSD: 1, OutputUSD: 1, Client: provider.Client()}
	f.h.ModelCall = model.Chat
	for phase = 0; phase < 3; phase++ {
		calls = 0
		if worked, e := runner.RunOnce(context.Background()); e != nil || !worked {
			t.Fatal(worked, e)
		}
		task := workspaceTask(t, f, "left")
		if phase == 2 {
			if task.Status != "succeeded" || p.N(task.Data, "attempts") != 1 || p.N(task.Data, "messageWaitCount") != 2 || p.N(task.Data, "messageStepsUsed") != 8 {
				t.Fatal("continuation reset task bounds", task)
			}
			break
		}
		if task.Status != "waiting_message" {
			t.Fatal("code workspace was not suspended", task.Status, task.Data)
		}
		if head, _ := f.svc.Git(repo, "rev-parse", "--verify", "refs/heads/checkpoint-work"); head != "" {
			t.Fatal("suspension moved output branch", head)
		}
		for _, kind := range []string{"source_commit", "change", "evidence", "artifact"} {
			items, _ := f.svc.Store.List(f.project.ID, kind, f.goal.ID)
			if len(items) != 0 {
				t.Fatal("checkpoint manufactured governed output", kind, items)
			}
		}
		files, _ := filepath.Glob(filepath.Join(runner.Config.StateDir, "run_*.json"))
		if len(files) != 0 {
			t.Fatal("acknowledged suspension retained journal", files)
		}
		f.command(t, f.owner, "workers.disable", p.M{"id": actor.WorkerID})
		workspaceReply(t, f)
		runner, actor = workspaceRunner(t, f)
	}
	commits, _ := f.svc.Store.List(f.project.ID, "source_commit", f.goal.ID)
	if len(commits) != 1 || commits[0].Status != "committed" || p.S(commits[0].Data, "baseCommit") != base {
		t.Fatal("missing one original-base source operation", commits)
	}
	commit := p.S(commits[0].Data, "commit")
	if body, e := f.svc.Git(repo, "show", commit+":asset.bin"); e != nil || body != string([]byte{0, 255, 1}) {
		t.Fatal("ignored checkpoint binary was lost", []byte(body), e)
	}
	if tree, e := f.svc.Git(repo, "ls-tree", "-r", commit); e != nil || strings.Contains(tree, "remove.txt") || strings.Contains(tree, "cache/no.txt") || !strings.Contains(tree, "100755 blob") {
		t.Fatal("wrong final deletion/modes/selection", tree, e)
	}
	checkpoints, _ := f.svc.Store.List(f.project.ID, "workspace_checkpoint", f.goal.ID)
	if len(checkpoints) != 2 {
		t.Fatal("missing immutable checkpoint history", checkpoints)
	}
	for _, checkpoint := range checkpoints {
		body, _ := json.Marshal(checkpoint)
		if checkpoint.Kind == "artifact" || strings.Contains(string(body), "leaseToken") {
			t.Fatal("checkpoint promoted or leaked lease")
		}
		if _, e := os.Stat(f.svc.ArtifactPath(p.S(checkpoint.Data, "digest"))); e != nil {
			t.Fatal(e)
		}
	}
	charges, e := f.svc.Store.List(f.project.ID, "charge", f.goal.ID)
	if e != nil || providerCalls != 10 || len(charges) != 10 {
		t.Fatal("local provider/replay billing count differs", providerCalls, len(charges), e)
	}
	for _, charge := range charges {
		if charge.Status != "settled" || p.N(charge.Data, "chargedCents") != 1 {
			t.Fatal("model cost lost on suspension", charge)
		}
	}
	goal, e := f.svc.Store.Get(f.goal.ID)
	if e != nil || p.N(goal.Data, "spentCents") != 10 {
		t.Fatal("cumulative billing reset", goal, e)
	}
}

// A saved HTML draft remains a proposal; resuming must not manufacture a code
// operation or approve the design by treating collaboration as authority.
func TestWorkspaceWaitDesignRestoresDraftBeforePublishingProposal(t *testing.T) {
	f, _, _ := workspaceHarness(t, "design")
	runner, actor := workspaceRunner(t, f)
	phase, calls := 0, 0
	f.h.ModelCall = func(_ context.Context, a p.Actor, in p.M) (p.M, error) {
		calls++
		if phase == 0 {
			switch calls {
			case 1:
				return actionResponse(p.M{"action": "write", "path": "draft.html", "content": "<!doctype html><title>UTC prototype</title><p>Saved draft</p>"}), nil
			case 2:
				return actionResponse(p.M{"action": "message", "recipient": f.owner.ID, "type": "review", "payload": p.M{"question": "Review the proposed UTC presentation?"}}), nil
			case 3:
				return actionResponse(workspaceWaitAction(t, f, a)), nil
			}
		} else {
			if calls == 1 {
				return actionResponse(p.M{"action": "read", "path": "draft.html"}), nil
			}
			if calls == 2 {
				body, _ := json.Marshal(in["messages"])
				if !strings.Contains(string(body), "Saved draft") {
					return nil, fmt.Errorf("prototype draft vanished")
				}
				return actionResponse(p.M{"action": "finish", "summary": "Submit the reviewed presentation for human selection", "design": p.M{"title": "UTC design", "summary": "Display all timestamps in UTC", "decisions": "UTC timestamps with clear labels", "criteria": []any{"utc"}, "reason": "Consistent approved scope", "prototypePath": "draft.html", "target": "web"}}), nil
			}
		}
		return nil, fmt.Errorf("unexpected design inference %d/%d", phase, calls)
	}
	if worked, e := runner.RunOnce(context.Background()); e != nil || !worked {
		t.Fatal(worked, e)
	}
	if workspaceTask(t, f, "left").Status != "waiting_message" {
		t.Fatal("design draft was not suspended", workspaceTask(t, f, "left"))
	}
	f.command(t, f.owner, "workers.disable", p.M{"id": actor.WorkerID})
	workspaceReply(t, f)
	runner, _ = workspaceRunner(t, f)
	phase, calls = 1, 0
	if worked, e := runner.RunOnce(context.Background()); e != nil || !worked {
		t.Fatal(worked, e)
	}
	if workspaceTask(t, f, "left").Status != "succeeded" {
		t.Fatal("design failed to resume", workspaceTask(t, f, "left"))
	}
	designs, _ := f.svc.Store.List(f.project.ID, "design", f.goal.ID)
	if len(designs) != 1 || designs[0].Status != "proposed" {
		t.Fatal("resumed prototype was lost or approved", designs)
	}
	for _, kind := range []string{"source_commit", "evidence"} {
		items, _ := f.svc.Store.List(f.project.ID, kind, f.goal.ID)
		if len(items) > 0 {
			t.Fatal("design checkpoint promoted into output", kind)
		}
	}
}

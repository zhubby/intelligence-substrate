package httpapi

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	p "github.com/intelligence-substrate/platform/internal/platform"
)

func TestWorkspaceCheckpointHTTPStrictMetadataAndWorkerAuthority(t *testing.T) {
	h := setup(t)
	cookie, csrf := login(t, h)
	identity, _ := h.Auth.IdentityByEmail("owner@example.com")
	owner := identityActor(identity)
	command := func(a p.Actor, name string, in p.M) p.Resource { return authorityCommand(t, h, a, name, in) }
	project := command(owner, "projects.create", p.M{"title": "Workspace API"})
	goal := command(owner, "goals.create", p.M{"projectId": project.ID, "title": "Save code", "outcome": "Keep exact source", "targets": []any{"web"}, "criteria": []any{p.M{"id": "source", "text": "Exact source"}}, "budget": 50})
	command(owner, "goals.approve", p.M{"id": goal.ID})
	repo := command(owner, "repositories.create", p.M{"projectId": project.ID, "title": "source"})
	plan := command(owner, "plans.propose", p.M{"goalId": goal.ID, "summary": "Clarify during development", "tasks": []any{p.M{"id": "code", "title": "Code task", "kind": "code", "role": "developer", "runtime": "linux", "repositoryId": repo.ID}}})
	command(owner, "plans.approve", p.M{"id": plan.ID})
	worker := command(owner, "workers.register", p.M{"projectId": project.ID, "title": "Developer", "roles": []any{"developer"}, "capabilities": []any{"code"}, "runtime": "linux"})
	token, actor, e := h.Auth.IssueToken(context.Background(), owner, p.M{"projectId": project.ID, "workerId": worker.ID})
	if e != nil {
		t.Fatal(e)
	}
	run := command(actor, "tasks.claim", p.M{})
	meta := p.M{"leaseToken": run.Data["leaseToken"], "repositoryId": repo.ID, "commit": p.Obj(run.Data["task"])["commit"]}
	raw, _ := json.Marshal(meta)
	encoded := base64.StdEncoding.EncodeToString(raw)
	var body bytes.Buffer
	tw := tar.NewWriter(&body)
	_ = tw.WriteHeader(&tar.Header{Name: "binary.dat", Mode: 0755, Size: 3})
	_, _ = tw.Write([]byte{0, 255, 1})
	_ = tw.Close()
	post := func(mode string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/v1/runs/"+run.ID+"/checkpoint", bytes.NewReader(body.Bytes()))
		r.Header.Set("Content-Type", "application/x-tar")
		r.Header.Set("X-IS-Checkpoint", encoded)
		if mode != "anonymous" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		switch mode {
		case "human", "csrf":
			r.Header.Del("Authorization")
			r.AddCookie(cookie)
			if mode == "human" {
				r.Header.Set("X-CSRF-Token", csrf)
			}
		case "missing":
			r.Header.Del("X-IS-Checkpoint")
		case "duplicate":
			r.Header.Add("X-IS-Checkpoint", encoded)
		case "oversize":
			r.Header.Set("X-IS-Checkpoint", strings.Repeat("a", 8193))
		case "encoding":
			r.Header.Set("X-IS-Checkpoint", "invalid")
		case "extra":
			r.Header.Set("X-IS-Checkpoint", base64.StdEncoding.EncodeToString([]byte(`{"leaseToken":"x","repositoryId":"x","commit":"x","unknown":1}`)))
		case "duplicate-json":
			r.Header.Set("X-IS-Checkpoint", base64.StdEncoding.EncodeToString([]byte(`{"leaseToken":"x","leaseToken":"y","repositoryId":"x","commit":"x"}`)))
		case "null":
			r.Header.Set("X-IS-Checkpoint", base64.StdEncoding.EncodeToString([]byte(`{"leaseToken":"x","repositoryId":null,"commit":null}`)))
		case "query":
			r.URL.RawQuery = "leaseToken=secret"
		case "media":
			r.Header.Set("Content-Type", "application/json")
		}
		out := httptest.NewRecorder()
		h.ServeHTTP(out, r)
		return out
	}
	for _, tc := range []struct {
		mode string
		want int
	}{{"anonymous", 401}, {"csrf", 403}, {"human", 403}, {"missing", 400}, {"duplicate", 400}, {"oversize", 400}, {"encoding", 400}, {"extra", 400}, {"duplicate-json", 400}, {"null", 400}, {"query", 400}, {"media", 415}} {
		t.Run(tc.mode, func(t *testing.T) {
			out := post(tc.mode)
			if out.Code != tc.want {
				t.Fatalf("got%d want%d: %s", out.Code, tc.want, out.Body.String())
			}
		})
	}
	out := post("valid")
	if out.Code != 200 {
		t.Fatal(out.Code, out.Body.String())
	}
	var checkpoint p.Resource
	_ = json.Unmarshal(out.Body.Bytes(), &checkpoint)
	if checkpoint.Kind != "workspace_checkpoint" || strings.Contains(out.Body.String(), "leaseToken") {
		t.Fatal("invalid checkpoint response", out.Body.String())
	}
	request := command(actor, "messages.send", p.M{"projectId": project.ID, "goalId": goal.ID, "runId": run.ID, "leaseToken": run.Data["leaseToken"], "recipient": owner.ID, "type": "request", "key": "checkpoint-question", "payload": p.M{"question": "Confirm exact source?"}})
	command(actor, "tasks.wait", p.M{"id": run.ID, "leaseToken": run.Data["leaseToken"], "requestMessageId": request.ID, "reason": "Need review", "summary": "Saved actual source", "timeoutSeconds": 600, "steps": 2, "source": p.M{"repositoryId": repo.ID, "commit": meta["commit"]}, "checkpointId": checkpoint.ID})
	command(owner, "messages.send", p.M{"projectId": project.ID, "goalId": goal.ID, "recipient": actor.ID, "recipientTaskId": run.Data["taskId"], "type": "result", "key": "reply", "causationId": request.ID, "payload": p.M{"answer": "Confirmed within scope"}})
	next := command(actor, "tasks.claim", p.M{})
	for _, mode := range []string{"valid", "missing", "wrong", "duplicate", "query", "human"} {
		r := httptest.NewRequest("GET", "/api/v1/runs/"+next.ID+"/checkpoint", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("X-IS-Lease", p.S(next.Data, "leaseToken"))
		if mode == "missing" {
			r.Header.Del("X-IS-Lease")
		}
		if mode == "wrong" {
			r.Header.Set("X-IS-Lease", "wrong")
		}
		if mode == "duplicate" {
			r.Header.Add("X-IS-Lease", "other")
		}
		if mode == "query" {
			r.URL.RawQuery = "x=1"
		}
		if mode == "human" {
			r.Header.Del("Authorization")
			r.AddCookie(cookie)
		}
		response := httptest.NewRecorder()
		h.ServeHTTP(response, r)
		if mode == "valid" {
			if response.Code != 200 || !bytes.Equal(response.Body.Bytes(), body.Bytes()) || response.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("exact active continuation download failed", response.Code)
			}
		} else if response.Code < 400 {
			t.Fatal("invalid checkpoint download accepted", mode, response.Code)
		}
	}
	command(owner, "workers.disable", p.M{"id": worker.ID})
	if out := post("valid"); out.Code != 401 && out.Code != 403 {
		t.Fatal("revoked Worker uploaded checkpoint", out.Code)
	}
}

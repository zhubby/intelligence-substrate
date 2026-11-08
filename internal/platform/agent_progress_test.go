package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"sync"
	"testing"
	"time"
)

func prepareAgentStep(t *testing.T, f *messageFixture, step int, action M, source M) Resource {
	t.Helper()
	return must(t, f.s, f.worker, "agent_steps.prepare", M{"runId": f.run.ID, "leaseToken": f.run.Data["leaseToken"], "step": step, "action": action, "source": source})
}

func TestAgentProgressConfirmedCodeResumesExactCheckpoint(t *testing.T) {
	for _, mode := range []string{"valid", "corrupt", "recipe", "requirement", "target"} {
		t.Run(mode, func(t *testing.T) {
			f, repo, meta := checkpointFixture(t, "code")
			input := M{"repositoryId": repo.ID, "commit": meta["commit"]}
			step := prepareAgentStep(t, f, 1, M{"action": "run", "command": []string{"generate"}}, input)
			body := checkpointTar(t, map[string]string{"binary.dat": string([]byte{0, 255, 2}), "tool.sh": "#!/bin/sh\nexit 7\n"}, "tool.sh")
			checkpoint, e := f.s.UploadAgentCheckpoint(context.Background(), f.worker, step.ID, S(meta, "leaseToken"), bytes.NewReader(body))
			if e != nil {
				t.Fatal(e)
			}
			completion := M{"id": step.ID, "runId": f.run.ID, "leaseToken": meta["leaseToken"], "observation": "exitCode=7\nactual completed command", "checkpointId": checkpoint.ID}
			confirmed := must(t, f.s, f.worker, "agent_steps.complete", completion)
			if confirmed.Status != "completed" {
				t.Fatal(confirmed)
			}
			if replay := must(t, f.s, f.worker, "agent_steps.complete", completion); replay.ID != step.ID {
				t.Fatal(replay)
			}
			seedAgentUsage(t, f, "running", 1, 1, "settled", 5)
			switch mode {
			case "corrupt":
				if e = os.WriteFile(f.s.ArtifactPath(S(checkpoint.Data, "digest")), []byte("changed"), 0600); e != nil {
					t.Fatal(e)
				}
			case "recipe":
				task, _ := f.s.Store.Get(S(f.run.Data, "taskId"))
				task.Data["instructions"] = "different recipe"
				if e = f.s.Store.Update(context.Background(), func(tx *Tx) error { return tx.Put(task) }); e != nil {
					t.Fatal(e)
				}
			case "requirement":
				must(t, f.s, f.a, "goals.revise", M{"id": f.g.ID, "outcome": "Changed scope", "reason": "Changed"})
			case "target":
				must(t, f.s, f.a, "repositories.commit", M{"id": repo.ID, "branch": "checkpoint", "message": "Another writer", "baseCommit": meta["commit"], "files": M{"other": "data"}})
			}
			next := must(t, f.s, f.worker, "tasks.claim", M{})
			if mode != "valid" {
				if next.Kind == "run" {
					t.Fatal("unsafe progress claimed", mode, next)
				}
				return
			}
			resume := Obj(next.Data["agentResume"])
			if next.Kind != "run" || S(resume, "stepId") != step.ID || S(resume, "checkpointId") != checkpoint.ID || S(resume, "checkpointDigest") != S(checkpoint.Data, "digest") || len(Items(resume["trace"])) != 1 {
				t.Fatal("replacement discarded confirmed source and real observation", next)
			}
			if N(Obj(next.Data["agentAllowance"]), "usedModelCalls") != 1 {
				t.Fatal("restore reset allowance", next)
			}
		})
	}
}

func TestAgentProgressPendingDeniesModelWaitAndSuccessfulCompletion(t *testing.T) {
	f := newAllowanceFixture(t)
	_, wait := waitRequest(t, f)
	prepareAgentStep(t, f, 1, M{"action": "run", "command": []string{"side-effect"}}, M{"repositoryId": "", "commit": ""})
	e := f.s.Store.Update(context.Background(), func(tx *Tx) error {
		return f.s.ValidateAgentModelAdmission(tx, f.worker, M{"runId": f.run.ID, "leaseToken": f.run.Data["leaseToken"]}, 1)
	})
	if e == nil {
		t.Fatal("outstanding action admitted another inference")
	}
	if _, e = f.s.Command(context.Background(), f.worker, "tasks.wait", wait); e == nil {
		t.Fatal("wait hid an outstanding action")
	}
	if _, e = f.s.Command(context.Background(), f.worker, "tasks.complete", M{"id": f.run.ID, "leaseToken": f.run.Data["leaseToken"], "exitCode": 0, "result": M{"summary": "invented completion"}}); e == nil {
		t.Fatal("unconfirmed action became successful")
	}
}

func TestAgentProgressMayConfirmLastPaidToolWithoutAnotherInference(t *testing.T) {
	f, repo, meta := checkpointFixture(t, "code")
	run, e := f.s.Store.Get(f.run.ID)
	if e != nil {
		t.Fatal(e)
	}
	task, e := f.s.Store.Get(S(run.Data, "taskId"))
	if e != nil {
		t.Fatal(e)
	}
	run.Data["spentCents"] = task.Data["budgetCents"]
	if e = f.s.Store.Update(context.Background(), func(tx *Tx) error { return tx.Put(run) }); e != nil {
		t.Fatal(e)
	}
	step := prepareAgentStep(t, f, 1, M{"action": "write", "path": "last-paid-result", "content": "confirmed"}, M{"repositoryId": repo.ID, "commit": meta["commit"]})
	checkpoint, e := f.s.UploadAgentCheckpoint(context.Background(), f.worker, step.ID, S(meta, "leaseToken"), bytes.NewReader(checkpointTar(t, map[string]string{"last-paid-result": "confirmed"})))
	if e != nil {
		t.Fatal(e)
	}
	must(t, f.s, f.worker, "agent_steps.complete", M{"id": step.ID, "runId": run.ID, "leaseToken": meta["leaseToken"], "checkpointId": checkpoint.ID, "observation": "source updated in isolated workspace"})
	if e = f.s.Store.Update(context.Background(), func(tx *Tx) error {
		return f.s.ValidateAgentModelAdmission(tx, f.worker, M{"runId": run.ID, "leaseToken": meta["leaseToken"]}, 1)
	}); e == nil {
		t.Fatal("last paid result permitted another over-budget inference")
	}
}

func TestAgentProgressOwnerMustStopAndReconcileExactAction(t *testing.T) {
	f := newAllowanceFixture(t)
	step := prepareAgentStep(t, f, 1, M{"action": "run", "command": []string{"side-effect"}}, M{"repositoryId": "", "commit": ""})
	decision := M{"id": step.ID, "key": "resolve-1", "actionDigest": step.Data["actionDigest"], "externalReconciled": true, "reason": "Operator independently stopped the old process and reconciled its external effect; discard uncertain workspace."}
	if _, e := f.s.Command(context.Background(), f.a, "agent_steps.reconcile", decision); e == nil {
		t.Fatal("active worker reconciled")
	}
	seedAgentUsage(t, f, "failed", 1, 1, "settled", 5)
	if _, e := f.s.Command(context.Background(), f.a, "agent_steps.reconcile", decision); e == nil {
		t.Fatal("enabled worker reconciled")
	}
	must(t, f.s, f.a, "workers.disable", M{"id": f.worker.WorkerID})
	bad := copyM(decision)
	bad["actionDigest"] = "another-action"
	if _, e := f.s.Command(context.Background(), f.a, "agent_steps.reconcile", bad); e == nil {
		t.Fatal("wrong action reconciled")
	}
	if _, e := f.s.Command(context.Background(), f.worker, "agent_steps.reconcile", decision); e == nil {
		t.Fatal("Worker made human decision")
	}
	got := must(t, f.s, f.a, "agent_steps.reconcile", decision)
	if got.Status != "abandoned" {
		t.Fatal(got)
	}
	if replay := must(t, f.s, f.a, "agent_steps.reconcile", decision); replay.ID != got.ID {
		t.Fatal(replay)
	}
	must(t, f.s, f.a, "tasks.retry", M{"id": S(f.run.Data, "taskId")})
	next := must(t, f.s, f.register(t, "reconciled replacement"), "tasks.claim", M{})
	if next.Kind != "run" || len(Obj(next.Data["agentResume"])) != 0 {
		t.Fatal("uncertain bytes were adopted", next)
	}
}

func TestAgentProgressCheckpointReauthorizesAfterStreaming(t *testing.T) {
	for _, mode := range []string{"worker", "credential", "requirement", "target", "budget"} {
		t.Run(mode, func(t *testing.T) {
			f, repo, meta := checkpointFixture(t, "code")
			step := prepareAgentStep(t, f, 1, M{"action": "write", "path": "saved", "content": "bytes"}, M{"repositoryId": repo.ID, "commit": meta["commit"]})
			actor := f.worker
			if mode == "credential" {
				registerMembershipIdentity(t, f.s, f.a.ID, true, false)
				body, err := json.Marshal(actor)
				if err != nil {
					t.Fatal(err)
				}
				actor.CredentialHash = "live-progress-upload-credential"
				if err = f.s.Store.Update(context.Background(), func(tx *Tx) error {
					return tx.InsertCredential(actor.CredentialHash, Credential{IdentityID: f.a.ID, Kind: "token", ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano), Body: string(body)})
				}); err != nil {
					t.Fatal(err)
				}
			}
			var once sync.Once
			streamed := false
			body := &afterRead{Reader: bytes.NewReader(checkpointTar(t, map[string]string{"saved": "bytes"})), after: func() {
				once.Do(func() {
					streamed = true
					switch mode {
					case "worker":
						must(t, f.s, f.a, "workers.disable", M{"id": f.worker.WorkerID})
					case "credential":
						if _, err := f.s.Store.DB.Exec(`DELETE FROM credentials WHERE hash=$1`, actor.CredentialHash); err != nil {
							t.Fatal(err)
						}
					case "requirement":
						must(t, f.s, f.a, "goals.revise", M{"id": f.g.ID, "outcome": "Changed", "reason": "During upload"})
					case "target":
						must(t, f.s, f.a, "repositories.commit", M{"id": repo.ID, "branch": "checkpoint", "message": "Another", "baseCommit": meta["commit"], "files": M{"other": "data"}})
					case "budget":
						run, _ := f.s.Store.Get(f.run.ID)
						run.Data["spentCents"] = 101
						if e := f.s.Store.Update(context.Background(), func(tx *Tx) error { return tx.Put(run) }); e != nil {
							t.Fatal(e)
						}
					}
				})
			}}
			if _, e := f.s.UploadAgentCheckpoint(context.Background(), actor, step.ID, S(meta, "leaseToken"), body); e == nil || !streamed {
				t.Fatal("changed streaming authority accepted", mode)
			}
			items, _ := f.s.Store.List(f.p.ID, "agent_checkpoint", f.g.ID)
			if len(items) != 0 {
				t.Fatal("denied upload advanced progress", items)
			}
			audits, e := f.s.Store.Audits(f.p.ID)
			if e != nil {
				t.Fatal(e)
			}
			denials := 0
			for _, audit := range audits {
				if audit.Action == "denied:agent.checkpoint" {
					denials++
				}
				if audit.Action == "denied:workspace.checkpoint" {
					t.Fatal("ordinary progress denial was mislabeled as an explicit wait")
				}
			}
			if denials != 1 {
				t.Fatal("streaming rejection must have exactly one agent checkpoint denial", denials)
			}
		})
	}
}

func TestAgentProgressAuditFailureCannotAdvanceOrEraseIntent(t *testing.T) {
	f := newAllowanceFixture(t)
	if _, e := f.s.Store.DB.Exec(`CREATE TRIGGER reject_agent_prepare BEFORE INSERT ON audit WHEN json_extract(NEW.body,'$.action')='agent_steps.prepare' BEGIN SELECT RAISE(ABORT,'test intent audit failure'); END`); e != nil {
		t.Fatal(e)
	}
	input := M{"runId": f.run.ID, "leaseToken": f.run.Data["leaseToken"], "step": 1, "action": M{"action": "read", "path": "absent"}, "source": M{"repositoryId": "", "commit": ""}}
	if _, e := f.s.Command(context.Background(), f.worker, "agent_steps.prepare", input); e == nil {
		t.Fatal("audit-failed intent admitted")
	}
	items, _ := f.s.Store.List(f.p.ID, "agent_step", f.g.ID)
	if len(items) != 0 {
		t.Fatal("rolled-back intent persisted")
	}
	if _, e := f.s.Store.DB.Exec(`DROP TRIGGER reject_agent_prepare`); e != nil {
		t.Fatal(e)
	}
	step := must(t, f.s, f.worker, "agent_steps.prepare", input)
	if _, e := f.s.Store.DB.Exec(`CREATE TRIGGER reject_agent_complete BEFORE INSERT ON audit WHEN json_extract(NEW.body,'$.action')='agent_steps.complete' BEGIN SELECT RAISE(ABORT,'test confirmation audit failure'); END`); e != nil {
		t.Fatal(e)
	}
	if _, e := f.s.Command(context.Background(), f.worker, "agent_steps.complete", M{"id": step.ID, "runId": f.run.ID, "leaseToken": f.run.Data["leaseToken"], "checkpointId": "", "observation": "actual file missing"}); e == nil {
		t.Fatal("audit-failed completion admitted")
	}
	stored, _ := f.s.Store.Get(step.ID)
	task, _ := f.s.Store.Get(S(f.run.Data, "taskId"))
	if stored.Status != "prepared" || S(task.Data, "agentProgressId") != "" {
		t.Fatal("audit failure hid pending action")
	}
}

func TestAgentProgressRestoresConfirmedIntegrationBeforeUnknownResolution(t *testing.T) {
	f, _, _ := integrationFixture(t, true)
	run, _ := prepareIntegration(t, f)
	mf := &messageFixture{s: f.s, a: f.a, p: f.p, g: f.g, worker: f.worker, run: run}
	input := M{"repositoryId": f.repo.ID, "commit": Obj(run.Data["task"])["commit"]}
	step := prepareAgentStep(t, mf, 1, M{"action": "read", "path": "shared.txt"}, input)
	checkpoint, e := f.s.UploadAgentCheckpoint(context.Background(), f.worker, step.ID, S(run.Data, "leaseToken"), bytes.NewReader(checkpointTar(t, map[string]string{"shared.txt": "confirmed draft"})))
	if e != nil {
		t.Fatal(e)
	}
	must(t, f.s, f.worker, "agent_steps.complete", M{"id": step.ID, "runId": run.ID, "leaseToken": run.Data["leaseToken"], "checkpointId": checkpoint.ID, "observation": "Confirmed draft before resolving conflict"})
	unknown := prepareAgentStep(t, mf, 2, M{"action": "resolve", "paths": []string{"shared.txt"}}, input)
	must(t, f.s, f.worker, "sources.resolve", M{"runId": run.ID, "leaseToken": run.Data["leaseToken"], "resolutions": []any{M{"path": "shared.txt", "deleted": true}}})
	seedAgentUsage(t, mf, "running", 2, 2, "settled", 5)
	must(t, f.s, f.a, "workers.disable", M{"id": f.worker.WorkerID})
	must(t, f.s, f.a, "agent_steps.reconcile", M{"id": unknown.ID, "key": "resolve-unknown-integration", "actionDigest": unknown.Data["actionDigest"], "externalReconciled": true, "reason": "Stopped original execution and discarded the unconfirmed later resolution."})
	w := must(t, f.s, f.a, "workers.register", M{"projectId": f.p.ID, "title": "Replacement integration", "roles": []string{"developer"}, "runtime": "linux", "capabilities": []string{"code"}})
	actor := Actor{ID: "worker:" + w.ID, Kind: "worker", ProjectID: f.p.ID, WorkerID: w.ID, Roles: []string{"developer"}}
	// Expiry processing first makes the original run inactive and its task pending.
	next := must(t, f.s, actor, "tasks.claim", M{})
	if next.Kind != "run" || S(Obj(next.Data["agentResume"]), "stepId") != step.ID || len(Obj(next.Data["integrationResolutions"])) != 0 || S(next.Data, "integrationResolutionDigest") != Hash(M{}) {
		task, _ := f.s.Store.Get(S(run.Data, "taskId"))
		t.Fatal("unconfirmed later decision prevented or contaminated confirmed restoration", next.Kind, task.Status, task.Data["reason"])
	}
}

func TestAgentProgressUploadRejectsRevokedCredentialBeforeReadingAndAuditsDenial(t *testing.T) {
	f, repo, meta := checkpointFixture(t, "code")
	step := prepareAgentStep(t, f, 1, M{"action": "write", "path": "draft", "content": "bytes"}, M{"repositoryId": repo.ID, "commit": meta["commit"]})
	actor := f.worker
	actor.CredentialHash = "revoked-credential-reference"
	body := &unreadSource{}
	if _, e := f.s.UploadAgentCheckpoint(context.Background(), actor, step.ID, S(meta, "leaseToken"), body); e == nil || body.reads != 0 {
		t.Fatal("revoked actor reached archive parsing", e, body.reads)
	}
	audits, e := f.s.Store.Audits(f.p.ID)
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, audit := range audits {
		if audit.Action == "denied:agent.checkpoint" && S(audit.Detail, "code") == "forbidden" {
			found = true
		}
	}
	if !found {
		t.Fatal("pre-stream authorization denial was not audited")
	}
}

func TestAgentProgressPendingIntentSurvivesRetryAndCannotBeReplaced(t *testing.T) {
	f := newAllowanceFixture(t)
	input := M{"runId": f.run.ID, "leaseToken": f.run.Data["leaseToken"], "step": 1, "action": M{"action": "run", "command": []string{"sh", "-c", "one external effect"}}, "source": M{"repositoryId": "", "commit": ""}}
	intent := must(t, f.s, f.worker, "agent_steps.prepare", input)
	if intent.Kind != "agent_step" || intent.Status != "prepared" || S(intent.Data, "runId") != f.run.ID {
		t.Fatal("missing durable original action intent", intent)
	}
	if replay := must(t, f.s, f.worker, "agent_steps.prepare", input); replay.ID != intent.ID {
		t.Fatal("duplicate preparation created another action", replay)
	}
	changed := copyM(input)
	changed["action"] = M{"action": "run", "command": []string{"a different effect"}}
	if _, e := f.s.Command(context.Background(), f.worker, "agent_steps.prepare", changed); e == nil {
		t.Fatal("same sequence accepted a different action")
	}
	seedAgentUsage(t, f, "failed", 1, 1, "settled", 5)
	if _, e := f.s.Command(context.Background(), f.a, "tasks.retry", M{"id": S(f.run.Data, "taskId")}); e == nil {
		t.Fatal("retry bypassed an action with unknown effects")
	}
	next := must(t, f.s, f.register(t, "replacement"), "tasks.claim", M{})
	if next.Kind == "run" {
		t.Fatal("replacement repeated an unconfirmed action", next)
	}
}

func TestAgentProgressUnknownOldPlanFencesReplacementPlan(t *testing.T) {
	f := newAllowanceFixture(t)
	prepareAgentStep(t, f, 1, M{"action": "run", "command": []string{"side-effect"}}, M{"repositoryId": "", "commit": ""})
	seedAgentUsage(t, f, "failed", 1, 1, "settled", 5)
	plan := must(t, f.s, f.a, "plans.propose", M{"goalId": f.g.ID, "summary": "Replacement still must reconcile old action", "tasks": []any{M{"id": "next", "title": "New analysis", "kind": "analysis", "role": "developer", "runtime": "linux", "budgetCents": 100}}})
	must(t, f.s, f.a, "plans.approve", M{"id": plan.ID})
	next := must(t, f.s, f.worker, "tasks.claim", M{})
	if next.Kind == "run" {
		t.Fatal("new plan bypassed old unknown external effect")
	}
}

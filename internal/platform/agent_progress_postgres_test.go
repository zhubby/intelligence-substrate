package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestPostgresAgentProgressConcurrentIntentCompletionAndPortableRecovery(t *testing.T) {
	dsn := os.Getenv("IS_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("set IS_TEST_POSTGRES_URL to an isolated PostgreSQL database")
	}
	admin, e := Open(dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer admin.Close()
	schema := NewID("agent_progress_test")
	if _, e = admin.DB.Exec(`CREATE SCHEMA ` + schema); e != nil {
		t.Fatal(e)
	}
	defer admin.DB.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
	u, e := url.Parse(dsn)
	if e != nil {
		t.Fatal(e)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	first, e := Open(u.String())
	if e != nil {
		t.Fatal(e)
	}
	defer first.Close()
	second, e := Open(u.String())
	if e != nil {
		t.Fatal(e)
	}
	defer second.Close()
	root := t.TempDir()
	if evidence := os.Getenv("IS_AGENT_PROGRESS_EVIDENCE_DIR"); evidence != "" {
		root = filepath.Join(evidence, "postgres")
		if e = os.Mkdir(root, 0700); e != nil {
			t.Fatal(e)
		}
	}
	s := &Service{Store: first, Root: filepath.Join(root, "source")}
	other := &Service{Store: second, Root: s.Root}
	owner := Actor{ID: "progress-pg-owner", Kind: "human", Admin: true}
	project := must(t, s, owner, "projects.create", M{"title": "Durable Agent progress"})
	f, repo, meta := checkpointFixtureOn(t, s, owner, project, "code")
	race := func(command string, input M) Resource {
		t.Helper()
		start := make(chan struct{})
		results := make(chan Resource, 2)
		var wg sync.WaitGroup
		for _, service := range []*Service{s, other} {
			wg.Add(1)
			go func(service *Service) {
				defer wg.Done()
				<-start
				r, err := service.Command(context.Background(), f.worker, command, input)
				if err != nil {
					t.Error(err)
					return
				}
				results <- r
			}(service)
		}
		close(start)
		wg.Wait()
		close(results)
		var winner Resource
		count := 0
		for got := range results {
			count++
			if winner.ID != "" && winner.ID != got.ID {
				t.Fatal("concurrent replay duplicated operation")
			}
			winner = got
		}
		if count != 2 {
			t.Fatal("missing acknowledgements", count)
		}
		return winner
	}
	input := M{"runId": f.run.ID, "leaseToken": meta["leaseToken"], "step": 1, "action": M{"action": "run", "command": []string{"actual-command-fixture"}}, "source": M{"repositoryId": repo.ID, "commit": meta["commit"]}}
	step := race("agent_steps.prepare", input)
	body := checkpointTar(t, map[string]string{"binary.dat": string([]byte{0, 255, 2}), "tool.sh": "#!/bin/sh\nexit 7\n"}, "tool.sh")
	checkpoint, e := other.UploadAgentCheckpoint(context.Background(), f.worker, step.ID, S(meta, "leaseToken"), bytes.NewReader(body))
	if e != nil {
		t.Fatal(e)
	}
	confirmed := race("agent_steps.complete", M{"id": step.ID, "runId": f.run.ID, "leaseToken": meta["leaseToken"], "checkpointId": checkpoint.ID, "observation": "exitCode=7\nactual completed command"})
	if confirmed.Status != "completed" {
		t.Fatal(confirmed)
	}
	unknown := prepareAgentStep(t, f, 2, M{"action": "run", "command": []string{"unknown-external-effect"}}, Obj(input["source"]))
	seedAgentUsage(t, f, "running", 2, 2, "settled", 5)
	must(t, s, owner, "workers.disable", M{"id": f.worker.WorkerID})
	archive := filepath.Join(root, "backup.tar.gz")
	if e = s.Backup(context.Background(), archive); e != nil {
		t.Fatal(e)
	}
	dest := filepath.Join(root, "restored")
	if e = Restore(context.Background(), archive, dest, ""); e != nil {
		t.Fatal(e)
	}
	store, e := Open(filepath.Join(dest, "platform.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	restored := &Service{Store: store, Root: dest}
	if e = restored.VerifyIntegrity(context.Background()); e != nil {
		t.Fatal(e)
	}
	worker := must(t, restored, owner, "workers.register", M{"projectId": project.ID, "title": "Replacement", "roles": []string{"developer"}, "runtime": "linux", "capabilities": []string{"code"}})
	actor := Actor{ID: "worker:" + worker.ID, Kind: "worker", WorkerID: worker.ID, ProjectID: project.ID, Roles: []string{"developer"}}
	if next := must(t, restored, actor, "tasks.claim", M{}); next.Kind == "run" {
		t.Fatal("restored unknown intent lost fence")
	}
	decision := M{"id": unknown.ID, "key": "restore-decision", "actionDigest": unknown.Data["actionDigest"], "externalReconciled": true, "reason": "Synthetic owner independently stopped original tool and reconciled its effect."}
	must(t, restored, owner, "agent_steps.reconcile", decision)
	must(t, restored, owner, "tasks.retry", M{"id": S(f.run.Data, "taskId")})
	next := must(t, restored, actor, "tasks.claim", M{})
	if next.Kind != "run" || S(Obj(next.Data["agentResume"]), "stepId") != step.ID || N(Obj(next.Data["agentAllowance"]), "usedModelCalls") != 2 || N(next.Data, "reservedCents") != 98 {
		t.Fatal("restore changed confirmed progress or allowance", next.Kind)
	}
	file, got, e := restored.AgentResumeCheckpoint(context.Background(), actor, next.ID, S(next.Data, "leaseToken"))
	if e != nil {
		t.Fatal(e)
	}
	actual, e := io.ReadAll(file)
	file.Close()
	if e != nil || !bytes.Equal(actual, body) || got.ID != checkpoint.ID {
		t.Fatal("portable restore changed exact checkpoint", e)
	}
	facts := M{"passed": true, "projectId": project.ID, "goalId": f.g.ID, "taskId": S(f.run.Data, "taskId"), "originalRunId": f.run.ID, "confirmedStepId": step.ID, "unknownStepId": unknown.ID, "checkpointId": checkpoint.ID, "checkpointDigest": checkpoint.Data["digest"], "replacementRunId": next.ID, "archive": archive, "restoredDatabase": filepath.Join(dest, "platform.db"), "duplicateIntentAcknowledgements": 2, "duplicateCompletionAcknowledgements": 2, "modelCallsRetained": 2, "remainingCents": 98}
	encoded, _ := json.MarshalIndent(facts, "", "  ")
	if e = os.WriteFile(filepath.Join(root, "proof.json"), append(encoded, '\n'), 0600); e != nil {
		t.Fatal(e)
	}
}

package platform

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestPostgresNativeSigningRepairLeaseReplayAndRestore(t *testing.T) {
	dsn := os.Getenv("IS_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("set an isolated PostgreSQL database")
	}
	for _, target := range []string{"ios-appstore", "macos", "windows"} {
		t.Run(target, func(t *testing.T) {
			root := t.TempDir()
			if evidence := os.Getenv("IS_SIGNING_REPAIR_EVIDENCE_DIR"); evidence != "" {
				root = filepath.Join(evidence, "postgres-"+target)
				if err := os.Mkdir(root, 0700); err != nil {
					t.Fatal(err)
				}
			}
			admin, err := Open(dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer admin.Close()
			schema := NewID("native_repair_test")
			if _, err = admin.DB.Exec("CREATE SCHEMA " + schema); err != nil {
				t.Fatal(err)
			}
			defer admin.DB.Exec("DROP SCHEMA " + schema + " CASCADE")
			u, err := url.Parse(dsn)
			if err != nil {
				t.Fatal(err)
			}
			q := u.Query()
			q.Set("search_path", schema)
			u.RawQuery = q.Encode()
			one, err := Open(u.String())
			if err != nil {
				t.Fatal(err)
			}
			defer one.Close()
			two, err := Open(u.String())
			if err != nil {
				t.Fatal(err)
			}
			defer two.Close()
			s := &Service{Store: one, Root: filepath.Join(root, "pg")}
			other := &Service{Store: two, Root: s.Root}
			owner := Actor{ID: "native-repair-owner", Kind: "human", Admin: true}
			project := must(t, s, owner, "projects.create", M{"title": "Native repair concurrency and restoration"})
			f := setupNativeSigningRepairFixture(t, s, owner, project, target)
			defect, oldRun, oldArtifact := f.rejected(t)
			f.code(t)
			f.unsignedBuild(t)
			system := "darwin"
			if target == "windows" {
				system = "windows"
			}
			w := must(t, s, owner, "workers.register", M{"projectId": project.ID, "title": "Competing dedicated signer", "runtime": system, "roles": []any{"release"}, "capabilities": []any{"build"}})
			actors := []Actor{f.signer, {ID: "worker:" + w.ID, Kind: "worker", WorkerID: w.ID, ProjectID: project.ID, Roles: []string{"release"}}}
			type response struct {
				r Resource
				a Actor
				e error
			}
			responses := make(chan response, 2)
			start := make(chan struct{})
			var group sync.WaitGroup
			for i, svc := range []*Service{s, other} {
				group.Add(1)
				go func(svc *Service, a Actor) {
					defer group.Done()
					<-start
					r, e := svc.Command(context.Background(), a, "tasks.claim", M{})
					responses <- response{r, a, e}
				}(svc, actors[i])
			}
			close(start)
			group.Wait()
			close(responses)
			claims := 0
			var run Resource
			for result := range responses {
				if result.e != nil {
					t.Fatal(result.e)
				}
				if result.r.Kind == "run" {
					claims++
					run, f.signer = result.r, result.a
				}
			}
			if claims != 1 || S(Obj(run.Data["task"]), "signingRepairId") == "" {
				t.Fatal("fresh repair signature did not have one lease", claims, run)
			}
			if target == "macos" {
				must(t, s, f.signer, "tasks.complete", macOSSigningPending(f.androidSigningFixture, run))
				dueMacOSSigning(t, s, S(run.Data, "taskId"))
				run = must(t, s, f.signer, "tasks.claim", M{})
			}
			artifact := f.uploadSigned(t, run)
			completion := f.signingCompletion(run, artifact)
			errors := make(chan error, 2)
			for _, svc := range []*Service{s, other} {
				group.Add(1)
				go func(svc *Service) {
					defer group.Done()
					_, e := svc.Command(context.Background(), f.signer, "tasks.complete", completion)
					errors <- e
				}(svc)
			}
			group.Wait()
			close(errors)
			for e := range errors {
				if e != nil {
					t.Fatal("concurrent completion failed", e)
				}
			}
			qa := must(t, s, f.quality, "tasks.claim", M{})
			must(t, s, f.quality, "tasks.complete", M{"id": qa.ID, "leaseToken": S(qa.Data, "leaseToken"), "exitCode": 0, "result": M{}})
			defect, _ = one.Get(defect.ID)
			if defect.Status != "closed" || len(Items(defect.Data["signingRepairs"])) != 1 {
				t.Fatal("PostgreSQL lost completed native repair", defect)
			}
			archive, destination := filepath.Join(root, "repair.tar.gz"), filepath.Join(root, "restored")
			if err = s.Backup(context.Background(), archive); err != nil {
				t.Fatal(err)
			}
			if err = Restore(context.Background(), archive, destination, ""); err != nil {
				t.Fatal(err)
			}
			restoredStore, err := Open(filepath.Join(destination, "platform.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer restoredStore.Close()
			restored := &Service{Store: restoredStore, Root: destination}
			resources, err := one.List("", "", "")
			if err != nil {
				t.Fatal(err)
			}
			for _, before := range resources {
				after, e := restoredStore.Get(before.ID)
				if e != nil || Hash(before) != Hash(after) {
					t.Fatal("backup lost exact resource", before.ID, e)
				}
			}
			if err = restored.Store.Update(context.Background(), func(tx *Tx) error { return restored.validateSigningArtifact(tx, artifact) }); err != nil {
				t.Fatal("restored repair authority unusable", err)
			}
			for _, actor := range []Actor{f.builder, f.signer, f.quality} {
				if idle := must(t, restored, actor, "tasks.claim", M{}); idle.Kind != "worker" {
					t.Fatal("restore repeated completed repair", idle)
				}
			}
			if err = restored.VerifyIntegrity(context.Background()); err != nil {
				t.Fatal(err)
			}
			for name, value := range map[string]any{
				"resources.json": resources,
				"result.json":    M{"passed": true, "target": target, "resourceCount": len(resources), "oneClaim": claims, "duplicateCompletions": 2, "defectId": defect.ID, "oldSigningRunId": oldRun.ID, "newSigningRunId": run.ID, "oldArtifactId": oldArtifact.ID, "newArtifactId": artifact.ID, "verifiedRunId": qa.ID, "realNativeSignature": false},
			} {
				body, e := json.MarshalIndent(value, "", "  ")
				if e != nil {
					t.Fatal(e)
				}
				if e = os.WriteFile(filepath.Join(root, name), body, 0600); e != nil {
					t.Fatal(e)
				}
			}
		})
	}
}

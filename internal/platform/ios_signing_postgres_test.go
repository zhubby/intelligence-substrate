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

func TestPostgresIOSSigningConcurrentLeaseCompletionAndExactRestore(t *testing.T) {
	dsn := os.Getenv("IS_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("set isolated PostgreSQL database")
	}
	root := t.TempDir()
	if evidence := os.Getenv("IS_IOS_SIGNING_EVIDENCE_DIR"); evidence != "" {
		root = filepath.Join(evidence, "postgres-proof")
		if err := os.Mkdir(root, 0700); err != nil {
			t.Fatal(err)
		}
	}
	admin, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := NewID("ios_signing_test")
	if _, err = admin.DB.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatal(err)
	}
	defer admin.DB.Exec("DROP SCHEMA " + schema + " CASCADE")
	u, _ := url.Parse(dsn)
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
	s, other := &Service{Store: one, Root: filepath.Join(root, "pg")}, &Service{Store: two, Root: filepath.Join(root, "pg")}
	owner := Actor{ID: "ios-signing-owner", Kind: "human", Admin: true}
	project := must(t, s, owner, "projects.create", M{"title": "App Store signature concurrency"})
	f := setupIOSSigningFixture(t, s, owner, project)
	f.build(t)
	type result struct {
		resource Resource
		err      error
	}
	start := make(chan struct{})
	outcomes := make(chan result, 2)
	var group sync.WaitGroup
	for _, svc := range []*Service{s, other} {
		group.Add(1)
		go func(svc *Service) {
			defer group.Done()
			<-start
			r, e := svc.Command(context.Background(), f.signer, "tasks.claim", M{})
			outcomes <- result{r, e}
		}(svc)
	}
	close(start)
	group.Wait()
	close(outcomes)
	claims, busy := 0, 0
	var run Resource
	for v := range outcomes {
		if v.err != nil {
			clarificationFault(t, v.err, 409)
			if v.err.Error() != "节点已有正在执行的工作项" {
				t.Fatal(v.err)
			}
			busy++
			continue
		}
		if v.resource.Kind == "run" {
			claims++
			run = v.resource
		}
	}
	if claims != 1 || busy != 1 {
		t.Fatal("concurrent signer did not get exactly one lease", claims, busy)
	}
	artifact := f.uploadSigned(t, run)
	completion := iOSSigningCompletion(f, run, artifact)
	start = make(chan struct{})
	errors := make(chan error, 2)
	for _, svc := range []*Service{s, other} {
		group.Add(1)
		go func(svc *Service) {
			defer group.Done()
			<-start
			_, e := svc.Command(context.Background(), f.signer, "tasks.complete", completion)
			errors <- e
		}(svc)
	}
	close(start)
	group.Wait()
	close(errors)
	for e := range errors {
		if e != nil {
			t.Fatal("idempotent completion failed", e)
		}
	}
	task, _ := one.Get(S(run.Data, "taskId"))
	if task.Status != "succeeded" || N(task.Data, "attempts") != 1 {
		t.Fatal("signature repeated or lost final state", task)
	}
	archive := filepath.Join(root, "completed.tar.gz")
	if err = s.Backup(context.Background(), archive); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(root, "restored")
	if err = Restore(context.Background(), archive, destination, ""); err != nil {
		t.Fatal(err)
	}
	restoredStore, err := Open(filepath.Join(destination, "platform.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer restoredStore.Close()
	resources, err := one.List("", "", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, before := range resources {
		after, e := restoredStore.Get(before.ID)
		if e != nil || Hash(after) != Hash(before) {
			t.Fatal("restored signing resource differs", before.ID, e)
		}
	}
	restored := &Service{Store: restoredStore, Root: destination}
	if idle := must(t, restored, f.signer, "tasks.claim", M{}); idle.Kind != "worker" {
		t.Fatal("completed restore resigned an IPA")
	}
	if err = restored.VerifyArtifact(artifact); err != nil {
		t.Fatal(err)
	}
	resultData := M{"passed": true, "resourceCount": len(resources), "runId": run.ID, "taskId": task.ID, "inputArtifactId": f.unsigned.ID, "artifactId": artifact.ID, "oneClaim": claims, "oneBusy": busy, "duplicateCompletions": 2, "realAppleSignature": false}
	for name, value := range map[string]any{"resources.json": resources, "result.json": resultData} {
		body, e := json.MarshalIndent(value, "", "  ")
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(root, name), body, 0600); e != nil {
			t.Fatal(e)
		}
	}
}

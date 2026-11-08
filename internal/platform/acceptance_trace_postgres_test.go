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

func TestPostgresAcceptanceTraceConsistentRevisionAndPortableRestore(t *testing.T) {
	dsn := os.Getenv("IS_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("set IS_TEST_POSTGRES_URL to an isolated PostgreSQL database")
	}
	admin, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := NewID("trace_test")
	if _, err = admin.DB.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	defer admin.DB.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	first, err := Open(u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Open(u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	root := t.TempDir()
	if evidence := os.Getenv("IS_TRACE_EVIDENCE_DIR"); evidence != "" {
		root = filepath.Join(evidence, "postgres-proof")
		if err = os.Mkdir(root, 0700); err != nil {
			t.Fatal(err)
		}
	}
	s := &Service{Store: first, Root: filepath.Join(root, "source")}
	other := &Service{Store: second, Root: s.Root}
	owner := Actor{ID: "trace-owner", Kind: "human", Admin: true}
	project := must(t, s, owner, "projects.create", M{"title": "Traceability snapshot"})
	f := setupQualityFixture(t, s, owner, project)
	must(t, s, f.quality, "quality.bind", f.input("durable-trace"))
	build, artifact, _ := f.build(t)
	run := must(t, s, f.verifier, "tasks.claim", M{})
	finished := must(t, s, f.verifier, "tasks.complete", M{"id": run.ID, "leaseToken": S(run.Data, "leaseToken"), "exitCode": 0, "result": M{}})
	release := must(t, s, owner, "releases.create", M{"goalId": f.goal.ID, "version": "1.0.0", "commit": f.commit, "artifactIds": []any{artifact.ID}})
	before := readTrace(t, f, 0)
	if !before.Criteria[0].Targets[0].Covered || !before.Artifacts[0].Build.Verified {
		t.Fatal("missing baseline trace", before)
	}
	// Separate SQL connections race a read against a baseline change. The report
	// must contain one complete revision, never old evidence under new criteria.
	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make(chan AcceptanceTrace, 8)
	failures := make(chan error, 9)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			r, e := other.AcceptanceTrace(context.Background(), owner, project.ID, f.goal.ID, 0)
			if e != nil {
				failures <- e
			} else {
				results <- r
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		_, e := s.Command(context.Background(), owner, "goals.revise", M{"id": f.goal.ID, "reason": "New acceptance scope", "criteria": []any{M{"id": "ticket", "text": "Revised criterion", "method": "manual"}}})
		if e != nil {
			failures <- e
		}
	}()
	close(start)
	wg.Wait()
	close(results)
	close(failures)
	for e := range failures {
		t.Fatal(e)
	}
	reads := 0
	for r := range results {
		reads++
		if r.RequirementRevision == 1 {
			if r.Criteria[0].Text != "A customer can submit a request" || !r.Criteria[0].Targets[0].Covered {
				t.Fatal("mixed old snapshot", r)
			}
		} else if r.RequirementRevision == 2 {
			if r.Criteria[0].Text != "Revised criterion" || r.Criteria[0].Targets[0].Covered || len(r.Artifacts) != 0 {
				t.Fatal("mixed new snapshot", r)
			}
		} else {
			t.Fatal("unexpected snapshot revision", r)
		}
	}
	archive := filepath.Join(root, "backup.tar.gz")
	if err = s.Backup(context.Background(), archive); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(root, "restored")
	if err = Restore(context.Background(), archive, destination, ""); err != nil {
		t.Fatal(err)
	}
	store, err := Open(filepath.Join(destination, "platform.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	restored := &Service{Store: store, Root: destination}
	historical, err := restored.AcceptanceTrace(context.Background(), owner, project.ID, f.goal.ID, 1)
	if err != nil || historical.Current || historical.Criteria[0].Targets[0].Covered || historical.Artifacts[0].Evidence[0].Status != "stale" || historical.Artifacts[0].Build.RunID != build.ID || historical.Releases[0].Status != "stale" {
		t.Fatal("restored history asserted current acceptance", historical, err)
	}
	original, err := s.AcceptanceTrace(context.Background(), owner, project.ID, f.goal.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	original.GeneratedAt, historical.GeneratedAt = "", ""
	if Hash(original) != Hash(historical) {
		t.Fatal("restore changed exact trace graph", original, historical)
	}
	if err = restored.VerifyIntegrity(context.Background()); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("IS_TRACE_EVIDENCE_DIR") != "" {
		body, _ := json.MarshalIndent(M{"passed": true, "concurrentReads": reads, "projectId": project.ID, "goalId": f.goal.ID, "artifactId": artifact.ID, "digest": artifact.Data["digest"], "commit": f.commit, "repositoryId": f.repository.ID, "buildRunId": build.ID, "qualityRunId": run.ID, "evidenceId": finished.Data["evidenceId"], "releaseId": release.ID, "before": before, "restoredHistorical": historical}, "", "  ")
		if err = os.WriteFile(filepath.Join(root, "result.json"), body, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

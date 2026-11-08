package platform

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestPostgresReleaseFeedbackConcurrentUploadAndHistoricalRestore(t *testing.T) {
	dsn := os.Getenv("IS_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("set IS_TEST_POSTGRES_URL to an isolated PostgreSQL database")
	}
	admin, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := NewID("feedback_test")
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
	if evidence := os.Getenv("IS_FEEDBACK_EVIDENCE_DIR"); evidence != "" {
		root = filepath.Join(evidence, "feedback-postgres-proof")
		if err = os.Mkdir(root, 0700); err != nil {
			t.Fatal(err)
		}
	}
	s := &Service{Store: first, Root: filepath.Join(root, "source")}
	other := &Service{Store: second, Root: s.Root}
	owner := Actor{ID: "feedback-postgres-owner", Kind: "human", Admin: true}
	project := must(t, s, owner, "projects.create", M{"title": "Historical feedback concurrency"})
	f := publishFeedbackFixture(t, metricFixtureOn(t, s, owner, project))
	must(t, s, owner, "goals.revise", M{"id": f.goal.ID, "targets": []any{"windows"}, "reason": "Changed requirement scope after publication"})
	start := make(chan struct{})
	results := make(chan Resource, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, svc := range []*Service{s, other} {
		wg.Add(1)
		go func(svc *Service) {
			defer wg.Done()
			<-start
			r, err := svc.UploadReleaseReport(context.Background(), owner, feedbackInput(f), strings.NewReader("Synthetic late feedback after requirements revision\n"))
			if err != nil {
				errs <- err
			} else {
				results <- r
			}
		}(svc)
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	var report Resource
	count := 0
	for r := range results {
		count++
		if report.ID != "" && report.ID != r.ID {
			t.Fatal("parallel upload duplicated report")
		}
		report = r
	}
	if count != 2 || revision(report) != 1 {
		t.Fatal("concurrent acknowledgement lost source", count, report)
	}
	in := f.input("production_defect", "late-production-ticket")
	in["proofArtifactId"], in["requirementRevision"] = report.ID, 1
	fact := must(t, other, owner, "measurements.record", in)
	archive := filepath.Join(root, "backup.tar.gz")
	if err = s.Backup(context.Background(), archive); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(root, "restored")
	if err = Restore(context.Background(), archive, dest, ""); err != nil {
		t.Fatal(err)
	}
	store, err := Open(filepath.Join(dest, "platform.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	restored := &Service{Store: store, Root: dest}
	if err = restored.VerifyIntegrity(context.Background()); err != nil {
		t.Fatal(err)
	}
	replay, err := restored.UploadReleaseReport(context.Background(), owner, feedbackInput(f), strings.NewReader("Synthetic late feedback after requirements revision\n"))
	if err != nil || replay.ID != report.ID {
		t.Fatal("restore lost immutable upload replay", err, replay)
	}
	if repeated := must(t, restored, owner, "measurements.record", in); repeated.ID != fact.ID {
		t.Fatal("restored measurement duplicated defect")
	}
	history := metricJSON(t, restored, owner, project.ID, f.goal.ID, 1)
	current := metricJSON(t, restored, owner, project.ID, f.goal.ID, 0)
	if N(history, "reportedProductionDefects") != 1 || N(current, "reportedProductionDefects") != 0 {
		t.Fatal("restore mixed requirement versions", history, current)
	}
	facts := M{"passed": true, "syntheticOnly": true, "projectId": project.ID, "goalId": f.goal.ID, "releaseId": f.release.ID, "releaseBinding": S(f.release.Data, "binding"), "packageId": f.artifact.ID, "packageDigest": S(f.artifact.Data, "digest"), "reportId": report.ID, "reportDigest": S(report.Data, "digest"), "measurementId": fact.ID, "historicalRevision": 1, "currentRevision": 2, "concurrentAcknowledgements": count, "paidModelCalled": false}
	b, err := json.MarshalIndent(facts, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "facts.json"), append(b, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
}

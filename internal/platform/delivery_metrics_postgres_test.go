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

func TestPostgresDeliveryMeasurementConcurrentReplayAndRestore(t *testing.T) {
	dsn := os.Getenv("IS_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("set IS_TEST_POSTGRES_URL to an isolated PostgreSQL database")
	}
	admin, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := NewID("metrics_test")
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
	if evidence := os.Getenv("IS_METRIC_EVIDENCE_DIR"); evidence != "" {
		root = filepath.Join(evidence, "metrics-postgres-proof")
		if err = os.Mkdir(root, 0700); err != nil {
			t.Fatal(err)
		}
	}
	s := &Service{Store: first, Root: filepath.Join(root, "source")}
	other := &Service{Store: second, Root: s.Root}
	owner := Actor{ID: "metric-postgres-owner", Name: "Synthetic owner", Kind: "human", Admin: true}
	project := must(t, s, owner, "projects.create", M{"title": "PostgreSQL measurement proof"})
	f := metricFixtureOn(t, s, owner, project)
	in := f.input("business_acceptance", "concurrent-review")
	start := make(chan struct{})
	results := make(chan Resource, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, svc := range []*Service{s, other} {
		wg.Add(1)
		go func(svc *Service) {
			defer wg.Done()
			<-start
			r, err := svc.Command(context.Background(), owner, "measurements.record", in)
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
	var original Resource
	count := 0
	for r := range results {
		count++
		if original.ID != "" && original.ID != r.ID {
			t.Fatal("parallel replay duplicated fact")
		}
		original = r
	}
	if count != 2 {
		t.Fatal("lost concurrent acknowledgement", count)
	}
	must(t, s, owner, "measurements.void", M{"id": original.ID, "key": "void", "reason": "Synthetic source correction"})
	corrected := copyM(in)
	corrected["key"] = "corrected"
	corrected["result"] = "passed"
	newFact := must(t, other, owner, "measurements.record", corrected)
	preview := must(t, s, owner, "measurements.record", f.input("preview", "browser-observation"))
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
	report := metricJSON(t, restored, owner, project.ID, f.goal.ID, 0)
	if N(report, "acceptanceSamples") != 1 || N(report, "firstPassRate") != 1 || N(report, "previewSamples") != 1 || len(Items(report["observations"])) != 3 {
		t.Fatal("restore lost corrected business history", report)
	}
	old := must(t, restored, owner, "measurements.record", in)
	if old.ID != original.ID || old.Status != "voided" {
		t.Fatal("restored replay resurrected voided fact")
	}
	facts := M{"passed": true, "syntheticOnly": true, "projectId": project.ID, "goalId": f.goal.ID, "originalId": original.ID, "correctedId": newFact.ID, "previewId": preview.ID, "proofId": f.proof.ID, "proofDigest": S(f.proof.Data, "digest"), "firstPassRate": 1, "observations": 3, "concurrentReplayAcknowledgements": 2, "paidModelCalled": false}
	data, err := json.MarshalIndent(facts, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "facts.json"), append(data, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
}

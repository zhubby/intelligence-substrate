package platform

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// Catch process-local dedup/ownership locking and omitted nonempty idempotency
// history. Two independent SQL clients contend, then the real archive restores.
func TestPostgresRuntimeIncidentsSerializeAndRestoreOwnership(t *testing.T) {
	dsn := os.Getenv("IS_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("set IS_TEST_POSTGRES_URL to an isolated PostgreSQL database")
	}
	admin, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := NewID("runtime_incident_test")
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
	store, err := Open(u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	other, err := Open(u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	s := &Service{Store: store, Root: t.TempDir()}
	second := &Service{Store: other, Root: s.Root}
	a := Actor{ID: "postgres-runtime-owner", Kind: "human", Admin: true}
	project := must(t, s, a, "projects.create", M{"title": "Concurrent runtime incidents"})
	d := resource("deployment", "Synthetic deployed version", project.ID, "", M{"application": "feedback", "workerId": "node", "activatedAt": time.Now().Add(-5 * time.Minute).UTC().Format(time.RFC3339Nano)})
	if err = store.Update(context.Background(), func(tx *Tx) error { return tx.Put(d) }); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errors := make(chan error, 2)
	for _, service := range []*Service{s, second} {
		wg.Add(1)
		go func() { defer wg.Done(); errors <- service.ReconcileRuntimeSignals(context.Background()) }()
	}
	wg.Wait()
	for range 2 {
		if err = <-errors; err != nil {
			t.Fatal(err)
		}
	}
	incidents := runtimeIncidents(t, s, d, "monitor_signal_lost")
	if len(incidents) != 1 {
		t.Fatal("independent PostgreSQL watchdogs created multiple incidents", incidents)
	}
	actors := []Actor{a, {ID: "postgres-other-operator", Kind: "human", Admin: true}}
	inputs := []M{{"id": incidents[0].ID, "key": "first", "note": "First operator checks monitoring"}, {"id": incidents[0].ID, "key": "second", "note": "Second operator checks monitoring"}}
	type outcome struct {
		index int
		err   error
	}
	results := make(chan outcome, 2)
	for i, service := range []*Service{s, second} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := service.Command(context.Background(), actors[i], "runtime.incident.acknowledge", inputs[i])
			results <- outcome{i, err}
		}()
	}
	wg.Wait()
	winner, successes := -1, 0
	for range 2 {
		result := <-results
		if result.err == nil {
			winner = result.index
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent first ownership accepted %d operators", successes)
	}
	var remembered int
	if err = store.DB.QueryRow(`SELECT COUNT(*) FROM idempotency`).Scan(&remembered); err != nil || remembered != 1 {
		t.Fatal("successful ownership was not remembered exactly once", err, remembered)
	}
	archive := filepath.Join(t.TempDir(), "runtime-backup.tar.gz")
	if err = s.Backup(context.Background(), archive); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "restored")
	if err = Restore(context.Background(), archive, destination, ""); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(filepath.Join(destination, "platform.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	recovered := &Service{Store: restored, Root: destination}
	if err = recovered.ReconcileRuntimeSignals(context.Background()); err != nil {
		t.Fatal(err)
	}
	after := runtimeIncidents(t, recovered, d, "monitor_signal_lost")
	if len(after) != 1 || S(after[0].Data, "acknowledgedBy") != actors[winner].ID || after[0].Status != "open" {
		t.Fatal("restore lost first ownership or duplicated the incident", after)
	}
	if err = restored.DB.QueryRow(`SELECT COUNT(*) FROM idempotency`).Scan(&remembered); err != nil || remembered != 1 {
		t.Fatal("restore omitted nonempty idempotency history", err, remembered)
	}
	replay := must(t, recovered, actors[winner], "runtime.incident.acknowledge", inputs[winner])
	if replay.ID != incidents[0].ID || S(replay.Data, "acknowledgedAt") != S(after[0].Data, "acknowledgedAt") {
		t.Fatal("restored retry changed the first ownership receipt")
	}
	if err = recovered.VerifyIntegrity(context.Background()); err != nil {
		t.Fatal(err)
	}
}

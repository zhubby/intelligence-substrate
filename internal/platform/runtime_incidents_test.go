package platform

import (
	"context"
	"testing"
	"time"
)

// Catch a missing/unsafe ownership transition: acknowledgement must persist
// ownership without making a failed application healthy or independently passed.
func TestRuntimeIncidentAcknowledgementPreservesFailureAndFirstOwner(t *testing.T) {
	s, officer, worker, _, d := deployedFixture(t)
	for i := 1; i <= 3; i++ {
		d = reportRuntime(t, s, worker, d, i, "unhealthy")
	}
	incidents, err := s.Store.List(d.ProjectID, "incident", d.GoalID)
	if err != nil || len(incidents) != 1 {
		t.Fatal("missing real failed-probe incident", err)
	}
	incident := incidents[0]
	in := M{"id": incident.ID, "key": "claim-once", "note": "Investigating current application logs and the failed health endpoint"}
	acknowledged := must(t, s, officer, "runtime.incident.acknowledge", in)
	if acknowledged.Status != "open" || S(acknowledged.Data, "acknowledgedBy") != officer.ID || S(acknowledged.Data, "acknowledgedAt") == "" || S(acknowledged.Data, "acknowledgementNote") != in["note"] {
		t.Fatal("acknowledgement did not preserve an open incident and its owner", acknowledged)
	}
	replay := must(t, s, officer, "runtime.incident.acknowledge", in)
	if S(replay.Data, "acknowledgedAt") != S(acknowledged.Data, "acknowledgedAt") {
		t.Fatal("idempotent retry changed the ownership time")
	}
	changed := copyM(in)
	changed["note"] = "Changed request with reused key"
	if _, err = s.Command(context.Background(), officer, "runtime.incident.acknowledge", changed); err == nil {
		t.Fatal("conflicting idempotency replay overwritten ownership")
	}
	other := Actor{ID: "another-human-operator", Kind: "human", Admin: true}
	if _, err = s.Command(context.Background(), other, "runtime.incident.acknowledge", M{"id": incident.ID, "key": "take-over", "note": "Replace the first owner"}); err == nil {
		t.Fatal("another request silently replaced the first incident owner")
	}
	current, err := s.Store.Get(d.ID)
	if err != nil || S(current.Data, "health") != "unhealthy" || S(current.Data, "verificationStatus") != "required" {
		t.Fatal("ownership altered runtime health or independent acceptance", err)
	}
	for i := 4; i <= 5; i++ {
		reportRuntime(t, s, worker, d, i, "healthy")
	}
	resolved, err := s.Store.Get(incident.ID)
	if err != nil || resolved.Status != "resolved" || S(resolved.Data, "acknowledgedBy") != officer.ID {
		t.Fatal("trusted recovery lost incident ownership", err, resolved)
	}
	if _, err = s.Command(context.Background(), other, "runtime.incident.acknowledge", M{"id": incident.ID, "key": "after-recovery", "note": "Claim a resolved incident"}); err == nil {
		t.Fatal("resolved incident accepted a new ownership transition")
	}
}

func runtimeIncidents(t *testing.T, s *Service, d Resource, kind string) []Resource {
	t.Helper()
	all, err := s.Store.List(d.ProjectID, "incident", d.GoalID)
	if err != nil {
		t.Fatal(err)
	}
	out := []Resource{}
	for _, r := range all {
		if S(r.Data, "type") == kind {
			out = append(out, r)
		}
	}
	return out
}

func reconcileRuntimeAt(t *testing.T, s *Service, at time.Time) {
	t.Helper()
	if err := s.Store.Update(context.Background(), func(tx *Tx) error {
		return reconcileRuntimeSignals(tx, at)
	}); err != nil {
		t.Fatal(err)
	}
}

// Catch missing deadlines, repeated alerts and volatile process-local dedup.
func TestRuntimeSignalGraceExpiryAndRestart(t *testing.T) {
	s, _, _, _, d := deployedFixture(t)
	activated, err := time.Parse(time.RFC3339Nano, S(d.Data, "activatedAt"))
	if err != nil {
		t.Fatal(err)
	}
	reconcileRuntimeAt(t, s, activated.Add(89*time.Second))
	if all := runtimeIncidents(t, s, d, "monitor_signal_lost"); len(all) != 0 {
		t.Fatal("first observation was not given its initial grace period")
	}
	reconcileRuntimeAt(t, s, activated.Add(90*time.Second))
	all := runtimeIncidents(t, s, d, "monitor_signal_lost")
	if len(all) != 1 || all[0].Status != "open" || S(all[0].Data, "deploymentId") != d.ID || S(all[0].Data, "signalDeadline") != activated.Add(90*time.Second).UTC().Format(time.RFC3339Nano) {
		t.Fatal("missing durable signal-loss incident at its exact deadline", all)
	}
	other, err := Open(s.Store.DSN)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	restarted := &Service{Store: other, Root: s.Root}
	reconcileRuntimeAt(t, restarted, activated.Add(10*time.Minute))
	all = runtimeIncidents(t, restarted, d, "monitor_signal_lost")
	if len(all) != 1 {
		t.Fatal("a second process duplicated the same open incident", all)
	}
	audits, err := other.Audits(d.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	opens := 0
	for _, audit := range audits {
		if audit.Action == "runtime.incident.opened" && audit.ResourceID == all[0].ID {
			opens++
		}
	}
	if opens != 1 {
		t.Fatalf("wanted one incident-open audit, got %d", opens)
	}
}

func expireRuntimeSignal(t *testing.T, s *Service, d Resource) {
	t.Helper()
	if err := s.Store.Update(context.Background(), func(tx *Tx) error {
		current, err := tx.Get(d.ID)
		if err != nil {
			return err
		}
		current.Data["validUntil"] = time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)
		return tx.Put(current)
	}); err != nil {
		t.Fatal(err)
	}
}

// Catch treating a resumed failed probe as an application recovery, or a
// deployment/ownership transition as evidence that the signal has resumed.
func TestRuntimeSignalRecoveryIsSeparateFromApplicationHealth(t *testing.T) {
	s, officer, worker, _, d := deployedFixture(t)
	for i := 1; i <= 3; i++ {
		d = reportRuntime(t, s, worker, d, i, "unhealthy")
	}
	expireRuntimeSignal(t, s, d)
	if err := s.ReconcileRuntimeSignals(context.Background()); err != nil {
		t.Fatal(err)
	}
	application := runtimeIncidents(t, s, d, "application_unhealthy")
	signals := runtimeIncidents(t, s, d, "monitor_signal_lost")
	if len(application) != 1 || len(signals) != 1 || application[0].Status != "open" || signals[0].Status != "open" {
		t.Fatal("signal loss replaced rather than coexisted with the application incident", application, signals)
	}
	must(t, s, officer, "runtime.incident.acknowledge", M{"id": signals[0].ID, "key": "signal-owner", "note": "Checking the monitor process"})
	d = reportRuntime(t, s, worker, d, 4, "unhealthy")
	signal, _ := s.Store.Get(signals[0].ID)
	app, _ := s.Store.Get(application[0].ID)
	if signal.Status != "resolved" || S(signal.Data, "resolutionReason") != "signal_resumed" || S(signal.Data, "recoveredDeploymentId") != d.ID || N(signal.Data, "recoverySequence") != 4 || S(signal.Data, "acknowledgedBy") != officer.ID || app.Status != "open" || N(d.Data, "failures") != 1 {
		t.Fatal("signal recovery incorrectly cleared the health incident or lost causal evidence", signal, app, d)
	}
	for i := 5; i <= 6; i++ {
		d = reportRuntime(t, s, worker, d, i, "healthy")
	}
	app, _ = s.Store.Get(application[0].ID)
	if app.Status != "resolved" || S(app.Data, "resolutionReason") != "healthy_probes" {
		t.Fatal("two accepted healthy probes did not recover the application", app)
	}
	expireRuntimeSignal(t, s, d)
	if err := s.ReconcileRuntimeSignals(context.Background()); err != nil {
		t.Fatal(err)
	}
	if again := runtimeIncidents(t, s, d, "monitor_signal_lost"); len(again) != 2 {
		t.Fatal("a later outage reused resolved history", again)
	}
}

func TestRuntimeSignalGapRecordedWithoutWatchdogTraffic(t *testing.T) {
	s, _, worker, _, d := deployedFixture(t)
	d = reportRuntime(t, s, worker, d, 1, "healthy")
	expireRuntimeSignal(t, s, d)
	d = reportRuntime(t, s, worker, d, 2, "unhealthy")
	all := runtimeIncidents(t, s, d, "monitor_signal_lost")
	if len(all) != 1 || all[0].Status != "resolved" || N(all[0].Data, "recoverySequence") != 2 || N(d.Data, "failures") != 1 {
		t.Fatal("returning report erased a real signal gap", all, d)
	}
}

func TestRuntimeSignalRejectsNonAdvancingObservationTime(t *testing.T) {
	s, _, worker, _, d := deployedFixture(t)
	d = reportRuntime(t, s, worker, d, 1, "healthy")
	before, err := time.Parse(time.RFC3339Nano, S(d.Data, "lastCheckedAt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, checked := range []time.Time{before, before.Add(-time.Millisecond)} {
		_, err := s.Command(context.Background(), worker, "runtime.report", M{"externalId": S(d.Data, "externalId"), "releaseId": S(d.Data, "releaseId"), "binding": S(d.Data, "binding"), "sequence": 2, "health": "unhealthy", "checkedAt": checked.UTC().Format(time.RFC3339Nano), "statusCode": 503, "latencyMs": 1})
		if err == nil {
			t.Fatal("higher sequence replayed a non-advancing probe time")
		}
	}
	current, _ := s.Store.Get(d.ID)
	if N(current.Data, "lastSequence") != 1 || S(current.Data, "health") != "healthy" {
		t.Fatal("rejected observation altered runtime state", current)
	}
}

// Catch false recovery from a queued report that is newer than the last
// sequence but already outside the 90-second signal lifetime on arrival.
func TestRuntimeSignalExpiredObservationCannotResolveCurrentOutage(t *testing.T) {
	s, _, worker, _, d := deployedFixture(t)
	at := time.Now()
	if err := s.Store.Update(context.Background(), func(tx *Tx) error {
		current, err := tx.Get(d.ID)
		if err != nil {
			return err
		}
		current.Data["activatedAt"] = at.Add(-3 * time.Minute).UTC().Format(time.RFC3339Nano)
		current.Data["lastCheckedAt"] = at.Add(-110 * time.Second).UTC().Format(time.RFC3339Nano)
		current.Data["validUntil"] = at.Add(-20 * time.Second).UTC().Format(time.RFC3339Nano)
		current.Data["lastSequence"] = 1
		return tx.Put(current)
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReconcileRuntimeSignals(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, err := s.Command(context.Background(), worker, "runtime.report", M{"externalId": S(d.Data, "externalId"), "releaseId": S(d.Data, "releaseId"), "binding": S(d.Data, "binding"), "sequence": 2, "health": "healthy", "statusCode": 200, "latencyMs": 1, "checkedAt": at.Add(-100 * time.Second).UTC().Format(time.RFC3339Nano)})
	if err == nil {
		t.Fatal("an expired queued observation falsely resolved the current outage")
	}
	incidents := runtimeIncidents(t, s, d, "monitor_signal_lost")
	current, _ := s.Store.Get(d.ID)
	if len(incidents) != 1 || incidents[0].Status != "open" || N(current.Data, "lastSequence") != 1 {
		t.Fatal("expired observation changed runtime or incident state", incidents, current)
	}
	reportRuntime(t, s, worker, d, 2, "healthy")
	incidents = runtimeIncidents(t, s, d, "monitor_signal_lost")
	if len(incidents) != 1 || incidents[0].Status != "resolved" {
		t.Fatal("fresh observation did not resolve the same outage", incidents)
	}
}

func TestRuntimeSignalNewActivationNeedsActualRecoveryAndLegacyHealthEventsStillResolve(t *testing.T) {
	s, officer, worker, _, first := deployedFixture(t)
	for i := 1; i <= 3; i++ {
		first = reportRuntime(t, s, worker, first, i, "unhealthy")
	}
	all, _ := s.Store.List(first.ProjectID, "incident", first.GoalID)
	legacy := all[0]
	delete(legacy.Data, "type")
	if err := s.Store.Update(context.Background(), func(tx *Tx) error { return tx.Put(legacy) }); err != nil {
		t.Fatal(err)
	}
	expireRuntimeSignal(t, s, first)
	if err := s.ReconcileRuntimeSignals(context.Background()); err != nil {
		t.Fatal(err)
	}
	second := secondDeployment(t, s, officer, worker, first)
	signals := runtimeIncidents(t, s, first, "monitor_signal_lost")
	if len(signals) != 1 || signals[0].Status != "open" {
		t.Fatal("new activation incorrectly proved signal recovery", signals)
	}
	second = reportRuntime(t, s, worker, second, 1, "healthy")
	stillOpen, _ := s.Store.Get(legacy.ID)
	if stillOpen.Status != "open" {
		t.Fatal("one healthy observation prematurely resolved the legacy event")
	}
	reportRuntime(t, s, worker, second, 2, "healthy")
	resolved, _ := s.Store.Get(legacy.ID)
	if resolved.Status != "resolved" || S(resolved.Data, "recoveredDeploymentId") != second.ID {
		t.Fatal("typed lifecycle orphaned an existing health incident", resolved)
	}
}

func TestRuntimeIncidentAcknowledgementRequiresHumanAuthorityAndNote(t *testing.T) {
	s, owner, project := fixture(t)
	d := resource("incident", "runtime problem", project.ID, "", M{"type": "monitor_signal_lost"})
	d.Status = "open"
	if err := s.Store.Update(context.Background(), func(tx *Tx) error { return tx.Put(d) }); err != nil {
		t.Fatal(err)
	}
	for name, a := range map[string]Actor{
		"agent":         {ID: "release-agent", Kind: "agent", ProjectID: project.ID, Roles: []string{"release"}},
		"worker":        {ID: "worker:node", Kind: "worker", ProjectID: project.ID, WorkerID: "node", Roles: []string{"release"}},
		"viewer":        {ID: "reader", Kind: "human", ProjectID: project.ID, Roles: []string{"viewer"}},
		"other-project": {ID: "other-owner", Kind: "human", ProjectID: "another-project", Roles: []string{"owner"}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := s.Command(context.Background(), a, "runtime.incident.acknowledge", M{"id": d.ID, "key": name, "note": "Attempt to take ownership"}); err == nil {
				t.Fatal("unauthorized ownership accepted")
			}
		})
	}
	for _, in := range []M{{"id": d.ID, "key": "empty-note"}, {"id": d.ID, "note": "Missing retry key"}} {
		if _, err := s.Command(context.Background(), owner, "runtime.incident.acknowledge", in); err == nil {
			t.Fatal("incomplete acknowledgement accepted")
		}
	}
	current, _ := s.Store.Get(d.ID)
	if S(current.Data, "acknowledgedBy") != "" || current.Status != "open" {
		t.Fatal("denied ownership request changed the incident", current)
	}
}

package publisher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type storeRecoveryFixture struct {
	config   Config
	request  Request
	engine   *Engine
	input    StoreReconciliationRequest
	writes   func() string
	requests func() int
	huawei   *huaweiFixtureState
	oppo     *oppoFixtureState
}

// The real HTTPS fixture accepts the write; a real SQLite trigger prevents its
// checkpoint/completion. Recovery must discover the accepted result by reading.
func stoppedStoreFixture(t *testing.T, channel string) storeRecoveryFixture {
	t.Helper()
	var f storeRecoveryFixture
	stageCondition := "NEW.state='complete'"
	switch channel {
	case "huawei-upload", "huawei-review":
		c, r, e, s := huaweiFixture(t)
		f.config, f.request, f.engine, f.huawei = c, r, e, s
		f.writes = func() string { s.mu.Lock(); defer s.mu.Unlock(); b, _ := json.Marshal(s.writes); return string(b) }
		f.requests = func() int {
			s.mu.Lock()
			defer s.mu.Unlock()
			n := 0
			for _, v := range s.requests {
				n += v
			}
			return n
		}
		stageCondition = "json_extract(NEW.receipt,'$.huawei.stage')='processing'"
		if channel == "huawei-review" {
			f.request = huaweiUploadForTest(t, e, r)
			f.request.Action, f.request.ArtifactPath = "continue", r.ArtifactPath
			s.mu.Lock()
			s.now = s.now.Add(3 * time.Minute)
			s.mu.Unlock()
			stageCondition = "json_extract(NEW.receipt,'$.huawei.stage')='submitted'"
		}
	case "oppo":
		c, r, e, s := oppoFixture(t)
		f.config, f.request, f.engine, f.oppo = c, r, e, s
		f.writes = func() string {
			s.mu.Lock()
			defer s.mu.Unlock()
			return fmt.Sprint(s.tickets, "/", s.uploads, "/", s.submits)
		}
		f.requests = func() int {
			s.mu.Lock()
			defer s.mu.Unlock()
			return s.tokens + s.tickets + s.uploads + s.submits + s.tasks + s.infos
		}
	case "xiaomi":
		c, r, e, s := xiaomiFixture(t)
		f.config, f.request, f.engine = c, r, e
		f.writes = func() string { s.mu.Lock(); defer s.mu.Unlock(); return fmt.Sprint(s.pushes) }
		f.requests = func() int { s.mu.Lock(); defer s.mu.Unlock(); return s.pushes + s.queries }
	case "vivo", "vivo-sandbox":
		c, r, e, s := vivoFixture(t)
		if channel == "vivo-sandbox" {
			c.Profiles[0].Environment = "sandbox"
			e.Config = c
		}
		f.config, f.request, f.engine = c, r, e
		f.writes = func() string { s.mu.Lock(); defer s.mu.Unlock(); return fmt.Sprint(s.uploads, "/", s.submits) }
		f.requests = func() int { s.mu.Lock(); defer s.mu.Unlock(); return s.queries + s.uploads + s.submits }
	default:
		t.Fatal(channel)
	}
	if _, err := f.engine.db.Exec("CREATE TRIGGER stop_store_completion BEFORE UPDATE ON operations WHEN " + stageCondition + " BEGIN SELECT RAISE(ABORT,'test-only lost local completion'); END"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.engine.Execute(context.Background(), f.request); err == nil {
		t.Fatal("accepted write did not lose checkpoint")
	}
	if _, err := f.engine.db.Exec("DROP TRIGGER stop_store_completion"); err != nil {
		t.Fatal(err)
	}
	j, err := f.engine.Inspect(context.Background(), f.request.OperationKey)
	if err != nil || j.State != "pending" {
		t.Fatal(j, err)
	}
	f.input = StoreReconciliationRequest{Request: f.request, JournalDigest: j.JournalDigest, ConfirmStoppedOperationKey: f.request.OperationKey, Operator: "release-admin", Reason: "Stopped exact publisher and excluded concurrent portal writers", Proof: "api"}
	if channel == "xiaomi" || strings.HasPrefix(channel, "vivo") {
		f.input.Proof = "portal"
		writeStorePortalProof(t, &f, nil)
	}
	return f
}

func writeStorePortalProof(t *testing.T, f *storeRecoveryFixture, mutate func(*StorePortalEvidence)) {
	t.Helper()
	p := f.config.Profiles[0]
	dir := t.TempDir()
	attachment := []byte("Local contract fixture: operator observed the exact submitted APK and reviewed materials. This is not a real portal receipt.")
	path := filepath.Join(dir, "portal-receipt.txt")
	if err := os.WriteFile(path, attachment, 0600); err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(attachment)
	env := p.Environment
	if env == "" {
		env = "production"
	}
	proof := StorePortalEvidence{ProtocolVersion: "1", Driver: p.Driver, Environment: env, AppID: p.AppID, PackageName: p.BundleID, OperationKey: f.request.OperationKey, JournalDigest: f.input.JournalDigest, ArtifactDigest: f.request.ArtifactDigest, ArtifactSize: f.request.ArtifactSize, MetadataDigest: p.MetadataDigest, Binding: f.request.Binding, Version: strings.TrimPrefix(f.request.Version, "v"), VersionCode: 42, State: "accepted", Operator: f.input.Operator, Reference: "fixture-portal-submission-42", ObservedAt: time.Now().UTC().Format(time.RFC3339Nano), Summary: "Exact approved package and material accepted, review and publication unconfirmed", Attachments: []StoreEvidenceAttachment{{Name: "portal-receipt.txt", Path: path, SHA256: hex.EncodeToString(h[:]), Bytes: int64(len(attachment))}}}
	if mutate != nil {
		mutate(&proof)
	}
	body, err := json.Marshal(proof)
	if err != nil {
		t.Fatal(err)
	}
	f.input.EvidenceFile = filepath.Join(dir, "report.json")
	if err = os.WriteFile(f.input.EvidenceFile, body, 0600); err != nil {
		t.Fatal(err)
	}
	h = sha256.Sum256(body)
	f.input.EvidenceDigest = hex.EncodeToString(h[:])
}

func TestStoreRecoveryAcceptedWritesAreReadOnlyAndDurable(t *testing.T) {
	for _, channel := range []string{"huawei-upload", "huawei-review", "oppo", "xiaomi", "vivo", "vivo-sandbox"} {
		t.Run(channel, func(t *testing.T) {
			f := stoppedStoreFixture(t, channel)
			ctx := context.Background()
			before := f.writes()
			requests := f.requests()
			op, err := f.engine.ReconcileStore(ctx, f.input)
			if err != nil || op.State != "complete" || op.Result == nil || op.JournalDigest == f.input.JournalDigest || len(op.StoreReconciliations) != 1 {
				t.Fatal(op, err)
			}
			if f.writes() != before {
				t.Fatal("reconciliation emitted an upstream mutation")
			}
			if f.input.Proof == "portal" && (op.Result.State != "processing" || op.Result.UpstreamState != "PORTAL_ACCEPTED_UNVERIFIED" || f.requests() != requests) {
				t.Fatal("portal assertion became API or publication proof", op)
			}
			if channel == "vivo-sandbox" && op.Result.ExternalID != "vivo:sandbox:com.company.feedback:42" {
				t.Fatal("sandbox escaped", op)
			}
			wantState := "processing"
			if channel == "huawei-review" {
				wantState = "awaiting_review"
			}
			if op.Result.State != wantState {
				t.Fatal(op)
			}
			history := op.StoreReconciliations[0]
			if history.JournalDigest != f.input.JournalDigest || history.ResultJournalDigest != op.JournalDigest || history.Operator != f.input.Operator || history.Proof != f.input.Proof || history.ResultState != op.Result.State {
				t.Fatal(history)
			}
			if f.input.Proof == "portal" {
				var n int
				if err = f.engine.db.QueryRow("SELECT count(*) FROM store_reconciliation_evidence WHERE input_digest=?", history.InputDigest).Scan(&n); err != nil || n != 2 {
					t.Fatal("report/attachment not retained", n, err)
				}
			}
			if err = f.engine.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := Open(f.config)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			reopened.HTTP.Transport = appleRecoveryTransport{after: func(*http.Request, *http.Response) (*http.Response, error) {
				t.Fatal("replay accessed network")
				return nil, nil
			}}
			if err = os.Remove(f.config.Profiles[0].CredentialsFile); err != nil {
				t.Fatal(err)
			}
			if err = os.Remove(f.request.ArtifactPath); err != nil {
				t.Fatal(err)
			}
			if f.input.EvidenceFile != "" {
				if err = os.RemoveAll(filepath.Dir(f.input.EvidenceFile)); err != nil {
					t.Fatal(err)
				}
			}
			again, err := reopened.ReconcileStore(ctx, f.input)
			if err != nil || again.JournalDigest != op.JournalDigest || len(again.StoreReconciliations) != 1 {
				t.Fatal(again, err)
			}
			changed := f.input
			changed.Reason += " changed"
			if _, err = reopened.ReconcileStore(ctx, changed); err == nil {
				t.Fatal("changed recovery borrowed result")
			}
			if _, err = reopened.Execute(ctx, Request{Action: "reconcile-store"}); err == nil {
				t.Fatal("worker protocol accepted administrator action")
			}
			if export := os.Getenv("IS_STORE_RECOVERY_EVIDENCE_DIR"); export != "" {
				exportStoreRecoveryFixture(t, export, channel, reopened, f, op)
			}
		})
	}
}

func exportStoreRecoveryFixture(t *testing.T, root, channel string, e *Engine, f storeRecoveryFixture, op Operation) {
	t.Helper()
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, channel)
	stateDir := filepath.Join(dir, "state")
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(f.config.StateDir, "publisher.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(stateDir, "publisher.db"), body, 0600); err != nil {
		t.Fatal(err)
	}
	f.config.StateDir = stateDir
	for name, value := range map[string]any{"config.json": f.config, "recovery.json": f.input, "operation.json": op} {
		b, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(dir, name), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestStoreRecoveryRejectsInvalidAuthorityBeforeNetwork(t *testing.T) {
	for _, channel := range []string{"huawei-review", "oppo", "xiaomi", "vivo"} {
		t.Run(channel, func(t *testing.T) {
			f := stoppedStoreFixture(t, channel)
			before, requests := f.writes(), f.requests()
			for _, change := range []struct {
				name   string
				mutate func(*StoreReconciliationRequest)
			}{
				{"digest", func(in *StoreReconciliationRequest) { in.JournalDigest = strings.Repeat("e", 64) }},
				{"stopped", func(in *StoreReconciliationRequest) { in.ConfirmStoppedOperationKey = "" }},
				{"operator", func(in *StoreReconciliationRequest) { in.Operator = "" }},
				{"reason", func(in *StoreReconciliationRequest) { in.Reason = " " }},
				{"binding", func(in *StoreReconciliationRequest) { in.Request.Binding = strings.Repeat("e", 64) }},
				{"commit", func(in *StoreReconciliationRequest) { in.Request.Commit = strings.Repeat("e", 40) }},
				{"material", func(in *StoreReconciliationRequest) { in.Request.MetadataDigest = strings.Repeat("e", 64) }},
				{"external", func(in *StoreReconciliationRequest) { in.Request.ExternalID = "foreign:42" }},
				{"proof", func(in *StoreReconciliationRequest) { in.Proof = "unlock" }},
				{"credentials path cannot select destination", func(in *StoreReconciliationRequest) { in.Request.GoalID = "other" }},
			} {
				t.Run(change.name, func(t *testing.T) {
					in := f.input
					change.mutate(&in)
					if _, err := f.engine.ReconcileStore(context.Background(), in); err == nil {
						t.Fatal("invalid recovery accepted")
					}
				})
			}
			if f.writes() != before || f.requests() != requests {
				t.Fatal("invalid authority reached upstream")
			}
			j, err := f.engine.Inspect(context.Background(), f.request.OperationKey)
			if err != nil || j.JournalDigest != f.input.JournalDigest || len(j.StoreReconciliations) != 0 {
				t.Fatal(j, err)
			}
		})
	}
}

func TestStoreRecoveryPortalRequiresExactPrivateProof(t *testing.T) {
	for _, channel := range []string{"xiaomi", "vivo"} {
		t.Run(channel, func(t *testing.T) {
			f := stoppedStoreFixture(t, channel)
			requests := f.requests()
			for _, tc := range []struct {
				name   string
				change func(*StorePortalEvidence)
			}{
				{"wrong operation", func(p *StorePortalEvidence) { p.OperationKey = strings.Repeat("e", 64) }},
				{"wrong journal", func(p *StorePortalEvidence) { p.JournalDigest = strings.Repeat("e", 64) }},
				{"wrong artifact", func(p *StorePortalEvidence) { p.ArtifactDigest = strings.Repeat("e", 64) }},
				{"wrong material", func(p *StorePortalEvidence) { p.MetadataDigest = strings.Repeat("e", 64) }},
				{"wrong environment", func(p *StorePortalEvidence) { p.Environment = "sandbox" }},
				{"wrong application", func(p *StorePortalEvidence) { p.AppID = "other" }},
				{"wrong package", func(p *StorePortalEvidence) { p.PackageName = "com.other.app" }},
				{"wrong version", func(p *StorePortalEvidence) { p.VersionCode = 43 }},
				{"publication assertion", func(p *StorePortalEvidence) { p.State = "published" }},
				{"future observation", func(p *StorePortalEvidence) { p.ObservedAt = time.Now().Add(time.Hour).UTC().Format(time.RFC3339) }},
				{"stale observation", func(p *StorePortalEvidence) {
					p.ObservedAt = time.Now().Add(-25 * time.Hour).UTC().Format(time.RFC3339)
				}},
				{"missing attachment", func(p *StorePortalEvidence) { p.Attachments = nil }},
				{"bad attachment digest", func(p *StorePortalEvidence) { p.Attachments[0].SHA256 = strings.Repeat("e", 64) }},
				{"bad attachment size", func(p *StorePortalEvidence) { p.Attachments[0].Bytes++ }},
				{"foreign operator", func(p *StorePortalEvidence) { p.Operator = "someone-else" }},
			} {
				t.Run(tc.name, func(t *testing.T) {
					writeStorePortalProof(t, &f, tc.change)
					if _, err := f.engine.ReconcileStore(context.Background(), f.input); err == nil {
						t.Fatal("invalid portal proof accepted")
					}
				})
			}
			if requests != f.requests() {
				t.Fatal("portal verification contacted channel")
			}
			j, err := f.engine.Inspect(context.Background(), f.request.OperationKey)
			if err != nil || j.State != "pending" || len(j.StoreReconciliations) != 0 {
				t.Fatal(j, err)
			}
		})
	}
}

package publisher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStoreRecoveryAPIPreservesObservedStatesAndOwnLock(t *testing.T) {
	for _, tc := range []struct{ channel, state, want string }{
		{"huawei-review", "1", "rejected"}, {"huawei-review", "2", "halted"}, {"huawei-review", "3", "awaiting_release"}, {"huawei-review", "0", "published"},
		{"oppo", "task_failed", "rejected"}, {"oppo", "review", "awaiting_review"}, {"oppo", "release", "awaiting_release"}, {"oppo", "halted", "halted"}, {"oppo", "published", "published"},
	} {
		t.Run(tc.channel+"/"+tc.state, func(t *testing.T) {
			f := stoppedStoreFixture(t, tc.channel)
			before := f.writes()
			if f.huawei != nil {
				s := f.huawei
				s.mu.Lock()
				var state int
				if _, err := fmt.Sscan(tc.state, &state); err != nil {
					t.Fatal(err)
				}
				s.appInfo["releaseState"] = state
				if state == 0 {
					s.appInfo["onShelfVersionId"] = "business-version-42"
					s.appInfo["onShelfVersionCode"] = 42
					s.appInfo["onShelfVersionNumber"] = "1.0.0"
				}
				s.mu.Unlock()
			} else {
				s := f.oppo
				s.mu.Lock()
				s.taskState = "2"
				switch tc.state {
				case "task_failed":
					s.taskState = "3"
				case "review":
					s.exact["audit_status"] = "1"
				case "release":
					s.exact["audit_status"] = "2"
				case "halted":
					s.exact["audit_status"] = "22"
				case "published":
					s.exact["audit_status"] = "111"
					s.exact["online_info_offline_apply"] = []any{map[string]any{"app_id": "100123", "version_id": "402", "online_state": "1", "online_time": "2026-11-01 10:00:00", "offline_time": ""}}
				}
				s.mu.Unlock()
			}
			op, err := f.engine.ReconcileStore(context.Background(), f.input)
			if err != nil || op.Result == nil || op.Result.State != tc.want {
				t.Fatal(op, err)
			}
			table, where, args := storeDestination(f.config.Profiles[0])
			var n int
			if err = f.engine.db.QueryRow("SELECT count(*) FROM "+table+" WHERE "+where, args...).Scan(&n); err != nil {
				t.Fatal(err)
			}
			wantLock := 0
			if tc.channel == "huawei-review" && tc.want == "awaiting_release" {
				wantLock = 1
			}
			if n != wantLock {
				t.Fatal("incorrect lock disposition", n, wantLock)
			}
			if f.writes() != before {
				t.Fatal("state recovery emitted publication write")
			}
		})
	}
}

func TestStoreRecoveryArtifactOrSDKMismatchStopsBeforeChannel(t *testing.T) {
	for _, channel := range []string{"huawei-review", "oppo", "xiaomi", "vivo"} {
		t.Run(channel, func(t *testing.T) {
			f := stoppedStoreFixture(t, channel)
			requests := f.requests()
			body, err := os.ReadFile(f.request.ArtifactPath)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(f.request.ArtifactPath, append(body, 'x'), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err = f.engine.ReconcileStore(context.Background(), f.input); err == nil {
				t.Fatal("changed APK bytes accepted")
			}
			if err = os.WriteFile(f.request.ArtifactPath, body, 0600); err != nil {
				t.Fatal(err)
			}
			f.engine.verifyAndroidPackage = func(context.Context, Profile, Request, string) (androidPackage, error) {
				return androidPackage{VersionCode: 43, Version: "1.0.0"}, nil
			}
			if _, err = f.engine.ReconcileStore(context.Background(), f.input); err == nil {
				t.Fatal("different SDK version accepted")
			}
			f.engine.verifyAndroidPackage = func(context.Context, Profile, Request, string) (androidPackage, error) {
				return androidPackage{}, fmt.Errorf("SDK rejected release signature")
			}
			if _, err = f.engine.ReconcileStore(context.Background(), f.input); err == nil {
				t.Fatal("SDK signature rejection bypassed")
			}
			if requests != f.requests() {
				t.Fatal("invalid artifact reached channel")
			}
			assertStorePendingUnchanged(t, f)
		})
	}
}

func TestStoreRecoveryForeignVersionAndConcurrentLockStayOwned(t *testing.T) {
	for _, channel := range []string{"oppo", "vivo"} {
		t.Run(channel, func(t *testing.T) {
			f := stoppedStoreFixture(t, channel)
			requests := f.requests()
			table := channel + "_versions"
			if _, err := f.engine.db.Exec("UPDATE " + table + " SET operation_key='foreign-owner'"); err != nil {
				t.Fatal(err)
			}
			if _, err := f.engine.ReconcileStore(context.Background(), f.input); err == nil {
				t.Fatal("foreign version adopted")
			}
			if requests != f.requests() {
				t.Fatal("foreign version reached channel")
			}
			var owner string
			if err := f.engine.db.QueryRow("SELECT operation_key FROM " + table).Scan(&owner); err != nil || owner != "foreign-owner" {
				t.Fatal(owner, err)
			}
		})
	}
	for _, channel := range []string{"huawei-review", "oppo", "xiaomi", "vivo"} {
		t.Run("changed-lock/"+channel, func(t *testing.T) {
			f := stoppedStoreFixture(t, channel)
			verify := f.engine.verifyAndroidPackage
			p := f.config.Profiles[0]
			table, where, args := storeDestination(p)
			f.engine.verifyAndroidPackage = func(ctx context.Context, p Profile, r Request, path string) (androidPackage, error) {
				pkg, err := verify(ctx, p, r, path)
				if _, sqlErr := f.engine.db.Exec("UPDATE "+table+" SET operation_key='foreign-owner' WHERE "+where, args...); sqlErr != nil {
					t.Fatal(sqlErr)
				}
				return pkg, err
			}
			if _, err := f.engine.ReconcileStore(context.Background(), f.input); err == nil {
				t.Fatal("concurrent foreign lock released")
			}
			j, err := f.engine.Inspect(context.Background(), f.request.OperationKey)
			if err != nil || j.JournalDigest != f.input.JournalDigest || len(j.StoreReconciliations) != 0 {
				t.Fatal(j, err)
			}
			var owner string
			if err = f.engine.db.QueryRow("SELECT operation_key FROM "+table+" WHERE "+where, args...).Scan(&owner); err != nil || owner != "foreign-owner" {
				t.Fatal(owner, err)
			}
		})
	}
}

func TestStoreRecoveryHuaweiSecondRecoveryRetainsOriginalHistory(t *testing.T) {
	f := stoppedStoreFixture(t, "huawei-upload")
	ctx := context.Background()
	first, err := f.engine.ReconcileStore(ctx, f.input)
	if err != nil {
		t.Fatal(err)
	}
	f.huawei.mu.Lock()
	f.huawei.now = f.huawei.now.Add(3 * time.Minute)
	f.huawei.mu.Unlock()
	if _, err = f.engine.db.Exec("CREATE TRIGGER lose_second BEFORE UPDATE ON operations WHEN json_extract(NEW.receipt,'$.huawei.stage')='submitted' BEGIN SELECT RAISE(ABORT,'test-only lost completion'); END"); err != nil {
		t.Fatal(err)
	}
	r := f.request
	r.Action = "continue"
	r.ExternalID = first.Result.ExternalID
	if _, err = f.engine.Execute(ctx, r); err == nil {
		t.Fatal("review did not lose checkpoint")
	}
	if _, err = f.engine.db.Exec("DROP TRIGGER lose_second"); err != nil {
		t.Fatal(err)
	}
	j, err := f.engine.Inspect(ctx, r.OperationKey)
	if err != nil || j.State != "pending" || len(j.StoreReconciliations) != 1 {
		t.Fatal(j, err)
	}
	in := f.input
	in.Request = r
	in.JournalDigest = j.JournalDigest
	in.Reason = "Stopped continuation after accepted review"
	before := f.writes()
	second, err := f.engine.ReconcileStore(ctx, in)
	if err != nil || len(second.StoreReconciliations) != 2 || second.Result.State != "awaiting_review" {
		t.Fatal(second, err)
	}
	if _, err = f.engine.ReconcileStore(ctx, f.input); err == nil {
		t.Fatal("old input reused second recovery")
	}
	if replay, err := f.engine.ReconcileStore(ctx, in); err != nil || replay.JournalDigest != second.JournalDigest {
		t.Fatal(replay, err)
	}
	if before != f.writes() {
		t.Fatal("second recovery repeated submission")
	}
}

func assertStorePendingUnchanged(t *testing.T, f storeRecoveryFixture) {
	t.Helper()
	j, err := f.engine.Inspect(context.Background(), f.request.OperationKey)
	if err != nil || j.State != "pending" || j.JournalDigest != f.input.JournalDigest || len(j.StoreReconciliations) != 0 {
		t.Fatal("pending journal changed", j, err)
	}
	for _, table := range []string{"store_reconciliations", "store_reconciliation_evidence", "store_recovery_versions"} {
		var n int
		if err = f.engine.db.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil || n != 0 {
			t.Fatal("failed recovery left partial state", table, n, err)
		}
	}
	if err = checkStoreRecoveryOwners(context.Background(), f.engine.db, f.config.Profiles[0], f.request.OperationKey, 42); err != nil {
		t.Fatal("failed recovery lost ownership", err)
	}
}

func TestStoreRecoveryAPIRejectsWeakOrForeignArtifactProof(t *testing.T) {
	t.Run("Huawei", func(t *testing.T) {
		f := stoppedStoreFixture(t, "huawei-review")
		before := f.writes()
		s := f.huawei
		original, _ := json.Marshal(s.packages)
		app, _ := json.Marshal(s.appInfo)
		for _, tc := range []struct {
			name   string
			mutate func()
		}{
			{"SHA256", func() { s.packages[0]["shaCode"] = strings.Repeat("e", 64) }},
			{"missing SHA256", func() { delete(s.packages[0], "shaCode") }},
			{"version name", func() { s.packages[0]["versionName"] = "other" }},
			{"missing version name", func() { delete(s.packages[0], "versionName") }},
			{"filename", func() { s.packages[0]["fileName"] = "other.apk" }},
			{"size", func() { s.packages[0]["packageSize"] = 999 }},
			{"package ID", func() { s.packages[0]["pkgVersion"] = "other-pkg" }},
			{"wrong business version", func() { s.appInfo["versionId"] = "other-version" }},
			{"draft only", func() { s.appInfo["releaseState"] = 7 }},
			{"unknown state", func() { s.appInfo["releaseState"] = 999 }},
		} {
			t.Run(tc.name, func(t *testing.T) {
				s.mu.Lock()
				_ = json.Unmarshal(original, &s.packages)
				s.appInfo = map[string]any{}
				_ = json.Unmarshal(app, &s.appInfo)
				tc.mutate()
				s.mu.Unlock()
				if _, err := f.engine.ReconcileStore(context.Background(), f.input); err == nil {
					t.Fatal("weak Huawei proof recovered")
				}
				assertStorePendingUnchanged(t, f)
			})
		}
		if f.writes() != before {
			t.Fatal("failed observation replayed Huawei writes")
		}
	})
	t.Run("OPPO", func(t *testing.T) {
		f := stoppedStoreFixture(t, "oppo")
		before := f.writes()
		s := f.oppo
		original, _ := json.Marshal(s.exact)
		for _, task := range []string{"1", "2", "3"} {
			for _, field := range []string{"apk_md5", "apk_size", "version_code", "version_name", "pkg_name", "app_id", "version_type", "version_id"} {
				t.Run(task+"/"+field, func(t *testing.T) {
					s.mu.Lock()
					s.exact = map[string]any{}
					_ = json.Unmarshal(original, &s.exact)
					s.taskState = task
					s.exact[field] = "unrelated"
					s.mu.Unlock()
					if _, err := f.engine.ReconcileStore(context.Background(), f.input); err == nil {
						t.Fatal("weak OPPO task proof recovered")
					}
					assertStorePendingUnchanged(t, f)
				})
			}
		}
		if f.writes() != before {
			t.Fatal("failed observation replayed OPPO writes")
		}
	})
}

func TestStoreRecoveryIncompleteStagesAndMissingOwnersStayBlocked(t *testing.T) {
	for _, channel := range []string{"huawei-upload", "oppo", "xiaomi", "vivo"} {
		t.Run(channel, func(t *testing.T) {
			f := stoppedStoreFixture(t, channel)
			ctx := context.Background()
			requests := f.requests()
			j, err := f.engine.inspectJournal(ctx, f.request.OperationKey)
			if err != nil {
				t.Fatal(err)
			}
			for _, stage := range []string{"permission_check", "apk_upload_intent", "byte_upload_intent", "unknown"} {
				var receipt Receipt
				_ = json.Unmarshal([]byte(j.receipt), &receipt)
				if receipt.Huawei != nil {
					receipt.Huawei.Stage = stage
				}
				if receipt.Oppo != nil {
					receipt.Oppo.Stage = stage
				}
				if receipt.Xiaomi != nil {
					receipt.Xiaomi.Stage = stage
				}
				if receipt.Vivo != nil {
					receipt.Vivo.Stage = stage
				}
				body, _ := json.Marshal(receipt)
				if _, err = f.engine.db.Exec("UPDATE operations SET receipt=? WHERE operation_key=?", string(body), f.request.OperationKey); err != nil {
					t.Fatal(err)
				}
				current, err := f.engine.Inspect(ctx, f.request.OperationKey)
				if err != nil {
					t.Fatal(err)
				}
				in := f.input
				in.JournalDigest = current.JournalDigest
				if _, err = f.engine.ReconcileStore(ctx, in); err == nil {
					t.Fatal("early/unknown stage unlocked", stage)
				}
			}
			if _, err = f.engine.db.Exec("UPDATE operations SET receipt=? WHERE operation_key=?", j.receipt, f.request.OperationKey); err != nil {
				t.Fatal(err)
			}
			p := f.config.Profiles[0]
			table, where, args := storeDestination(p)
			if _, err = f.engine.db.Exec("UPDATE "+table+" SET operation_key='foreign-owner' WHERE "+where, args...); err != nil {
				t.Fatal(err)
			}
			if _, err = f.engine.ReconcileStore(ctx, f.input); err == nil {
				t.Fatal("foreign destination unlocked")
			}
			var owner string
			if err = f.engine.db.QueryRow("SELECT operation_key FROM "+table+" WHERE "+where, args...).Scan(&owner); err != nil || owner != "foreign-owner" {
				t.Fatal(owner, err)
			}
			if f.requests() != requests {
				t.Fatal("invalid stage/ownership reached channel")
			}
		})
	}
}

func TestStoreRecoverySQLFailuresRollBackJournalHistoryEvidenceAndLock(t *testing.T) {
	for _, channel := range []string{"huawei-review", "oppo", "xiaomi", "vivo"} {
		t.Run(channel, func(t *testing.T) {
			f := stoppedStoreFixture(t, channel)
			before := f.writes()
			if f.huawei != nil {
				f.huawei.mu.Lock()
				f.huawei.appInfo["releaseState"] = 0
				f.huawei.appInfo["onShelfVersionId"] = "business-version-42"
				f.huawei.appInfo["onShelfVersionCode"] = 42
				f.huawei.appInfo["onShelfVersionNumber"] = "1.0.0"
				f.huawei.mu.Unlock()
			}
			destination, _, _ := storeDestination(f.config.Profiles[0])
			triggers := []string{"BEFORE UPDATE ON operations", "BEFORE INSERT ON store_reconciliations", "BEFORE INSERT ON store_recovery_versions", "BEFORE DELETE ON " + destination}
			if f.input.Proof == "portal" {
				triggers = append(triggers, "BEFORE INSERT ON store_reconciliation_evidence")
			}
			for _, trigger := range triggers {
				t.Run(trigger, func(t *testing.T) {
					if _, err := f.engine.db.Exec("CREATE TRIGGER fail_recovery " + trigger + " BEGIN SELECT RAISE(ABORT,'test-only failure'); END"); err != nil {
						t.Fatal(err)
					}
					if _, err := f.engine.ReconcileStore(context.Background(), f.input); err == nil {
						t.Fatal("SQL failure accepted recovery")
					}
					if _, err := f.engine.db.Exec("DROP TRIGGER fail_recovery"); err != nil {
						t.Fatal(err)
					}
					assertStorePendingUnchanged(t, f)
				})
			}
			if f.writes() != before {
				t.Fatal("rollback replayed channel write")
			}
		})
	}
}

func TestStoreRecoveryConcurrentReadersCommitOnce(t *testing.T) {
	for _, channel := range []string{"huawei-review", "oppo", "xiaomi", "vivo"} {
		t.Run(channel, func(t *testing.T) {
			f := stoppedStoreFixture(t, channel)
			before := f.writes()
			verify := f.engine.verifyAndroidPackage
			var arrived sync.WaitGroup
			arrived.Add(2)
			f.engine.verifyAndroidPackage = func(ctx context.Context, p Profile, r Request, path string) (androidPackage, error) {
				pkg, err := verify(ctx, p, r, path)
				arrived.Done()
				arrived.Wait()
				return pkg, err
			}
			results := make(chan error, 2)
			for i := 0; i < 2; i++ {
				go func() { _, err := f.engine.ReconcileStore(context.Background(), f.input); results <- err }()
			}
			passed := 0
			for i := 0; i < 2; i++ {
				if <-results == nil {
					passed++
				}
			}
			if passed != 1 {
				t.Fatal("concurrent readers did not isolate exact snapshot", passed)
			}
			op, err := f.engine.Inspect(context.Background(), f.request.OperationKey)
			if err != nil || op.State != "complete" || len(op.StoreReconciliations) != 1 {
				t.Fatal(op, err)
			}
			if f.writes() != before {
				t.Fatal("concurrent read replayed mutation")
			}
		})
	}
}

func TestStoreRecoveryConcurrentJournalChangeIsNeverOverwritten(t *testing.T) {
	for _, channel := range []string{"huawei-review", "oppo", "xiaomi", "vivo"} {
		t.Run(channel, func(t *testing.T) {
			f := stoppedStoreFixture(t, channel)
			verify := f.engine.verifyAndroidPackage
			f.engine.verifyAndroidPackage = func(ctx context.Context, p Profile, r Request, path string) (androidPackage, error) {
				pkg, err := verify(ctx, p, r, path)
				if _, sqlErr := f.engine.db.Exec("UPDATE operations SET updated_at='changed-during-observation' WHERE operation_key=?", r.OperationKey); sqlErr != nil {
					t.Fatal(sqlErr)
				}
				return pkg, err
			}
			if _, err := f.engine.ReconcileStore(context.Background(), f.input); err == nil {
				t.Fatal("concurrent snapshot overwritten")
			}
			j, err := f.engine.Inspect(context.Background(), f.request.OperationKey)
			if err != nil || j.State != "pending" || len(j.StoreReconciliations) != 0 || j.JournalDigest == f.input.JournalDigest {
				t.Fatal(j, err)
			}
			var n int
			if err = f.engine.db.QueryRow("SELECT count(*) FROM store_recovery_versions").Scan(&n); err != nil || n != 0 {
				t.Fatal("CAS failure left version reservation", n, err)
			}
		})
	}
}

func TestStoreRecoveryHuaweiHistorySurvivesContinuation(t *testing.T) {
	f := stoppedStoreFixture(t, "huawei-upload")
	ctx := context.Background()
	first, err := f.engine.ReconcileStore(ctx, f.input)
	if err != nil {
		t.Fatal(err)
	}
	f.huawei.mu.Lock()
	f.huawei.now = f.huawei.now.Add(3 * time.Minute)
	f.huawei.mu.Unlock()
	r := f.request
	r.Action = "continue"
	r.ExternalID = first.Result.ExternalID
	result, err := f.engine.Execute(ctx, r)
	if err != nil || result.State != "awaiting_review" {
		t.Fatal(result, err)
	}
	j, err := f.engine.Inspect(ctx, r.OperationKey)
	if err != nil || len(j.StoreReconciliations) != 1 || j.Receipt.Huawei.Stage != "submitted" {
		t.Fatal(j, err)
	}
	if _, err = f.engine.ReconcileStore(ctx, f.input); err == nil {
		t.Fatal("old recovery borrowed subsequent result")
	}
}

func TestStoreRecoveryPermanentVersionPreventsNewKeyReplay(t *testing.T) {
	for _, channel := range []string{"huawei-review", "oppo", "xiaomi", "vivo"} {
		t.Run(channel, func(t *testing.T) {
			f := stoppedStoreFixture(t, channel)
			if _, err := f.engine.ReconcileStore(context.Background(), f.input); err != nil {
				t.Fatal(err)
			}
			before := f.writes()
			requests := f.requests()
			if f.huawei != nil {
				// An old package can disappear from upstream retention. The local
				// permanent reservation still denies reusing its code under a new key.
				f.huawei.mu.Lock()
				f.huawei.packages = nil
				f.huawei.appInfo["releaseState"] = 7
				f.huawei.mu.Unlock()
				if _, err := f.engine.db.Exec("DELETE FROM huawei_destinations"); err != nil {
					t.Fatal(err)
				}
			}
			r := f.request
			r.Action = "publish"
			r.ExternalID = ""
			r.OperationKey = strings.Repeat("d", 64)
			if _, err := f.engine.Execute(context.Background(), r); err == nil {
				t.Fatal("new key reused recovered version")
			}
			if f.writes() != before || f.requests() != requests {
				t.Fatal("new key replay reached channel")
			}
		})
	}
}

func TestStoreRecoveryPortalFileBoundaries(t *testing.T) {
	f := stoppedStoreFixture(t, "xiaomi")
	requests := f.requests()
	for _, tc := range []string{"report digest", "report permission", "report symlink", "report trailing JSON", "report unknown field", "report duplicate field", "attachment permission", "attachment symlink", "duplicate attachment name", "state permission", "database permission"} {
		t.Run(tc, func(t *testing.T) {
			writeStorePortalProof(t, &f, nil)
			var proof StorePortalEvidence
			body, err := os.ReadFile(f.input.EvidenceFile)
			if err != nil {
				t.Fatal(err)
			}
			_ = json.Unmarshal(body, &proof)
			switch tc {
			case "report digest":
				f.input.EvidenceDigest = strings.Repeat("a", 64)
			case "report permission":
				if err = os.Chmod(f.input.EvidenceFile, 0644); err != nil {
					t.Fatal(err)
				}
			case "report symlink":
				link := filepath.Join(t.TempDir(), "report.json")
				if err = os.Symlink(f.input.EvidenceFile, link); err != nil {
					t.Fatal(err)
				}
				f.input.EvidenceFile = link
			case "report trailing JSON":
				body = append(body, []byte(" {}")...)
			case "report unknown field":
				body = append([]byte(`{"unexpected":true,`), body[1:]...)
			case "report duplicate field":
				body = append([]byte(`{"state":"published",`), body[1:]...)
			case "attachment permission":
				if err = os.Chmod(proof.Attachments[0].Path, 0644); err != nil {
					t.Fatal(err)
				}
			case "attachment symlink":
				link := filepath.Join(t.TempDir(), "attachment")
				if err = os.Symlink(proof.Attachments[0].Path, link); err != nil {
					t.Fatal(err)
				}
				proof.Attachments[0].Path = link
				body, _ = json.Marshal(proof)
			case "duplicate attachment name":
				proof.Attachments = append(proof.Attachments, proof.Attachments[0])
				body, _ = json.Marshal(proof)
			case "state permission":
				if err = os.Chmod(f.config.StateDir, 0755); err != nil {
					t.Fatal(err)
				}
				defer os.Chmod(f.config.StateDir, 0700)
			case "database permission":
				path := filepath.Join(f.config.StateDir, "publisher.db")
				if err = os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
				defer os.Chmod(path, 0600)
			}
			if strings.Contains(tc, "JSON") || strings.Contains(tc, "field") || tc == "attachment symlink" || tc == "duplicate attachment name" {
				if err = os.WriteFile(f.input.EvidenceFile, body, 0600); err != nil {
					t.Fatal(err)
				}
				hash := sha256.Sum256(body)
				f.input.EvidenceDigest = hex.EncodeToString(hash[:])
			}
			if _, err = f.engine.ReconcileStore(context.Background(), f.input); err == nil {
				t.Fatal("unsafe evidence accepted", tc)
			}
			assertStorePendingUnchanged(t, f)
		})
	}
	if requests != f.requests() {
		t.Fatal("portal boundary checks accessed network")
	}
}

package publisher

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type materialReviewFixture struct {
	mu                   sync.Mutex
	rejected, resolved   bool
	wrongItem, extraItem bool
	failWrite            string
	writes               []string
}

func appleRejectedMaterials(t *testing.T, prepare ...func(*Engine)) (Request, *Engine, *appleFixtureState, *appleMaterialsFixtureState, *materialReviewFixture) {
	t.Helper()
	_, r, e, base, material, server := appleManagedFixture(t)
	t.Cleanup(func() { e.Close() })
	for _, configure := range prepare {
		configure(e)
	}
	result, err := e.Execute(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	r.Action, r.ExternalID, r.ArtifactPath = "continue", result.ExternalID, ""
	base.uploadState, material.imageReady = "COMPLETE", true
	if _, err = e.Execute(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	state := &materialReviewFixture{rejected: true}
	wrapper := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		path := q.Method + " " + q.URL.Path
		if strings.Contains(q.URL.Path, "reviewSubmissions/submission1") || path == "PATCH /v1/reviewSubmissionItems/item1" {
			state.mu.Lock()
			defer state.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			respond := func(v any) { _ = json.NewEncoder(w).Encode(map[string]any{"data": v}) }
			if q.Method != "GET" {
				state.writes = append(state.writes, path)
				if state.failWrite == path {
					w.WriteHeader(503)
					return
				}
				var in struct {
					Data struct {
						Type, ID   string
						Attributes map[string]any
					}
				}
				if json.NewDecoder(q.Body).Decode(&in) != nil {
					t.Error("invalid review update")
				}
				if path == "PATCH /v1/reviewSubmissionItems/item1" {
					if in.Data.Type != "reviewSubmissionItems" || in.Data.ID != "item1" || len(in.Data.Attributes) != 1 || in.Data.Attributes["resolved"] != true {
						t.Error("must resolve only the original item")
					}
					state.resolved = true
					respond(appleResourceFixture("reviewSubmissionItems", "item1", map[string]any{"state": "READY_FOR_REVIEW"}, map[string]any{"appStoreVersion": appleRelationshipFixture("appStoreVersions", "version1")}))
					return
				}
				if path == "PATCH /v1/reviewSubmissions/submission1" {
					if in.Data.Type != "reviewSubmissions" || in.Data.ID != "submission1" || len(in.Data.Attributes) != 1 || in.Data.Attributes["submitted"] != true || !state.resolved {
						t.Error("must resubmit only after resolving exact item")
					}
					state.rejected = false
					base.mu.Lock()
					base.storeState = "WAITING_FOR_REVIEW"
					base.mu.Unlock()
				} else {
					t.Errorf("unexpected review mutation: %s", path)
				}
			}
			if strings.HasSuffix(q.URL.Path, "/items") {
				item, status := "item1", "ACCEPTED"
				if state.wrongItem {
					item = "another-item"
				}
				if state.rejected {
					status = "REJECTED"
				}
				if state.resolved && state.rejected {
					status = "READY_FOR_REVIEW"
				}
				items := []any{appleResourceFixture("reviewSubmissionItems", item, map[string]any{"state": status}, map[string]any{"appStoreVersion": appleRelationshipFixture("appStoreVersions", "version1")})}
				if state.extraItem {
					items = append(items, items[0])
				}
				respond(items)
				return
			}
			status := "WAITING_FOR_REVIEW"
			if state.rejected {
				status = "UNRESOLVED_ISSUES"
			}
			respond(appleResourceFixture("reviewSubmissions", "submission1", map[string]any{"platform": "IOS", "state": status, "submittedDate": "2026-11-05T10:00:00Z"}, map[string]any{"app": appleRelationshipFixture("apps", "1234567890"), "appStoreVersionForReview": appleRelationshipFixture("appStoreVersions", "version1")}))
			return
		}
		server.Config.Handler.ServeHTTP(w, q)
	}))
	t.Cleanup(wrapper.Close)
	e.appleBase, e.HTTP = wrapper.URL, wrapper.Client()
	base.storeState = "METADATA_REJECTED"
	material.writes = nil
	return r, e, base, material, state
}

func nextAppleMaterialRequest(t *testing.T, e *Engine, source Request, n int) Request {
	t.Helper()
	profile := &e.Config.Profiles[0]
	b, err := os.ReadFile(profile.MetadataFile)
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if json.Unmarshal(b, &manifest) != nil {
		t.Fatal("manifest")
	}
	loc := manifest["locales"].([]any)[0].(map[string]any)
	loc["versionText"].(map[string]any)["description"] = fmt.Sprintf("Reviewed correction %d", n)
	oldDigest := profile.MetadataDigest
	writeAppleMaterialFixture(t, profile, manifest)
	b, _ = json.Marshal(source)
	var value map[string]any
	_ = json.Unmarshal(b, &value)
	value["action"], value["operationKey"], value["binding"] = "materials", strings.Repeat(fmt.Sprint(n), 64), strings.Repeat(fmt.Sprint(n+3), 64)
	value["metadataDigest"], value["materialRevisionId"] = profile.MetadataDigest, fmt.Sprintf("store_revision_%d", n)
	value["sourceOperationKey"], value["sourceBinding"], value["sourceMetadataDigest"] = source.OperationKey, source.Binding, oldDigest
	b, _ = json.Marshal(value)
	var r Request
	_ = json.Unmarshal(b, &r)
	return r
}

func TestAppleMaterialRevisionSameBuildResubmissionAndTwoRounds(t *testing.T) {
	source, e, base, material, review := appleRejectedMaterials(t)
	for n := 1; n <= 2; n++ {
		r := nextAppleMaterialRequest(t, e, source, n)
		material.expectedDescription = fmt.Sprintf("Reviewed correction %d", n)
		before := base.writes
		result, err := e.Execute(context.Background(), r)
		if err != nil || result.State != "awaiting_review" || result.ExternalID != source.ExternalID {
			t.Fatalf("same-build correction failed: %+v %v", result, err)
		}
		if base.writes != before || len(material.writes) != n*2 || len(review.writes) != n*2 {
			t.Fatal("revision reuploaded IPA/media or replaced review", base.writes, material.writes, review.writes)
		}
		count := len(review.writes)
		materialCount, packageCount := len(material.writes), base.writes
		if _, err = e.Execute(context.Background(), r); err != nil || len(review.writes) != count {
			t.Fatal("replay resubmitted", err)
		}
		r.Action = "status"
		if _, err = e.Execute(context.Background(), r); err != nil || len(review.writes) != count || len(material.writes) != materialCount || base.writes != packageCount {
			t.Fatal("status mutated or lost new binding", err)
		}
		if _, err = e.Execute(context.Background(), source); err == nil {
			t.Fatal("superseded operation remained authoritative")
		}
		var versions, locks int
		if err = e.db.QueryRow("SELECT count(*) FROM apple_versions").Scan(&versions); err != nil || versions != 1 {
			t.Fatal("changed permanent version reservation", err, versions)
		}
		if err = e.db.QueryRow("SELECT count(*) FROM apple_destinations").Scan(&locks); err != nil || locks != 0 {
			t.Fatal("did not release app reservation", err, locks)
		}
		source = r
		review.rejected = true
		review.resolved = false
		base.storeState = "METADATA_REJECTED"
	}
}

// Two independent SQLite connections must never reserve two successors, even
// after the transient app lock has been released. No remote mutation is needed
// to test the journal transaction that grants write authority.
func TestAppleMaterialRevisionCrossEngineSingleSuccessor(t *testing.T) {
	source, one, _, _, _ := appleRejectedMaterials(t)
	r := nextAppleMaterialRequest(t, one, source, 1)
	two, err := Open(one.Config)
	if err != nil {
		t.Fatal(err)
	}
	defer two.Close()
	parent, err := one.inspectJournal(context.Background(), source.OperationKey)
	if err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		key string
		err error
	}
	results := make(chan outcome, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i, engine := range []*Engine{one, two} {
		candidate := r
		candidate.OperationKey = strings.Repeat(fmt.Sprint(i+1), 64)
		body, _ := json.Marshal(parent.Receipt.Apple)
		var receipt appleReceipt
		_ = json.Unmarshal(body, &receipt)
		receipt.Material.Digest = r.MetadataDigest
		receipt.Revision = &appleMaterialRevisionReceipt{RevisionID: r.MaterialRevisionID, Binding: r.Binding, BaseHash: appleMaterialBaseHash(r, one.Config.Profiles[0]), SourceOperationKey: source.OperationKey, SourceJournalDigest: parent.JournalDigest, RootOperationKey: source.OperationKey}
		wg.Add(1)
		go func(e *Engine, request Request, receipt appleReceipt) {
			defer wg.Done()
			<-start
			results <- outcome{request.OperationKey, e.appleReserveMaterialRevision(context.Background(), e.Config.Profiles[0], request, requestHash(request, e.Config.Profiles[0]), &receipt, parent)}
		}(engine, candidate, receipt)
	}
	close(start)
	wg.Wait()
	successes, winner := 0, ""
	for range 2 {
		result := <-results
		if result.err == nil {
			successes++
			winner = result.key
		}
	}
	if successes != 1 {
		t.Fatal("multiple or no material successors reserved", successes)
	}
	var child, owner string
	if err = one.db.QueryRow("SELECT operation_key FROM apple_material_revisions WHERE parent_operation_key=?", source.OperationKey).Scan(&child); err != nil || child != winner {
		t.Fatal("unique successor journal differs", child, winner, err)
	}
	if err = one.db.QueryRow("SELECT operation_key FROM apple_versions").Scan(&owner); err != nil || owner != source.OperationKey {
		t.Fatal("permanent version owner changed", owner, err)
	}
	if _, err = one.db.Exec("DELETE FROM apple_destinations"); err != nil {
		t.Fatal(err)
	}
	r.OperationKey = strings.Repeat("9", 64)
	receipt := parent.Receipt.Apple
	receipt.Revision = &appleMaterialRevisionReceipt{RootOperationKey: source.OperationKey}
	if err = two.appleReserveMaterialRevision(context.Background(), two.Config.Profiles[0], r, requestHash(r, two.Config.Profiles[0]), receipt, parent); err == nil {
		t.Fatal("released app lock allowed a second successor")
	}
	var locks int
	if err = one.db.QueryRow("SELECT count(*) FROM apple_destinations").Scan(&locks); err != nil || locks != 0 {
		t.Fatal("failed fork left a partial app reservation", locks, err)
	}
}

func TestAppleMaterialRevisionRejectsDriftBeforeWrites(t *testing.T) {
	for _, scenario := range []string{"binary-rejection", "wrong-build", "wrong-item", "extra-item", "old-text", "extra-media", "wrong-source", "same-digest", "changed-image"} {
		t.Run(scenario, func(t *testing.T) {
			source, e, base, material, review := appleRejectedMaterials(t)
			r := nextAppleMaterialRequest(t, e, source, 1)
			material.expectedDescription = "Reviewed correction 1"
			switch scenario {
			case "binary-rejection":
				base.storeState = "INVALID_BINARY"
			case "wrong-build":
				base.wrongBuild = true
			case "wrong-item":
				review.wrongItem = true
			case "extra-item":
				review.extraItem = true
			case "old-text":
				material.versionText["description"] = "outside edit"
			case "extra-media":
				material.extraMedia = true
			case "wrong-source":
				r.ReleaseID = "release_other"
			case "same-digest":
				r.MetadataDigest = source.MetadataDigest
				e.Config.Profiles[0].MetadataDigest = source.MetadataDigest
			case "changed-image":
				b, _ := os.ReadFile(e.Config.Profiles[0].MetadataFile)
				var value map[string]any
				_ = json.Unmarshal(b, &value)
				img := value["locales"].([]any)[0].(map[string]any)["screenshots"].([]any)[0].(map[string]any)["images"].([]any)[0].(map[string]any)
				img["specId"] = "different-spec"
				writeAppleMaterialFixture(t, &e.Config.Profiles[0], value)
				r.MetadataDigest = e.Config.Profiles[0].MetadataDigest
			}
			if _, err := e.Execute(context.Background(), r); err == nil {
				t.Fatal("revision accepted unsafe source", scenario)
			}
			if len(material.writes) != 0 || len(review.writes) != 0 {
				t.Fatal("unsafe revision wrote externally")
			}
		})
	}
}

func TestAppleMaterialRevisionUncertainWriteCannotRetryOrFork(t *testing.T) {
	source, e, _, material, review := appleRejectedMaterials(t)
	r := nextAppleMaterialRequest(t, e, source, 1)
	material.expectedDescription = "Reviewed correction 1"
	review.failWrite = "PATCH /v1/reviewSubmissions/submission1"
	if _, err := e.Execute(context.Background(), r); err == nil {
		t.Fatal("unknown submission considered complete")
	}
	count := len(review.writes)
	if _, err := e.Execute(context.Background(), r); err == nil || len(review.writes) != count {
		t.Fatal("uncertain write retried")
	}
	r.OperationKey = strings.Repeat("9", 64)
	if _, err := e.Execute(context.Background(), r); err == nil || len(review.writes) != count {
		t.Fatal("uncertain write forked")
	}
}

func TestAppleMaterialRevisionReadOnlyRecoveryAfterConfirmedResubmission(t *testing.T) {
	source, e, base, material, review := appleRejectedMaterials(t)
	r := nextAppleMaterialRequest(t, e, source, 1)
	material.expectedDescription = "Reviewed correction 1"
	review.failWrite = "PATCH /v1/reviewSubmissions/submission1"
	if _, err := e.Execute(context.Background(), r); err == nil {
		t.Fatal("expected uncertain resubmission")
	}
	op, err := e.Inspect(context.Background(), r.OperationKey)
	if err != nil {
		t.Fatal(err)
	}
	before := len(review.writes)
	r.ArtifactPath = filepath.Join(t.TempDir(), "restored-approved.ipa")
	if err = os.WriteFile(r.ArtifactPath, base.uploaded, 0600); err != nil {
		t.Fatal(err)
	}
	in := AppleReconciliationRequest{Request: r, JournalDigest: op.JournalDigest, ConfirmStoppedOperationKey: r.OperationKey, Reason: "Synthetic stopped publisher; read exact remote resubmission"}
	if _, err = e.ReconcileApple(context.Background(), in); err == nil {
		t.Fatal("unresolved original submission treated as positive resubmission")
	}
	review.rejected = false
	base.storeState = "WAITING_FOR_REVIEW"
	result, err := e.ReconcileApple(context.Background(), in)
	if err != nil || result.State != "complete" || result.Result.State != "awaiting_review" || len(review.writes) != before {
		t.Fatal("exact read-only recovery failed", result, err)
	}
	if _, err = e.ReconcileApple(context.Background(), in); err != nil || len(review.writes) != before {
		t.Fatal("recovery replay wrote or failed", err)
	}
}

func TestAppleMaterialRevisionLostReservationStopsNextWrite(t *testing.T) {
	for _, scenario := range []string{"app-lock", "root-version", "parent-link", "source-journal"} {
		t.Run(scenario, func(t *testing.T) {
			source, e, _, material, review := appleRejectedMaterials(t)
			r := nextAppleMaterialRequest(t, e, source, 1)
			material.expectedDescription = "Reviewed correction 1"
			material.beforeWrite = func(path string) {
				if len(material.writes) != 1 {
					return
				}
				var err error
				switch scenario {
				case "app-lock":
					_, err = e.db.Exec("DELETE FROM apple_destinations")
				case "root-version":
					_, err = e.db.Exec("DELETE FROM apple_versions")
				case "parent-link":
					_, err = e.db.Exec("DELETE FROM apple_material_revisions")
				case "source-journal":
					_, err = e.db.Exec("UPDATE operations SET updated_at='changed' WHERE operation_key=?", source.OperationKey)
				}
				if err != nil {
					t.Error(err)
				}
			}
			if _, err := e.Execute(context.Background(), r); err == nil {
				t.Fatal("lost write reservation accepted")
			}
			if len(material.writes) != 1 || len(review.writes) != 0 {
				t.Fatal("lost reservation permitted another write", material.writes, review.writes)
			}
			op, err := e.Inspect(context.Background(), r.OperationKey)
			if err != nil || op.State != "pending" {
				t.Fatal("unknown partial write not retained", op, err)
			}
		})
	}
}

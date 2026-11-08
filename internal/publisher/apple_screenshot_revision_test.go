package publisher

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type screenshotRevisionFixture struct {
	mu         sync.Mutex
	images     map[string]map[string]any
	expected   map[string][]byte
	uploaded   map[string][]byte
	placements []map[string]any
	writes     []string
	ready      bool
	next       int
}

func screenshotFixture(t *testing.T) (Request, *Engine, *appleFixtureState, *appleMaterialsFixtureState, *materialReviewFixture, *screenshotRevisionFixture) {
	t.Helper()
	clone := func(value any, result any) { b, _ := json.Marshal(value); _ = json.Unmarshal(b, result) }
	state := &screenshotRevisionFixture{images: map[string]map[string]any{}, expected: map[string][]byte{}, uploaded: map[string][]byte{}}
	var previous *http.Client
	var oldBase string
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		path := q.Method + " " + q.URL.Path
		managed := strings.HasPrefix(q.URL.Path, "/v1/appAssetLibraryImages") || strings.HasPrefix(q.URL.Path, "/v1/appAssetLibraries/library1/images") || strings.HasPrefix(q.URL.Path, "/v1/appAssetLibraryPlacement") || strings.HasSuffix(q.URL.Path, "/placements") || strings.HasPrefix(q.URL.Path, "/revision-blob/")
		if !managed {
			proxy, _ := http.NewRequestWithContext(q.Context(), q.Method, oldBase+q.URL.RequestURI(), q.Body)
			proxy.Header = q.Header.Clone()
			response, err := previous.Do(proxy)
			if err != nil {
				t.Error(err)
				http.Error(w, "fixture proxy", 500)
				return
			}
			defer response.Body.Close()
			for key, values := range response.Header {
				for _, value := range values {
					w.Header().Add(key, value)
				}
			}
			w.WriteHeader(response.StatusCode)
			_, _ = io.Copy(w, response.Body)
			return
		}
		state.mu.Lock()
		defer state.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		respond := func(value any) { _ = json.NewEncoder(w).Encode(map[string]any{"data": value}) }
		if q.Method != "GET" {
			state.writes = append(state.writes, path)
		}
		if strings.HasPrefix(q.URL.Path, "/revision-blob/") {
			id := strings.TrimPrefix(q.URL.Path, "/revision-blob/")
			if q.Method != "PUT" || q.Header.Get("Authorization") != "" || q.Header.Get("Cookie") != "" {
				t.Error("image blob must not receive API credentials")
			}
			body, _ := io.ReadAll(q.Body)
			expected := state.expected[strings.TrimPrefix(state.images[id]["referenceName"].(string), "sha256:")]
			if !bytes.Equal(body, expected) || q.ContentLength != int64(len(body)) {
				t.Error("uploaded screenshot is not the exact approved bytes")
			}
			state.uploaded[id] = append(state.uploaded[id], body...)
			return
		}
		if !strings.HasPrefix(q.Header.Get("Authorization"), "Bearer ey") {
			t.Error("missing synthetic API authentication")
		}
		var in struct {
			Data struct {
				Type, ID      string
				Attributes    map[string]any
				Relationships map[string]struct{ Data json.RawMessage }
			}
		}
		if q.Method == "POST" || q.Method == "PATCH" {
			if json.NewDecoder(q.Body).Decode(&in) != nil {
				t.Error("invalid screenshot request JSON")
			}
		}
		switch {
		case path == "GET /v1/appAssetLibraries/library1/images":
			id := q.URL.Query().Get("filter[id]")
			attrs, ok := state.images[id]
			if !ok {
				respond([]any{})
				return
			}
			if state.ready && id != "image1" {
				attrs["state"], attrs["specId"], attrs["imageAsset"] = "PREPARE_FOR_SUBMISSION", "test-spec", map[string]any{"width": 100, "height": 200}
			}
			respond([]any{appleResourceFixture("appAssetLibraryImages", id, attrs, nil)})
		case path == "POST /v1/appAssetLibraryImages":
			var library appleRef
			_ = json.Unmarshal(in.Data.Relationships["assetLibrary"].Data, &library)
			digest := strings.TrimPrefix(fmt.Sprint(in.Data.Attributes["referenceName"]), "sha256:")
			body, ok := state.expected[digest]
			if !ok || library.ID != "library1" || library.Type != "appAssetLibraries" || in.Data.Attributes["fileSize"] != float64(len(body)) {
				t.Error("image reservation escaped the reviewed bytes/library")
			}
			state.next++
			id := fmt.Sprintf("replacement%d", state.next)
			attrs := map[string]any{"fileName": in.Data.Attributes["fileName"], "fileSize": len(body), "category": "APP_SCREENSHOTS_AND_PREVIEWS", "referenceName": in.Data.Attributes["referenceName"], "state": "AWAITING_UPLOAD", "uploadOperations": []any{map[string]any{"method": "PUT", "url": server.URL + "/revision-blob/" + id, "offset": 0, "length": len(body)}}}
			state.images[id] = attrs
			respond(appleResourceFixture("appAssetLibraryImages", id, attrs, nil))
		case q.Method == "PATCH" && strings.HasPrefix(q.URL.Path, "/v1/appAssetLibraryImages/"):
			id := strings.TrimPrefix(q.URL.Path, "/v1/appAssetLibraryImages/")
			attrs := state.images[id]
			if len(in.Data.Attributes) != 1 || in.Data.Attributes["uploaded"] != true || int64(len(state.uploaded[id])) != int64(attrs["fileSize"].(int)) {
				t.Error("image commit was not exact uploaded:true")
			}
			attrs["state"] = "UPLOAD_COMPLETE"
			respond(appleResourceFixture("appAssetLibraryImages", id, attrs, nil))
		case path == "GET /v1/appStoreVersionLocalizations/locale1/placements":
			if q.URL.Query().Get("sort") != "placementGroupPosition" {
				t.Error("missing independent display-order query")
			}
			respond(state.placements)
		case q.Method == "DELETE" && strings.HasPrefix(q.URL.Path, "/v1/appAssetLibraryPlacements/"):
			id := strings.TrimPrefix(q.URL.Path, "/v1/appAssetLibraryPlacements/")
			found := false
			for i, placement := range state.placements {
				if placement["id"] == id {
					state.placements = append(state.placements[:i], state.placements[i+1:]...)
					found = true
					break
				}
			}
			if !found {
				t.Error("deleted a placement outside the exact approved source")
			}
			w.WriteHeader(http.StatusNoContent)
		case path == "POST /v1/appAssetLibraryPlacements":
			var ref, locale appleRef
			_ = json.Unmarshal(in.Data.Relationships["image"].Data, &ref)
			_ = json.Unmarshal(in.Data.Relationships["appStoreVersionLocalization"].Data, &locale)
			if !state.ready || state.images[ref.ID] == nil || ref.Type != "appAssetLibraryImages" || locale.ID != "locale1" || in.Data.Attributes["placementGroup"] != "IPHONE_TEST_PROFILE" {
				t.Error("placed unready or unapproved image")
			}
			id := fmt.Sprintf("newplacement%d", len(state.writes))
			placement := appleResourceFixture("appAssetLibraryPlacements", id, map[string]any{"mediaType": "IMAGE", "placementType": "APP_SCREENSHOT", "placementGroup": "IPHONE_TEST_PROFILE", "state": "PARENT_PREPARE_FOR_SUBMISSION"}, map[string]any{"image": appleRelationshipFixture("appAssetLibraryImages", ref.ID)})
			state.placements = append(state.placements, placement)
			respond(placement)
		case path == "POST /v1/appAssetLibraryPlacementOrderingRequests":
			var refs []appleRef
			_ = json.Unmarshal(in.Data.Relationships["orderedPlacements"].Data, &refs)
			if len(refs) != len(state.placements) {
				t.Error("ordered an incomplete placement set")
			}
			ordered := []map[string]any{}
			seen := map[string]bool{}
			for _, ref := range refs {
				for _, placement := range state.placements {
					if placement["id"] == ref.ID && !seen[ref.ID] {
						ordered = append(ordered, placement)
						seen[ref.ID] = true
					}
				}
			}
			if len(ordered) != len(refs) {
				t.Error("unknown or duplicate placement order")
			}
			state.placements = ordered
			respond(appleResourceFixture("appAssetLibraryPlacementOrderingRequests", "neworder", nil, nil))
		default:
			t.Errorf("unexpected screenshot API request %s", path)
			http.Error(w, "unexpected", 500)
		}
	}))
	t.Cleanup(server.Close)
	u, _ := url.Parse(server.URL)
	source, engine, base, material, review := appleRejectedMaterials(t, func(e *Engine) {
		e.Config.Profiles[0].UploadHosts = append(e.Config.Profiles[0].UploadHosts, u.Host)
	})
	var old map[string]any
	clone(material.image, &old)
	old["state"], old["specId"], old["imageAsset"] = "PREPARE_FOR_SUBMISSION", "test-spec", map[string]any{"width": 100, "height": 200}
	state.images["image1"] = old
	clone(material.placements, &state.placements)
	previous, oldBase = engine.HTTP, engine.appleBase
	engine.appleBase, engine.HTTP = server.URL, server.Client()
	return source, engine, base, material, review, state
}

// Export raw local protocol fixtures for a separate SQLite/PNG/hash reader.
// These files contain synthetic bytes and no private key or API credential.
func screenshotProof(t *testing.T, name string, e *Engine, r Request, state *screenshotRevisionFixture, review *materialReviewFixture, base *appleFixtureState) {
	t.Helper()
	directory := os.Getenv("IS_SCREENSHOT_REVISION_EVIDENCE_DIR")
	if directory == "" {
		return
	}
	root := filepath.Join(directory, name)
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	write := func(name string, value any) {
		body, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(root, name), append(body, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.db.Exec("VACUUM INTO ?", filepath.Join(root, "publisher.db")); err != nil {
		t.Fatal(err)
	}
	operation, err := e.Inspect(context.Background(), r.OperationKey)
	if err != nil {
		t.Fatal(err)
	}
	write("operation.json", operation)
	write("request.json", r)
	profile := e.Config.Profiles[0]
	profile.CredentialsFile, profile.SigningKeyFile = "", ""
	write("profile.json", profile)
	write("remote.json", map[string]any{"images": state.images, "placements": state.placements, "writes": state.writes, "reviewWrites": review.writes, "packageWrites": base.writes, "ready": state.ready})
	if err = os.WriteFile(filepath.Join(root, "original.ipa"), base.uploaded, 0600); err != nil {
		t.Fatal(err)
	}
	if body, err := os.ReadFile(profile.MetadataFile); err != nil {
		t.Fatal(err)
	} else if err = os.WriteFile(filepath.Join(root, "manifest.json"), body, 0600); err != nil {
		t.Fatal(err)
	}
	for digest, body := range state.expected {
		if err = os.WriteFile(filepath.Join(root, digest+".png"), body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for id, body := range state.uploaded {
		if err = os.WriteFile(filepath.Join(root, id+".uploaded.png"), body, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func screenshotRequest(t *testing.T, e *Engine, state *screenshotRevisionFixture, source Request, n, count int) Request {
	t.Helper()
	r := nextAppleMaterialRequest(t, e, source, n)
	profile := &e.Config.Profiles[0]
	body, _ := os.ReadFile(profile.MetadataFile)
	var manifest map[string]any
	_ = json.Unmarshal(body, &manifest)
	locale := manifest["locales"].([]any)[0].(map[string]any)
	group := locale["screenshots"].([]any)[0].(map[string]any)
	images := []any{}
	for i := 0; i < count; i++ {
		img := image.NewRGBA(image.Rect(0, 0, 100, 200))
		for y := 0; y < 200; y++ {
			for x := 0; x < 100; x++ {
				img.SetRGBA(x, y, color.RGBA{uint8(40 + n*20 + i), 90, 150, 255})
			}
		}
		var data bytes.Buffer
		if err := png.Encode(&data, img); err != nil {
			t.Fatal(err)
		}
		b := data.Bytes()
		h := sha256.Sum256(b)
		digest := hex.EncodeToString(h[:])
		path := filepath.Join(filepath.Dir(profile.MetadataFile), fmt.Sprintf("replacement-%d-%d.png", n, i))
		if err := os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
		images = append(images, map[string]any{"path": path, "sha256": digest, "bytes": len(b), "specId": "test-spec"})
		state.expected[digest] = append([]byte(nil), b...)
	}
	group["images"] = images
	writeAppleMaterialFixture(t, profile, manifest)
	body, _ = json.Marshal(r)
	var fields map[string]any
	_ = json.Unmarshal(body, &fields)
	fields["materialScope"], fields["metadataDigest"] = "screenshots", profile.MetadataDigest
	body, _ = json.Marshal(fields)
	_ = json.Unmarshal(body, &r)
	return r
}

// An implementation which treats screenshot approval as ordinary text, deletes
// old placements while new bytes process, or uploads the IPA again fails here.
func TestAppleScreenshotRevisionWaitThenReplaceOnOriginalBuild(t *testing.T) {
	source, e, base, material, review, state := screenshotFixture(t)
	r := screenshotRequest(t, e, state, source, 1, 2)
	material.expectedDescription = "Reviewed correction 1"
	packageWrites := base.writes
	result, err := e.Execute(context.Background(), r)
	if err != nil || result.State != "processing" || result.ExternalID != source.ExternalID {
		t.Fatalf("screenshot revision must persist processing on original build: %+v %v", result, err)
	}
	if len(state.placements) != 1 || state.placements[0]["id"] != "placement1" || len(review.writes) != 0 || base.writes != packageWrites {
		t.Fatal("old screenshots/review/build changed before new media became ready")
	}
	screenshotProof(t, "publisher-processing", e, r, state, review, base)
	r.Action = "status"
	before := len(state.writes)
	if got, err := e.Execute(context.Background(), r); err != nil || got.ContinueRequired {
		t.Fatal("processing status should be read-only and not ready", got, err)
	}
	if len(state.writes) != before {
		t.Fatal("status mutated screenshot resources")
	}
	state.ready = true
	if got, err := e.Execute(context.Background(), r); err != nil || !got.ContinueRequired {
		t.Fatal("ready screenshots did not authorize exact continuation", got, err)
	}
	screenshotProof(t, "publisher-ready", e, r, state, review, base)
	r.Action = "continue"
	result, err = e.Execute(context.Background(), r)
	if err != nil || result.State != "awaiting_review" {
		t.Fatal("ready media did not resubmit original item", result, err)
	}
	if base.writes != packageWrites || len(review.writes) != 2 || len(state.placements) != 2 || state.images["image1"] == nil {
		t.Fatal("revision changed original build/review or deleted shared original image")
	}
	for _, write := range state.writes {
		if strings.HasPrefix(write, "DELETE /v1/appAssetLibraryImages/") {
			t.Fatal("shared image library was deleted")
		}
	}
	before = len(state.writes)
	if _, err = e.Execute(context.Background(), r); err != nil || len(state.writes) != before {
		t.Fatal("continuation replay repeated screenshot changes", err)
	}
	screenshotProof(t, "publisher-complete", e, r, state, review, base)
	var locks int
	if err = e.db.QueryRow("SELECT count(*) FROM apple_destinations").Scan(&locks); err != nil || locks != 0 {
		t.Fatal("submitted operation retained app lock", locks, err)
	}
}

func TestAppleScreenshotRevisionTwoRoundsRestartAndReadOnlyRecovery(t *testing.T) {
	source, e, base, material, review, state := screenshotFixture(t)
	packageWrites := base.writes
	for n := 1; n <= 2; n++ {
		r := screenshotRequest(t, e, state, source, n, 3-n)
		material.expectedDescription = fmt.Sprintf("Reviewed correction %d", n)
		state.ready = false
		if got, err := e.Execute(context.Background(), r); err != nil || got.State != "processing" {
			t.Fatal(got, err)
		}
		if n == 1 {
			config, api, client := e.Config, e.appleBase, e.HTTP
			if err := e.Close(); err != nil {
				t.Fatal(err)
			}
			var err error
			e, err = Open(config)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { e.Close() })
			e.appleBase, e.HTTP = api, client
		}
		state.ready = true
		r.Action = "continue"
		if n == 2 {
			e.HTTP.Transport = appleRecoveryTransport{base: e.HTTP.Transport, after: func(q *http.Request, response *http.Response) (*http.Response, error) {
				if q.Method == "PATCH" && q.URL.Path == "/v1/reviewSubmissions/submission1" {
					response.Body.Close()
					response.StatusCode = 503
					response.Body = io.NopCloser(strings.NewReader(`{"errors":[]}`))
				}
				return response, nil
			}}
			if _, err := e.Execute(context.Background(), r); err == nil {
				t.Fatal("fixture did not lose the accepted final response")
			}
			op, err := e.Inspect(context.Background(), r.OperationKey)
			if err != nil || op.State != "pending" || op.Receipt.Apple.Stage != "material_resubmit_intent" {
				t.Fatal(op, err)
			}
			screenshotProof(t, "publisher-lost-final", e, r, state, review, base)
			before := len(state.writes) + len(review.writes)
			r.ArtifactPath = filepath.Join(t.TempDir(), "restored-approved.ipa")
			if err = os.WriteFile(r.ArtifactPath, base.uploaded, 0600); err != nil {
				t.Fatal(err)
			}
			recovered, err := e.ReconcileApple(context.Background(), AppleReconciliationRequest{Request: r, JournalDigest: op.JournalDigest, ConfirmStoppedOperationKey: r.OperationKey, Reason: "Operator stopped the exact synthetic operation and checked all writes ended"})
			if err != nil || recovered.State != "complete" || recovered.Result.State != "awaiting_review" || len(state.writes)+len(review.writes) != before {
				t.Fatal("read-only recovery failed or wrote remotely", recovered, err)
			}
		} else if got, err := e.Execute(context.Background(), r); err != nil || got.State != "awaiting_review" {
			t.Fatal(got, err)
		}
		if base.writes != packageWrites || len(state.placements) != 3-n || state.images["image1"] == nil {
			t.Fatal("same-build revision replaced the package or deleted shared images")
		}
		screenshotProof(t, fmt.Sprintf("publisher-round-%d", n), e, r, state, review, base)
		source = r
		review.rejected, review.resolved, base.storeState = true, false, "METADATA_REJECTED"
	}
}

func TestAppleScreenshotRevisionPreflightAndWaitingDrift(t *testing.T) {
	for _, scenario := range []string{"text-scope", "invalid-scope", "locale", "catalog", "old-placement", "old-image", "waiting-placement", "source-journal", "lock-lost", "version-lost", "new-image"} {
		t.Run(scenario, func(t *testing.T) {
			source, e, _, material, _, state := screenshotFixture(t)
			r := screenshotRequest(t, e, state, source, 1, 1)
			material.expectedDescription = "Reviewed correction 1"
			preflight := true
			switch scenario {
			case "text-scope":
				r.MaterialScope = ""
			case "invalid-scope":
				r.MaterialScope = "force"
			case "locale", "catalog":
				p := &e.Config.Profiles[0]
				b, _ := os.ReadFile(p.MetadataFile)
				var m map[string]any
				_ = json.Unmarshal(b, &m)
				locale := m["locales"].([]any)[0].(map[string]any)
				if scenario == "locale" {
					locale["locale"] = "fr-FR"
				} else {
					locale["screenshots"].([]any)[0].(map[string]any)["images"].([]any)[0].(map[string]any)["specId"] = "unapproved-spec"
				}
				writeAppleMaterialFixture(t, p, m)
				r.MetadataDigest = p.MetadataDigest
			case "old-placement":
				state.placements[0]["id"] = "unapproved-placement"
			case "old-image":
				state.images["image1"]["fileName"] = "unapproved.png"
			default:
				preflight = false
			}
			if !preflight {
				if _, err := e.Execute(context.Background(), r); err != nil {
					t.Fatal(err)
				}
				state.ready = true
				r.Action = "continue"
				switch scenario {
				case "waiting-placement":
					state.placements[0]["id"] = "unapproved-placement"
				case "source-journal":
					_, err := e.db.Exec("UPDATE operations SET updated_at='changed' WHERE operation_key=?", source.OperationKey)
					if err != nil {
						t.Fatal(err)
					}
				case "lock-lost":
					if _, err := e.db.Exec("DELETE FROM apple_destinations"); err != nil {
						t.Fatal(err)
					}
				case "version-lost":
					if _, err := e.db.Exec("DELETE FROM apple_versions"); err != nil {
						t.Fatal(err)
					}
				case "new-image":
					state.images["replacement1"]["fileName"] = "unapproved.png"
				}
			}
			before := len(state.writes)
			if _, err := e.Execute(context.Background(), r); err == nil {
				t.Fatal("scope or exact material authority drift was ignored")
			}
			if len(state.writes) != before {
				t.Fatal("rejected preflight/continuation performed a screenshot write")
			}
		})
	}
}

func TestAppleScreenshotRevisionConcurrentContinuationDoesNotDuplicateWrites(t *testing.T) {
	source, one, _, material, review, state := screenshotFixture(t)
	r := screenshotRequest(t, one, state, source, 1, 1)
	material.expectedDescription = "Reviewed correction 1"
	if _, err := one.Execute(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	two, err := Open(one.Config)
	if err != nil {
		t.Fatal(err)
	}
	defer two.Close()
	two.appleBase, two.HTTP = one.appleBase, one.HTTP
	state.ready = true
	r.Action = "continue"
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, engine := range []*Engine{one, two} {
		wg.Add(1)
		go func(e *Engine) {
			defer wg.Done()
			<-start
			_, err := e.Execute(context.Background(), r)
			results <- err
		}(engine)
	}
	close(start)
	wg.Wait()
	close(results)
	passed := 0
	for err := range results {
		if err == nil {
			passed++
		}
	}
	deletes := 0
	for _, write := range state.writes {
		if write == "DELETE /v1/appAssetLibraryPlacements/placement1" {
			deletes++
		}
	}
	if passed == 0 || deletes != 1 || len(review.writes) != 2 || len(state.placements) != 1 {
		t.Fatal("two engines duplicated or lost the one approved continuation", passed, state.writes, review.writes)
	}
}

func TestAppleScreenshotRevisionUnknownWritesNeverReplayOrReconcileByGuess(t *testing.T) {
	for _, phase := range []string{"create", "bytes", "commit", "delete", "place", "order"} {
		t.Run(phase, func(t *testing.T) {
			source, e, base, material, review, state := screenshotFixture(t)
			count := 1
			if phase == "commit" || phase == "order" {
				// A lost non-final commit or incomplete final display cannot
				// satisfy complete-image/complete-placement recovery proof.
				count = 2
			}
			r := screenshotRequest(t, e, state, source, 1, count)
			material.expectedDescription = "Reviewed correction 1"
			if phase == "delete" || phase == "place" || phase == "order" {
				if _, err := e.Execute(context.Background(), r); err != nil {
					t.Fatal(err)
				}
				state.ready = true
				r.Action = "continue"
			}
			match := map[string]string{"create": "POST /v1/appAssetLibraryImages", "bytes": "PUT /revision-blob/replacement1", "commit": "PATCH /v1/appAssetLibraryImages/replacement1", "delete": "DELETE /v1/appAssetLibraryPlacements/placement1", "place": "POST /v1/appAssetLibraryPlacements", "order": "POST /v1/appAssetLibraryPlacementOrderingRequests"}[phase]
			e.HTTP.Transport = appleRecoveryTransport{base: e.HTTP.Transport, after: func(q *http.Request, response *http.Response) (*http.Response, error) {
				if q.Method+" "+q.URL.Path == match {
					response.Body.Close()
					response.StatusCode = 503
					response.Body = io.NopCloser(strings.NewReader(`{"errors":[]}`))
				}
				return response, nil
			}}
			if _, err := e.Execute(context.Background(), r); err == nil {
				t.Fatal("accepted response was not lost")
			}
			journal, err := e.Inspect(context.Background(), r.OperationKey)
			if err != nil || journal.State != "pending" {
				t.Fatal(journal, err)
			}
			if phase == "order" {
				// Positive final-order recovery is tested separately. Here the
				// provider has only a partial display, despite known local IDs.
				state.mu.Lock()
				state.placements = state.placements[:1]
				state.mu.Unlock()
			}
			before := len(state.writes) + len(review.writes)
			for _, action := range []string{"materials", "status", "continue"} {
				r.Action = action
				if _, err := e.Execute(context.Background(), r); err == nil {
					t.Fatal("uncertain write was replayed", action)
				}
			}
			r.ArtifactPath = filepath.Join(t.TempDir(), "restored-approved.ipa")
			if err = os.WriteFile(r.ArtifactPath, base.uploaded, 0600); err != nil {
				t.Fatal(err)
			}
			_, err = e.ReconcileApple(context.Background(), AppleReconciliationRequest{Request: r, JournalDigest: journal.JournalDigest, ConfirmStoppedOperationKey: r.OperationKey, Reason: "Stopped exact synthetic process; ambiguous intermediate writes must remain blocked"})
			if err == nil || len(state.writes)+len(review.writes) != before {
				t.Fatal("intermediate recovery guessed success or mutated")
			}
			after, _ := e.Inspect(context.Background(), r.OperationKey)
			screenshotProof(t, "publisher-unknown-"+phase, e, r, state, review, base)
			if after.JournalDigest != journal.JournalDigest || review.resolved {
				t.Fatal("failed recovery altered the journal or review item")
			}
		})
	}
}

func TestAppleScreenshotRevisionReordersAndReusesOnlyExactImageIdentity(t *testing.T) {
	source, e, base, material, review, state := screenshotFixture(t)
	first := screenshotRequest(t, e, state, source, 1, 3)
	material.expectedDescription = "Reviewed correction 1"
	if _, err := e.Execute(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	state.ready = true
	first.Action = "continue"
	if _, err := e.Execute(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	firstOp, err := e.Inspect(context.Background(), first.OperationKey)
	if err != nil {
		t.Fatal(err)
	}
	originals := firstOp.Receipt.Apple.Material.Locales[0].Screenshots[0].Images
	review.rejected, review.resolved, base.storeState = true, false, "METADATA_REJECTED"
	second := nextAppleMaterialRequest(t, e, first, 2)
	second.MaterialScope = "screenshots"
	p := &e.Config.Profiles[0]
	b, err := os.ReadFile(p.MetadataFile)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err = json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	images := m["locales"].([]any)[0].(map[string]any)["screenshots"].([]any)[0].(map[string]any)["images"].([]any)
	images[1], images[2] = images[2], images[1]
	writeAppleMaterialFixture(t, p, m)
	second.MetadataDigest = p.MetadataDigest
	material.expectedDescription = "Reviewed correction 2"
	uploads := len(state.uploaded)
	if got, err := e.Execute(context.Background(), second); err != nil || got.State != "processing" {
		t.Fatal(got, err)
	}
	if len(state.uploaded) != uploads+2 || state.next != 5 {
		t.Fatal("position-stable image should be reused, moved generated filenames require two exact uploads")
	}
	second.Action = "continue"
	if _, err = e.Execute(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	var op Operation
	op, err = e.Inspect(context.Background(), second.OperationKey)
	if err != nil {
		t.Fatal(err)
	}
	snapshots := op.Receipt.Apple.Material.Locales[0].Screenshots[0].Images
	if snapshots[0].ImageID != originals[0].ImageID || snapshots[1].SHA256 != originals[2].SHA256 || snapshots[2].SHA256 != originals[1].SHA256 || len(state.images) != 6 {
		t.Fatal("exact image order or shared library changed", snapshots)
	}
	screenshotProof(t, "publisher-reordered", e, second, state, review, base)
}

package publisher

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
)

type appleMaterialsFixtureState struct {
	mu                                 sync.Mutex
	writes                             []string
	imageReady                         bool
	versionText, infoText              map[string]any
	image                              map[string]any
	placements                         []any
	uploaded                           []byte
	imageBytes                         []byte
	failWrite                          string
	driftOnReview                      bool
	extraLocale, extraMedia, paginated bool
	beforeWrite                        func(string)
	expectedDescription                string
	expectedPrivacyURL                 string
}

func appleManagedFixture(t *testing.T) (Config, Request, *Engine, *appleFixtureState, *appleMaterialsFixtureState, *httptest.Server) {
	t.Helper()
	config, r, e, base, old := appleFixture(t)
	manifest, p := appleMaterialJSONFixture(t)
	writeAppleMaterialFixture(t, &p, manifest)
	loc := manifest["locales"].([]any)[0].(map[string]any)
	asset := loc["screenshots"].([]any)[0].(map[string]any)["images"].([]any)[0].(map[string]any)
	body, err := os.ReadFile(asset["path"].(string))
	if err != nil {
		t.Fatal(err)
	}
	state := &appleMaterialsFixtureState{versionText: map[string]any{"locale": "en-US", "description": "old"}, infoText: map[string]any{"locale": "en-US", "name": "Old"}, imageBytes: body, placements: []any{}}
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		path := q.Method + " " + q.URL.Path
		managed := strings.Contains(q.URL.Path, "Localizations") || strings.Contains(q.URL.Path, "/appInfos") || strings.Contains(q.URL.Path, "assetLibrary") || strings.Contains(q.URL.Path, "appAssetLibrar") || strings.HasPrefix(q.URL.Path, "/material-blob")
		if !managed {
			if state.driftOnReview && path == "POST /v1/reviewSubmissionItems" {
				state.mu.Lock()
				state.versionText["description"] = "unapproved edit"
				state.mu.Unlock()
			}
			old.Config.Handler.ServeHTTP(w, q)
			return
		}
		state.mu.Lock()
		defer state.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		respond := func(data any) {
			response := map[string]any{"data": data}
			if state.paginated {
				response["links"] = map[string]string{"next": "https://api.appstoreconnect.apple.com/next"}
			}
			_ = json.NewEncoder(w).Encode(response)
		}
		if q.Method != "GET" {
			state.writes = append(state.writes, path)
			if state.beforeWrite != nil {
				state.beforeWrite(path)
			}
			if state.failWrite == path {
				w.WriteHeader(503)
				return
			}
		}
		if strings.HasPrefix(q.URL.Path, "/material-blob") {
			if q.Header.Get("Authorization") != "" || q.Header.Get("Cookie") != "" || q.Method != "PUT" {
				t.Error("material blob leaked JWT/cookie or wrong method")
			}
			b, _ := io.ReadAll(q.Body)
			state.uploaded = append(state.uploaded, b...)
			if q.ContentLength != int64(len(b)) {
				t.Error("material byte range length missing")
			}
			return
		}
		if !strings.HasPrefix(q.Header.Get("Authorization"), "Bearer ey") {
			t.Error("material request missing API JWT")
		}
		var input struct {
			Data struct {
				Type          string         `json:"type"`
				ID            string         `json:"id"`
				Attributes    map[string]any `json:"attributes"`
				Relationships map[string]struct {
					Data json.RawMessage `json:"data"`
				} `json:"relationships"`
			} `json:"data"`
		}
		if q.Method != "GET" {
			if err := json.NewDecoder(q.Body).Decode(&input); err != nil {
				t.Error(err)
			}
		}
		switch path {
		case "GET /v1/apps/1234567890/appInfos":
			respond([]any{appleResourceFixture("appInfos", "info1", map[string]any{"state": "PREPARE_FOR_SUBMISSION"}, nil)})
		case "GET /v1/appStoreVersions/version1/appStoreVersionLocalizations":
			data := []any{appleResourceFixture("appStoreVersionLocalizations", "locale1", state.versionText, map[string]any{"appScreenshotSets": map[string]any{"data": []any{}}})}
			if state.extraLocale {
				data = append(data, appleResourceFixture("appStoreVersionLocalizations", "locale2", map[string]any{"locale": "fr-FR"}, nil))
			}
			respond(data)
		case "GET /v1/appInfos/info1/appInfoLocalizations":
			respond([]any{appleResourceFixture("appInfoLocalizations", "infolocale1", state.infoText, nil)})
		case "GET /v1/appStoreVersionLocalizations/locale1/appScreenshotSets", "GET /v1/appStoreVersionLocalizations/locale1/appPreviewSets":
			respond([]any{})
		case "GET /v1/apps/1234567890/assetLibrary":
			respond(appleResourceFixture("appAssetLibraries", "library1", nil, nil))
		case "GET /v1/appAssetLibraryRefData":
			respond([]any{appleResourceFixture("appAssetLibraryRefData", "1", appleCatalogFixtureJSON(), nil)})
		case "PATCH /v1/appStoreVersionLocalizations/locale1":
			description := state.expectedDescription
			if description == "" {
				description = "Reviewed description"
			}
			if input.Data.Attributes["description"] != description || input.Data.Attributes["supportUrl"] != "https://company.example/support" || input.Data.Attributes["marketingUrl"] != nil {
				t.Error("version material patch differs from reviewed values")
			}
			for k, v := range input.Data.Attributes {
				state.versionText[k] = v
			}
			respond(appleResourceFixture("appStoreVersionLocalizations", "locale1", state.versionText, nil))
		case "PATCH /v1/appInfoLocalizations/infolocale1":
			privacy := state.expectedPrivacyURL
			if privacy == "" {
				privacy = "https://company.example/privacy"
			}
			if input.Data.Attributes["privacyPolicyUrl"] != privacy || input.Data.Attributes["name"] != "Feedback" {
				t.Error("privacy/name patch is not reviewed content")
			}
			for k, v := range input.Data.Attributes {
				state.infoText[k] = v
			}
			respond(appleResourceFixture("appInfoLocalizations", "infolocale1", state.infoText, nil))
		case "POST /v1/appAssetLibraryImages":
			var library appleRef
			_ = json.Unmarshal(input.Data.Relationships["assetLibrary"].Data, &library)
			if library.Type != "appAssetLibraries" || library.ID != "library1" || input.Data.Attributes["category"] != "APP_SCREENSHOTS_AND_PREVIEWS" || input.Data.Attributes["fileSize"] != float64(len(body)) {
				t.Error("image reservation not bound to this app and reviewed bytes")
			}
			state.image = map[string]any{"fileName": input.Data.Attributes["fileName"], "fileSize": len(body), "category": "APP_SCREENSHOTS_AND_PREVIEWS", "referenceName": input.Data.Attributes["referenceName"], "state": "AWAITING_UPLOAD", "uploadOperations": []any{map[string]any{"method": "PUT", "url": server.URL + "/material-blob/1", "offset": 0, "length": len(body), "requestHeaders": []any{map[string]any{"name": "Content-Type", "value": "image/png"}}}}}
			respond(appleResourceFixture("appAssetLibraryImages", "image1", state.image, nil))
		case "PATCH /v1/appAssetLibraryImages/image1":
			if len(input.Data.Attributes) != 1 || input.Data.Attributes["uploaded"] != true || !bytes.Equal(state.uploaded, body) {
				t.Error("asset library commit must contain uploaded only after exact reviewed bytes")
			}
			state.image["state"] = "UPLOAD_COMPLETE"
			respond(appleResourceFixture("appAssetLibraryImages", "image1", state.image, nil))
		case "GET /v1/appAssetLibraries/library1/images":
			if q.URL.Query().Get("filter[id]") != "image1" {
				t.Error("image read not scoped to exact recorded ID")
			}
			if state.imageReady {
				state.image["state"] = "PREPARE_FOR_SUBMISSION"
				state.image["specId"] = "test-spec"
				state.image["imageAsset"] = map[string]any{"width": 100, "height": 200}
			}
			respond([]any{appleResourceFixture("appAssetLibraryImages", "image1", state.image, nil)})
		case "GET /v1/appStoreVersionLocalizations/locale1/placements":
			if q.URL.Query().Get("sort") != "placementGroupPosition" {
				t.Error("placement display order not independently queried")
			}
			data := append([]any{}, state.placements...)
			if state.extraMedia {
				data = append(data, appleResourceFixture("appAssetLibraryPlacements", "unexpected", map[string]any{"placementType": "APP_PREVIEW"}, nil))
			}
			respond(data)
		case "POST /v1/appAssetLibraryPlacements":
			var imageRef, localeRef appleRef
			_ = json.Unmarshal(input.Data.Relationships["image"].Data, &imageRef)
			_ = json.Unmarshal(input.Data.Relationships["appStoreVersionLocalization"].Data, &localeRef)
			if imageRef.ID != "image1" || localeRef.ID != "locale1" || input.Data.Attributes["placementType"] != "APP_SCREENSHOT" || input.Data.Attributes["placementGroup"] != "IPHONE_TEST_PROFILE" {
				t.Error("placement changed approved image/group/locale")
			}
			placement := appleResourceFixture("appAssetLibraryPlacements", "placement1", map[string]any{"mediaType": "IMAGE", "placementType": "APP_SCREENSHOT", "placementGroup": "IPHONE_TEST_PROFILE", "state": "PARENT_PREPARE_FOR_SUBMISSION"}, map[string]any{"image": appleRelationshipFixture("appAssetLibraryImages", "image1")})
			state.placements = append(state.placements, placement)
			respond(placement)
		case "POST /v1/appAssetLibraryPlacementOrderingRequests":
			var refs []appleRef
			_ = json.Unmarshal(input.Data.Relationships["orderedPlacements"].Data, &refs)
			if len(refs) != 1 || refs[0].ID != "placement1" || input.Data.Attributes["placementGroup"] != "IPHONE_TEST_PROFILE" {
				t.Error("order differs from manifest")
			}
			respond(appleResourceFixture("appAssetLibraryPlacementOrderingRequests", "order1", nil, nil))
		default:
			t.Errorf("unexpected managed request %s", path)
			http.Error(w, "unexpected", 500)
		}
	}))
	t.Cleanup(server.Close)
	u, _ := url.Parse(server.URL)
	config.Profiles[0].MetadataFile = p.MetadataFile
	config.Profiles[0].MetadataDigest = p.MetadataDigest
	config.Profiles[0].UploadHosts = append(config.Profiles[0].UploadHosts, u.Host)
	e.Config = config
	e.appleBase = server.URL
	e.HTTP = server.Client()
	r.MetadataDigest = p.MetadataDigest
	return config, r, e, base, state, server
}

func TestAppleManagedMaterialProcessingAndExactReview(t *testing.T) {
	config, r, e, base, state, server := appleManagedFixture(t)
	ctx := context.Background()
	result, err := e.Execute(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.writes) != 5 || result.State != "processing" || !bytes.Equal(state.uploaded, state.imageBytes) {
		t.Fatalf("managed metadata and image upload missing: writes=%v result=%+v", state.writes, result)
	}
	op, err := e.Inspect(ctx, r.OperationKey)
	if err != nil {
		t.Fatal(err)
	}
	journal := mustAppleReceiptJSON(op.Receipt)
	if !strings.Contains(journal, r.MetadataDigest) || !strings.Contains(journal, `"imageId":"image1"`) || strings.Contains(journal, `"path"`) {
		t.Fatal("material journal is not a bound immutable image snapshot")
	}
	e.Close()
	e, err = Open(config)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	configureAppleFixture(e, server)
	r.ArtifactPath = ""
	r.ExternalID = result.ExternalID
	r.Action = "status"
	base.uploadState = "COMPLETE"
	before := len(state.writes)
	result, err = e.Execute(ctx, r)
	if err != nil || result.ContinueRequired {
		t.Fatal("incomplete image processing allowed review", result, err)
	}
	state.imageReady = true
	result, err = e.Execute(ctx, r)
	if err != nil || !result.ContinueRequired {
		t.Fatal("processed exact image/build did not allow continuation", result, err)
	}
	if len(state.writes) != before {
		t.Fatal("status mutated material")
	}
	r.Action = "continue"
	result, err = e.Execute(ctx, r)
	if err != nil || result.State != "awaiting_review" {
		t.Fatal(result, err)
	}
	if len(state.placements) != 1 || len(state.writes) != 7 {
		t.Fatal("placements and ordering not performed exactly once", state.writes)
	}
	if _, err = e.Execute(ctx, r); err != nil || len(state.writes) != 7 {
		t.Fatal("replayed a completed material mutation", err)
	}
	var locks int
	if err = e.db.QueryRow("SELECT count(*) FROM apple_destinations").Scan(&locks); err != nil || locks != 0 {
		t.Fatal("confirmed submission did not release app lock", locks, err)
	}
}

func TestAppleManagedMaterialsRejectPreflightScopeAndReviewDrift(t *testing.T) {
	for _, scenario := range []string{"extra-locale", "extra-media", "paginated", "text-before-continue", "text-before-final-submit"} {
		t.Run(scenario, func(t *testing.T) {
			_, r, e, base, state, _ := appleManagedFixture(t)
			defer e.Close()
			ctx := context.Background()
			switch scenario {
			case "extra-locale":
				state.extraLocale = true
			case "extra-media":
				state.extraMedia = true
			case "paginated":
				state.paginated = true
			}
			result, err := e.Execute(ctx, r)
			if strings.HasPrefix(scenario, "text-") {
				if err != nil {
					t.Fatal(err)
				}
				base.uploadState = "COMPLETE"
				state.imageReady = true
				r.Action = "continue"
				r.ExternalID = result.ExternalID
				if scenario == "text-before-continue" {
					state.infoText["privacyPolicyUrl"] = "https://attacker.example/privacy"
				} else {
					state.driftOnReview = true
				}
				_, err = e.Execute(ctx, r)
			}
			if err == nil {
				t.Fatal("unapproved or incomplete material allowed publication")
			}
			if !strings.HasPrefix(scenario, "text-") && (len(state.writes) != 0 || base.writes != 0) {
				t.Fatal("preflight failure performed external writes")
			}
			if base.storeState == "WAITING_FOR_REVIEW" {
				t.Fatal("material drift reached final review submit")
			}
		})
	}
}

func TestAppleManagedUnknownWritesNeverReplay(t *testing.T) {
	paths := []string{"PATCH /v1/appStoreVersionLocalizations/locale1", "PATCH /v1/appInfoLocalizations/infolocale1", "POST /v1/appAssetLibraryImages", "PUT /material-blob/1", "PATCH /v1/appAssetLibraryImages/image1", "POST /v1/appAssetLibraryPlacements", "POST /v1/appAssetLibraryPlacementOrderingRequests"}
	for i, path := range paths {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			config, r, e, base, state, server := appleManagedFixture(t)
			ctx := context.Background()
			state.failWrite = path
			result, err := e.Execute(ctx, r)
			if i >= 5 {
				if err != nil {
					t.Fatal(err)
				}
				r.Action = "continue"
				r.ExternalID = result.ExternalID
				state.imageReady = true
				base.uploadState = "COMPLETE"
				_, err = e.Execute(ctx, r)
			}
			if err == nil {
				t.Fatal("ambiguous write reported success")
			}
			before := len(state.writes)
			e.Close()
			e, err = Open(config)
			if err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			configureAppleFixture(e, server)
			if _, err = e.Execute(ctx, r); err == nil {
				t.Fatal("unknown material operation was replayed")
			}
			r.Action = "publish"
			r.OperationKey = strings.Repeat(fmt.Sprint(i+1), 64)
			if _, err = e.Execute(ctx, r); err == nil {
				t.Fatal("new operation key bypassed app/version reservation")
			}
			if len(state.writes) != before {
				t.Fatal("unknown operation issued another write")
			}
		})
	}
}

func TestAppleManagedDuplicateWriteResponseCannotConfirm(t *testing.T) {
	_, r, e, _, _, _ := appleManagedFixture(t)
	defer e.Close()
	e.HTTP.Transport = appleRecoveryTransport{base: e.HTTP.Transport, after: func(q *http.Request, res *http.Response) (*http.Response, error) {
		if q.Method == "PATCH" && q.URL.Path == "/v1/appAssetLibraryImages/image1" {
			res.Body.Close()
			res.Body = io.NopCloser(strings.NewReader(`{"data":{"type":"appAssetLibraryImages","id":"image1","attributes":{"state":"FAILED","state":"UPLOAD_COMPLETE"}}}`))
		}
		return res, nil
	}}
	if _, err := e.Execute(context.Background(), r); err == nil {
		t.Fatal("duplicate response state falsely confirmed material write")
	}
}

func TestAppleManagedPlacementOrderReadBack(t *testing.T) {
	for _, scenario := range []string{"ordered", "reversed", "duplicate", "wrong-image", "wrong-group", "processing"} {
		t.Run(scenario, func(t *testing.T) {
			_, _, e, _, state, _ := appleManagedFixture(t)
			defer e.Close()
			locale := &appleLocaleSnapshot{VersionLocalizationID: "locale1", Screenshots: []appleGroupSnapshot{{PlacementGroup: "IPHONE_TEST_PROFILE", Images: []appleImageSnapshot{{ImageID: "image1", PlacementID: "placement1"}, {ImageID: "image2", PlacementID: "placement2"}}}}}
			makePlacement := func(id, image string) map[string]any {
				return appleResourceFixture("appAssetLibraryPlacements", id, map[string]any{"mediaType": "IMAGE", "placementType": "APP_SCREENSHOT", "placementGroup": "IPHONE_TEST_PROFILE", "state": "PARENT_PREPARE_FOR_SUBMISSION"}, map[string]any{"image": appleRelationshipFixture("appAssetLibraryImages", image)})
			}
			first, second := makePlacement("placement1", "image1"), makePlacement("placement2", "image2")
			state.placements = []any{first, second}
			switch scenario {
			case "reversed":
				state.placements = []any{second, first}
			case "duplicate":
				state.placements = []any{first, first}
			case "wrong-image":
				second["relationships"] = map[string]any{"image": appleRelationshipFixture("appAssetLibraryImages", "unapproved")}
			case "wrong-group":
				second["attributes"].(map[string]any)["placementGroup"] = "IPAD_OTHER"
			case "processing":
				second["attributes"].(map[string]any)["state"] = "ASSET_PROCESSING"
			}
			err := e.appleMaterialMedia(context.Background(), e.Config.Profiles[0], locale, true)
			if scenario == "ordered" && err != nil {
				t.Fatal(err)
			}
			if scenario != "ordered" && err == nil {
				t.Fatal("incorrect screenshot display order or identity accepted")
			}
		})
	}
}

func TestAppleManagedImageStatusRejectsForeignOrIncompleteProof(t *testing.T) {
	for _, scenario := range []string{"foreign-image", "wrong-size", "wrong-category", "wrong-spec", "wrong-dimensions", "failed-image", "unknown-state", "pagination"} {
		t.Run(scenario, func(t *testing.T) {
			_, r, e, base, state, _ := appleManagedFixture(t)
			defer e.Close()
			ctx := context.Background()
			result, err := e.Execute(ctx, r)
			if err != nil {
				t.Fatal(err)
			}
			r.Action = "status"
			r.ExternalID = result.ExternalID
			base.mu.Lock()
			base.uploadState = "COMPLETE"
			base.mu.Unlock()
			state.mu.Lock()
			state.imageReady = true
			state.mu.Unlock()
			e.HTTP.Transport = appleRecoveryTransport{base: e.HTTP.Transport, after: func(q *http.Request, res *http.Response) (*http.Response, error) {
				if q.URL.Path != "/v1/appAssetLibraries/library1/images" {
					return res, nil
				}
				var body map[string]any
				if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				res.Body.Close()
				resource := body["data"].([]any)[0].(map[string]any)
				attrs := resource["attributes"].(map[string]any)
				switch scenario {
				case "foreign-image":
					resource["id"] = "other-image"
				case "wrong-size":
					attrs["fileSize"] = 1
				case "wrong-category":
					attrs["category"] = "CREATIVE_ASSETS"
				case "wrong-spec":
					attrs["specId"] = "other-spec"
				case "wrong-dimensions":
					attrs["imageAsset"].(map[string]any)["width"] = 101
				case "failed-image":
					attrs["state"] = "FAILED"
				case "unknown-state":
					attrs["state"] = "NEW_UNRECOGNIZED_STATE"
				case "pagination":
					body["links"] = map[string]string{"next": "https://outside.invalid/page"}
				}
				b, _ := json.Marshal(body)
				res.Body = io.NopCloser(bytes.NewReader(b))
				return res, nil
			}}
			state.mu.Lock()
			before := len(state.writes)
			state.mu.Unlock()
			if _, err = e.Execute(ctx, r); err == nil {
				t.Fatal("image status granted review continuation from foreign or incomplete proof")
			}
			state.mu.Lock()
			defer state.mu.Unlock()
			if len(state.writes) != before {
				t.Fatal("status failure mutated Apple")
			}
		})
	}
}

func TestAppleManagedConcurrentAppReservationsPreventDuplicateWrites(t *testing.T) {
	for _, sameKey := range []bool{true, false} {
		t.Run(fmt.Sprint(sameKey), func(t *testing.T) {
			config, r, e, _, state, server := appleManagedFixture(t)
			defer e.Close()
			ctx := context.Background()
			other, err := Open(config)
			if err != nil {
				t.Fatal(err)
			}
			defer other.Close()
			configureAppleFixture(other, server)
			entered, release := make(chan struct{}), make(chan struct{})
			defer func() {
				select {
				case <-release:
				default:
					close(release)
				}
			}()
			e.HTTP.Transport = appleRecoveryTransport{base: e.HTTP.Transport, after: func(q *http.Request, res *http.Response) (*http.Response, error) {
				if q.Method == "PATCH" && q.URL.Path == "/v1/appStoreVersionLocalizations/locale1" {
					close(entered)
					<-release
				}
				return res, nil
			}}
			done := make(chan error, 1)
			go func() { _, err := e.Execute(ctx, r); done <- err }()
			<-entered
			second := r
			if !sameKey {
				second.OperationKey = strings.Repeat("d", 64)
			}
			if _, err = other.Execute(ctx, second); err == nil {
				t.Error("second process acquired an in-flight app operation")
			}
			close(release)
			if err = <-done; err != nil {
				t.Fatal(err)
			}
			state.mu.Lock()
			defer state.mu.Unlock()
			if len(state.writes) != 5 {
				t.Fatal("concurrent process duplicated external writes", state.writes)
			}
		})
	}
}

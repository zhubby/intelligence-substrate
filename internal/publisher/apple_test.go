package publisher

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
)

type appleFixtureState struct {
	mu                          sync.Mutex
	uploadState, storeState     string
	wrongBuild, uncertainReview bool
	writes                      int
	uploaded                    []byte
}

func appleRefFixture(kind, id string) map[string]any { return map[string]any{"type": kind, "id": id} }
func appleResourceFixture(kind, id string, attributes any, relationships map[string]any) map[string]any {
	return map[string]any{"type": kind, "id": id, "attributes": attributes, "relationships": relationships}
}
func appleRelationshipFixture(kind, id string) map[string]any {
	return map[string]any{"data": appleRefFixture(kind, id)}
}

// API contract fixture deliberately substitutes package signature verification;
// none of these tests claim a signed IPA or live Apple account.
func appleFixture(t *testing.T) (Config, Request, *Engine, *appleFixtureState, *httptest.Server) {
	t.Helper()
	config, r := publisherFixture(t)
	credentials, _ := appleCredentials(t)
	r.Target = "ios-appstore"
	content, err := os.ReadFile(r.ArtifactPath)
	if err != nil {
		t.Fatal(err)
	}
	state := &appleFixtureState{uploadState: "PROCESSING", storeState: "PREPARE_FOR_SUBMISSION"}
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		state.mu.Lock()
		defer state.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		respond := func(data any) {
			if e := json.NewEncoder(w).Encode(map[string]any{"data": data}); e != nil {
				t.Error(e)
			}
		}
		if q.Method != "GET" {
			state.writes++
		}
		body, _ := io.ReadAll(q.Body)
		var input map[string]any
		if len(body) > 0 && !strings.HasPrefix(q.URL.Path, "/blob/") {
			if e := json.Unmarshal(body, &input); e != nil {
				t.Error(e)
			}
		}
		if strings.HasPrefix(q.URL.Path, "/blob/") {
			if q.Header.Get("Authorization") != "" || q.Header.Get("Cookie") != "" {
				t.Error("ASC credentials leaked to blob host")
			}
			if q.Method != "PUT" {
				t.Error("blob operation not PUT")
			}
			state.uploaded = append(state.uploaded, body...)
			w.WriteHeader(200)
			return
		}
		if !strings.HasPrefix(q.Header.Get("Authorization"), "Bearer ey") {
			t.Error("missing Apple JWT")
		}
		version := func() map[string]any {
			rel := map[string]any{"app": appleRelationshipFixture("apps", "1234567890")}
			if state.storeState != "PREPARE_FOR_SUBMISSION" {
				rel["build"] = appleRelationshipFixture("builds", "build1")
			}
			return appleResourceFixture("appStoreVersions", "version1", map[string]any{"versionString": "1.0.0", "platform": "IOS", "appStoreState": state.storeState, "releaseType": "AFTER_APPROVAL"}, rel)
		}
		switch q.Method + " " + q.URL.Path {
		case "GET /v1/apps/1234567890":
			respond(appleResourceFixture("apps", "1234567890", map[string]any{"bundleId": "com.company.feedback"}, nil))
		case "GET /v1/apps/1234567890/appStoreVersions":
			if q.URL.Query().Get("filter[versionString]") != "1.0.0" || q.URL.Query().Get("filter[platform]") != "IOS" {
				t.Error("version selection not exact")
			}
			respond([]any{version()})
		case "GET /v1/appStoreVersions/version1":
			respond(version())
		case "POST /v1/buildUploads":
			attrs := input["data"].(map[string]any)["attributes"].(map[string]any)
			if attrs["cfBundleVersion"] != "42" || attrs["cfBundleShortVersionString"] != "1.0.0" || attrs["platform"] != "IOS" {
				t.Error("wrong uploaded version")
			}
			respond(appleResourceFixture("buildUploads", "upload1", nil, nil))
		case "POST /v1/buildUploadFiles":
			attrs := input["data"].(map[string]any)["attributes"].(map[string]any)
			if attrs["assetType"] != "ASSET" || attrs["uti"] != "com.apple.ipa" || attrs["fileSize"] != float64(len(content)) {
				t.Error("wrong IPA reservation")
			}
			half := int64(len(content) / 2)
			ops := []map[string]any{{"method": "PUT", "url": server.URL + "/blob/1", "offset": 0, "length": half}, {"method": "PUT", "url": server.URL + "/blob/2", "offset": half, "length": int64(len(content)) - half}}
			respond(appleResourceFixture("buildUploadFiles", "file1", map[string]any{"fileSize": len(content), "fileName": "approved.ipa", "uploadOperations": ops}, nil))
		case "PATCH /v1/buildUploadFiles/file1":
			attrs := input["data"].(map[string]any)["attributes"].(map[string]any)
			hash := md5.Sum(content)
			checks := attrs["sourceFileChecksums"].(map[string]any)["file"].(map[string]any)
			if attrs["uploaded"] != true || checks["algorithm"] != "MD5" || checks["hash"] != hex.EncodeToString(hash[:]) || !bytes.Equal(state.uploaded, content) {
				t.Error("commit is not exact approved bytes")
			}
			respond(appleResourceFixture("buildUploadFiles", "file1", map[string]any{"assetDeliveryState": map[string]string{"state": "UPLOAD_COMPLETE"}}, nil))
		case "GET /v1/buildUploads/upload1":
			build := "build1"

			respond(appleResourceFixture("buildUploads", "upload1", map[string]any{"cfBundleVersion": "42", "cfBundleShortVersionString": "1.0.0", "platform": "IOS", "state": map[string]string{"state": state.uploadState}}, map[string]any{"build": appleRelationshipFixture("builds", build)}))
		case "GET /v1/buildUploads/upload1/buildUploadFiles":
			hash := md5.Sum(content)
			respond([]any{appleResourceFixture("buildUploadFiles", "file1", map[string]any{"assetType": "ASSET", "uti": "com.apple.ipa", "fileName": "approved.ipa", "fileSize": len(content), "assetDeliveryState": map[string]string{"state": "UPLOAD_COMPLETE"}, "sourceFileChecksums": map[string]any{"file": map[string]string{"algorithm": "MD5", "hash": hex.EncodeToString(hash[:])}}}, nil)})
		case "GET /v1/builds/build1":
			buildNumber := "42"
			if state.wrongBuild {
				buildNumber = "43"
			}
			res := map[string]any{"data": appleResourceFixture("builds", "build1", map[string]any{"version": buildNumber, "processingState": "VALID", "expired": false}, map[string]any{"app": appleRelationshipFixture("apps", "1234567890"), "buildUpload": appleRelationshipFixture("buildUploads", "upload1"), "preReleaseVersion": appleRelationshipFixture("preReleaseVersions", "pre1")}), "included": []any{appleResourceFixture("preReleaseVersions", "pre1", map[string]any{"version": "1.0.0", "platform": "IOS"}, nil)}}
			_ = json.NewEncoder(w).Encode(res)
		case "PATCH /v1/appStoreVersions/version1":
			data := input["data"].(map[string]any)
			rel := data["relationships"].(map[string]any)["build"].(map[string]any)["data"].(map[string]any)
			if rel["id"] != "build1" {
				t.Error("different build linked")
			}
			state.storeState = "READY_FOR_REVIEW"
			respond(version())
		case "POST /v1/reviewSubmissions":
			if state.uncertainReview {
				w.WriteHeader(503)
				return
			}
			respond(appleResourceFixture("reviewSubmissions", "submission1", map[string]string{"state": "READY_FOR_REVIEW"}, map[string]any{"app": appleRelationshipFixture("apps", "1234567890")}))
		case "POST /v1/reviewSubmissionItems":
			respond(appleResourceFixture("reviewSubmissionItems", "item1", nil, nil))
		case "PATCH /v1/reviewSubmissions/submission1":
			if input["data"].(map[string]any)["attributes"].(map[string]any)["submitted"] != true {
				t.Error("review not submitted")
			}
			state.storeState = "WAITING_FOR_REVIEW"
			respond(appleResourceFixture("reviewSubmissions", "submission1", map[string]string{"state": "WAITING_FOR_REVIEW"}, map[string]any{"app": appleRelationshipFixture("apps", "1234567890")}))
		case "GET /v1/reviewSubmissions/submission1":
			respond(appleResourceFixture("reviewSubmissions", "submission1", map[string]string{"platform": "IOS", "state": "WAITING_FOR_REVIEW", "submittedDate": "2026-11-05T10:00:00Z"}, map[string]any{"app": appleRelationshipFixture("apps", "1234567890"), "appStoreVersionForReview": appleRelationshipFixture("appStoreVersions", "version1")}))
		case "GET /v1/reviewSubmissions/submission1/items":
			respond([]any{appleResourceFixture("reviewSubmissionItems", "item1", map[string]string{"state": "ACCEPTED"}, map[string]any{"appStoreVersion": appleRelationshipFixture("appStoreVersions", "version1")})})
		default:
			t.Errorf("unexpected Apple request %s %s", q.Method, q.URL.Path)
			http.Error(w, "unexpected", 500)
		}
	}))
	t.Cleanup(server.Close)
	u, _ := url.Parse(server.URL)
	config.Profiles[0] = Profile{ProjectID: r.ProjectID, GoalID: r.GoalID, Target: "ios-appstore", Driver: "app-store", AppID: "1234567890", BundleID: "com.company.feedback", ExpectedSigner: "ABCD123456", CredentialsFile: credentials, UploadHosts: []string{u.Host}}
	e, err := Open(config)
	if err != nil {
		t.Fatal(err)
	}
	configureAppleFixture(e, server)
	return config, r, e, state, server
}
func configureAppleFixture(e *Engine, server *httptest.Server) {
	e.appleBase = server.URL
	e.HTTP = server.Client()
	e.verifyApplePackage = func(context.Context, Profile, Request, string) (applePackage, error) {
		return applePackage{Version: "1.0.0", BuildNumber: "42"}, nil
	}
}

func TestAppleSyntheticPackageUploadWaitReviewAndReadOnlyStatus(t *testing.T) {
	config, r, e, state, server := appleFixture(t)
	ctx := context.Background()
	result, err := e.Execute(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if result.State != "processing" || result.ExternalID == "" {
		t.Fatalf("upload mislabeled review: %+v", result)
	}
	external := result.ExternalID
	state.mu.Lock()
	before := state.writes
	state.mu.Unlock()
	e.Close()
	e, err = Open(config)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	configureAppleFixture(e, server)
	r.Action = "status"
	r.ExternalID = external
	r.ArtifactPath = ""
	result, err = e.Execute(ctx, r)
	if err != nil || result.State != "processing" || result.ContinueRequired {
		t.Fatal(result, err)
	}
	state.mu.Lock()
	if state.writes != before {
		t.Error("read-only processing query mutated Apple")
	}
	state.uploadState = "COMPLETE"
	state.mu.Unlock()
	result, err = e.Execute(ctx, r)
	if err != nil || !result.ContinueRequired {
		t.Fatal(result, err)
	}
	r.Action = "continue"
	result, err = e.Execute(ctx, r)
	if err != nil || result.State != "awaiting_review" || result.ExternalID != external {
		t.Fatal(result, err)
	}
	state.mu.Lock()
	before = state.writes
	state.mu.Unlock()
	if replay, err := e.Execute(ctx, r); err != nil || replay.State != "awaiting_review" {
		t.Fatal("continuation not durably idempotent", replay, err)
	}
	state.mu.Lock()
	if state.writes != before {
		t.Error("completed continuation resubmitted review")
	}
	state.storeState = "PENDING_DEVELOPER_RELEASE"
	state.mu.Unlock()
	r.Action = "status"
	result, err = e.Execute(ctx, r)
	if err != nil || result.State != "awaiting_release" {
		t.Fatal("approved version mislabeled as awaiting review", result, err)
	}
	state.mu.Lock()
	state.storeState = "READY_FOR_SALE"
	state.mu.Unlock()
	r.Action = "status"
	result, err = e.Execute(ctx, r)
	if err != nil || result.State != "published" {
		t.Fatal(result, err)
	}
	state.mu.Lock()
	if state.writes != before {
		t.Error("status query mutated review")
	}
	state.mu.Unlock()
	r.Binding = strings.Repeat("f", 64)
	if _, err = e.Execute(ctx, r); err == nil {
		t.Fatal("different approval borrowed Apple journal")
	}
}

func TestAppleUncertainReviewRestartNeverResubmits(t *testing.T) {
	config, r, e, state, server := appleFixture(t)
	ctx := context.Background()
	result, err := e.Execute(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	state.mu.Lock()
	state.uploadState = "COMPLETE"
	state.uncertainReview = true
	state.mu.Unlock()
	r.Action = "continue"
	r.ExternalID = result.ExternalID
	r.ArtifactPath = ""
	if _, err = e.Execute(ctx, r); err == nil {
		t.Fatal("uncertain review accepted")
	}
	op, err := e.Inspect(ctx, r.OperationKey)
	if err != nil || op.State != "pending" || op.Receipt.Apple == nil || op.Receipt.Apple.Stage != "review_create_intent" {
		t.Fatal("missing uncertain stage", op, err)
	}
	state.mu.Lock()
	before := state.writes
	state.mu.Unlock()
	e.Close()
	e, err = Open(config)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	configureAppleFixture(e, server)
	if _, err = e.Execute(ctx, r); err == nil {
		t.Fatal("uncertain review restarted automatically")
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.writes != before {
		t.Fatal("uncertain write replayed")
	}
}

func TestAppleFailedProcessingCannotSubmitForReview(t *testing.T) {
	_, r, e, state, _ := appleFixture(t)
	defer e.Close()
	ctx := context.Background()
	result, err := e.Execute(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	state.mu.Lock()
	state.uploadState = "FAILED"
	before := state.writes
	state.mu.Unlock()
	r.Action = "status"
	r.ExternalID = result.ExternalID
	r.ArtifactPath = ""
	result, err = e.Execute(ctx, r)
	if err != nil || result.State != "rejected" || result.ContinueRequired {
		t.Fatal(result, err)
	}
	r.Action = "continue"
	if _, err = e.Execute(ctx, r); err == nil {
		t.Fatal("failed upload submitted for review")
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.writes != before {
		t.Fatal("failed processing caused a mutation")
	}
}

func TestAppleConcurrentContinuationHasOneExternalSubmission(t *testing.T) {
	config, r, e, state, server := appleFixture(t)
	defer e.Close()
	ctx := context.Background()
	result, err := e.Execute(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Open(config)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	configureAppleFixture(second, server)
	state.mu.Lock()
	state.uploadState = "COMPLETE"
	before := state.writes
	state.mu.Unlock()
	r.Action = "continue"
	r.ExternalID = result.ExternalID
	r.ArtifactPath = ""
	var wg sync.WaitGroup
	errors := make(chan error, 2)
	for _, engine := range []*Engine{e, second} {
		wg.Add(1)
		go func(engine *Engine) { defer wg.Done(); _, err := engine.Execute(ctx, r); errors <- err }(engine)
	}
	wg.Wait()
	close(errors)
	success := 0
	for err := range errors {
		if err == nil {
			success++
		}
	}
	if success < 1 {
		t.Fatal("neither continuation completed")
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.writes-before != 4 {
		t.Fatalf("review mutation repeated: %d writes", state.writes-before)
	}
}

func TestAppleProcessedBuildAndPreparedVersionDriftCannotMutate(t *testing.T) {
	for _, scenario := range []string{"build", "store"} {
		t.Run(scenario, func(t *testing.T) {
			_, r, e, state, _ := appleFixture(t)
			defer e.Close()
			ctx := context.Background()
			result, err := e.Execute(ctx, r)
			if err != nil {
				t.Fatal(err)
			}
			state.mu.Lock()
			state.uploadState = "COMPLETE"
			before := state.writes
			if scenario == "build" {
				state.wrongBuild = true
			} else {
				state.storeState = "IN_REVIEW"
			}
			state.mu.Unlock()
			r.Action = "continue"
			r.ExternalID = result.ExternalID
			r.ArtifactPath = ""
			if _, err = e.Execute(ctx, r); err == nil {
				t.Fatal("changed store/build allowed review")
			}
			state.mu.Lock()
			defer state.mu.Unlock()
			if state.writes != before {
				t.Fatal("drifted build caused external mutation")
			}
		})
	}
}

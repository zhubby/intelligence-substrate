package publisher

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func huaweiConfig(t *testing.T) (Config, Request) {
	t.Helper()
	config, request := publisherFixture(t)
	credentials := filepath.Join(t.TempDir(), "huawei-client.json")
	if err := os.WriteFile(credentials, []byte(`{"clientId":"123456789","clientSecret":"test-only-api-client-secret"}`), 0600); err != nil {
		t.Fatal(err)
	}
	// JSON exercises the administrator configuration boundary, including fields
	// absent before this driver. These paths are never executed by this helper.
	body, _ := json.Marshal(map[string]any{
		"projectId": request.ProjectID, "goalId": request.GoalID,
		"target": "android-huawei", "driver": "huawei-appgallery",
		"appId": "1234567890", "bundleId": "com.company.feedback",
		"expectedSigner": strings.Repeat("c", 64), "credentialsFile": credentials,
		"aaptPath": "/opt/android/build-tools/aapt", "apkSignerPath": "/opt/android/build-tools/apksigner",
		"uploadHosts": []string{"uploads.example"}, "chineseMainland": true,
	})
	if err := json.Unmarshal(body, &config.Profiles[0]); err != nil {
		t.Fatal(err)
	}
	// Clear hosted fields inherited from the generic fixture.
	config.Profiles[0].DistributionRoot = ""
	config.Profiles[0].PublicURL = ""
	config.Profiles[0].SigningKeyFile = ""
	config.Profiles[0].Channel = ""
	request.Target = "android-huawei"
	return config, request
}

type huaweiFixtureState struct {
	mu            sync.Mutex
	now           time.Time
	writes        map[string]int
	requests      map[string]int
	failures      map[string]string
	overrides     map[string]any
	rawResponses  map[string]string
	packages      []map[string]any
	appInfo       map[string]any
	uploadHeaders map[string]string
	uploadURL     string
	uploaded      []byte
	fileName      string
	hidePackages  bool
}

func huaweiFixture(t *testing.T) (Config, Request, *Engine, *huaweiFixtureState) {
	t.Helper()
	config, request := huaweiConfig(t)
	config.Profiles[0].UploadHosts = []string{"obs.example"}
	state := &huaweiFixtureState{
		now: time.Now().UTC(), writes: map[string]int{}, requests: map[string]int{}, failures: map[string]string{}, overrides: map[string]any{},
		appInfo:      map[string]any{"publishCountry": "CN", "releaseState": 7},
		rawResponses: map[string]string{},
	}
	state.uploadHeaders = map[string]string{
		"Authorization": "AWS4-HMAC-SHA256 Credential=test-observer, SignedHeaders=host, Signature=test-only-obs-signature",
		"Host":          "obs.example", "Content-Type": "application/octet-stream", "user-agent": "Huawei-OBS-Test",
		"x-amz-content-sha256": request.ArtifactDigest, "x-amz-date": state.now.Format("20060102T150405Z"),
	}
	state.uploadURL = "https://obs.example/obs-object.apk?signature=test-only-signed-url"
	content, err := os.ReadFile(request.ArtifactPath)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		state.mu.Lock()
		defer state.mu.Unlock()
		key := q.Method + " " + q.URL.Path
		state.requests[key]++
		body, _ := io.ReadAll(q.Body)
		w.Header().Set("Content-Type", "application/json")
		respond := func(data any) { _ = json.NewEncoder(w).Encode(data) }
		if q.URL.Path == "/api/oauth2/v1/token" {
			var input map[string]any
			_ = json.Unmarshal(body, &input)
			if q.Method != "POST" || input["client_id"] != "123456789" || input["client_secret"] != "test-only-api-client-secret" || input["grant_type"] != "client_credentials" || len(input) != 3 || q.Header.Get("Authorization") != "" {
				t.Error("incorrect API client token exchange")
			}
			if override, ok := state.overrides[key]; ok {
				respond(override)
				return
			}
			respond(map[string]any{"access_token": "test-only-huawei-control-token", "expires_in": 172800, "token_type": "Bearer"})
			return
		}
		if q.Method != "GET" {
			state.writes[key]++
		}
		if q.URL.Path == "/obs-object.apk" {
			if q.Host != "obs.example" || q.Method != "PUT" || q.Header.Get("Authorization") != state.uploadHeaders["Authorization"] || q.Header.Get("Cookie") != "" || q.Header.Get("client_id") != "" || q.Header.Get("x-amz-content-sha256") != request.ArtifactDigest || q.Header.Get("Content-Type") != "application/octet-stream" || q.ContentLength != request.ArtifactSize || !bytes.Equal(body, content) {
				t.Error("OBS upload leaked control credentials or changed approved bytes/headers")
			}
			state.uploaded = append(state.uploaded, body...)
		} else if q.Host != "connect-api.cloud.huawei.com" || q.Header.Get("client_id") != "123456789" || q.Header.Get("Authorization") != "Bearer test-only-huawei-control-token" || q.Header.Get("Cookie") != "" {
			t.Error("incorrect Huawei control-plane origin/authorization")
		}
		if failure := state.failures[key]; failure != "" {
			if failure == "lost" {
				connection, _, err := w.(http.Hijacker).Hijack()
				if err == nil {
					_ = connection.Close()
				} else {
					t.Error(err)
				}
			} else if failure == "redirect" {
				w.Header().Set("Location", "https://untrusted.example/leaked")
				w.WriteHeader(http.StatusTemporaryRedirect)
			} else {
				w.WriteHeader(http.StatusServiceUnavailable)
			}
			return
		}
		if override, ok := state.overrides[key]; ok {
			respond(override)
			return
		}
		if raw, ok := state.rawResponses[key]; ok {
			_, _ = io.WriteString(w, raw)
			return
		}
		success := map[string]any{"code": 0, "msg": "success"}
		query := q.URL.Query()
		if strings.HasPrefix(q.URL.Path, "/api/publish/") && q.URL.Path != "/api/publish/v2/appid-list" && q.URL.Path != "/api/publish/v2/package-list" {
			if query.Get("appId") != "1234567890" || query.Get("releaseType") != "1" {
				t.Error("destination or release type is not fixed")
			}
		}
		switch key {
		case "GET /api/publish/v2/appid-list":
			if query.Get("packageName") != "com.company.feedback" || query.Get("packageTypes") != "1" {
				t.Error("app ID lookup must use exact APK package identity")
			}
			respond(map[string]any{"ret": success, "appids": []map[string]string{{"key": "Feedback", "value": "1234567890"}}})
		case "GET /api/publish/v2/app-info":
			respond(map[string]any{"ret": success, "appInfo": state.appInfo})
		case "GET /api/publish/v2/package-list":
			start, _ := strconv.Atoi(query.Get("fromRecCount"))
			if query.Get("appId") != "1234567890" || query.Get("maxReqCount") != "100" || start < 1 {
				t.Error("invalid bounded package query")
				w.WriteHeader(400)
				return
			}
			packages := state.packages
			if state.hidePackages {
				packages = nil
			}
			end := start - 1 + 100
			if end > len(packages) {
				end = len(packages)
			}
			if start-1 > end {
				w.WriteHeader(400)
				return
			}
			page := packages[start-1 : end]
			respond(map[string]any{"ret": success, "retCount": len(page), "totalCount": len(packages), "pkgList": page})
		case "GET /api/publish/v2/upload-url/for-obs":
			if query.Get("contentLength") != strconv.FormatInt(request.ArtifactSize, 10) || query.Get("sha256") != request.ArtifactDigest || query.Get("chineseMainlandFlag") != "1" || !strings.HasSuffix(query.Get("fileName"), ".apk") || query.Has("suffix") {
				t.Error("upload reservation not bound to approved SHA256/size and mainland policy")
			}
			state.fileName = query.Get("fileName")
			respond(map[string]any{"ret": success, "urlInfo": map[string]any{"objectId": "obs-object.apk", "url": state.uploadURL, "method": "PUT", "headers": state.uploadHeaders}})
		case "PUT /obs-object.apk":
			w.WriteHeader(http.StatusOK)
		case "PUT /api/publish/v2/app-file-info":
			var input struct {
				FileType int `json:"fileType"`
				Files    []struct {
					FileName    string `json:"fileName"`
					FileDestURL string `json:"fileDestUrl"`
				} `json:"files"`
			}
			if json.Unmarshal(body, &input) != nil || input.FileType != 5 || len(input.Files) != 1 || input.Files[0].FileName != state.fileName || input.Files[0].FileDestURL != "obs-object.apk" || !bytes.Equal(state.uploaded, content) {
				t.Error("file registration is not exactly the uploaded object")
			}
			state.packages = append(state.packages, map[string]any{"pkgVersion": "pkg-42", "fileName": state.fileName, "versionCode": 42, "versionName": "1.0.0", "packageSize": request.ArtifactSize, "shaCode": request.ArtifactDigest, "packageType": 1, "uploadTime": state.now.Unix()})
			state.appInfo["versionId"], state.appInfo["versionCode"], state.appInfo["versionNumber"] = "business-version-42", 42, "1.0.0"
			respond(map[string]any{"ret": success, "pkgVersion": []string{"pkg-42"}})
		case "POST /api/publish/v2/app-submit":
			if len(bytes.TrimSpace(body)) != 0 && string(bytes.TrimSpace(body)) != "{}" {
				t.Error("submit must not invent compliance/release statements")
			}
			state.appInfo["releaseState"] = 4
			respond(map[string]any{"ret": success})
		default:
			t.Errorf("unexpected channel request: %s", key)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	local, _ := url.Parse(server.URL)
	transport := server.Client().Transport.(*http.Transport).Clone()
	// Keep real certificate verification while routing the fixed public API
	// hostname to httptest's certificate and loopback listener.
	transport.TLSClientConfig.ServerName = server.Certificate().DNSNames[0]
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != "connect-api.cloud.huawei.com:443" && address != "obs.example:443" {
			t.Errorf("unauthorized outbound destination: %s", address)
			return nil, io.ErrUnexpectedEOF
		}
		return (&net.Dialer{}).DialContext(ctx, network, local.Host)
	}
	t.Cleanup(transport.CloseIdleConnections)
	engine := openHuaweiTestEngine(t, config, request, state, transport)
	return config, request, engine, state
}

func openHuaweiTestEngine(t *testing.T, config Config, request Request, state *huaweiFixtureState, transport http.RoundTripper) *Engine {
	t.Helper()
	engine, err := Open(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Close() })
	engine.HTTP.Transport = transport
	engine.huaweiNow = func() time.Time { state.mu.Lock(); defer state.mu.Unlock(); return state.now }
	engine.verifyAndroidPackage = func(_ context.Context, _ Profile, _ Request, staged string) (androidPackage, error) {
		body, err := os.ReadFile(staged)
		original, _ := os.ReadFile(request.ArtifactPath)
		if err != nil || staged == request.ArtifactPath || !bytes.Equal(body, original) {
			t.Error("APK SDK did not inspect the exact private staged copy")
		}
		return androidPackage{Version: "1.0.0", VersionCode: 42}, nil
	}
	return engine
}

func TestHuaweiUploadsExactArtifactAndReopensWithoutResubmission(t *testing.T) {
	config, request, engine, state := huaweiFixture(t)
	result, err := engine.Execute(context.Background(), request)
	if err != nil || result.State != "processing" || result.ExternalID != "huawei:1234567890:pkg-42" || result.ContinueRequired {
		t.Fatalf("upload should persist processing without review: %+v, %v", result, err)
	}
	op, err := engine.Inspect(context.Background(), request.OperationKey)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(op)
	for _, secret := range []string{"test-only-api-client-secret", "test-only-huawei-control-token", "test-only-obs-signature", "test-only-signed-url"} {
		if bytes.Contains(body, []byte(secret)) {
			t.Fatal("private upstream credentials entered journal")
		}
	}
	state.mu.Lock()
	if state.writes["PUT /obs-object.apk"] != 1 || state.writes["PUT /api/publish/v2/app-file-info"] != 1 || state.writes["POST /api/publish/v2/app-submit"] != 0 {
		t.Error("upload was not one exact upload without submission")
	}
	state.mu.Unlock()
	engine.Close()
	reopened := openHuaweiTestEngine(t, config, request, state, engine.HTTP.Transport)
	request.ArtifactPath = "" // Replaying a confirmed result does not need another APK.
	again, err := reopened.Execute(context.Background(), request)
	if err != nil || again != result {
		t.Fatalf("reopen lost the accepted result: %+v %v", again, err)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.writes["PUT /obs-object.apk"] != 1 || state.writes["PUT /api/publish/v2/app-file-info"] != 1 {
		t.Fatal("confirmed upload was replayed")
	}
}

func huaweiUploadForTest(t *testing.T, engine *Engine, request Request) Request {
	t.Helper()
	result, err := engine.Execute(context.Background(), request)
	if err != nil || result.State != "processing" {
		t.Fatalf("upload failed: %+v %v", result, err)
	}
	request.Action, request.ExternalID, request.ArtifactPath = "status", result.ExternalID, ""
	return request
}

func TestHuaweiPersistsWaitAndAssociatesPublishedBusinessVersion(t *testing.T) {
	config, request, engine, state := huaweiFixture(t)
	query := huaweiUploadForTest(t, engine, request)
	engine.Close()
	engine = openHuaweiTestEngine(t, config, request, state, engine.HTTP.Transport)
	result, err := engine.Execute(context.Background(), query)
	if err != nil || result.State != "processing" || result.ContinueRequired {
		t.Fatalf("lost persisted two-minute wait: %+v %v", result, err)
	}
	state.mu.Lock()
	state.now = state.now.Add(121 * time.Second)
	state.hidePackages = true
	state.mu.Unlock()
	result, err = engine.Execute(context.Background(), query)
	if err != nil || result.State != "processing" || result.ContinueRequired {
		t.Fatalf("missing parsed package allowed review: %+v %v", result, err)
	}
	state.mu.Lock()
	state.hidePackages = false
	state.mu.Unlock()
	result, err = engine.Execute(context.Background(), query)
	if err != nil || result.State != "processing" || !result.ContinueRequired {
		t.Fatalf("exact parsed package did not become continuable: %+v %v", result, err)
	}
	state.mu.Lock()
	if state.writes["POST /api/publish/v2/app-submit"] != 0 {
		t.Error("status query submitted for review")
	}
	state.mu.Unlock()
	query.Action = "continue"
	result, err = engine.Execute(context.Background(), query)
	if err != nil || result.State != "awaiting_review" {
		t.Fatalf("exact original operation could not submit: %+v %v", result, err)
	}
	op, err := engine.Inspect(context.Background(), request.OperationKey)
	if err != nil {
		t.Fatal(err)
	}
	if op.Receipt.Huawei.PackageID != "pkg-42" || op.Receipt.Huawei.VersionID != "business-version-42" || op.Receipt.Huawei.Stage != "submitted" {
		t.Fatalf("package ID and business version ID were conflated: %+v", op.Receipt)
	}
	query.Action = "status"
	for _, tc := range []struct {
		name               string
		releaseState       int
		shelfID, shelfName string
		shelfCode          int
		want               string
	}{
		{"review", 4, "business-version-old", "0.9.0", 41, "awaiting_review"},
		{"old released version", 0, "business-version-old", "0.9.0", 41, "unchanged"},
		{"different business ID", 0, "other-version", "1.0.0", 42, "unchanged"},
		{"different shelf version", 0, "business-version-42", "2.0.0", 42, "unchanged"},
		{"current rejected", 8, "business-version-old", "0.9.0", 41, "rejected"},
		{"exact released version", 0, "business-version-42", "1.0.0", 42, "published"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state.mu.Lock()
			state.appInfo["releaseState"], state.appInfo["onShelfVersionId"], state.appInfo["onShelfVersionNumber"], state.appInfo["onShelfVersionCode"] = tc.releaseState, tc.shelfID, tc.shelfName, tc.shelfCode
			state.mu.Unlock()
			result, err := engine.Execute(context.Background(), query)
			if err != nil || result.State != tc.want || result.ExternalID != "huawei:1234567890:pkg-42" {
				t.Fatalf("status confused release identities: %+v %v", result, err)
			}
		})
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.writes["POST /api/publish/v2/app-submit"] != 1 {
		t.Fatal("queries replayed the review submission")
	}
}

func TestHuaweiConcurrentContinuationSubmitsOnlyOnceAcrossEngines(t *testing.T) {
	config, request, engine, state := huaweiFixture(t)
	query := huaweiUploadForTest(t, engine, request)
	state.mu.Lock()
	state.now = state.now.Add(121 * time.Second)
	state.mu.Unlock()
	query.Action = "continue"
	other := openHuaweiTestEngine(t, config, request, state, engine.HTTP.Transport)
	var wait sync.WaitGroup
	results := make(chan error, 2)
	for _, executor := range []*Engine{engine, other} {
		wait.Add(1)
		go func(e *Engine) {
			defer wait.Done()
			result, err := e.Execute(context.Background(), query)
			if err == nil && result.State != "awaiting_review" {
				err = fmt.Errorf("unexpected continuation state: %s", result.State)
			}
			results <- err
		}(executor)
	}
	wait.Wait()
	close(results)
	succeeded := 0
	for err := range results {
		if err == nil {
			succeeded++
		}
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if succeeded == 0 || state.writes["POST /api/publish/v2/app-submit"] != 1 {
		t.Fatalf("lost single-submission reservation: successes=%d writes=%v", succeeded, state.writes)
	}
}

func TestHuaweiUncertainWritesStayReservedAfterReopen(t *testing.T) {
	for _, key := range []string{"GET /api/publish/v2/upload-url/for-obs", "PUT /obs-object.apk", "PUT /api/publish/v2/app-file-info", "POST /api/publish/v2/app-submit"} {
		t.Run(key, func(t *testing.T) {
			config, request, engine, state := huaweiFixture(t)
			if strings.HasPrefix(key, "POST") {
				request = huaweiUploadForTest(t, engine, request)
				request.Action = "continue"
				state.mu.Lock()
				state.now = state.now.Add(121 * time.Second)
				state.mu.Unlock()
			}
			state.mu.Lock()
			state.failures[key] = "lost"
			state.mu.Unlock()
			if _, err := engine.Execute(context.Background(), request); err == nil {
				t.Fatal("lost external receipt treated as success")
			}
			engine.Close()
			reopened := openHuaweiTestEngine(t, config, request, state, engine.HTTP.Transport)
			if _, err := reopened.Execute(context.Background(), request); err == nil || !strings.Contains(err.Error(), "uncertain") {
				t.Fatalf("interrupted write became replayable: %v", err)
			}
			state.mu.Lock()
			defer state.mu.Unlock()
			if state.requests[key] != 1 {
				t.Fatalf("uncertain external operation repeated: %v", state.requests)
			}
		})
	}
}

func TestHuaweiRequiresPositiveResponseProofAndRejectsRedirects(t *testing.T) {
	for _, tc := range []struct {
		name, path       string
		response         any
		redirect, wantOK bool
	}{
		{"missing result", "GET /api/publish/v2/appid-list", map[string]any{"appids": []map[string]string{{"value": "1234567890"}}}, false, false},
		{"upstream error", "GET /api/publish/v2/appid-list", map[string]any{"ret": map[string]any{"code": 12345, "msg": "private-upstream-text-must-not-escape"}}, false, false},
		{"string result", "GET /api/publish/v2/appid-list", map[string]any{"ret": `{"code":0,"msg":"success"}`, "appids": []map[string]string{{"value": "1234567890"}}}, false, true},
		{"different application", "GET /api/publish/v2/appid-list", map[string]any{"ret": map[string]any{"code": 0}, "appids": []map[string]string{{"value": "999999"}}}, false, false},
		{"unknown release state", "GET /api/publish/v2/app-info", map[string]any{"ret": map[string]any{"code": 0}, "appInfo": map[string]any{"publishCountry": "CN"}}, false, false},
		{"missing pagination", "GET /api/publish/v2/package-list", map[string]any{"ret": map[string]any{"code": 0}, "pkgList": []any{}}, false, false},
		{"too many packages", "GET /api/publish/v2/package-list", map[string]any{"ret": map[string]any{"code": 0}, "retCount": 0, "totalCount": 1001, "pkgList": []any{}}, false, false},
		{"no token", "POST /api/oauth2/v1/token", map[string]any{"expires_in": 172800}, false, false},
		{"expired token", "POST /api/oauth2/v1/token", map[string]any{"access_token": "test-only-huawei-control-token", "expires_in": 0}, false, false},
		{"API redirect", "GET /api/publish/v2/appid-list", nil, true, false},
		{"OBS redirect", "PUT /obs-object.apk", nil, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, request, engine, state := huaweiFixture(t)
			state.mu.Lock()
			if tc.redirect {
				state.failures[tc.path] = "redirect"
			} else {
				state.overrides[tc.path] = tc.response
			}
			state.mu.Unlock()
			_, err := engine.Execute(context.Background(), request)
			if (err == nil) != tc.wantOK {
				t.Fatalf("incorrect acceptance of channel proof: %v", err)
			}
			if err != nil && strings.Contains(err.Error(), "private-upstream-text-must-not-escape") {
				t.Fatal("upstream body leaked in returned error")
			}
			state.mu.Lock()
			defer state.mu.Unlock()
			if !tc.wantOK && state.writes["PUT /api/publish/v2/app-file-info"] != 0 {
				t.Fatal("invalid upstream proof reached registration")
			}
		})
	}
}

func TestHuaweiPrivateCredentialsAndArtifactMustValidateBeforeNetwork(t *testing.T) {
	for _, kind := range []string{"public credentials", "unsupported account", "changed artifact", "SDK failure"} {
		t.Run(kind, func(t *testing.T) {
			config, request, engine, state := huaweiFixture(t)
			switch kind {
			case "public credentials":
				if err := os.Chmod(config.Profiles[0].CredentialsFile, 0644); err != nil {
					t.Fatal(err)
				}
			case "unsupported account":
				if err := os.WriteFile(config.Profiles[0].CredentialsFile, []byte(`{"key_id":"unsupported-service-account","private_key":"never-read"}`), 0600); err != nil {
					t.Fatal(err)
				}
			case "changed artifact":
				request.ArtifactDigest = strings.Repeat("f", 64)
			case "SDK failure":
				engine.verifyAndroidPackage = func(context.Context, Profile, Request, string) (androidPackage, error) {
					return androidPackage{}, fmt.Errorf("APK signature rejected")
				}
			}
			if _, err := engine.Execute(context.Background(), request); err == nil {
				t.Fatal("unapproved APK or credentials reached publisher")
			}
			state.mu.Lock()
			defer state.mu.Unlock()
			if len(state.requests) != 0 {
				t.Fatal("local validation failure contacted external API")
			}
		})
	}
}

func TestHuaweiOversizedResponseCannotHideTrailingJSON(t *testing.T) {
	_, request, engine, state := huaweiFixture(t)
	state.mu.Lock()
	state.rawResponses["GET /api/publish/v2/appid-list"] = `{"ret":{"code":0},"appids":[{"value":"1234567890"}]}` + strings.Repeat(" ", 2<<20) + `{"ret":{"code":99}}`
	state.mu.Unlock()
	if _, err := engine.Execute(context.Background(), request); err == nil {
		t.Fatal("oversized response hid trailing JSON and authorized upload")
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.writes) != 0 {
		t.Fatal("oversized identity response reached external writes")
	}
}

func TestHuaweiAcceptsCanonicalHexDigestIndependentOfLetterCase(t *testing.T) {
	_, request, engine, state := huaweiFixture(t)
	query := huaweiUploadForTest(t, engine, request)
	state.mu.Lock()
	state.now = state.now.Add(121 * time.Second)
	state.packages[0]["shaCode"] = strings.ToUpper(request.ArtifactDigest)
	state.mu.Unlock()
	result, err := engine.Execute(context.Background(), query)
	if err != nil || !result.ContinueRequired {
		t.Fatalf("equal SHA256 bytes were rejected for hex letter case: %+v %v", result, err)
	}
}

func TestHuaweiRejectsUntrustedUploadsBeforeAnyBlobRequest(t *testing.T) {
	for _, tc := range []struct{ name, field, value string }{
		{"foreign URL", "url", "https://untrusted.example/blob"},
		{"HTTP URL", "url", "http://obs.example/blob"},
		{"userinfo URL", "url", "https://user@obs.example/blob"},
		{"wrong Host", "Host", "untrusted.example"},
		{"control Bearer", "Authorization", "Bearer test-only-huawei-control-token"},
		{"embedded control token", "Authorization", "OBS test-only-huawei-control-token"},
		{"cookie header", "Cookie", "session=secret"},
		{"control header", "client_id", "123456789"},
		{"token in user agent", "user-agent", "test-only-huawei-control-token"},
		{"case duplicate", "host", "obs.example"},
		{"wrong digest", "x-amz-content-sha256", strings.Repeat("d", 64)},
		{"expired", "x-amz-date", "20200101T000000Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, request, engine, state := huaweiFixture(t)
			state.mu.Lock()
			if tc.field == "url" {
				state.uploadURL = tc.value
			} else {
				state.uploadHeaders[tc.field] = tc.value
			}
			state.mu.Unlock()
			if _, err := engine.Execute(context.Background(), request); err == nil {
				t.Fatal("untrusted upload reservation accepted")
			}
			state.mu.Lock()
			defer state.mu.Unlock()
			if state.writes["PUT /obs-object.apk"] != 0 || state.writes["PUT /api/publish/v2/app-file-info"] != 0 {
				t.Fatal("invalid reservation reached blob or registration API")
			}
		})
	}
}

func TestHuaweiParsedPackageDriftCannotContinue(t *testing.T) {
	for _, tc := range []struct {
		field    string
		value    any
		wantWait bool
	}{
		{"shaCode", strings.Repeat("d", 64), false}, {"shaCode", "", true},
		{"packageSize", int64(1), false}, {"versionName", "2.0.0", false},
		{"pkgVersion", "other-package", false}, {"fileName", "unrelated.apk", false},
	} {
		t.Run(fmt.Sprint(tc.field, tc.value), func(t *testing.T) {
			_, request, engine, state := huaweiFixture(t)
			query := huaweiUploadForTest(t, engine, request)
			state.mu.Lock()
			state.now = state.now.Add(121 * time.Second)
			state.packages[0][tc.field] = tc.value
			state.mu.Unlock()
			query.Action = "continue"
			result, err := engine.Execute(context.Background(), query)
			if tc.wantWait {
				if err != nil || result.State != "processing" || result.ContinueRequired {
					t.Fatalf("incomplete digest should remain processing: %+v %v", result, err)
				}
			} else if err == nil {
				t.Fatalf("changed approved package allowed continuation: %+v", result)
			}
			state.mu.Lock()
			defer state.mu.Unlock()
			if state.writes["POST /api/publish/v2/app-submit"] != 0 {
				t.Fatal("package drift reached review submission")
			}
		})
	}
}

func TestHuaweiAppLockAndBindingPreventAnotherRelease(t *testing.T) {
	_, request, engine, state := huaweiFixture(t)
	query := huaweiUploadForTest(t, engine, request)
	wrong := query
	wrong.ExternalID = "huawei:1234567890:other-package"
	if _, err := engine.Execute(context.Background(), wrong); err == nil {
		t.Fatal("wrong external package accepted")
	}
	wrong = query
	wrong.Binding = strings.Repeat("f", 64)
	if _, err := engine.Execute(context.Background(), wrong); err == nil {
		t.Fatal("different approved binding accepted")
	}
	request.OperationKey = strings.Repeat("d", 64)
	engine.verifyAndroidPackage = func(context.Context, Profile, Request, string) (androidPackage, error) {
		return androidPackage{Version: "1.0.0", VersionCode: 43}, nil
	}
	if _, err := engine.Execute(context.Background(), request); err == nil {
		t.Fatal("another release overwrote the active application")
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.writes["PUT /obs-object.apk"] != 1 {
		t.Fatal("active destination lock did not protect upload")
	}
}

func TestHuaweiPackagePaginationFindsExactPackageBeyondFirstPage(t *testing.T) {
	_, request, engine, state := huaweiFixture(t)
	state.mu.Lock()
	for i := 0; i < 101; i++ {
		state.packages = append(state.packages, map[string]any{"pkgVersion": fmt.Sprintf("old-%d", i), "versionCode": 1000 + i, "packageSize": int64(1), "packageType": 1})
	}
	state.mu.Unlock()
	query := huaweiUploadForTest(t, engine, request)
	state.mu.Lock()
	state.now = state.now.Add(121 * time.Second)
	state.mu.Unlock()
	result, err := engine.Execute(context.Background(), query)
	if err != nil || !result.ContinueRequired {
		t.Fatalf("exact package beyond first page was missed: %+v %v", result, err)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.requests["GET /api/publish/v2/package-list"] != 4 {
		t.Fatal("package lookup did not traverse both bounded pages")
	}
}

func TestHuaweiAcceptsFixedAdministratorDestination(t *testing.T) {
	config, _ := huaweiConfig(t)
	engine, err := Open(config)
	if err != nil {
		t.Fatalf("valid fixed Huawei destination rejected: %v", err)
	}
	engine.Close()
}

func TestHuaweiRejectsIncompleteOrAmbiguousDestinations(t *testing.T) {
	for _, tc := range []struct {
		name, field string
		value       any
	}{
		{"wrong target", "target", "android-google-play"},
		{"nonnumeric app", "appId", "com.company.feedback"},
		{"missing package", "bundleId", ""},
		{"untrusted signer", "expectedSigner", "Android Debug"},
		{"relative credentials", "credentialsFile", "secrets.json"},
		{"relative SDK", "aaptPath", "aapt"},
		{"missing verifier", "apkSignerPath", ""},
		{"wildcard host", "uploadHosts", []string{"*.example"}},
		{"ambiguous mainland", "chineseMainland", nil},
		{"duplicate host", "uploadHosts", []string{"obs.example", "obs.example"}},
		{"irrelevant destination selector", "track", "another-track"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config, _ := huaweiConfig(t)
			body, _ := json.Marshal(config.Profiles[0])
			var raw map[string]any
			_ = json.Unmarshal(body, &raw)
			raw[tc.field] = tc.value
			body, _ = json.Marshal(raw)
			config.Profiles[0] = Profile{}
			_ = json.Unmarshal(body, &config.Profiles[0])
			if engine, err := Open(config); err == nil {
				engine.Close()
				t.Fatal("unsafe Huawei destination accepted")
			}
		})
	}
}

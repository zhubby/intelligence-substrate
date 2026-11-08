package publisher

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"image"
	"image/png"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/cookiejar"
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

type xiaomiFixtureState struct {
	mu                    sync.Mutex
	queries, pushes       int
	latest                map[string]any
	create, update        bool
	failure, queryFailure string
	engine                *Engine
	private               *rsa.PrivateKey
	metadataPath          string
	metadata              map[string]any
	files                 map[string][]byte
	transport             http.RoundTripper
	queryHook             func(int)
	pushHook              func()
}

func xiaomiFixture(t *testing.T) (Config, Request, *Engine, *xiaomiFixtureState) {
	t.Helper()
	config, request := publisherFixture(t)
	approvedPayload, err := os.ReadFile(request.ArtifactPath)
	if err != nil {
		t.Fatal(err)
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	certificate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test-only Xiaomi encryption certificate"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageKeyEncipherment}
	der, err := x509.CreateCertificate(rand.Reader, certificate, certificate, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	credentials := filepath.Join(dir, "credentials.json")
	encoded, _ := json.Marshal(map[string]string{"userName": "test-owner@example.test", "accessPassword": "test-only-xiaomi-api-access-password", "publicCertificatePEM": string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))})
	if err = os.WriteFile(credentials, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	var picture bytes.Buffer
	if err = png.Encode(&picture, image.NewRGBA(image.Rect(0, 0, 32, 32))); err != nil {
		t.Fatal(err)
	}
	asset := func(name string) map[string]any {
		path := filepath.Join(dir, name+".png")
		if err := os.WriteFile(path, picture.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(picture.Bytes())
		return map[string]any{"path": path, "sha256": hex.EncodeToString(digest[:]), "bytes": picture.Len()}
	}
	state := &xiaomiFixtureState{private: key, create: true, metadataPath: filepath.Join(dir, "metadata.json"), files: map[string][]byte{}}
	state.metadata = map[string]any{"synchroType": 0, "appInfo": map[string]any{"appName": "测试反馈", "category": 10, "keyWords": "反馈 企业", "desc": "企业反馈应用", "brief": "收集客户反馈", "privacyUrl": "https://company.example/privacy"}, "icon": asset("icon"), "screenshots": []any{asset("screen1"), asset("screen2"), asset("screen3")}}
	body, _ := json.Marshal(state.metadata)
	if err = os.WriteFile(state.metadataPath, body, 0600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(body)
	profile, _ := json.Marshal(map[string]any{"projectId": request.ProjectID, "goalId": request.GoalID, "target": "android-xiaomi", "driver": "xiaomi-market", "appId": "com.company.feedback", "bundleId": "com.company.feedback", "expectedSigner": strings.Repeat("c", 64), "credentialsFile": credentials, "aaptPath": "/opt/android/build-tools/aapt", "apkSignerPath": "/opt/android/build-tools/apksigner", "metadataFile": state.metadataPath, "metadataDigest": hex.EncodeToString(digest[:])})
	config.Profiles = []Profile{{}}
	if err = json.Unmarshal(profile, &config.Profiles[0]); err != nil {
		t.Fatal(err)
	}
	request.Target = "android-xiaomi"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		state.mu.Lock()
		defer state.mu.Unlock()
		if q.Method != "POST" || q.Host != "api.developer.xiaomi.com" || q.TLS == nil || q.Header.Get("Cookie") != "" || q.Header.Get("Authorization") != "" || q.URL.RawQuery != "" {
			t.Error("Xiaomi used an unapproved request origin/method or leaked credentials")
		}
		if q.ContentLength <= 0 {
			t.Error("Xiaomi multipart requires an exact content length")
		}
		reader, err := q.MultipartReader()
		if err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		parts := map[string][]byte{}
		for {
			part, readErr := reader.NextPart()
			if readErr == io.EOF {
				break
			}
			if readErr != nil {
				t.Error(readErr)
				return
			}
			if _, ok := parts[part.FormName()]; ok {
				t.Error("duplicate multipart field")
			}
			parts[part.FormName()], err = io.ReadAll(part)
			if err != nil {
				t.Error(err)
				return
			}
		}
		cipher, err := hex.DecodeString(string(parts["SIG"]))
		if err != nil || len(cipher)%key.Size() != 0 || string(parts["SIG"]) != strings.ToLower(string(parts["SIG"])) {
			t.Error("invalid encrypted signature")
			w.WriteHeader(400)
			return
		}
		var plain []byte
		for start := 0; start < len(cipher); start += key.Size() {
			block, err := rsa.DecryptPKCS1v15(rand.Reader, key, cipher[start:start+key.Size()])
			if err != nil {
				t.Error(err)
				return
			}
			plain = append(plain, block...)
		}
		var signature struct {
			Password string                        `json:"password"`
			Sig      []struct{ Name, Hash string } `json:"sig"`
		}
		if json.Unmarshal(plain, &signature) != nil || signature.Password != "test-only-xiaomi-api-access-password" || len(signature.Sig) != len(parts)-1 {
			t.Error("signature does not bind every multipart field")
		}
		seen := map[string]bool{}
		for _, item := range signature.Sig {
			hash := md5.Sum(parts[item.Name])
			if seen[item.Name] || item.Name == "SIG" || item.Hash != hex.EncodeToString(hash[:]) {
				t.Error("incorrect independent field MD5 binding")
			}
			seen[item.Name] = true
		}
		var data struct {
			UserName    string         `json:"userName"`
			PackageName string         `json:"packageName"`
			SynchroType int            `json:"synchroType"`
			AppInfo     map[string]any `json:"appInfo"`
		}
		if json.Unmarshal(parts["RequestData"], &data) != nil || data.UserName != "test-owner@example.test" {
			t.Error("incorrect account or RequestData representation")
		}
		w.Header().Set("Content-Type", "application/json")
		switch q.URL.Path {
		case "/devupload/dev/query":
			state.queries++
			if state.queryHook != nil {
				state.queryHook(state.queries)
			}
			if data.PackageName != "com.company.feedback" || len(parts) != 2 {
				t.Error("query destination or attachments differ")
			}
			if state.queryFailure != "" {
				_, _ = io.WriteString(w, state.queryFailure)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"result": 0, "create": state.create, "updateVersion": state.update, "updateInfo": false, "packageInfo": state.latest})
		case "/devupload/dev/push":
			state.pushes++
			if data.AppInfo["packageName"] != "com.company.feedback" || data.AppInfo["versionName"] != "1.0.0" {
				t.Error("push changed the verified APK identity")
			}
			op, err := state.engine.Inspect(q.Context(), request.OperationKey)
			if err != nil || op.State != "pending" {
				t.Error("external push preceded durable intent")
			}
			if !bytes.Equal(parts["apk"], approvedPayload) || !bytes.Equal(parts["icon"], picture.Bytes()) {
				t.Error("push changed approved artifact/material bytes")
			}
			if data.SynchroType == 0 {
				for _, name := range []string{"screenshot_1", "screenshot_2", "screenshot_3"} {
					if !bytes.Equal(parts[name], picture.Bytes()) {
						t.Error("initial push omitted approved screenshots")
					}
				}
			}
			state.files = parts
			if state.pushHook != nil {
				state.pushHook()
			}
			if state.failure == "lost" {
				connection, _, err := w.(http.Hijacker).Hijack()
				if err == nil {
					_ = connection.Close()
				}
				return
			}
			if state.failure == "redirect" {
				w.Header().Set("Location", "https://untrusted.example/stolen")
				w.WriteHeader(307)
				return
			}
			if state.failure != "" {
				_, _ = io.WriteString(w, state.failure)
				return
			}
			state.latest = map[string]any{"appName": "测试反馈", "packageName": "com.company.feedback", "versionCode": 42, "versionName": "1.0.0"}
			state.create = false
			state.update = false
			_, _ = io.WriteString(w, `{"result":0,"message":"提交新应用成功"}`)
		default:
			t.Error("unsupported Xiaomi API path")
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(server.Close)
	local, _ := url.Parse(server.URL)
	transport := server.Client().Transport.(*http.Transport).Clone()
	transport.TLSClientConfig.ServerName = server.Certificate().DNSNames[0]
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != "api.developer.xiaomi.com:443" {
			t.Error("unexpected outbound host")
			return nil, io.ErrUnexpectedEOF
		}
		return (&net.Dialer{}).DialContext(ctx, network, local.Host)
	}
	t.Cleanup(transport.CloseIdleConnections)
	state.transport = transport
	engine := openXiaomiTestEngine(t, config, request, state)
	state.engine = engine
	return config, request, engine, state
}

func openXiaomiTestEngine(t *testing.T, config Config, r Request, state *xiaomiFixtureState) *Engine {
	t.Helper()
	e, err := Open(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	e.HTTP.Transport = state.transport
	e.verifyAndroidPackage = func(_ context.Context, p Profile, binding Request, path string) (androidPackage, error) {
		body, err := os.ReadFile(path)
		if err != nil {
			return androidPackage{}, err
		}
		expected, _ := os.ReadFile(r.ArtifactPath)
		if !bytes.Equal(body, expected) || p.BundleID != "com.company.feedback" || binding.ArtifactDigest != r.ArtifactDigest {
			t.Fatal("SDK did not receive exact staged approved bytes")
		}
		return androidPackage{Version: "1.0.0", VersionCode: 42}, nil
	}
	return e
}

func rewriteXiaomiMetadata(t *testing.T, config *Config, state *xiaomiFixtureState) {
	t.Helper()
	body, _ := json.Marshal(state.metadata)
	if err := os.WriteFile(state.metadataPath, body, 0600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(body)
	p, _ := json.Marshal(config.Profiles[0])
	var obj map[string]any
	_ = json.Unmarshal(p, &obj)
	obj["metadataDigest"] = hex.EncodeToString(digest[:])
	p, _ = json.Marshal(obj)
	_ = json.Unmarshal(p, &config.Profiles[0])
}

func TestXiaomiNewApplicationBindsAllBytesAndNeverInfersPublished(t *testing.T) {
	config, r, e, state := xiaomiFixture(t)
	result, err := e.Execute(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if result.State != "awaiting_review" || result.ExternalID == "" || result.ContinueRequired {
		t.Fatal("push acceptance must remain awaiting review")
	}
	queries := state.queries
	if _, err = e.Execute(context.Background(), r); err != nil || state.pushes != 1 || state.queries != queries {
		t.Fatal("confirmed push was replayed or queried")
	}
	for _, flags := range []bool{false, true} {
		state.update = flags
		r.Action = "status"
		r.ExternalID = result.ExternalID
		observed, err := e.Execute(context.Background(), r)
		if err != nil {
			t.Fatal(err)
		}
		if observed.State != "unchanged" || observed.ContinueRequired || !strings.Contains(observed.Reason, "人工") {
			t.Fatal("latest package or update permission was treated as store publication")
		}
	}
	if state.pushes != 1 {
		t.Fatal("status caused an external mutation")
	}
	reopened := openXiaomiTestEngine(t, config, r, state)
	r.Action = "publish"
	if _, err = reopened.Execute(context.Background(), r); err != nil || state.pushes != 1 {
		t.Fatal("reopen duplicated push")
	}
	op, err := e.Inspect(context.Background(), r.OperationKey)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(op)
	if bytes.Contains(body, []byte("test-owner@")) || bytes.Contains(body, []byte("test-only-xiaomi-api-access-password")) || bytes.Contains(body, []byte("\"SIG\"")) {
		t.Fatal("journal leaked channel credentials or request signature")
	}
}

func TestXiaomiMaterialFailurePreventsAnyChannelRequest(t *testing.T) {
	for _, scenario := range []string{"changed metadata", "changed image", "missing screenshots", "unsafe privacy", "unknown selector"} {
		t.Run(scenario, func(t *testing.T) {
			config, r, e, state := xiaomiFixture(t)
			switch scenario {
			case "changed metadata":
				_ = os.WriteFile(state.metadataPath, []byte(`{}`), 0600)
			case "changed image":
				_ = os.WriteFile(state.metadata["icon"].(map[string]any)["path"].(string), []byte("different image"), 0600)
			case "missing screenshots":
				state.metadata["screenshots"] = []any{}
				rewriteXiaomiMetadata(t, &config, state)
				e.Config = config
			case "unsafe privacy":
				state.metadata["appInfo"].(map[string]any)["privacyUrl"] = "http://public.example/privacy"
				rewriteXiaomiMetadata(t, &config, state)
				e.Config = config
			case "unknown selector":
				state.metadata["apiURL"] = "https://untrusted.example"
				rewriteXiaomiMetadata(t, &config, state)
				e.Config = config
			}
			if _, err := e.Execute(context.Background(), r); err == nil {
				t.Fatal("invalid material accepted")
			}
			if state.queries != 0 || state.pushes != 0 {
				t.Fatal("invalid material reached the channel")
			}
			if _, err := e.Inspect(context.Background(), r.OperationKey); err == nil {
				t.Fatal("invalid material reserved a publication")
			}
		})
	}
}

func TestXiaomiUnknownPushRemainsReservedAndNeverReplays(t *testing.T) {
	for _, failure := range []string{"lost", "redirect", `{"result":-20014,"message":"test-only-xiaomi-api-access-password"}`, `{}`, `{"result":0} {"result":0}`} {
		t.Run(failure, func(t *testing.T) {
			config, r, e, state := xiaomiFixture(t)
			state.failure = failure
			_, err := e.Execute(context.Background(), r)
			if err == nil || strings.Contains(err.Error(), "test-only-xiaomi-api-access-password") {
				t.Fatal("unknown push succeeded or leaked upstream content")
			}
			op, err := e.Inspect(context.Background(), r.OperationKey)
			if err != nil || op.State != "pending" {
				t.Fatal("unknown push lost its durable intent")
			}
			reopened := openXiaomiTestEngine(t, config, r, state)
			for _, action := range []string{"publish", "status", "continue"} {
				r.Action = action
				r.ExternalID = "xiaomi:com.company.feedback:42"
				if _, err = reopened.Execute(context.Background(), r); err == nil {
					t.Fatal("uncertain operation allowed retry/status/continuation")
				}
			}
			r.Action = "publish"
			r.OperationKey = strings.Repeat("e", 64)
			if _, err = reopened.Execute(context.Background(), r); err == nil {
				t.Fatal("new key bypassed uncertain package reservation")
			}
			if state.pushes != 1 {
				t.Fatal("external push was duplicated")
			}
		})
	}
}

func TestXiaomiUpdateRequiresNewVersionAndUpstreamPermission(t *testing.T) {
	for _, scenario := range []string{"allowed", "permission denied", "same version", "other package"} {
		t.Run(scenario, func(t *testing.T) {
			config, r, e, state := xiaomiFixture(t)
			state.metadata["synchroType"] = 1
			state.metadata["screenshots"] = []any{}
			state.metadata["appInfo"].(map[string]any)["updateDesc"] = "修复反馈流程"
			rewriteXiaomiMetadata(t, &config, state)
			e.Config = config
			state.create = false
			state.update = true
			state.latest = map[string]any{"packageName": "com.company.feedback", "versionCode": 41, "versionName": "0.9.0"}
			if scenario == "permission denied" {
				state.update = false
			}
			if scenario == "same version" {
				state.latest["versionCode"] = 42
			}
			if scenario == "other package" {
				state.latest["packageName"] = "com.other.app"
			}
			result, err := e.Execute(context.Background(), r)
			if scenario == "allowed" {
				if err != nil || result.State != "awaiting_review" || state.pushes != 1 {
					t.Fatal("valid update failed", err)
				}
			} else if err == nil || state.pushes != 0 {
				t.Fatal("unsafe update reached push")
			}
		})
	}
}

func TestXiaomiConcurrentEnginesSubmitOnlyOnce(t *testing.T) {
	config, r, e, state := xiaomiFixture(t)
	other := openXiaomiTestEngine(t, config, r, state)
	var group sync.WaitGroup
	group.Add(2)
	for _, engine := range []*Engine{e, other} {
		go func(engine *Engine) { defer group.Done(); _, _ = engine.Execute(context.Background(), r) }(engine)
	}
	group.Wait()
	if state.pushes != 1 {
		t.Fatal("concurrent engines duplicated submission")
	}
}

func TestXiaomiApprovalAndMaterialChangeCannotBorrowTheOriginalResult(t *testing.T) {
	_, r, e, state := xiaomiFixture(t)
	result, err := e.Execute(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	r.Binding = strings.Repeat("f", 64)
	if _, err = e.Execute(context.Background(), r); err == nil {
		t.Fatal("changed approval reused prior result")
	}
	r.Action = "status"
	r.ExternalID = result.ExternalID
	if _, err = e.Execute(context.Background(), r); err == nil {
		t.Fatal("changed approval queried prior result")
	}
	if state.pushes != 1 {
		t.Fatal("binding conflict caused a push")
	}
}

func TestXiaomiProfilesRejectUnrelatedOrIncompleteDestinations(t *testing.T) {
	config, _, _, _ := xiaomiFixture(t)
	for _, scenario := range []string{"target", "package", "signer", "sdk", "material", "digest", "track", "hosts", "mainland", "foreign driver"} {
		t.Run(scenario, func(t *testing.T) {
			p := config.Profiles[0]
			switch scenario {
			case "target":
				p.Target = "android-huawei"
			case "package":
				p.BundleID = "com.other.app"
			case "signer":
				p.ExpectedSigner = "debug"
			case "sdk":
				p.AAPTPath = "aapt"
			case "material":
				p.MetadataFile = ""
			case "digest":
				p.MetadataDigest = ""
			case "track":
				p.Track = "production"
			case "hosts":
				p.UploadHosts = []string{"untrusted.example"}
			case "mainland":
				value := true
				p.ChineseMainland = &value
			case "foreign driver":
				p.Driver = "google-play"
				p.Target = "android-google-play"
				p.Track = "production"
			}
			changed := config
			changed.Profiles = []Profile{p}
			if e, err := Open(changed); err == nil {
				e.Close()
				t.Fatal("invalid fixed profile accepted")
			}
		})
	}
}

func TestXiaomiRejectsUnreviewedOrInvalidMaterialWithoutReservation(t *testing.T) {
	for _, scenario := range []string{"missing type", "content only", "missing name", "six screenshots", "image size", "not an image", "image link", "private metadata", "credential mode", "unknown credential", "trailing metadata", "duplicate metadata"} {
		t.Run(scenario, func(t *testing.T) {
			config, r, e, state := xiaomiFixture(t)
			switch scenario {
			case "missing type":
				delete(state.metadata, "synchroType")
			case "content only":
				state.metadata["synchroType"] = 2
			case "missing name":
				delete(state.metadata["appInfo"].(map[string]any), "appName")
			case "six screenshots":
				items := state.metadata["screenshots"].([]any)
				state.metadata["screenshots"] = append(items, items...)
			case "image size":
				state.metadata["icon"].(map[string]any)["bytes"] = 1
			case "not an image":
				asset := state.metadata["icon"].(map[string]any)
				body := []byte("not a PNG")
				digest := sha256.Sum256(body)
				if err := os.WriteFile(asset["path"].(string), body, 0600); err != nil {
					t.Fatal(err)
				}
				asset["bytes"] = len(body)
				asset["sha256"] = hex.EncodeToString(digest[:])
			case "image link":
				asset := state.metadata["icon"].(map[string]any)
				link := filepath.Join(t.TempDir(), "icon-link")
				if err := os.Symlink(asset["path"].(string), link); err != nil {
					t.Fatal(err)
				}
				asset["path"] = link
			case "private metadata":
				if err := os.Chmod(state.metadataPath, 0644); err != nil {
					t.Fatal(err)
				}
			case "credential mode":
				if err := os.Chmod(config.Profiles[0].CredentialsFile, 0644); err != nil {
					t.Fatal(err)
				}
			case "unknown credential":
				body, _ := os.ReadFile(config.Profiles[0].CredentialsFile)
				var obj map[string]any
				_ = json.Unmarshal(body, &obj)
				obj["apiURL"] = "https://untrusted.example"
				body, _ = json.Marshal(obj)
				if err := os.WriteFile(config.Profiles[0].CredentialsFile, body, 0600); err != nil {
					t.Fatal(err)
				}
			}
			rewriteXiaomiMetadata(t, &config, state)
			if scenario == "trailing metadata" || scenario == "duplicate metadata" {
				body, _ := os.ReadFile(state.metadataPath)
				if scenario == "trailing metadata" {
					body = append(body, []byte(` {}`)...)
				} else {
					body = append([]byte(`{"synchroType":1,`), body[1:]...)
				}
				if err := os.WriteFile(state.metadataPath, body, 0600); err != nil {
					t.Fatal(err)
				}
				digest := sha256.Sum256(body)
				config.Profiles[0].MetadataDigest = hex.EncodeToString(digest[:])
			}
			e.Config = config
			if _, err := e.Execute(context.Background(), r); err == nil {
				t.Fatal("invalid private material accepted")
			}
			if state.queries != 0 || state.pushes != 0 {
				t.Fatal("invalid input reached Xiaomi")
			}
			if _, err := e.Inspect(context.Background(), r.OperationKey); err == nil {
				t.Fatal("invalid input reserved a push")
			}
		})
	}
}

func TestXiaomiUsesStagedBytesAndDropsInheritedCookiesAndRedirects(t *testing.T) {
	config, r, e, state := xiaomiFixture(t)
	jar, _ := cookiejar.New(nil)
	destination, _ := url.Parse(xiaomiAPIOrigin)
	jar.SetCookies(destination, []*http.Cookie{{Name: "private-session", Value: "must-not-leak", Path: "/"}})
	e.HTTP.Jar = jar
	e.HTTP.CheckRedirect = func(*http.Request, []*http.Request) error {
		t.Error("inherited redirect callback executed")
		return nil
	}
	state.metadata["appInfo"].(map[string]any)["testAccount"] = map[string]string{"zh_CN": "test-only-demo-user"}
	rewriteXiaomiMetadata(t, &config, state)
	e.Config = config
	state.queryHook = func(n int) {
		if n != 1 {
			return
		}
		for _, path := range []string{r.ArtifactPath, state.metadata["icon"].(map[string]any)["path"].(string), state.metadataPath} {
			if err := os.WriteFile(path, []byte("modified after staging"), 0600); err != nil {
				t.Error(err)
			}
		}
	}
	if _, err := e.Execute(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	var input struct {
		AppInfo struct {
			TestAccount string `json:"testAccount"`
		} `json:"appInfo"`
	}
	if json.Unmarshal(state.files["RequestData"], &input) != nil || input.AppInfo.TestAccount != `{"zh_CN":"test-only-demo-user"}` {
		t.Fatal("test account was not encoded using the official nested JSON string")
	}
}

func TestXiaomiAmbiguousSuccessRemainsUncertain(t *testing.T) {
	for _, failure := range []string{`{"result":-7,"result":0}`, `{"result":-7,"Result":0}`, `{"result":-7,"reſult":0}`, `null`, `{"result":"0"}`, `{"result":0,"message":"` + strings.Repeat("x", 1<<20) + `"}`} {
		t.Run(strconv.Itoa(len(failure))+failure[:min(20, len(failure))], func(t *testing.T) {
			config, r, e, state := xiaomiFixture(t)
			state.failure = failure
			if _, err := e.Execute(context.Background(), r); err == nil {
				t.Fatal("ambiguous success released the write reservation")
			}
			op, err := e.Inspect(context.Background(), r.OperationKey)
			if err != nil || op.State != "pending" {
				t.Fatal("uncertain intent was lost", err)
			}
			reopened := openXiaomiTestEngine(t, config, r, state)
			if _, err = reopened.Execute(context.Background(), r); err == nil || state.pushes != 1 {
				t.Fatal("uncertain request replayed")
			}
		})
	}
}

func TestXiaomiRechecksPermissionUnderReservation(t *testing.T) {
	_, r, e, state := xiaomiFixture(t)
	state.queryHook = func(n int) {
		if n == 2 {
			state.create = false
		}
	}
	if _, err := e.Execute(context.Background(), r); err == nil || state.pushes != 0 {
		t.Fatal("stale creation permission reached push")
	}
	op, err := e.Inspect(context.Background(), r.OperationKey)
	if err != nil || op.State != "pending" || op.Receipt.Xiaomi.Stage != "permission_check" {
		t.Fatal("reservation did not record the stopped no-write stage", err)
	}
}

func TestXiaomiLocalCompletionFailureRetainsIntentAndReservation(t *testing.T) {
	config, r, e, state := xiaomiFixture(t)
	state.pushHook = func() {
		if _, err := e.db.Exec(`CREATE TRIGGER reject_xiaomi_completion BEFORE UPDATE OF state ON operations WHEN NEW.state='complete' BEGIN SELECT RAISE(ABORT,'injected completion failure'); END`); err != nil {
			t.Error(err)
		}
	}
	if _, err := e.Execute(context.Background(), r); err == nil {
		t.Fatal("local completion failure was ignored")
	}
	op, err := e.Inspect(context.Background(), r.OperationKey)
	if err != nil || op.State != "pending" || op.Receipt.Xiaomi.Stage != "push_intent" {
		t.Fatal("accepted push lost its uncertain intent", err)
	}
	var reservations int
	if err := e.db.QueryRow("SELECT COUNT(*) FROM xiaomi_destinations WHERE operation_key=?", r.OperationKey).Scan(&reservations); err != nil || reservations != 1 {
		t.Fatal("local transaction failure released the destination lock", err)
	}
	reopened := openXiaomiTestEngine(t, config, r, state)
	r.OperationKey = strings.Repeat("e", 64)
	if _, err = reopened.Execute(context.Background(), r); err == nil || state.pushes != 1 {
		t.Fatal("failed completion freed the package reservation")
	}
}

func TestXiaomiConfirmedSubmissionReleasesReservationAndBindsNewMaterial(t *testing.T) {
	config, r, e, state := xiaomiFixture(t)
	if _, err := e.Execute(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err := e.db.QueryRow("SELECT COUNT(*) FROM xiaomi_destinations").Scan(&remaining); err != nil || remaining != 0 {
		t.Fatal("confirmed submission kept the package reserved", err)
	}
	state.metadata["appInfo"].(map[string]any)["brief"] = "重新审核的简介"
	rewriteXiaomiMetadata(t, &config, state)
	e.Config = config
	if _, err := e.Execute(context.Background(), r); err == nil || state.pushes != 1 {
		t.Fatal("changed reviewed material borrowed the previous acknowledgment")
	}
}

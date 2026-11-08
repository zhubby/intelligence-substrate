package publisher

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type oppoFixtureState struct {
	mu                                              sync.Mutex
	engine                                          *Engine
	transport                                       http.RoundTripper
	tokens, tickets, uploads, submits, tasks, infos int
	metadata                                        map[string]any
	materialPath                                    string
	files                                           map[string][]byte
	uploadURL                                       string
	latest, exact                                   map[string]any
	taskState                                       string
	failurePath, failureBody                        string
	tokenExpiry                                     int64
	badUploadDigest                                 bool
	infoHook                                        func(int)
	submitHook                                      func()
	seenSign                                        map[string]bool
}

var oppoPictureCache sync.Map

func oppoTestPicture(t *testing.T, w, h int) []byte {
	t.Helper()
	size := image.Pt(w, h)
	if cached, ok := oppoPictureCache.Load(size); ok {
		return cached.([]byte)
	}
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	stored, _ := oppoPictureCache.LoadOrStore(size, b.Bytes())
	return stored.([]byte)
}

func oppoFixture(t *testing.T) (Config, Request, *Engine, *oppoFixtureState) {
	t.Helper()
	config, r := publisherFixture(t)
	dir := t.TempDir()
	credentials := filepath.Join(dir, "oppo-credentials.json")
	b, _ := json.Marshal(map[string]string{"clientId": "1234567890123456789", "clientSecret": strings.Repeat("s", 64)})
	if err := os.WriteFile(credentials, b, 0600); err != nil {
		t.Fatal(err)
	}
	state := &oppoFixtureState{files: map[string][]byte{}, seenSign: map[string]bool{}, taskState: "1", uploadURL: "https://upload.oppo.example.test/api/utility/upload", materialPath: filepath.Join(dir, "metadata.json")}
	asset := func(name string, w, h int) map[string]any {
		picture := oppoTestPicture(t, w, h)
		p := filepath.Join(dir, name+".png")
		if err := os.WriteFile(p, picture, 0600); err != nil {
			t.Fatal(err)
		}
		hash := sha256.Sum256(picture)
		state.files[name+".png"] = picture
		return map[string]any{"path": p, "sha256": hex.EncodeToString(hash[:]), "bytes": len(picture)}
	}
	state.metadata = map[string]any{"appName": "企业反馈", "secondCategoryId": 74, "thirdCategoryId": 6685, "summary": "收集客户反馈", "detailDesc": "用于企业团队收集客户反馈意见并跟踪处理进度的应用", "updateDesc": "修复问题并完善反馈流程", "privacyURL": "https://company.example/privacy?a=1&b=中文", "testDesc": "使用企业测试账号进行验收", "businessUsername": "张三", "businessEmail": "owner@example.test", "businessMobile": "13800000000", "ageLevel": 3, "adaptiveEquipment": 4, "icon": asset("icon", 512, 512), "screenshots": []any{asset("screen1", 1080, 1920), asset("screen2", 1080, 1920), asset("screen3", 1080, 1920)}, "copyright": asset("copyright", 256, 256)}
	b, _ = json.Marshal(state.metadata)
	if err := os.WriteFile(state.materialPath, b, 0600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(b)
	config.Profiles = []Profile{{ProjectID: r.ProjectID, GoalID: r.GoalID, Target: "android-oppo", Driver: "oppo-store", AppID: "100123", BundleID: "com.company.feedback", ExpectedSigner: strings.Repeat("c", 64), CredentialsFile: credentials, AAPTPath: "/opt/android/aapt", APKSignerPath: "/opt/android/apksigner", MetadataFile: state.materialPath, MetadataDigest: hex.EncodeToString(digest[:]), UploadHosts: []string{"upload.oppo.example.test"}}}
	r.Target = "android-oppo"
	payload, err := os.ReadFile(r.ArtifactPath)
	if err != nil {
		t.Fatal(err)
	}
	state.files["release.apk"] = payload
	md := md5.Sum(payload)
	state.latest = map[string]any{"app_id": "100123", "pkg_name": "com.company.feedback", "type": 7, "is_business": "0", "is_freeze": "0", "transfer_state": -1, "update_info_check": 0, "version_id": "401", "version_code": "41", "version_name": "0.9.0", "audit_status": "111", "state": "1", "release_type": "1", "release_status": "0"}
	state.exact = map[string]any{"app_id": "100123", "pkg_name": "com.company.feedback", "type": 7, "is_business": "0", "is_freeze": "0", "transfer_state": -1, "update_info_check": 0, "version_id": "402", "version_code": "42", "version_name": "1.0.0", "version_type": "2", "apk_md5": hex.EncodeToString(md[:]), "apk_size": strconv.Itoa(len(payload)), "audit_status": "1", "state": "1", "release_type": "1", "release_status": "0", "online_info_offline_apply": []any{map[string]any{"app_id": "100123", "version_id": "401", "online_state": "1", "online_time": "2026-11-01 10:00:00", "offline_time": ""}}, "app_secret": "do-not-persist-app-secret", "server_secret": "do-not-persist-server-secret", "audit_status_name": "untrusted upstream free text"}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		state.mu.Lock()
		defer state.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if q.TLS == nil || q.Header.Get("Cookie") != "" || q.Header.Get("Authorization") != "" {
			t.Error("OPPO leaked ambient authentication")
		}
		if q.Host != "oop-openapi-cn.heytapmobi.com" && q.Host != "upload.oppo.example.test" {
			t.Error("unapproved OPPO host")
		}
		if q.URL.Path == "/developer/v1/token" {
			state.tokens++
			if q.Method != "GET" || q.URL.Query().Get("client_id") != "1234567890123456789" || q.URL.Query().Get("client_secret") != strings.Repeat("s", 64) || len(q.URL.Query()) != 2 {
				t.Error("incorrect OPPO token parameters")
			}
			if oppoFail(w, state, q.URL.Path) {
				return
			}
			exp := time.Now().Add(48 * time.Hour).Unix()
			if state.tokenExpiry != 0 {
				exp = state.tokenExpiry
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"errno": 0, "data": map[string]any{"access_token": "test-only-oppo-access-token", "expire_in": exp}})
			return
		}
		params := q.URL.Query()
		var uploaded []byte
		var filename string
		if q.URL.Path == "/api/utility/upload" {
			if q.Method != "POST" || q.Host != "upload.oppo.example.test" || q.ContentLength < 1 {
				t.Error("upload origin/method/length changed")
			}
			reader, err := q.MultipartReader()
			if err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			seen := map[string]bool{}
			for {
				part, err := reader.NextPart()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Error(err)
					return
				}
				name := part.FormName()
				if seen[name] {
					t.Error("duplicate upload part")
				}
				seen[name] = true
				body, err := io.ReadAll(part)
				if err != nil {
					t.Error(err)
					return
				}
				if name == "file" {
					uploaded = body
					filename = part.FileName()
				} else {
					if !strings.Contains(strings.ToLower(part.Header.Get("Content-Type")), "charset=utf-8") {
						t.Error("upload text lacks UTF-8 charset")
					}
					params.Set(name, string(body))
				}
			}
			if len(seen) != 3 || !seen["sign"] || !seen["type"] || !seen["file"] {
				t.Error("unexpected upload fields")
			}
		} else if q.Method == "POST" {
			if q.URL.RawQuery != "" {
				t.Error("form business request mixed query and body")
			}
			if err := q.ParseForm(); err != nil {
				t.Error(err)
				return
			}
			params = q.PostForm
		}
		// Independently implement the official raw-value signing formula.
		var keys []string
		for key, values := range params {
			if len(values) != 1 {
				t.Error("ambiguous signed parameters")
			}
			if key != "api_sign" {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		var pairs []string
		for _, key := range keys {
			pairs = append(pairs, key+"="+params.Get(key))
		}
		h := hmac.New(sha256.New, []byte(strings.Repeat("s", 64)))
		h.Write([]byte(strings.Join(pairs, "&")))
		stamp, err := strconv.ParseInt(params.Get("timestamp"), 10, 64)
		if err != nil || stamp < time.Now().Add(-time.Minute).Unix() || params.Get("access_token") != "test-only-oppo-access-token" || params.Get("api_sign") != hex.EncodeToString(h.Sum(nil)) || params.Get("client_secret") != "" {
			t.Error("OPPO signature does not bind raw parameters")
		}
		switch q.URL.Path {
		case "/resource/v1/app/info":
			state.infos++
			if state.infoHook != nil {
				state.infoHook(state.infos)
			}
			if q.Method != "GET" || params.Get("pkg_name") != "com.company.feedback" {
				t.Error("wrong application query")
			}
			if oppoFail(w, state, q.URL.Path) {
				return
			}
			data := state.latest
			if code := params.Get("version_code"); code != "" {
				if code != "42" {
					t.Error("query not bound to verified version")
				}
				data = state.exact
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"errno": 0, "data": data})
		case "/resource/v1/upload/get-upload-url":
			state.tickets++
			if q.Method != "GET" {
				t.Error("upload configuration method")
			}
			op, err := state.engine.Inspect(q.Context(), r.OperationKey)
			if err != nil || op.State != "pending" || op.Receipt.Oppo == nil || !strings.HasSuffix(op.Receipt.Oppo.Stage, "_upload_config_intent") {
				t.Error("upload config before durable reservation")
			}
			if oppoFail(w, state, q.URL.Path) {
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"errno": 0, "data": map[string]any{"upload_url": state.uploadURL, "sign": fmt.Sprint("ticket-", state.tickets)}})
		case "/api/utility/upload":
			state.uploads++
			sign := params.Get("sign")
			if sign == "" || state.seenSign[sign] {
				t.Error("reused upload ticket")
			}
			state.seenSign[sign] = true
			if params.Get("type") != "apk" && params.Get("type") != "photo" {
				t.Error("unexpected upload type")
			}
			if !bytes.Equal(state.files[filename], uploaded) {
				t.Error("uploaded bytes differ from approved staged asset")
			}
			op, err := state.engine.Inspect(q.Context(), r.OperationKey)
			name := strings.TrimSuffix(filename, filepath.Ext(filename))
			if name == "release" {
				name = "apk"
			}
			if err != nil || op.State != "pending" || op.Receipt.Oppo == nil || op.Receipt.Oppo.Stage != name+"_bytes_intent" {
				t.Error("upload before durable intent")
			}
			if oppoFail(w, state, q.URL.Path) {
				return
			}
			digest := md5.Sum(uploaded)
			sum := hex.EncodeToString(digest[:])
			if state.badUploadDigest {
				sum = strings.Repeat("e", 32)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"errno": 0, "data": map[string]any{"url": "https://store-download.oppo.example.test/apk/" + filename, "md5": sum, "file_size": len(uploaded), "file_extension": strings.TrimPrefix(filepath.Ext(filename), "."), "id": sign}})
		case "/resource/v1/app/upd":
			state.submits++
			if q.Method != "POST" {
				t.Error("submission method")
			}
			if state.submitHook != nil {
				state.submitHook()
			}
			op, err := state.engine.Inspect(q.Context(), r.OperationKey)
			if err != nil || op.State != "pending" {
				t.Error("submission without intent")
			}
			if state.uploads != 6 || state.tickets != 6 || params.Get("pkg_name") != "com.company.feedback" || params.Get("version_code") != "42" || params.Get("online_type") != "1" || params.Get("age_level") != "3" || params.Get("adaptive_equipment") != "4" || params.Get("privacy_source_url") != "https://company.example/privacy?a=1&b=中文" || params.Get("app_name") != "企业反馈" {
				t.Error("submission lost reviewed material or verified identity")
			}
			var apk []struct {
				URL, MD5 string
				CPU      int `json:"cpu_code"`
			}
			if json.Unmarshal([]byte(params.Get("apk_url")), &apk) != nil || len(apk) != 1 || apk[0].URL != "https://store-download.oppo.example.test/apk/release.apk" || apk[0].MD5 != hex.EncodeToString(md[:]) || apk[0].CPU != 0 {
				t.Error("submission changed upload receipt")
			}
			if !strings.HasSuffix(params.Get("copyright_url"), "/copyright.png") || len(strings.Split(params.Get("pic_url"), ",")) != 3 {
				t.Error("missing uploaded images/copyright")
			}
			if oppoFail(w, state, q.URL.Path) {
				return
			}
			_, _ = io.WriteString(w, `{"errno":0,"data":{"success":true}}`)
		case "/resource/v1/app/task-state":
			state.tasks++
			if q.Method != "POST" || params.Get("pkg_name") != "com.company.feedback" || params.Get("version_code") != "42" {
				t.Error("task observation not bound to version")
			}
			if oppoFail(w, state, q.URL.Path) {
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"errno": 0, "data": map[string]any{"pkg_name": "com.company.feedback", "version_code": "42", "task_state": state.taskState, "err_msg": "untrusted secret-bearing error"}})
		default:
			t.Error("unsupported OPPO endpoint", q.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(server.Close)
	local, _ := url.Parse(server.URL)
	transport := server.Client().Transport.(*http.Transport).Clone()
	transport.TLSClientConfig.ServerName = server.Certificate().DNSNames[0]
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != "oop-openapi-cn.heytapmobi.com:443" && address != "upload.oppo.example.test:443" {
			t.Error("unapproved outbound request", address)
			return nil, io.ErrUnexpectedEOF
		}
		return (&net.Dialer{}).DialContext(ctx, network, local.Host)
	}
	t.Cleanup(transport.CloseIdleConnections)
	state.transport = transport
	engine, err := Open(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Close() })
	configureOppoFixture(t, engine, r, state)
	state.engine = engine
	return config, r, engine, state
}

func oppoFail(w http.ResponseWriter, s *oppoFixtureState, path string) bool {
	if s.failurePath != path {
		return false
	}
	if s.failureBody == "lost" {
		connection, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			_ = connection.Close()
		}
		return true
	}
	if s.failureBody == "redirect" {
		w.Header().Set("Location", "https://evil.example/stolen")
		w.WriteHeader(307)
		return true
	}
	_, _ = io.WriteString(w, s.failureBody)
	return true
}

func configureOppoFixture(t *testing.T, e *Engine, r Request, s *oppoFixtureState) {
	t.Helper()
	e.HTTP.Transport = s.transport
	e.verifyAndroidPackage = func(_ context.Context, p Profile, request Request, name string) (androidPackage, error) {
		b, err := os.ReadFile(name)
		if err != nil || !bytes.Equal(b, s.files["release.apk"]) || p.BundleID != "com.company.feedback" || request.Version != r.Version {
			t.Fatal("SDK boundary lost staged artifact binding")
		}
		return androidPackage{Version: "1.0.0", VersionCode: 42}, nil
	}
	jar, _ := cookiejar.New(nil)
	u, _ := url.Parse("https://oop-openapi-cn.heytapmobi.com")
	jar.SetCookies(u, []*http.Cookie{{Name: "unrelated", Value: "must-not-leak"}})
	e.HTTP.Jar = jar
}

func rewriteOppoMetadata(t *testing.T, c *Config, s *oppoFixtureState) {
	t.Helper()
	b, err := json.Marshal(s.metadata)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(s.materialPath, b, 0600); err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(b)
	c.Profiles[0].MetadataDigest = hex.EncodeToString(h[:])
}

func TestOppoApprovedBytesAsyncSubmissionAndTokenCache(t *testing.T) {
	config, r, e, s := oppoFixture(t)
	result, err := e.Execute(context.Background(), r)
	if err != nil || result.State != "processing" || result.ExternalID != "oppo:100123:42" {
		t.Fatalf("want accepted async task, got %+v %v", result, err)
	}
	again, err := e.Execute(context.Background(), r)
	if err != nil || again != result {
		t.Fatal("confirmed submission not idempotent", err)
	}
	q := r
	q.Action = "status"
	q.ExternalID = result.ExternalID
	q.ArtifactPath = ""
	for i := 0; i < 2; i++ {
		out, err := e.Execute(context.Background(), q)
		if err != nil || out.State != "processing" {
			t.Fatal(out, err)
		}
	}
	reopened, err := Open(config)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	configureOppoFixture(t, reopened, r, s)
	if _, err = reopened.Execute(context.Background(), q); err != nil {
		t.Fatal(err)
	}
	if s.tokens != 1 || s.submits != 1 || s.uploads != 6 || s.tickets != 6 || s.tasks != 3 {
		t.Fatal("unexpected retry/token refresh or missing assets", s.tokens, s.submits, s.uploads, s.tickets, s.tasks)
	}
	var locks int
	if err = e.db.QueryRow("SELECT count(*) FROM oppo_destinations").Scan(&locks); err != nil || locks != 0 {
		t.Fatal("confirmed task left package reservation", err)
	}
	op, err := e.Inspect(context.Background(), r.OperationKey)
	if err != nil || op.State != "complete" {
		t.Fatal(op, err)
	}
	b, _ := json.Marshal(op)
	for _, secret := range []string{"test-only-oppo-access-token", strings.Repeat("s", 64), "ticket-", "do-not-persist", "store-download"} {
		if bytes.Contains(b, []byte(secret)) {
			t.Fatal("operation persisted credential or mutable upstream data")
		}
	}
}

func TestOppoProfileAndMaterialFailBeforeNetwork(t *testing.T) {
	for _, name := range []string{"app-id", "package", "target", "missing-signer", "upload-wildcard", "upload-http-path", "upload-api-host", "extra-destination", "metadata-digest", "asset-tampering", "asset-symlink", "missing-copyright", "wrong-icon-size", "wrong-screen-size", "few-screens", "bad-summary", "short-description", "missing-age", "privacy-http", "unknown-field", "duplicate-json", "duplicate-case", "credentials-public", "bad-credentials", "sdk-rejection", "artifact-digest"} {
		t.Run(name, func(t *testing.T) {
			c, r, e, s := oppoFixture(t)
			invalidProfile := false
			switch name {
			case "app-id":
				c.Profiles[0].AppID = "other"
				invalidProfile = true
			case "package":
				c.Profiles[0].BundleID = "not a package"
				invalidProfile = true
			case "target":
				c.Profiles[0].Target = "android-xiaomi"
				invalidProfile = true
			case "missing-signer":
				c.Profiles[0].ExpectedSigner = ""
				invalidProfile = true
			case "upload-wildcard":
				c.Profiles[0].UploadHosts = []string{"*.oppo.example.test"}
				invalidProfile = true
			case "upload-http-path":
				c.Profiles[0].UploadHosts = []string{"upload.oppo.example.test/path"}
				invalidProfile = true
			case "upload-api-host":
				c.Profiles[0].UploadHosts = []string{"oop-openapi-cn.heytapmobi.com"}
				invalidProfile = true
			case "extra-destination":
				c.Profiles[0].Channel = "stable"
				invalidProfile = true
			case "metadata-digest":
				c.Profiles[0].MetadataDigest = strings.Repeat("e", 64)
			case "asset-tampering":
				path := s.metadata["icon"].(map[string]any)["path"].(string)
				if err := os.WriteFile(path, []byte("changed image"), 0600); err != nil {
					t.Fatal(err)
				}
			case "asset-symlink":
				asset := s.metadata["icon"].(map[string]any)
				link := filepath.Join(t.TempDir(), "icon-link.png")
				if err := os.Symlink(asset["path"].(string), link); err != nil {
					t.Fatal(err)
				}
				asset["path"] = link
				rewriteOppoMetadata(t, &c, s)
			case "missing-copyright":
				delete(s.metadata, "copyright")
				rewriteOppoMetadata(t, &c, s)
			case "wrong-icon-size":
				s.metadata["icon"] = s.metadata["copyright"]
				rewriteOppoMetadata(t, &c, s)
			case "wrong-screen-size":
				s.metadata["screenshots"] = []any{s.metadata["icon"], s.metadata["icon"], s.metadata["icon"]}
				rewriteOppoMetadata(t, &c, s)
			case "few-screens":
				s.metadata["screenshots"] = s.metadata["screenshots"].([]any)[:2]
				rewriteOppoMetadata(t, &c, s)
			case "bad-summary":
				s.metadata["summary"] = "包含 标点！"
				rewriteOppoMetadata(t, &c, s)
			case "short-description":
				s.metadata["detailDesc"] = "太短"
				rewriteOppoMetadata(t, &c, s)
			case "missing-age":
				delete(s.metadata, "ageLevel")
				rewriteOppoMetadata(t, &c, s)
			case "privacy-http":
				s.metadata["privacyURL"] = "http://company.example/privacy"
				rewriteOppoMetadata(t, &c, s)
			case "unknown-field":
				s.metadata["apkURL"] = "https://unreviewed.example/apk"
				rewriteOppoMetadata(t, &c, s)
			case "duplicate-json", "duplicate-case":
				b, _ := json.Marshal(s.metadata)
				field := `"appName":"changed",`
				if name == "duplicate-case" {
					field = `"APPNAME":"changed",`
				}
				b = append([]byte("{"+field), b[1:]...)
				if err := os.WriteFile(s.materialPath, b, 0600); err != nil {
					t.Fatal(err)
				}
				h := sha256.Sum256(b)
				c.Profiles[0].MetadataDigest = hex.EncodeToString(h[:])
			case "credentials-public":
				if err := os.Chmod(c.Profiles[0].CredentialsFile, 0644); err != nil {
					t.Fatal(err)
				}
			case "bad-credentials":
				if err := os.WriteFile(c.Profiles[0].CredentialsFile, []byte(`{"clientId":"short","clientSecret":"private"}`), 0600); err != nil {
					t.Fatal(err)
				}
			case "sdk-rejection":
				e.verifyAndroidPackage = func(context.Context, Profile, Request, string) (androidPackage, error) {
					return androidPackage{}, fmt.Errorf("synthetic SDK signer rejection")
				}
			case "artifact-digest":
				r.ArtifactDigest = strings.Repeat("e", 64)
			}
			if invalidProfile {
				opened, err := Open(c)
				if err == nil {
					opened.Close()
					t.Fatal("unapproved OPPO profile accepted")
				}
			} else {
				e.Config = c
				if _, err := e.Execute(context.Background(), r); err == nil {
					t.Fatal("unapproved bytes/material accepted")
				}
			}
			if s.tokens+s.infos+s.tickets+s.uploads+s.submits != 0 {
				t.Fatal("invalid inputs reached network")
			}
			for _, table := range []string{"operations", "oppo_destinations"} {
				var n int
				if err := e.db.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil || n != 0 {
					t.Fatal("failed preconditions reserved publication", table, n, err)
				}
			}
		})
	}
}

func TestOppoExistingOrdinaryPublishedAppRequired(t *testing.T) {
	for _, name := range []string{"wrong-app", "wrong-package", "game", "cooperative", "frozen", "transfer", "no-transfer-field", "info-review", "pending-review", "unpublished", "off-shelf", "staged", "version-equal", "version-newer", "invalid-version", "missing-version-id"} {
		t.Run(name, func(t *testing.T) {
			_, r, e, s := oppoFixture(t)
			switch name {
			case "wrong-app":
				s.latest["app_id"] = "999"
			case "wrong-package":
				s.latest["pkg_name"] = "com.other.app"
			case "game":
				s.latest["type"] = 8
			case "cooperative":
				s.latest["is_business"] = "1"
			case "frozen":
				s.latest["is_freeze"] = "1"
			case "transfer":
				s.latest["transfer_state"] = 1
			case "no-transfer-field":
				delete(s.latest, "transfer_state")
			case "info-review":
				s.latest["update_info_check"] = 1
			case "pending-review":
				s.latest["audit_status"] = "1"
			case "unpublished":
				s.latest["audit_status"] = "0"
			case "off-shelf":
				s.latest["state"] = "2"
			case "staged":
				s.latest["release_type"] = "2"
			case "version-equal":
				s.latest["version_code"] = "42"
			case "version-newer":
				s.latest["version_code"] = "43"
			case "invalid-version":
				s.latest["version_code"] = "unknown"
			case "missing-version-id":
				delete(s.latest, "version_id")
			}
			if _, err := e.Execute(context.Background(), r); err == nil {
				t.Fatal("unapproved existing app state accepted")
			}
			if s.tickets+s.uploads+s.submits != 0 {
				t.Fatal("ineligible app mutated")
			}
			var n int
			if err := e.db.QueryRow("SELECT count(*) FROM operations").Scan(&n); err != nil || n != 0 {
				t.Fatal("deterministic app failure reserved operation", err)
			}
		})
	}
}

func TestOppoTokenErrorsDoNotReserveMutation(t *testing.T) {
	for _, body := range []string{`{}`, `{"errno":0,"data":{"access_token":"short","expire_in":9999999999}}`, `{"errno":-1,"errno":0,"data":{}}`, `{"errno":0,"data":{"access_token":"test-only-oppo-access-token","expire_in":172800}}`, `{"errno":0,"data":{}}`, `{"errno":800002,"data":{"message":"private token URL"}}`, "redirect", "lost"} {
		t.Run(fmt.Sprint(len(body), "-", body[:min(12, len(body))]), func(t *testing.T) {
			_, r, e, s := oppoFixture(t)
			s.failurePath = "/developer/v1/token"
			s.failureBody = body
			if _, err := e.Execute(context.Background(), r); err == nil || strings.Contains(err.Error(), "client_secret") || strings.Contains(err.Error(), "private token URL") {
				t.Fatal("invalid token accepted or raw secret exposed", err)
			}
			if s.tokens != 1 || s.infos+s.uploads+s.submits != 0 {
				t.Fatal("token error retried or reached mutation")
			}
			var n int
			if err := e.db.QueryRow("SELECT count(*) FROM operations").Scan(&n); err != nil || n != 0 {
				t.Fatal("token failure reserved operation", err)
			}
		})
	}
}

func TestOppoUncertainWritesNeverReplay(t *testing.T) {
	cases := []struct{ name, path, body string }{
		{"ticket-lost", "/resource/v1/upload/get-upload-url", "lost"},
		{"bytes-lost", "/api/utility/upload", "lost"},
		{"bytes-redirect", "/api/utility/upload", "redirect"},
		{"submit-lost", "/resource/v1/app/upd", "lost"},
		{"submit-redirect", "/resource/v1/app/upd", "redirect"},
		{"submit-rejected", "/resource/v1/app/upd", `{"errno":911000,"data":{"message":"private upstream error"}}`},
		{"missing-success", "/resource/v1/app/upd", `{"errno":0,"data":{}}`},
		{"duplicate-success", "/resource/v1/app/upd", `{"errno":0,"data":{"success":false,"success":true}}`},
		{"duplicate-code", "/resource/v1/app/upd", `{"errno":-1,"errno":0,"data":{"success":true}}`},
		{"trailing-json", "/resource/v1/app/upd", `{"errno":0,"data":{"success":true}} {}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, r, e, s := oppoFixture(t)
			s.failurePath, s.failureBody = tc.path, tc.body
			if _, err := e.Execute(context.Background(), r); err == nil || strings.Contains(err.Error(), "private upstream error") {
				t.Fatal("uncertain mutation accepted/leaked upstream text", err)
			}
			op, err := e.Inspect(context.Background(), r.OperationKey)
			if err != nil || op.State != "pending" || op.Result != nil {
				t.Fatal("uncertain operation lost", op, err)
			}
			beforeTickets, beforeUploads, beforeSubmits := s.tickets, s.uploads, s.submits
			s.failurePath = ""
			reopened, err := Open(c)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			configureOppoFixture(t, reopened, r, s)
			for _, engine := range []*Engine{e, reopened} {
				if _, err := engine.Execute(context.Background(), r); err == nil {
					t.Fatal("uncertain same-key operation replayed")
				}
				changed := r
				changed.OperationKey = strings.Repeat("d", 64)
				if _, err := engine.Execute(context.Background(), changed); err == nil {
					t.Fatal("new key bypassed package reservation")
				}
				q := r
				q.Action = "status"
				q.ExternalID = "oppo:100123:42"
				if _, err := engine.Execute(context.Background(), q); err == nil {
					t.Fatal("status silently adopted pending write")
				}
			}
			if s.tickets != beforeTickets || s.uploads != beforeUploads || s.submits != beforeSubmits {
				t.Fatal("uncertain operation repeated a mutation")
			}
			var n int
			if err = e.db.QueryRow("SELECT count(*) FROM oppo_destinations").Scan(&n); err != nil || n != 1 {
				t.Fatal("uncertain reservation was released", err)
			}
		})
	}
}

func TestOppoUploadDestinationAndDigestCannotBeChanged(t *testing.T) {
	for _, name := range []string{"http", "untrusted", "userinfo", "fragment", "query", "api-origin", "bad-digest"} {
		t.Run(name, func(t *testing.T) {
			_, r, e, s := oppoFixture(t)
			switch name {
			case "http":
				s.uploadURL = "http://upload.oppo.example.test/api/utility/upload"
			case "untrusted":
				s.uploadURL = "https://evil.example/api/utility/upload"
			case "userinfo":
				s.uploadURL = "https://user:pass@upload.oppo.example.test/api/utility/upload"
			case "fragment":
				s.uploadURL += "#fragment"
			case "query":
				s.uploadURL += "?access_token=stolen"
			case "api-origin":
				s.uploadURL = oppoOrigin + "/api/utility/upload"
			case "bad-digest":
				s.badUploadDigest = true
			}
			if _, err := e.Execute(context.Background(), r); err == nil {
				t.Fatal("untrusted upload destination/receipt accepted")
			}
			if s.submits != 0 || s.tickets != 1 || (name != "bad-digest" && s.uploads != 0) {
				t.Fatal("bad upload proceeded into publication")
			}
		})
	}
}

func TestOppoReservationRechecksAndAtomicFinish(t *testing.T) {
	for _, name := range []string{"changed-after-reservation", "completion-failure"} {
		t.Run(name, func(t *testing.T) {
			_, r, e, s := oppoFixture(t)
			if name == "changed-after-reservation" {
				s.infoHook = func(n int) {
					if n == 2 {
						s.latest["audit_status"] = "1"
					}
				}
			} else {
				s.submitHook = func() {
					if _, err := e.db.Exec(`CREATE TRIGGER fail_oppo_finish BEFORE DELETE ON oppo_destinations BEGIN SELECT RAISE(ABORT,'test completion failure'); END`); err != nil {
						t.Error(err)
					}
				}
			}
			if _, err := e.Execute(context.Background(), r); err == nil {
				t.Fatal("lost finalization accepted")
			}
			op, err := e.Inspect(context.Background(), r.OperationKey)
			if err != nil || op.State != "pending" || op.Result != nil {
				t.Fatal("incomplete finish partly committed", op, err)
			}
			var n int
			if err = e.db.QueryRow("SELECT count(*) FROM oppo_destinations").Scan(&n); err != nil || n != 1 {
				t.Fatal("failure lost package reservation", err)
			}
			if name == "changed-after-reservation" && s.tickets+s.uploads+s.submits != 0 {
				t.Fatal("changed permissions reached mutation")
			}
			writes := s.submits
			if _, err = e.Execute(context.Background(), r); err == nil || s.submits != writes {
				t.Fatal("finish failure replayed submission", err)
			}
		})
	}
}

func TestOppoStatusTaskAndExactVersion(t *testing.T) {
	_, r, e, s := oppoFixture(t)
	result, err := e.Execute(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	q := r
	q.Action = "status"
	q.ExternalID = result.ExternalID
	q.ArtifactPath = ""
	before, err := e.Inspect(context.Background(), r.OperationKey)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ task, audit, want string }{{"1", "111", "processing"}, {"3", "111", "rejected"}, {"2", "1", "awaiting_review"}, {"2", "2", "awaiting_release"}, {"2", "6", "awaiting_release"}, {"2", "7", "awaiting_release"}, {"2", "3", "rejected"}, {"2", "5", "rejected"}, {"2", "444", "rejected"}, {"2", "-11", "rejected"}, {"2", "22", "halted"}, {"2", "222", "halted"}, {"2", "00", "awaiting_review"}, {"2", "111", "unchanged"}, {"2", "unknown", "unchanged"}, {"unknown", "111", "unchanged"}} {
		t.Run(tc.task+"-"+tc.audit, func(t *testing.T) {
			s.taskState = tc.task
			s.exact["audit_status"] = tc.audit
			out, err := e.Execute(context.Background(), q)
			if err != nil || out.State != tc.want || out.ExternalID != q.ExternalID {
				t.Fatal("incorrect task/audit observation", out, err)
			}
			if strings.Contains(out.Reason, "untrusted") {
				t.Fatal("leaked upstream free text")
			}
		})
	}
	s.taskState = "2"
	s.exact["audit_status"] = "111"
	s.exact["online_info_offline_apply"] = []any{map[string]any{"app_id": "100123", "version_id": "402", "online_state": "1", "online_time": "2026-11-06 11:00:00", "offline_time": "2026-11-05 10:00:00"}}
	if out, err := e.Execute(context.Background(), q); err != nil || out.State != "published" {
		t.Fatal("matching on-shelf version not observed", out, err)
	}
	after, err := e.Inspect(context.Background(), r.OperationKey)
	if err != nil || before.JournalDigest != after.JournalDigest || s.submits != 1 || s.uploads != 6 {
		t.Fatal("read-only observation changed journal or wrote store", err)
	}
}

func TestOppoObservationRejectsMismatchedAndAmbiguousVersions(t *testing.T) {
	for _, name := range []string{"app", "package", "version-code", "version-name", "version-id", "apk-md5", "apk-size", "missing-md5", "game", "cooperative", "frozen", "transfer", "phased", "phased-paused", "info-review", "old-shelf", "off-shelf", "duplicate-shelf", "later-offline", "invalid-time"} {
		t.Run(name, func(t *testing.T) {
			_, r, e, s := oppoFixture(t)
			result, err := e.Execute(context.Background(), r)
			if err != nil {
				t.Fatal(err)
			}
			q := r
			q.Action = "status"
			q.ExternalID = result.ExternalID
			s.taskState = "2"
			s.exact["audit_status"] = "111"
			shelf := map[string]any{"app_id": "100123", "version_id": "402", "online_state": "1", "online_time": "2026-11-06 11:00:00", "offline_time": ""}
			s.exact["online_info_offline_apply"] = []any{shelf}
			switch name {
			case "app":
				s.exact["app_id"] = "999"
			case "package":
				s.exact["pkg_name"] = "com.other.app"
			case "version-code":
				s.exact["version_code"] = "41"
			case "version-name":
				s.exact["version_name"] = "0.9.0"
			case "version-id":
				s.exact["version_id"] = ""
			case "apk-md5":
				s.exact["apk_md5"] = strings.Repeat("e", 32)
			case "apk-size":
				s.exact["apk_size"] = "1"
			case "missing-md5":
				delete(s.exact, "apk_md5")
			case "game":
				s.exact["type"] = 8
			case "cooperative":
				s.exact["is_business"] = "1"
			case "frozen":
				s.exact["is_freeze"] = "1"
			case "transfer":
				s.exact["transfer_state"] = 1
			case "phased":
				s.exact["release_type"] = "2"
			case "phased-paused":
				s.exact["release_status"] = "2"
			case "info-review":
				s.exact["update_info_check"] = 1
			case "old-shelf":
				shelf["version_id"] = "401"
			case "off-shelf":
				s.exact["state"] = "2"
			case "duplicate-shelf":
				s.exact["online_info_offline_apply"] = []any{shelf, shelf}
			case "later-offline":
				shelf["offline_time"] = "2026-11-06 12:00:00"
			case "invalid-time":
				shelf["online_time"] = "unknown"
			}
			out, err := e.Execute(context.Background(), q)
			if err == nil && out.State == "published" {
				t.Fatal("ambiguous or different package counted as published", name, out)
			}
			if s.submits != 1 || s.uploads != 6 {
				t.Fatal("status mutated channel")
			}
		})
	}
}

func TestOppoBindingAndUnknownOperationCannotQueryOrContinue(t *testing.T) {
	_, r, e, s := oppoFixture(t)
	q := r
	q.Action = "status"
	q.ExternalID = "oppo:100123:42"
	if _, err := e.Execute(context.Background(), q); err == nil {
		t.Fatal("unrecorded version queried")
	}
	result, err := e.Execute(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"external-id", "binding", "material", "continue"} {
		t.Run(name, func(t *testing.T) {
			q := r
			q.Action = "status"
			q.ExternalID = result.ExternalID
			original := e.Config
			switch name {
			case "external-id":
				q.ExternalID = "oppo:100123:41"
			case "binding":
				q.Binding = strings.Repeat("e", 64)
			case "material":
				e.Config.Profiles = append([]Profile(nil), e.Config.Profiles...)
				e.Config.Profiles[0].MetadataDigest = strings.Repeat("e", 64)
			case "continue":
				q.Action = "continue"
			}
			if _, err := e.Execute(context.Background(), q); err == nil {
				t.Fatal("unapproved observation/continuation accepted")
			}
			e.Config = original
		})
	}
	if s.tasks != 0 || s.submits != 1 {
		t.Fatal("rejected operation reached status or submission")
	}
}

func TestOppoAcceptedVersionCannotBeSubmittedUnderNewKey(t *testing.T) {
	c, r, e, s := oppoFixture(t)
	if _, err := e.Execute(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(c)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	configureOppoFixture(t, reopened, r, s)
	changed := r
	changed.OperationKey = strings.Repeat("d", 64)
	if _, err = reopened.Execute(context.Background(), changed); err == nil {
		t.Fatal("asynchronous accepted version was submitted again under another operation key")
	}
	if s.submits != 1 || s.uploads != 6 {
		t.Fatal("fresh key bypassed durable accepted-version ownership")
	}
}

func TestOppoConcurrentEnginesShareTokenAndMutationOwnership(t *testing.T) {
	c, r, e, s := oppoFixture(t)
	other, err := Open(c)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	configureOppoFixture(t, other, r, s)
	entered, release := make(chan struct{}), make(chan struct{})
	s.submitHook = func() { close(entered); <-release }
	done := make(chan error, 1)
	go func() { _, err := e.Execute(context.Background(), r); done <- err }()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("submission did not reach controlled boundary")
	}
	_, secondErr := other.Execute(context.Background(), r)
	close(release)
	if secondErr == nil {
		t.Fatal("concurrent engine adopted/replayed in-flight mutation")
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if s.submits != 1 || s.uploads != 6 || s.tokens != 1 {
		t.Fatal("concurrency repeated upload/token issue")
	}
}

func TestOppoExpiredTokenRefreshIsSerializedAcrossProcesses(t *testing.T) {
	c, r, e, s := oppoFixture(t)
	result, err := e.Execute(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.db.Exec("UPDATE oppo_tokens SET expires_at=1"); err != nil {
		t.Fatal(err)
	}
	other, err := Open(c)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	configureOppoFixture(t, other, r, s)
	q := r
	q.Action = "status"
	q.ExternalID = result.ExternalID
	results := make(chan error, 2)
	for _, engine := range []*Engine{e, other} {
		go func(engine *Engine) {
			out, err := engine.Execute(context.Background(), q)
			if err == nil && out.State != "processing" {
				err = fmt.Errorf("unexpected state %s", out.State)
			}
			results <- err
		}(engine)
	}
	for i := 0; i < 2; i++ {
		if err = <-results; err != nil {
			t.Fatal(err)
		}
	}
	if s.tokens != 2 || s.tasks != 2 || s.submits != 1 {
		t.Fatal("expired token refreshed twice or replayed mutation", s.tokens, s.tasks, s.submits)
	}
}

func TestOppoTokenStateMustRemainPrivate(t *testing.T) {
	for _, mode := range []string{"public-directory", "symlink-directory", "public-database", "symlink-database"} {
		t.Run(mode, func(t *testing.T) {
			c, _, _, _ := oppoFixture(t)
			root := t.TempDir()
			c.StateDir = filepath.Join(root, "oppo-state")
			if err := os.Mkdir(c.StateDir, 0700); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "public-directory":
				if err := os.Chmod(c.StateDir, 0755); err != nil {
					t.Fatal(err)
				}
			case "symlink-directory":
				c.StateDir = filepath.Join(root, "link-state")
				if err := os.Symlink(filepath.Join(root, "oppo-state"), c.StateDir); err != nil {
					t.Fatal(err)
				}
			case "public-database":
				if err := os.WriteFile(filepath.Join(c.StateDir, "publisher.db"), nil, 0644); err != nil {
					t.Fatal(err)
				}
			case "symlink-database":
				other := filepath.Join(root, "other.db")
				if err := os.WriteFile(other, nil, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(other, filepath.Join(c.StateDir, "publisher.db")); err != nil {
					t.Fatal(err)
				}
			}
			opened, err := Open(c)
			if err == nil {
				opened.Close()
				t.Fatal("OPPO token cache accepted public or symlinked state")
			}
		})
	}
}

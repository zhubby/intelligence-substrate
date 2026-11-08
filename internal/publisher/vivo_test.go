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

// SDK success alone is replaced at the external tool boundary. HTTPS, HMAC,
// staged bytes, journals and state transitions below execute the real code.
// These fixtures do not establish a live vivo account or release signature.
type vivoFixtureState struct {
	mu                         sync.Mutex
	engine                     *Engine
	transport                  http.RoundTripper
	metadata                   map[string]any
	materialPath               string
	files                      map[string][]byte
	details                    map[string]any
	queries, uploads, submits  int
	failureMethod, failureBody string
	uploadOverrides            map[string]any
	queryHook                  func(int)
	submitHook                 func()
	uploadHook                 func(int)
	seenHosts                  []string
	versionCode                int64
	versionName                string
}

func vivoFixture(t *testing.T) (Config, Request, *Engine, *vivoFixtureState) {
	t.Helper()
	c, r := publisherFixture(t)
	r.Target = "android-vivo"
	dir := t.TempDir()
	credentialPath := filepath.Join(dir, "vivo.json")
	if err := os.WriteFile(credentialPath, []byte(`{"accessKey":"test-only-vivo-key","accessSecret":"test-only-vivo-secret-0123456789"}`), 0600); err != nil {
		t.Fatal(err)
	}
	s := &vivoFixtureState{materialPath: filepath.Join(dir, "material.json"), files: map[string][]byte{}, details: map[string]any{"packageName": "com.company.feedback", "appType": 1, "saleStatus": 1, "status": 3, "versionCode": 41, "versionName": "0.9.0", "apkSize": 100, "onlineType": 1, "unPassReason": "untrusted text must not enter the journal"}}
	asset := func(name string, width, height int) map[string]any {
		body := oppoTestPicture(t, width, height)
		p := filepath.Join(dir, name+".png")
		if err := os.WriteFile(p, body, 0600); err != nil {
			t.Fatal(err)
		}
		s.files[name+".png"] = body
		h := sha256.Sum256(body)
		return map[string]any{"path": p, "sha256": hex.EncodeToString(h[:]), "bytes": len(body)}
	}
	s.metadata = map[string]any{"updateDesc": "完善客户反馈处理流程", "compatibleDevice": 1, "privateSelfCheck": "https://company.example/privacy?a=1&b=中文", "detailDesc": strings.Repeat("客户反馈管理功能说明", 8), "simpleDesc": "客户反馈", "remark": "请使用提供的企业测试账号验收", "customerService": "owner@example.test", "rateAge": 3, "testAccount": []any{map[string]any{"testAccount": "reviewer", "testPassword": "test-only-account-password"}}, "icon": asset("icon", 512, 512), "screenshots": []any{asset("screenshot1", 1080, 1920), asset("screenshot2", 1080, 1920), asset("screenshot3", 1080, 1920)}}
	b, _ := json.Marshal(s.metadata)
	if err := os.WriteFile(s.materialPath, b, 0600); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(b)
	// JSON construction lets the initial red test reach Open's unsupported
	// driver behavior before adding the new environment field to Profile.
	b, _ = json.Marshal(map[string]any{"projectId": r.ProjectID, "goalId": r.GoalID, "target": r.Target, "driver": "vivo-store", "environment": "production", "appId": "com.company.feedback", "bundleId": "com.company.feedback", "expectedSigner": strings.Repeat("c", 64), "credentialsFile": credentialPath, "aaptPath": "/opt/android/aapt", "apkSignerPath": "/opt/android/apksigner", "metadataFile": s.materialPath, "metadataDigest": hex.EncodeToString(hash[:])})
	c.Profiles = make([]Profile, 1)
	if err := json.Unmarshal(b, &c.Profiles[0]); err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(r.ArtifactPath)
	if err != nil {
		t.Fatal(err)
	}
	s.files["release.apk"] = payload
	apkMD5 := md5.Sum(payload)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if q.TLS == nil || q.Method != "POST" || q.URL.Path != "/router/rest" || q.URL.RawQuery != "" || q.Header.Get("Cookie") != "" || q.Header.Get("Authorization") != "" || q.ContentLength < 1 {
			t.Error("vivo request leaked ambient authentication or lost its fixed non-replayable HTTPS contract")
		}
		s.seenHosts = append(s.seenHosts, q.Host)
		values := url.Values{}
		var file []byte
		var filename string
		if strings.HasPrefix(q.Header.Get("Content-Type"), "multipart/form-data;") {
			reader, err := q.MultipartReader()
			if err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
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
				body, err := io.ReadAll(part)
				if err != nil {
					t.Error(err)
					return
				}
				if name == "file" {
					if file != nil {
						t.Error("duplicate upload file")
					}
					file, filename = body, part.FileName()
				} else {
					if len(values[name]) != 0 || !strings.Contains(strings.ToLower(part.Header.Get("Content-Type")), "charset=utf-8") {
						t.Error("ambiguous upload text or missing UTF-8 charset")
					}
					values.Set(name, string(body))
				}
			}
		} else {
			if err := q.ParseForm(); err != nil {
				t.Error(err)
				return
			}
			values = q.PostForm
		}
		// Independent literal wire contract: HMAC over sorted raw parameter
		// values, excluding file and sign. The timestamp is milliseconds.
		keys := make([]string, 0, len(values))
		for k, vals := range values {
			if len(vals) != 1 || k == "access_secret" || k == "accessSecret" {
				t.Error("invalid or secret-bearing business field")
			}
			if k != "sign" {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		pairs := make([]string, 0, len(keys))
		for _, k := range keys {
			pairs = append(pairs, k+"="+values.Get(k))
		}
		h := hmac.New(sha256.New, []byte("test-only-vivo-secret-0123456789"))
		_, _ = h.Write([]byte(strings.Join(pairs, "&")))
		stamp, err := strconv.ParseInt(values.Get("timestamp"), 10, 64)
		if err != nil || stamp < time.Now().Add(-time.Minute).UnixMilli() || stamp > time.Now().Add(time.Minute).UnixMilli() || values.Get("sign") != hex.EncodeToString(h.Sum(nil)) || values.Get("access_key") != "test-only-vivo-key" || values.Get("target_app_key") != "developer" || values.Get("v") != "1.0" || values.Get("format") != "json" || values.Get("sign_method") != "HMAC-SHA256" || values.Get("packageName") != "com.company.feedback" {
			t.Error("vivo signature, identity or protocol fields differ from official contract")
		}
		method := values.Get("method")
		switch method {
		case "app.query.details":
			s.queries++
			if s.queryHook != nil {
				s.queryHook(s.queries)
			}
		case "app.upload.apk.app", "app.upload.icon", "app.upload.screenshot":
			s.uploads++
			if !bytes.Equal(file, s.files[filename]) || len(file) == 0 {
				t.Error("vivo did not upload the reviewed staged bytes", filename)
			}
			if s.uploadHook != nil {
				s.uploadHook(s.uploads)
			}
		case "app.sync.update.app":
			s.submits++
			version := "42"
			if s.versionCode != 0 {
				version = strconv.FormatInt(s.versionCode, 10)
			}
			if file != nil || values.Get("apk") == "" || values.Get("versionCode") != version || values.Get("fileMd5") != hex.EncodeToString(apkMD5[:]) || values.Get("onlineType") != "1" || values.Get("privateSelfCheck") != s.metadata["privateSelfCheck"] || values.Get("updateDesc") != s.metadata["updateDesc"] || values.Get("compatibleDevice") != "1" {
				t.Error("vivo update lost reviewed material or APK binding")
			}
			if s.metadata["icon"] != nil && (values.Get("apk") != "serial-1" || values.Get("icon") != "serial-2" || values.Get("screenshot") != "serial-3,serial-4,serial-5" || values.Get("testAccount") != `[{"testAccount":"reviewer","testPassword":"test-only-account-password"}]` || values.Get("rateAge") != "3") {
				t.Error("vivo update substituted or omitted reviewed attachment/account fields")
			}
			if s.submitHook != nil {
				s.submitHook()
			}
		default:
			t.Error("unsupported vivo method", method)
			w.WriteHeader(404)
			return
		}
		if s.failureMethod == method {
			if s.failureBody == "lost" {
				conn, _, err := w.(http.Hijacker).Hijack()
				if err == nil {
					_ = conn.Close()
				}
			} else if s.failureBody == "redirect" {
				w.Header().Set("Location", "https://unapproved.example/credentials")
				w.WriteHeader(307)
			} else {
				_, _ = io.WriteString(w, s.failureBody)
			}
			return
		}
		reply := map[string]any{"code": 0, "subCode": "0", "msg": "untrusted result text"}
		if method == "app.query.details" {
			reply["data"] = s.details
		} else if file != nil {
			data := map[string]any{"packageName": "com.company.feedback", "serialnumber": fmt.Sprintf("serial-%d", s.uploads)}
			if method == "app.upload.apk.app" {
				data["versionCode"], data["versionName"], data["fileMd5"] = 42, "1.0.0", hex.EncodeToString(apkMD5[:])
				if s.versionCode != 0 {
					data["versionCode"], data["versionName"] = s.versionCode, s.versionName
				}
				if values.Get("fileMd5") != hex.EncodeToString(apkMD5[:]) {
					t.Error("APK MD5 does not cover staged bytes")
				}
			}
			for k, v := range s.uploadOverrides {
				data[k] = v
			}
			reply["data"] = data
		}
		_ = json.NewEncoder(w).Encode(reply)
	}))
	t.Cleanup(server.Close)
	local, _ := url.Parse(server.URL)
	transport := server.Client().Transport.(*http.Transport).Clone()
	transport.TLSClientConfig.ServerName = server.Certificate().DNSNames[0]
	transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if addr != "developer-api.vivo.com.cn:443" && addr != "sandbox-developer-api.vivo.com.cn:443" {
			t.Error("unapproved vivo outbound authority", addr)
			return nil, io.ErrUnexpectedEOF
		}
		return (&net.Dialer{}).DialContext(ctx, network, local.Host)
	}
	t.Cleanup(transport.CloseIdleConnections)
	s.transport = transport
	e, err := Open(c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	configureVivoFixture(t, e, r, s)
	s.engine = e
	return c, r, e, s
}

func TestVivoProductionAndSandboxVersionNamespacesStayIndependent(t *testing.T) {
	c, r, e, s := vivoFixture(t)
	vivoProfileField(t, &c, "environment", "sandbox")
	e.Config = c
	if _, err := e.Execute(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	vivoProfileField(t, &c, "environment", "production")
	e.Config = c
	r.OperationKey = strings.Repeat("d", 64)
	// Upstream fixtures for each independent environment both start at 41.
	s.metadata = map[string]any{"updateDesc": "完善客户反馈处理流程", "compatibleDevice": 1, "privateSelfCheck": "https://company.example/privacy?a=1&b=中文"}
	vivoRewriteMaterial(t, &c, s)
	e.Config = c
	result, err := e.Execute(context.Background(), r)
	if err != nil || result.ExternalID != "vivo:production:com.company.feedback:42" || vivoRows(t, e, "vivo_versions") != 2 || vivoRows(t, e, "vivo_destinations") != 0 || s.submits != 2 {
		t.Fatal("sandbox reserved a production version", result, err)
	}
	if s.seenHosts[0] != "sandbox-developer-api.vivo.com.cn" || s.seenHosts[len(s.seenHosts)-1] != "developer-api.vivo.com.cn" {
		t.Fatal("environment did not select distinct fixed origins")
	}
}

func TestVivoReadOnlyFailureLeavesAcceptedJournalUnchanged(t *testing.T) {
	for _, body := range []string{"lost", `{"code":0,"subCode":"11001"}`, `{"code":0,"subCode":"0","data":{"packageName":"com.company.feedback","status":2,"Status":4}}`} {
		t.Run(body, func(t *testing.T) {
			_, r, e, s := vivoFixture(t)
			result, err := e.Execute(context.Background(), r)
			if err != nil {
				t.Fatal(err)
			}
			before, err := e.Inspect(context.Background(), r.OperationKey)
			if err != nil {
				t.Fatal(err)
			}
			beforeJSON, _ := json.Marshal(before)
			s.failureMethod, s.failureBody = "app.query.details", body
			r.Action, r.ExternalID = "status", result.ExternalID
			if _, err = e.Execute(context.Background(), r); err == nil {
				t.Fatal("invalid status response confirmed observation")
			}
			after, err := e.Inspect(context.Background(), r.OperationKey)
			if err != nil {
				t.Fatal(err)
			}
			afterJSON, _ := json.Marshal(after)
			if !bytes.Equal(beforeJSON, afterJSON) || s.uploads != 5 || s.submits != 1 {
				t.Fatal("read-only query mutated accepted journal or channel")
			}
		})
	}
}

func configureVivoFixture(t *testing.T, e *Engine, r Request, s *vivoFixtureState) {
	t.Helper()
	e.HTTP.Transport = s.transport
	e.verifyAndroidPackage = func(_ context.Context, p Profile, request Request, name string) (androidPackage, error) {
		b, err := os.ReadFile(name)
		if err != nil || !bytes.Equal(b, s.files["release.apk"]) || p.BundleID != "com.company.feedback" || request.Version != r.Version {
			t.Error("SDK boundary lost staged artifact binding")
			return androidPackage{}, fmt.Errorf("invalid SDK boundary")
		}
		return androidPackage{Version: "1.0.0", VersionCode: 42}, nil
	}
	jar, _ := cookiejar.New(nil)
	for _, host := range []string{"developer-api.vivo.com.cn", "sandbox-developer-api.vivo.com.cn"} {
		u, _ := url.Parse("https://" + host)
		jar.SetCookies(u, []*http.Cookie{{Name: "unrelated", Value: "do-not-forward"}})
	}
	e.HTTP.Jar = jar
}

func TestVivoReviewedUpdateWithSignedByteExactUploads(t *testing.T) {
	_, r, e, s := vivoFixture(t)
	result, err := e.Execute(context.Background(), r)
	if err != nil || result.State != "awaiting_review" || result.ExternalID != "vivo:production:com.company.feedback:42" {
		t.Fatalf("vivo update: %+v %v", result, err)
	}
	if s.queries != 2 || s.uploads != 5 || s.submits != 1 {
		t.Fatal("vivo update did not recheck prerequisites and submit every reviewed file exactly once", s.queries, s.uploads, s.submits)
	}
	op, err := e.Inspect(context.Background(), r.OperationKey)
	if err != nil || op.State != "complete" || op.Result == nil || *op.Result != result {
		t.Fatal("confirmed vivo update not durably recorded", err)
	}
	raw, _ := json.Marshal(op)
	for _, secret := range []string{"test-only-vivo-secret", "test-only-account-password", "untrusted result text", "untrusted text"} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatal("sensitive upstream/material content persisted in operation")
		}
	}
	if err := os.Remove(r.ArtifactPath); err != nil {
		t.Fatal(err)
	}
	replay, err := e.Execute(context.Background(), r)
	if err != nil || replay != result || s.queries != 2 || s.uploads != 5 || s.submits != 1 {
		t.Fatal("confirmed vivo operation replayed network writes", err)
	}
}

func vivoProfileField(t *testing.T, c *Config, key string, value any) {
	t.Helper()
	b, _ := json.Marshal(c.Profiles[0])
	var fields map[string]any
	_ = json.Unmarshal(b, &fields)
	fields[key] = value
	b, _ = json.Marshal(fields)
	if err := json.Unmarshal(b, &c.Profiles[0]); err != nil {
		t.Fatal(err)
	}
}

func vivoRewriteMaterial(t *testing.T, c *Config, s *vivoFixtureState) {
	t.Helper()
	b, _ := json.Marshal(s.metadata)
	if err := os.WriteFile(s.materialPath, b, 0600); err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(b)
	c.Profiles[0].MetadataDigest = hex.EncodeToString(h[:])
}

func vivoRows(t *testing.T, e *Engine, table string) int {
	t.Helper()
	var n int
	if err := e.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestVivoProfilesMaterialsAndCredentialsFailBeforeNetwork(t *testing.T) {
	for _, kind := range []string{"environment-empty", "environment-url", "sandbox-typo", "wrong-target", "wrong-package", "bundle-mismatch", "signer", "relative-sdk", "relative-credential", "relative-material", "unreviewed-material", "upload-host", "foreign-track", "foreign-channel", "duplicate-destination", "manifest-changed", "unknown-field", "short-update", "long-update", "short-detail", "wrong-device", "wrong-age", "http-privacy", "url-credentials", "too-many-accounts", "bad-account", "screenshot-count", "image-hash", "image-size", "image-dimensions", "icon-format", "truncated-image", "manifest-permissions", "credential-permissions", "bad-credential", "credential-duplicate", "wrong-sdk", "artifact-digest"} {
		t.Run(kind, func(t *testing.T) {
			c, r, e, s := vivoFixture(t)
			invalidProfile := true
			switch kind {
			case "environment-empty":
				vivoProfileField(t, &c, "environment", "")
			case "environment-url":
				vivoProfileField(t, &c, "environment", "https://evil.example")
			case "sandbox-typo":
				vivoProfileField(t, &c, "environment", "Sandbox")
			case "wrong-target":
				c.Profiles[0].Target = "android-oppo"
			case "wrong-package":
				c.Profiles[0].AppID = "invalid"
			case "bundle-mismatch":
				c.Profiles[0].BundleID = "com.other.application"
			case "signer":
				c.Profiles[0].ExpectedSigner = "debug"
			case "relative-sdk":
				c.Profiles[0].AAPTPath = "aapt"
			case "relative-credential":
				c.Profiles[0].CredentialsFile = "private.json"
			case "relative-material":
				c.Profiles[0].MetadataFile = "material.json"
			case "unreviewed-material":
				c.Profiles[0].MetadataDigest = ""
			case "upload-host":
				c.Profiles[0].UploadHosts = []string{"arbitrary.example"}
			case "foreign-track":
				c.Profiles[0].Track = "production"
			case "foreign-channel":
				c.Profiles[0].Channel = "stable"
			case "duplicate-destination":
				p := c.Profiles[0]
				p.GoalID = "goal_other"
				c.Profiles = append(c.Profiles, p)
			default:
				invalidProfile = false
				switch kind {
				case "manifest-changed":
					_ = os.WriteFile(s.materialPath, []byte(`{}`), 0600)
				case "unknown-field":
					s.metadata["onlineType"] = 2
				case "short-update":
					s.metadata["updateDesc"] = "abc"
				case "long-update":
					s.metadata["updateDesc"] = strings.Repeat("字", 201)
				case "short-detail":
					s.metadata["detailDesc"] = "too short"
				case "wrong-device":
					s.metadata["compatibleDevice"] = 4
				case "wrong-age":
					s.metadata["rateAge"] = 9
				case "http-privacy":
					s.metadata["privateSelfCheck"] = "http://company.example/privacy"
				case "url-credentials":
					s.metadata["privateSelfCheck"] = "https://password@company.example/privacy"
				case "too-many-accounts":
					s.metadata["testAccount"] = []any{1, 2, 3, 4, 5, 6}
				case "bad-account":
					s.metadata["testAccount"] = []any{map[string]any{"testAccount": "name", "testPassword": ""}}
				case "screenshot-count":
					s.metadata["screenshots"] = s.metadata["screenshots"].([]any)[:1]
				case "image-hash":
					s.metadata["icon"].(map[string]any)["sha256"] = strings.Repeat("f", 64)
				case "image-size":
					s.metadata["icon"].(map[string]any)["bytes"] = 500 << 10
				case "image-dimensions":
					asset := s.metadata["icon"].(map[string]any)
					body := oppoTestPicture(t, 256, 257)
					_ = os.WriteFile(asset["path"].(string), body, 0600)
					h := sha256.Sum256(body)
					asset["sha256"], asset["bytes"] = hex.EncodeToString(h[:]), len(body)
				case "icon-format":
					s.metadata["icon"].(map[string]any)["path"] = r.ArtifactPath
				case "truncated-image":
					asset := s.metadata["icon"].(map[string]any)
					body := s.files["icon.png"][:50]
					_ = os.WriteFile(asset["path"].(string), body, 0600)
					h := sha256.Sum256(body)
					asset["sha256"], asset["bytes"] = hex.EncodeToString(h[:]), len(body)
				case "manifest-permissions":
					_ = os.Chmod(s.materialPath, 0644)
				case "credential-permissions":
					_ = os.Chmod(c.Profiles[0].CredentialsFile, 0644)
				case "bad-credential":
					_ = os.WriteFile(c.Profiles[0].CredentialsFile, []byte(`{"accessKey":"","accessSecret":""}`), 0600)
				case "credential-duplicate":
					_ = os.WriteFile(c.Profiles[0].CredentialsFile, []byte(`{"accessKey":"key","accessSecret":"one","accessSecret":"two"}`), 0600)
				case "wrong-sdk":
					e.verifyAndroidPackage = func(context.Context, Profile, Request, string) (androidPackage, error) {
						return androidPackage{}, fmt.Errorf("synthetic SDK rejection")
					}
				case "artifact-digest":
					r.ArtifactDigest = strings.Repeat("f", 64)
				}
				if kind != "manifest-changed" {
					vivoRewriteMaterial(t, &c, s)
				}
			}
			if invalidProfile {
				other, err := Open(c)
				if err == nil {
					_ = other.Close()
					t.Fatal("invalid vivo publishing destination accepted")
				}
			} else {
				e.Config = c
				if _, err := e.Execute(context.Background(), r); err == nil {
					t.Fatal("unapproved vivo material reached the network")
				}
			}
			if s.queries+s.uploads+s.submits != 0 || vivoRows(t, e, "operations") != 0 || vivoRows(t, e, "vivo_destinations") != 0 || vivoRows(t, e, "vivo_versions") != 0 {
				t.Fatal("invalid local input made a network call or consumed durable ownership")
			}
		})
	}
}

func TestVivoUnsupportedOrBusyApplicationNeverUploads(t *testing.T) {
	for _, kind := range []string{"other-package", "game", "missing-type", "old-version", "equal-version", "bad-version", "draft", "reviewing", "awaiting-release", "unknown-status", "unknown-sale", "missing-version", "missing-size"} {
		t.Run(kind, func(t *testing.T) {
			_, r, e, s := vivoFixture(t)
			switch kind {
			case "other-package":
				s.details["packageName"] = "com.other.app"
			case "game":
				s.details["appType"] = 2
			case "missing-type":
				delete(s.details, "appType")
			case "old-version":
				s.details["versionCode"] = 43
			case "equal-version":
				s.details["versionCode"] = 42
			case "bad-version":
				s.details["versionCode"] = 0
			case "draft":
				s.details["status"] = 1
			case "reviewing":
				s.details["status"] = 2
			case "awaiting-release":
				s.details["status"], s.details["saleStatus"] = 3, 0
			case "unknown-status":
				s.details["status"] = 6
			case "unknown-sale":
				s.details["saleStatus"] = 6
			case "missing-version":
				delete(s.details, "versionName")
			case "missing-size":
				delete(s.details, "apkSize")
			}
			if _, err := e.Execute(context.Background(), r); err == nil || s.uploads+s.submits != 0 || vivoRows(t, e, "operations") != 0 {
				t.Fatal("busy/unrelated/incomplete vivo application reached mutation", err)
			}
		})
	}
}

func TestVivoUncertainWriteCannotReplayAcrossKeysOrReopen(t *testing.T) {
	for _, method := range []string{"app.upload.apk.app", "app.upload.icon", "app.upload.screenshot", "app.sync.update.app"} {
		for _, failure := range []string{"lost", "redirect", `{"code":500,"msg":"private upstream detail"}`, `{"code":0,"subCode":"21002","msg":"private upstream detail"}`, `{"code":0}`, `{"code":0,"subCode":"0","subCode":"21002"}`, `{"code":500,"Code":0,"subCode":"0"}`, `{"code":0,"subCode":"0"} {}`, `{"code":0,"subCode":"0"`} {
			t.Run(method+"/"+failure, func(t *testing.T) {
				c, r, e, s := vivoFixture(t)
				s.failureMethod, s.failureBody = method, failure
				if result, err := e.Execute(context.Background(), r); err == nil || result.ExternalID != "" || strings.Contains(err.Error(), "private upstream detail") {
					t.Fatal("uncertain response confirmed or exposed arbitrary upstream text", result, err)
				}
				before := s.queries + s.uploads + s.submits
				op, err := e.Inspect(context.Background(), r.OperationKey)
				if err != nil || op.State != "pending" || op.Result != nil || vivoRows(t, e, "vivo_destinations") != 1 || vivoRows(t, e, "vivo_versions") != 1 {
					t.Fatal("unknown vivo write lost its durable reservation", err)
				}
				if _, err = e.Execute(context.Background(), r); err == nil || before != s.queries+s.uploads+s.submits {
					t.Fatal("same-key unknown operation made a network request", err)
				}
				_ = e.Close()
				reopened, err := Open(c)
				if err != nil {
					t.Fatal(err)
				}
				defer reopened.Close()
				configureVivoFixture(t, reopened, r, s)
				s.engine = reopened
				for _, key := range []string{r.OperationKey, strings.Repeat("d", 64)} {
					r.OperationKey = key
					if _, err = reopened.Execute(context.Background(), r); err == nil {
						t.Fatal("reopen/new key replayed uncertain write")
					}
				}
				if s.queries+s.uploads+s.submits != before || vivoRows(t, reopened, "operations") != 1 || vivoRows(t, reopened, "vivo_versions") != 1 {
					t.Fatal("uncertain operation replaced or mutated")
				}
			})
		}
	}
}

func TestVivoChangedAuthorityAndUnknownStatusFailLocally(t *testing.T) {
	for _, kind := range []string{"unknown-status", "continue", "binding", "commit", "artifact", "release", "metadata", "environment", "external-id", "missing-external-id"} {
		t.Run(kind, func(t *testing.T) {
			_, r, e, s := vivoFixture(t)
			if kind == "unknown-status" {
				r.Action, r.ExternalID = "status", "vivo:production:com.company.feedback:42"
			} else {
				result, err := e.Execute(context.Background(), r)
				if err != nil {
					t.Fatal(err)
				}
				r.Action, r.ExternalID = "status", result.ExternalID
				switch kind {
				case "continue":
					r.Action = "continue"
				case "binding":
					r.Binding = strings.Repeat("e", 64)
				case "commit":
					r.Commit = strings.Repeat("2", 40)
				case "artifact":
					r.ArtifactDigest = strings.Repeat("f", 64)
				case "release":
					r.ReleaseID = "release_other"
				case "metadata":
					e.Config.Profiles[0].MetadataDigest = strings.Repeat("f", 64)
				case "environment":
					e.Config.Profiles[0].Environment = "sandbox"
				case "external-id":
					r.ExternalID = "vivo:production:com.company.feedback:43"
				case "missing-external-id":
					r.ExternalID = ""
				}
			}
			before := s.queries + s.uploads + s.submits
			if _, err := e.Execute(context.Background(), r); err == nil || before != s.queries+s.uploads+s.submits {
				t.Fatal("changed authority or unknown operation reached the network", err)
			}
		})
	}
}

func TestVivoStagedMaterialSurvivesSourceMutation(t *testing.T) {
	_, r, e, s := vivoFixture(t)
	s.queryHook = func(n int) {
		if n == 1 {
			_ = os.WriteFile(r.ArtifactPath, []byte("changed after approval"), 0600)
			_ = os.WriteFile(s.metadata["icon"].(map[string]any)["path"].(string), []byte("changed after approval"), 0600)
			_ = os.WriteFile(s.materialPath, []byte(`{}`), 0600)
		}
	}
	if result, err := e.Execute(context.Background(), r); err != nil || result.State != "awaiting_review" || s.uploads != 5 {
		t.Fatal("source mutation changed reviewed outgoing bytes", result, err)
	}
}

func TestVivoNewCandidateAfterObservedRejectionAndMinimalMaterial(t *testing.T) {
	c, r, e, s := vivoFixture(t)
	if _, err := e.Execute(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	s.details["versionCode"], s.details["versionName"], s.details["apkSize"], s.details["status"] = 42, "1.0.0", r.ArtifactSize, 4
	s.versionCode, s.versionName = 43, "1.1.0"
	r.OperationKey, r.Version = strings.Repeat("d", 64), "1.1.0"
	s.metadata = map[string]any{"updateDesc": "修复已拒审版本的问题", "compatibleDevice": 1, "privateSelfCheck": "https://company.example/privacy?a=1&b=中文"}
	vivoRewriteMaterial(t, &c, s)
	e.Config = c
	e.verifyAndroidPackage = func(context.Context, Profile, Request, string) (androidPackage, error) {
		return androidPackage{Version: "1.1.0", VersionCode: 43}, nil
	}
	result, err := e.Execute(context.Background(), r)
	if err != nil || result.ExternalID != "vivo:production:com.company.feedback:43" || s.uploads != 6 || s.submits != 2 || vivoRows(t, e, "vivo_versions") != 2 || vivoRows(t, e, "vivo_destinations") != 0 {
		t.Fatal("observed rejection could not accept a newly approved higher version", result, err)
	}
}

func TestVivoIntentMustStillOwnOperationAtEachExternalWrite(t *testing.T) {
	for _, change := range []string{"delete-operation", "replace-request-hash", "replace-lock"} {
		t.Run(change, func(t *testing.T) {
			_, r, e, s := vivoFixture(t)
			s.queryHook = func(n int) {
				if n == 2 {
					statement := `DELETE FROM operations WHERE operation_key=?`
					if change == "replace-request-hash" {
						statement = `UPDATE operations SET request_hash='changed' WHERE operation_key=?`
					}
					if change == "replace-lock" {
						statement = `UPDATE vivo_destinations SET operation_key='changed' WHERE operation_key=?`
					}
					if _, err := e.db.Exec(statement, r.OperationKey); err != nil {
						t.Error(err)
					}
				}
			}
			if _, err := e.Execute(context.Background(), r); err == nil || s.uploads+s.submits != 0 {
				t.Fatal("external write started after durable ownership changed", err, s.uploads, s.submits)
			}
		})
	}
}

func TestVivoUploadReceiptMustMatchApprovedPackage(t *testing.T) {
	for key, value := range map[string]any{"packageName": "com.other.app", "serialnumber": "unsafe,serial", "versionCode": 43, "versionName": "1.0.1", "fileMd5": strings.Repeat("f", 32)} {
		t.Run(key, func(t *testing.T) {
			_, r, e, s := vivoFixture(t)
			s.uploadOverrides = map[string]any{key: value}
			if _, err := e.Execute(context.Background(), r); err == nil || s.uploads != 1 || s.submits != 0 || vivoRows(t, e, "vivo_destinations") != 1 {
				t.Fatal("unrelated upload receipt allowed update", err)
			}
		})
	}
}

func TestVivoCheckpointsAndFinalSQLFailClosed(t *testing.T) {
	for _, phase := range []string{"first-intent", "second-intent", "submit-intent", "complete", "unlock"} {
		t.Run(phase, func(t *testing.T) {
			_, r, e, s := vivoFixture(t)
			if phase == "first-intent" {
				_, err := e.db.Exec(`CREATE TRIGGER vivo_abort BEFORE UPDATE ON operations WHEN NEW.state='pending' BEGIN SELECT RAISE(ABORT,'fixture'); END`)
				if err != nil {
					t.Fatal(err)
				}
			} else if phase == "second-intent" || phase == "submit-intent" {
				s.uploadHook = func(n int) {
					if (phase == "second-intent" && n == 1) || (phase == "submit-intent" && n == 5) {
						_, err := e.db.Exec(`CREATE TRIGGER vivo_abort BEFORE UPDATE ON operations WHEN NEW.state='pending' BEGIN SELECT RAISE(ABORT,'fixture'); END`)
						if err != nil {
							t.Error(err)
						}
					}
				}
			} else {
				s.submitHook = func() {
					statement := `CREATE TRIGGER vivo_abort BEFORE UPDATE ON operations WHEN NEW.state='complete' BEGIN SELECT RAISE(ABORT,'fixture'); END`
					if phase == "unlock" {
						statement = `CREATE TRIGGER vivo_abort BEFORE DELETE ON vivo_destinations BEGIN SELECT RAISE(ABORT,'fixture'); END`
					}
					if _, err := e.db.Exec(statement); err != nil {
						t.Error(err)
					}
				}
			}
			if _, err := e.Execute(context.Background(), r); err == nil {
				t.Fatal("journal failure confirmed update")
			}
			op, err := e.Inspect(context.Background(), r.OperationKey)
			if err != nil || op.State != "pending" || op.Result != nil || vivoRows(t, e, "vivo_destinations") != 1 || vivoRows(t, e, "vivo_versions") != 1 {
				t.Fatal("SQL failure lost pending ownership", err)
			}
			if phase == "first-intent" && s.uploads != 0 || phase == "second-intent" && s.uploads != 1 || phase == "submit-intent" && s.submits != 0 {
				t.Fatal("external write preceded durable intent")
			}
			before := s.queries + s.uploads + s.submits
			if _, err = e.Execute(context.Background(), r); err == nil || before != s.queries+s.uploads+s.submits {
				t.Fatal("SQL failure caused replay", err)
			}
		})
	}
}

func TestVivoConcurrentWritersAndPermanentVersionOwnership(t *testing.T) {
	c, r, e, s := vivoFixture(t)
	other, err := Open(c)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	configureVivoFixture(t, other, r, s)
	var group sync.WaitGroup
	errors := make(chan error, 2)
	for _, engine := range []*Engine{e, other} {
		group.Add(1)
		go func(engine *Engine) {
			defer group.Done()
			_, err := engine.Execute(context.Background(), r)
			errors <- err
		}(engine)
	}
	group.Wait()
	close(errors)
	for err := range errors {
		if err != nil && !strings.Contains(err.Error(), "reserved") && !strings.Contains(err.Error(), "uncertain") && !strings.Contains(err.Error(), "active") {
			t.Fatal(err)
		}
	}
	if s.submits != 1 || s.uploads != 5 || vivoRows(t, e, "operations") != 1 || vivoRows(t, e, "vivo_destinations") != 0 || vivoRows(t, e, "vivo_versions") != 1 {
		t.Fatal("concurrent writers duplicated publication")
	}
	beforeUploads := s.uploads
	r.OperationKey = strings.Repeat("d", 64)
	if _, err = e.Execute(context.Background(), r); err == nil || s.uploads != beforeUploads {
		t.Fatal("new key reused an accepted versionCode", err)
	}
	// Even when a higher candidate is used, an upstream observation older than
	// our last accepted version must not open another write window.
	e.verifyAndroidPackage = func(context.Context, Profile, Request, string) (androidPackage, error) {
		return androidPackage{Version: "1.0.0", VersionCode: 43}, nil
	}
	if _, err = e.Execute(context.Background(), r); err == nil || s.uploads != beforeUploads {
		t.Fatal("stale upstream observation admitted a second update", err)
	}
}

func TestVivoStatusRequiresExactVersionAndManualPublicationProof(t *testing.T) {
	for _, sample := range []struct {
		name         string
		status, sale int
		change       string
		want         string
		manual       bool
	}{
		{"review", 2, 1, "", "awaiting_review", false}, {"reject", 4, 1, "", "rejected", false},
		{"ready", 3, 0, "", "awaiting_release", false}, {"offline", 3, 2, "", "halted", false},
		{"on-sale-not-exact-proof", 3, 1, "", "unchanged", true}, {"withdrawn", 5, 1, "", "unchanged", true},
		{"draft", 1, 1, "", "unchanged", true}, {"unknown", 8, 1, "", "unchanged", true},
		{"old-live-version", 3, 1, "versionCode", "unchanged", true}, {"wrong-name", 2, 1, "versionName", "unchanged", true},
		{"wrong-size", 2, 1, "apkSize", "unchanged", true}, {"wrong-package", 2, 1, "packageName", "unchanged", true},
		{"missing-status", 2, 1, "status", "unchanged", true}, {"unknown-sale", 3, 8, "", "unchanged", true},
	} {
		t.Run(sample.name, func(t *testing.T) {
			_, r, e, s := vivoFixture(t)
			result, err := e.Execute(context.Background(), r)
			if err != nil {
				t.Fatal(err)
			}
			s.details["versionCode"], s.details["versionName"], s.details["apkSize"] = 42, "1.0.0", r.ArtifactSize
			s.details["status"], s.details["saleStatus"] = sample.status, sample.sale
			switch sample.change {
			case "versionCode":
				s.details["versionCode"] = 41
			case "versionName":
				s.details["versionName"] = "0.9.0"
			case "apkSize":
				s.details["apkSize"] = 1
			case "packageName":
				s.details["packageName"] = "com.other.app"
			case "status":
				delete(s.details, "status")
			}
			r.Action, r.ExternalID = "status", result.ExternalID
			before := s.uploads + s.submits
			observed, err := e.Execute(context.Background(), r)
			if err != nil || observed.State != sample.want || observed.ExternalID != result.ExternalID || s.uploads+s.submits != before {
				t.Fatal("invalid vivo observation", observed, err)
			}
			if sample.manual != (observed.UpstreamState == "MANUAL_VERIFICATION_REQUIRED") || strings.Contains(observed.Reason, "untrusted") {
				t.Fatal("ambiguous observation not routed to manual proof", observed)
			}
		})
	}
}

func TestVivoSandboxNeverUsesProductionDestination(t *testing.T) {
	c, r, e, s := vivoFixture(t)
	vivoProfileField(t, &c, "environment", "sandbox")
	e.Config = c
	result, err := e.Execute(context.Background(), r)
	if err != nil || result.ExternalID != "vivo:sandbox:com.company.feedback:42" {
		t.Fatal(result, err)
	}
	for _, host := range s.seenHosts {
		if host != "sandbox-developer-api.vivo.com.cn" {
			t.Fatal("sandbox contacted production", host)
		}
	}
	s.details["versionCode"], s.details["versionName"], s.details["apkSize"], s.details["status"], s.details["saleStatus"] = 42, "1.0.0", r.ArtifactSize, 3, 1
	r.Action, r.ExternalID = "status", result.ExternalID
	observed, err := e.Execute(context.Background(), r)
	if err != nil || observed.State != "unchanged" || observed.UpstreamState != "MANUAL_VERIFICATION_REQUIRED" {
		t.Fatal("sandbox became production publication", observed, err)
	}
	// An environment change cannot reuse the same operation or its external ID.
	vivoProfileField(t, &c, "environment", "production")
	e.Config = c
	before := len(s.seenHosts)
	if _, err = e.Execute(context.Background(), r); err == nil || len(s.seenHosts) != before {
		t.Fatal("sandbox receipt crossed environment", err)
	}
}

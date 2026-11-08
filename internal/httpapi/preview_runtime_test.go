package httpapi

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	p "github.com/intelligence-substrate/platform/internal/platform"
	"github.com/intelligence-substrate/platform/internal/previewnode"
	"github.com/intelligence-substrate/platform/internal/webpreview"
)

func previewBackendArtifact(t *testing.T, f previewHTTPFixture) p.Resource {
	t.Helper()
	binary := []byte("backend fixture")
	sum := sha256.Sum256(binary)
	manifest, _ := json.Marshal(webpreview.Runtime{SchemaVersion: 1, Profile: "flutter-go-v1", Mode: "feedback", Goarch: "arm64", BackendSHA256: hex.EncodeToString(sum[:]), BackendSize: int64(len(binary))})
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	for n, v := range map[string][]byte{"index.html": []byte("<main>actual entry</main>"), "substrate-preview/backend": binary, "substrate-preview/manifest.json": manifest} {
		if err := tw.WriteHeader(&tar.Header{Name: n, Mode: 0644, Size: int64(len(v))}); err != nil {
			t.Fatal(err)
		}
		tw.Write(v)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	artifact, err := f.h.Service.Upload(context.Background(), f.a, p.M{"goalId": f.goal.ID, "repositoryId": p.S(f.artifact.Data, "repositoryId"), "commit": p.S(f.artifact.Data, "commit"), "target": "web", "type": "package", "name": "backend.tar"}, bytes.NewReader(b.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	return artifact
}
func TestPreviewRuntimeRequiresConfiguredNodeBeforeCreatingResource(t *testing.T) {
	f := makePreviewHTTPFixture(t)
	artifact := previewBackendArtifact(t, f)
	_, err := f.h.Service.Command(context.Background(), f.a, "previews.create", p.M{"artifactId": artifact.ID, "expectedRequirementRevision": 1, "title": "Full preview", "reason": "Business verification", "key": "with-backend"})
	if err == nil {
		t.Fatal("runtime preview accepted without isolated node")
	}
}

type previewAPIDriver struct {
	mu                    sync.Mutex
	starts, writes, stops int
	last                  previewnode.Request
	fail                  bool
	onStart               func()
}

func (d *previewAPIDriver) Start(context.Context, previewnode.Binding, []byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.starts++
	if d.onStart != nil {
		d.onStart()
	}
	return nil
}

func TestPreviewRuntimeRevocationDuringStartupPreventsBusinessWrite(t *testing.T) {
	f, d, _ := runtimeHTTPFixture(t)
	link := f.access(t)
	started := make(chan struct{})
	release := make(chan struct{})
	d.onStart = func() { close(started); <-release }
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		r := httptest.NewRequest("POST", link+"api/feedback", strings.NewReader(`{"title":"one"}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "null")
		w := httptest.NewRecorder()
		f.h.PreviewGateway().ServeHTTP(w, r)
		done <- w
	}()
	<-started
	authorityCommand(t, f.h, f.a, "previews.stop", p.M{"id": f.preview.ID, "expectedRevision": 1, "reason": "Stopped during cold startup", "key": "stop-during-start"})
	close(release)
	w := <-done
	if w.Code != 404 || d.writes != 0 {
		t.Fatal("revoked startup performed business write", w.Code, d.writes)
	}
	if err := f.h.ReconcilePreviewRuntimes(context.Background()); err != nil {
		t.Fatal(err)
	}
	if d.stops != 1 {
		t.Fatal("revoked cold-start resource retained")
	}
}
func (d *previewAPIDriver) Stop(context.Context, previewnode.Binding) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.stops++
	return nil
}
func (d *previewAPIDriver) Request(_ context.Context, _ previewnode.Binding, q previewnode.Request) (previewnode.Response, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.last = q
	if q.Method == "POST" {
		d.writes++
	}
	if d.fail {
		return previewnode.Response{}, errors.New("response lost")
	}
	return previewnode.Response{Status: 201, Body: []byte(`{"id":"business-record"}`)}, nil
}

func TestPreviewRuntimeFailureRemainsReadableWithoutRetryingWrite(t *testing.T) {
	f, d, _ := runtimeHTTPFixture(t)
	link := f.access(t)
	d.mu.Lock()
	d.fail = true
	d.mu.Unlock()
	r := httptest.NewRequest("POST", link+"api/feedback", strings.NewReader(`{"title":"one"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "null")
	w := httptest.NewRecorder()
	f.h.PreviewGateway().ServeHTTP(w, r)
	if w.Code != 502 || w.Header().Get("Access-Control-Allow-Origin") != "null" || !strings.Contains(w.Body.String(), "结果不明") || d.writes != 1 {
		t.Fatal("lost write response unreadable or replayed", w.Code, w.Header(), d.writes)
	}
}
func runtimeHTTPFixture(t *testing.T) (previewHTTPFixture, *previewAPIDriver, *previewnode.Engine) {
	t.Helper()
	f := makePreviewHTTPFixture(t)
	dir, err := os.MkdirTemp("/tmp", "ispv-http-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	d := &previewAPIDriver{}
	engine, err := previewnode.Open(dir, d, 2)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", filepath.Join(dir, "node.sock"))
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: previewnode.NewHandler(engine)}
	go server.Serve(listener)
	t.Cleanup(func() { server.Close() })
	if err = f.h.ConfigurePreviewNode(filepath.Join(dir, "node.sock")); err != nil {
		t.Fatal(err)
	}
	artifact := previewBackendArtifact(t, f)
	f.artifact = artifact
	f.preview = authorityCommand(t, f.h, f.a, "previews.create", p.M{"artifactId": artifact.ID, "expectedRequirementRevision": 1, "title": "Full business preview", "reason": "Verify isolated backend", "key": "full"})
	return f, d, engine
}
func TestPreviewRuntimeGatewayExactJSONNoCredentialsAndRevocation(t *testing.T) {
	f, d, engine := runtimeHTTPFixture(t)
	link := f.access(t)
	call := func(method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, link+path, strings.NewReader(body))
		for k, v := range headers {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		f.h.PreviewGateway().ServeHTTP(w, r)
		return w
	}
	headers := map[string]string{"Origin": "null", "Content-Type": "application/json"}
	w := call("OPTIONS", "api/feedback", "", map[string]string{"Origin": "null", "Access-Control-Request-Method": "POST", "Access-Control-Request-Headers": "content-type"})
	if w.Code != 204 || w.Header().Get("Access-Control-Allow-Origin") != "null" {
		t.Fatal("opaque origin preflight denied", w.Code, w.Body.String())
	}
	w = call("POST", "api/feedback", `{"title":"one"}`, headers)
	if w.Code != 201 || !strings.Contains(w.Body.String(), "business-record") || w.Header().Get("Set-Cookie") != "" || w.Header().Get("Access-Control-Allow-Credentials") != "" {
		t.Fatal("business request failed", w.Code, w.Header(), w.Body.String())
	}
	if d.writes != 1 || d.starts != 1 || d.last.Path != "/api/feedback" {
		t.Fatal("wrong business routing")
	}
	for _, name := range []string{"files/substrate-preview/backend", "files/substrate-preview/manifest.json"} {
		if w = call("GET", name, "", nil); w.Code != 404 {
			t.Fatal("runtime member exposed", name, w.Code)
		}
	}
	for _, path := range []string{"api/feedback?other=1", "api/slots", "api/../feedback", "api/%2e%2e/feedback", "api/bookings", "api/v1/projects"} {
		if w = call("POST", path, `{}`, headers); w.Code < 400 {
			t.Fatal("invalid API path accepted", path)
		}
	}
	for _, extra := range []map[string]string{{"Authorization": "Bearer private"}, {"Cookie": "is_session=private"}, {"Origin": "https://external.test"}, {"Content-Type": "text/plain"}} {
		h := map[string]string{}
		for k, v := range headers {
			h[k] = v
		}
		for k, v := range extra {
			h[k] = v
		}
		if w = call("POST", "api/feedback", `{}`, h); w.Code < 400 {
			t.Fatal("forbidden request accepted")
		}
	}
	if w = call("POST", "api/feedback", strings.Repeat("x", 16385), headers); w.Code < 400 {
		t.Fatal("oversized body accepted")
	}
	if d.writes != 1 {
		t.Fatal("rejected request wrote to backend", d.writes)
	}
	authorityCommand(t, f.h, f.a, "previews.stop", p.M{"id": f.preview.ID, "expectedRevision": 1, "reason": "Finished", "key": "stop-runtime"})
	if w = call("POST", "api/feedback", `{}`, headers); w.Code != 404 {
		t.Fatal("stopped preview still writes", w.Code)
	}
	if err := f.h.ReconcilePreviewRuntimes(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(engine.List()) != 0 || d.stops != 1 {
		t.Fatal("stopped backend not reclaimed")
	}
}

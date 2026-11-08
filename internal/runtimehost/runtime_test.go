package runtimehost

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/intelligence-substrate/platform/internal/publisher"
)

func testConfig(t *testing.T) Config {
	t.Helper()
	return Config{InstanceID: "runtime_fixture", StateDir: t.TempDir(), ProjectID: "project_test", GoalID: "goal_test", AppID: "feedback", PublicURL: "https://feedback.example.test", DockerBinary: "/usr/bin/docker", DockerHost: "unix:///var/run/docker.sock", ContainerPort: 8080, HealthPath: "/api/health", DataContract: "feedback-v1", PersistentData: true, MemoryMB: 128, CPUs: 1}
}

func TestRuntimeRejectsMalformedCommitBeforeReservingOperation(t *testing.T) {
	c := testConfig(t)
	s, err := Open(c, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	engine := Engine{Store: s, Driver: &fixtureDriver{fail: true}}
	for _, commit := range []string{strings.Repeat("g", 40), strings.Repeat("c", 65)} {
		r := testRequest(t, c, "a")
		r.Commit = commit
		// Invalid metadata must be rejected before reserving any operation.
		r.Action = "publish"
		_, err := engine.Execute(context.Background(), r)
		if err == nil || !strings.Contains(err.Error(), "binding required") {
			t.Fatal("malformed commit reached the runtime journal", err)
		}
		if _, err = s.Inspect(context.Background(), r.OperationKey); err == nil {
			t.Fatal("invalid input reserved an operation")
		}
	}
}

type recoveringDriver struct{ port int }

func (*recoveringDriver) Load(context.Context, string, string) error { return nil }
func (d *recoveringDriver) Start(_ context.Context, dep Deployment) (Deployment, error) {
	if dep.ReleaseID == "release_d" {
		return dep, errors.New("candidate could not start")
	}
	dep.Address = "127.0.0.1"
	dep.Port = d.port
	return dep, nil
}
func (*recoveringDriver) Stop(context.Context, Deployment) error { return nil }

func TestFailedCandidateRefreshesRestoredBackendWithoutRewritingHistory(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("restored")) }))
	defer backend.Close()
	port, _ := strconv.Atoi(strings.TrimPrefix(backend.URL, "http://127.0.0.1:"))
	c := testConfig(t)
	s, err := Open(c, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	previous := Deployment{ID: strings.Repeat("a", 64), ReleaseID: "release_a", Port: 1, Address: "127.0.0.1"}
	ctx := context.Background()
	if err = s.activate(ctx, previous, ""); err != nil {
		t.Fatal(err)
	}
	before, _ := s.Active(ctx)
	engine := Engine{Store: s, Driver: &recoveringDriver{port: port}}
	if _, err = engine.Execute(ctx, testRequest(t, c, "d")); err == nil || !strings.Contains(err.Error(), "previous active version retained") {
		t.Fatal("restored backend was not checked at its actual new address", err)
	}
	after, _ := s.Active(ctx)
	if after.Port != port || after.ActivatedAt != before.ActivatedAt || after.PreviousID != before.PreviousID {
		t.Fatal("restored endpoint lost or existing activation history rewritten", after)
	}
	op, err := s.Inspect(ctx, strings.Repeat("d", 64))
	if err != nil || op.State != "failed" {
		t.Fatal("confirmed restoration remained uncertain", op, err)
	}
}

func TestReconciliationRefreshesAlreadyActiveCandidateEndpoint(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	defer backend.Close()
	port, _ := strconv.Atoi(strings.TrimPrefix(backend.URL, "http://127.0.0.1:"))
	c := testConfig(t)
	s, err := Open(c, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	dep := Deployment{ID: strings.Repeat("a", 64), ReleaseID: "release_a", Port: 1, Address: "127.0.0.1"}
	if _, err = s.reserve(ctx, dep.ID, "request"); err != nil {
		t.Fatal(err)
	}
	if err = s.intent(ctx, dep.ID, dep, ""); err != nil {
		t.Fatal(err)
	}
	if err = s.activate(ctx, dep, ""); err != nil {
		t.Fatal(err)
	}
	before, _ := s.Active(ctx)
	engine := Engine{Store: s, Driver: &recoveringDriver{port: port}}
	if _, err = engine.Reconcile(ctx, dep.ID, "candidate"); err != nil {
		t.Fatal(err)
	}
	after, _ := s.Active(ctx)
	if after.Port != port || after.PreviousID != before.PreviousID || after.ActivatedAt != before.ActivatedAt {
		t.Fatal("reconciliation retained stale endpoint or rewrote history", after)
	}
}

func TestMonitorBoundsUndeliveredHistoryAndPreservesSequence(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	defer backend.Close()
	port, _ := strconv.Atoi(strings.TrimPrefix(backend.URL, "http://127.0.0.1:"))
	c := testConfig(t)
	s, err := Open(c, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	dep := Deployment{ID: strings.Repeat("a", 64), Port: port}
	if err = s.activate(ctx, dep, ""); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`WITH RECURSIVE n(v) AS (SELECT 1 UNION ALL SELECT v+1 FROM n WHERE v<10005) INSERT INTO observations(deployment_id,body) SELECT ?,'{}' FROM n`, dep.ID); err != nil {
		t.Fatal(err)
	}
	if err = (&Engine{Store: s}).MonitorOnce(ctx); err != nil {
		t.Fatal(err)
	}
	var count, latest int
	if err = s.db.QueryRow(`SELECT COUNT(*),MAX(sequence) FROM observations`).Scan(&count, &latest); err != nil {
		t.Fatal(err)
	}
	if count != 10000 || latest != 10006 {
		t.Fatalf("outage history was unbounded or sequence rewound: count=%d latest=%d", count, latest)
	}
}

func TestRuntimeRequestBindingIncludesFixedDestination(t *testing.T) {
	c := testConfig(t)
	s, err := Open(c, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	engine := Engine{Store: s}
	request := testRequest(t, c, "a")
	original := engine.requestHash(request)
	s.Config.ContainerPort++
	if engine.requestHash(request) == original {
		t.Fatal("different application destination accepted the same operation binding")
	}
	s.Config = c
	s.Config.EnvironmentFile = "/rotated/secret.env"
	s.Config.StateDir = "/restored/state"
	s.Config.WorkerTokenFile = "/rotated/token"
	if engine.requestHash(request) != original {
		t.Fatal("directory restore or credential rotation invalidated a confirmed receipt")
	}
}

type fixtureDriver struct {
	port    int
	stopped []string
	fail    bool
}

func (d *fixtureDriver) Load(context.Context, string, string) error { return nil }
func (d *fixtureDriver) Start(_ context.Context, deployment Deployment) (Deployment, error) {
	if d.fail {
		return deployment, errors.New("container start failed")
	}
	deployment.Port = d.port
	return deployment, nil
}
func (d *fixtureDriver) Stop(_ context.Context, deployment Deployment) error {
	d.stopped = append(d.stopped, deployment.ID)
	return nil
}
func testRequest(t *testing.T, c Config, key string) Request {
	t.Helper()
	config := []byte(`{"os":"linux","config":{"Labels":{"org.intelligence-substrate.data-contract":"feedback-v1"}},"rootfs":{"type":"layers","diff_ids":[]}}`)
	file := archiveFixture(t, map[string][]byte{"manifest.json": []byte(`[{"Config":"config.json","Layers":[]}]`), "config.json": config})
	body, _ := os.ReadFile(file)
	sum := sha256.Sum256(body)
	return Request{Request: publisher.Request{ProtocolVersion: "1", Action: "publish", OperationKey: strings.Repeat(key, 64), ProjectID: c.ProjectID, GoalID: c.GoalID, ReleaseID: "release_" + key, Target: "web", Version: "1.0.0", ArtifactID: "artifact_" + key, ArtifactPath: file, ArtifactDigest: hex.EncodeToString(sum[:]), ArtifactSize: int64(len(body)), Commit: strings.Repeat("c", 40), Binding: strings.Repeat("b", 64)}}
}
func TestEngineRequiresExactActiveForRollbackAndNeverReplaysUnknown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) }))
	defer server.Close()
	port, _ := strconv.Atoi(strings.TrimPrefix(server.URL, "http://127.0.0.1:"))
	c := testConfig(t)
	s, e := Open(c, false)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	driver := &fixtureDriver{port: port}
	engine := Engine{Store: s, Driver: driver}
	ctx := context.Background()
	first := testRequest(t, c, "a")
	r, e := engine.Execute(ctx, first)
	if e != nil || r.State != "published" {
		t.Fatal(r, e)
	}
	second := testRequest(t, c, "d")
	if _, e = engine.Execute(ctx, second); e != nil {
		t.Fatal(e)
	}
	rollback := first
	rollback.Action = "rollback"
	rollback.ArtifactPath = ""
	rollback.OperationKey = strings.Repeat("e", 64)
	rollback.ExpectedActive = r.ExternalID
	rollback.RollbackTo = r.ExternalID
	if _, e = engine.Execute(ctx, rollback); e == nil {
		t.Fatal("stale rollback changed active application")
	}
	active, _ := s.Active(ctx)
	if active.ID != second.OperationKey {
		t.Fatal("stale rollback changed pointer")
	}
	rollback.OperationKey = strings.Repeat("f", 64)
	rollback.ExpectedActive = c.external(second.OperationKey)
	rolled, e := engine.Execute(ctx, rollback)
	if e != nil || rolled.State != "published" {
		t.Fatal(rolled, e)
	}
	active, _ = s.Active(ctx)
	if active.ID != rollback.OperationKey || active.ReleaseID != first.ReleaseID {
		t.Fatal("rollback did not create a distinct, traceable activation")
	}
	if _, e = engine.Execute(ctx, second); e != nil {
		t.Fatal(e)
	}
	active, _ = s.Active(ctx)
	if active.ID != rollback.OperationKey {
		t.Fatal("old publish replay replaced rollback")
	}
	unknown := testRequest(t, c, "1")
	if _, e = s.reserve(ctx, unknown.OperationKey, engine.requestHash(unknown)); e != nil {
		t.Fatal(e)
	}
	if _, e = engine.Execute(ctx, unknown); e == nil {
		t.Fatal("uncertain operation silently retried")
	}
}
func archiveFixture(t *testing.T, files map[string][]byte) string {
	t.Helper()
	var b bytes.Buffer
	w := tar.NewWriter(&b)
	for name, data := range files {
		if err := w.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(data))}); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(t.TempDir(), "image.tar")
	if err := os.WriteFile(name, b.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	return name
}
func TestArchiveStripsTagsAndVerifiesApprovedBytes(t *testing.T) {
	config := []byte(`{"architecture":"arm64","os":"linux","config":{"Labels":{"org.intelligence-substrate.data-contract":"feedback-v1"}},"rootfs":{"type":"layers","diff_ids":[]}}`)
	manifest, _ := json.Marshal([]map[string]any{{"Config": "config.json", "RepoTags": []string{"unrelated:latest"}, "Layers": []string{}}})
	name := archiveFixture(t, map[string][]byte{"manifest.json": manifest, "config.json": config, "repositories": []byte(`{"unrelated":{}}`), "index.json": []byte(`{"annotations":{"org.opencontainers.image.ref.name":"unrelated:latest"}}`)})
	body, _ := os.ReadFile(name)
	sum := sha256.Sum256(body)
	request := publisher.Request{ArtifactPath: name, ArtifactDigest: hex.EncodeToString(sum[:]), ArtifactSize: int64(len(body))}
	image, cleanup, err := prepareArchive(request, testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	expected := sha256.Sum256(config)
	if image.ID != "sha256:"+hex.EncodeToString(expected[:]) {
		t.Fatal("image identity did not bind the config bytes")
	}
	f, err := os.Open(image.Archive)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	tr := tar.NewReader(f)
	for {
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
		data, _ := io.ReadAll(tr)
		if h.Name == "repositories" || h.Name == "index.json" || bytes.Contains(data, []byte("unrelated:latest")) {
			t.Fatal("approved image import could replace an unrelated Docker tag")
		}
	}
	request.ArtifactDigest = strings.Repeat("0", 64)
	if _, clean, e := prepareArchive(request, testConfig(t)); e == nil {
		clean()
		t.Fatal("wrong approved digest accepted")
	}
}
func TestArchiveRejectsEscapeMultipleImagesAndDataContractChange(t *testing.T) {
	for _, files := range []map[string][]byte{
		{"../escape": []byte("bad")},
		{"manifest.json": []byte(`[{"Config":"a.json","Layers":[]},{"Config":"b.json","Layers":[]}]`), "a.json": []byte(`{}`), "b.json": []byte(`{}`)},
		{"manifest.json": []byte(`[{"Config":"a.json","Layers":[]}]`), "a.json": []byte(`{"os":"linux","config":{"Labels":{"org.intelligence-substrate.data-contract":"breaking-v2"}},"rootfs":{"type":"layers","diff_ids":[]}}`)},
	} {
		name := archiveFixture(t, files)
		b, _ := os.ReadFile(name)
		sum := sha256.Sum256(b)
		if _, clean, e := prepareArchive(publisher.Request{ArtifactPath: name, ArtifactDigest: hex.EncodeToString(sum[:]), ArtifactSize: int64(len(b))}, testConfig(t)); e == nil {
			clean()
			t.Fatal("unsafe image accepted")
		}
	}
}
func TestActivationCompareAndSwapAndDurableOperationReceipt(t *testing.T) {
	c := testConfig(t)
	s, e := Open(c, false)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	ctx := context.Background()
	d := Deployment{ID: strings.Repeat("a", 64), ReleaseID: "release_one", ArtifactDigest: strings.Repeat("b", 64), ImageID: "sha256:" + strings.Repeat("c", 64), Container: "container-one", Port: 18111, Version: "1.0.0"}
	if _, e = s.reserve(ctx, d.ID, "binding-one"); e != nil {
		t.Fatal(e)
	}
	if _, e = s.reserve(ctx, strings.Repeat("d", 64), "other"); e == nil {
		t.Fatal("concurrent active switch permitted")
	}
	if e = s.activate(ctx, d, "wrong-active"); e == nil {
		t.Fatal("stale activation won")
	}
	if e = s.activate(ctx, d, ""); e != nil {
		t.Fatal(e)
	}
	if e = s.finish(ctx, d.ID, Result{ExternalID: "runtime:feedback:" + d.ID, State: "published"}, ""); e != nil {
		t.Fatal(e)
	}
	if _, e = s.reserve(ctx, d.ID, "changed-binding"); e == nil {
		t.Fatal("operation binding changed")
	}
	op, e := s.reserve(ctx, d.ID, "binding-one")
	if e != nil || op.Result == nil || op.Result.State != "published" {
		t.Fatal("persisted publication receipt missing", e)
	}
	second := d
	second.ID = strings.Repeat("e", 64)
	second.Port = 18112
	if _, e = s.reserve(ctx, second.ID, "second"); e != nil {
		t.Fatal(e)
	}
	if e = s.activate(ctx, second, d.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = s.reserve(ctx, d.ID, "binding-one"); e != nil {
		t.Fatal(e)
	}
	current, e := s.Active(ctx)
	if e != nil || current.ID != second.ID {
		t.Fatal("replayed receipt reactivated old image", e)
	}
}
func TestGatewayUsesActiveVersionAndOverridesForgedProvenance(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Substrate-Release-ID", "forged")
		w.Write([]byte(r.URL.Path))
	}))
	defer backend.Close()
	c := testConfig(t)
	s, e := Open(c, false)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	port, e := strconv.Atoi(strings.TrimPrefix(backend.URL, "http://127.0.0.1:"))
	if e != nil {
		t.Fatal(e)
	}
	d := Deployment{ID: strings.Repeat("a", 64), ReleaseID: "release_actual", ArtifactDigest: strings.Repeat("b", 64), Port: port, Version: "1.0.0"}
	if e = s.activate(context.Background(), d, ""); e != nil {
		t.Fatal(e)
	}
	gateway := httptest.NewServer(Gateway(s))
	defer gateway.Close()
	response, e := http.Get(gateway.URL + "/api/feedback")
	if e != nil {
		t.Fatal(e)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != 200 || string(body) != "/api/feedback" || response.Header.Get("X-Substrate-Release-ID") != "release_actual" || response.Header.Get("X-Substrate-Artifact-SHA256") != d.ArtifactDigest {
		t.Fatal("gateway lost exact-version provenance")
	}
	d.Address = "8.8.8.8"
	if e = s.activate(context.Background(), d, d.ID); e != nil {
		t.Fatal(e)
	}
	denied, e := http.Get(gateway.URL + "/api/feedback")
	if e != nil {
		t.Fatal(e)
	}
	denied.Body.Close()
	if denied.StatusCode != 503 {
		t.Fatal("gateway accepted an untrusted public backend address")
	}
}

func TestMonitorPersistsUndeliveredProbeAndReadOnlyGatewayCannotWrite(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	defer backend.Close()
	port, _ := strconv.Atoi(strings.TrimPrefix(backend.URL, "http://127.0.0.1:"))
	attempts := 0
	var delivered map[string]any
	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/commands/runtime.report" || r.Header.Get("Authorization") != "Bearer monitor-test-token" {
			t.Error("unbound runtime observation request")
		}
		json.NewDecoder(r.Body).Decode(&delivered)
		attempts++
		if attempts == 1 {
			w.WriteHeader(503)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"deployment_confirmed"}`))
	}))
	defer control.Close()
	c := testConfig(t)
	c.PlatformURL = control.URL
	c.WorkerTokenFile = filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(c.WorkerTokenFile, []byte("monitor-test-token"), 0600); err != nil {
		t.Fatal(err)
	}
	store, e := Open(c, false)
	if e != nil {
		t.Fatal(e)
	}
	deployment := Deployment{ID: strings.Repeat("a", 64), ReleaseID: "release_test", Binding: strings.Repeat("b", 64), Port: port}
	if e = store.activate(context.Background(), deployment, ""); e != nil {
		t.Fatal(e)
	}
	engine := Engine{Store: store}
	if e = engine.MonitorOnce(context.Background()); e == nil {
		t.Fatal("control plane outage was hidden")
	}
	store.Close()
	reopened, e := Open(c, false)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	engine.Store = reopened
	if e = engine.DeliverPending(context.Background()); e != nil {
		t.Fatal(e)
	}
	if attempts != 2 || delivered["health"] != "healthy" || delivered["sequence"] != float64(1) || delivered["externalId"] != "runtime:feedback:"+strings.Repeat("a", 64) {
		t.Fatal("probe was lost or rebound across monitor restart")
	}
	readOnly, e := Open(c, true)
	if e != nil {
		t.Fatal(e)
	}
	defer readOnly.Close()
	if e = (&Engine{Store: readOnly}).MonitorOnce(context.Background()); e == nil {
		t.Fatal("public gateway could persist privileged observations")
	}
}

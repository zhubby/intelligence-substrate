package previewnode

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/intelligence-substrate/platform/internal/webpreview"
)

type memoryDriver struct {
	starts, stops, writes            int
	failStart, failStop, failRequest bool
	onStart                          func()
}

func privateDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "ispv-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}
func (d *memoryDriver) Start(_ context.Context, _ Binding, _ []byte) error {
	d.starts++
	if d.onStart != nil {
		d.onStart()
	}
	if d.failStart {
		return errors.New("uncertain start")
	}
	return nil
}
func (d *memoryDriver) Stop(_ context.Context, _ Binding) error {
	d.stops++
	if d.failStop {
		return errors.New("uncertain stop")
	}
	return nil
}
func (d *memoryDriver) Request(_ context.Context, _ Binding, r Request) (Response, error) {
	if r.Method == "POST" {
		d.writes++
	}
	if d.failRequest {
		return Response{}, errors.New("connection lost")
	}
	return Response{Status: 200, Body: []byte(`{"ok":true}`)}, nil
}
func fixtureBinding(t *testing.T) (Binding, []byte) {
	t.Helper()
	data := []byte("exact executable")
	sum := sha256.Sum256(data)
	return Binding{PreviewID: "preview_" + strings.Repeat("a", 24), Binding: strings.Repeat("b", 64), ArtifactDigest: strings.Repeat("c", 64), ExpiresAt: time.Now().UTC().Add(time.Hour).Truncate(time.Second), Runtime: webpreview.Runtime{SchemaVersion: 1, Profile: "flutter-go-v1", Mode: "feedback", Goarch: "arm64", BackendSHA256: hex.EncodeToString(sum[:]), BackendSize: int64(len(data))}}, data
}
func TestPreviewNodePersistsIntentRecoversAndNeverDuplicatesStart(t *testing.T) {
	ctx := context.Background()
	dir := privateDir(t)
	d := &memoryDriver{}
	e, err := Open(dir, d, 1)
	if err != nil {
		t.Fatal(err)
	}
	b, bin := fixtureBinding(t)
	d.onStart = func() {
		body, x := os.ReadFile(filepath.Join(dir, "instances.json"))
		if x != nil || !strings.Contains(string(body), `"starting"`) {
			t.Fatal("execution happened before durable intent", x)
		}
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, x := e.Ensure(ctx, b, bin); x != nil {
				t.Error(x)
			}
		})
	}
	wg.Wait()
	if d.starts != 1 {
		t.Fatal("duplicate start", d.starts)
	}
	recovered, x := Open(dir, d, 1)
	if x != nil {
		t.Fatal(x)
	}
	if _, x = recovered.Ensure(ctx, b, nil); x != nil {
		t.Fatal(x)
	}
	if d.starts != 1 {
		t.Fatal("restart replayed side effect")
	}
	changed := b
	changed.ArtifactDigest = strings.Repeat("d", 64)
	if _, x = recovered.Ensure(ctx, changed, bin); x == nil {
		t.Fatal("rebound preview executable")
	}
	if x = recovered.Stop(ctx, b); x != nil {
		t.Fatal(x)
	}
	if _, x = recovered.Ensure(ctx, b, bin); x == nil {
		t.Fatal("stopped instance revived")
	}
	if d.stops != 1 {
		t.Fatal("stop missing")
	}
}
func TestPreviewNodeUncertainStartCapacityAndSingleWrite(t *testing.T) {
	ctx := context.Background()
	d := &memoryDriver{failStart: true}
	e, _ := Open(privateDir(t), d, 1)
	b, bin := fixtureBinding(t)
	if _, x := e.Ensure(ctx, b, bin); x == nil {
		t.Fatal("unknown start accepted")
	}
	d.failStart = false
	if _, x := e.Ensure(ctx, b, bin); x == nil || d.starts != 1 {
		t.Fatal("uncertain start replayed")
	}
	other := b
	other.PreviewID = "preview_" + strings.Repeat("d", 24)
	if _, x := e.Ensure(ctx, other, bin); x == nil {
		t.Fatal("uncertain capacity ignored")
	}
	if x := e.Stop(ctx, b); x != nil {
		t.Fatal(x)
	}
	if _, x := e.Ensure(ctx, other, bin); x != nil {
		t.Fatal(x)
	}
	d.failRequest = true
	_, x := e.Request(ctx, other, Request{Method: "POST", Path: "/api/feedback", Body: []byte(`{"title":"one"}`)})
	if x == nil || d.writes != 1 {
		t.Fatal("unknown business result was retried", x, d.writes)
	}
}
func TestPreviewNodeLeaseExpiryAndCorruptionFailClosed(t *testing.T) {
	ctx := context.Background()
	dir := privateDir(t)
	d := &memoryDriver{}
	e, _ := Open(dir, d, 2)
	b, bin := fixtureBinding(t)
	if _, x := e.Ensure(ctx, b, bin); x != nil {
		t.Fatal(x)
	}
	e.mu.Lock()
	r := e.records[b.PreviewID]
	r.LeaseUntil = time.Now().Add(-time.Second)
	e.records[b.PreviewID] = r
	e.mu.Unlock()
	if x := e.Renew(ctx, b); x == nil {
		t.Fatal("expired lease revived")
	}
	if x := e.Reap(ctx); x != nil {
		t.Fatal(x)
	}
	if _, x := e.Request(ctx, b, Request{Method: "GET", Path: "/api/feedback"}); x == nil {
		t.Fatal("expired instance served")
	}
	if d.stops != 1 {
		t.Fatal("expired resources not stopped")
	}
	if x := os.WriteFile(filepath.Join(dir, "instances.json"), []byte(`{"unknown":true}`), 0600); x != nil {
		t.Fatal(x)
	}
	if _, x := Open(dir, d, 2); x == nil {
		t.Fatal("corrupt journal reset silently")
	}
}
func TestPreviewNodeRejectsBadExecutableAndInvalidRPCBeforeExecution(t *testing.T) {
	ctx := context.Background()
	d := &memoryDriver{}
	e, _ := Open(privateDir(t), d, 2)
	b, bin := fixtureBinding(t)
	if _, x := e.Ensure(ctx, b, []byte("other")); x == nil || d.starts != 0 {
		t.Fatal("mismatched binary executed")
	}
	if _, x := e.Ensure(ctx, b, bin); x != nil {
		t.Fatal(x)
	}
	for _, r := range []Request{{Method: "POST", Path: "/api/feedback?url=http://host", Body: []byte(`{}`)}, {Method: "GET", Path: "/api/slots"}, {Method: "DELETE", Path: "/api/feedback"}, {Method: "POST", Path: "/api/feedback", Body: []byte(strings.Repeat("x", 16385))}, {Method: "POST", Path: "/api/feedback", Body: []byte(`not json`)}} {
		if _, x := e.Request(ctx, b, r); x == nil {
			v, _ := json.Marshal(r)
			t.Fatal("invalid RPC reached backend", string(v))
		}
	}
	if d.writes != 0 {
		t.Fatal("invalid RPC caused writes")
	}
}

func TestPreviewNodeReapingFailureDoesNotStarveOtherExpiredInstances(t *testing.T) {
	d := &memoryDriver{}
	engine, err := Open(privateDir(t), d, 2)
	if err != nil {
		t.Fatal(err)
	}
	b, bin := fixtureBinding(t)
	second := b
	second.PreviewID = "preview_" + strings.Repeat("d", 24)
	for _, binding := range []Binding{b, second} {
		if _, err = engine.Ensure(context.Background(), binding, bin); err != nil {
			t.Fatal(err)
		}
	}
	engine.mu.Lock()
	for key, r := range engine.records {
		r.LeaseUntil = time.Now().Add(-time.Second)
		engine.records[key] = r
	}
	engine.mu.Unlock()
	d.failStop = true
	if err = engine.Reap(context.Background()); err == nil || d.stops != 2 {
		t.Fatal("one uncertain cleanup prevented unrelated cleanup", err, d.stops)
	}
}

package previewnode

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPreviewNodeUnixClientAndStrictBoundary(t *testing.T) {
	dir := privateDir(t)
	d := &memoryDriver{}
	e, _ := Open(dir, d, 2)
	b, bin := fixtureBinding(t)
	socket := filepath.Join(dir, "node.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: NewHandler(e)}
	go server.Serve(listener)
	defer server.Close()
	c, err := NewClient(socket)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err = c.Ensure(ctx, b, bin); err != nil {
		t.Fatal(err)
	}
	response, err := c.Request(ctx, b, Request{Method: "POST", Path: "/api/feedback", Body: []byte(`{"title":"x"}`)})
	if err != nil || response.Status != 200 {
		t.Fatal(response, err)
	}
	rows, err := c.List(ctx)
	if err != nil || len(rows) != 1 || !rows[0].Binding.matches(b) {
		t.Fatal(rows, err)
	}
	if err = c.Renew(ctx, b); err != nil {
		t.Fatal(err)
	}
	if err = c.Stop(ctx, b); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Request(ctx, b, Request{Method: "GET", Path: "/api/feedback"}); err == nil {
		t.Fatal("stopped Unix instance served")
	}
	for _, body := range []string{`{"url":"http://127.0.0.1"}`, `{} {}`, strings.Repeat("x", 40000)} {
		w := httptest.NewRecorder()
		NewHandler(e).ServeHTTP(w, httptest.NewRequest("POST", "/v1/request", strings.NewReader(body)))
		if w.Code < 400 {
			t.Fatal("invalid RPC accepted", w.Code)
		}
	}
	w := httptest.NewRecorder()
	NewHandler(e).ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/projects", nil))
	if w.Code != 404 {
		t.Fatal("unrecognized node route")
	}
	if d.writes != 1 {
		t.Fatal("invalid calls caused write or retry", d.writes)
	}
}
func TestPreviewRelaySendsOnlyFixedLoopbackJSONAndNeverFollowsRedirect(t *testing.T) {
	count := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" || r.Header.Get("Origin") != "" {
			t.Error("ambient credentials forwarded")
		}
		if r.URL.Path != "/api/feedback" || r.Method != "POST" {
			t.Error("wrong API request")
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != `{"title":"one"}` {
			t.Error("body changed")
		}
		w.Header().Set("Location", "http://outside.test")
		w.WriteHeader(307)
		_, _ = w.Write([]byte(`{"redirect":true}`))
	}))
	defer upstream.Close()
	address := upstream.Listener.Addr().String()
	transport := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		if addr != "127.0.0.1:8081" {
			t.Error("non-fixed upstream", addr)
		}
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}}
	r, e := Relay(context.Background(), transport, "feedback", Request{Method: "POST", Path: "/api/feedback", Body: []byte(`{"title":"one"}`)})
	if e != nil || r.Status != 307 || count != 1 {
		t.Fatal("redirect followed or response lost", r, e, count)
	}
	for _, q := range []Request{{Method: "GET", Path: "http://external.test"}, {Method: "GET", Path: "/api/../health"}, {Method: "GET", Path: "/api/feedback?x=y"}} {
		if _, err := Relay(context.Background(), transport, "feedback", q); err == nil {
			t.Fatal("invalid relay route")
		}
	}
	if count != 1 {
		t.Fatal("invalid routes reached upstream")
	}
}
func TestPreviewRelayBoundsMalformedResponseAndCancellation(t *testing.T) {
	for _, kind := range []string{"too-big", "not-json", "timeout"} {
		t.Run(kind, func(t *testing.T) {
			var calls atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if kind == "timeout" {
					time.Sleep(100 * time.Millisecond)
				}
				if kind == "too-big" {
					w.Write(bytes.Repeat([]byte("x"), MaxResponse+1))
				} else if kind == "not-json" {
					w.Write([]byte("invalid"))
				} else {
					json.NewEncoder(w).Encode(map[string]bool{"ok": true})
				}
			}))
			defer s.Close()
			tr := &http.Transport{DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, s.Listener.Addr().String())
			}}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			if _, err := Relay(ctx, tr, "feedback", Request{Method: "POST", Path: "/api/feedback", Body: []byte(`{}`)}); err == nil {
				t.Fatal("invalid response accepted")
			}
			if calls.Load() != 1 {
				t.Fatal("write retried", calls.Load())
			}
		})
	}
}

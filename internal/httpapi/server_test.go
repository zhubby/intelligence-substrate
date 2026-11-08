package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	p "github.com/intelligence-substrate/platform/internal/platform"
)

func setup(t *testing.T) *Handler {
	t.Helper()
	s, e := p.Open(filepath.Join(t.TempDir(), "test.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	h := New(p.NewService(s), false)
	if e = h.Auth.Bootstrap("owner@example.com", "long-test-password-123"); e != nil {
		t.Fatal(e)
	}
	return h
}
func req(h http.Handler, method, path string, v any, cookie *http.Cookie, csrf string) *httptest.ResponseRecorder {
	b, _ := json.Marshal(v)
	r := httptest.NewRequest(method, path, bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		r.AddCookie(cookie)
	}
	if csrf != "" {
		r.Header.Set("X-CSRF-Token", csrf)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func login(t *testing.T, h *Handler) (*http.Cookie, string) {
	t.Helper()
	w := req(h, "POST", "/api/v1/auth/login", p.M{"email": "owner@example.com", "password": "long-test-password-123"}, nil, "")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var s Session
	if e := json.Unmarshal(w.Body.Bytes(), &s); e != nil {
		t.Fatal(e)
	}
	return w.Result().Cookies()[0], s.CSRF
}
func TestAuthenticationAndCSRF(t *testing.T) {
	h := setup(t)
	if w := req(h, "GET", "/api/v1/projects", nil, nil, ""); w.Code != 401 {
		t.Fatalf("anonymous project access: %d", w.Code)
	} else if w.Header().Get("WWW-Authenticate") != "" {
		t.Fatal("JSON API triggered the browser's native Basic authentication dialog")
	}
	cookie, csrf := login(t, h)
	v := p.M{"title": "Example"}
	if w := req(h, "POST", "/api/v1/commands/projects.create", v, cookie, ""); w.Code != 403 {
		t.Fatal("missing CSRF accepted")
	}
	if w := req(h, "POST", "/api/v1/commands/projects.create", v, cookie, csrf); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
}
func TestCrossOriginLoginRejected(t *testing.T) {
	h := setup(t)
	r := httptest.NewRequest("POST", "http://platform.test/api/v1/auth/login", bytes.NewBufferString(`{"email":"owner@example.com","password":"long-test-password-123"}`))
	r.Header.Set("Origin", "https://attacker.test")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("cross-origin login accepted")
	}
}
func TestPasswordsNeverReturned(t *testing.T) {
	h := setup(t)
	cookie, csrf := login(t, h)
	w := req(h, "GET", "/api/v1/identities", nil, cookie, csrf)
	if bytes.Contains(w.Body.Bytes(), []byte("passwordHash")) || bytes.Contains(w.Body.Bytes(), []byte("long-test-password")) {
		t.Fatal("identity API leaked password material")
	}
}

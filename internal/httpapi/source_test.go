package httpapi

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	p "github.com/intelligence-substrate/platform/internal/platform"
)

func TestSourceSnapshotHTTPBoundsMetadataAndUsesSessionGuards(t *testing.T) {
	h := setup(t)
	cookie, csrf := login(t, h)
	identity, _ := h.Auth.IdentityByEmail("owner@example.com")
	a := p.Actor{ID: identity.ID, Kind: "human", Admin: true}
	project, e := h.Service.Command(context.Background(), a, "projects.create", p.M{"title": "Source endpoint"})
	if e != nil {
		t.Fatal(e)
	}
	repo, e := h.Service.Command(context.Background(), a, "repositories.create", p.M{"projectId": project.ID, "title": "source"})
	if e != nil {
		t.Fatal(e)
	}
	meta, _ := json.Marshal(p.M{"key": "source", "branch": "feature", "baseCommit": p.S(repo.Data, "head"), "expectedHead": "", "message": "Raw binary"})
	encoded := base64.StdEncoding.EncodeToString(meta)
	var body bytes.Buffer
	tw := tar.NewWriter(&body)
	_ = tw.WriteHeader(&tar.Header{Name: "binary", Mode: 0755, Size: 3})
	_, _ = tw.Write([]byte{0, 255, 128})
	_ = tw.Close()
	post := func(header, csrfToken, origin, contentType string, authenticated bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/v1/repositories/"+repo.ID+"/snapshot", bytes.NewReader(body.Bytes()))
		if authenticated {
			r.AddCookie(cookie)
		}
		r.Header.Set("X-CSRF-Token", csrfToken)
		r.Header.Set("Origin", origin)
		r.Header.Set("Content-Type", contentType)
		r.Header.Set("X-IS-Source", header)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	for _, tc := range []struct {
		name, header, csrf, origin, ctype string
		auth                              bool
		status                            int
	}{
		{"anonymous", encoded, csrf, "", "application/x-tar", false, 401},
		{"csrf", encoded, "", "", "application/x-tar", true, 403},
		{"origin", encoded, csrf, "https://untrusted.test", "application/x-tar", true, 403},
		{"media", encoded, csrf, "", "application/json", true, 415},
		{"missing", "", csrf, "", "application/x-tar", true, 400},
		{"bad encoding", "not base64", csrf, "", "application/x-tar", true, 400},
		{"oversize", strings.Repeat("a", 8193), csrf, "", "application/x-tar", true, 400},
		{"unknown", base64.StdEncoding.EncodeToString([]byte(`{"key":"x","extra":true}`)), csrf, "", "application/x-tar", true, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := post(tc.header, tc.csrf, tc.origin, tc.ctype, tc.auth)
			if w.Code != tc.status {
				t.Fatalf("got %d: %s", w.Code, w.Body.String())
			}
		})
	}
	w := post(encoded, csrf, "", "application/x-tar", true)
	if w.Code != 200 {
		t.Fatalf("snapshot: %d %s", w.Code, w.Body.String())
	}
	var op p.Resource
	if e = json.Unmarshal(w.Body.Bytes(), &op); e != nil || op.Status != "committed" {
		t.Fatal("no committed source response", e)
	}
	if bytes.Contains(w.Body.Bytes(), []byte("leaseToken")) {
		t.Fatal("snapshot response exposes lease")
	}
	replay := post(encoded, csrf, "", "application/x-tar", true)
	if replay.Code != 200 || !bytes.Equal(w.Body.Bytes(), replay.Body.Bytes()) {
		t.Fatal("HTTP retry changed operation", replay.Body.String())
	}
}

package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	p "github.com/intelligence-substrate/platform/internal/platform"
)

func TestOIDCVerifiedIdentityNonceAndOneTimeState(t *testing.T) {
	h := setup(t)
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	signer, e := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "test"))
	if e != nil {
		t.Fatal(e)
	}
	issuer, nonce := "", ""
	email := "owner@example.com"
	badNonce := false
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(p.M{"issuer": issuer, "authorization_endpoint": issuer + "/authorize", "token_endpoint": issuer + "/token", "jwks_uri": issuer + "/jwks", "id_token_signing_alg_values_supported": []string{"RS256"}})
		case "/jwks":
			_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "test", Algorithm: "RS256", Use: "sig"}}})
		case "/token":
			n := nonce
			if badNonce {
				n = "wrong"
			}
			claims, _ := json.Marshal(p.M{"iss": issuer, "aud": "test-client", "sub": "employee-1", "exp": time.Now().Add(time.Minute).Unix(), "iat": time.Now().Unix(), "nonce": n, "email": email, "email_verified": true})
			signed, e := signer.Sign(claims)
			if e != nil {
				t.Error(e)
				w.WriteHeader(500)
				return
			}
			token, e := signed.CompactSerialize()
			if e != nil {
				t.Error(e)
				return
			}
			_ = json.NewEncoder(w).Encode(p.M{"access_token": "test-only", "token_type": "Bearer", "expires_in": 60, "id_token": token})
		default:
			http.NotFound(w, r)
		}
	}))
	defer provider.Close()
	issuer = provider.URL
	t.Setenv("IS_OIDC_ISSUER", issuer)
	t.Setenv("IS_OIDC_CLIENT_ID", "test-client")
	t.Setenv("IS_OIDC_CLIENT_SECRET", "test-client-secret")
	h.PublicURL = "http://platform.test"
	if e = h.ConfigureOIDC(context.Background()); e != nil {
		t.Fatal(e)
	}
	begin := func() (string, *http.Cookie) {
		t.Helper()
		out := req(h, "GET", "http://platform.test/auth/oidc/start", nil, nil, "")
		if out.Code != 302 {
			t.Fatal(out.Body.String())
		}
		u, e := url.Parse(out.Header().Get("Location"))
		if e != nil {
			t.Fatal(e)
		}
		nonce = u.Query().Get("nonce")
		if u.Query().Get("code_challenge_method") != "S256" {
			t.Fatal("PKCE missing")
		}
		return u.Query().Get("state"), out.Result().Cookies()[0]
	}
	callback := func(state string, cookie *http.Cookie) *httptest.ResponseRecorder {
		return req(h, "GET", "http://platform.test/auth/oidc/callback?code=test&state="+state, nil, cookie, "")
	}
	state, cookie := begin()
	out := callback(state, cookie)
	if out.Code != 302 {
		t.Fatal(out.Body.String())
	}
	var session *http.Cookie
	for _, c := range out.Result().Cookies() {
		if c.Name == "is_session" {
			session = c
		}
	}
	if session == nil {
		t.Fatal("no authenticated session")
	}
	if me := req(h, "GET", "/api/v1/auth/me", nil, session, ""); me.Code != 200 {
		t.Fatal(me.Body.String())
	}
	if replay := callback(state, cookie); replay.Code != 403 {
		t.Fatal("OIDC state replay accepted")
	}
	state, cookie = begin()
	badNonce = true
	if out = callback(state, cookie); out.Code != 403 {
		t.Fatalf("invalid nonce accepted: %d %s", out.Code, out.Body.String())
	}
	badNonce = false
	state, cookie = begin()
	email = "unknown@example.com"
	if out = callback(state, cookie); out.Code != 403 {
		t.Fatal("unprovisioned SSO identity was admitted")
	}
}

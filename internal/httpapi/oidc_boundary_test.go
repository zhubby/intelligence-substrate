package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-jose/go-jose/v4"
	p "github.com/intelligence-substrate/platform/internal/platform"
)

// The external boundary is a real HTTPS issuer with signed JWTs and single-use
// authorization codes. Application identity, sessions, audit and SQL stay real.
type boundaryOIDC struct {
	h        *Handler
	provider *httptest.Server
	mu       sync.Mutex
	codes    map[string]p.M
	calls    int
}

func boundaryIssuer(t *testing.T, h *Handler) *boundaryOIDC {
	t.Helper()
	f := &boundaryOIDC{h: h, codes: map[string]p.M{}}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "boundary"))
	if err != nil {
		t.Fatal(err)
	}
	f.provider = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(p.M{"issuer": f.provider.URL, "authorization_endpoint": f.provider.URL + "/authorize", "token_endpoint": f.provider.URL + "/token", "jwks_uri": f.provider.URL + "/jwks", "id_token_signing_alg_values_supported": []string{"RS256"}})
		case "/jwks":
			_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "boundary", Algorithm: "RS256", Use: "sig"}}})
		case "/token":
			if err := r.ParseForm(); err != nil {
				http.Error(w, "invalid form", 400)
				return
			}
			f.mu.Lock()
			f.calls++
			code := f.codes[r.Form.Get("code")]
			delete(f.codes, r.Form.Get("code"))
			f.mu.Unlock()
			client, secret, _ := r.BasicAuth()
			hash := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			if code == nil || r.Form.Get("grant_type") != "authorization_code" || client != "boundary-client" || secret != "boundary-secret" || r.Form.Get("redirect_uri") != "https://platform.test/auth/oidc/callback" || base64.RawURLEncoding.EncodeToString(hash[:]) != p.S(code, "challenge") {
				http.Error(w, "invalid authorization code", 400)
				return
			}
			claims, _ := json.Marshal(p.M{"iss": f.provider.URL, "aud": "boundary-client", "sub": code["subject"], "email": code["email"], "email_verified": true, "nonce": code["nonce"], "iat": time.Now().Unix(), "exp": time.Now().Add(time.Minute).Unix()})
			signed, err := signer.Sign(claims)
			if err != nil {
				http.Error(w, "signing failed", 500)
				return
			}
			compact, err := signed.CompactSerialize()
			if err != nil {
				http.Error(w, "serialization failed", 500)
				return
			}
			_ = json.NewEncoder(w).Encode(p.M{"access_token": "boundary-only", "token_type": "Bearer", "expires_in": 60, "id_token": compact})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.provider.Close)
	t.Setenv("IS_OIDC_ISSUER", f.provider.URL)
	t.Setenv("IS_OIDC_CLIENT_ID", "boundary-client")
	t.Setenv("IS_OIDC_CLIENT_SECRET", "boundary-secret")
	h.PublicURL, h.Auth.Secure = "https://platform.test", true
	if err = h.ConfigureOIDC(oidc.ClientContext(context.Background(), f.provider.Client())); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *boundaryOIDC) begin(t *testing.T, email, subject string) (string, string, *http.Cookie) {
	t.Helper()
	w := req(f.h, "GET", "https://platform.test/auth/oidc/start", nil, nil, "")
	if w.Code != 302 {
		t.Fatal("OIDC start failed", w.Code)
	}
	u, err := url.Parse(w.Header().Get("Location"))
	if err != nil || u.Query().Get("code_challenge_method") != "S256" || u.Query().Get("nonce") == "" {
		t.Fatal("missing nonce or PKCE")
	}
	code := p.Secret()
	f.mu.Lock()
	f.codes[code] = p.M{"email": email, "subject": subject, "nonce": u.Query().Get("nonce"), "challenge": u.Query().Get("code_challenge")}
	f.mu.Unlock()
	return code, u.Query().Get("state"), w.Result().Cookies()[0]
}

func (f *boundaryOIDC) callback(code, state string, cookie *http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", "https://platform.test/auth/oidc/callback?"+url.Values{"code": {code}, "state": {state}}.Encode(), nil)
	r = r.WithContext(oidc.ClientContext(r.Context(), f.provider.Client()))
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	return w
}

func (f *boundaryOIDC) exchanges() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func noOIDCSession(t *testing.T, h *Handler, w *httptest.ResponseRecorder) {
	t.Helper()
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == "is_session" && cookie.Value != "" {
			t.Fatal("rejected identity received a session")
		}
	}
}

func TestOIDCStableSubjectCannotBindAnotherAccount(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "enabled-origin", true: "disabled-origin"}[disabled], func(t *testing.T) {
			h := setup(t)
			other, err := h.Auth.CreateIdentity("other@example.test", "Other human", "boundary-password-2026", true)
			if err != nil {
				t.Fatal(err)
			}
			f := boundaryIssuer(t, h)
			code, state, cookie := f.begin(t, "owner@example.com", "employee-stable-1")
			if w := f.callback(code, state, cookie); w.Code != 302 {
				t.Fatal("initial exact identity failed", w.Code, w.Body.String())
			}
			if disabled {
				original, _ := h.Auth.IdentityByEmail("owner@example.com")
				if err = h.Auth.Disable(context.Background(), identityActor(other), original.ID); err != nil {
					t.Fatal(err)
				}
			}
			code, state, cookie = f.begin(t, other.Email, "employee-stable-1")
			w := f.callback(code, state, cookie)
			if w.Code != 403 {
				t.Fatalf("same issuer/subject moved to another preprovisioned account: HTTP %d", w.Code)
			}
			noOIDCSession(t, h, w)
			current, _ := h.Auth.identity(other.ID)
			if current.OIDCSub != "" {
				t.Fatal("rejected account retained the other identity binding")
			}
		})
	}
}

func TestOIDCRejectsEmptyOrUnboundedStableSubject(t *testing.T) {
	for name, subject := range map[string]string{"empty": "", "whitespace": " \t", "oversized": strings.Repeat("s", 256)} {
		t.Run(name, func(t *testing.T) {
			h := setup(t)
			f := boundaryIssuer(t, h)
			code, state, cookie := f.begin(t, "owner@example.com", subject)
			w := f.callback(code, state, cookie)
			if w.Code != 403 {
				t.Fatalf("malformed stable identity admitted: HTTP %d", w.Code)
			}
			noOIDCSession(t, h, w)
			current, _ := h.Auth.IdentityByEmail("owner@example.com")
			if current.OIDCSub != "" {
				t.Fatal("malformed stable identity was bound")
			}
		})
	}
}

func TestOIDCRejectsInvalidStateBeforeTokenExchange(t *testing.T) {
	for _, name := range []string{"invalid-expiry", "expired-offset", "malformed-json", "missing-verifier", "missing-nonce"} {
		t.Run(name, func(t *testing.T) {
			h := setup(t)
			f := boundaryIssuer(t, h)
			code, state, cookie := f.begin(t, "owner@example.com", "employee-stable-1")
			var err error
			switch name {
			case "invalid-expiry":
				_, err = h.Service.Store.DB.Exec(`UPDATE credentials SET expires_at='invalid' WHERE hash=$1`, p.Hash(state))
			case "expired-offset":
				expired := time.Now().Add(-time.Minute).In(time.FixedZone("fixture", 12*3600)).Format(time.RFC3339Nano)
				_, err = h.Service.Store.DB.Exec(`UPDATE credentials SET expires_at=$1 WHERE hash=$2`, expired, p.Hash(state))
			case "malformed-json":
				_, err = h.Service.Store.DB.Exec(`UPDATE credentials SET body='{' WHERE hash=$1`, p.Hash(state))
			default:
				var body string
				if err = h.Service.Store.DB.QueryRow(`SELECT body FROM credentials WHERE hash=$1`, p.Hash(state)).Scan(&body); err == nil {
					var value p.M
					_ = json.Unmarshal([]byte(body), &value)
					delete(value, strings.TrimPrefix(name, "missing-"))
					encoded, _ := json.Marshal(value)
					_, err = h.Service.Store.DB.Exec(`UPDATE credentials SET body=$1 WHERE hash=$2`, string(encoded), p.Hash(state))
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			w := f.callback(code, state, cookie)
			if w.Code != 403 || f.exchanges() != 0 {
				t.Fatalf("invalid state reached provider or was admitted: HTTP %d, exchanges %d", w.Code, f.exchanges())
			}
			noOIDCSession(t, h, w)
		})
	}
}

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
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-jose/go-jose/v4"
	p "github.com/intelligence-substrate/platform/internal/platform"
)

// A real TLS issuer and a separate HTTP receiver expose actual transport effects.
// The receiver records only counts/booleans, never codes or credentials.
type oidcTransportFixture struct {
	h             *Handler
	tls, plain    *httptest.Server
	mode          string
	metadata      map[string]string
	mu            sync.Mutex
	codes         map[string]p.M
	plaintext     int
	codeSeen      bool
	secretSeen    bool
	stallEntered  chan struct{}
	stallReleased chan struct{}
	stallOnce     sync.Once
	releaseOnce   sync.Once
}

func newOIDCTransportFixture(t *testing.T, mode string) *oidcTransportFixture {
	t.Helper()
	f := &oidcTransportFixture{h: setup(t), mode: mode, metadata: map[string]string{}, codes: map[string]p.M{}, stallEntered: make(chan struct{}), stallReleased: make(chan struct{})}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "transport"))
	if err != nil {
		t.Fatal(err)
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.TLS == nil {
			_ = r.ParseForm()
			_, secret, _ := r.BasicAuth()
			f.mu.Lock()
			f.plaintext++
			f.codeSeen = f.codeSeen || r.Form.Get("code") != ""
			f.secretSeen = f.secretSeen || secret != "" || r.Form.Get("client_secret") != ""
			f.mu.Unlock()
		}
		stage := ""
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			stage = "discovery"
		case "/token":
			stage = "token"
		case "/jwks":
			stage = "jwks"
		}
		if stage != "" && r.TLS != nil && (f.mode == stage+"-http" || f.mode == stage+"-https") {
			target := f.plain.URL
			if strings.HasSuffix(f.mode, "-https") {
				target = f.tls.URL
			}
			http.Redirect(w, r, target+"/resolved/"+stage, http.StatusTemporaryRedirect)
			return
		}
		if f.mode == "stall-token" && stage == "token" {
			f.stallOnce.Do(func() { close(f.stallEntered) })
			select {
			case <-r.Context().Done():
				return
			case <-f.stallReleased:
			}
		}
		switch r.URL.Path {
		case "/.well-known/openid-configuration", "/resolved/discovery":
			issuer := f.tls.URL
			if f.mode == "development" {
				issuer = f.plain.URL
			}
			value := p.M{"issuer": issuer, "authorization_endpoint": issuer + "/authorize", "token_endpoint": issuer + "/token", "jwks_uri": issuer + "/jwks", "id_token_signing_alg_values_supported": []string{"RS256"}}
			for k, v := range f.metadata {
				value[k] = v
			}
			_ = json.NewEncoder(w).Encode(value)
		case "/jwks", "/resolved/jwks":
			_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "transport", Algorithm: "RS256", Use: "sig"}}})
		case "/token", "/resolved/token":
			_ = r.ParseForm()
			f.mu.Lock()
			code := f.codes[r.Form.Get("code")]
			delete(f.codes, r.Form.Get("code"))
			f.mu.Unlock()
			client, secret, _ := r.BasicAuth()
			hash := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			if code == nil || client != "transport-client" || secret != "transport-secret" || r.Form.Get("grant_type") != "authorization_code" || base64.RawURLEncoding.EncodeToString(hash[:]) != p.S(code, "challenge") {
				http.Error(w, "invalid authorization", 400)
				return
			}
			issuer := f.tls.URL
			if f.mode == "development" {
				issuer = f.plain.URL
			}
			claims, _ := json.Marshal(p.M{"iss": issuer, "aud": "transport-client", "sub": "stable-transport-employee", "email": "owner@example.com", "email_verified": true, "nonce": code["nonce"], "exp": time.Now().Add(time.Minute).Unix(), "iat": time.Now().Unix()})
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
			_ = json.NewEncoder(w).Encode(p.M{"access_token": "transport-fixture-only", "token_type": "Bearer", "expires_in": 60, "id_token": compact})
		default:
			http.NotFound(w, r)
		}
	})
	f.plain = httptest.NewServer(handler)
	f.tls = httptest.NewTLSServer(handler)
	t.Cleanup(func() { f.releaseOnce.Do(func() { close(f.stallReleased) }); f.tls.Close(); f.plain.Close() })
	t.Setenv("IS_OIDC_ISSUER", f.tls.URL)
	t.Setenv("IS_OIDC_CLIENT_ID", "transport-client")
	t.Setenv("IS_OIDC_CLIENT_SECRET", "transport-secret")
	f.h.PublicURL, f.h.Auth.Secure = "https://platform.test", true
	if mode == "development" {
		t.Setenv("IS_OIDC_ISSUER", f.plain.URL)
		f.h.Auth.Secure = false
	}
	return f
}

func (f *oidcTransportFixture) configure() error {
	return f.h.ConfigureOIDC(oidc.ClientContext(context.Background(), f.tls.Client()))
}

func (f *oidcTransportFixture) begin(t *testing.T) (string, string, *http.Cookie) {
	t.Helper()
	w := req(f.h, "GET", "https://platform.test/auth/oidc/start", nil, nil, "")
	if w.Code != 302 {
		t.Fatal("SSO start failed", w.Code)
	}
	u, err := url.Parse(w.Header().Get("Location"))
	if err != nil || u.Query().Get("code_challenge_method") != "S256" || u.Query().Get("nonce") == "" {
		t.Fatal("missing signed-flow input")
	}
	code := p.Secret()
	f.mu.Lock()
	f.codes[code] = p.M{"nonce": u.Query().Get("nonce"), "challenge": u.Query().Get("code_challenge")}
	f.mu.Unlock()
	return code, u.Query().Get("state"), w.Result().Cookies()[0]
}

func (f *oidcTransportFixture) callback(code, state string, cookie *http.Cookie, client *http.Client) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", "https://platform.test/auth/oidc/callback?"+url.Values{"code": {code}, "state": {state}}.Encode(), nil)
	if client != nil {
		r = r.WithContext(oidc.ClientContext(r.Context(), client))
	}
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	return w
}

func (f *oidcTransportFixture) plaintextFacts() (int, bool, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.plaintext, f.codeSeen, f.secretSeen
}

func TestOIDCProductionRejectsUnsafeDiscoveredEndpoints(t *testing.T) {
	for _, field := range []string{"authorization_endpoint", "token_endpoint", "jwks_uri"} {
		for _, kind := range []string{"http", "relative", "missing", "userinfo", "fragment"} {
			t.Run(field+"/"+kind, func(t *testing.T) {
				f := newOIDCTransportFixture(t, "")
				value := map[string]string{"http": f.plain.URL + "/token", "relative": "/token", "missing": "", "userinfo": strings.Replace(f.tls.URL, "https://", "https://untrusted@", 1) + "/token", "fragment": f.tls.URL + "/token#fragment"}[kind]
				f.metadata[field] = value
				if err := f.configure(); err == nil {
					t.Fatalf("production SSO enabled with unsafe %s (%s)", field, kind)
				}
				if hits, _, _ := f.plaintextFacts(); hits != 0 {
					t.Fatal("unsafe configuration contacted plaintext receiver")
				}
			})
		}
	}
}

func TestOIDCProductionRejectsPlaintextRedirectBeforeNetwork(t *testing.T) {
	for _, stage := range []string{"discovery", "token", "jwks"} {
		t.Run(stage, func(t *testing.T) {
			f := newOIDCTransportFixture(t, stage+"-http")
			err := f.configure()
			if stage == "discovery" {
				if err == nil {
					t.Error("plaintext discovery redirect enabled production SSO")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				code, state, cookie := f.begin(t)
				w := f.callback(code, state, cookie, f.tls.Client())
				if w.Code != 401 && w.Code != 403 {
					t.Errorf("unsafe redirect admitted callback: HTTP %d", w.Code)
				}
				for _, c := range w.Result().Cookies() {
					if c.Name == "is_session" && c.Value != "" {
						t.Error("unsafe redirect issued a session")
					}
				}
				identity, _ := f.h.Auth.IdentityByEmail("owner@example.com")
				if identity.OIDCSub != "" {
					t.Error("unsafe redirect bound a stable identity")
				}
				var audits int
				if err := f.h.Service.Store.DB.QueryRow(`SELECT COUNT(*) FROM audit WHERE body LIKE '%auth.oidc%'`).Scan(&audits); err != nil || audits != 0 {
					t.Error("unsafe redirect recorded successful authentication", audits)
				}
				if replay := f.callback(code, state, cookie, f.tls.Client()); replay.Code != 403 {
					t.Error("consumed unsafe-exchange state was replayed")
				}
			}
			hits, codeSeen, secretSeen := f.plaintextFacts()
			t.Logf("plaintext requests=%d code-transmitted=%t secret-transmitted=%t", hits, codeSeen, secretSeen)
			if hits != 0 || codeSeen || secretSeen {
				t.Error("production OIDC reached plaintext transport")
			}
		})
	}
}

func TestOIDCConfiguredTrustAndHTTPSRedirectsSurviveCallback(t *testing.T) {
	for _, mode := range []string{"", "discovery-https", "token-https", "jwks-https", "development"} {
		t.Run(mode, func(t *testing.T) {
			f := newOIDCTransportFixture(t, mode)
			if err := f.configure(); err != nil {
				t.Fatal(err)
			}
			code, state, cookie := f.begin(t)
			// Ordinary incoming HTTP requests do not carry the configuration's TLS
			// client. The configured trust must persist without global mutation.
			w := f.callback(code, state, cookie, nil)
			if w.Code != 302 {
				t.Fatalf("configured provider trust/redirect was lost: HTTP %d", w.Code)
			}
			var session *http.Cookie
			for _, c := range w.Result().Cookies() {
				if c.Name == "is_session" {
					session = c
				}
			}
			if session == nil || req(f.h, "GET", "/api/v1/auth/me", nil, session, "").Code != 200 {
				t.Fatal("signed login did not authenticate the actual platform identity")
			}
			if hits, _, _ := f.plaintextFacts(); mode != "development" && hits != 0 {
				t.Fatal("valid HTTPS flow contacted plaintext receiver")
			}
		})
	}
}

func TestOIDCStalledTokenExchangeIsBounded(t *testing.T) {
	f := newOIDCTransportFixture(t, "stall-token")
	if err := f.configure(); err != nil {
		t.Fatal(err)
	}
	code, state, cookie := f.begin(t)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- f.callback(code, state, cookie, f.tls.Client()) }()
	select {
	case <-f.stallEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("callback did not reach actual token endpoint")
	}
	select {
	case result := <-done:
		if result.Code != 401 {
			t.Fatal("stalled token exchange did not fail authentication", result.Code)
		}
		noOIDCSession(t, f.h, result)
	case <-time.After(18 * time.Second):
		t.Error("token exchange remained live beyond bounded request lifetime")
		f.releaseOnce.Do(func() { close(f.stallReleased) })
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("released fixture callback did not terminate")
		}
	}
}

func TestOIDCProductionDoesNotTrustUnknownTLSCertificate(t *testing.T) {
	f := newOIDCTransportFixture(t, "")
	if err := f.h.ConfigureOIDC(context.Background()); err == nil {
		t.Fatal("untrusted provider certificate was accepted")
	}
}

func TestOIDCConfiguredRedirectPolicyAndShorterTimeout(t *testing.T) {
	for _, name := range []string{"redirect-policy", "short-timeout"} {
		t.Run(name, func(t *testing.T) {
			mode := "token-https"
			if name == "short-timeout" {
				mode = "stall-token"
			}
			f := newOIDCTransportFixture(t, mode)
			client := f.tls.Client()
			if name == "redirect-policy" {
				client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
			} else {
				client.Timeout = 100 * time.Millisecond
			}
			if err := f.h.ConfigureOIDC(oidc.ClientContext(context.Background(), client)); err != nil {
				t.Fatal(err)
			}
			code, state, cookie := f.begin(t)
			start := time.Now()
			w := f.callback(code, state, cookie, nil)
			if w.Code != 401 || time.Since(start) > 2*time.Second {
				t.Fatal("configured redirect policy or shorter timeout was lost", w.Code)
			}
			noOIDCSession(t, f.h, w)
		})
	}
}

func TestOIDCCallbackCancellationStopsTokenExchange(t *testing.T) {
	f := newOIDCTransportFixture(t, "stall-token")
	if err := f.configure(); err != nil {
		t.Fatal(err)
	}
	code, state, cookie := f.begin(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := httptest.NewRequest("GET", "https://platform.test/auth/oidc/callback?"+url.Values{"code": {code}, "state": {state}}.Encode(), nil).WithContext(ctx)
	r.AddCookie(cookie)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { w := httptest.NewRecorder(); f.h.ServeHTTP(w, r); done <- w }()
	select {
	case <-f.stallEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("callback did not reach actual token endpoint")
	}
	cancel()
	select {
	case w := <-done:
		if w.Code != 401 {
			t.Fatal("canceled callback did not fail authentication", w.Code)
		}
		noOIDCSession(t, f.h, w)
	case <-time.After(2 * time.Second):
		t.Fatal("configured client discarded caller cancellation")
	}
}

func TestOIDCSeparateHTTPSOriginAndEndpointQueries(t *testing.T) {
	f := newOIDCTransportFixture(t, "")
	target, _ := url.Parse(f.tls.URL)
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = f.tls.Client().Transport
	other := httptest.NewTLSServer(proxy)
	t.Cleanup(other.Close)
	f.metadata["authorization_endpoint"] = f.tls.URL + "/authorize?tenant=accepted"
	f.metadata["token_endpoint"] = other.URL + "/token?tenant=accepted"
	f.metadata["jwks_uri"] = other.URL + "/jwks?tenant=accepted"
	if err := f.configure(); err != nil {
		t.Fatal(err)
	}
	code, state, cookie := f.begin(t)
	if w := f.callback(code, state, cookie, nil); w.Code != 302 {
		t.Fatal("valid separate HTTPS endpoints or endpoint queries were rejected", w.Code)
	}
}

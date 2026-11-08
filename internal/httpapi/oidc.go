package httpapi

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	p "github.com/intelligence-substrate/platform/internal/platform"
	"golang.org/x/oauth2"
)

func (h *Handler) ConfigureOIDC(ctx context.Context) error {
	issuer := os.Getenv("IS_OIDC_ISSUER")
	if issuer == "" {
		return nil
	}
	if h.Auth.Secure && !validOIDCHTTPSURL(issuer) {
		return &p.Fault{Code: "oidc_config", Message: "生产 OIDC 身份提供方必须使用 HTTPS", Status: 422}
	}
	clientID := os.Getenv("IS_OIDC_CLIENT_ID")
	if clientID == "" || h.PublicURL == "" {
		return &p.Fault{Code: "oidc_config", Message: "OIDC 需要客户端标识和 IS_PUBLIC_URL", Status: 422}
	}
	client := oidcHTTPClient(ctx, h.Auth.Secure)
	ctx, cancel := context.WithTimeout(oidc.ClientContext(ctx, client), 15*time.Second)
	defer cancel()
	provider, e := oidc.NewProvider(ctx, issuer)
	if e != nil {
		return e
	}
	if h.Auth.Secure {
		var metadata struct {
			JWKSURL string `json:"jwks_uri"`
		}
		if e = provider.Claims(&metadata); e != nil || !validOIDCHTTPSURL(provider.Endpoint().AuthURL) || !validOIDCHTTPSURL(provider.Endpoint().TokenURL) || !validOIDCHTTPSURL(metadata.JWKSURL) {
			return &p.Fault{Code: "oidc_config", Message: "生产 OIDC 授权、令牌和 JWKS 端点必须使用不含用户信息和片段的完整 HTTPS URL", Status: 422}
		}
	}
	config := oauth2.Config{ClientID: clientID, ClientSecret: os.Getenv("IS_OIDC_CLIENT_SECRET"), Endpoint: provider.Endpoint(), RedirectURL: strings.TrimRight(h.PublicURL, "/") + "/auth/oidc/callback", Scopes: []string{oidc.ScopeOpenID, "email", "profile"}}
	verifier := provider.Verifier(&oidc.Config{ClientID: clientID})
	h.OIDCStart = func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			http.NotFound(w, r)
			return
		}
		state, nonce, pkce := p.Secret(), p.Secret(), p.Secret()
		body, _ := json.Marshal(p.M{"nonce": nonce, "verifier": pkce})
		_, e := h.Service.Store.DB.Exec(`INSERT INTO credentials(hash,identity_id,kind,expires_at,body) VALUES($1,'','oidc',$2,$3)`, p.Hash(state), time.Now().Add(10*time.Minute).UTC().Format(time.RFC3339Nano), string(body))
		if e != nil {
			writeError(w, e)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "is_oidc_state", Value: state, Path: "/auth/oidc", HttpOnly: true, Secure: h.Auth.Secure, SameSite: http.SameSiteLaxMode, MaxAge: 600})
		hash := sha256.Sum256([]byte(pkce))
		http.Redirect(w, r, config.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.SetAuthURLParam("code_challenge", base64.RawURLEncoding.EncodeToString(hash[:])), oauth2.SetAuthURLParam("code_challenge_method", "S256")), http.StatusFound)
	}
	h.OIDCCallback = func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			http.NotFound(w, r)
			return
		}
		state := r.URL.Query().Get("state")
		cookie, e := r.Cookie("is_oidc_state")
		if e != nil || state == "" || subtle.ConstantTimeCompare([]byte(state), []byte(cookie.Value)) != 1 {
			writeError(w, &p.Fault{Code: "oidc_state", Message: "SSO 登录状态无效", Status: 403})
			return
		}
		var saved struct {
			Nonce    string `json:"nonce"`
			Verifier string `json:"verifier"`
		}
		e = h.Service.Store.Update(r.Context(), func(t *p.Tx) error {
			credential, err := t.Credential(p.Hash(state))
			if err != nil || credential.Kind != "oidc" || credential.IdentityID != "" {
				return &p.Fault{Code: "oidc_state", Message: "SSO 登录状态已失效", Status: 403}
			}
			expires, err := time.Parse(time.RFC3339Nano, credential.ExpiresAt)
			if err != nil || !expires.After(time.Now()) || json.Unmarshal([]byte(credential.Body), &saved) != nil || saved.Nonce == "" || saved.Verifier == "" {
				return &p.Fault{Code: "oidc_state", Message: "SSO 登录状态已失效", Status: 403}
			}
			return t.RevokeCredential(p.Hash(state))
		})
		if e != nil {
			writeError(w, e)
			return
		}
		exchangeCtx, cancel := context.WithTimeout(oidc.ClientContext(r.Context(), client), 15*time.Second)
		defer cancel()
		token, e := config.Exchange(exchangeCtx, r.URL.Query().Get("code"), oauth2.SetAuthURLParam("code_verifier", saved.Verifier))
		if e != nil {
			writeError(w, &p.Fault{Code: "oidc_exchange", Message: "身份提供方登录未完成", Status: 401})
			return
		}
		raw, ok := token.Extra("id_token").(string)
		if !ok {
			writeError(w, &p.Fault{Code: "oidc_token", Message: "缺少身份令牌", Status: 401})
			return
		}
		id, e := verifier.Verify(exchangeCtx, raw)
		if e != nil {
			writeError(w, &p.Fault{Code: "oidc_token", Message: "身份令牌校验失败", Status: 401})
			return
		}
		claims := struct {
			Email         string `json:"email"`
			EmailVerified bool   `json:"email_verified"`
			Nonce         string `json:"nonce"`
		}{}
		if e = id.Claims(&claims); e != nil || !claims.EmailVerified || claims.Nonce != saved.Nonce || strings.TrimSpace(id.Subject) == "" || len(id.Subject) > 255 {
			writeError(w, &p.Fault{Code: "oidc_claims", Message: "SSO 身份邮箱或校验信息无效", Status: 403})
			return
		}
		u, e := h.Auth.IdentityByEmail(claims.Email)
		subject := issuer + "|" + id.Subject
		if e != nil || u.Disabled || (u.OIDCSub != "" && u.OIDCSub != subject) {
			writeError(w, &p.Fault{Code: "oidc_membership", Message: "请先由管理员开通企业成员身份", Status: 403})
			return
		}
		if e = h.Service.Store.Update(r.Context(), func(t *p.Tx) error {
			if e := t.BindOIDCSubject(u.ID, subject); e != nil {
				return e
			}
			return t.Audit(identityActor(u), "auth.oidc", p.Resource{ID: u.ID}, p.M{})
		}); e != nil {
			writeError(w, e)
			return
		}
		session, _, e := h.Auth.newSession(u)
		if e != nil {
			writeError(w, e)
			return
		}
		h.Auth.SetCookie(w, session)
		http.SetCookie(w, &http.Cookie{Name: "is_oidc_state", Value: "", Path: "/auth/oidc", HttpOnly: true, Secure: h.Auth.Secure, SameSite: http.SameSiteLaxMode, MaxAge: -1})
		http.Redirect(w, r, "/", http.StatusFound)
	}
	return nil
}

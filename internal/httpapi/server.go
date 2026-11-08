package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	p "github.com/intelligence-substrate/platform/internal/platform"
	"github.com/intelligence-substrate/platform/internal/previewnode"
)

type Handler struct {
	Service                *p.Service
	Auth                   *Auth
	Static                 string
	PublicURL              string
	PreviewURL             string
	PreviewNode            *previewnode.Client
	previewCache           previewCache
	Integrations           func() []p.M
	GeneratePlan           func(context.Context, p.Actor, p.M) (p.Resource, error)
	GenerateClarifications func(context.Context, p.Actor, p.M) (p.Resource, error)
	GenerateRequirements   func(context.Context, p.Actor, p.M) (p.Resource, error)
	ModelCall              func(context.Context, p.Actor, p.M) (p.M, error)
	OIDCStart              http.HandlerFunc
	OIDCCallback           http.HandlerFunc
	mu                     sync.Mutex
	attempts               map[string][]time.Time
}

func New(service *p.Service, secure bool) *Handler {
	return &Handler{Service: service, Auth: &Auth{Store: service.Store, Service: service, Secure: secure}, Static: "web/dist", attempts: map[string][]time.Time{}}
}
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "same-origin")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
	defer func() {
		if e := recover(); e != nil {
			slog.Error("request panic", "path", r.URL.Path, "error", fmt.Sprint(e))
			writeError(w, &p.Fault{Code: "internal", Message: "操作未完成，请查看服务日志", Status: 500})
		}
	}()
	if r.URL.Path == "/healthz" {
		if e := h.Service.Store.DB.PingContext(r.Context()); e != nil {
			writeError(w, e)
		} else {
			writeJSON(w, 200, p.M{"status": "ok"})
		}
		return
	}
	if r.URL.Path == "/auth/oidc/start" && h.OIDCStart != nil {
		h.OIDCStart(w, r)
		return
	}
	if r.URL.Path == "/auth/oidc/callback" && h.OIDCCallback != nil {
		h.OIDCCallback(w, r)
		return
	}
	if r.Method != "GET" && r.Method != "HEAD" {
		if !h.validOrigin(r) {
			writeError(w, &p.Fault{Code: "origin", Message: "请求来源不受信任", Status: 403})
			return
		}
	}
	if r.URL.Path == "/api/v1/auth/login" && r.Method == "POST" {
		h.login(w, r)
		return
	}
	if r.URL.Path == "/api/v1/auth/options" && r.Method == "GET" {
		writeJSON(w, 200, p.M{"oidc": h.OIDCStart != nil})
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/api/") && !strings.HasPrefix(r.URL.Path, "/git/") && r.URL.Path != "/mcp" {
		h.static(w, r)
		return
	}
	a, csrf, bearer, e := h.Auth.Authenticate(r)
	if e != nil {
		if strings.HasPrefix(r.URL.Path, "/git/") {
			w.Header().Set("WWW-Authenticate", `Basic realm="Intelligence Substrate"`)
		}
		writeError(w, &p.Fault{Code: "unauthorized", Message: "请先登录", Status: 401})
		return
	}
	if r.Method != "GET" && r.Method != "HEAD" && !bearer && r.Header.Get("X-CSRF-Token") != csrf {
		writeError(w, &p.Fault{Code: "csrf", Message: "会话校验失败，请刷新后重试", Status: 403})
		return
	}
	if r.URL.Path == "/mcp" {
		h.mcp(w, r, a)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/git/") {
		if !bearer {
			writeError(w, &p.Fault{Code: "token_required", Message: "Git 使用项目访问令牌", Status: 403})
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 128<<20)
		h.Service.GitHTTP(a, w, r)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/")
	switch {
	case strings.HasPrefix(path, "projects/") && strings.HasSuffix(path, "/blueprints") && r.Method == "GET":
		id := strings.TrimSuffix(strings.TrimPrefix(path, "projects/"), "/blueprints")
		v, e := h.Service.BlueprintCatalog(r.Context(), a, id)
		reply(w, v, e)
	case strings.HasPrefix(path, "runs/") && strings.HasSuffix(path, "/blueprint") && r.Method == "POST":
		h.blueprintSeed(w, r, a, strings.TrimSuffix(strings.TrimPrefix(path, "runs/"), "/blueprint"))
	case strings.HasPrefix(path, "previews/") && strings.HasSuffix(path, "/access") && r.Method == "POST":
		h.previewAccess(w, r, a)
	case path == "auth/me" && r.Method == "GET":
		writeJSON(w, 200, Session{Actor: a, CSRF: csrf})
	case path == "auth/logout" && r.Method == "POST":
		h.Auth.Logout(r)
		http.SetCookie(w, &http.Cookie{Name: "is_session", Value: "", Path: "/", HttpOnly: true, Secure: h.Auth.Secure, SameSite: http.SameSiteStrictMode, MaxAge: -1})
		writeJSON(w, 200, p.M{"ok": true})
	case path == "identities" && r.Method == "GET":
		if !a.Admin || a.Kind != "human" {
			writeError(w, &p.Fault{Code: "forbidden", Message: "需要平台管理员", Status: 403})
			return
		}
		v, e := h.Auth.Identities()
		reply(w, v, e)
	case path == "identities" && r.Method == "POST":
		if !a.Admin || a.Kind != "human" {
			writeError(w, &p.Fault{Code: "forbidden", Message: "需要平台管理员", Status: 403})
			return
		}
		in, e := decode(w, r)
		if e != nil {
			writeError(w, e)
			return
		}
		u, e := h.Auth.CreateIdentity(p.S(in, "email"), p.S(in, "name"), p.S(in, "password"), false)
		if e == nil {
			e = h.Service.Store.Update(r.Context(), func(t *p.Tx) error {
				return t.Audit(a, "identities.create", p.Resource{ID: u.ID}, p.M{"email": u.Email})
			})
		}
		reply(w, identityActor(u), e)
	case path == "tokens" && r.Method == "POST":
		in, e := decode(w, r)
		if e != nil {
			writeError(w, e)
			return
		}
		token, actor, e := h.Auth.IssueToken(r.Context(), a, in)
		if e != nil {
			writeError(w, e)
			return
		}
		writeJSON(w, 201, p.M{"token": token, "actor": actor})
	case path == "tokens" && r.Method == "GET":
		v, e := h.Auth.Tokens(r.Context(), a, r.URL.Query().Get("projectId"))
		reply(w, v, e)
	case path == "tokens/revoke" && r.Method == "POST":
		in, e := decode(w, r)
		if e != nil {
			writeError(w, e)
			return
		}
		reply(w, p.M{"ok": true}, h.Auth.Revoke(r.Context(), a, in))
	case path == "identities/disable" && r.Method == "POST":
		in, e := decode(w, r)
		if e != nil {
			writeError(w, e)
			return
		}
		reply(w, p.M{"ok": true}, h.Auth.Disable(r.Context(), a, p.S(in, "id")))
	case strings.HasPrefix(path, "agent-steps/") && strings.HasSuffix(path, "/checkpoint") && r.Method == "POST":
		id := strings.TrimSuffix(strings.TrimPrefix(path, "agent-steps/"), "/checkpoint")
		h.agentCheckpoint(w, r, a, id)
	case strings.HasPrefix(path, "runs/") && strings.HasSuffix(path, "/agent-checkpoint") && r.Method == "GET":
		id := strings.TrimSuffix(strings.TrimPrefix(path, "runs/"), "/agent-checkpoint")
		h.agentCheckpoint(w, r, a, id)
	case strings.HasPrefix(path, "runs/") && strings.HasSuffix(path, "/checkpoint") && (r.Method == "POST" || r.Method == "GET"):
		id := strings.TrimSuffix(strings.TrimPrefix(path, "runs/"), "/checkpoint")
		h.workspaceCheckpoint(w, r, a, id)
	case path == "projects" && r.Method == "GET":
		v, e := h.Service.Projects(r.Context(), a)
		reply(w, v, e)
	case path == "teams" && r.Method == "GET":
		h.teams(w, r, a)
	case strings.HasPrefix(path, "projects/") && strings.HasSuffix(path, "/delivery-metrics") && r.Method == "GET":
		h.deliveryMetrics(w, r, a)
	case strings.HasPrefix(path, "projects/") && strings.HasSuffix(path, "/costs") && r.Method == "GET":
		id := strings.TrimSuffix(strings.TrimPrefix(path, "projects/"), "/costs")
		v, e := h.Service.DeliveryCosts(r.Context(), a, id, r.URL.Query().Get("goalId"))
		for i := range v.Charges {
			v.Charges[i] = sanitize(v.Charges[i])
		}
		for i := range v.Expenses {
			v.Expenses[i] = sanitize(v.Expenses[i])
		}
		reply(w, v, e)
	case strings.HasPrefix(path, "projects/") && strings.HasSuffix(path, "/workspace") && r.Method == "GET":
		id := strings.TrimSuffix(strings.TrimPrefix(path, "projects/"), "/workspace")
		v, e := h.Service.Workspace(r.Context(), a, id)
		if e != nil {
			writeError(w, e)
			return
		}
		safe := []p.Resource{}
		for _, x := range v {
			safe = append(safe, sanitize(x))
		}
		writeJSON(w, 200, safe)
	case strings.HasPrefix(path, "projects/") && strings.HasSuffix(path, "/acceptance-trace") && r.Method == "GET":
		h.acceptanceTrace(w, r, a)
	case strings.HasPrefix(path, "resources/") && r.Method == "GET":
		id := strings.TrimPrefix(path, "resources/")
		v, e := h.Service.GetResource(r.Context(), a, id)
		reply(w, sanitize(v), e)
	case strings.HasPrefix(path, "source-operations/") && r.Method == "GET":
		v, e := h.Service.InspectSourceOperation(r.Context(), a, strings.TrimPrefix(path, "source-operations/"))
		reply(w, v, e)
	case strings.HasPrefix(path, "commands/") && r.Method == "POST":
		in, e := decode(w, r)
		if e != nil {
			writeError(w, e)
			return
		}
		v, e := h.Service.Command(r.Context(), a, strings.TrimPrefix(path, "commands/"), in)
		if a.Kind != "worker" && strings.TrimPrefix(path, "commands/") != "messages.claim" {
			v = sanitize(v)
		}
		reply(w, v, e)
	case strings.HasPrefix(path, "repositories/") && strings.HasSuffix(path, "/snapshot") && r.Method == "POST":
		id := strings.TrimSuffix(strings.TrimPrefix(path, "repositories/"), "/snapshot")
		h.sourceSnapshot(w, r, a, id)
	case strings.HasPrefix(path, "repositories/") && strings.HasSuffix(path, "/archive") && r.Method == "GET":
		id := strings.TrimSuffix(strings.TrimPrefix(path, "repositories/"), "/archive")
		file, e := h.Service.SourceArchive(r.Context(), a, id, r.URL.Query().Get("commit"))
		if e != nil {
			writeError(w, e)
			return
		}
		defer os.Remove(file)
		w.Header().Set("Content-Type", "application/x-tar")
		http.ServeFile(w, r, file)
	case strings.HasPrefix(path, "repositories/") && strings.HasSuffix(path, "/refs") && r.Method == "GET":
		id := strings.TrimSuffix(strings.TrimPrefix(path, "repositories/"), "/refs")
		v, e := h.Service.RepositoryRefs(r.Context(), a, id)
		reply(w, v, e)
	case strings.HasPrefix(path, "repositories/") && r.Method == "GET":
		id := strings.TrimPrefix(path, "repositories/")
		v, e := h.Service.RepositoryView(r.Context(), a, id, r.URL.Query().Get("branch"), r.URL.Query().Get("path"))
		reply(w, v, e)
	case strings.HasPrefix(path, "releases/") && strings.HasSuffix(path, "/reports") && r.Method == "POST":
		h.releaseFeedback(w, r, a)
	case path == "artifacts/upload" && r.Method == "POST":
		meta := p.M{}
		for _, k := range []string{"goalId", "repositoryId", "commit", "target", "name", "type", "runId", "mediaType", "leaseToken", "deploymentId"} {
			meta[k] = r.URL.Query().Get(k)
		}
		r.Body = http.MaxBytesReader(w, r.Body, 512<<20+1)
		v, e := h.Service.Upload(r.Context(), a, meta, r.Body)
		reply(w, v, e)
	case strings.HasPrefix(path, "artifacts/") && strings.HasSuffix(path, "/content") && r.Method == "GET":
		id := strings.TrimSuffix(strings.TrimPrefix(path, "artifacts/"), "/content")
		v, e := h.Service.GetResource(r.Context(), a, id)
		if e != nil || v.Kind != "artifact" {
			writeError(w, &p.Fault{Code: "not_found", Message: "制品不存在", Status: 404})
			return
		}
		if e = h.Service.VerifyArtifact(v); e != nil {
			writeError(w, e)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filepath.Base(v.Title)))
		http.ServeFile(w, r, h.Service.ArtifactPath(p.S(v.Data, "digest")))
	case strings.HasPrefix(path, "artifacts/") && strings.HasSuffix(path, "/preview") && r.Method == "GET":
		id := strings.TrimSuffix(strings.TrimPrefix(path, "artifacts/"), "/preview")
		artifact, e := h.Service.GetResource(r.Context(), a, id)
		if e != nil || artifact.Kind != "artifact" || p.S(artifact.Data, "type") != "prototype" || p.N(artifact.Data, "size") > 2<<20 {
			writeError(w, &p.Fault{Code: "invalid_preview", Message: "仅支持小于 2 MiB 的独立 HTML 原型", Status: 422})
			return
		}
		if e = h.Service.VerifyArtifact(artifact); e != nil {
			writeError(w, e)
			return
		}
		w.Header().Set("Content-Security-Policy", "sandbox allow-scripts; default-src 'none'; script-src 'unsafe-inline' blob:; style-src 'unsafe-inline'; img-src data: blob:; connect-src 'none'; form-action 'none'; frame-ancestors 'self'; base-uri 'none'; frame-src 'none'")
		w.Header().Del("X-Frame-Options")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Disposition", "inline")
		body, e := os.ReadFile(h.Service.ArtifactPath(p.S(artifact.Data, "digest")))
		if e != nil {
			writeError(w, e)
			return
		}
		// The parent frame policy blocks document navigation as well as resource
		// requests. A CSP on a standalone document cannot prevent self-navigation.
		_, _ = fmt.Fprintf(w, `<!doctype html><html lang="zh-CN"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>原型预览</title><style>html,body,iframe{width:100%%;height:100%%;margin:0;border:0}iframe{display:block}</style><iframe title="原型预览" sandbox="allow-scripts" referrerpolicy="no-referrer" srcdoc="%s"></iframe></html>`, html.EscapeString(string(body)))
	case path == "audit" && r.Method == "GET":
		project := r.URL.Query().Get("projectId")
		if e := h.Service.CanAccess(r.Context(), a, project); e != nil {
			writeError(w, e)
			return
		}
		v, e := h.Service.Store.Audits(project)
		reply(w, v, e)
	case path == "events" && r.Method == "GET":
		h.events(w, r, a)
	case path == "integrations" && r.Method == "GET":
		out := []p.M{}
		if h.Integrations != nil {
			out = h.Integrations()
		}
		previewStatus, previewReason := "unconfigured", "尚未配置独立开发预览入口"
		if h.PreviewURL != "" {
			previewStatus, previewReason = "configured", "静态 Web 开发预览入口已配置；实际入口连通性需在部署环境验证"
		}
		out = append(out, p.M{"id": "development-previews", "title": "开发预览", "status": previewStatus, "reason": previewReason, "fullstackConfigured": h.PreviewNode != nil, "verified": false})
		writeJSON(w, 200, out)
	case path == "plans/generate" && r.Method == "POST":
		in, e := decode(w, r)
		if e != nil {
			writeError(w, e)
			return
		}
		if h.GeneratePlan == nil {
			writeError(w, &p.Fault{Code: "integration_unavailable", Message: "尚未配置模型，仍可人工提交交付方案", Status: 424})
			return
		}
		v, e := h.GeneratePlan(r.Context(), a, in)
		reply(w, v, e)
	case path == "requirements/generate" && r.Method == "POST":
		in, e := decode(w, r)
		if e != nil {
			writeError(w, e)
			return
		}
		if h.GenerateRequirements == nil {
			writeError(w, &p.Fault{Code: "integration_unavailable", Message: "尚未配置模型，可人工整理需求稿", Status: 424})
			return
		}
		v, e := h.GenerateRequirements(r.Context(), a, in)
		reply(w, v, e)
	case path == "clarifications/generate" && r.Method == "POST":
		in, e := decode(w, r)
		if e != nil {
			writeError(w, e)
			return
		}
		if h.GenerateClarifications == nil {
			writeError(w, &p.Fault{Code: "integration_unavailable", Message: "尚未配置模型，可人工提出需求澄清", Status: 424})
			return
		}
		v, e := h.GenerateClarifications(r.Context(), a, in)
		reply(w, v, e)
	case path == "model/chat" && r.Method == "POST":
		if h.ModelCall == nil {
			writeError(w, &p.Fault{Code: "integration_unavailable", Message: "模型网关未配置", Status: 424})
			return
		}
		in, e := decode(w, r)
		if e != nil {
			writeError(w, e)
			return
		}
		v, e := h.ModelCall(r.Context(), a, in)
		reply(w, v, e)
	case path == "metrics" && r.Method == "GET":
		project := r.URL.Query().Get("projectId")
		items, e := h.Service.Workspace(r.Context(), a, project)
		if e != nil {
			writeError(w, e)
			return
		}
		counts := p.M{}
		spent := 0.0
		for _, x := range items {
			key := x.Kind + ":" + x.Status
			counts[key] = p.N(counts, key) + 1
			if x.Kind == "goal" {
				spent += p.N(x.Data, "spentCents")
			}
		}
		writeJSON(w, 200, p.M{"counts": counts, "spentCents": spent})
	default:
		writeError(w, &p.Fault{Code: "not_found", Message: "接口不存在", Status: 404})
	}
}
func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	h.mu.Lock()
	now := time.Now()
	cut := now.Add(-5 * time.Minute)
	// Remote addresses are untrusted cardinality. Reclaim expired buckets and
	// reject new ones at capacity rather than retaining every peer forever.
	if _, exists := h.attempts[host]; !exists && len(h.attempts) >= 4096 {
		for peer, attempts := range h.attempts {
			if len(attempts) == 0 || !attempts[len(attempts)-1].After(cut) {
				delete(h.attempts, peer)
			}
		}
		if len(h.attempts) >= 4096 {
			h.mu.Unlock()
			writeError(w, &p.Fault{Code: "rate_limit", Message: "登录尝试过多，请稍后重试", Status: 429})
			return
		}
	}
	past := []time.Time{}
	for _, v := range h.attempts[host] {
		if v.After(cut) {
			past = append(past, v)
		}
	}
	h.attempts[host] = past
	if len(past) >= 10 {
		h.mu.Unlock()
		writeError(w, &p.Fault{Code: "rate_limit", Message: "登录尝试过多，请稍后重试", Status: 429})
		return
	}
	h.attempts[host] = append(past, now)
	h.mu.Unlock()
	in, e := decode(w, r)
	if e != nil {
		writeError(w, e)
		return
	}
	token, s, e := h.Auth.Login(p.S(in, "email"), p.S(in, "password"))
	if e != nil {
		writeError(w, e)
		return
	}
	// Reserve before checking credentials to bound concurrent work, but only
	// failed logins should consume the window. Keep concurrent failures intact.
	h.mu.Lock()
	for i, attempt := range h.attempts[host] {
		if attempt.Equal(now) {
			h.attempts[host] = append(h.attempts[host][:i], h.attempts[host][i+1:]...)
			break
		}
	}
	if len(h.attempts[host]) == 0 {
		delete(h.attempts, host)
	}
	h.mu.Unlock()
	h.Auth.SetCookie(w, token)
	writeJSON(w, 200, s)
}
func (h *Handler) validOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, e := url.Parse(origin)
	if e != nil {
		return false
	}
	if h.PublicURL != "" {
		expected, e := url.Parse(h.PublicURL)
		return e == nil && u.Scheme == expected.Scheme && u.Host == expected.Host
	}
	return u.Host == r.Host
}
func (h *Handler) static(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" && r.Method != "HEAD" {
		http.NotFound(w, r)
		return
	}
	clean := filepath.Clean("/" + r.URL.Path)
	path := filepath.Join(h.Static, clean)
	if st, e := os.Stat(path); e == nil && !st.IsDir() {
		http.ServeFile(w, r, path)
		return
	}
	index := filepath.Join(h.Static, "index.html")
	if _, e := os.Stat(index); e != nil {
		writeError(w, &p.Fault{Code: "ui_not_built", Message: "工作台尚未构建，请运行 make build", Status: 503})
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	http.ServeFile(w, r, index)
}
func decode(w http.ResponseWriter, r *http.Request) (p.M, error) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	defer r.Body.Close()
	d := json.NewDecoder(r.Body)
	var v p.M
	if e := d.Decode(&v); e != nil || v == nil {
		return nil, &p.Fault{Code: "invalid_json", Message: "请求必须为有效 JSON 对象，且不超过 1 MiB", Status: 400}
	}
	if e := d.Decode(new(any)); e != io.EOF {
		return nil, &p.Fault{Code: "invalid_json", Message: "请求只能包含一个 JSON 对象", Status: 400}
	}
	return v, nil
}
func sanitize(v p.Resource) p.Resource {
	if v.Data == nil {
		return v
	}
	b, _ := json.Marshal(v.Data)
	v.Data = p.M{}
	_ = json.Unmarshal(b, &v.Data)
	delete(v.Data, "leaseToken")
	delete(v.Data, "completionDigest")
	return v
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func writeError(w http.ResponseWriter, e error) {
	var f *p.Fault
	if errors.As(e, &f) {
		writeJSON(w, f.Status, p.M{"error": f})
		return
	}
	slog.Error("API failure", "error", e)
	writeJSON(w, 500, p.M{"error": p.M{"code": "internal", "message": "操作未完成，请查看服务日志"}})
}
func reply(w http.ResponseWriter, v any, e error) {
	if e != nil {
		writeError(w, e)
	} else {
		writeJSON(w, 200, v)
	}
}

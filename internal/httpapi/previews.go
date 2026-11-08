package httpapi

import (
	"bytes"
	"fmt"
	"html"
	"mime"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	p "github.com/intelligence-substrate/platform/internal/platform"
	"github.com/intelligence-substrate/platform/internal/webpreview"
)

type previewCache struct {
	mu     sync.Mutex
	digest string
	bundle *webpreview.Bundle
}

// ConfigurePreviews is startup-only. An empty pair disables the optional
// gateway. Different hostnames keep platform host-only cookies off previews.
func (h *Handler) ConfigurePreviews(listen, publicURL string, dev bool) error {
	if listen == "" && publicURL == "" {
		h.PreviewURL = ""
		return nil
	}
	if listen == "" || publicURL == "" {
		return fmt.Errorf("IS_PREVIEW_LISTEN 和 IS_PREVIEW_URL 必须同时配置")
	}
	u, err := url.Parse(publicURL)
	control, controlErr := url.Parse(h.PublicURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Path != "" && u.Path != "/") || controlErr != nil || control.Hostname() == "" || strings.EqualFold(strings.TrimSuffix(u.Hostname(), "."), strings.TrimSuffix(control.Hostname(), ".")) {
		return fmt.Errorf("预览 URL 必须为与控制面不同 hostname 的独立 origin")
	}
	if !previewHostname(u.Hostname()) {
		return fmt.Errorf("预览 hostname 无效")
	}
	if u.Scheme != "https" {
		ip := net.ParseIP(u.Hostname())
		loopback := u.Hostname() == "localhost" || (ip != nil && ip.IsLoopback())
		if u.Scheme != "http" || !dev || !loopback {
			return fmt.Errorf("预览入口需要 HTTPS；仅本地开发允许 loopback HTTP")
		}
	}
	if _, _, err = net.SplitHostPort(listen); err != nil {
		return fmt.Errorf("预览 listen 需要明确 host:port")
	}
	h.PreviewURL = strings.TrimSuffix(publicURL, "/")
	return nil
}

func previewHostname(host string) bool {
	if net.ParseIP(host) != nil {
		return true
	}
	if len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(strings.TrimSuffix(host, "."), ".") {
		if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

func (h *Handler) previewAccess(w http.ResponseWriter, r *http.Request, a p.Actor) {
	w.Header().Set("Cache-Control", "no-store")
	if h.PreviewURL == "" {
		writeError(w, &p.Fault{Code: "unconfigured", Message: "开发预览入口尚未配置，请联系平台管理员", Status: 409})
		return
	}
	id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/previews/"), "/access")
	if !p.IDPattern.MatchString(id) || r.URL.RawQuery != "" {
		writeError(w, &p.Fault{Code: "invalid", Message: "预览访问参数无效", Status: 422})
		return
	}
	in, err := decode(w, r)
	if err != nil {
		writeError(w, err)
		return
	}
	if len(in) != 0 {
		writeError(w, &p.Fault{Code: "invalid", Message: "预览访问请求不接受额外参数", Status: 422})
		return
	}
	access, err := h.Service.IssuePreviewAccess(r.Context(), a, id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, p.M{"url": h.PreviewURL + "/p/" + access.Token + "/", "expiresAt": access.ExpiresAt, "previewId": id})
}

func (h *Handler) previewBundle(artifact p.Resource) (*webpreview.Bundle, error) {
	digest := p.S(artifact.Data, "digest")
	h.previewCache.mu.Lock()
	defer h.previewCache.mu.Unlock()
	if h.previewCache.digest == digest && h.previewCache.bundle != nil {
		return h.previewCache.bundle, nil
	}
	bundle, err := webpreview.Read(h.Service.ArtifactPath(digest), digest, int64(p.N(artifact.Data, "size")))
	if err != nil {
		return nil, err
	}
	h.previewCache.digest, h.previewCache.bundle = digest, bundle
	return bundle, nil
}

// PreviewGateway has no auth/session/control-plane route or proxy. Its only
// authority is the exact, short-lived preview credential in a private URL.
func (h *Handler) PreviewGateway() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), usb=(), display-capture=(), clipboard-read=(), clipboard-write=()")
		u, err := url.Parse(h.PreviewURL)
		if err != nil || h.PreviewURL == "" || r.Host != u.Host || r.URL.RawQuery != "" || r.URL.RawPath != "" {
			http.NotFound(w, r)
			return
		}
		parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/p/"), "/", 2)
		if !strings.HasPrefix(r.URL.Path, "/p/") || len(parts) != 2 || !p.IDPattern.MatchString(parts[0]) {
			http.NotFound(w, r)
			return
		}
		preview, artifact, err := h.Service.ResolvePreviewAccess(r.Context(), parts[0])
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("X-Substrate-Preview-ID", preview.ID)
		w.Header().Set("X-Substrate-Artifact-SHA256", p.S(artifact.Data, "digest"))
		w.Header().Set("X-Substrate-Commit", p.S(artifact.Data, "commit"))
		w.Header().Set("X-Substrate-Requirement-Revision", strconv.Itoa(int(p.N(preview.Data, "requirementRevision"))))
		base := h.PreviewURL + "/p/" + parts[0] + "/"
		if strings.HasPrefix(parts[1], "api/") {
			h.previewAPI(w, r, parts[0], parts[1], preview, artifact)
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" {
			w.Header().Set("Allow", "GET, HEAD")
			w.WriteHeader(405)
			return
		}
		if parts[1] == "status" {
			writeJSON(w, 200, p.M{"status": "active", "previewId": preview.ID})
			return
		}
		if parts[1] == "" {
			nonce := p.Secret()
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'nonce-"+nonce+"'; style-src 'unsafe-inline'; connect-src 'self'; frame-src "+base+"files/; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
			if r.Method == "HEAD" {
				return
			}
			_, _ = fmt.Fprintf(w, `<!doctype html><html lang="zh-CN"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>%s · 开发预览</title><style>html,body{height:100%%;margin:0;background:#f6f7f9;color:#1b2533;font:14px system-ui}body{display:flex;flex-direction:column}header{padding:10px 16px;border-bottom:1px solid #d8dde4;display:flex;gap:16px;flex-wrap:wrap}header span{color:#586577}iframe{flex:1;width:100%%;border:0;background:white}p{padding:24px}</style><header><strong>%s</strong><span>开发预览 · 需求 v%d · %s · 到期 %s</span></header><iframe title="应用开发预览" sandbox="allow-scripts" referrerpolicy="no-referrer" src="%sfiles/index.html"></iframe><script nonce="%s">setInterval(async()=>{try{const r=await fetch(location.pathname+'status',{credentials:'omit',cache:'no-store'});if(r.ok)return}catch{}const f=document.querySelector('iframe');if(f){f.remove();const p=document.createElement('p');p.textContent='预览已停止或访问已过期，请返回工作台重新打开。';document.body.append(p)}},15000)</script></html>`, html.EscapeString(preview.Title), html.EscapeString(preview.Title), int(p.N(preview.Data, "requirementRevision")), html.EscapeString(p.S(artifact.Data, "commit")[:12]), html.EscapeString(p.S(preview.Data, "expiresAt")), html.EscapeString(base), nonce)
			return
		}
		if !strings.HasPrefix(parts[1], "files/") {
			http.NotFound(w, r)
			return
		}
		name := strings.TrimPrefix(parts[1], "files/")
		bundle, err := h.previewBundle(artifact)
		if err != nil {
			http.Error(w, "预览制品无法验证，请返回工作台核对", 409)
			return
		}
		body, ok := bundle.File(name)
		if !ok {
			http.NotFound(w, r)
			return
		}
		mediaType := mime.TypeByExtension(path.Ext(name))
		if mediaType == "" {
			mediaType = "application/octet-stream"
		}
		if r.Header.Get("Sec-Fetch-Dest") == "document" {
			http.Error(w, "请从开发预览入口打开应用", 403)
			return
		}
		if strings.HasPrefix(mediaType, "text/html") || strings.HasPrefix(mediaType, "application/xhtml+xml") {
			// The parent constrains iframe navigation, which a document CSP alone
			// cannot do. Browser top-level loads must not bypass that parent.
			if r.Header.Get("Sec-Fetch-Dest") != "iframe" {
				http.Error(w, "请从开发预览入口打开应用", 403)
				return
			}
		}
		w.Header().Set("Content-Type", mediaType)
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Cross-Origin-Resource-Policy", "cross-origin")
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		connections := base + "files/"
		if p.S(preview.Data, "runtimeProfile") != "" {
			connections += " " + base + "api/"
		}
		w.Header().Set("Content-Security-Policy", "sandbox allow-scripts; default-src 'none'; script-src "+base+"files/ 'unsafe-inline' 'wasm-unsafe-eval'; style-src "+base+"files/ 'unsafe-inline'; img-src "+base+"files/ data: blob:; font-src "+base+"files/ data:; connect-src "+connections+"; frame-src 'none'; worker-src 'none'; object-src 'none'; base-uri "+base+"files/; form-action 'none'; frame-ancestors 'self'")
		http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(body))
	})
}

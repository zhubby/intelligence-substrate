package httpapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	p "github.com/intelligence-substrate/platform/internal/platform"
	"github.com/intelligence-substrate/platform/internal/previewnode"
)

func (h *Handler) ConfigurePreviewNode(socket string) error {
	if socket == "" {
		h.PreviewNode = nil
		h.Service.PreviewRuntimeAvailable = false
		return nil
	}
	if h.PreviewURL == "" {
		return fmt.Errorf("preview runtime requires a separately configured preview gateway")
	}
	client, err := previewnode.NewClient(socket)
	if err != nil {
		return err
	}
	h.PreviewNode = client
	h.Service.PreviewRuntimeAvailable = true
	return nil
}
func (h *Handler) previewRuntimeBinding(preview, artifact p.Resource) (previewnode.Binding, []byte, error) {
	bundle, err := h.previewBundle(artifact)
	if err != nil {
		return previewnode.Binding{}, nil, err
	}
	runtime := bundle.Runtime()
	if runtime == nil {
		return previewnode.Binding{}, nil, fmt.Errorf("static preview has no business API")
	}
	expires, err := time.Parse(time.RFC3339Nano, p.S(preview.Data, "expiresAt"))
	if err != nil {
		return previewnode.Binding{}, nil, err
	}
	return previewnode.Binding{PreviewID: preview.ID, Binding: p.S(preview.Data, "binding"), ArtifactDigest: p.S(artifact.Data, "digest"), ExpiresAt: expires, Runtime: *runtime}, bundle.Backend(), nil
}
func (h *Handler) previewAPI(w http.ResponseWriter, r *http.Request, token, path string, preview, artifact p.Resource) {
	if h.PreviewNode == nil || p.S(preview.Data, "runtimeProfile") == "" {
		http.NotFound(w, r)
		return
	}
	reject := func(status int, message string) { writeJSON(w, status, p.M{"error": message}) }
	if r.Header.Get("Origin") != "" && r.Header.Get("Origin") != "null" || r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" || r.Header.Get("Upgrade") != "" {
		reject(403, "预览接口不接受外部来源或身份凭据")
		return
	}
	w.Header().Set("Access-Control-Allow-Origin", "null")
	w.Header().Set("Vary", "Origin")
	b, binary, err := h.previewRuntimeBinding(preview, artifact)
	if err != nil {
		reject(409, "预览制品无法核对")
		return
	}
	q := previewnode.Request{Method: r.Method, Path: "/" + path}
	if r.Method == "OPTIONS" {
		allowed := previewnode.Request{Method: "GET", Path: q.Path}.Valid(b.Runtime.Mode) || previewnode.Request{Method: "POST", Path: q.Path, Body: []byte(`{}`)}.Valid(b.Runtime.Mode)
		requested := r.Header.Get("Access-Control-Request-Method")
		header := strings.ToLower(strings.TrimSpace(r.Header.Get("Access-Control-Request-Headers")))
		if !allowed || (requested != "GET" && requested != "POST") || (header != "" && header != "content-type") {
			reject(403, "预览接口预检不受支持")
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", "null")
		w.Header().Set("Vary", "Origin")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.WriteHeader(204)
		return
	}
	if r.Method != "GET" && r.Method != "POST" {
		w.Header().Set("Allow", "GET, POST, OPTIONS")
		reject(405, "预览接口不支持此方法")
		return
	}
	if r.Method == "POST" {
		media, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if e != nil || media != "application/json" {
			reject(415, "预览接口需要 JSON")
			return
		}
	}
	q.Body, err = io.ReadAll(http.MaxBytesReader(w, r.Body, previewnode.MaxRequest))
	if err != nil {
		reject(413, "预览请求超过限制")
		return
	}
	if !q.Valid(b.Runtime.Mode) {
		reject(422, "预览接口路径或内容无效")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	if err = h.PreviewNode.Ensure(ctx, b, binary); err != nil {
		reject(503, "预览执行暂不可用或已回收，请创建新预览或联系管理员核对")
		return
	}
	// Recheck authority after cold startup, before any business side effect.
	if _, _, err = h.Service.ResolvePreviewAccess(ctx, token); err != nil {
		http.NotFound(w, r)
		return
	}
	response, err := h.PreviewNode.Request(ctx, b, q)
	if err != nil {
		reject(502, "业务请求结果不明，请刷新核对后再操作")
		return
	}
	w.Header().Set("Access-Control-Allow-Origin", "null")
	w.Header().Set("Vary", "Origin")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(response.Status)
	_, _ = w.Write(response.Body)
}
func (h *Handler) ReconcilePreviewRuntimes(ctx context.Context) error {
	if h.PreviewNode == nil {
		return nil
	}
	instances, err := h.PreviewNode.List(ctx)
	if err != nil {
		return err
	}
	var failures []error
	for _, instance := range instances {
		valid, err := h.Service.PreviewRuntimeCurrent(ctx, instance.Binding.PreviewID, instance.Binding.Binding)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if valid && instance.State == "running" {
			err = h.PreviewNode.Renew(ctx, instance.Binding)
		} else {
			err = h.PreviewNode.Stop(ctx, instance.Binding)
		}
		if err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

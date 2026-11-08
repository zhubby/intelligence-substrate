package httpapi

import (
	"io"
	"mime"
	"net/http"
	"strconv"

	p "github.com/intelligence-substrate/platform/internal/platform"
	"github.com/intelligence-substrate/platform/internal/source"
)

func (h *Handler) agentCheckpoint(w http.ResponseWriter, r *http.Request, a p.Actor, id string) {
	leases := r.Header.Values("X-IS-Lease")
	if r.URL.RawQuery != "" || len(leases) != 1 || leases[0] == "" || len(leases[0]) > 1024 {
		writeError(w, &p.Fault{Code: "invalid_metadata", Message: "需要唯一且有界的租约头，不允许查询参数", Status: 400})
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == "GET" {
		file, checkpoint, e := h.Service.AgentResumeCheckpoint(r.Context(), a, id, leases[0])
		if e != nil {
			writeError(w, e)
			return
		}
		defer file.Close()
		w.Header().Set("Content-Type", "application/x-tar")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Disposition", `attachment; filename="agent-workspace.tar"`)
		w.Header().Set("Content-Length", strconv.FormatInt(int64(p.N(checkpoint.Data, "size")), 10))
		_, _ = io.Copy(w, file)
		return
	}
	media, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if e != nil || media != "application/x-tar" {
		writeError(w, &p.Fault{Code: "media_type", Message: "Agent 工作区必须使用 application/x-tar", Status: 415})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, source.MaxBytes)
	defer r.Body.Close()
	checkpoint, e := h.Service.UploadAgentCheckpoint(r.Context(), a, id, leases[0], r.Body)
	reply(w, sanitize(checkpoint), e)
}

package httpapi

import (
	"io"
	"net/http"

	p "github.com/intelligence-substrate/platform/internal/platform"
)

func (h *Handler) blueprintSeed(w http.ResponseWriter, r *http.Request, a p.Actor, runID string) {
	if r.URL.RawQuery != "" {
		writeError(w, &p.Fault{Code: "invalid", Message: "蓝图归档接口不接受查询参数", Status: 422})
		return
	}
	in, err := decode(w, r)
	if err != nil {
		writeError(w, err)
		return
	}
	file, err := h.Service.OpenBlueprintSeed(r.Context(), a, runID, in)
	if err != nil {
		writeError(w, err)
		return
	}
	defer file.Close()
	w.Header().Set("Content-Type", "application/x-tar")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.Copy(w, file)
}

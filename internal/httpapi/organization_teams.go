package httpapi

import (
	"net/http"
	"net/url"

	p "github.com/intelligence-substrate/platform/internal/platform"
)

func (h *Handler) teams(w http.ResponseWriter, r *http.Request, a p.Actor) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeError(w, &p.Fault{Code: "invalid", Message: "团队目录参数无效", Status: 400})
		return
	}
	project := ""
	for key, values := range query {
		if key != "projectId" || len(values) != 1 || !p.IDPattern.MatchString(values[0]) {
			writeError(w, &p.Fault{Code: "invalid", Message: "仅支持单个有效的 projectId 参数", Status: 400})
			return
		}
		project = values[0]
	}
	v, err := h.Service.Teams(r.Context(), a, project)
	reply(w, v, err)
}

package httpapi

import (
	"net/http"
	"strconv"
	"strings"

	p "github.com/intelligence-substrate/platform/internal/platform"
)

func (h *Handler) acceptanceTrace(w http.ResponseWriter, r *http.Request, actor p.Actor) {
	query := r.URL.Query()
	if len(query["goalId"]) != 1 || query.Get("goalId") == "" || len(query["revision"]) > 1 {
		writeError(w, &p.Fault{Code: "invalid", Message: "需要唯一的目标与需求版本参数", Status: 400})
		return
	}
	revision := 0
	if values, present := query["revision"]; present {
		value, err := strconv.Atoi(values[0])
		if err != nil || value <= 0 {
			writeError(w, &p.Fault{Code: "invalid", Message: "需求版本必须为正整数", Status: 400})
			return
		}
		revision = value
	}
	projectID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/projects/"), "/acceptance-trace")
	report, err := h.Service.AcceptanceTrace(r.Context(), actor, projectID, query.Get("goalId"), revision)
	reply(w, report, err)
}

package httpapi

import (
	"net/http"
	"net/url"
	"strings"

	p "github.com/intelligence-substrate/platform/internal/platform"
)

func (h *Handler) releaseFeedback(w http.ResponseWriter, r *http.Request, a p.Actor) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeError(w, feedbackQueryError())
		return
	}
	if len(q) != 3 {
		writeError(w, feedbackQueryError())
		return
	}
	for key, values := range q {
		if !p.Has([]string{"artifactId", "name", "key"}, key) || len(values) != 1 || strings.TrimSpace(values[0]) == "" {
			writeError(w, feedbackQueryError())
			return
		}
	}
	id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/releases/"), "/reports")
	r.Body = http.MaxBytesReader(w, r.Body, p.ReleaseReportLimit+1)
	result, err := h.Service.UploadReleaseReport(r.Context(), a, p.M{"releaseId": id, "artifactId": q.Get("artifactId"), "name": q.Get("name"), "key": q.Get("key")}, r.Body)
	reply(w, result, err)
}

func feedbackQueryError() error {
	return &p.Fault{Code: "invalid", Message: "问题报告需要唯一的制品、文件名和幂等键", Status: 400}
}

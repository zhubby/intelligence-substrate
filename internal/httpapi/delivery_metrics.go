package httpapi

import (
	"math"
	"net/http"
	"strconv"
	"strings"

	p "github.com/intelligence-substrate/platform/internal/platform"
)

func metricQueryError() error {
	return &p.Fault{Code: "invalid", Message: "需要唯一的目标与正整数需求版本；历史版本需要指定目标", Status: 400}
}

func (h *Handler) deliveryMetrics(w http.ResponseWriter, r *http.Request, a p.Actor) {
	q := r.URL.Query()
	for key, values := range q {
		if !p.Has([]string{"goalId", "revision"}, key) || len(values) != 1 || strings.TrimSpace(values[0]) == "" {
			writeError(w, metricQueryError())
			return
		}
	}
	rev := 0
	if value, exists := q["revision"]; exists {
		n, err := strconv.Atoi(value[0])
		if err != nil || n <= 0 || n > 1e9 || q.Get("goalId") == "" {
			writeError(w, metricQueryError())
			return
		}
		rev = n
	}
	project := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/projects/"), "/delivery-metrics")
	report, err := h.Service.DeliveryMetrics(r.Context(), a, project, q.Get("goalId"), rev)
	reply(w, report, err)
}

func metricArguments(args p.M) (string, string, int, error) {
	for key, value := range args {
		if !p.Has([]string{"projectId", "goalId", "revision"}, key) {
			return "", "", 0, metricQueryError()
		}
		if key != "revision" {
			if _, ok := value.(string); !ok || p.S(args, key) == "" {
				return "", "", 0, metricQueryError()
			}
		}
	}
	if p.S(args, "projectId") == "" {
		return "", "", 0, metricQueryError()
	}
	rev := 0
	if value, exists := args["revision"]; exists {
		n, ok := value.(float64)
		if !ok || math.IsNaN(n) || math.IsInf(n, 0) || n < 1 || n > 1e9 || math.Trunc(n) != n || p.S(args, "goalId") == "" {
			return "", "", 0, metricQueryError()
		}
		rev = int(n)
	}
	return p.S(args, "projectId"), p.S(args, "goalId"), rev, nil
}

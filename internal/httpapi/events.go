package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	p "github.com/intelligence-substrate/platform/internal/platform"
)

// Event IDs are durable audit IDs. Reconnecting clients can replay from the last
// observed event; authorization is checked again on each polling cycle.
func (h *Handler) events(w http.ResponseWriter, r *http.Request, a p.Actor) {
	project := r.URL.Query().Get("projectId")
	if e := h.Service.CanAccess(r.Context(), a, project); e != nil {
		writeError(w, e)
		return
	}
	after := r.URL.Query().Get("after")
	if after == "" {
		after = r.Header.Get("Last-Event-ID")
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, &p.Fault{Code: "unsupported", Message: "事件流不可用", Status: 500})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	deadline := time.NewTimer(10 * time.Minute)
	defer deadline.Stop()
	for {
		if e := h.Service.CanAccess(r.Context(), a, project); e != nil {
			return
		}
		if _, _, _, e := h.Auth.Authenticate(r); e != nil {
			return
		}
		events, e := h.Service.Store.Audits(project)
		if e != nil {
			return
		}
		start := 0
		if after != "" {
			found := false
			for i, event := range events {
				if event.ID == after {
					start = i + 1
					found = true
					break
				}
			}
			if !found {
				_, _ = fmt.Fprint(w, "event: reset\ndata: {\"reason\":\"cursor_not_found\"}\n\n")
				flusher.Flush()
				return
			}
		}
		for _, event := range events[start:] {
			b, _ := json.Marshal(event)
			if _, e = fmt.Fprintf(w, "id: %s\nevent: audit\ndata: %s\n\n", event.ID, b); e != nil {
				return
			}
			after = event.ID
		}
		_, _ = fmt.Fprint(w, ": heartbeat\n\n")
		flusher.Flush()
		select {
		case <-r.Context().Done():
			return
		case <-deadline.C:
			return
		case <-ticker.C:
		}
	}
}

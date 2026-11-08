package previewnode

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

type envelope struct {
	Binding Binding `json:"binding"`
	Backend []byte  `json:"backend,omitempty"`
	Request Request `json:"request,omitempty"`
}

func NewHandler(e *Engine) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		reply := func(status int, value any) { w.WriteHeader(status); _ = json.NewEncoder(w).Encode(value) }
		if r.URL.RawQuery != "" || r.URL.RawPath != "" {
			reply(404, map[string]string{"error": "unknown route"})
			return
		}
		if r.Method == "GET" && r.URL.Path == "/v1/instances" {
			reply(200, e.List())
			return
		}
		if r.Method != "POST" || !strings.HasPrefix(r.URL.Path, "/v1/") {
			reply(404, map[string]string{"error": "unknown route"})
			return
		}
		limit := int64(32 << 10)
		if r.URL.Path == "/v1/ensure" {
			limit = 48 << 20
		}
		var in envelope
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
		dec.DisallowUnknownFields()
		if dec.Decode(&in) != nil || dec.Decode(new(any)) != io.EOF {
			reply(422, map[string]string{"error": "invalid RPC payload"})
			return
		}
		var err error
		var value any = map[string]bool{"ok": true}
		switch r.URL.Path {
		case "/v1/ensure":
			value, err = e.Ensure(r.Context(), in.Binding, in.Backend)
		case "/v1/request":
			value, err = e.Request(r.Context(), in.Binding, in.Request)
		case "/v1/renew":
			err = e.Renew(r.Context(), in.Binding)
		case "/v1/stop":
			err = e.Stop(r.Context(), in.Binding)
		default:
			reply(404, map[string]string{"error": "unknown route"})
			return
		}
		if err != nil {
			reply(409, map[string]string{"error": err.Error()})
			return
		}
		reply(200, value)
	})
}

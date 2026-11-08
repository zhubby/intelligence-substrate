package distribution

import (
	"encoding/json"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

func (s *Store) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		if r.Method != "GET" && r.Method != "HEAD" {
			w.Header().Set("Allow", "GET, HEAD")
			w.WriteHeader(405)
			return
		}
		if r.URL.Path == "/healthz" {
			if err := s.db.PingContext(r.Context()); err != nil {
				http.Error(w, "unavailable", 503)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
			return
		}
		p := r.URL.Path
		if strings.ContainsAny(p, "\\\x00:") || path.Clean(p) != strings.TrimSuffix(p, "/") {
			http.NotFound(w, r)
			return
		}
		parts := strings.Split(strings.Trim(p, "/"), "/")
		if len(parts) == 4 && parts[0] == "updates" && strings.HasSuffix(parts[3], ".json") {
			var feed string
			if err := s.db.QueryRowContext(r.Context(), "SELECT feed FROM channels WHERE app=? AND target=? AND channel=?", parts[1], parts[2], strings.TrimSuffix(parts[3], ".json")).Scan(&feed); err != nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Cache-Control", "no-cache, max-age=0")
			w.Header().Set("Content-Type", "application/json")
			if r.Method != "HEAD" {
				_, _ = w.Write([]byte(feed))
			}
			return
		}
		if len(parts) == 5 && parts[0] == "downloads" {
			release, err := s.Lookup(r.Context(), parts[3])
			if err != nil || release.App != parts[1] || release.Target != parts[2] || release.Filename != parts[4] {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Disposition", `attachment; filename="`+release.Filename+`"`)
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("ETag", `"`+release.Digest+`"`)
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			http.ServeFile(w, r, filepath.Join(s.root, "blobs", release.Digest))
			return
		}
		if len(parts) >= 3 && parts[0] == "apps" && parts[2] == "web" {
			release, err := s.Current(r.Context(), parts[1], "web", "stable")
			if err != nil {
				http.NotFound(w, r)
				return
			}
			name := strings.Join(parts[3:], "/")
			if name == "" {
				name = "index.html"
			}
			for _, part := range strings.Split(name, "/") {
				if strings.HasPrefix(part, ".") {
					http.NotFound(w, r)
					return
				}
			}
			file := filepath.Join(s.root, "sites", release.Digest, filepath.FromSlash(name))
			stat, err := os.Stat(file)
			if err != nil || !stat.Mode().IsRegular() {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Cache-Control", "no-cache, max-age=0")
			w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'; base-uri 'self'")
			w.Header().Set("X-Substrate-Release-ID", release.ReleaseID)
			w.Header().Set("X-Substrate-Artifact-SHA256", release.Digest)
			http.ServeFile(w, r, file)
			return
		}
		http.NotFound(w, r)
	})
}

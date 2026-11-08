package runtimehost

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"
)

// Gateway never executes Docker commands or reads environment/credential files.
// It is run separately with read-only access to runtime.db.
func Gateway(store *Store) http.Handler {
	transport := &http.Transport{Proxy: nil, ResponseHeaderTimeout: 15 * time.Second, IdleConnTimeout: 60 * time.Second, MaxIdleConns: 100}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d, e := store.Active(r.Context())
		if e != nil {
			http.Error(w, "Application has no active deployment", 503)
			return
		}
		if r.URL.Path == "/__substrate/health" {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			w.Write([]byte(`{"status":"ready"}`))
			return
		}
		address, err := backendAddress(d)
		if err != nil {
			http.Error(w, "Application routing unavailable", 503)
			return
		}
		target, _ := url.Parse("http://" + address)
		proxy := httputil.NewSingleHostReverseProxy(target)
		proxy.Transport = transport
		proxy.ModifyResponse = func(response *http.Response) error {
			response.Header.Set("X-Substrate-Release-ID", d.ReleaseID)
			response.Header.Set("X-Substrate-Artifact-SHA256", d.ArtifactDigest)
			response.Header.Set("X-Substrate-Activation-ID", store.Config.external(d.ID))
			return nil
		}
		proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) {
			http.Error(w, "Application temporarily unavailable", 503)
		}
		r.Body = http.MaxBytesReader(w, r.Body, 16<<20)
		proxy.ServeHTTP(w, r)
	})
}

func backendAddress(d Deployment) (string, error) {
	host := d.Address
	if host == "" {
		host = "127.0.0.1"
	}
	ip := net.ParseIP(host)
	if d.Port < 1 || d.Port > 65535 || ip == nil || (!ip.IsPrivate() && !ip.IsLoopback()) {
		return "", fmt.Errorf("backend must be a managed private IP endpoint")
	}
	return net.JoinHostPort(host, fmt.Sprint(d.Port)), nil
}

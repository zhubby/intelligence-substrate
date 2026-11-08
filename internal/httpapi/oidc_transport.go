package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"time"

	"golang.org/x/oauth2"
)

func validOIDCHTTPSURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.Fragment == "" && u.Opaque == ""
}

type oidcTLSTransport struct{ next http.RoundTripper }

func (t oidcTLSTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	// The client checks every request, including redirects, before the underlying
	// transport can send authorization codes or credentials to an unsafe URL.
	if r.URL == nil || !validOIDCHTTPSURL(r.URL.String()) {
		return nil, errors.New("OIDC requires an absolute HTTPS URL without userinfo or fragment")
	}
	return t.next.RoundTrip(r)
}

func oidcHTTPClient(ctx context.Context, secure bool) *http.Client {
	configured, _ := ctx.Value(oauth2.HTTPClient).(*http.Client)
	if configured == nil {
		configured = http.DefaultClient
	}
	// Keep the operator's trust, proxy, cookie and redirect settings without
	// mutating a shared client. A shorter configured timeout stays in force.
	client := *configured
	if client.Timeout <= 0 || client.Timeout > 15*time.Second {
		client.Timeout = 15 * time.Second
	}
	if secure {
		transport := client.Transport
		if transport == nil {
			transport = http.DefaultTransport
		}
		client.Transport = oidcTLSTransport{next: transport}
	}
	return &client
}

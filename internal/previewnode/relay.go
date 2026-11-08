package previewnode

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Relay has exactly one possible peer inside network=none. No request headers,
// URL authority, proxy environment, redirects or retries cross this boundary.
func Relay(ctx context.Context, transport http.RoundTripper, mode string, q Request) (Response, error) {
	if !q.Valid(mode) {
		return Response{}, fmt.Errorf("invalid preview API")
	}
	if transport == nil {
		transport = &http.Transport{Proxy: nil, DisableKeepAlives: true}
	}
	client := &http.Client{Transport: transport, Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request, err := http.NewRequestWithContext(ctx, q.Method, "http://127.0.0.1:8081"+q.Path, bytes.NewReader(q.Body))
	if err != nil {
		return Response{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return Response{}, fmt.Errorf("preview API response unknown")
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, MaxResponse+1))
	result := Response{response.StatusCode, body}
	if err != nil || !result.Valid() {
		return Response{}, fmt.Errorf("invalid or oversized preview API response")
	}
	return result, nil
}

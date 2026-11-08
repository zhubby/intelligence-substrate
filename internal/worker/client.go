package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	p "github.com/intelligence-substrate/platform/internal/platform"
)

type Client struct {
	URL, Token string
	HTTP       *http.Client
}
type APIError struct {
	Code, Message string
	Status        int
}

func (e *APIError) Error() string { return fmt.Sprintf("%s (HTTP %d)", e.Message, e.Status) }
func NewClient(base, token string, allowHTTP bool) (*Client, error) {
	u, e := url.Parse(base)
	if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(allowHTTP && u.Scheme == "http")) {
		return nil, fmt.Errorf("worker requires an HTTPS platform URL, or explicit local HTTP opt-in")
	}
	if token == "" {
		return nil, fmt.Errorf("worker token is missing")
	}
	return &Client{URL: strings.TrimRight(base, "/"), Token: strings.TrimSpace(token), HTTP: &http.Client{Timeout: 150 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (c *Client) request(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	return c.requestHeaders(ctx, method, path, body, nil)
}
func (c *Client) requestHeaders(ctx context.Context, method, path string, body io.Reader, headers map[string]string) (*http.Response, error) {
	r, e := http.NewRequestWithContext(ctx, method, c.URL+"/api/v1/"+path, body)
	if e != nil {
		return nil, e
	}
	r.Header.Set("Authorization", "Bearer "+c.Token)
	r.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		r.Header.Set(key, value)
	}
	response, e := c.HTTP.Do(r)
	if e != nil {
		return nil, e
	}
	if response.StatusCode >= 300 {
		defer response.Body.Close()
		var result struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result)
		return nil, &APIError{Code: result.Error.Code, Message: result.Error.Message, Status: response.StatusCode}
	}
	return response, nil
}
func (c *Client) JSON(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, e := json.Marshal(in)
		if e != nil {
			return e
		}
		body = bytes.NewReader(b)
	}
	r, e := c.request(ctx, method, path, body)
	if e != nil {
		return e
	}
	defer r.Body.Close()
	return json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(out)
}
func (c *Client) Command(ctx context.Context, name string, in p.M) (p.Resource, error) {
	var r p.Resource
	e := c.JSON(ctx, "POST", "commands/"+name, in, &r)
	return r, e
}

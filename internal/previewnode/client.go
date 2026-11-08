package previewnode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"time"
)

type Client struct{ http *http.Client }

func NewClient(socket string) (*Client, error) {
	if !filepath.IsAbs(socket) || filepath.Clean(socket) != socket || len(socket) > 100 {
		return nil, fmt.Errorf("preview socket must be an absolute local Unix socket path, at most 100 bytes")
	}
	transport := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	return &Client{http: &http.Client{Transport: transport, Timeout: 25 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (c *Client) call(ctx context.Context, path string, in *envelope, out any) error {
	var body io.Reader
	method := "GET"
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
		method = "POST"
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://preview-node/v1/"+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("预览执行节点不可用或结果不明")
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil || len(raw) >= 2<<20 {
		return conflict()
	}
	if response.StatusCode != 200 {
		return conflict()
	}
	if out != nil && json.Unmarshal(raw, out) != nil {
		return conflict()
	}
	return nil
}
func (c *Client) Ensure(ctx context.Context, b Binding, binary []byte) error {
	return c.call(ctx, "ensure", &envelope{Binding: b, Backend: binary}, nil)
}
func (c *Client) Request(ctx context.Context, b Binding, r Request) (Response, error) {
	var result Response
	err := c.call(ctx, "request", &envelope{Binding: b, Request: r}, &result)
	if err == nil && !result.Valid() {
		err = conflict()
	}
	return result, err
}
func (c *Client) List(ctx context.Context) ([]Record, error) {
	var result []Record
	err := c.call(ctx, "instances", nil, &result)
	return result, err
}
func (c *Client) Renew(ctx context.Context, b Binding) error {
	return c.call(ctx, "renew", &envelope{Binding: b}, nil)
}
func (c *Client) Stop(ctx context.Context, b Binding) error {
	return c.call(ctx, "stop", &envelope{Binding: b}, nil)
}

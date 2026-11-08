package publisher

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

const huaweiAPIOrigin = "https://connect-api.cloud.huawei.com"

type huaweiClient struct {
	engine   *Engine
	clientID string
	token    string
}

type huaweiReply struct {
	Ret json.RawMessage `json:"ret"`
}

var huaweiClientIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)

func (e *Engine) huaweiClient(ctx context.Context, p Profile) (*huaweiClient, error) {
	body, err := readPrivateFile(p.CredentialsFile)
	if err != nil {
		return nil, err
	}
	if stat, err := os.Stat(p.CredentialsFile); err != nil || stat.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("Huawei API client credentials must be private to their service owner")
	}
	var credentials struct {
		ClientID     string `json:"clientId"`
		ClientSecret string `json:"clientSecret"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&credentials) != nil || !huaweiClientIDPattern.MatchString(credentials.ClientID) || len(credentials.ClientSecret) < 8 || len(credentials.ClientSecret) > 4096 || strings.ContainsAny(credentials.ClientSecret, "\r\n\x00") {
		return nil, fmt.Errorf("invalid Huawei API client credentials; Service Account and OAuth clients are not supported by this driver")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return nil, fmt.Errorf("Huawei credentials must contain one JSON object")
	}
	input := map[string]string{"grant_type": "client_credentials", "client_id": credentials.ClientID, "client_secret": credentials.ClientSecret}
	var response struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		TokenType   string `json:"token_type"`
	}
	client := &huaweiClient{engine: e, clientID: credentials.ClientID}
	if err := client.request(ctx, "POST", "/api/oauth2/v1/token", input, &response, false); err != nil {
		return nil, err
	}
	if len(response.AccessToken) == 0 || len(response.AccessToken) > 16384 || response.ExpiresIn < 60 || response.ExpiresIn > 604800 || (response.TokenType != "" && !strings.EqualFold(response.TokenType, "Bearer")) {
		return nil, fmt.Errorf("Huawei token response is invalid")
	}
	for _, ch := range response.AccessToken {
		if ch < 33 || ch > 126 {
			return nil, fmt.Errorf("Huawei token response is invalid")
		}
	}
	client.token = response.AccessToken
	return client, nil
}

func (c *huaweiClient) request(ctx context.Context, method, path string, input, out any, authorized bool) error {
	var body io.Reader
	if input != nil {
		b, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, huaweiAPIOrigin+path, body)
	if err != nil {
		return fmt.Errorf("invalid Huawei control-plane request")
	}
	req.Header.Set("Content-Type", "application/json")
	if authorized {
		req.Header.Set("client_id", c.clientID)
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	// No cookie jar or redirect callback is inherited from a supplied client.
	transport := c.engine.HTTP.Transport
	if method == "GET" && strings.HasPrefix(path, "/api/publish/v2/upload-url/for-obs?") {
		// This GET allocates an upload object. The standard transport otherwise
		// retries a lost response on a reused connection because GET is normally
		// replayable. Use a fresh HTTP/1 connection for this one reservation.
		if transport == nil {
			transport = http.DefaultTransport
		}
		standard, ok := transport.(*http.Transport)
		if !ok {
			return fmt.Errorf("Huawei upload reservation requires a standard isolated HTTP transport")
		}
		isolated := standard.Clone()
		isolated.DisableKeepAlives = true
		isolated.Protocols = new(http.Protocols)
		isolated.Protocols.SetHTTP1(true)
		transport = isolated
		defer isolated.CloseIdleConnections()
	}
	client := &http.Client{Transport: transport, Timeout: 45 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("Huawei request interrupted; inspect the persisted stage before any mutation")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("Huawei API returned HTTP %d; inspect the operation and channel prerequisites", response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
	if err != nil || len(raw) > 2<<20 {
		return fmt.Errorf("oversized or incomplete Huawei API response")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if decoder.Decode(out) != nil {
		return fmt.Errorf("invalid Huawei API response")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return fmt.Errorf("oversized or trailing Huawei API response")
	}
	return nil
}

func (c *huaweiClient) call(ctx context.Context, method, path string, input any) (huaweiResponse, error) {
	var response huaweiResponse
	if err := c.request(ctx, method, path, input, &response, true); err != nil {
		return response, err
	}
	// Some official tables describe ret as a JSON string; examples use an object.
	ret := response.Ret
	if len(ret) > 0 && ret[0] == '"' {
		var value string
		if json.Unmarshal(ret, &value) != nil {
			return response, fmt.Errorf("invalid Huawei result code")
		}
		ret = []byte(value)
	}
	var result struct {
		Code *int `json:"code"`
	}
	if json.Unmarshal(ret, &result) != nil || result.Code == nil {
		return response, fmt.Errorf("Huawei response is missing a valid result code")
	}
	if *result.Code != 0 {
		return response, fmt.Errorf("Huawei API result code %d; inspect channel prerequisites", *result.Code)
	}
	return response, nil
}

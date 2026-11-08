package publisher

import (
	"context"
	"crypto/hmac"
	"crypto/md5" // Required only for the OPPO wire receipt; approvals use SHA256.
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const oppoOrigin = "https://oop-openapi-cn.heytapmobi.com"

type oppoClient struct {
	engine                  *Engine
	clientID, secret, token string
}

func (e *Engine) oppoClient(ctx context.Context, p Profile) (*oppoClient, error) {
	body, err := readOppoPrivateFile(p.CredentialsFile)
	if err != nil {
		return nil, err
	}
	var credentials struct {
		ClientID string `json:"clientId"`
		Secret   string `json:"clientSecret"`
	}
	if err = decodeOppoJSON(body, &credentials, true); err != nil {
		return nil, err
	}
	if (len(credentials.ClientID) != 18 && len(credentials.ClientID) != 19) || len(credentials.Secret) != 64 || strings.ContainsAny(credentials.ClientID+credentials.Secret, " \t\r\n\x00") || !utf8ASCII(credentials.ClientID+credentials.Secret) {
		return nil, fmt.Errorf("OPPO requires an 18/19-character client ID and a private 64-character client secret")
	}
	c := &oppoClient{engine: e, clientID: credentials.ClientID, secret: credentials.Secret}
	credentialHash := sha256.Sum256([]byte(credentials.ClientID + ":" + credentials.Secret))
	digest := hex.EncodeToString(credentialHash[:])
	// Acquire SQLite's write reservation before reading the cache. This serializes
	// token issuance across CLI processes without rotating a token for every poll.
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO oppo_tokens(client_id,credential_hash,access_token,expires_at) VALUES(?,'','',0)", c.clientID); err != nil {
		return nil, err
	}
	var previous, token string
	var expiry int64
	if err = tx.QueryRowContext(ctx, "SELECT credential_hash,access_token,expires_at FROM oppo_tokens WHERE client_id=?", c.clientID).Scan(&previous, &token, &expiry); err != nil {
		return nil, err
	}
	now := e.huaweiNow()
	if previous == digest && validOppoToken(token) && expiry > now.Add(5*time.Minute).Unix() && expiry <= now.Add(49*time.Hour).Unix() {
		c.token = token
		return c, tx.Commit()
	}
	var reply struct {
		Token  string `json:"access_token"`
		Expiry int64  `json:"expire_in"`
	}
	endpoint := oppoOrigin + "/developer/v1/token?" + url.Values{"client_id": {c.clientID}, "client_secret": {c.secret}}.Encode()
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("invalid OPPO token request")
	}
	if err = c.send(req, &reply, false); err != nil {
		return nil, err
	}
	if !validOppoToken(reply.Token) || reply.Expiry <= now.Add(5*time.Minute).Unix() || reply.Expiry > now.Add(49*time.Hour).Unix() {
		return nil, fmt.Errorf("OPPO token is invalid or has no valid absolute expiry")
	}
	if _, err = tx.ExecContext(ctx, "UPDATE oppo_tokens SET credential_hash=?,access_token=?,expires_at=? WHERE client_id=?", digest, reply.Token, reply.Expiry, c.clientID); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	c.token = reply.Token
	return c, nil
}

func utf8ASCII(s string) bool {
	for _, r := range s {
		if r < 33 || r > 126 {
			return false
		}
	}
	return true
}
func validOppoToken(s string) bool { return len(s) >= 16 && len(s) <= 8192 && utf8ASCII(s) }

func (c *oppoClient) signed(values url.Values) url.Values {
	values.Set("access_token", c.token)
	values.Set("timestamp", strconv.FormatInt(c.engine.huaweiNow().Unix(), 10))
	keys := make([]string, 0, len(values))
	for k := range values {
		if k != "api_sign" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+values.Get(k))
	}
	h := hmac.New(sha256.New, []byte(c.secret))
	_, _ = h.Write([]byte(strings.Join(parts, "&")))
	values.Set("api_sign", hex.EncodeToString(h.Sum(nil)))
	return values
}

func (c *oppoClient) call(ctx context.Context, method, path string, values url.Values, out any) error {
	allowed := map[string]string{"/resource/v1/app/info": "GET", "/resource/v1/upload/get-upload-url": "GET", "/resource/v1/app/upd": "POST", "/resource/v1/app/task-state": "POST"}
	if allowed[path] != method {
		return fmt.Errorf("unsupported OPPO endpoint")
	}
	values = c.signed(values)
	endpoint := oppoOrigin + path
	var body io.Reader
	if method == "GET" {
		endpoint += "?" + values.Encode()
	} else {
		body = strings.NewReader(values.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return fmt.Errorf("invalid OPPO business request")
	}
	// Deliberately remove GetBody: a transport cannot replay a mutating request.
	req.GetBody = nil
	if method == "POST" {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	}
	return c.send(req, out, false)
}

func (c *oppoClient) send(req *http.Request, out any, upload bool) error {
	transport := c.engine.HTTP.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	standard, ok := transport.(*http.Transport)
	if !ok {
		return fmt.Errorf("OPPO requires a standard isolated HTTP transport")
	}
	isolated := standard.Clone()
	isolated.DisableKeepAlives = true
	isolated.Protocols = new(http.Protocols)
	isolated.Protocols.SetHTTP1(true)
	defer isolated.CloseIdleConnections()
	timeout := 45 * time.Second
	if upload {
		timeout = 5 * time.Minute
	}
	client := &http.Client{Transport: isolated, Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("OPPO request interrupted; inspect the persisted stage before any mutation")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("OPPO API returned HTTP %d; do not replay an uncertain write", response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return fmt.Errorf("OPPO response is incomplete or oversized")
	}
	var envelope struct {
		Errno *int            `json:"errno"`
		Data  json.RawMessage `json:"data"`
	}
	if err = decodeOppoJSON(raw, &envelope, false); err != nil {
		return err
	}
	if envelope.Errno == nil {
		return fmt.Errorf("OPPO response is missing its result code")
	}
	if *envelope.Errno != 0 {
		return fmt.Errorf("OPPO API errno %d; inspect prerequisites without replaying a write", *envelope.Errno)
	}
	return decodeOppoJSON(envelope.Data, out, false)
}

type oppoUploadReply struct {
	URL       string `json:"url"`
	MD5       string `json:"md5"`
	Size      int64  `json:"file_size"`
	Extension string `json:"file_extension"`
}

func (c *oppoClient) upload(ctx context.Context, p Profile, file oppoFile, md5sum string, beforeUpload func() error) (oppoUploadReply, error) {
	var reply oppoUploadReply
	var ticket struct {
		URL  string `json:"upload_url"`
		Sign string `json:"sign"`
	}
	if err := c.call(ctx, "GET", "/resource/v1/upload/get-upload-url", url.Values{}, &ticket); err != nil {
		return reply, err
	}
	u, err := url.Parse(ticket.URL)
	allowed := false
	if err == nil {
		for _, host := range p.UploadHosts {
			if u.Host == host {
				allowed = true
			}
		}
	}
	if err != nil || !allowed || u.Scheme != "https" || u.User != nil || u.Fragment != "" || u.Opaque != "" || u.RawQuery != "" || u.Path == "" || strings.ContainsAny(ticket.URL, ",\r\n\x00") || len(ticket.URL) > 8192 || len(ticket.Sign) < 8 || len(ticket.Sign) > 4096 || !utf8ASCII(ticket.Sign) {
		return reply, fmt.Errorf("OPPO upload ticket is invalid or outside the exact administrator HTTPS allowlist")
	}
	// The API's common signed parameters also apply to multipart uploads. They
	// reach only the explicitly trusted upload host; client_secret is never sent.
	params := c.signed(url.Values{"type": {file.Type}, "sign": {ticket.Sign}})
	query := url.Values{}
	for _, name := range []string{"access_token", "timestamp", "api_sign"} {
		query.Set(name, params.Get(name))
	}
	u.RawQuery = query.Encode()
	body, err := os.CreateTemp("", "is-oppo-upload-")
	if err != nil {
		return reply, err
	}
	defer os.Remove(body.Name())
	defer body.Close()
	writer := multipart.NewWriter(body)
	for _, name := range []string{"type", "sign"} {
		header := textproto.MIMEHeader{}
		header.Set("Content-Disposition", `form-data; name="`+name+`"`)
		header.Set("Content-Type", "text/plain; charset=utf-8")
		part, err := writer.CreatePart(header)
		if err != nil {
			return reply, err
		}
		if _, err = io.WriteString(part, params.Get(name)); err != nil {
			return reply, err
		}
	}
	in, err := os.Open(file.Path)
	if err != nil {
		return reply, err
	}
	part, err := writer.CreateFormFile("file", file.Filename)
	if err != nil {
		in.Close()
		return reply, err
	}
	h := md5.New()
	n, copyErr := io.Copy(io.MultiWriter(part, h), io.LimitReader(in, file.Bytes+1))
	closeErr := in.Close()
	if copyErr != nil || closeErr != nil || n != file.Bytes || hex.EncodeToString(h.Sum(nil)) != md5sum {
		return reply, fmt.Errorf("OPPO staged file changed before upload")
	}
	if err = writer.Close(); err != nil {
		return reply, err
	}
	length, err := body.Seek(0, io.SeekCurrent)
	if err != nil {
		return reply, err
	}
	if _, err = body.Seek(0, io.SeekStart); err != nil {
		return reply, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", u.String(), io.NopCloser(body))
	if err != nil {
		return reply, fmt.Errorf("invalid OPPO upload request")
	}
	req.ContentLength = length
	req.Header.Set("Content-Type", writer.FormDataContentType())
	if err = beforeUpload(); err != nil {
		return reply, err
	}
	if err = c.send(req, &reply, true); err != nil {
		return reply, err
	}
	returned, err := url.Parse(reply.URL)
	if err != nil || (returned.Scheme != "https" && returned.Scheme != "http") || returned.Hostname() == "" || returned.User != nil || returned.Fragment != "" || returned.Opaque != "" || strings.ContainsAny(reply.URL, ",\r\n\x00") || strings.Contains(reply.URL, c.token) || strings.Contains(reply.URL, c.secret) || len(reply.URL) > 8192 || reply.Size != file.Bytes || reply.MD5 != md5sum || reply.Extension != strings.TrimPrefix(filepath.Ext(file.Filename), ".") {
		return reply, fmt.Errorf("OPPO upload receipt differs from the approved bytes or has an invalid resource URL")
	}
	return reply, nil
}

func oppoFileMD5(name string) (string, error) {
	f, err := os.Open(name)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := md5.New()
	if _, err = io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

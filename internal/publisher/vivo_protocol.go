package publisher

import (
	"context"
	"crypto/hmac"
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
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type vivoClient struct {
	engine         *Engine
	endpoint       string
	access, secret string
}

func (e *Engine) vivoClient(p Profile) (*vivoClient, error) {
	body, err := readVivoPrivateFile(p.CredentialsFile)
	if err != nil {
		return nil, err
	}
	var credentials struct {
		Access string `json:"accessKey"`
		Secret string `json:"accessSecret"`
	}
	if err = decodeVivoJSON(body, &credentials, true); err != nil {
		return nil, err
	}
	if len(credentials.Access) < 8 || len(credentials.Access) > 512 || len(credentials.Secret) < 16 || len(credentials.Secret) > 4096 || !utf8ASCII(credentials.Access+credentials.Secret) {
		return nil, fmt.Errorf("vivo requires the private environment-specific accessKey and accessSecret")
	}
	endpoint := "https://developer-api.vivo.com.cn/router/rest"
	if p.Environment == "sandbox" {
		endpoint = "https://sandbox-developer-api.vivo.com.cn/router/rest"
	} else if p.Environment != "production" {
		return nil, fmt.Errorf("vivo environment is not configured")
	}
	return &vivoClient{engine: e, endpoint: endpoint, access: credentials.Access, secret: credentials.Secret}, nil
}

func (c *vivoClient) call(ctx context.Context, method string, values url.Values, file *vivoFile, out any) error {
	switch method {
	case "app.query.details", "app.sync.update.app":
		if file != nil {
			return fmt.Errorf("vivo business endpoint cannot carry an unmodeled upload")
		}
	case "app.upload.apk.app", "app.upload.icon", "app.upload.screenshot":
		if file == nil {
			return fmt.Errorf("vivo upload requires staged bytes")
		}
	default:
		return fmt.Errorf("unsupported vivo method")
	}
	values.Set("method", method)
	values.Set("access_key", c.access)
	values.Set("timestamp", strconv.FormatInt(c.engine.huaweiNow().UnixMilli(), 10))
	values.Set("v", "1.0")
	values.Set("format", "json")
	values.Set("sign_method", "HMAC-SHA256")
	values.Set("target_app_key", "developer")
	keys := make([]string, 0, len(values))
	for key := range values {
		if key != "sign" && key != "file" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	for _, key := range keys {
		pairs = append(pairs, key+"="+values.Get(key))
	}
	h := hmac.New(sha256.New, []byte(c.secret))
	_, _ = h.Write([]byte(strings.Join(pairs, "&")))
	values.Set("sign", hex.EncodeToString(h.Sum(nil)))
	var reader io.Reader = strings.NewReader(values.Encode())
	contentType := "application/x-www-form-urlencoded; charset=utf-8"
	var size int64
	if file != nil {
		body, err := os.CreateTemp("", "is-vivo-request-")
		if err != nil {
			return err
		}
		defer os.Remove(body.Name())
		defer body.Close()
		writer := multipart.NewWriter(body)
		for key, entries := range values {
			if len(entries) != 1 {
				return fmt.Errorf("vivo does not permit ambiguous request values")
			}
			header := make(textproto.MIMEHeader)
			header.Set("Content-Disposition", `form-data; name="`+key+`"`)
			header.Set("Content-Type", "text/plain; charset=utf-8")
			part, err := writer.CreatePart(header)
			if err != nil {
				return err
			}
			if _, err = io.WriteString(part, entries[0]); err != nil {
				return err
			}
		}
		in, err := os.Open(file.Path)
		if err != nil {
			return err
		}
		part, err := writer.CreateFormFile("file", file.Filename)
		if err == nil {
			_, err = io.Copy(part, in)
		}
		_ = in.Close()
		if err != nil {
			return err
		}
		if err = writer.Close(); err != nil {
			return err
		}
		if size, err = body.Seek(0, io.SeekCurrent); err != nil {
			return err
		}
		if _, err = body.Seek(0, io.SeekStart); err != nil {
			return err
		}
		reader, contentType = io.NopCloser(body), writer.FormDataContentType()
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.endpoint, reader)
	if err != nil {
		return fmt.Errorf("invalid vivo request")
	}
	req.GetBody = nil
	if file != nil {
		req.ContentLength = size
	}
	req.Header.Set("Content-Type", contentType)
	transport := c.engine.HTTP.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	standard, ok := transport.(*http.Transport)
	if !ok {
		return fmt.Errorf("vivo requires a standard isolated HTTP transport")
	}
	isolated := standard.Clone()
	isolated.DisableKeepAlives = true
	isolated.Protocols = new(http.Protocols)
	isolated.Protocols.SetHTTP1(true)
	defer isolated.CloseIdleConnections()
	timeout := 45 * time.Second
	if file != nil {
		timeout = 5 * time.Minute
	}
	client := &http.Client{Transport: isolated, Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("vivo request interrupted; inspect the persisted intent without replaying a write")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("vivo API returned HTTP %d; do not replay an uncertain write", response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return fmt.Errorf("vivo response is incomplete or oversized")
	}
	var envelope struct {
		Code    *int            `json:"code"`
		SubCode *string         `json:"subCode"`
		Data    json.RawMessage `json:"data"`
	}
	if err = decodeVivoJSON(raw, &envelope, false); err != nil {
		return err
	}
	if envelope.Code == nil || *envelope.Code != 0 || envelope.SubCode == nil || *envelope.SubCode != "0" {
		// Do not expose arbitrary upstream subcodes, messages or credentials.
		return fmt.Errorf("vivo API or business result is not explicitly successful; inspect without replaying a write")
	}
	if out != nil {
		return decodeVivoJSON(envelope.Data, out, false)
	}
	return nil
}

var vivoSerialPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,127}$`)

type vivoUploadReply struct {
	PackageName  string `json:"packageName"`
	SerialNumber string `json:"serialnumber"`
	VersionCode  int64  `json:"versionCode"`
	VersionName  string `json:"versionName"`
	FileMD5      string `json:"fileMd5"`
}

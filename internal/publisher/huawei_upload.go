package publisher

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type huaweiUpload struct {
	ObjectID string            `json:"objectId"`
	URL      string            `json:"url"`
	Method   string            `json:"method"`
	Headers  map[string]string `json:"headers"`
}

func (e *Engine) huaweiUploadBytes(ctx context.Context, c *huaweiClient, p Profile, r Request, artifact string, upload *huaweiUpload) error {
	if upload == nil || upload.Method != "PUT" || !huaweiObjectPattern.MatchString(upload.ObjectID) || len(upload.URL) > 16384 {
		return fmt.Errorf("Huawei upload reservation is incomplete")
	}
	u, err := url.Parse(upload.URL)
	allowed := false
	if err == nil {
		for _, host := range p.UploadHosts {
			if host == u.Host {
				allowed = true
			}
		}
	}
	if err != nil || !allowed || u.Scheme != "https" || u.User != nil || u.Fragment != "" || u.Opaque != "" || u.Host == "connect-api.cloud.huawei.com" || strings.Contains(upload.URL, c.token) {
		return fmt.Errorf("Huawei upload destination is outside the administrator OBS HTTPS allowlist")
	}
	headers := map[string]string{}
	for name, value := range upload.Headers {
		lower := strings.ToLower(name)
		permitted := lower == "authorization" || lower == "host" || lower == "content-type" || lower == "user-agent" || lower == "x-amz-content-sha256" || lower == "x-amz-date"
		if !permitted || headers[lower] != "" || len(value) == 0 || len(value) > 8192 || strings.ContainsAny(name+value, "\r\n\x00") || strings.Contains(value, c.token) {
			return fmt.Errorf("Huawei OBS upload header is not permitted")
		}
		headers[lower] = value
	}
	authorization := headers["authorization"]
	storageScheme := strings.HasPrefix(authorization, "AWS4-HMAC-SHA256 ") || strings.HasPrefix(authorization, "OBS ") || strings.HasPrefix(authorization, "AWS ")
	date, dateErr := time.Parse("20060102T150405Z", headers["x-amz-date"])
	if len(headers) != 6 || !storageScheme || strings.Contains(authorization, c.token) || headers["host"] != u.Host || headers["x-amz-content-sha256"] != r.ArtifactDigest || headers["content-type"] != "application/octet-stream" || dateErr != nil || date.Before(e.huaweiNow().Add(-5*time.Minute)) || date.After(e.huaweiNow().Add(time.Minute)) {
		return fmt.Errorf("Huawei OBS authorization, host, digest or reservation time is invalid")
	}
	file, err := os.Open(artifact)
	if err != nil {
		return err
	}
	defer file.Close()
	req, err := http.NewRequestWithContext(ctx, "PUT", upload.URL, io.LimitReader(file, r.ArtifactSize))
	if err != nil {
		return fmt.Errorf("invalid Huawei byte upload request")
	}
	req.ContentLength, req.Host = r.ArtifactSize, u.Host
	for name, value := range headers {
		if name != "host" {
			req.Header.Set(name, value)
		}
	}
	client := &http.Client{Transport: e.HTTP.Transport, Timeout: 5 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("Huawei byte upload interrupted; reconcile the persisted stage without replay")
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("Huawei byte upload returned HTTP %d; reconcile without replay", res.StatusCode)
	}
	return nil
}

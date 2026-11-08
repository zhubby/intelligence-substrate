package publisher

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

type appleHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}
type appleUploadOperation struct {
	Method         string        `json:"method"`
	URL            string        `json:"url"`
	Offset         int64         `json:"offset"`
	Length         int64         `json:"length"`
	RequestHeaders []appleHeader `json:"requestHeaders"`
	Expiration     string        `json:"expiration"`
}

func validateAppleUploadOperations(p Profile, operations []appleUploadOperation, size int64) error {
	if len(operations) == 0 || len(operations) > 10000 || size < 1 {
		return fmt.Errorf("complete bounded Apple upload operations required")
	}
	sorted := append([]appleUploadOperation(nil), operations...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Offset < sorted[j].Offset })
	offset := int64(0)
	for _, op := range sorted {
		if op.Method != "PUT" || op.Offset != offset || op.Length <= 0 || op.Length > size-offset {
			return fmt.Errorf("Apple upload ranges must cover the approved artifact exactly once")
		}
		offset += op.Length
		u, err := url.Parse(op.URL)
		allowed := false
		if err == nil {
			for _, host := range p.UploadHosts {
				if u.Host == host {
					allowed = true
				}
			}
		}
		if err != nil || !allowed || u.Scheme != "https" || u.User != nil || u.Fragment != "" || u.Opaque != "" || len(op.URL) > 16384 {
			return fmt.Errorf("Apple upload destination is outside the administrator HTTPS host allowlist")
		}
		if op.Expiration != "" {
			expiry, err := time.Parse(time.RFC3339, op.Expiration)
			if err != nil || !expiry.After(time.Now()) {
				return fmt.Errorf("Apple upload operation has expired")
			}
		}
		seen := map[string]bool{}
		for _, header := range op.RequestHeaders {
			name := strings.ToLower(header.Name)
			if len(name) == 0 || len(header.Value) > 8192 || strings.ContainsAny(header.Name+header.Value, "\r\n") || seen[name] {
				return fmt.Errorf("invalid Apple upload header")
			}
			allowed := name == "content-type" || name == "content-md5" || name == "content-disposition" || strings.HasPrefix(name, "x-amz-") || strings.HasPrefix(name, "x-apple-") || strings.HasPrefix(name, "x-ms-")
			if !allowed {
				return fmt.Errorf("Apple upload header is not permitted")
			}
			seen[name] = true
		}
	}
	if offset != size {
		return fmt.Errorf("Apple upload operations do not cover the complete approved artifact")
	}
	return nil
}

func (e *Engine) appleUploadBytes(ctx context.Context, p Profile, artifact string, operations []appleUploadOperation, size int64, guards ...func() error) error {
	if err := validateAppleUploadOperations(p, operations, size); err != nil {
		return err
	}
	file, err := os.Open(artifact)
	if err != nil {
		return err
	}
	defer file.Close()
	client := &http.Client{Transport: e.HTTP.Transport, Timeout: 5 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, op := range operations {
		for _, guard := range guards {
			if guard != nil {
				if err := guard(); err != nil {
					return err
				}
			}
		}
		req, err := http.NewRequestWithContext(ctx, "PUT", op.URL, io.NewSectionReader(file, op.Offset, op.Length))
		if err != nil {
			return fmt.Errorf("invalid Apple upload request")
		}
		req.ContentLength = op.Length
		for _, header := range op.RequestHeaders {
			req.Header.Set(header.Name, header.Value)
		}
		res, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("Apple byte upload interrupted; reconcile the persisted operation")
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
		res.Body.Close()
		if res.StatusCode < 200 || res.StatusCode >= 300 {
			return fmt.Errorf("Apple byte upload returned HTTP %d; reconcile without replay", res.StatusCode)
		}
	}
	return nil
}

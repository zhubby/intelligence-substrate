package publisher

import (
	"context"
	"crypto/md5" // Xiaomi's wire protocol requires MD5; approvals use SHA256.
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/mail"
	"os"
	"strings"
	"time"
)

const xiaomiAPIOrigin = "https://api.developer.xiaomi.com/devupload"

type xiaomiClient struct {
	engine   *Engine
	userName string
	password string
	key      *rsa.PublicKey
}

type xiaomiReply struct {
	Result        *int  `json:"result"`
	Create        *bool `json:"create"`
	UpdateVersion *bool `json:"updateVersion"`
	PackageInfo   *struct {
		PackageName string `json:"packageName"`
		VersionCode int64  `json:"versionCode"`
	} `json:"packageInfo"`
}

func (e *Engine) xiaomiClient(p Profile) (*xiaomiClient, error) {
	body, err := readXiaomiPrivateFile(p.CredentialsFile)
	if err != nil {
		return nil, err
	}
	var credentials struct {
		UserName             string `json:"userName"`
		AccessPassword       string `json:"accessPassword"`
		PublicCertificatePEM string `json:"publicCertificatePEM"`
	}
	if err = decodeXiaomiJSON(body, &credentials, true); err != nil {
		return nil, err
	}
	address, err := mail.ParseAddress(credentials.UserName)
	if err != nil || address.Address != credentials.UserName || len(credentials.UserName) > 254 || len(credentials.AccessPassword) < 8 || len(credentials.AccessPassword) > 4096 || strings.ContainsAny(credentials.AccessPassword, "\r\n\x00") {
		return nil, fmt.Errorf("Xiaomi requires the developer email and an explicit private API access password")
	}
	block, rest := pem.Decode([]byte(credentials.PublicCertificatePEM))
	if block == nil || block.Type != "CERTIFICATE" || strings.TrimSpace(string(rest)) != "" {
		return nil, fmt.Errorf("Xiaomi requires its administrator-provisioned RSA public certificate")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("invalid Xiaomi encryption certificate")
	}
	key, ok := certificate.PublicKey.(*rsa.PublicKey)
	// The upstream examples include legacy 1024-bit encryption certificates.
	// This is a pinned encryption key, not a TLS trust or certificate chain.
	if !ok || key.N.BitLen() < 1024 || key.N.BitLen() > 8192 || key.E < 3 || key.E%2 == 0 {
		return nil, fmt.Errorf("unsupported Xiaomi RSA encryption key")
	}
	return &xiaomiClient{engine: e, userName: credentials.UserName, password: credentials.AccessPassword, key: key}, nil
}

func (c *xiaomiClient) signature(fields []xiaomiFieldDigest) (string, error) {
	body, err := json.Marshal(struct {
		Password string              `json:"password"`
		Sig      []xiaomiFieldDigest `json:"sig"`
	}{c.password, fields})
	if err != nil {
		return "", err
	}
	var encrypted []byte
	for len(body) > 0 {
		n := min(len(body), c.key.Size()-11)
		block, err := rsa.EncryptPKCS1v15(rand.Reader, c.key, body[:n])
		if err != nil {
			return "", fmt.Errorf("Xiaomi request encryption failed")
		}
		encrypted = append(encrypted, block...)
		body = body[n:]
	}
	return hex.EncodeToString(encrypted), nil
}

type xiaomiFieldDigest struct {
	Name string `json:"name"`
	Hash string `json:"hash"`
}

// Use a disk-backed, fixed-length, non-replayable body. Memory use does not grow
// with APK size; filenames and origins are never supplied by repository code.
func (c *xiaomiClient) call(ctx context.Context, path string, data any, files []xiaomiFile) (xiaomiReply, error) {
	var reply xiaomiReply
	if path != "/dev/query" && path != "/dev/push" {
		return reply, fmt.Errorf("unsupported Xiaomi endpoint")
	}
	input, err := json.Marshal(data)
	if err != nil {
		return reply, err
	}
	body, err := os.CreateTemp("", "is-xiaomi-request-")
	if err != nil {
		return reply, err
	}
	defer os.Remove(body.Name())
	defer body.Close()
	writer := multipart.NewWriter(body)
	if err = writer.WriteField("RequestData", string(input)); err != nil {
		return reply, err
	}
	requestMD5 := md5.Sum(input)
	digests := []xiaomiFieldDigest{{"RequestData", hex.EncodeToString(requestMD5[:])}}
	for _, file := range files {
		digest, err := xiaomiMultipartFile(writer, file)
		if err != nil {
			return reply, err
		}
		digests = append(digests, digest)
	}
	signature, err := c.signature(digests)
	if err != nil {
		return reply, err
	}
	if err = writer.WriteField("SIG", signature); err != nil {
		return reply, err
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
	req, err := http.NewRequestWithContext(ctx, "POST", xiaomiAPIOrigin+path, io.NopCloser(body))
	if err != nil {
		return reply, fmt.Errorf("invalid Xiaomi request")
	}
	req.ContentLength = length
	req.Header.Set("Content-Type", writer.FormDataContentType())
	// No inherited cookies, redirect callbacks or automatic transport replay.
	transport := c.engine.HTTP.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	standard, ok := transport.(*http.Transport)
	if !ok {
		return reply, fmt.Errorf("Xiaomi requires a standard isolated HTTP transport")
	}
	isolated := standard.Clone()
	isolated.DisableKeepAlives = true
	isolated.Protocols = new(http.Protocols)
	isolated.Protocols.SetHTTP1(true)
	defer isolated.CloseIdleConnections()
	timeout := 45 * time.Second
	if path == "/dev/push" {
		timeout = 5 * time.Minute
	}
	client := &http.Client{Transport: isolated, Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(req)
	if err != nil {
		return reply, fmt.Errorf("Xiaomi request interrupted; inspect the persisted operation before any mutation")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return reply, fmt.Errorf("Xiaomi API returned HTTP %d; inspect without replaying a push", response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return reply, fmt.Errorf("oversized or incomplete Xiaomi response")
	}
	if err = decodeXiaomiJSON(raw, &reply, false); err != nil {
		return reply, err
	}
	if reply.Result == nil {
		return reply, fmt.Errorf("Xiaomi response is missing a result code")
	}
	if *reply.Result != 0 {
		return reply, fmt.Errorf("Xiaomi API result code %d; inspect channel prerequisites without replaying a push", *reply.Result)
	}
	return reply, nil
}

func xiaomiMultipartFile(writer *multipart.Writer, file xiaomiFile) (xiaomiFieldDigest, error) {
	var digest xiaomiFieldDigest
	in, err := os.Open(file.Path)
	if err != nil {
		return digest, err
	}
	defer in.Close()
	part, err := writer.CreateFormFile(file.Name, file.Filename)
	if err != nil {
		return digest, err
	}
	hash := md5.New()
	if _, err = io.Copy(io.MultiWriter(part, hash), in); err != nil {
		return digest, err
	}
	return xiaomiFieldDigest{file.Name, hex.EncodeToString(hash.Sum(nil))}, nil
}

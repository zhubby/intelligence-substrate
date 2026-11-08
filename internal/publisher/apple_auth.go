package publisher

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var appleIDPattern = regexp.MustCompile(`^[0-9]{1,20}$`)
var appleVersionPattern = regexp.MustCompile(`^[0-9]{1,4}(\.[0-9]{1,4}){0,2}$`)
var appleTeamPattern = regexp.MustCompile(`^[A-Z0-9]{10}$`)
var appleIssuerPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}(-[0-9a-fA-F]{4}){3}-[0-9a-fA-F]{12}$`)

func validateAppleProfile(p Profile) error {
	if (p.MetadataFile != "" || p.MetadataDigest != "") && (!filepath.IsAbs(p.MetadataFile) || !hashPattern.MatchString(p.MetadataDigest)) {
		return fmt.Errorf("App Store managed materials require an absolute manifest path and reviewed SHA256")
	}
	if p.Target != "ios-appstore" || !appleIDPattern.MatchString(p.AppID) || !packagePattern.MatchString(p.BundleID) || !appleTeamPattern.MatchString(p.ExpectedSigner) || !filepath.IsAbs(p.CredentialsFile) || len(p.UploadHosts) == 0 || len(p.UploadHosts) > 16 {
		return fmt.Errorf("App Store profile requires fixed numeric appId, bundleId, distribution team, credential file and exact uploadHosts")
	}
	seen := map[string]bool{}
	for _, host := range p.UploadHosts {
		u, err := url.Parse("https://" + host)
		if err != nil || u.Host != host || u.Hostname() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || host != strings.ToLower(host) || strings.ContainsAny(host, "*[] \t\r\n") || seen[host] {
			return fmt.Errorf("uploadHosts must be distinct exact lowercase HTTPS host names with optional ports")
		}
		seen[host] = true
	}
	return nil
}

func appleToken(p Profile, now time.Time) (string, error) {
	b, err := readPrivateFile(p.CredentialsFile)
	if err != nil {
		return "", err
	}
	if stat, e := os.Stat(p.CredentialsFile); e != nil || stat.Mode().Perm()&0077 != 0 {
		return "", fmt.Errorf("App Store credentials must be private to their service owner")
	}
	var c struct {
		IssuerID   string `json:"issuerId"`
		KeyID      string `json:"keyId"`
		PrivateKey string `json:"privateKey"`
	}
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&c) != nil || !appleIssuerPattern.MatchString(c.IssuerID) || !appleTeamPattern.MatchString(c.KeyID) {
		return "", fmt.Errorf("invalid App Store Connect team API credentials")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return "", fmt.Errorf("invalid App Store Connect credentials")
	}
	block, rest := pem.Decode([]byte(c.PrivateKey))
	if block == nil || block.Type != "PRIVATE KEY" || len(bytes.TrimSpace(rest)) != 0 {
		return "", fmt.Errorf("App Store API key must be a PKCS8 P256 private key")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return "", fmt.Errorf("invalid App Store API private key")
	}
	ec, ok := key.(*ecdsa.PrivateKey)
	if !ok || ec.Curve != elliptic.P256() {
		return "", fmt.Errorf("App Store API key must use P256")
	}
	enc := func(v any) string { b, _ := json.Marshal(v); return base64.RawURLEncoding.EncodeToString(b) }
	unsigned := enc(map[string]string{"alg": "ES256", "kid": c.KeyID, "typ": "JWT"}) + "." + enc(map[string]any{"iss": c.IssuerID, "iat": now.Add(-30 * time.Second).Unix(), "exp": now.Add(9 * time.Minute).Unix(), "aud": "appstoreconnect-v1"})
	hash := sha256.Sum256([]byte(unsigned))
	r, s, err := ecdsa.Sign(rand.Reader, ec, hash[:])
	if err != nil {
		return "", fmt.Errorf("App Store JWT signing failed")
	}
	signature := make([]byte, 64)
	r.FillBytes(signature[:32])
	s.FillBytes(signature[32:])
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

// ASC authorization is attached only to this fixed API origin, never to blob URLs.
func (e *Engine) appleCall(ctx context.Context, p Profile, method, path string, input, out any) error {
	token, err := appleToken(p, time.Now())
	if err != nil {
		return err
	}
	var body io.Reader
	if input != nil {
		b, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, e.appleBase+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.GetBody = nil
	client := &http.Client{Transport: e.HTTP.Transport, Timeout: e.HTTP.Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("App Store request interrupted; inspect the persisted operation before any mutation")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("App Store API returned HTTP %d; inspect the persisted stage and channel prerequisites", res.StatusCode)
	}
	if out == nil {
		return nil
	}
	responseBody, err := io.ReadAll(io.LimitReader(res.Body, 2<<20+1))
	if err != nil || len(responseBody) > 2<<20 || decodeXiaomiJSON(responseBody, out, false) != nil {
		return fmt.Errorf("invalid App Store response")
	}
	return nil
}

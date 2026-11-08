package publisher

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func appleCredentials(t *testing.T) (string, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	b, _ := json.Marshal(map[string]string{"issuerId": "12345678-1234-1234-1234-123456789012", "keyId": "ABC1234567", "privateKey": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))})
	name := filepath.Join(t.TempDir(), "asc.json")
	if err = os.WriteFile(name, b, 0600); err != nil {
		t.Fatal(err)
	}
	return name, key
}

func TestAppleJWTIsShortLivedAndCryptographicallyBound(t *testing.T) {
	name, key := appleCredentials(t)
	token, err := appleToken(Profile{CredentialsFile: name}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatal("not JWT")
	}
	var head, claims map[string]any
	decode := func(s string, v any) {
		b, e := base64.RawURLEncoding.DecodeString(s)
		if e != nil {
			t.Fatal(e)
		}
		if e = json.Unmarshal(b, v); e != nil {
			t.Fatal(e)
		}
	}
	decode(parts[0], &head)
	decode(parts[1], &claims)
	if head["alg"] != "ES256" || head["kid"] != "ABC1234567" || claims["aud"] != "appstoreconnect-v1" || claims["iss"] != "12345678-1234-1234-1234-123456789012" || claims["exp"].(float64)-claims["iat"].(float64) > 1200 {
		t.Fatal("wrong Apple JWT claims")
	}
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	hash := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if len(sig) != 64 || !ecdsa.Verify(&key.PublicKey, hash[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Fatal("JWT is not valid JOSE ES256")
	}
	if e := os.WriteFile(name, []byte(`{"issuerId":"wrong","keyId":"ABC1234567","privateKey":"no"}`), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := appleToken(Profile{CredentialsFile: name}, time.Now()); e == nil {
		t.Fatal("invalid credentials accepted")
	}
}

func TestAppleUploadRangesAndCredentialsAreConstrained(t *testing.T) {
	p := Profile{UploadHosts: []string{"upload.example"}}
	valid := []appleUploadOperation{{Method: "PUT", URL: "https://upload.example/part1?token=abc", Offset: 0, Length: 3}, {Method: "PUT", URL: "https://upload.example/part2", Offset: 3, Length: 2}}
	if err := validateAppleUploadOperations(p, valid, 5); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"host", "http", "userinfo", "overlap", "gap", "overflow", "method", "auth", "cookie", "hostheader", "duplicateheader", "fragment"} {
		t.Run(scenario, func(t *testing.T) {
			ops := append([]appleUploadOperation(nil), valid...)
			switch scenario {
			case "host":
				ops[0].URL = "https://evil.example/part"
			case "http":
				ops[0].URL = "http://upload.example/part"
			case "userinfo":
				ops[0].URL = "https://secret@upload.example/part"
			case "overlap":
				ops[1].Offset = 2
			case "gap":
				ops[1].Offset = 4
			case "overflow":
				ops[1].Length = 1 << 62
			case "method":
				ops[0].Method = "GET"
			case "auth":
				ops[0].RequestHeaders = []appleHeader{{Name: "Authorization", Value: "Bearer abc"}}
			case "cookie":
				ops[0].RequestHeaders = []appleHeader{{Name: "Cookie", Value: "secret"}}
			case "hostheader":
				ops[0].RequestHeaders = []appleHeader{{Name: "Host", Value: "evil.example"}}
			case "duplicateheader":
				ops[0].RequestHeaders = []appleHeader{{Name: "Content-Type", Value: "a"}, {Name: "content-type", Value: "b"}}
			case "fragment":
				ops[0].URL += "#x"
			}
			if err := validateAppleUploadOperations(p, ops, 5); err == nil {
				t.Fatal("unsafe upload operation accepted")
			}
		})
	}
}

func TestUnsignedIPARejectedBeforeExternalReservation(t *testing.T) {
	config, r := publisherFixture(t)
	name, _ := appleCredentials(t)
	config.Profiles[0] = Profile{ProjectID: r.ProjectID, GoalID: r.GoalID, Target: "ios-appstore", Driver: "app-store", AppID: "1234567890", BundleID: "com.company.feedback", ExpectedSigner: "ABCD123456", CredentialsFile: name, UploadHosts: []string{"upload.example"}}
	r.Target = "ios-appstore"
	e, err := Open(config)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if _, err = e.Execute(context.Background(), r); err == nil {
		t.Fatal("unsigned archive reached App Store")
	}
	if _, err = e.Inspect(context.Background(), r.OperationKey); err == nil {
		t.Fatal("invalid package reserved an external operation")
	}
}

func TestAppleProfileRejectsWildcardOrTaskSelectableDestinations(t *testing.T) {
	config, r := publisherFixture(t)
	name, _ := appleCredentials(t)
	base := Profile{ProjectID: r.ProjectID, GoalID: r.GoalID, Target: "ios-appstore", Driver: "app-store", AppID: "1234567890", BundleID: "com.company.feedback", ExpectedSigner: "ABCD123456", CredentialsFile: name, UploadHosts: []string{"upload.example"}}
	for _, host := range []string{"*.apple.com", "upload.example/path", "secret@upload.example", "UPPER.example", "upload.example?x=1"} {
		t.Run(host, func(t *testing.T) {
			p := base
			p.UploadHosts = []string{host}
			config.Profiles = []Profile{p}
			if engine, err := Open(config); err == nil {
				engine.Close()
				t.Fatal("unsafe upload host configuration accepted")
			}
		})
	}
}

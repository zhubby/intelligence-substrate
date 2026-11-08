// Package trustedtls loads immutable administrator-controlled TLS materials.
package trustedtls

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type ClientConfig struct {
	Endpoint       string `json:"endpoint"`
	ServerName     string `json:"serverName"`
	ServerSPKI     string `json:"serverSPKI"`
	CAFile         string `json:"caFile"`
	ClientCertFile string `json:"clientCertFile"`
	ClientKeyFile  string `json:"clientKeyFile"`
}

func ValidEndpoint(endpoint string) bool {
	host, port, e := net.SplitHostPort(endpoint)
	number, pe := strconv.Atoi(port)
	return e == nil && pe == nil && number >= 1 && number <= 65535 && len(endpoint) <= 512 && (net.ParseIP(host) != nil || ValidDNSName(host))
}
func ValidDNSName(name string) bool {
	if len(name) == 0 || len(name) > 253 {
		return false
	}
	for _, part := range strings.Split(name, ".") {
		if !regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`).MatchString(part) {
			return false
		}
	}
	return true
}
func ReadFile(path string, private bool) ([]byte, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("credential material requires an absolute private-node file")
	}
	before, e := os.Lstat(path)
	if e != nil || !before.Mode().IsRegular() || before.Size() < 1 || before.Size() > 1<<20 || private && before.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("credential material is unavailable or has unsafe type, size or permissions")
	}
	f, e := os.Open(path)
	if e != nil {
		return nil, fmt.Errorf("credential material is unavailable")
	}
	defer f.Close()
	after, e := f.Stat()
	if e != nil || !os.SameFile(before, after) {
		return nil, fmt.Errorf("credential material changed while opening")
	}
	body, e := io.ReadAll(io.LimitReader(f, 1<<20+1))
	if e != nil || len(body) != int(before.Size()) {
		return nil, fmt.Errorf("credential material could not be read consistently")
	}
	return body, nil
}

func LoadClient(c ClientConfig) (*tls.Config, map[string]any, error) {
	if !ValidEndpoint(c.Endpoint) || !ValidDNSName(c.ServerName) {
		return nil, nil, fmt.Errorf("explicit endpoint and DNS service identity required")
	}
	pin, e := hex.DecodeString(c.ServerSPKI)
	if e != nil || len(pin) != sha256.Size || c.ServerSPKI != strings.ToLower(c.ServerSPKI) {
		return nil, nil, fmt.Errorf("server SPKI must be a SHA256 fingerprint")
	}
	ca, e := ReadFile(c.CAFile, false)
	if e != nil {
		return nil, nil, e
	}
	cert, e := ReadFile(c.ClientCertFile, false)
	if e != nil {
		return nil, nil, e
	}
	key, e := ReadFile(c.ClientKeyFile, true)
	if e != nil {
		return nil, nil, e
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return nil, nil, fmt.Errorf("invalid private trust roots")
	}
	pair, e := tls.X509KeyPair(cert, key)
	if e != nil {
		return nil, nil, fmt.Errorf("invalid client certificate or private key")
	}
	leaf, e := x509.ParseCertificate(pair.Certificate[0])
	if e != nil {
		return nil, nil, fmt.Errorf("invalid client leaf certificate")
	}
	now := time.Now()
	clientAuth := false
	for _, usage := range leaf.ExtKeyUsage {
		if usage == x509.ExtKeyUsageClientAuth || usage == x509.ExtKeyUsageAny {
			clientAuth = true
		}
	}
	if !clientAuth || now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) {
		return nil, nil, fmt.Errorf("client certificate is not currently valid for client authentication")
	}
	caHash, clientHash := sha256.Sum256(ca), sha256.Sum256(leaf.Raw)
	public := map[string]any{"endpoint": c.Endpoint, "serverName": c.ServerName, "serverSPKI": c.ServerSPKI, "caDigest": hex.EncodeToString(caHash[:]), "clientCertDigest": hex.EncodeToString(clientHash[:])}
	config := &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: pool, Certificates: []tls.Certificate{pair}, ServerName: c.ServerName, NextProtos: []string{"h2"}, VerifyConnection: func(state tls.ConnectionState) error {
		if len(state.PeerCertificates) == 0 || len(state.VerifiedChains) == 0 || state.NegotiatedProtocol != "h2" {
			return fmt.Errorf("AX peer authentication incomplete")
		}
		actual := sha256.Sum256(state.PeerCertificates[0].RawSubjectPublicKeyInfo)
		if subtle.ConstantTimeCompare(actual[:], pin) != 1 {
			return fmt.Errorf("AX peer SPKI differs from the administrator pin")
		}
		return nil
	}}
	return config, public, nil
}

func DialGRPC(ctx context.Context, endpoint string, config *tls.Config) (*grpc.ClientConn, error) {
	if !ValidEndpoint(endpoint) || config == nil || config.MinVersion < tls.VersionTLS13 || config.InsecureSkipVerify || config.RootCAs == nil || len(config.Certificates) == 0 || !ValidDNSName(config.ServerName) || config.VerifyConnection == nil {
		return nil, fmt.Errorf("explicit authenticated TLS configuration required")
	}
	config = config.Clone()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	conn, e := grpc.NewClient("passthrough:///"+endpoint, grpc.WithTransportCredentials(credentials.NewTLS(config)), grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return dialer.DialContext(ctx, "tcp", endpoint) }), grpc.WithDisableRetry(), grpc.WithConnectParams(grpc.ConnectParams{Backoff: backoff.Config{BaseDelay: 100 * time.Millisecond, Multiplier: 1.5, Jitter: 0.2, MaxDelay: time.Second}, MinConnectTimeout: time.Second}))
	if e != nil {
		return nil, e
	}
	conn.Connect()
	for {
		state := conn.GetState()
		if state == connectivity.Ready {
			return conn, nil
		}
		if state == connectivity.Shutdown || !conn.WaitForStateChange(ctx, state) {
			conn.Close()
			return nil, fmt.Errorf("authenticated peer unavailable")
		}
	}
}

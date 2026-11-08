package trustedtls

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"time"
)

type ServerConfig struct {
	CertFile     string `json:"certFile"`
	KeyFile      string `json:"keyFile"`
	ClientCAFile string `json:"clientCAFile"`
}

func LoadServer(c ServerConfig) (*tls.Config, error) {
	cert, e := ReadFile(c.CertFile, false)
	if e != nil {
		return nil, e
	}
	key, e := ReadFile(c.KeyFile, true)
	if e != nil {
		return nil, e
	}
	ca, e := ReadFile(c.ClientCAFile, false)
	if e != nil {
		return nil, e
	}
	pair, e := tls.X509KeyPair(cert, key)
	if e != nil {
		return nil, fmt.Errorf("invalid server certificate or private key")
	}
	leaf, e := x509.ParseCertificate(pair.Certificate[0])
	if e != nil {
		return nil, fmt.Errorf("invalid server leaf certificate")
	}
	serverAuth := false
	for _, usage := range leaf.ExtKeyUsage {
		if usage == x509.ExtKeyUsageServerAuth || usage == x509.ExtKeyUsageAny {
			serverAuth = true
		}
	}
	if !serverAuth || time.Now().Before(leaf.NotBefore) || !time.Now().Before(leaf.NotAfter) {
		return nil, fmt.Errorf("server certificate is not currently valid for server authentication")
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return nil, fmt.Errorf("invalid client trust roots")
	}
	return &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{pair}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: pool, NextProtos: []string{"h2"}}, nil
}

// Package tlsfixture creates short-lived private test identities. It is not a
// certificate issuer used by any deployed service.
package tlsfixture

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

type Authority struct {
	Root, CAFile, ServerCertFile, ServerKeyFile, ServerSPKI string
	ServerTLS                                               *tls.Config
	ca                                                      *x509.Certificate
	key                                                     *ecdsa.PrivateKey
	pool                                                    *x509.CertPool
	serial                                                  int64
}
type Client struct {
	CertFile, KeyFile, SHA256 string
	TLS                       *tls.Config
}

func New(root string) (*Authority, error) {
	if e := os.MkdirAll(root, 0700); e != nil {
		return nil, e
	}
	key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		return nil, e
	}
	now := time.Now()
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "local gateway test CA"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	der, e := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if e != nil {
		return nil, e
	}
	public := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	a := &Authority{Root: root, CAFile: filepath.Join(root, "ca.pem"), ca: ca, key: key, pool: x509.NewCertPool(), serial: 1}
	a.pool.AppendCertsFromPEM(public)
	if e = os.WriteFile(a.CAFile, public, 0600); e != nil {
		return nil, e
	}
	pair, certPath, keyPath, e := a.issue("gateway", false)
	if e != nil {
		return nil, e
	}
	a.ServerCertFile, a.ServerKeyFile = certPath, keyPath
	cert, e := x509.ParseCertificate(pair.Certificate[0])
	if e != nil {
		return nil, e
	}
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	a.ServerSPKI = hex.EncodeToString(sum[:])
	a.ServerTLS = &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{pair}, ClientCAs: a.pool, ClientAuth: tls.RequireAndVerifyClientCert, NextProtos: []string{"h2"}}
	return a, nil
}
func (a *Authority) issue(name string, client bool) (tls.Certificate, string, string, error) {
	key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		return tls.Certificate{}, "", "", e
	}
	a.serial++
	cert := &x509.Certificate{SerialNumber: big.NewInt(a.serial), Subject: pkix.Name{CommonName: name}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature}
	if client {
		cert.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	} else {
		cert.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		cert.DNSNames = []string{"gateway.test"}
	}
	der, e := x509.CreateCertificate(rand.Reader, cert, a.ca, &key.PublicKey, a.key)
	if e != nil {
		return tls.Certificate{}, "", "", e
	}
	keyDER, e := x509.MarshalPKCS8PrivateKey(key)
	if e != nil {
		return tls.Certificate{}, "", "", e
	}
	public, private := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	certPath, keyPath := filepath.Join(a.Root, name+"-cert.pem"), filepath.Join(a.Root, name+"-key.pem")
	if e = os.WriteFile(certPath, public, 0600); e != nil {
		return tls.Certificate{}, "", "", e
	}
	if e = os.WriteFile(keyPath, private, 0600); e != nil {
		return tls.Certificate{}, "", "", e
	}
	pair, e := tls.X509KeyPair(public, private)
	return pair, certPath, keyPath, e
}
func (a *Authority) Issue(name string) (Client, error) {
	pair, certPath, keyPath, e := a.issue(name, true)
	if e != nil {
		return Client{}, e
	}
	sum := sha256.Sum256(pair.Certificate[0])
	return Client{CertFile: certPath, KeyFile: keyPath, SHA256: hex.EncodeToString(sum[:]), TLS: &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{pair}, RootCAs: a.pool, ServerName: "gateway.test", NextProtos: []string{"h2"}}}, nil
}

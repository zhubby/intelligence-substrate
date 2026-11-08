package axgateway

import (
	"fmt"
	"github.com/intelligence-substrate/platform/internal/trustedtls"
	"net"
	"path/filepath"
)

type ListenerConfig struct {
	Listen string                  `json:"listen"`
	TLS    trustedtls.ServerConfig `json:"tls"`
}
type ControlUpstreamConfig struct {
	Endpoint                string `json:"endpoint"`
	PrivateNetworkNamespace bool   `json:"privateNetworkNamespace"`
}
type Config struct {
	Version         int                     `json:"version"`
	Control         ListenerConfig          `json:"control"`
	Guest           ListenerConfig          `json:"guest"`
	ControlUpstream ControlUpstreamConfig   `json:"controlUpstream"`
	GuestUpstream   trustedtls.ClientConfig `json:"guestUpstream"`
	PolicyFile      string                  `json:"policyFile"`
	AuditFile       string                  `json:"auditFile"`
	sourcePath      string
}

func LoadConfig(path string) (Config, error) {
	var c Config
	body, e := trustedtls.ReadFile(path, true)
	if e != nil {
		return c, e
	}
	if e = decodeStrict(body, &c); e != nil {
		return c, e
	}
	c.sourcePath = path
	return c, c.validate()
}
func (c Config) materialPaths() []string {
	paths := []string{c.PolicyFile, c.Control.TLS.CertFile, c.Control.TLS.KeyFile, c.Control.TLS.ClientCAFile, c.Guest.TLS.CertFile, c.Guest.TLS.KeyFile, c.Guest.TLS.ClientCAFile, c.GuestUpstream.CAFile, c.GuestUpstream.ClientCertFile, c.GuestUpstream.ClientKeyFile}
	if c.sourcePath != "" {
		paths = append(paths, c.sourcePath)
	}
	return paths
}
func numericEndpoint(endpoint string, loopback bool) bool {
	if !trustedtls.ValidEndpoint(endpoint) {
		return false
	}
	host, _, _ := net.SplitHostPort(endpoint)
	ip := net.ParseIP(host)
	return ip != nil && (!loopback || ip.IsLoopback())
}
func (c Config) validate() error {
	if c.Version != 1 || !numericEndpoint(c.Control.Listen, false) || !numericEndpoint(c.Guest.Listen, false) || c.Control.Listen == c.Guest.Listen || !c.ControlUpstream.PrivateNetworkNamespace || !numericEndpoint(c.ControlUpstream.Endpoint, true) || c.ControlUpstream.Endpoint == c.Control.Listen || c.ControlUpstream.Endpoint == c.Guest.Listen || !trustedtls.ValidEndpoint(c.GuestUpstream.Endpoint) || !trustedtls.ValidDNSName(c.GuestUpstream.ServerName) || !hashPattern.MatchString(c.GuestUpstream.ServerSPKI) || !filepath.IsAbs(c.AuditFile) {
		return fmt.Errorf("invalid explicit listeners or upstream authority")
	}
	for _, path := range c.materialPaths() {
		if !filepath.IsAbs(path) || filepath.Clean(path) == filepath.Clean(c.AuditFile) {
			return fmt.Errorf("private materials require distinct absolute administrator paths")
		}
	}
	return nil
}

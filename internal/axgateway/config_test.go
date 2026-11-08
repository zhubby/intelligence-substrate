package axgateway

import (
	"encoding/json"
	"github.com/intelligence-substrate/platform/internal/trustedtls"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func configFixture(t *testing.T) Config {
	t.Helper()
	root := t.TempDir()
	return Config{Version: 1, Control: ListenerConfig{Listen: "127.0.0.1:18443", TLS: trustedtls.ServerConfig{CertFile: filepath.Join(root, "cert.pem"), KeyFile: filepath.Join(root, "key.pem"), ClientCAFile: filepath.Join(root, "ca.pem")}}, Guest: ListenerConfig{Listen: "127.0.0.1:18444", TLS: trustedtls.ServerConfig{CertFile: filepath.Join(root, "cert2.pem"), KeyFile: filepath.Join(root, "key2.pem"), ClientCAFile: filepath.Join(root, "ca2.pem")}}, ControlUpstream: ControlUpstreamConfig{Endpoint: "127.0.0.1:50051", PrivateNetworkNamespace: true}, GuestUpstream: trustedtls.ClientConfig{Endpoint: "guest.example:443", ServerName: "guest.example", ServerSPKI: strings.Repeat("a", 64), CAFile: filepath.Join(root, "up-ca.pem"), ClientCertFile: filepath.Join(root, "up-cert.pem"), ClientKeyFile: filepath.Join(root, "up-key.pem")}, PolicyFile: filepath.Join(root, "policy.json"), AuditFile: filepath.Join(root, "audit.jsonl")}
}
func TestGatewayConfigRejectsImplicitRemoteAndAmbiguousAuthority(t *testing.T) {
	for _, mode := range []string{"valid", "remote", "dns-loopback", "no-namespace", "same-listener", "relative-policy", "relative-key", "duplicate", "unknown", "public", "symlink", "trailing"} {
		t.Run(mode, func(t *testing.T) {
			c := configFixture(t)
			switch mode {
			case "remote":
				c.ControlUpstream.Endpoint = "10.0.0.1:50051"
			case "dns-loopback":
				c.ControlUpstream.Endpoint = "localhost:50051"
			case "no-namespace":
				c.ControlUpstream.PrivateNetworkNamespace = false
			case "same-listener":
				c.Guest.Listen = c.Control.Listen
			case "relative-policy":
				c.PolicyFile = "policy.json"
			case "relative-key":
				c.Control.TLS.KeyFile = "key.pem"
			}
			body, _ := json.Marshal(c)
			switch mode {
			case "duplicate":
				body = append([]byte(`{"version":1,`), body[1:]...)
			case "unknown":
				body = append([]byte(`{"allowRemote":true,`), body[1:]...)
			case "trailing":
				body = append(body, []byte(` {}`)...)
			}
			path := filepath.Join(t.TempDir(), "config.json")
			if e := os.WriteFile(path, body, 0600); e != nil {
				t.Fatal(e)
			}
			if mode == "public" {
				if e := os.Chmod(path, 0644); e != nil {
					t.Fatal(e)
				}
			}
			if mode == "symlink" {
				target := path
				path += ".link"
				if e := os.Symlink(target, path); e != nil {
					t.Fatal(e)
				}
			}
			_, e := LoadConfig(path)
			if (mode == "valid") != (e == nil) {
				t.Fatal("configuration boundary", mode, e)
			}
		})
	}
}
func TestGatewayAuditIsPrivateAppendOnlyAndFailsAfterClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	log, e := openAudit(path)
	if e != nil {
		t.Fatal(e)
	}
	if e = log.Append(Event{Outcome: "admitted", Method: "task.create"}); e != nil {
		t.Fatal(e)
	}
	if e = log.Close(); e != nil {
		t.Fatal(e)
	}
	if e = log.Append(Event{Outcome: "admitted"}); e == nil {
		t.Fatal("closed audit accepted write")
	}
	log, e = openAudit(path)
	if e != nil {
		t.Fatal(e)
	}
	if e = log.Append(Event{Outcome: "denied", Method: "task.delete"}); e != nil {
		t.Fatal(e)
	}
	_ = log.Close()
	info, _ := os.Stat(path)
	body, _ := os.ReadFile(path)
	if info.Mode().Perm() != 0600 || len(strings.Split(strings.TrimSpace(string(body)), "\n")) != 2 {
		t.Fatal("audit lost records or privacy")
	}
	if e = os.Chmod(path, 0644); e != nil {
		t.Fatal(e)
	}
	if log, e = openAudit(path); e == nil {
		log.Close()
		t.Fatal("public audit reopened")
	}
	if e = os.Chmod(path, 0600); e != nil {
		t.Fatal(e)
	}
	alias := path + ".link"
	if e = os.Symlink(path, alias); e != nil {
		t.Fatal(e)
	}
	if log, e = openAudit(alias); e == nil {
		log.Close()
		t.Fatal("symlink audit opened")
	}
}

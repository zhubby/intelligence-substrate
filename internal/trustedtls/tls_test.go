package trustedtls

import (
	"context"
	"crypto/tls"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/intelligence-substrate/platform/internal/testutil/tlsfixture"
)

func TestServerTLSRequiresPrivateMaterialsAndBothAuthenticatedPeers(t *testing.T) {
	a, e := tlsfixture.New(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	client, e := a.Issue("worker")
	if e != nil {
		t.Fatal(e)
	}
	config := ServerConfig{CertFile: a.ServerCertFile, KeyFile: a.ServerKeyFile, ClientCAFile: a.CAFile}
	serverTLS, e := LoadServer(config)
	if e != nil {
		t.Fatal(e)
	}
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer listener.Close()
	for _, bad := range []bool{false, true} {
		done := make(chan error, 1)
		go func() {
			raw, e := listener.Accept()
			if e != nil {
				done <- e
				return
			}
			defer raw.Close()
			_ = raw.SetDeadline(time.Now().Add(time.Second))
			done <- tls.Server(raw, serverTLS).Handshake()
		}()
		configuration := client.TLS.Clone()
		if bad {
			configuration.Certificates = nil
		}
		conn, e := tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp", listener.Addr().String(), configuration)
		if conn != nil {
			conn.Close()
		}
		serverError := <-done
		if !bad && (e != nil || serverError != nil) {
			t.Fatal(e, serverError)
		}
		if bad && serverError == nil {
			t.Fatal("server accepted no client certificate")
		}
	}
	for _, mode := range []string{"public-key", "symlink", "client-as-server"} {
		t.Run(mode, func(t *testing.T) {
			candidate := config
			switch mode {
			case "public-key":
				if e := os.Chmod(a.ServerKeyFile, 0644); e != nil {
					t.Fatal(e)
				}
				defer os.Chmod(a.ServerKeyFile, 0600)
			case "symlink":
				link := filepath.Join(t.TempDir(), "key")
				if e := os.Symlink(a.ServerKeyFile, link); e != nil {
					t.Fatal(e)
				}
				candidate.KeyFile = link
			case "client-as-server":
				candidate.CertFile = client.CertFile
				candidate.KeyFile = client.KeyFile
			}
			if _, e := LoadServer(candidate); e == nil {
				t.Fatal("unsafe server identity accepted", mode)
			}
		})
	}
}

func TestClientTrustRejectsAmbiguousPinsAndUnvalidatedDial(t *testing.T) {
	a, e := tlsfixture.New(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	client, e := a.Issue("worker")
	if e != nil {
		t.Fatal(e)
	}
	config := ClientConfig{Endpoint: "127.0.0.1:443", ServerName: "gateway.test", ServerSPKI: strings.ToUpper(a.ServerSPKI), CAFile: a.CAFile, ClientCertFile: client.CertFile, ClientKeyFile: client.KeyFile}
	if _, _, e = LoadClient(config); e == nil {
		t.Error("uppercase noncanonical pin accepted")
	}
	defer func() {
		if recover() != nil {
			t.Error("unvalidated TLS dial panicked")
		}
	}()
	if connection, e := DialGRPC(context.Background(), "127.0.0.1:443", nil); e == nil {
		connection.Close()
		t.Fatal("unvalidated TLS dial accepted")
	}
}

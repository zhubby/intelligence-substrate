package axgateway

import (
	"context"
	"fmt"
	"net"
	"os"
	"time"

	guest "github.com/agent-substrate/env/proto/ateenv/v1alpha"
	ax "github.com/google/ax/pkg/apis/v1alpha1"
	"github.com/intelligence-substrate/platform/internal/trustedtls"
	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

// dialControl reaches only the explicitly asserted same-namespace loopback AX.
// Native AX speaks h2c; this is not a remote plaintext transport option.
func dialControl(ctx context.Context, endpoint string) (*grpc.ClientConn, error) {
	if !numericEndpoint(endpoint, true) {
		return nil, fmt.Errorf("native control requires numeric loopback")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	connection, e := grpc.NewClient("passthrough:///"+endpoint, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return dialer.DialContext(ctx, "tcp", endpoint) }), grpc.WithDisableRetry(), grpc.WithDisableServiceConfig(), grpc.WithConnectParams(grpc.ConnectParams{Backoff: backoff.Config{BaseDelay: 100 * time.Millisecond, Multiplier: 1.5, Jitter: 0.2, MaxDelay: time.Second}, MinConnectTimeout: time.Second}))
	if e != nil {
		return nil, fmt.Errorf("native control connection unavailable")
	}
	connection.Connect()
	for {
		state := connection.GetState()
		if state == connectivity.Ready {
			return connection, nil
		}
		if state == connectivity.Shutdown || !connection.WaitForStateChange(ctx, state) {
			connection.Close()
			return nil, fmt.Errorf("native control connection unavailable")
		}
	}
}

// Run owns all listeners/connections. Reload reads only the fixed policy path;
// it never reloads transport authority or retries an application operation.
func Run(ctx context.Context, c Config, reloads <-chan struct{}, report func(string)) error {
	if e := c.validate(); e != nil {
		return e
	}
	if report == nil {
		report = func(string) {}
	}
	controlTLS, e := trustedtls.LoadServer(c.Control.TLS)
	if e != nil {
		return e
	}
	guestTLS, e := trustedtls.LoadServer(c.Guest.TLS)
	if e != nil {
		return e
	}
	upstreamTLS, _, e := trustedtls.LoadClient(c.GuestUpstream)
	if e != nil {
		return e
	}
	policy, e := trustedtls.ReadFile(c.PolicyFile, true)
	if e != nil {
		return e
	}
	audit, e := openAudit(c.AuditFile)
	if e != nil {
		return e
	}
	defer audit.Close()
	auditInfo, e := audit.file.Stat()
	if e != nil {
		return fmt.Errorf("audit could not be verified")
	}
	for _, path := range c.materialPaths() {
		if info, e := os.Stat(path); e == nil && os.SameFile(info, auditInfo) {
			return fmt.Errorf("audit cannot alias administrator materials")
		}
	}
	gate, e := NewGate(policy, audit.Append)
	if e != nil {
		return e
	}
	control, e := dialControl(ctx, c.ControlUpstream.Endpoint)
	if e != nil {
		return e
	}
	defer control.Close()
	guestConnection, e := trustedtls.DialGRPC(ctx, c.GuestUpstream.Endpoint, upstreamTLS)
	if e != nil {
		return e
	}
	defer guestConnection.Close()
	gateway := NewGateway(gate, ax.NewAXClient(control), guest.NewProcessServiceClient(guestConnection))
	controlListener, e := net.Listen("tcp", c.Control.Listen)
	if e != nil {
		return fmt.Errorf("control listener unavailable")
	}
	defer controlListener.Close()
	guestListener, e := net.Listen("tcp", c.Guest.Listen)
	if e != nil {
		return fmt.Errorf("guest listener unavailable")
	}
	defer guestListener.Close()
	options := []grpc.ServerOption{grpc.MaxRecvMsgSize(1 << 20), grpc.MaxSendMsgSize(1 << 20), grpc.MaxConcurrentStreams(64), grpc.ConnectionTimeout(10 * time.Second)}
	controlServer := grpc.NewServer(append(options, grpc.Creds(credentials.NewTLS(controlTLS)))...)
	guestServer := grpc.NewServer(append(options, grpc.Creds(credentials.NewTLS(guestTLS)))...)
	ax.RegisterAXServer(controlServer, gateway.ControlServer())
	guest.RegisterProcessServiceServer(guestServer, gateway.GuestServer())
	errors := make(chan error, 2)
	go func() { errors <- controlServer.Serve(controlListener) }()
	go func() { errors <- guestServer.Serve(guestListener) }()
	defer func() {
		_ = gate.Reload(nil)
		done := make(chan struct{})
		go func() { controlServer.GracefulStop(); guestServer.GracefulStop(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			controlServer.Stop()
			guestServer.Stop()
			<-done
		}
	}()
	report("gateway.ready")
	for {
		select {
		case <-ctx.Done():
			report("gateway.stopped")
			return nil
		case <-reloads:
			body, readError := trustedtls.ReadFile(c.PolicyFile, true)
			if readError != nil {
				body = nil
			}
			if e = gate.Reload(body); e != nil {
				report("policy.denied")
			} else {
				report("policy.accepted")
			}
		case <-errors:
			return fmt.Errorf("gateway listener stopped unexpectedly")
		}
	}
}

package integrations

import (
	"context"
	"encoding/json"
	"fmt"
	guest "github.com/agent-substrate/env/proto/ateenv/v1alpha"
	ax "github.com/google/ax/pkg/apis/v1alpha1"
	p "github.com/intelligence-substrate/platform/internal/platform"
	"github.com/intelligence-substrate/platform/internal/trustedtls"
	"google.golang.org/grpc"
)

// AXSecureConfig names both authenticated services. Credential files stay on
// the trusted Worker and are never part of a sandbox backend record.
type AXSecureConfig struct {
	Control AXTLSConfig `json:"control"`
	Guest   AXTLSConfig `json:"guest"`
}

type AXTLSConfig = trustedtls.ClientConfig

type axSecureSession struct {
	controlConn, guestConn *grpc.ClientConn
	control                ax.AXClient
	guest                  guest.ProcessServiceClient
	backend                p.M
}

// Open freezes routing and credential bytes for one complete tool lifecycle.
// It authenticates both peers without issuing any application RPC or mutation.
// The caller persists backend before submitting the first external operation.
func (a AX) Open(ctx context.Context) (AX, p.M, func(), error) {
	noop := func() {}
	backend := p.M{"context": a.Context, "namespace": a.Namespace, "atespace": a.Atespace, "image": a.Image}
	if a.session != nil {
		body, _ := json.Marshal(a.session.backend)
		var public p.M
		_ = json.Unmarshal(body, &public)
		return a, public, noop, nil
	}
	if a.Secure == nil {
		if !a.AllowLegacyCLI {
			return a, nil, noop, fmt.Errorf("AX requires secure endpoints; legacy CLI is diagnostic and must be explicitly enabled")
		}
		if e := p.ValidateAXBackend(backend); e != nil {
			return a, nil, noop, e
		}
		return a, backend, noop, nil
	}
	if a.AllowLegacyCLI {
		return a, nil, noop, fmt.Errorf("AX secure and legacy CLI modes cannot be combined")
	}
	controlTLS, controlIdentity, e := trustedtls.LoadClient(a.Secure.Control)
	if e != nil {
		return a, nil, noop, fmt.Errorf("AX control TLS configuration invalid: %w", e)
	}
	guestTLS, guestIdentity, e := trustedtls.LoadClient(a.Secure.Guest)
	if e != nil {
		return a, nil, noop, fmt.Errorf("AX guest TLS configuration invalid: %w", e)
	}
	backend["transport"] = p.M{"version": "mtls-v1", "control": controlIdentity, "guest": guestIdentity}
	if e = p.ValidateAXBackend(backend); e != nil {
		return a, nil, noop, e
	}
	control, e := trustedtls.DialGRPC(ctx, a.Secure.Control.Endpoint, controlTLS)
	if e != nil {
		return a, nil, noop, fmt.Errorf("AX control authenticated connection unavailable")
	}
	guestConn, e := trustedtls.DialGRPC(ctx, a.Secure.Guest.Endpoint, guestTLS)
	if e != nil {
		control.Close()
		return a, nil, noop, fmt.Errorf("AX guest authenticated connection unavailable")
	}
	a.session = &axSecureSession{controlConn: control, guestConn: guestConn, control: ax.NewAXClient(control), guest: guest.NewProcessServiceClient(guestConn), backend: backend}
	close := func() { guestConn.Close(); control.Close() }
	return a, backend, close, nil
}

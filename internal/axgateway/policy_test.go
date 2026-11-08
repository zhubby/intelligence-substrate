package axgateway

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
)

func policyPeer(raw string) (context.Context, string) {
	cert := &x509.Certificate{Raw: []byte(raw), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	sum := sha256.Sum256(cert.Raw)
	ctx := peer.NewContext(context.Background(), &peer.Peer{AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{Version: tls.VersionTLS13, NegotiatedProtocol: "h2", PeerCertificates: []*x509.Certificate{cert}, VerifiedChains: [][]*x509.Certificate{{cert}}}}})
	return ctx, hex.EncodeToString(sum[:])
}
func policyBody(revision uint64, fingerprint string) []byte {
	body, _ := json.Marshal(Policy{Version: 1, Revision: revision, Principals: []Principal{{ID: "worker-one", ProjectID: "project_012345678901234567890123", Atespace: "project-one", CertificateSHA256: fingerprint, Control: true, Guest: true, Images: []string{"runner@sha256:" + strings.Repeat("a", 64)}}}})
	return body
}

const ownedName = "is-sandbox-012345678901234567890123"

func TestGatewayPolicyScopesAndRevokesExistingConnections(t *testing.T) {
	ctx, fingerprint := policyPeer("client-one")
	var events []Event
	gate, e := NewGate(policyBody(1, fingerprint), func(event Event) error { events = append(events, event); return nil })
	if e != nil {
		t.Fatal(e)
	}
	live, principal, release, e := gate.Admit(ctx, "control", "task.get", "project-one", ownedName)
	if e != nil || principal.ProjectID != "project_012345678901234567890123" {
		t.Fatal(principal, e)
	}
	defer release()
	for _, scope := range []struct{ role, space, name string }{{"control", "other-project", ownedName}, {"guest", "", ownedName}, {"control", "project-one", "arbitrary-task"}} {
		_, _, done, e := gate.Admit(ctx, scope.role, "task.get", scope.space, scope.name)
		done()
		if e == nil {
			t.Fatal("foreign/invalid scope authorized", scope)
		}
	}
	if e = gate.Reload([]byte(`{"version":1,"revision":2,"principals":[]}`)); e != nil {
		t.Fatal(e)
	}
	select {
	case <-live.Done():
	case <-time.After(time.Second):
		t.Fatal("revocation retained admitted call")
	}
	_, _, done, e := gate.Admit(ctx, "control", "task.get", "project-one", ownedName)
	done()
	if e == nil {
		t.Fatal("revoked certificate on existing TLS connection authorized")
	}
	if len(events) < 6 {
		t.Fatal("missing authorization/reload audit", len(events))
	}
}

func TestGatewayInvalidPolicyDeniesAllUntilHigherRevision(t *testing.T) {
	ctx, fingerprint := policyPeer("client-one")
	gate, e := NewGate(policyBody(7, fingerprint), func(Event) error { return nil })
	if e != nil {
		t.Fatal(e)
	}
	for _, body := range [][]byte{policyBody(7, fingerprint), []byte(`{"version":1,"revision":8,"unknown":true}`)} {
		if e = gate.Reload(body); e == nil {
			t.Fatal("invalid or replayed policy accepted")
		}
		_, _, done, e := gate.Admit(ctx, "control", "task.get", "project-one", ownedName)
		done()
		if e == nil {
			t.Fatal("invalid reload retained old allow policy")
		}
	}
	if e = gate.Reload(policyBody(8, fingerprint)); e != nil {
		t.Fatal(e)
	}
	_, _, done, e := gate.Admit(ctx, "guest", "process.start", "project-one", ownedName)
	defer done()
	if e != nil {
		t.Fatal("corrected higher revision did not restore authorized access", e)
	}
}

func TestGatewayPolicyRejectsAmbiguousCrossProjectBindings(t *testing.T) {
	_, fingerprint := policyPeer("client-one")
	for _, mode := range []string{"duplicate-cert", "shared-space", "multiple-spaces", "wildcard-space", "unpinned-image", "unknown-field", "trailing-json", "no-audit"} {
		t.Run(mode, func(t *testing.T) {
			var p Policy
			_ = json.Unmarshal(policyBody(1, fingerprint), &p)
			other := p.Principals[0]
			other.ID = "worker-two"
			other.CertificateSHA256 = strings.Repeat("b", 64)
			switch mode {
			case "duplicate-cert":
				p.Principals = append(p.Principals, p.Principals[0])
			case "shared-space":
				other.ProjectID = "project_112345678901234567890123"
				p.Principals = append(p.Principals, other)
			case "multiple-spaces":
				other.Atespace = "project-two"
				p.Principals = append(p.Principals, other)
			case "wildcard-space":
				p.Principals[0].Atespace = "*"
			case "unpinned-image":
				p.Principals[0].Images = []string{"runner:latest"}
			}
			body, _ := json.Marshal(p)
			if mode == "unknown-field" {
				body = []byte(strings.Replace(string(body), `"version":1`, `"version":1,"allowAll":true`, 1))
			}
			if mode == "trailing-json" {
				body = append(body, []byte(` {}`)...)
			}
			audit := func(Event) error { return nil }
			if mode == "no-audit" {
				audit = nil
			}
			if _, e := NewGate(body, audit); e == nil {
				t.Fatal("ambiguous policy accepted", mode)
			}
		})
	}
}

func TestGatewayAuditFailureRevokesAndPreventsAdmission(t *testing.T) {
	ctx, fingerprint := policyPeer("client-one")
	fail := false
	gate, e := NewGate(policyBody(1, fingerprint), func(Event) error {
		if fail {
			return errors.New("audit disk unavailable")
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	live, _, done, e := gate.Admit(ctx, "control", "task.get", "project-one", ownedName)
	if e != nil {
		t.Fatal(e)
	}
	defer done()
	fail = true
	_, _, reject, e := gate.Admit(ctx, "control", "task.delete", "project-one", ownedName)
	reject()
	if e == nil {
		t.Fatal("mutation admitted without audit receipt")
	}
	select {
	case <-live.Done():
	case <-time.After(time.Second):
		t.Fatal("audit failure retained previous calls")
	}
}

func TestGatewayConcurrentAdmissionAndReloadNeverLeavesOldCallsLive(t *testing.T) {
	ctx, fingerprint := policyPeer("client-one")
	gate, e := NewGate(policyBody(1, fingerprint), func(Event) error { return nil })
	if e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	calls := make(chan context.Context, 64)
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			live, _, _, e := gate.Admit(ctx, "control", "task.get", "project-one", ownedName)
			if e == nil {
				calls <- live
			}
		}()
	}
	close(start)
	if e = gate.Reload([]byte(`{"version":1,"revision":2,"principals":[]}`)); e != nil {
		t.Fatal(e)
	}
	wg.Wait()
	close(calls)
	for live := range calls {
		select {
		case <-live.Done():
		default:
			t.Fatal("old generation escaped reload cancellation")
		}
	}
}

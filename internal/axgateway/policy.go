package axgateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

type Principal struct {
	ID                string   `json:"id"`
	ProjectID         string   `json:"projectId"`
	Atespace          string   `json:"atespace"`
	CertificateSHA256 string   `json:"certificateSHA256"`
	Control           bool     `json:"control"`
	Guest             bool     `json:"guest"`
	Images            []string `json:"images"`
}
type Policy struct {
	Version    int         `json:"version"`
	Revision   uint64      `json:"revision"`
	Principals []Principal `json:"principals"`
}
type Event struct {
	At                string `json:"at"`
	Revision          uint64 `json:"revision"`
	Outcome           string `json:"outcome"`
	Method            string `json:"method"`
	PrincipalID       string `json:"principalId,omitempty"`
	ProjectID         string `json:"projectId,omitempty"`
	Atespace          string `json:"atespace,omitempty"`
	Name              string `json:"name,omitempty"`
	CertificateSHA256 string `json:"certificateSHA256,omitempty"`
}

var spacePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
var namePattern = regexp.MustCompile(`^is-sandbox-[a-f0-9]{24}$`)
var hashPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var imagePattern = regexp.MustCompile(`^[^\s]+@sha256:[a-f0-9]{64}$`)

// Gate owns both admission and cancellation registration under the same lock.
// No upstream mutation is authorized without a successful audit admission.
type Gate struct {
	mu         sync.Mutex
	available  bool
	revision   uint64
	principals map[string]Principal
	active     map[uint64]context.CancelFunc
	next       uint64
	audit      func(Event) error
}

func NewGate(body []byte, audit func(Event) error) (*Gate, error) {
	if audit == nil {
		return nil, fmt.Errorf("gateway requires an audit sink")
	}
	g := &Gate{audit: audit, active: map[uint64]context.CancelFunc{}}
	if e := g.Reload(body); e != nil {
		return nil, e
	}
	return g, nil
}

func decodeStrict(body []byte, target any) error {
	if len(body) == 0 || len(body) > 1<<20 {
		return fmt.Errorf("configuration exceeds bounded JSON document")
	}
	d := json.NewDecoder(bytes.NewReader(body))
	var value func(int) error
	value = func(depth int) error {
		if depth > 32 {
			return fmt.Errorf("configuration nesting exceeds limit")
		}
		token, e := d.Token()
		if e != nil {
			return e
		}
		if delimiter, ok := token.(json.Delim); ok {
			switch delimiter {
			case '{':
				seen := map[string]bool{}
				for d.More() {
					key, e := d.Token()
					if e != nil {
						return e
					}
					name, ok := key.(string)
					if !ok || seen[name] {
						return fmt.Errorf("configuration contains duplicate JSON fields")
					}
					seen[name] = true
					if e = value(depth + 1); e != nil {
						return e
					}
				}
			case '[':
				for d.More() {
					if e = value(depth + 1); e != nil {
						return e
					}
				}
			default:
				return fmt.Errorf("invalid JSON delimiter")
			}
			_, e = d.Token()
			return e
		}
		return nil
	}
	if e := value(0); e != nil {
		return fmt.Errorf("invalid strict configuration JSON")
	}
	if _, e := d.Token(); e != io.EOF {
		return fmt.Errorf("configuration must contain one JSON document")
	}
	d = json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	if e := d.Decode(target); e != nil {
		return fmt.Errorf("configuration has invalid or unknown fields")
	}
	return nil
}

func parsePolicy(body []byte) (Policy, error) {
	var p Policy
	if e := decodeStrict(body, &p); e != nil {
		return p, e
	}
	if p.Version != 1 || p.Revision == 0 || p.Principals == nil || len(p.Principals) > 4096 {
		return p, fmt.Errorf("invalid policy version, revision or principal list")
	}
	certs, ids, projects, spaces := map[string]bool{}, map[string]bool{}, map[string]string{}, map[string]string{}
	for _, principal := range p.Principals {
		if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`).MatchString(principal.ID) || !regexp.MustCompile(`^project_[a-f0-9]{24}$`).MatchString(principal.ProjectID) || !spacePattern.MatchString(principal.Atespace) || !hashPattern.MatchString(principal.CertificateSHA256) || certs[principal.CertificateSHA256] || ids[principal.ID] || !principal.Control && !principal.Guest || len(principal.Images) < 1 || len(principal.Images) > 32 {
			return p, fmt.Errorf("invalid or ambiguous certificate principal")
		}
		if prior := spaces[principal.Atespace]; prior != "" && prior != principal.ProjectID {
			return p, fmt.Errorf("atespace cannot belong to multiple projects")
		}
		if prior := projects[principal.ProjectID]; prior != "" && prior != principal.Atespace {
			return p, fmt.Errorf("project cannot span multiple atespaces")
		}
		images := map[string]bool{}
		for _, image := range principal.Images {
			if !imagePattern.MatchString(image) || len(image) > 1024 || images[image] {
				return p, fmt.Errorf("principal requires distinct pinned images")
			}
			images[image] = true
		}
		certs[principal.CertificateSHA256], ids[principal.ID] = true, true
		spaces[principal.Atespace], projects[principal.ProjectID] = principal.ProjectID, principal.Atespace
	}
	return p, nil
}

func (g *Gate) denyLocked() {
	g.available = false
	for _, cancel := range g.active {
		cancel()
	}
	g.active = map[uint64]context.CancelFunc{}
}

func (g *Gate) Reload(body []byte) error {
	policy, e := parsePolicy(body)
	g.mu.Lock()
	defer g.mu.Unlock()
	g.denyLocked()
	if e == nil && policy.Revision <= g.revision {
		e = fmt.Errorf("policy revision must strictly increase")
	}
	if e != nil {
		_ = g.audit(Event{At: time.Now().UTC().Format(time.RFC3339Nano), Revision: g.revision, Method: "policy.reload", Outcome: "denied"})
		return e
	}
	if e = g.audit(Event{At: time.Now().UTC().Format(time.RFC3339Nano), Revision: policy.Revision, Method: "policy.reload", Outcome: "accepted"}); e != nil {
		return fmt.Errorf("policy audit unavailable")
	}
	g.revision = policy.Revision
	g.principals = map[string]Principal{}
	for _, p := range policy.Principals {
		g.principals[p.CertificateSHA256] = p
	}
	g.available = true
	return nil
}

func clientFingerprint(ctx context.Context) string {
	remote, ok := peer.FromContext(ctx)
	if !ok {
		return ""
	}
	info, ok := remote.AuthInfo.(credentials.TLSInfo)
	if !ok || info.State.Version < tls.VersionTLS13 || info.State.NegotiatedProtocol != "h2" || len(info.State.PeerCertificates) == 0 || len(info.State.VerifiedChains) == 0 {
		return ""
	}
	cert := info.State.PeerCertificates[0]
	if time.Now().Before(cert.NotBefore) || !time.Now().Before(cert.NotAfter) {
		return ""
	}
	sum := sha256.Sum256(cert.Raw)
	return hex.EncodeToString(sum[:])
}

func (g *Gate) Admit(ctx context.Context, role, method, space, name string) (context.Context, Principal, func(), error) {
	return g.admit(ctx, role, method, space, name, nil)
}

func (g *Gate) admit(ctx context.Context, role, method, space, name string, checks []func(Principal) bool) (context.Context, Principal, func(), error) {
	fingerprint := clientFingerprint(ctx)
	g.mu.Lock()
	defer g.mu.Unlock()
	p, exists := g.principals[fingerprint]
	allowed := g.available && exists && ctx.Err() == nil && p.Atespace == space && spacePattern.MatchString(space) && (name == "" || namePattern.MatchString(name)) && (role == "control" && p.Control || role == "guest" && p.Guest)
	if allowed {
		for _, check := range checks {
			if !check(p) {
				allowed = false
				break
			}
		}
	}
	event := Event{At: time.Now().UTC().Format(time.RFC3339Nano), Revision: g.revision, Outcome: "denied", Method: method, CertificateSHA256: fingerprint}
	if exists {
		event.PrincipalID, event.ProjectID = p.ID, p.ProjectID
	}
	if spacePattern.MatchString(space) {
		event.Atespace = space
	}
	if namePattern.MatchString(name) {
		event.Name = name
	}
	if allowed {
		event.Outcome = "admitted"
	}
	if e := g.audit(event); e != nil {
		g.denyLocked()
		allowed = false
	}
	if !allowed {
		return ctx, Principal{}, func() {}, status.Error(codes.PermissionDenied, "gateway request is not authorized")
	}
	live, cancel := context.WithCancel(ctx)
	g.next++
	id := g.next
	g.active[id] = cancel
	p.Images = append([]string{}, p.Images...)
	return live, p, func() { cancel(); g.mu.Lock(); delete(g.active, id); g.mu.Unlock() }, nil
}

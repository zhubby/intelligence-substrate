package platform

import (
	"context"
	"strings"
	"testing"
)

func sandboxTransport() M {
	identity := func(endpoint, name string) M {
		return M{"endpoint": endpoint, "serverName": name, "serverSPKI": strings.Repeat("b", 64), "caDigest": strings.Repeat("c", 64), "clientCertDigest": strings.Repeat("d", 64)}
	}
	return M{"version": "mtls-v1", "control": identity("control.example:443", "control.example"), "guest": identity("[::1]:7443", "guest.example")}
}

func TestSandboxTransportIdentitySurvivesReplayAndCannotChange(t *testing.T) {
	f := newAllowanceFixture(t)
	input := sandboxInput(f)
	Obj(input["backend"])["transport"] = sandboxTransport()
	first := must(t, f.s, f.worker, "sandboxes.prepare", input)
	second := must(t, f.s, f.worker, "sandboxes.prepare", input)
	if first.ID != second.ID || S(first.Data, "backendDigest") != Hash(input["backend"]) || Hash(Obj(first.Data["backend"])["transport"]) != Hash(sandboxTransport()) {
		t.Fatal("transport binding lost", first, second)
	}
	Obj(Obj(Obj(input["backend"])["transport"])["guest"])["serverSPKI"] = strings.Repeat("e", 64)
	if _, e := f.s.Command(context.Background(), f.worker, "sandboxes.prepare", input); e == nil {
		t.Fatal("same command key changed remote identity")
	}
	if all, _ := f.s.Store.List(f.p.ID, "sandbox", f.g.ID); len(all) != 1 {
		t.Fatal("transport replay created another resource")
	}
}

func TestSandboxTransportRejectsCredentialAndRouteInjection(t *testing.T) {
	for _, mode := range []string{"private-file", "private-key", "unknown", "missing-guest", "wrong-version", "resolver", "url", "port", "name", "pin", "numeric", "backend-extra"} {
		t.Run(mode, func(t *testing.T) {
			f := newAllowanceFixture(t)
			input := sandboxInput(f)
			backend := Obj(input["backend"])
			transport := sandboxTransport()
			backend["transport"] = transport
			control := Obj(transport["control"])
			switch mode {
			case "private-file":
				control["clientKeyFile"] = "/private/node/key.pem"
			case "private-key":
				control["privateKey"] = "not-a-key"
			case "unknown":
				transport["fallback"] = "plaintext"
			case "missing-guest":
				delete(transport, "guest")
			case "wrong-version":
				transport["version"] = "auto"
			case "resolver":
				control["endpoint"] = "dns:///control.example:443"
			case "url":
				control["endpoint"] = "https://control.example:443"
			case "port":
				control["endpoint"] = "control.example:0"
			case "name":
				control["serverName"] = "*.example"
			case "pin":
				control["serverSPKI"] = strings.Repeat("B", 64)
			case "numeric":
				control["caDigest"] = 42
			case "backend-extra":
				backend["server"] = "other.example:443"
			}
			if _, e := f.s.Command(context.Background(), f.worker, "sandboxes.prepare", input); e == nil {
				t.Fatal("unsafe transport persisted", mode)
			}
			if all, _ := f.s.Store.List(f.p.ID, "sandbox", f.g.ID); len(all) != 0 {
				t.Fatal("invalid identity left durable ownership", mode)
			}
		})
	}
}

package integrations

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	ax "github.com/google/ax/pkg/apis/v1alpha1"
	p "github.com/intelligence-substrate/platform/internal/platform"
	"github.com/intelligence-substrate/platform/internal/testutil/axfixture"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

type rejectingAXOutput struct{}

func (rejectingAXOutput) Write([]byte) (int, error) { return 0, errors.New("test storage unavailable") }

func TestAXNoImplicitCLI(t *testing.T) {
	a, log := axScript(t, "printf 'unexpected-backend\\n'\n")
	// Using the public JSON contract keeps this regression independent of a
	// compile-time field. Old implementations silently inherited CLI routing.
	var input map[string]any
	body, _ := json.Marshal(a)
	if e := json.Unmarshal(body, &input); e != nil {
		t.Fatal(e)
	}
	input["allowLegacyCLI"] = false
	body, _ = json.Marshal(input)
	if e := json.Unmarshal(body, &a); e != nil {
		t.Fatal(e)
	}
	if _, e := a.Describe(context.Background(), "owned-task"); e == nil {
		t.Fatal("unconfigured execution inherited CLI routing")
	}
	if _, e := os.Stat(log); !os.IsNotExist(e) {
		t.Fatal("unconfigured execution invoked external CLI", e)
	}
}

func TestAXSecureNeverFallsBackToCLI(t *testing.T) {
	a, log := axScript(t, "printf 'unexpected-backend\\n'\n")
	body, _ := json.Marshal(map[string]any{"binary": a.Binary, "atespace": "test", "secure": map[string]any{"control": map[string]any{"endpoint": "127.0.0.1:1"}}})
	if e := json.Unmarshal(body, &a); e != nil {
		t.Fatal(e)
	}
	if _, e := a.Describe(context.Background(), "owned-task"); e == nil {
		t.Fatal("invalid secure configuration was accepted")
	}
	if _, e := os.Stat(log); !os.IsNotExist(e) {
		t.Fatal("secure connection failure fell back to CLI", e)
	}
}

func secureAX(t *testing.T, config map[string]any) AX {
	t.Helper()
	var a AX
	body, e := json.Marshal(config)
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(body, &a); e != nil {
		t.Fatal(e)
	}
	return a
}
func secureFixture(t *testing.T) *axfixture.Fixture {
	t.Helper()
	f, e := axfixture.New(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(f.Close)
	return f
}
func seedSecureTask(t *testing.T, f *axfixture.Fixture, name string) {
	t.Helper()
	conn, e := grpc.NewClient("passthrough:///"+f.Control["endpoint"].(string), grpc.WithTransportCredentials(credentials.NewTLS(f.ControlTLS.Clone())))
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	c := ax.NewAXClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, e = c.UpdateWorkspace(ctx, &ax.UpdateWorkspaceRequest{Workspace: &ax.Workspace{Metadata: &ax.ObjectMeta{Name: name, Atespace: "test"}}}); e != nil {
		t.Fatal(e)
	}
	if _, e = c.CreateTask(ctx, &ax.CreateTaskRequest{Task: &ax.Task{Metadata: &ax.ObjectMeta{Name: name, Atespace: "test"}, Spec: &ax.TaskSpec{Debug: true, Workspace: &ax.WorkspaceRef{Name: name}}}}); e != nil {
		t.Fatal(e)
	}
}

func TestAXSecureAuthenticatedControlIgnoresAmbientRouting(t *testing.T) {
	f := secureFixture(t)
	seedSecureTask(t, f, "owned-task")
	for _, name := range []string{"AX_SERVER", "AX_HOME", "KUBECONFIG", "HTTPS_PROXY", "HTTP_PROXY", "ALL_PROXY"} {
		t.Setenv(name, "not-the-configured-service")
	}
	a := secureAX(t, f.Config())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	body, e := a.Describe(ctx, "owned-task")
	if e != nil || !strings.Contains(body, "Running") {
		t.Fatalf("authenticated control failed: %q %v", body, e)
	}
	if e = a.Suspend(ctx, "owned-task"); e != nil {
		t.Fatal(e)
	}
	if e = a.Resume(ctx, "owned-task"); e != nil {
		t.Fatal(e)
	}
	if e = a.Cancel(ctx, "owned-task"); e != nil {
		t.Fatal(e)
	}
	if tasks, workspaces := f.Counts(); tasks != 0 || workspaces != 0 {
		t.Fatal(tasks, workspaces)
	}
	for _, call := range f.Calls() {
		if call.ClientSHA256 == "" {
			t.Fatal("no authenticated client at wire boundary")
		}
	}
}

func TestAXSecureOwnedCommandRoundTrip(t *testing.T) {
	for _, nonzero := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal", true: "nonzero"}[nonzero], func(t *testing.T) {
			f := secureFixture(t)
			a := secureAX(t, f.Config())
			dir := filepath.Join(t.TempDir(), "source")
			if e := os.Mkdir(dir, 0700); e != nil {
				t.Fatal(e)
			}
			if e := os.WriteFile(filepath.Join(dir, "input.bin"), []byte{0, 1, 2, 255}, 0600); e != nil {
				t.Fatal(e)
			}
			command := []string{"/bin/sh", "-c", "cp input.bin result.bin; printf 'mtls-command-ran\\n'; exit 0"}
			want := 0
			if nonzero {
				command[2] = "cp input.bin result.bin; printf 'mtls-command-ran\\n'; exit 7"
				want = 7
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			var out bytes.Buffer
			exit, stopped, e := a.RunOwnedCommand(ctx, "sandbox_012345678901234567890123", dir, command, &out)
			if e != nil || exit != want || !stopped {
				t.Fatalf("exit=%d stopped=%v error=%v log=%s wire=%+v", exit, stopped, e, out.String(), f.Calls())
			}
			result, e := os.ReadFile(filepath.Join(dir, "result.bin"))
			if e != nil || !bytes.Equal(result, []byte{0, 1, 2, 255}) {
				t.Fatal(result, e)
			}
			if !strings.Contains(out.String(), "mtls-command-ran") {
				t.Fatal("actual command output missing")
			}
			seen := map[string]bool{}
			for _, c := range f.Calls() {
				seen[c.Operation] = true
				if strings.HasPrefix(c.Operation, "process.") && c.Actor != "test/is-sandbox-012345678901234567890123" {
					t.Fatal("guest actor binding lost", c)
				}
			}
			for _, op := range []string{"workspace.update", "task.create", "task.resume", "process.start", "process.stream", "task.delete", "workspace.delete", "tasks.list", "workspaces.list"} {
				if !seen[op] {
					t.Fatal("wire operation missing", op)
				}
			}
			if tasks, workspaces := f.Counts(); tasks != 0 || workspaces != 0 {
				t.Fatal(tasks, workspaces)
			}
		})
	}
}

func TestAXSecureGuestReportsActualExit(t *testing.T) {
	f := secureFixture(t)
	seedSecureTask(t, f, "owned-task")
	a := secureAX(t, f.Config())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var out bytes.Buffer
	if exit, e := a.Exec(ctx, "owned-task", []string{"/bin/true"}, &out); e != nil || exit != 0 {
		t.Fatalf("Linux guest readiness command: exit=%d err=%v", exit, e)
	}
	exit, e := a.Exec(ctx, "owned-task", []string{"/bin/sh", "-c", "printf 'real-exit\\n'; exit 7"}, &out)
	if e != nil || exit != 7 || out.String() != "real-exit\n" {
		t.Fatalf("exit=%d err=%v output=%q calls=%+v", exit, e, out.String(), f.Calls())
	}
}

func TestAXSecureRejectsTLSAndConfigurationDriftBeforeCreation(t *testing.T) {
	for _, name := range []string{"control-ca", "control-name", "control-pin", "control-client", "guest-ca", "guest-name", "guest-pin", "guest-client", "resolver", "mixed", "public-key-file", "symlink"} {
		t.Run(name, func(t *testing.T) {
			f := secureFixture(t)
			config := f.Config()
			secure := config["secure"].(map[string]any)
			control, guest := secure["control"].(map[string]any), secure["guest"].(map[string]any)
			switch name {
			case "control-ca":
				control["caFile"] = guest["caFile"]
			case "guest-ca":
				guest["caFile"] = control["caFile"]
			case "control-name":
				control["serverName"] = "other.ax.test"
			case "guest-name":
				guest["serverName"] = "other.ax.test"
			case "control-pin":
				control["serverSPKI"] = strings.Repeat("f", 64)
			case "guest-pin":
				guest["serverSPKI"] = strings.Repeat("f", 64)
			case "control-client":
				control["clientCertFile"], control["clientKeyFile"] = guest["clientCertFile"], guest["clientKeyFile"]
			case "guest-client":
				guest["clientCertFile"], guest["clientKeyFile"] = control["clientCertFile"], control["clientKeyFile"]
			case "resolver":
				control["endpoint"] = "dns:///other.ax.test:443"
			case "mixed":
				config["allowLegacyCLI"] = true
			case "public-key-file":
				if e := os.Chmod(control["clientKeyFile"].(string), 0644); e != nil {
					t.Fatal(e)
				}
			case "symlink":
				path := filepath.Join(t.TempDir(), "ca-link")
				if e := os.Symlink(control["caFile"].(string), path); e != nil {
					t.Fatal(e)
				}
				control["caFile"] = path
			}
			a := secureAX(t, config)
			dir := t.TempDir()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			var out bytes.Buffer
			exit, stopped, e := a.RunOwnedCommand(ctx, "sandbox_012345678901234567890123", dir, []string{"/bin/true"}, &out)
			if e == nil || exit == 0 || !stopped {
				t.Fatalf("invalid trust configuration accepted or left external state: %d %v %v", exit, stopped, e)
			}
			if len(f.Calls()) > 0 {
				t.Fatal("creation or API request preceded authentication of both endpoints", f.Calls())
			}
			if strings.Contains(e.Error(), "PRIVATE KEY") || strings.Contains(e.Error(), filepath.Dir(control["clientKeyFile"].(string))) {
				t.Fatal("private credential location leaked", e)
			}
		})
	}
}

func TestAXSecureGuestRejectsUnboundOrUnconfirmedResults(t *testing.T) {
	for _, mode := range []string{"wrong-task", "wrong-space", "unsafe-actor", "other-actor", "not-debug", "missing-process", "missing-exit", "stream-error", "wrong-process", "running-exit", "invalid-exit"} {
		t.Run(mode, func(t *testing.T) {
			f := secureFixture(t)
			seedSecureTask(t, f, "owned-task")
			f.SetMode(mode)
			a := secureAX(t, f.Config())
			var out bytes.Buffer
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			exit, e := a.Exec(ctx, "owned-task", []string{"/bin/sh", "-c", "printf 'observed-output\\n'"}, &out)
			if e == nil || exit == 0 {
				t.Fatal("unconfirmed result became success", mode, exit, e)
			}
			if mode == "wrong-task" || mode == "wrong-space" || mode == "unsafe-actor" || mode == "other-actor" || mode == "not-debug" {
				for _, c := range f.Calls() {
					if c.Operation == "process.start" {
						t.Fatal("invalid task binding reached guest", c)
					}
				}
			}
		})
	}
}

func TestAXSecureGuestCancellationAndOutputFailure(t *testing.T) {
	for _, mode := range []string{"stall", "writer"} {
		t.Run(mode, func(t *testing.T) {
			f := secureFixture(t)
			seedSecureTask(t, f, "owned-task")
			a := secureAX(t, f.Config())
			f.SetMode(mode)
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel()
			exit, e := a.Exec(ctx, "owned-task", []string{"/bin/sh", "-c", "printf 'must-retain-output\\n'"}, rejectingAXOutput{})
			if e == nil || exit == 0 {
				t.Fatal("cancelled/lost-output process succeeded", mode, exit, e)
			}
		})
	}
}

func TestAXSecureCleanupIsIndependentOfCommandExit(t *testing.T) {
	for _, mode := range []string{"partial", "cleanup-denied", "retained", "hidden-retained", "page-error", "point-read-retained", "list-error", "list-wrong-scope"} {
		t.Run(mode, func(t *testing.T) {
			f := secureFixture(t)
			f.SetMode(mode)
			a := secureAX(t, f.Config())
			var out bytes.Buffer
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			exit, stopped, e := a.RunOwnedCommand(ctx, "sandbox_012345678901234567890123", t.TempDir(), []string{"/bin/sh", "-c", "exit 0"}, &out)
			if e == nil || exit == 0 {
				t.Fatal("unknown cleanup or failed create became success", mode, exit, e)
			}
			if stopped {
				t.Fatal("incorrect external lifecycle state", mode, stopped, e)
			}
			seen := map[string]bool{}
			for _, c := range f.Calls() {
				seen[c.Operation] = true
			}
			for _, op := range []string{"task.delete", "workspace.delete", "tasks.list", "workspaces.list"} {
				if !seen[op] {
					t.Fatal("cleanup skipped independent observation", mode, op)
				}
			}
		})
	}
}

func TestAXSecureSessionFreezesMaterialAndRouting(t *testing.T) {
	f := secureFixture(t)
	original := secureAX(t, f.Config())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	a, backend, close, e := original.Open(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer close()
	for _, config := range []AXTLSConfig{original.Secure.Control, original.Secure.Guest} {
		for _, path := range []string{config.CAFile, config.ClientCertFile, config.ClientKeyFile} {
			if e = os.WriteFile(path, []byte("replacement-invalid-material"), 0600); e != nil {
				t.Fatal(e)
			}
		}
	}
	original.Secure.Control.Endpoint = "127.0.0.1:1"
	original.Secure.Guest.Endpoint = "127.0.0.1:1"
	if p.S(p.Obj(p.Obj(backend["transport"])["control"]), "endpoint") == "127.0.0.1:1" {
		t.Fatal("public identity changed with source configuration")
	}
	var out bytes.Buffer
	exit, stopped, e := a.RunOwnedCommand(ctx, "sandbox_012345678901234567890123", t.TempDir(), []string{"/bin/sh", "-c", "exit 0"}, &out)
	if e != nil || exit != 0 || !stopped {
		t.Fatal("existing command reloaded changed routing/credentials", exit, stopped, e)
	}
	if _, _, close, e := original.Open(ctx); e == nil {
		close()
		t.Fatal("new command reused invalid/replaced trust material")
	}
}

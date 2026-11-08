package axgateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	guest "github.com/agent-substrate/env/proto/ateenv/v1alpha"
	ax "github.com/google/ax/pkg/apis/v1alpha1"
	"github.com/intelligence-substrate/platform/internal/testutil/axfixture"
	"github.com/intelligence-substrate/platform/internal/testutil/tlsfixture"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/types/known/structpb"
)

type gatewayTest struct {
	upstream    *axfixture.Fixture
	gate        *Gate
	control     ax.AXClient
	guest       guest.ProcessServiceClient
	fingerprint string
}

func newGatewayTest(t *testing.T) *gatewayTest {
	return newGatewayTestAudit(t, func(Event) error { return nil })
}
func newGatewayTestAudit(t *testing.T, audit func(Event) error) *gatewayTest {
	t.Helper()
	upstream, e := axfixture.New(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(upstream.Close)
	a, e := tlsfixture.New(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	client, e := a.Issue("worker")
	if e != nil {
		t.Fatal(e)
	}
	var policy Policy
	_ = json.Unmarshal(policyBody(1, client.SHA256), &policy)
	policy.Principals[0].Atespace = "test"
	body, _ := json.Marshal(policy)
	gate, e := NewGate(body, audit)
	if e != nil {
		t.Fatal(e)
	}
	upControl, e := grpc.NewClient(upstream.Control["endpoint"].(string), grpc.WithTransportCredentials(credentials.NewTLS(upstream.ControlTLS.Clone())))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { upControl.Close() })
	upGuest, e := grpc.NewClient(upstream.Guest["endpoint"].(string), grpc.WithTransportCredentials(credentials.NewTLS(upstream.GuestTLS.Clone())))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { upGuest.Close() })
	g := NewGateway(gate, ax.NewAXClient(upControl), guest.NewProcessServiceClient(upGuest))
	f := &gatewayTest{upstream: upstream, gate: gate, fingerprint: client.SHA256}
	for _, role := range []string{"control", "guest"} {
		listener, e := net.Listen("tcp", "127.0.0.1:0")
		if e != nil {
			t.Fatal(e)
		}
		server := grpc.NewServer(grpc.Creds(credentials.NewTLS(a.ServerTLS.Clone())))
		if role == "control" {
			ax.RegisterAXServer(server, g.ControlServer())
		} else {
			guest.RegisterProcessServiceServer(server, g.GuestServer())
		}
		go server.Serve(listener)
		t.Cleanup(server.Stop)
		connection, e := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(credentials.NewTLS(client.TLS.Clone())), grpc.WithDisableRetry())
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() { connection.Close() })
		if role == "control" {
			f.control = ax.NewAXClient(connection)
		} else {
			f.guest = guest.NewProcessServiceClient(connection)
		}
	}
	return f
}
func taskRequest() *ax.Task {
	return &ax.Task{ApiVersion: "ax.io/v1alpha1", Kind: "Task", Metadata: &ax.ObjectMeta{Name: ownedName, Atespace: "test"}, Spec: &ax.TaskSpec{Image: "runner@sha256:" + strings.Repeat("a", 64), Command: []string{"/bin/true"}, Debug: true, Workspace: &ax.WorkspaceRef{Name: ownedName, Path: "/workspace"}, Resources: &ax.ResourcesSpec{Limits: &ax.ResourceList{Cpu: "2", Memory: "4Gi"}}}}
}
func (f *gatewayTest) seed(t *testing.T, ctx context.Context) {
	t.Helper()
	_, e := f.control.UpdateWorkspace(ctx, &ax.UpdateWorkspaceRequest{Workspace: &ax.Workspace{ApiVersion: "ax.io/v1alpha1", Kind: "Workspace", Metadata: &ax.ObjectMeta{Name: ownedName, Atespace: "test"}, Spec: &ax.WorkspaceSpec{}}})
	if e != nil {
		t.Fatal(e)
	}
	_, e = f.control.CreateTask(ctx, &ax.CreateTaskRequest{Task: taskRequest()})
	if e != nil {
		t.Fatal(e)
	}
}
func TestGatewayTLSLifecycleUsesExactActorAndActualExit(t *testing.T) {
	f := newGatewayTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	f.seed(t, ctx)
	ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("ate-target-actor", "test/"+ownedName, "authorization", "must-not-forward"))
	process, e := f.guest.StartProcess(ctx, &guest.StartProcessRequest{Command: []string{"/bin/sh", "-c", "printf 'gateway-command\\n'; exit 7"}})
	if e != nil {
		t.Fatal(e)
	}
	stream, e := f.guest.StreamProcessOutput(ctx, &guest.StreamProcessOutputRequest{ProcessId: process.ProcessId, Follow: true})
	if e != nil {
		t.Fatal(e)
	}
	var output strings.Builder
	exit := -1
	for {
		item, e := stream.Recv()
		if e == io.EOF {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
		output.Write(item.GetStdout())
		if result := item.GetExit(); result != nil {
			exit = int(result.ExitCode)
		}
	}
	if exit != 7 || output.String() != "gateway-command\n" {
		t.Fatal(exit, output.String())
	}
	for _, call := range f.upstream.Calls() {
		if strings.HasPrefix(call.Operation, "process.") && call.Actor != "test/"+ownedName {
			t.Fatal(call)
		}
		if call.ForwardedCredential {
			t.Fatal("upstream received user credential metadata")
		}
	}
	if _, e := f.control.DeleteTask(context.Background(), &ax.DeleteTaskRequest{Atespace: "test", Name: ownedName}); e != nil {
		t.Fatal(e)
	}
}
func TestGatewayRejectsScopeImageFieldsAndUnknownMethodsBeforeUpstream(t *testing.T) {
	for _, mode := range []string{"foreign-space", "foreign-name", "image", "workspace", "env", "command", "unknown", "foreign-list", "model", "resources", "config", "routing", "offset", "limit", "workspace-config"} {
		t.Run(mode, func(t *testing.T) {
			f := newGatewayTest(t)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			request := &ax.CreateTaskRequest{Task: taskRequest()}
			switch mode {
			case "foreign-space":
				request.Task.Metadata.Atespace = "foreign"
			case "foreign-name":
				request.Task.Metadata.Name = "some-other-task"
			case "image":
				request.Task.Spec.Image = "runner:latest"
			case "workspace":
				request.Task.Spec.Workspace.Name = "foreign"
			case "env":
				request.Task.Spec.Env = []*ax.EnvVar{{Name: "TOKEN", Value: "untrusted"}}
			case "command":
				request.Task.Spec.Command = []string{"/bin/sh"}
			case "resources":
				request.Task.Spec.Resources.Requests = &ax.ResourceList{Cpu: "100"}
			case "config":
				request.Task.Spec.Config = &structpb.Struct{}
			case "routing":
				ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("ate-target-actor", "foreign/actor"))
			case "unknown":
				request.Task.ProtoReflect().SetUnknown(protowire.AppendVarint(protowire.AppendTag(nil, 99, protowire.VarintType), 1))
			}
			var e error
			if mode == "workspace-config" {
				_, e = f.control.UpdateWorkspace(ctx, &ax.UpdateWorkspaceRequest{Workspace: &ax.Workspace{ApiVersion: "ax.io/v1alpha1", Kind: "Workspace", Metadata: &ax.ObjectMeta{Name: ownedName, Atespace: "test"}, Spec: &ax.WorkspaceSpec{Git: []*ax.GitRepo{{Repo: "https://example.invalid/repo"}}}}})
			} else if mode == "offset" || mode == "limit" {
				query := &ax.ListTasksRequest{Atespace: "test", Limit: 100, Offset: 1}
				if mode == "limit" {
					query.Limit = 500
					query.Offset = 0
				}
				_, e = f.control.ListTasks(ctx, query)
			} else if mode == "foreign-list" {
				_, e = f.control.ListTasks(ctx, &ax.ListTasksRequest{Atespace: "foreign"})
			} else if mode == "model" {
				_, e = f.control.ListModels(ctx, &ax.ListModelsRequest{Atespace: "test"})
			} else {
				_, e = f.control.CreateTask(ctx, request)
			}
			if e == nil {
				t.Fatal("unauthorized request succeeded", mode)
			}
			if len(f.upstream.Calls()) != 0 {
				t.Fatal("unauthorized request reached upstream", mode, f.upstream.Calls())
			}
		})
	}
}
func TestGatewayGuestRejectsSpoofedMetadataAndUnboundActor(t *testing.T) {
	for _, mode := range []string{"missing", "duplicate", "foreign", "other-actor", "not-debug", "wrong-task", "wrong-space"} {
		t.Run(mode, func(t *testing.T) {
			f := newGatewayTest(t)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			f.seed(t, ctx)
			route := []string{"ate-target-actor", "test/" + ownedName}
			if mode == "missing" {
				route = nil
			}
			if mode == "duplicate" {
				route = append(route, "ate-target-actor", "test/"+ownedName)
			}
			if mode == "foreign" {
				route = []string{"ate-target-actor", "foreign/" + ownedName}
			}
			if mode == "other-actor" || mode == "not-debug" || mode == "wrong-task" || mode == "wrong-space" {
				f.upstream.SetMode(mode)
			}
			ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs(route...))
			_, e := f.guest.StartProcess(ctx, &guest.StartProcessRequest{Command: []string{"/bin/sh", "-c", "exit 0"}})
			if e == nil {
				t.Fatal("unbound guest command authorized", mode)
			}
			for _, call := range f.upstream.Calls() {
				if call.Operation == "process.start" {
					t.Fatal("unbound command reached guest", call)
				}
			}
		})
	}
}
func TestGatewayMalformedResponsesCannotProveAbsence(t *testing.T) {
	for _, mode := range []string{"list-wrong-scope", "wrong-task", "wrong-space"} {
		t.Run(mode, func(t *testing.T) {
			f := newGatewayTest(t)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			f.seed(t, ctx)
			f.upstream.SetMode(mode)
			var e error
			if mode == "list-wrong-scope" {
				_, e = f.control.ListTasks(ctx, &ax.ListTasksRequest{Atespace: "test", Limit: 100})
			} else {
				_, e = f.control.GetTask(ctx, &ax.GetTaskRequest{Atespace: "test", Name: ownedName})
			}
			if e == nil {
				t.Fatal("malformed upstream response accepted", mode)
			}
		})
	}
}
func TestGatewayRevokesLiveOutputStreamAndSameTLSConnection(t *testing.T) {
	f := newGatewayTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	f.seed(t, ctx)
	f.upstream.SetMode("stream-stall")
	ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("ate-target-actor", "test/"+ownedName))
	process, e := f.guest.StartProcess(ctx, &guest.StartProcessRequest{Command: []string{"/bin/sh", "-c", "printf 'stream-ready'"}})
	if e != nil {
		t.Fatal(e)
	}
	stream, e := f.guest.StreamProcessOutput(ctx, &guest.StreamProcessOutputRequest{ProcessId: process.ProcessId, Follow: true})
	if e != nil {
		t.Fatal(e)
	}
	if first, e := stream.Recv(); e != nil || string(first.GetStdout()) != "stream-ready" {
		t.Fatal(first, e)
	}
	if e = f.gate.Reload([]byte(`{"version":1,"revision":2,"principals":[]}`)); e != nil {
		t.Fatal(e)
	}
	if _, e = stream.Recv(); status.Code(e) != codes.Canceled {
		t.Fatal("revoked stream remained active", e)
	}
	if _, e = f.control.GetTask(context.Background(), &ax.GetTaskRequest{Atespace: "test", Name: ownedName}); status.Code(e) != codes.PermissionDenied {
		t.Fatal("revoked existing connection retained access", e)
	}
}

func TestGatewayAuditFailureCannotReachMutationAndRevokesConnections(t *testing.T) {
	var failed atomic.Bool
	f := newGatewayTestAudit(t, func(Event) error {
		if failed.Load() {
			return errors.New("test audit failed")
		}
		return nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	f.seed(t, ctx)
	before := len(f.upstream.Calls())
	failed.Store(true)
	if _, e := f.control.DeleteTask(ctx, &ax.DeleteTaskRequest{Atespace: "test", Name: ownedName}); status.Code(e) != codes.PermissionDenied {
		t.Fatal(e)
	}
	failed.Store(false)
	if _, e := f.control.GetTask(ctx, &ax.GetTaskRequest{Atespace: "test", Name: ownedName}); status.Code(e) != codes.PermissionDenied {
		t.Fatal("failed audit did not revoke", e)
	}
	if len(f.upstream.Calls()) != before {
		t.Fatal("audit failure allowed upstream request")
	}
}
func TestGatewayRolesAndMalformedReloadApplyOnExistingTLSConnections(t *testing.T) {
	for _, mode := range []string{"control-only", "guest-only", "unregistered", "malformed", "stale"} {
		t.Run(mode, func(t *testing.T) {
			f := newGatewayTest(t)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			f.seed(t, ctx)
			var p Policy
			_ = json.Unmarshal(policyBody(2, f.fingerprint), &p)
			p.Principals[0].Atespace = "test"
			if mode == "control-only" {
				p.Principals[0].Guest = false
			}
			if mode == "guest-only" {
				p.Principals[0].Control = false
			}
			if mode == "unregistered" {
				p.Principals[0].CertificateSHA256 = strings.Repeat("f", 64)
			}
			body, _ := json.Marshal(p)
			if mode == "malformed" {
				body = []byte(`{"version":1`)
			}
			if mode == "stale" {
				p.Revision = 1
				body, _ = json.Marshal(p)
			}
			e := f.gate.Reload(body)
			if (mode == "malformed" || mode == "stale") != (e != nil) {
				t.Fatal(e)
			}
			before := len(f.upstream.Calls())
			if mode != "control-only" {
				_, e = f.control.GetTask(ctx, &ax.GetTaskRequest{Atespace: "test", Name: ownedName})
				if status.Code(e) != codes.PermissionDenied {
					t.Fatal(e)
				}
			}
			if mode != "guest-only" {
				route := metadata.NewOutgoingContext(ctx, metadata.Pairs("ate-target-actor", "test/"+ownedName))
				_, e = f.guest.StartProcess(route, &guest.StartProcessRequest{Command: []string{"/bin/sh", "-c", "exit 0"}})
				if status.Code(e) != codes.PermissionDenied {
					t.Fatal(e)
				}
			}
			if len(f.upstream.Calls()) != before {
				t.Fatal("denied role/reload reached upstream")
			}
		})
	}
}
func TestGatewayRejectsMalformedGuestCompletion(t *testing.T) {
	for _, mode := range []string{"missing-exit", "wrong-process", "running-exit", "invalid-exit", "missing-process", "start-invalid-exit"} {
		t.Run(mode, func(t *testing.T) {
			f := newGatewayTest(t)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			f.seed(t, ctx)
			f.upstream.SetMode(mode)
			ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("ate-target-actor", "test/"+ownedName))
			p, e := f.guest.StartProcess(ctx, &guest.StartProcessRequest{Command: []string{"/bin/sh", "-c", "exit 0"}})
			if mode == "missing-process" || mode == "start-invalid-exit" {
				if status.Code(e) != codes.DataLoss {
					t.Fatal("malformed process accepted", p, e)
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			stream, e := f.guest.StreamProcessOutput(ctx, &guest.StreamProcessOutputRequest{ProcessId: p.ProcessId, Follow: true})
			if e != nil {
				t.Fatal(e)
			}
			for {
				_, e = stream.Recv()
				if e != nil {
					break
				}
			}
			if status.Code(e) != codes.DataLoss {
				t.Fatal("malformed completion accepted", e)
			}
		})
	}
}

func TestGatewayCreateResponseCannotSubstituteAnotherAllowedImage(t *testing.T) {
	f := newGatewayTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var policy Policy
	_ = json.Unmarshal(policyBody(2, f.fingerprint), &policy)
	policy.Principals[0].Atespace = "test"
	policy.Principals[0].Images = append(policy.Principals[0].Images, "runner@sha256:"+strings.Repeat("b", 64))
	body, _ := json.Marshal(policy)
	if e := f.gate.Reload(body); e != nil {
		t.Fatal(e)
	}
	_, e := f.control.UpdateWorkspace(ctx, &ax.UpdateWorkspaceRequest{Workspace: &ax.Workspace{ApiVersion: "ax.io/v1alpha1", Kind: "Workspace", Metadata: &ax.ObjectMeta{Name: ownedName, Atespace: "test"}, Spec: &ax.WorkspaceSpec{}}})
	if e != nil {
		t.Fatal(e)
	}
	f.upstream.SetMode("create-swapped-image")
	if _, e = f.control.CreateTask(ctx, &ax.CreateTaskRequest{Task: taskRequest()}); status.Code(e) != codes.DataLoss {
		t.Fatal("different allowed image substituted for requested build", e)
	}
}
func TestGatewayRejectsDuplicateTaskInventory(t *testing.T) {
	f := newGatewayTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	f.seed(t, ctx)
	f.upstream.SetMode("list-duplicate")
	if _, e := f.control.ListTasks(ctx, &ax.ListTasksRequest{Atespace: "test"}); status.Code(e) != codes.DataLoss {
		t.Fatal("ambiguous inventory accepted", e)
	}
}

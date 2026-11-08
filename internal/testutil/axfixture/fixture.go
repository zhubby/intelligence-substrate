// Package axfixture serves the pinned upstream protocols over actual mutual
// TLS for tests and isolated drills. It is not an AX or Substrate deployment.
package axfixture

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	guest "github.com/agent-substrate/env/proto/ateenv/v1alpha"
	ax "github.com/google/ax/pkg/apis/v1alpha1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type Call struct {
	Operation, Space, Name, Actor, ClientSHA256 string
	ForwardedCredential                         bool
}

type Fixture struct {
	ax.UnimplementedAXServer
	guest.UnimplementedProcessServiceServer
	mu                   sync.Mutex
	mode                 string
	root                 string
	Control, Guest       map[string]any
	ControlTLS, GuestTLS *tls.Config
	BeforeMutation       func(context.Context, string, string) error
	servers              []*grpc.Server
	tasks                map[string]*ax.Task
	workspaces           map[string]*ax.Workspace
	processes            map[string]*processResult
	calls                []Call
}
type processResult struct {
	actor  string
	output []byte
	exit   int32
}

func New(root string) (*Fixture, error) {
	f := &Fixture{root: root, tasks: map[string]*ax.Task{}, workspaces: map[string]*ax.Workspace{}, processes: map[string]*processResult{}}
	if e := os.MkdirAll(filepath.Join(root, "workspace"), 0700); e != nil {
		return nil, e
	}
	for _, role := range []string{"control", "guest"} {
		config, serverTLS, clientTLS, e := materials(root, role)
		if e != nil {
			f.Close()
			return nil, e
		}
		listener, e := net.Listen("tcp", "127.0.0.1:0")
		if e != nil {
			f.Close()
			return nil, e
		}
		config["endpoint"] = listener.Addr().String()
		s := grpc.NewServer(grpc.Creds(credentials.NewTLS(serverTLS)))
		if role == "control" {
			ax.RegisterAXServer(s, f)
			f.Control, f.ControlTLS = config, clientTLS
		} else {
			guest.RegisterProcessServiceServer(s, f)
			f.Guest, f.GuestTLS = config, clientTLS
		}
		f.servers = append(f.servers, s)
		go s.Serve(listener)
	}
	return f, nil
}
func (f *Fixture) Close() {
	for _, s := range f.servers {
		s.Stop()
	}
}
func (f *Fixture) SetMode(mode string) { f.mu.Lock(); defer f.mu.Unlock(); f.mode = mode }
func (f *Fixture) Calls() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Call{}, f.calls...)
}
func (f *Fixture) Counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.tasks), len(f.workspaces)
}
func (f *Fixture) Config() map[string]any {
	copyMap := func(in map[string]any) map[string]any {
		out := map[string]any{}
		for k, v := range in {
			out[k] = v
		}
		return out
	}
	return map[string]any{"namespace": "ax-system", "atespace": "test", "context": "test-mtls", "image": "runner@sha256:" + strings.Repeat("a", 64), "secure": map[string]any{"control": copyMap(f.Control), "guest": copyMap(f.Guest)}}
}
func (f *Fixture) record(ctx context.Context, op, space, name string) error {
	c := Call{Operation: op, Space: space, Name: name}
	if incoming, ok := metadata.FromIncomingContext(ctx); ok {
		c.Actor = strings.Join(incoming.Get("ate-target-actor"), ",")
		c.ForwardedCredential = len(incoming.Get("authorization")) > 0 || len(incoming.Get("cookie")) > 0 || len(incoming.Get("x-is-lease")) > 0
	}
	if p, ok := peer.FromContext(ctx); ok {
		if tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo); ok && len(tlsInfo.State.PeerCertificates) > 0 {
			sum := sha256.Sum256(tlsInfo.State.PeerCertificates[0].Raw)
			c.ClientSHA256 = hex.EncodeToString(sum[:])
		}
	}
	f.calls = append(f.calls, c)
	if c.ClientSHA256 == "" {
		return status.Error(codes.Unauthenticated, "no client identity")
	}
	if space != "" && space != "test" {
		return status.Error(codes.PermissionDenied, "unexpected atespace")
	}
	if f.mode == "revoke-client" {
		return status.Error(codes.PermissionDenied, "test client revoked")
	}
	return nil
}
func (f *Fixture) UpdateWorkspace(ctx context.Context, r *ax.UpdateWorkspaceRequest) (*ax.Workspace, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w := r.GetWorkspace()
	if f.BeforeMutation != nil {
		if e := f.BeforeMutation(ctx, w.GetMetadata().GetAtespace(), w.GetMetadata().GetName()); e != nil {
			return nil, e
		}
	}
	if e := f.record(ctx, "workspace.update", w.GetMetadata().GetAtespace(), w.GetMetadata().GetName()); e != nil {
		return nil, e
	}
	f.workspaces[w.GetMetadata().GetName()] = proto.Clone(w).(*ax.Workspace)
	return proto.Clone(w).(*ax.Workspace), nil
}
func (f *Fixture) CreateTask(ctx context.Context, r *ax.CreateTaskRequest) (*ax.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := proto.Clone(r.GetTask()).(*ax.Task)
	if f.BeforeMutation != nil {
		if e := f.BeforeMutation(ctx, t.GetMetadata().GetAtespace(), t.GetMetadata().GetName()); e != nil {
			return nil, e
		}
	}
	if e := f.record(ctx, "task.create", t.GetMetadata().GetAtespace(), t.GetMetadata().GetName()); e != nil {
		return nil, e
	}
	if f.mode == "partial" {
		return nil, status.Error(codes.Unavailable, "test failure after workspace creation")
	}
	if _, ok := f.tasks[t.Metadata.Name]; ok {
		return nil, status.Error(codes.AlreadyExists, "task exists")
	}
	if f.workspaces[t.GetSpec().GetWorkspace().GetName()] == nil {
		return nil, status.Error(codes.FailedPrecondition, "missing exact workspace")
	}
	t.Status = &ax.TaskStatus{Phase: "Running", Actor: t.Metadata.Name, WorkerIp: "127.0.0.1:1"}
	f.tasks[t.Metadata.Name] = t
	result := proto.Clone(t).(*ax.Task)
	if f.mode == "create-swapped-image" {
		result.Spec.Image = "runner@sha256:" + strings.Repeat("b", 64)
	}
	return result, nil
}
func (f *Fixture) GetTask(ctx context.Context, r *ax.GetTaskRequest) (*ax.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e := f.record(ctx, "task.get", r.Atespace, r.Name); e != nil {
		return nil, e
	}
	t := f.tasks[r.Name]
	if t == nil {
		return nil, status.Error(codes.NotFound, "task absent")
	}
	t = proto.Clone(t).(*ax.Task)
	if f.mode == "wrong-task" {
		t.Metadata.Name = "other"
	}
	if f.mode == "wrong-space" {
		t.Metadata.Atespace = "other"
	}
	if f.mode == "unsafe-actor" {
		t.Status.Actor = "other/actor"
	}
	if f.mode == "other-actor" {
		t.Status.Actor = "unrelated-actor"
	}
	if f.mode == "not-debug" {
		t.Spec.Debug = false
	}
	return t, nil
}
func (f *Fixture) ResumeTask(ctx context.Context, r *ax.ResumeTaskRequest) (*ax.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e := f.record(ctx, "task.resume", r.Atespace, r.Name); e != nil {
		return nil, e
	}
	if t := f.tasks[r.Name]; t != nil {
		return proto.Clone(t).(*ax.Task), nil
	}
	return nil, status.Error(codes.NotFound, "task absent")
}
func (f *Fixture) SuspendTask(ctx context.Context, r *ax.SuspendTaskRequest) (*ax.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e := f.record(ctx, "task.suspend", r.Atespace, r.Name); e != nil {
		return nil, e
	}
	if t := f.tasks[r.Name]; t != nil {
		return proto.Clone(t).(*ax.Task), nil
	}
	return nil, status.Error(codes.NotFound, "task absent")
}
func (f *Fixture) DeleteTask(ctx context.Context, r *ax.DeleteTaskRequest) (*ax.DeleteTaskResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e := f.record(ctx, "task.delete", r.Atespace, r.Name); e != nil {
		return nil, e
	}
	if f.mode == "cleanup-denied" {
		return nil, status.Error(codes.PermissionDenied, "test task deletion denied")
	}
	if f.tasks[r.Name] == nil {
		return nil, status.Error(codes.NotFound, "task absent")
	}
	if f.mode != "retained" && f.mode != "hidden-retained" && f.mode != "point-read-retained" {
		delete(f.tasks, r.Name)
	}
	return &ax.DeleteTaskResponse{}, nil
}
func (f *Fixture) DeleteWorkspace(ctx context.Context, r *ax.DeleteWorkspaceRequest) (*ax.DeleteWorkspaceResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e := f.record(ctx, "workspace.delete", r.Atespace, r.Name); e != nil {
		return nil, e
	}
	delete(f.workspaces, r.Name)
	return &ax.DeleteWorkspaceResponse{}, nil
}
func (f *Fixture) GetWorkspace(ctx context.Context, r *ax.GetWorkspaceRequest) (*ax.Workspace, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e := f.record(ctx, "workspace.get", r.Atespace, r.Name); e != nil {
		return nil, e
	}
	w := f.workspaces[r.Name]
	if w == nil {
		return nil, status.Error(codes.NotFound, "workspace absent")
	}
	return proto.Clone(w).(*ax.Workspace), nil
}
func (f *Fixture) ListTasks(ctx context.Context, r *ax.ListTasksRequest) (*ax.ListTasksResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e := f.record(ctx, "tasks.list", r.Atespace, ""); e != nil {
		return nil, e
	}
	if f.mode == "list-error" {
		return nil, status.Error(codes.Unavailable, "test list unavailable")
	}
	res := &ax.ListTasksResponse{}
	if f.mode == "hidden-retained" || f.mode == "page-error" {
		if f.mode == "page-error" && r.Offset > 0 {
			return nil, status.Error(codes.Unavailable, "second page unavailable")
		}
		for i := 0; i < 101; i++ {
			res.Tasks = append(res.Tasks, &ax.Task{Metadata: &ax.ObjectMeta{Name: fmt.Sprintf("is-sandbox-%024x", i+1), Atespace: r.Atespace}})
		}
	}
	for _, t := range f.tasks {
		res.Tasks = append(res.Tasks, proto.Clone(t).(*ax.Task))
	}
	if f.mode == "point-read-retained" {
		res.Tasks = nil
	}
	if f.mode == "list-wrong-scope" {
		res.Tasks = append(res.Tasks, &ax.Task{Metadata: &ax.ObjectMeta{Name: "unrelated", Atespace: "other"}})
	}
	if f.mode == "list-duplicate" && len(res.Tasks) > 0 {
		res.Tasks = append(res.Tasks, proto.Clone(res.Tasks[0]).(*ax.Task))
	}
	// Match the pinned AX server's default50-item pagination. A resource past
	// that boundary is not absent merely because the default page omitted it.
	limit := r.Limit
	if limit <= 0 {
		limit = 50
	}
	start := r.Offset
	if start < 0 {
		start = 0
	}
	if start > int64(len(res.Tasks)) {
		start = int64(len(res.Tasks))
	}
	end := start + limit
	if end > int64(len(res.Tasks)) {
		end = int64(len(res.Tasks))
	}
	res.Tasks = res.Tasks[start:end]
	return res, nil
}
func (f *Fixture) ListWorkspaces(ctx context.Context, r *ax.ListWorkspacesRequest) (*ax.ListWorkspacesResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e := f.record(ctx, "workspaces.list", r.Atespace, ""); e != nil {
		return nil, e
	}
	res := &ax.ListWorkspacesResponse{}
	for _, w := range f.workspaces {
		res.Workspaces = append(res.Workspaces, proto.Clone(w).(*ax.Workspace))
	}
	return res, nil
}
func (f *Fixture) StartProcess(ctx context.Context, r *guest.StartProcessRequest) (*guest.Process, error) {
	f.mu.Lock()
	if e := f.record(ctx, "process.start", "", ""); e != nil {
		f.mu.Unlock()
		return nil, e
	}
	actor := f.calls[len(f.calls)-1].Actor
	mode := f.mode
	valid := false
	for _, t := range f.tasks {
		if actor == "test/"+t.Status.Actor {
			valid = true
		}
	}
	f.mu.Unlock()
	if !valid {
		return nil, status.Error(codes.PermissionDenied, "unexpected actor")
	}
	if mode == "stall" {
		<-ctx.Done()
		return nil, status.FromContextError(ctx.Err()).Err()
	}
	if mode == "start-invalid-exit" {
		return &guest.Process{ProcessId: "invalid", State: guest.ProcessState_PROCESS_STATE_EXITED, ExitCode: -1}, nil
	}
	if mode == "missing-process" {
		return &guest.Process{}, nil
	}
	args := append([]string{}, r.Command...)
	if len(args) == 0 {
		return nil, status.Error(codes.InvalidArgument, "empty command")
	}
	for i := range args {
		args[i] = strings.ReplaceAll(args[i], "/workspace", filepath.Join(f.root, "workspace"))
	}
	// This provider runs on macOS as well as Linux; the guest's Linux readiness
	// binary lives in /usr/bin on macOS. This is a local protocol fixture only.
	if args[0] == "/bin/true" {
		if _, e := os.Stat(args[0]); os.IsNotExist(e) {
			args[0] = "/usr/bin/true"
		}
	}
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = filepath.Join(f.root, "workspace")
	cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=" + f.root}
	output, e := cmd.CombinedOutput()
	exit := int32(0)
	if e != nil {
		if ee, ok := e.(*exec.ExitError); ok {
			exit = int32(ee.ExitCode())
		} else {
			return nil, status.Error(codes.Internal, "local test process failed")
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	id := fmt.Sprintf("process-%d", len(f.processes)+1)
	f.processes[id] = &processResult{actor: actor, output: output, exit: exit}
	return &guest.Process{ProcessId: id, State: guest.ProcessState_PROCESS_STATE_RUNNING}, nil
}
func (f *Fixture) StreamProcessOutput(r *guest.StreamProcessOutputRequest, s grpc.ServerStreamingServer[guest.ProcessOutput]) error {
	f.mu.Lock()
	if e := f.record(s.Context(), "process.stream", "", r.ProcessId); e != nil {
		f.mu.Unlock()
		return e
	}
	result := f.processes[r.ProcessId]
	actor := f.calls[len(f.calls)-1].Actor
	mode := f.mode
	f.mu.Unlock()
	if result == nil || actor != result.actor || !r.Follow {
		return status.Error(codes.PermissionDenied, "unexpected process binding")
	}
	if len(result.output) > 0 {
		if e := s.Send(&guest.ProcessOutput{Output: &guest.ProcessOutput_Stdout{Stdout: result.output}}); e != nil {
			return e
		}
	}
	if mode == "missing-exit" {
		return nil
	}
	if mode == "stream-stall" {
		<-s.Context().Done()
		return status.FromContextError(s.Context().Err()).Err()
	}
	if mode == "stream-error" {
		return status.Error(codes.Unavailable, "test stream interrupted")
	}
	exit := &guest.Process{ProcessId: r.ProcessId, State: guest.ProcessState_PROCESS_STATE_EXITED, ExitCode: result.exit}
	if mode == "wrong-process" {
		exit.ProcessId = "unrelated"
	}
	if mode == "running-exit" {
		exit.State = guest.ProcessState_PROCESS_STATE_RUNNING
	}
	if mode == "invalid-exit" {
		exit.ExitCode = -1
	}
	return s.Send(&guest.ProcessOutput{Output: &guest.ProcessOutput_Exit{Exit: exit}})
}

func materials(root, role string) (map[string]any, *tls.Config, *tls.Config, error) {
	key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		return nil, nil, nil, e
	}
	now := time.Now()
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: role + " local test CA"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	caDER, e := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if e != nil {
		return nil, nil, nil, e
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(caPEM)
	leaf := func(cn string, client bool) (tls.Certificate, []byte, []byte, error) {
		k, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if e != nil {
			return tls.Certificate{}, nil, nil, e
		}
		t := &x509.Certificate{SerialNumber: big.NewInt(int64(len(cn) + 2)), Subject: pkix.Name{CommonName: cn}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature}
		if client {
			t.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
		} else {
			t.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
			t.DNSNames = []string{cn}
		}
		d, e := x509.CreateCertificate(rand.Reader, t, ca, &k.PublicKey, key)
		if e != nil {
			return tls.Certificate{}, nil, nil, e
		}
		kb, e := x509.MarshalPKCS8PrivateKey(k)
		if e != nil {
			return tls.Certificate{}, nil, nil, e
		}
		cp, kp := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: d}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kb})
		pair, e := tls.X509KeyPair(cp, kp)
		return pair, cp, kp, e
	}
	name := role + ".ax.test"
	server, _, _, e := leaf(name, false)
	if e != nil {
		return nil, nil, nil, e
	}
	client, certPEM, keyPEM, e := leaf(role+"-worker", true)
	if e != nil {
		return nil, nil, nil, e
	}
	paths := map[string]string{"caFile": role + "-ca.pem", "clientCertFile": role + "-client.pem", "clientKeyFile": role + "-key.pem"}
	contents := map[string][]byte{"caFile": caPEM, "clientCertFile": certPEM, "clientKeyFile": keyPEM}
	config := map[string]any{"serverName": name}
	for k, n := range paths {
		p := filepath.Join(root, n)
		if e = os.WriteFile(p, contents[k], 0600); e != nil {
			return nil, nil, nil, e
		}
		config[k] = p
	}
	cert, e := x509.ParseCertificate(server.Certificate[0])
	if e != nil {
		return nil, nil, nil, e
	}
	spki := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	config["serverSPKI"] = hex.EncodeToString(spki[:])
	serverTLS := &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{server}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: pool, NextProtos: []string{"h2"}}
	clientTLS := &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{client}, RootCAs: pool, ServerName: name, NextProtos: []string{"h2"}}
	return config, serverTLS, clientTLS, nil
}

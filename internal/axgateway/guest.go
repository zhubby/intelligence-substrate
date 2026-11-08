package axgateway

import (
	"context"
	"io"
	"strings"
	"time"

	guest "github.com/agent-substrate/env/proto/ateenv/v1alpha"
	ax "github.com/google/ax/pkg/apis/v1alpha1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
)

type guestProxy struct {
	guest.UnimplementedProcessServiceServer
	g *Gateway
}

func (c *guestProxy) admit(ctx context.Context, method string, request proto.Message, valid bool) (context.Context, func(), error) {
	incoming, _ := metadata.FromIncomingContext(ctx)
	routes := incoming.Get("ate-target-actor")
	space, name := "", ""
	if len(routes) == 1 {
		parts := strings.Split(routes[0], "/")
		if len(parts) == 2 {
			space, name = parts[0], parts[1]
		}
	}
	live, p, release, e := c.g.authorize(ctx, "guest", method, space, name, request, valid && len(routes) == 1 && namePattern.MatchString(name))
	if e != nil {
		return ctx, release, e
	}
	task, e := c.g.control.GetTask(live, &ax.GetTaskRequest{Atespace: space, Name: name})
	if e != nil {
		release()
		return ctx, func() {}, upstreamError(e)
	}
	if !knownMessage(task) || !metadataMatches(task.GetMetadata(), p.Atespace, name) || !taskSpecAllowed(task, p) || task.GetStatus().GetPhase() != "Running" || task.GetStatus().GetActor() != name {
		release()
		return ctx, func() {}, invalidResponse()
	}
	live = metadata.NewOutgoingContext(live, metadata.Pairs("ate-target-actor", space+"/"+name))
	return live, release, nil
}
func (c *guestProxy) StartProcess(ctx context.Context, r *guest.StartProcessRequest) (*guest.Process, error) {
	valid := r != nil && len(r.GetCommand()) > 0 && len(r.GetCommand()) <= 1024 && r.GetCommand()[0] != "" && r.GetCwd() == "" && len(r.GetEnv()) == 0 && !r.GetStdin()
	for _, arg := range r.GetCommand() {
		if strings.ContainsRune(arg, 0) {
			valid = false
		}
	}
	if timeout := r.GetTimeout(); timeout != nil && (timeout.CheckValid() != nil || timeout.AsDuration() < 0 || timeout.AsDuration() > 30*time.Minute) {
		valid = false
	}
	live, release, e := c.admit(ctx, "process.start", r, valid)
	if e != nil {
		return nil, e
	}
	defer release()
	query := proto.Clone(r).(*guest.StartProcessRequest)
	until, _ := live.Deadline()
	remaining := time.Until(until)
	if query.Timeout == nil || query.Timeout.AsDuration() == 0 || query.Timeout.AsDuration() > remaining {
		query.Timeout = durationpb.New(remaining)
	}
	result, e := c.g.guest.StartProcess(live, query)
	if e != nil {
		return nil, upstreamError(e)
	}
	if !knownMessage(result) || result.GetProcessId() == "" || len(result.ProcessId) > 1024 || result.State != guest.ProcessState_PROCESS_STATE_RUNNING && result.State != guest.ProcessState_PROCESS_STATE_EXITED {
		return nil, invalidResponse()
	}
	if result.State == guest.ProcessState_PROCESS_STATE_EXITED && (result.ExitCode < 0 || result.ExitCode > 255) {
		return nil, invalidResponse()
	}
	if live.Err() != nil {
		return nil, status.FromContextError(live.Err()).Err()
	}
	return result, nil
}
func (c *guestProxy) StreamProcessOutput(r *guest.StreamProcessOutputRequest, stream grpc.ServerStreamingServer[guest.ProcessOutput]) error {
	live, release, e := c.admit(stream.Context(), "process.stream", r, r.GetProcessId() != "" && len(r.GetProcessId()) <= 1024 && r.GetFollow() && r.GetStdoutOffset() == 0 && r.GetStderrOffset() == 0)
	if e != nil {
		return e
	}
	defer release()
	upstream, e := c.g.guest.StreamProcessOutput(live, r)
	if e != nil {
		return upstreamError(e)
	}
	for {
		item, e := upstream.Recv()
		if e == io.EOF {
			return invalidResponse()
		}
		if e != nil {
			return upstreamError(e)
		}
		if live.Err() != nil {
			return status.FromContextError(live.Err()).Err()
		}
		if !knownMessage(item) || item.GetOutput() == nil || proto.Size(item) > 1<<20 {
			return invalidResponse()
		}
		if result := item.GetExit(); result != nil {
			if result.GetProcessId() != r.GetProcessId() || result.GetState() != guest.ProcessState_PROCESS_STATE_EXITED || result.ExitCode < 0 || result.ExitCode > 255 {
				return invalidResponse()
			}
			if e = stream.Send(item); e != nil {
				return e
			}
			return nil
		}
		if e = stream.Send(item); e != nil {
			return e
		}
	}
}

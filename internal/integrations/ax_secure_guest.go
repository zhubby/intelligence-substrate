package integrations

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	guest "github.com/agent-substrate/env/proto/ateenv/v1alpha"
	ax "github.com/google/ax/pkg/apis/v1alpha1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
)

func stringsReplaceID(id string) string { return strings.ReplaceAll(strings.ToLower(id), "_", "-") }
func publicRPCCode(e error) string      { return status.Code(e).String() }

func (a AX) execSecure(ctx context.Context, name string, command []string, output io.Writer) (int, error) {
	if len(command) == 0 || command[0] == "" || output == nil {
		return 1, fmt.Errorf("AX guest requires an explicit command and output")
	}
	s, close, e := a.secureControl(ctx)
	if e != nil {
		return 1, e
	}
	defer close()
	task, e := s.control.GetTask(ctx, &ax.GetTaskRequest{Name: name, Atespace: a.Atespace})
	if e != nil {
		return 1, fmt.Errorf("AX guest task read failed: %s", publicRPCCode(e))
	}
	if e = a.exactTask(task, name); e != nil {
		return 1, e
	}
	actor := task.GetStatus().GetActor()
	if task.GetStatus().GetPhase() != "Running" || !task.GetSpec().GetDebug() || actor != name || !regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,251}[a-z0-9]$|^[a-z0-9]$`).MatchString(actor) {
		return 1, fmt.Errorf("AX guest requires an exact running debug task and valid Actor")
	}
	// WorkerIP and all automatic/plaintext/kube routes are intentionally absent.
	ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("ate-target-actor", a.Atespace+"/"+actor))
	request := &guest.StartProcessRequest{Command: command}
	if until, ok := ctx.Deadline(); ok {
		remaining := time.Until(until)
		if remaining <= 0 {
			return 1, ctx.Err()
		}
		request.Timeout = durationpb.New(remaining)
	}
	process, e := s.guest.StartProcess(ctx, request)
	if e != nil {
		return 1, fmt.Errorf("AX guest process start unconfirmed: %s", publicRPCCode(e))
	}
	id := process.GetProcessId()
	if len(id) == 0 || len(id) > 1024 {
		return 1, fmt.Errorf("AX guest process identity missing or invalid")
	}
	stream, e := s.guest.StreamProcessOutput(ctx, &guest.StreamProcessOutputRequest{ProcessId: id, Follow: true})
	if e != nil {
		return 1, fmt.Errorf("AX guest output unavailable: %s", publicRPCCode(e))
	}
	for {
		out, e := stream.Recv()
		if e != nil {
			return 1, fmt.Errorf("AX guest output ended without a confirmed exit: %s", publicRPCCode(e))
		}
		if exit := out.GetExit(); exit != nil {
			if exit.GetProcessId() != id || exit.GetState() != guest.ProcessState_PROCESS_STATE_EXITED || exit.GetExitCode() < 0 || exit.GetExitCode() > 255 {
				return 1, fmt.Errorf("AX guest exit does not bind the completed process")
			}
			return int(exit.GetExitCode()), nil
		}
		data := out.GetStdout()
		if len(data) == 0 {
			data = out.GetStderr()
		}
		if len(data) > 0 {
			n, e := output.Write(data)
			if e != nil || n != len(data) {
				return 1, fmt.Errorf("AX guest output could not be retained")
			}
		}
	}
}

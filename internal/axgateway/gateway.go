package axgateway

import (
	"context"
	"time"

	guest "github.com/agent-substrate/env/proto/ateenv/v1alpha"
	ax "github.com/google/ax/pkg/apis/v1alpha1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

type Gateway struct {
	gate    *Gate
	control ax.AXClient
	guest   guest.ProcessServiceClient
}

func NewGateway(gate *Gate, control ax.AXClient, guestClient guest.ProcessServiceClient) *Gateway {
	return &Gateway{gate: gate, control: control, guest: guestClient}
}
func (g *Gateway) ControlServer() ax.AXServer              { return &controlProxy{g: g} }
func (g *Gateway) GuestServer() guest.ProcessServiceServer { return &guestProxy{g: g} }

func knownMessage(message proto.Message) bool {
	if message == nil || !message.ProtoReflect().IsValid() {
		return false
	}
	var visit func(protoreflect.Message) bool
	visit = func(m protoreflect.Message) bool {
		if len(m.GetUnknown()) != 0 {
			return false
		}
		valid := true
		m.Range(func(field protoreflect.FieldDescriptor, value protoreflect.Value) bool {
			if field.IsMap() {
				if field.MapValue().Kind() == protoreflect.MessageKind {
					value.Map().Range(func(_ protoreflect.MapKey, v protoreflect.Value) bool { valid = visit(v.Message()); return valid })
				}
			}
			if field.IsList() && field.Kind() == protoreflect.MessageKind {
				for i := 0; i < value.List().Len(); i++ {
					if !visit(value.List().Get(i).Message()) {
						valid = false
						break
					}
				}
			}
			if !field.IsMap() && !field.IsList() && field.Kind() == protoreflect.MessageKind {
				valid = visit(value.Message())
			}
			return valid
		})
		return valid
	}
	return visit(message.ProtoReflect())
}

func (g *Gateway) authorize(ctx context.Context, role, method, space, name string, request proto.Message, valid bool, checks ...func(Principal) bool) (context.Context, Principal, func(), error) {
	if !knownMessage(request) || proto.Size(request) > 1<<20 {
		valid = false
	}
	if role == "control" {
		incoming, _ := metadata.FromIncomingContext(ctx)
		if len(incoming.Get("ate-target-actor")) > 0 {
			valid = false
		}
	}
	if !valid {
		role = ""
	}
	live, principal, release, e := g.gate.admit(ctx, role, method, space, name, checks)
	if e != nil {
		return ctx, principal, release, e
	}
	limit := 30 * time.Second
	if role == "guest" {
		limit = 30 * time.Minute
	}
	live, cancel := context.WithTimeout(live, limit)
	// User credentials and routing headers never become upstream authority.
	live = metadata.NewOutgoingContext(live, metadata.MD{})
	return live, principal, func() { cancel(); release() }, nil
}
func upstreamError(e error) error {
	if e == nil {
		return nil
	}
	return status.Error(status.Code(e), "upstream operation unconfirmed")
}
func invalidResponse() error {
	return status.Error(codes.DataLoss, "upstream response does not bind the authorized resource")
}
func permittedImage(p Principal, image string) bool {
	for _, allowed := range p.Images {
		if image == allowed {
			return true
		}
	}
	return false
}
func metadataMatches(m *ax.ObjectMeta, space, name string) bool {
	return m != nil && m.Atespace == space && namePattern.MatchString(m.Name) && (name == "" || m.Name == name)
}
func taskSpecAllowed(task *ax.Task, p Principal) bool {
	spec := task.GetSpec()
	limits := spec.GetResources().GetLimits()
	return spec != nil && permittedImage(p, spec.Image) && len(spec.Command) == 1 && spec.Command[0] == "/bin/true" && spec.Debug && len(spec.Env) == 0 && spec.Config == nil && spec.GetWorkspace().GetName() == task.GetMetadata().GetName() && spec.GetWorkspace().GetPath() == "/workspace" && spec.GetResources() != nil && spec.GetResources().Requests == nil && limits.GetCpu() == "2" && limits.GetMemory() == "4Gi"
}

func controlCall[T proto.Message](g *Gateway, ctx context.Context, method, space, name string, request proto.Message, valid bool, call func(context.Context) (T, error), check func(T, Principal) bool, requestChecks ...func(Principal) bool) (T, error) {
	var zero T
	live, p, release, e := g.authorize(ctx, "control", method, space, name, request, valid, requestChecks...)
	if e != nil {
		return zero, e
	}
	defer release()
	response, e := call(live)
	if e != nil {
		return zero, upstreamError(e)
	}
	if !knownMessage(response) || !check(response, p) {
		return zero, invalidResponse()
	}
	if live.Err() != nil {
		return zero, status.FromContextError(live.Err()).Err()
	}
	return response, nil
}

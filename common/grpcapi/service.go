package grpcapi

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

// Unary is the handler shape for a unary RPC.
//
// It takes and returns typed values rather than a *gin.Context because a gRPC
// call has no URL, no query string and no HTTP status. Returning an error is how
// a status code is produced: see the helpers in errors.go, or return a
// status.Error directly for full control.
type Unary[In, Out any] func(ctx context.Context, in *In) (*Out, error)

// ServiceNamer lets a controller declare the service name it implements.
//
// It is an interface rather than a struct tag because the name is a published
// part of the contract — clients address `package.ServiceName/Method` — and
// deriving it from the Go type name would make an ordinary rename a breaking API
// change nobody noticed.
type ServiceNamer interface {
	GRPCService() string
}

// methodDef is one RPC discovered on a controller.
type methodDef struct {
	name        string
	serviceName string
	handler     reflect.Value

	inType  reflect.Type
	outType reflect.Type

	inBinding  *messageBinding
	outBinding *messageBinding
}

// serviceDef is one gRPC service discovered on a controller.
type serviceDef struct {
	name    string
	methods []*methodDef
}

// registerController scans a controller for grpc-tagged handler fields.
func (s *Server) registerController(controller any) error {
	if controller == nil {
		return nil
	}

	value := reflect.ValueOf(controller)
	if value.Kind() == reflect.Ptr {
		if value.IsNil() {
			return errors.New("grpcapi: cannot register a nil controller pointer")
		}
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return nil
	}
	structType := value.Type()

	// Collect the tagged fields first, so a controller with none is skipped before
	// its service name is even required.
	type candidate struct {
		method string
		field  reflect.Value
		name   string
	}
	var candidates []candidate

	for i := 0; i < structType.NumField(); i++ {
		field := structType.Field(i)

		tag := field.Tag.Get("grpc")
		if tag == "" || tag == "-" {
			continue
		}
		if !field.IsExported() {
			return fmt.Errorf("grpcapi: %s.%s has a grpc tag but is unexported", structType.Name(), field.Name)
		}
		candidates = append(candidates, candidate{method: tag, field: value.Field(i), name: field.Name})
	}

	if len(candidates) == 0 {
		return nil
	}

	namer, ok := controller.(ServiceNamer)
	if !ok {
		return fmt.Errorf(
			"grpcapi: %s declares grpc methods but no service name; add:\n\n\tfunc (c *%s) GRPCService() string { return \"%sService\" }",
			structType.Name(), structType.Name(), strings.TrimSuffix(structType.Name(), "Controller"),
		)
	}

	serviceName := strings.TrimSpace(namer.GRPCService())
	if err := validateIdentifier(serviceName, "service name"); err != nil {
		return fmt.Errorf("grpcapi: %s: %w", structType.Name(), err)
	}

	service, exists := s.services[serviceName]
	if !exists {
		service = &serviceDef{name: serviceName}
		s.services[serviceName] = service
	}

	for _, c := range candidates {
		method, err := newMethodDef(c.method, c.field)
		if err != nil {
			return fmt.Errorf("grpcapi: %s.%s: %w", structType.Name(), c.name, err)
		}
		for _, existing := range service.methods {
			if existing.name == method.name {
				return fmt.Errorf("grpcapi: %s.%s is declared twice", serviceName, method.name)
			}
		}
		service.methods = append(service.methods, method)
	}

	// Deterministic method order in the descriptor.
	sort.Slice(service.methods, func(i, j int) bool {
		return service.methods[i].name < service.methods[j].name
	})

	return nil
}

// newMethodDef validates a handler field and extracts its request and response
// types.
func newMethodDef(name string, field reflect.Value) (*methodDef, error) {
	if err := validateIdentifier(name, "method name"); err != nil {
		return nil, err
	}

	fieldType := field.Type()
	if fieldType.Kind() != reflect.Func {
		return nil, fmt.Errorf("a grpc field must be a grpcapi.Unary[In, Out], got %s", fieldType)
	}
	if field.IsNil() {
		return nil, errors.New("handler is nil — assign it in the controller constructor")
	}

	// Unary[In, Out] is a named func type, so validate the shape structurally:
	// func(context.Context, *In) (*Out, error).
	if fieldType.NumIn() != 2 || fieldType.NumOut() != 2 {
		return nil, fmt.Errorf("a grpc handler must be func(context.Context, *In) (*Out, error), got %s", fieldType)
	}
	if fieldType.In(0) != reflect.TypeOf((*context.Context)(nil)).Elem() {
		return nil, fmt.Errorf("the first parameter must be context.Context, got %s", fieldType.In(0))
	}
	if fieldType.Out(1) != reflect.TypeOf((*error)(nil)).Elem() {
		return nil, fmt.Errorf("the second result must be error, got %s", fieldType.Out(1))
	}

	inPtr, outPtr := fieldType.In(1), fieldType.Out(0)
	if inPtr.Kind() != reflect.Ptr || outPtr.Kind() != reflect.Ptr {
		return nil, fmt.Errorf("the request and response must be pointers to structs, got %s and %s", inPtr, outPtr)
	}

	return &methodDef{
		name:    name,
		handler: field,
		inType:  inPtr.Elem(),
		outType: outPtr.Elem(),
	}, nil
}

// build derives the descriptors for this service's messages and methods.
func (d *serviceDef) build(builder *schemaBuilder) (*descriptorpb.ServiceDescriptorProto, error) {
	proto := &descriptorpb.ServiceDescriptorProto{Name: strPtr(d.name)}

	for _, method := range d.methods {
		method.serviceName = d.name

		in, err := builder.message(method.inType)
		if err != nil {
			return nil, fmt.Errorf("grpcapi: %s.%s request: %w", d.name, method.name, err)
		}
		out, err := builder.message(method.outType)
		if err != nil {
			return nil, fmt.Errorf("grpcapi: %s.%s response: %w", d.name, method.name, err)
		}

		method.inBinding, method.outBinding = in, out
		proto.Method = append(proto.Method, &descriptorpb.MethodDescriptorProto{
			Name:       strPtr(method.name),
			InputType:  strPtr("." + in.name),
			OutputType: strPtr("." + out.name),
		})
	}

	return proto, nil
}

// grpcServiceDesc builds the grpc.ServiceDesc that registers this service.
//
// This is the same struct protoc-gen-go-grpc emits; writing it by hand is what
// removes the codegen step without changing anything a client can observe.
func (d *serviceDef) grpcServiceDesc(cfg Config, descriptor protoreflect.ServiceDescriptor) *grpc.ServiceDesc {
	desc := &grpc.ServiceDesc{
		ServiceName: cfg.Package + "." + d.name,
		HandlerType: (*any)(nil),
		Metadata:    fileNameFor(cfg.Package),
	}

	for _, method := range d.methods {
		desc.Methods = append(desc.Methods, grpc.MethodDesc{
			MethodName: method.name,
			Handler:    method.grpcHandler(cfg),
		})
	}

	return desc
}

// grpcHandler adapts a typed Go handler to gRPC's handler signature.
func (m *methodDef) grpcHandler(cfg Config) func(any, context.Context, func(any) error, grpc.UnaryServerInterceptor) (any, error) {
	return func(_ any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
		// A dynamic message satisfies proto.Message, so the *standard* protobuf
		// codec decodes into it. That is what makes this a real gRPC service: the
		// bytes on the wire are ordinary protobuf, not an envelope in disguise.
		request := dynamicpb.NewMessage(m.inBinding.descriptor)
		if err := dec(request); err != nil {
			return nil, err
		}

		invoke := func(ctx context.Context, req any) (any, error) {
			message, ok := req.(proto.Message)
			if !ok {
				return nil, status.Error(codes.Internal, "unexpected request type")
			}

			in := reflect.New(m.inType)
			if err := fromDynamic(m.inBinding, message.ProtoReflect(), in.Elem()); err != nil {
				return nil, status.Errorf(codes.InvalidArgument, "cannot decode request: %v", err)
			}

			if cfg.Validate != nil {
				if err := cfg.Validate(in.Interface()); err != nil {
					// A validation failure is the caller's fault, so it is
					// InvalidArgument rather than Internal — a client branches on
					// that to decide whether retrying could ever help.
					return nil, toStatus(err, codes.InvalidArgument)
				}
			}

			results := m.handler.Call([]reflect.Value{reflect.ValueOf(ctx), in})
			if errValue := results[1]; !errValue.IsNil() {
				return nil, toStatus(errValue.Interface().(error), codes.Internal)
			}

			out := results[0]
			if out.IsNil() {
				return nil, status.Error(codes.Internal, "handler returned no response")
			}

			response := dynamicpb.NewMessage(m.outBinding.descriptor)
			if err := toDynamic(m.outBinding, out, response); err != nil {
				return nil, status.Errorf(codes.Internal, "cannot encode response: %v", err)
			}
			return response, nil
		}

		if interceptor == nil {
			return invoke(ctx, request)
		}
		return interceptor(ctx, request, &grpc.UnaryServerInfo{
			Server:     nil,
			FullMethod: "/" + m.fullMethod(cfg),
		}, invoke)
	}
}

func (m *methodDef) fullMethod(cfg Config) string {
	return cfg.Package + "." + m.serviceName + "/" + m.name
}

// validateIdentifier rejects a name protobuf would not accept, at registration
// rather than deep inside protodesc.
func validateIdentifier(name, what string) error {
	if name == "" {
		return fmt.Errorf("%s cannot be empty", what)
	}
	first := rune(name[0])
	if !((first >= 'a' && first <= 'z') || (first >= 'A' && first <= 'Z') || first == '_') {
		return fmt.Errorf("%s %q must start with a letter", what, name)
	}
	for _, r := range name {
		isLetter := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		isDigit := r >= '0' && r <= '9'
		if !isLetter && !isDigit && r != '_' {
			return fmt.Errorf("%s %q contains %q; use letters, digits and underscores", what, name, r)
		}
	}
	return nil
}

// Package grpcapi serves a real gRPC API defined in Go, with no .proto file and
// no code-generation step.
//
// The protobuf descriptors are derived from your request and response structs at
// startup, registered in a descriptor registry, and served over gRPC server
// reflection. A Python, NestJS, Java or Bruno client therefore discovers the
// schema exactly as it would for a protoc-generated service, and speaks ordinary
// protobuf on the wire — there is no custom codec and no envelope.
//
// This is not the message-transport layer in common/microservice. That layer
// carries a JSON envelope and routes on a pattern, which is what lets one handler
// serve Redis, NATS and TCP alike, and is precisely why a generated gRPC client
// cannot call it. This package is the opposite trade: a genuine protobuf contract
// for one protocol.
//
//	type UserController struct {
//	    CreateUser grpcapi.Unary[dto.CreateUserDto, res.UserResponse] `grpc:"CreateUser"`
//	    GetUser    grpcapi.Unary[dto.GetUserDto, res.UserResponse]    `grpc:"GetUser"`
//	}
//
//	func (c *UserController) GRPCService() string { return "UserService" }
//
//	func NewUserController(service *services.UserService) *UserController {
//	    c := &UserController{}
//	    c.CreateUser = func(ctx context.Context, in *dto.CreateUserDto) (*res.UserResponse, error) {
//	        ...
//	    }
//	    return c
//	}
//
// Handlers take and return typed values rather than a *gin.Context, because a
// gRPC call has no URL, no query string and no HTTP status — mapping it onto an
// HTTP context would misrepresent all three.
package grpcapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sort"
	"sync"
	"time"

	"github.com/nika-framework/nika"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/reflection"
	reflectv1 "google.golang.org/grpc/reflection/grpc_reflection_v1"
	reflectv1alpha "google.golang.org/grpc/reflection/grpc_reflection_v1alpha"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
)

// Defaults applied to a zero Config.
const (
	DefaultAddr                = ":50051"
	DefaultMaxRecvMsgSize      = 8 << 20 // 8 MiB
	DefaultKeepaliveMinTime    = 30 * time.Second
	DefaultConnectionTimeout   = 20 * time.Second
	DefaultGracefulStopTimeout = 15 * time.Second
)

// Config wires the gRPC API.
type Config struct {
	// Addr is the listen address. Defaults to DefaultAddr.
	Addr string

	// Package is the protobuf package every derived message and service lands in,
	// for example "myapp.user.v1".
	//
	// It is the namespace clients see, so it is a published name: changing it
	// renames every message and breaks every generated client. Include a version.
	Package string

	// Insecure must be set explicitly to serve without transport security.
	//
	// There is no implicit plaintext default. gRPC's history of accidentally
	// unencrypted production deployments is the reason grpc-go itself requires an
	// explicit insecure credential, and repeating that here means nobody ships
	// plaintext by forgetting a field.
	Insecure bool

	// Creds is the transport credentials. It wins over TLSConfig.
	Creds credentials.TransportCredentials

	// Reflection serves the derived descriptors over gRPC server reflection.
	//
	// It defaults to on, because without it there is no .proto file for a client
	// to fall back to and the API would be undiscoverable — the whole point of
	// deriving descriptors is that they can be served. Turn it off on a public
	// port: reflection publishes the entire API surface.
	DisableReflection bool

	// MaxRecvMsgSize bounds an inbound message. Defaults to
	// DefaultMaxRecvMsgSize; grpc-go's own 4 MiB default silently rejects larger
	// payloads with a confusing error.
	MaxRecvMsgSize int

	// KeepaliveMinTime is the minimum interval between client pings the server
	// accepts. Defaults to DefaultKeepaliveMinTime. Without an enforcement policy
	// an abusive client can ping in a tight loop for free.
	KeepaliveMinTime time.Duration

	// ConnectionTimeout bounds connection setup.
	ConnectionTimeout time.Duration

	// GracefulStopTimeout bounds shutdown before connections are forced closed.
	// GracefulStop alone can hang indefinitely on a stuck stream.
	GracefulStopTimeout time.Duration

	// UnaryInterceptors run in order around every call. They are ordinary gRPC
	// interceptors, so anything from the ecosystem works.
	UnaryInterceptors []grpc.UnaryServerInterceptor

	// ServerOptions are appended last and can override anything above.
	ServerOptions []grpc.ServerOption

	// RegisterServices registers additional, hand-written or generated services on
	// the same server — useful when part of an API has a checked-in .proto.
	RegisterServices func(*grpc.Server)

	// Validate runs on every decoded request before the handler.
	//
	// Wire it to the framework validator to get the same rules the REST layer
	// uses; a non-nil error becomes InvalidArgument.
	Validate func(any) error

	// Logger receives lifecycle events. Defaults to nika.Logger().
	Logger *slog.Logger
}

func (c Config) withDefaults() Config {
	if c.Addr == "" {
		c.Addr = DefaultAddr
	}
	if c.MaxRecvMsgSize <= 0 {
		c.MaxRecvMsgSize = DefaultMaxRecvMsgSize
	}
	if c.KeepaliveMinTime <= 0 {
		c.KeepaliveMinTime = DefaultKeepaliveMinTime
	}
	if c.ConnectionTimeout <= 0 {
		c.ConnectionTimeout = DefaultConnectionTimeout
	}
	if c.GracefulStopTimeout <= 0 {
		c.GracefulStopTimeout = DefaultGracefulStopTimeout
	}
	if c.Logger == nil {
		c.Logger = nika.Logger()
	}
	return c
}

// Server owns the derived schema and the gRPC server built from it.
type Server struct {
	cfg      Config
	services map[string]*serviceDef

	mu       sync.Mutex
	grpcSrv  *grpc.Server
	listener net.Listener
	files    *protoregistry.Files
	started  bool
	closed   bool
	ready    chan struct{}
	addr     net.Addr
}

// Setup derives the API from the app's controllers and serves it when the app
// starts.
//
// It does not build the schema here: controllers are registered by LoadModule,
// which usually runs after this call, so the descriptors are assembled at start.
func Setup(app *nika.App, cfg Config) (*Server, error) {
	if app == nil {
		return nil, errors.New("grpcapi: app is required")
	}

	cfg = cfg.withDefaults()

	if cfg.Package == "" {
		return nil, errors.New("grpcapi: Config.Package is required — it is the protobuf namespace clients see, e.g. \"myapp.user.v1\"")
	}
	if err := validatePackage(cfg.Package); err != nil {
		return nil, err
	}
	if cfg.Creds == nil && !cfg.Insecure {
		return nil, errors.New("grpcapi: set Config.Creds for TLS, or Config.Insecure to serve plaintext deliberately")
	}

	server := &Server{
		cfg:      cfg,
		services: make(map[string]*serviceDef),
		ready:    make(chan struct{}),
	}

	app.RegisterSingleton(server)
	app.OnController(server.registerController)
	app.OnStart(server.Start)
	app.OnShutdown(server.Stop)

	return server, nil
}

// Start builds the schema, registers the services and begins serving.
func (s *Server) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errors.New("grpcapi: server is closed")
	}
	if s.started {
		s.mu.Unlock()
		return nil
	}
	s.started = true
	s.mu.Unlock()

	if len(s.services) == 0 {
		s.cfg.Logger.Warn("grpcapi: no controllers declared a gRPC service; not listening")
		return nil
	}

	// Building the descriptors is what turns Go types into a real contract, and it
	// happens once, at startup — a schema error is a boot failure, not a surprise
	// on the first call.
	files, descriptors, err := s.buildSchema()
	if err != nil {
		return err
	}
	s.files = files

	grpcSrv := grpc.NewServer(s.serverOptions()...)

	for name, def := range s.services {
		desc, ok := descriptors[name]
		if !ok {
			return fmt.Errorf("grpcapi: no descriptor built for service %q", name)
		}
		grpcSrv.RegisterService(def.grpcServiceDesc(s.cfg, desc), def)
	}

	if s.cfg.RegisterServices != nil {
		s.cfg.RegisterServices(grpcSrv)
	}

	if !s.cfg.DisableReflection {
		// reflection.Register cannot be used here: it resolves descriptors from
		// protoregistry.GlobalFiles, and these were built at runtime and never
		// linked into the binary — a client would see the service listed and then
		// get "not found" when it asked for the schema.
		//
		// Registering the server by hand with our own resolver is what makes the
		// derived descriptors actually retrievable. Both wire versions are served,
		// because tooling is split between them: grpcurl and Bruno speak v1,
		// older clients still ask for v1alpha.
		resolver := &compositeResolver{primary: files}
		reflectionSrv := reflection.NewServerV1(reflection.ServerOptions{
			Services:           grpcSrv,
			DescriptorResolver: resolver,
			ExtensionResolver:  protoregistry.GlobalTypes,
		})
		reflectv1.RegisterServerReflectionServer(grpcSrv, reflectionSrv)

		reflectv1alpha.RegisterServerReflectionServer(grpcSrv, reflection.NewServer(reflection.ServerOptions{
			Services:           grpcSrv,
			DescriptorResolver: resolver,
			ExtensionResolver:  protoregistry.GlobalTypes,
		}))
	}

	listener, err := net.Listen("tcp", s.cfg.Addr)
	if err != nil {
		return fmt.Errorf("grpcapi: listen on %s: %w", s.cfg.Addr, err)
	}

	s.mu.Lock()
	s.grpcSrv, s.listener, s.addr = grpcSrv, listener, listener.Addr()
	s.mu.Unlock()
	close(s.ready)

	s.cfg.Logger.Info("grpcapi server started",
		slog.String("addr", listener.Addr().String()),
		slog.String("package", s.cfg.Package),
		slog.Any("services", s.serviceNames()),
		slog.Bool("reflection", !s.cfg.DisableReflection),
		slog.Bool("tls", s.cfg.Creds != nil),
	)

	go func() {
		if err := grpcSrv.Serve(listener); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			s.cfg.Logger.Error("grpcapi server stopped", slog.Any("error", err))
		}
	}()

	return nil
}

// Stop drains the server.
func (s *Server) Stop(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	grpcSrv := s.grpcSrv
	s.mu.Unlock()

	if grpcSrv == nil {
		return nil
	}

	// GracefulStop can hang forever on a stuck stream, so bound it and fall back
	// to a hard stop rather than blocking shutdown indefinitely.
	stopped := make(chan struct{})
	go func() {
		grpcSrv.GracefulStop()
		close(stopped)
	}()

	timeout := s.cfg.GracefulStopTimeout
	select {
	case <-stopped:
	case <-time.After(timeout):
		s.cfg.Logger.Warn("grpcapi: graceful stop timed out; forcing", slog.Duration("after", timeout))
		grpcSrv.Stop()
	case <-ctx.Done():
		grpcSrv.Stop()
	}

	return nil
}

// Addr returns the bound address, or nil before the server starts. It is how a
// test discovers the port when Addr was ":0".
func (s *Server) Addr() net.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addr
}

// Ready is closed once the server is listening.
func (s *Server) Ready() <-chan struct{} { return s.ready }

// Files returns the derived descriptors, for tests or for writing the equivalent
// .proto to disk.
func (s *Server) Files() *protoregistry.Files { return s.files }

// ServiceNames returns the fully-qualified names of the derived services.
func (s *Server) ServiceNames() []string { return s.serviceNames() }

func (s *Server) serviceNames() []string {
	names := make([]string, 0, len(s.services))
	for name := range s.services {
		names = append(names, s.cfg.Package+"."+name)
	}
	sort.Strings(names)
	return names
}

// buildSchema assembles one FileDescriptor from every registered service.
func (s *Server) buildSchema() (*protoregistry.Files, map[string]protoreflect.ServiceDescriptor, error) {
	builder := newSchemaBuilder(s.cfg.Package)

	names := make([]string, 0, len(s.services))
	for name := range s.services {
		names = append(names, name)
	}
	// Deterministic order: the descriptor is a published artefact, and a file that
	// reorders itself between runs makes diffs and caches useless.
	sort.Strings(names)

	serviceProtos := make([]*descriptorpb.ServiceDescriptorProto, 0, len(names))
	for _, name := range names {
		def := s.services[name]
		proto, err := def.build(builder)
		if err != nil {
			return nil, nil, err
		}
		serviceProtos = append(serviceProtos, proto)
	}

	descriptor, err := builder.fileDescriptor(serviceProtos...)
	if err != nil {
		return nil, nil, err
	}

	files := new(protoregistry.Files)
	if err := files.RegisterFile(descriptor); err != nil {
		return nil, nil, fmt.Errorf("grpcapi: registering the derived schema: %w", err)
	}

	// Bind each message descriptor back to its Go type, so the handlers can build
	// dynamic messages without another lookup.
	for _, binding := range builder.orderedBinds {
		message := descriptor.Messages().ByName(protoreflect.Name(shortName(binding.name)))
		if message == nil {
			return nil, nil, fmt.Errorf("grpcapi: message %q vanished from the built file", binding.name)
		}
		binding.descriptor = message
		for _, field := range binding.fields {
			field.descriptor = message.Fields().ByNumber(field.number)
		}
	}

	descriptors := make(map[string]protoreflect.ServiceDescriptor, len(names))
	for _, name := range names {
		service := descriptor.Services().ByName(protoreflect.Name(name))
		if service == nil {
			return nil, nil, fmt.Errorf("grpcapi: service %q vanished from the built file", name)
		}
		descriptors[name] = service
	}

	return files, descriptors, nil
}

func (s *Server) serverOptions() []grpc.ServerOption {
	creds := s.cfg.Creds
	if creds == nil {
		creds = insecure.NewCredentials()
	}

	options := []grpc.ServerOption{
		grpc.Creds(creds),
		grpc.MaxRecvMsgSize(s.cfg.MaxRecvMsgSize),
		grpc.ConnectionTimeout(s.cfg.ConnectionTimeout),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             s.cfg.KeepaliveMinTime,
			PermitWithoutStream: false,
		}),
	}
	if len(s.cfg.UnaryInterceptors) > 0 {
		options = append(options, grpc.ChainUnaryInterceptor(s.cfg.UnaryInterceptors...))
	}
	return append(options, s.cfg.ServerOptions...)
}

// compositeResolver looks in the runtime registry first, then the global one.
//
// The derived descriptors live only in the runtime registry. The well-known types
// they reference — Timestamp — live only in the global one, because they were
// linked into the binary. Reflection needs both.
type compositeResolver struct {
	primary *protoregistry.Files
}

func (c *compositeResolver) FindFileByPath(path string) (protoreflect.FileDescriptor, error) {
	if c.primary != nil {
		if file, err := c.primary.FindFileByPath(path); err == nil {
			return file, nil
		}
	}
	return protoregistry.GlobalFiles.FindFileByPath(path)
}

func (c *compositeResolver) FindDescriptorByName(name protoreflect.FullName) (protoreflect.Descriptor, error) {
	if c.primary != nil {
		if desc, err := c.primary.FindDescriptorByName(name); err == nil {
			return desc, nil
		}
	}
	return protoregistry.GlobalFiles.FindDescriptorByName(name)
}

// fileNameFor derives a stable synthetic path for the generated file.
func fileNameFor(pkg string) string {
	path := ""
	for _, part := range splitPackage(pkg) {
		path += part + "/"
	}
	return path + "nika.grpcapi.proto"
}

func splitPackage(pkg string) []string {
	var parts []string
	current := ""
	for _, r := range pkg {
		if r == '.' {
			parts = append(parts, current)
			current = ""
			continue
		}
		current += string(r)
	}
	if current != "" {
		parts = append(parts, current)
	}
	return parts
}

func shortName(qualified string) string {
	for i := len(qualified) - 1; i >= 0; i-- {
		if qualified[i] == '.' {
			return qualified[i+1:]
		}
	}
	return qualified
}

// validatePackage rejects a package name protobuf would not accept, at setup
// rather than deep inside protodesc with a less obvious message.
func validatePackage(pkg string) error {
	if pkg == "" {
		return errors.New("grpcapi: package cannot be empty")
	}
	for _, part := range splitPackage(pkg) {
		if part == "" {
			return fmt.Errorf("grpcapi: package %q has an empty segment", pkg)
		}
		if part[0] >= '0' && part[0] <= '9' {
			return fmt.Errorf("grpcapi: package segment %q starts with a digit", part)
		}
		for _, r := range part {
			isLetter := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
			isDigit := r >= '0' && r <= '9'
			if !isLetter && !isDigit && r != '_' {
				return fmt.Errorf("grpcapi: package segment %q contains %q; use letters, digits and underscores", part, r)
			}
		}
	}
	return nil
}

package grpcapi

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

// Client calls a service that was defined the same way this package serves one.
//
// It derives the descriptors from the Go request and response types on this side,
// so there is still no .proto file and no codegen. Both ends deriving from
// structurally identical types produce identical wire formats — which is also the
// risk: if the two definitions drift, the mismatch shows up as a decode error
// rather than a compile error. Share the types where you can, and pin field
// numbers with `protobuf:"N"` where you cannot.
//
// A caller in another language does not need this: it discovers the schema over
// reflection and uses its own generated stubs.
type Client struct {
	conn  *grpc.ClientConn
	owned bool

	pkg     string
	timeout time.Duration

	mu     sync.RWMutex
	schema map[reflect.Type]*messageBinding
}

// ClientConfig configures a Client.
type ClientConfig struct {
	// Target is the address to dial, in gRPC target syntax.
	Target string

	// Conn reuses an existing connection instead of dialing. Ownership stays with
	// the caller: Close does not close a connection it did not create.
	Conn *grpc.ClientConn

	// Package must match the server's Config.Package, because it is part of every
	// message's fully-qualified name and therefore part of the wire contract.
	Package string

	// Insecure must be set explicitly to dial without transport security, for the
	// same reason as on the server: plaintext should never be a default.
	Insecure bool

	// Creds is the transport credentials. It wins over Insecure.
	Creds credentials.TransportCredentials

	// Timeout bounds a call when the context carries no deadline. Defaults to 30s;
	// an unbounded RPC pins a goroutine until the peer decides otherwise.
	Timeout time.Duration

	// MaxRecvMsgSize bounds an inbound message. Defaults to DefaultMaxRecvMsgSize.
	MaxRecvMsgSize int

	// Keepalive configures client-side pings. Keep Time at or above the server's
	// KeepaliveMinTime or the server will drop the connection.
	Keepalive keepalive.ClientParameters

	// DialOptions are appended last and can override anything above.
	DialOptions []grpc.DialOption
}

// NewClient dials a service.
func NewClient(cfg ClientConfig) (*Client, error) {
	if cfg.Package == "" {
		return nil, errors.New("grpcapi: ClientConfig.Package is required and must match the server's")
	}
	if err := validatePackage(cfg.Package); err != nil {
		return nil, err
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	if cfg.MaxRecvMsgSize <= 0 {
		cfg.MaxRecvMsgSize = DefaultMaxRecvMsgSize
	}

	client := &Client{
		pkg:     cfg.Package,
		timeout: cfg.Timeout,
		schema:  make(map[reflect.Type]*messageBinding),
	}

	if cfg.Conn != nil {
		client.conn = cfg.Conn
		return client, nil
	}

	if cfg.Target == "" {
		return nil, errors.New("grpcapi: ClientConfig needs a Target or a Conn")
	}

	creds := cfg.Creds
	if creds == nil {
		if !cfg.Insecure {
			return nil, errors.New("grpcapi: set ClientConfig.Creds for TLS, or Insecure to dial plaintext deliberately")
		}
		creds = insecure.NewCredentials()
	}

	options := []grpc.DialOption{
		grpc.WithTransportCredentials(creds),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(cfg.MaxRecvMsgSize)),
	}
	if cfg.Keepalive.Time > 0 {
		options = append(options, grpc.WithKeepaliveParams(cfg.Keepalive))
	}
	options = append(options, cfg.DialOptions...)

	conn, err := grpc.NewClient(cfg.Target, options...)
	if err != nil {
		return nil, fmt.Errorf("grpcapi: dialing %s: %w", cfg.Target, err)
	}

	client.conn = conn
	client.owned = true
	return client, nil
}

// Conn exposes the underlying connection, for interceptors or health checks.
func (c *Client) Conn() *grpc.ClientConn { return c.conn }

// Close releases the connection, unless it was supplied by the caller.
func (c *Client) Close() error {
	if c == nil || !c.owned || c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

// binding returns the derived message binding for a Go type, building it once.
//
// A separate builder per type would give each message its own descriptor pool and
// break nested types that are shared between messages, so the whole schema for a
// type is derived in one pass and cached.
func (c *Client) binding(goType reflect.Type) (*messageBinding, error) {
	c.mu.RLock()
	cached, ok := c.schema[goType]
	c.mu.RUnlock()
	if ok {
		return cached, nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if cached, ok := c.schema[goType]; ok {
		return cached, nil
	}

	builder := newSchemaBuilder(c.pkg)
	binding, err := builder.message(goType)
	if err != nil {
		return nil, err
	}
	if err := builder.resolve(); err != nil {
		return nil, err
	}

	for _, derived := range builder.orderedBinds {
		c.schema[derived.goType] = derived
	}
	return binding, nil
}

// Invoke performs a unary call.
//
//	user, err := grpcapi.Invoke[GetUserRequest, User](
//	    ctx, client, "UserService", "GetUser", &GetUserRequest{ID: 1})
//
// It is a function rather than a method because a method cannot introduce its own
// type parameters in Go. The upside is that the result is a real struct instead of
// a map every call site has to re-assert.
func Invoke[In, Out any](
	ctx context.Context,
	client *Client,
	service, method string,
	in *In,
) (*Out, error) {
	if client == nil {
		return nil, errors.New("grpcapi: nil client")
	}
	if in == nil {
		return nil, errors.New("grpcapi: nil request")
	}

	inBinding, err := client.binding(reflect.TypeOf(in).Elem())
	if err != nil {
		return nil, fmt.Errorf("grpcapi: deriving the request schema: %w", err)
	}
	outBinding, err := client.binding(reflect.TypeOf((*Out)(nil)).Elem())
	if err != nil {
		return nil, fmt.Errorf("grpcapi: deriving the response schema: %w", err)
	}

	request := dynamicpb.NewMessage(inBinding.descriptor)
	if err := toDynamic(inBinding, reflect.ValueOf(in), request); err != nil {
		return nil, fmt.Errorf("grpcapi: encoding the request: %w", err)
	}
	response := dynamicpb.NewMessage(outBinding.descriptor)

	if _, hasDeadline := ctx.Deadline(); !hasDeadline && client.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, client.timeout)
		defer cancel()
	}

	fullMethod := "/" + client.pkg + "." + service + "/" + method
	if err := client.conn.Invoke(ctx, fullMethod, request, response); err != nil {
		// The status is passed through untouched: the caller branches on the code,
		// and wrapping it would hide NotFound behind Unknown.
		return nil, err
	}

	out := new(Out)
	if err := fromDynamic(outBinding, response.ProtoReflect(), reflect.ValueOf(out).Elem()); err != nil {
		return nil, fmt.Errorf("grpcapi: decoding the response: %w", err)
	}
	return out, nil
}

// resolve builds the descriptors for everything the builder has collected.
//
// The server does this as part of assembling the whole file; a client needs the
// same step for the subset of types it actually sends, which is why it lives on
// the builder rather than inside Server.
func (b *schemaBuilder) resolve() error {
	file, err := b.fileDescriptor()
	if err != nil {
		return err
	}

	for _, binding := range b.orderedBinds {
		message := file.Messages().ByName(protoreflect.Name(shortName(binding.name)))
		if message == nil {
			return fmt.Errorf("grpcapi: message %q vanished from the derived schema", binding.name)
		}
		binding.descriptor = message
		for _, field := range binding.fields {
			field.descriptor = message.Fields().ByNumber(field.number)
		}
	}
	return nil
}

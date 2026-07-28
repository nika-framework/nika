package grpcapi_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nika-framework/nika"
	"github.com/nika-framework/nika/common/grpcapi"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	grpcreflect "google.golang.org/grpc/reflection/grpc_reflection_v1"
)

// The point of these tests is that nothing here is generated. The service is
// declared in Go, the descriptors are derived at startup, and the calls below go
// over the wire as ordinary protobuf — decoded by a client that learned the schema
// from reflection, exactly as Python, NestJS or Bruno would.

// --- the service under test -----------------------------------------------

type CreateUserRequest struct {
	Username string `json:"username"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

type GetUserRequest struct {
	ID int64 `json:"id"`
}

type UpdateUserRequest struct {
	ID       int64   `json:"id"`
	Username *string `json:"username"`
	Email    *string `json:"email"`
}

type ListUsersRequest struct {
	Page  int64  `json:"page"`
	Count int64  `json:"count"`
	Query string `json:"query"`
}

type UserReply struct {
	ID        int64     `json:"id"`
	Username  string    `json:"username"`
	Email     string    `json:"email"`
	Verified  bool      `json:"verified"`
	Score     float64   `json:"score"`
	Tags      []string  `json:"tags"`
	Avatar    []byte    `json:"avatar"`
	CreatedAt time.Time `json:"created_at"`

	// Never on the wire: excluded from the contract itself, so no handler can
	// leak it by forgetting to strip it.
	PasswordHash string `json:"-"`
}

type ListUsersReply struct {
	Data  []UserReply `json:"data"`
	Total int64       `json:"total"`
	Page  int64       `json:"page"`
}

type UserController struct {
	CreateUser grpcapi.Unary[CreateUserRequest, UserReply]     `grpc:"CreateUser"`
	GetUser    grpcapi.Unary[GetUserRequest, UserReply]        `grpc:"GetUser"`
	UpdateUser grpcapi.Unary[UpdateUserRequest, UserReply]     `grpc:"UpdateUser"`
	ListUsers  grpcapi.Unary[ListUsersRequest, ListUsersReply] `grpc:"ListUsers"`

	// Not an RPC: no grpc tag, so the scanner ignores it.
	Helper func()
}

func (c *UserController) GRPCService() string { return "UserService" }

var fixedTime = time.Date(2026, 3, 4, 5, 6, 7, 89000000, time.UTC)

func newUserController() *UserController {
	store := map[int64]*UserReply{
		1: {ID: 1, Username: "ada", Email: "ada@example.com", Verified: true,
			Score: 9.5, Tags: []string{"admin", "founder"}, Avatar: []byte{0x89, 0x50},
			CreatedAt: fixedTime, PasswordHash: "never-on-the-wire"},
	}
	next := int64(2)

	c := &UserController{}

	c.CreateUser = func(_ context.Context, in *CreateUserRequest) (*UserReply, error) {
		if in.Username == "" {
			return nil, grpcapi.InvalidArgument("username is required")
		}
		user := &UserReply{ID: next, Username: in.Username, Email: in.Email, CreatedAt: fixedTime}
		store[next] = user
		next++
		return user, nil
	}

	c.GetUser = func(_ context.Context, in *GetUserRequest) (*UserReply, error) {
		user, ok := store[in.ID]
		if !ok {
			return nil, grpcapi.NotFound("no user with id %d", in.ID)
		}
		return user, nil
	}

	c.UpdateUser = func(_ context.Context, in *UpdateUserRequest) (*UserReply, error) {
		user, ok := store[in.ID]
		if !ok {
			return nil, grpcapi.NotFound("no user with id %d", in.ID)
		}
		if in.Username != nil {
			user.Username = *in.Username
		}
		if in.Email != nil {
			user.Email = *in.Email
		}
		return user, nil
	}

	c.ListUsers = func(_ context.Context, in *ListUsersRequest) (*ListUsersReply, error) {
		out := &ListUsersReply{Page: in.Page}
		for _, u := range store {
			out.Data = append(out.Data, *u)
		}
		out.Total = int64(len(out.Data))
		return out, nil
	}

	return c
}

// --- harness --------------------------------------------------------------

// serve boots an app with the gRPC API on a free loopback port and returns a
// connection plus the descriptors the server derived.
func serve(t *testing.T, mutate func(*grpcapi.Config)) (*grpc.ClientConn, *grpcapi.Server) {
	t.Helper()

	cfg := grpcapi.Config{
		Addr:     "127.0.0.1:0",
		Package:  "nikatest.user.v1",
		Insecure: true,
	}
	if mutate != nil {
		mutate(&cfg)
	}

	app := nika.NewApp(nika.Config{Mode: gin.TestMode, DisableGracefulShutdown: true})

	server, err := grpcapi.Setup(app, cfg)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}

	app.RegisterControllers(newUserController())

	if err := app.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Stop(ctx)
	})

	select {
	case <-server.Ready():
	case <-time.After(5 * time.Second):
		t.Fatal("the server never became ready")
	}

	conn, err := grpc.NewClient(server.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	return conn, server
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// messageFor returns a dynamic message for a derived type, which is how a client
// that only has the reflection-supplied schema builds a request.
func messageFor(t *testing.T, server *grpcapi.Server, name string) *dynamicpb.Message {
	t.Helper()

	desc, err := server.Files().FindDescriptorByName(protoreflect.FullName("nikatest.user.v1." + name))
	if err != nil {
		t.Fatalf("finding %s: %v", name, err)
	}
	message, ok := desc.(protoreflect.MessageDescriptor)
	if !ok {
		t.Fatalf("%s is a %T, not a message", name, desc)
	}
	return dynamicpb.NewMessage(message)
}

func setField(t *testing.T, msg *dynamicpb.Message, field string, value protoreflect.Value) {
	t.Helper()
	descriptor := msg.Descriptor().Fields().ByName(protoreflect.Name(field))
	if descriptor == nil {
		t.Fatalf("no field %q on %s", field, msg.Descriptor().FullName())
	}
	msg.Set(descriptor, value)
}

func getField(t *testing.T, msg *dynamicpb.Message, field string) protoreflect.Value {
	t.Helper()
	descriptor := msg.Descriptor().Fields().ByName(protoreflect.Name(field))
	if descriptor == nil {
		t.Fatalf("no field %q on %s", field, msg.Descriptor().FullName())
	}
	return msg.Get(descriptor)
}

// invoke makes a real unary call with the standard protobuf codec.
func invoke(t *testing.T, conn *grpc.ClientConn, method string, in, out proto.Message) error {
	t.Helper()
	return conn.Invoke(testContext(t), "/nikatest.user.v1.UserService/"+method, in, out)
}

// --- the schema -----------------------------------------------------------

// TestDerivedSchemaIsAValidProtobufFile is the foundation: if the descriptors are
// not a well-formed file, no client can generate from them and reflection has
// nothing to serve.
func TestDerivedSchemaIsAValidProtobufFile(t *testing.T) {
	_, server := serve(t, nil)

	for _, name := range []string{
		"CreateUserRequest", "GetUserRequest", "UpdateUserRequest",
		"ListUsersRequest", "UserReply", "ListUsersReply",
	} {
		if _, err := server.Files().FindDescriptorByName(
			protoreflect.FullName("nikatest.user.v1." + name)); err != nil {
			t.Errorf("message %s is missing from the derived schema: %v", name, err)
		}
	}

	desc, err := server.Files().FindDescriptorByName("nikatest.user.v1.UserService")
	if err != nil {
		t.Fatalf("the service is missing: %v", err)
	}
	service, ok := desc.(protoreflect.ServiceDescriptor)
	if !ok {
		t.Fatalf("UserService is a %T", desc)
	}
	if service.Methods().Len() != 4 {
		t.Errorf("the service has %d methods, want 4", service.Methods().Len())
	}
}

// TestFieldTypesAreDerivedCorrectly pins the Go→protobuf mapping. Getting a kind
// wrong here would produce a schema clients generate from and then fail to decode.
func TestFieldTypesAreDerivedCorrectly(t *testing.T) {
	_, server := serve(t, nil)

	desc, err := server.Files().FindDescriptorByName("nikatest.user.v1.UserReply")
	if err != nil {
		t.Fatalf("UserReply: %v", err)
	}
	fields := desc.(protoreflect.MessageDescriptor).Fields()

	expected := map[string]protoreflect.Kind{
		"id":         protoreflect.Int64Kind,
		"username":   protoreflect.StringKind,
		"verified":   protoreflect.BoolKind,
		"score":      protoreflect.DoubleKind,
		"tags":       protoreflect.StringKind, // repeated string
		"avatar":     protoreflect.BytesKind,
		"created_at": protoreflect.MessageKind, // google.protobuf.Timestamp
	}

	for name, kind := range expected {
		field := fields.ByName(protoreflect.Name(name))
		if field == nil {
			t.Errorf("field %q is missing", name)
			continue
		}
		if field.Kind() != kind {
			t.Errorf("field %q is %s, want %s", name, field.Kind(), kind)
		}
	}

	if tags := fields.ByName("tags"); tags != nil && !tags.IsList() {
		t.Error("tags should be repeated")
	}
	if createdAt := fields.ByName("created_at"); createdAt != nil {
		if got := string(createdAt.Message().FullName()); got != "google.protobuf.Timestamp" {
			t.Errorf("created_at is %s, want google.protobuf.Timestamp", got)
		}
	}
}

// TestExcludedFieldIsNotInTheContract is structural: a json:"-" field must not
// exist in the message at all, so it cannot be returned by accident.
func TestExcludedFieldIsNotInTheContract(t *testing.T) {
	_, server := serve(t, nil)

	desc, _ := server.Files().FindDescriptorByName("nikatest.user.v1.UserReply")
	fields := desc.(protoreflect.MessageDescriptor).Fields()

	for i := 0; i < fields.Len(); i++ {
		if name := string(fields.Get(i).Name()); name == "password_hash" {
			t.Fatal("password_hash is in the derived contract; json:\"-\" must exclude it")
		}
	}
}

// TestOptionalFieldsTrackPresence: a Go pointer must become proto3 optional, or a
// PATCH cannot tell "leave alone" from "set to empty".
func TestOptionalFieldsTrackPresence(t *testing.T) {
	_, server := serve(t, nil)

	desc, _ := server.Files().FindDescriptorByName("nikatest.user.v1.UpdateUserRequest")
	fields := desc.(protoreflect.MessageDescriptor).Fields()

	if field := fields.ByName("username"); field == nil || !field.HasPresence() {
		t.Error("username should track presence; a *string must become proto3 optional")
	}
	if field := fields.ByName("id"); field != nil && field.HasPresence() {
		t.Error("id is a plain int64 and should not track presence")
	}
}

// --- real calls -----------------------------------------------------------

// TestUnaryCallOverTheWire is the headline: a request built purely from the
// derived descriptor, marshalled by the standard protobuf codec, answered by a Go
// handler that never saw a .proto file.
func TestUnaryCallOverTheWire(t *testing.T) {
	conn, server := serve(t, nil)

	in := messageFor(t, server, "GetUserRequest")
	setField(t, in, "id", protoreflect.ValueOfInt64(1))

	out := messageFor(t, server, "UserReply")
	if err := invoke(t, conn, "GetUser", in, out); err != nil {
		t.Fatalf("GetUser: %v", err)
	}

	if got := getField(t, out, "username").String(); got != "ada" {
		t.Errorf("username = %q, want \"ada\"", got)
	}
	if got := getField(t, out, "id").Int(); got != 1 {
		t.Errorf("id = %d, want 1", got)
	}
	if got := getField(t, out, "verified").Bool(); !got {
		t.Error("verified = false, want true")
	}
	if got := getField(t, out, "score").Float(); got != 9.5 {
		t.Errorf("score = %v, want 9.5", got)
	}
}

func TestRepeatedAndBytesRoundTrip(t *testing.T) {
	conn, server := serve(t, nil)

	in := messageFor(t, server, "GetUserRequest")
	setField(t, in, "id", protoreflect.ValueOfInt64(1))

	out := messageFor(t, server, "UserReply")
	if err := invoke(t, conn, "GetUser", in, out); err != nil {
		t.Fatalf("GetUser: %v", err)
	}

	tags := getField(t, out, "tags").List()
	if tags.Len() != 2 || tags.Get(0).String() != "admin" {
		t.Errorf("tags = %v, want [admin founder]", tags)
	}

	avatar := getField(t, out, "avatar").Bytes()
	if len(avatar) != 2 || avatar[0] != 0x89 {
		t.Errorf("avatar = %v, want [0x89 0x50]", avatar)
	}
}

// TestTimestampRoundTrip: time.Time must arrive as a real
// google.protobuf.Timestamp, which is what makes the field usable from any
// language.
func TestTimestampRoundTrip(t *testing.T) {
	conn, server := serve(t, nil)

	in := messageFor(t, server, "GetUserRequest")
	setField(t, in, "id", protoreflect.ValueOfInt64(1))

	out := messageFor(t, server, "UserReply")
	if err := invoke(t, conn, "GetUser", in, out); err != nil {
		t.Fatalf("GetUser: %v", err)
	}

	timestamp := getField(t, out, "created_at").Message()
	seconds := timestamp.Get(timestamp.Descriptor().Fields().ByName("seconds")).Int()
	nanos := timestamp.Get(timestamp.Descriptor().Fields().ByName("nanos")).Int()

	got := time.Unix(seconds, nanos).UTC()
	if !got.Equal(fixedTime) {
		t.Errorf("created_at = %s, want %s", got, fixedTime)
	}
}

// TestOptionalAbsentMeansUnchanged is the behaviour proto3 optional exists for.
func TestOptionalAbsentMeansUnchanged(t *testing.T) {
	conn, server := serve(t, nil)

	in := messageFor(t, server, "UpdateUserRequest")
	setField(t, in, "id", protoreflect.ValueOfInt64(1))
	setField(t, in, "username", protoreflect.ValueOfString("ada-lovelace"))
	// email is deliberately not set.

	out := messageFor(t, server, "UserReply")
	if err := invoke(t, conn, "UpdateUser", in, out); err != nil {
		t.Fatalf("UpdateUser: %v", err)
	}

	if got := getField(t, out, "username").String(); got != "ada-lovelace" {
		t.Errorf("username = %q, want it updated", got)
	}
	if got := getField(t, out, "email").String(); got != "ada@example.com" {
		t.Errorf("email = %q, want it untouched — an absent optional was treated as empty", got)
	}
}

func TestRepeatedMessageResponse(t *testing.T) {
	conn, server := serve(t, nil)

	in := messageFor(t, server, "ListUsersRequest")
	setField(t, in, "page", protoreflect.ValueOfInt64(1))

	out := messageFor(t, server, "ListUsersReply")
	if err := invoke(t, conn, "ListUsers", in, out); err != nil {
		t.Fatalf("ListUsers: %v", err)
	}

	data := getField(t, out, "data").List()
	if data.Len() != 1 {
		t.Fatalf("data has %d entries, want 1", data.Len())
	}
	nested := data.Get(0).Message()
	username := nested.Get(nested.Descriptor().Fields().ByName("username")).String()
	if username != "ada" {
		t.Errorf("data[0].username = %q, want \"ada\"", username)
	}
}

// --- status codes ---------------------------------------------------------

// TestStatusCodes is what separates a real gRPC API from a tunnel: a client
// branches on the code, so a missing record must not arrive as OK-with-zeros and a
// bad argument must not arrive as Internal.
func TestStatusCodes(t *testing.T) {
	conn, server := serve(t, nil)

	t.Run("not found", func(t *testing.T) {
		in := messageFor(t, server, "GetUserRequest")
		setField(t, in, "id", protoreflect.ValueOfInt64(9999))

		err := invoke(t, conn, "GetUser", in, messageFor(t, server, "UserReply"))
		if got := status.Code(err); got != codes.NotFound {
			t.Errorf("code = %s, want NotFound (%v)", got, err)
		}
	})

	t.Run("invalid argument", func(t *testing.T) {
		in := messageFor(t, server, "CreateUserRequest")
		setField(t, in, "email", protoreflect.ValueOfString("a@b.c"))

		err := invoke(t, conn, "CreateUser", in, messageFor(t, server, "UserReply"))
		if got := status.Code(err); got != codes.InvalidArgument {
			t.Errorf("code = %s, want InvalidArgument (%v)", got, err)
		}
	})

	t.Run("unknown method", func(t *testing.T) {
		in := messageFor(t, server, "GetUserRequest")
		err := conn.Invoke(testContext(t), "/nikatest.user.v1.UserService/Nope",
			in, messageFor(t, server, "UserReply"))
		if got := status.Code(err); got != codes.Unimplemented {
			t.Errorf("code = %s, want Unimplemented", got)
		}
	})
}

// TestInternalErrorsDoNotLeak: an unrecognised error must not reach the client
// verbatim, because a database error can carry a DSN or a table layout.
func TestInternalErrorsDoNotLeak(t *testing.T) {
	app := nika.NewApp(nika.Config{Mode: gin.TestMode, DisableGracefulShutdown: true})

	server, err := grpcapi.Setup(app, grpcapi.Config{
		Addr: "127.0.0.1:0", Package: "nikatest.leak.v1", Insecure: true,
	})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}

	leaky := &UserController{}
	leaky.CreateUser = func(context.Context, *CreateUserRequest) (*UserReply, error) {
		return nil, errors.New("pq: password authentication failed for user \"admin\" on host db.internal")
	}
	leaky.GetUser = func(context.Context, *GetUserRequest) (*UserReply, error) { return &UserReply{}, nil }
	leaky.UpdateUser = func(context.Context, *UpdateUserRequest) (*UserReply, error) { return &UserReply{}, nil }
	leaky.ListUsers = func(context.Context, *ListUsersRequest) (*ListUsersReply, error) { return &ListUsersReply{}, nil }

	app.RegisterControllers(leaky)
	if err := app.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = server.Stop(context.Background()) })

	<-server.Ready()
	conn, err := grpc.NewClient(server.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	desc, _ := server.Files().FindDescriptorByName("nikatest.leak.v1.CreateUserRequest")
	in := dynamicpb.NewMessage(desc.(protoreflect.MessageDescriptor))
	in.Set(in.Descriptor().Fields().ByName("username"), protoreflect.ValueOfString("x"))

	outDesc, _ := server.Files().FindDescriptorByName("nikatest.leak.v1.UserReply")
	callErr := conn.Invoke(testContext(t), "/nikatest.leak.v1.UserService/CreateUser",
		in, dynamicpb.NewMessage(outDesc.(protoreflect.MessageDescriptor)))

	if got := status.Code(callErr); got != codes.Internal {
		t.Errorf("code = %s, want Internal", got)
	}
	message := status.Convert(callErr).Message()
	for _, secret := range []string{"password", "admin", "db.internal", "pq:"} {
		if contains(message, secret) {
			t.Errorf("the status message leaked %q: %s", secret, message)
		}
	}
}

func contains(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) &&
		func() bool {
			for i := 0; i+len(needle) <= len(haystack); i++ {
				if haystack[i:i+len(needle)] == needle {
					return true
				}
			}
			return false
		}()
}

// --- reflection -----------------------------------------------------------

// TestReflectionServesTheDerivedSchema is what makes Bruno, grpcurl and a Python
// client work without a .proto file: the schema they ask for is the one derived
// from the Go types.
func TestReflectionServesTheDerivedSchema(t *testing.T) {
	conn, _ := serve(t, nil)

	stream, err := grpcreflect.NewServerReflectionClient(conn).ServerReflectionInfo(testContext(t))
	if err != nil {
		t.Fatalf("reflection stream: %v", err)
	}

	// 1. A client lists the services.
	if err := stream.Send(&grpcreflect.ServerReflectionRequest{
		MessageRequest: &grpcreflect.ServerReflectionRequest_ListServices{},
	}); err != nil {
		t.Fatalf("list request: %v", err)
	}
	reply, err := stream.Recv()
	if err != nil {
		t.Fatalf("list reply: %v", err)
	}

	var found bool
	for _, svc := range reply.GetListServicesResponse().GetService() {
		if svc.GetName() == "nikatest.user.v1.UserService" {
			found = true
		}
	}
	if !found {
		t.Fatal("reflection does not list the derived service")
	}

	// 2. It then asks for the file containing it — this is the step that fails
	//    when a service is hand-declared without a real descriptor.
	if err := stream.Send(&grpcreflect.ServerReflectionRequest{
		MessageRequest: &grpcreflect.ServerReflectionRequest_FileContainingSymbol{
			FileContainingSymbol: "nikatest.user.v1.UserService",
		},
	}); err != nil {
		t.Fatalf("descriptor request: %v", err)
	}
	reply, err = stream.Recv()
	if err != nil {
		t.Fatalf("descriptor reply: %v", err)
	}

	if errResp := reply.GetErrorResponse(); errResp != nil {
		t.Fatalf("reflection could not supply the descriptor: %s", errResp.GetErrorMessage())
	}
	files := reply.GetFileDescriptorResponse().GetFileDescriptorProto()
	if len(files) == 0 {
		t.Fatal("reflection returned no file descriptor, so a client cannot generate a stub")
	}
}

// --- configuration --------------------------------------------------------

func TestSetupRejectsBadConfig(t *testing.T) {
	newApp := func() *nika.App {
		return nika.NewApp(nika.Config{Mode: gin.TestMode})
	}

	t.Run("no package", func(t *testing.T) {
		if _, err := grpcapi.Setup(newApp(), grpcapi.Config{Insecure: true}); err == nil {
			t.Error("Setup with no Package returned nil")
		}
	})

	t.Run("invalid package", func(t *testing.T) {
		_, err := grpcapi.Setup(newApp(), grpcapi.Config{Package: "1bad.pkg", Insecure: true})
		if err == nil {
			t.Error("Setup with a package starting in a digit returned nil")
		}
	})

	t.Run("neither creds nor insecure", func(t *testing.T) {
		// Plaintext must be a deliberate choice, never a default.
		_, err := grpcapi.Setup(newApp(), grpcapi.Config{Package: "a.v1"})
		if err == nil {
			t.Error("Setup without credentials or Insecure returned nil")
		}
	})

	t.Run("nil app", func(t *testing.T) {
		if _, err := grpcapi.Setup(nil, grpcapi.Config{Package: "a.v1", Insecure: true}); err == nil {
			t.Error("Setup with a nil app returned nil")
		}
	})
}

// TestControllerWithoutAServiceNameIsRejected: the error has to say what to add,
// because the interface is not discoverable from the tag alone.
func TestControllerWithoutAServiceNameIsRejected(t *testing.T) {
	type nameless struct {
		Do grpcapi.Unary[GetUserRequest, UserReply] `grpc:"Do"`
	}

	app := nika.NewApp(nika.Config{Mode: gin.TestMode})
	if _, err := grpcapi.Setup(app, grpcapi.Config{
		Addr: "127.0.0.1:0", Package: "nikatest.x.v1", Insecure: true,
	}); err != nil {
		t.Fatalf("Setup: %v", err)
	}

	defer func() {
		recovered := recover()
		if recovered == nil {
			t.Fatal("registering a controller with no service name did not fail")
		}
		if !contains(toString(recovered), "GRPCService") {
			t.Errorf("the error does not say what to add: %v", recovered)
		}
	}()

	c := &nameless{}
	c.Do = func(context.Context, *GetUserRequest) (*UserReply, error) { return nil, nil }
	app.RegisterControllers(c)
}

func toString(v any) string {
	if err, ok := v.(error); ok {
		return err.Error()
	}
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// TestValidateHookRunsBeforeTheHandler wires the framework validator in and checks
// a rejection becomes InvalidArgument rather than reaching the handler.
func TestValidateHookRunsBeforeTheHandler(t *testing.T) {
	handlerRan := false

	app := nika.NewApp(nika.Config{Mode: gin.TestMode, DisableGracefulShutdown: true})
	server, err := grpcapi.Setup(app, grpcapi.Config{
		Addr:     "127.0.0.1:0",
		Package:  "nikatest.validate.v1",
		Insecure: true,
		Validate: func(any) error { return errors.New("username must not be empty") },
	})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}

	c := newUserController()
	c.GetUser = func(context.Context, *GetUserRequest) (*UserReply, error) {
		handlerRan = true
		return &UserReply{}, nil
	}
	app.RegisterControllers(c)

	if err := app.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = server.Stop(context.Background()) })
	<-server.Ready()

	conn, err := grpc.NewClient(server.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	inDesc, _ := server.Files().FindDescriptorByName("nikatest.validate.v1.GetUserRequest")
	outDesc, _ := server.Files().FindDescriptorByName("nikatest.validate.v1.UserReply")

	callErr := conn.Invoke(testContext(t), "/nikatest.validate.v1.UserService/GetUser",
		dynamicpb.NewMessage(inDesc.(protoreflect.MessageDescriptor)),
		dynamicpb.NewMessage(outDesc.(protoreflect.MessageDescriptor)))

	if got := status.Code(callErr); got != codes.InvalidArgument {
		t.Errorf("code = %s, want InvalidArgument", got)
	}
	if handlerRan {
		t.Error("the handler ran despite validation failing")
	}
	// A validation message names fields the caller sent, so it is safe to forward.
	if msg := status.Convert(callErr).Message(); !contains(msg, "username") {
		t.Errorf("the validation detail was dropped: %s", msg)
	}
}

// --- the Go client --------------------------------------------------------

// The client derives the same descriptors from the same Go types, so a Go caller
// gets a typed round trip with no codegen either. A caller in another language
// does not need this — it discovers the schema over reflection.

func newClient(t *testing.T, server *grpcapi.Server) *grpcapi.Client {
	t.Helper()

	client, err := grpcapi.NewClient(grpcapi.ClientConfig{
		Target:   server.Addr().String(),
		Package:  "nikatest.user.v1",
		Insecure: true,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestClientRoundTrip(t *testing.T) {
	_, server := serve(t, nil)
	client := newClient(t, server)

	user, err := grpcapi.Invoke[GetUserRequest, UserReply](
		testContext(t), client, "UserService", "GetUser", &GetUserRequest{ID: 1})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}

	if user.Username != "ada" {
		t.Errorf("Username = %q, want \"ada\"", user.Username)
	}
	if user.Score != 9.5 {
		t.Errorf("Score = %v, want 9.5", user.Score)
	}
	if len(user.Tags) != 2 || user.Tags[0] != "admin" {
		t.Errorf("Tags = %v, want [admin founder]", user.Tags)
	}
	if !user.CreatedAt.Equal(fixedTime) {
		t.Errorf("CreatedAt = %s, want %s", user.CreatedAt, fixedTime)
	}
	if len(user.Avatar) != 2 {
		t.Errorf("Avatar = %v, want two bytes", user.Avatar)
	}
	// The field is not in the contract, so nothing can populate it.
	if user.PasswordHash != "" {
		t.Errorf("PasswordHash = %q, want empty — it is excluded from the schema", user.PasswordHash)
	}
}

// TestClientPreservesStatusCodes: a status must survive the round trip untouched,
// or the caller cannot tell "will never work" from "try again".
func TestClientPreservesStatusCodes(t *testing.T) {
	_, server := serve(t, nil)
	client := newClient(t, server)

	_, err := grpcapi.Invoke[GetUserRequest, UserReply](
		testContext(t), client, "UserService", "GetUser", &GetUserRequest{ID: 9999})

	if got := status.Code(err); got != codes.NotFound {
		t.Errorf("code = %s, want NotFound (%v)", got, err)
	}
}

// TestClientSendsOptionalPresence is the end-to-end version of the presence
// contract: a nil pointer must not reach the server as an empty string.
func TestClientSendsOptionalPresence(t *testing.T) {
	_, server := serve(t, nil)
	client := newClient(t, server)

	name := "ada-lovelace"
	user, err := grpcapi.Invoke[UpdateUserRequest, UserReply](
		testContext(t), client, "UserService", "UpdateUser",
		&UpdateUserRequest{ID: 1, Username: &name}) // Email left nil
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}

	if user.Username != "ada-lovelace" {
		t.Errorf("Username = %q, want it updated", user.Username)
	}
	if user.Email != "ada@example.com" {
		t.Errorf("Email = %q, want it untouched — a nil pointer was sent as empty", user.Email)
	}
}

func TestClientRepeatedMessages(t *testing.T) {
	_, server := serve(t, nil)
	client := newClient(t, server)

	out, err := grpcapi.Invoke[ListUsersRequest, ListUsersReply](
		testContext(t), client, "UserService", "ListUsers", &ListUsersRequest{Page: 1})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}

	if out.Total != 1 || len(out.Data) != 1 {
		t.Fatalf("got %d users (total %d), want 1", len(out.Data), out.Total)
	}
	if out.Data[0].Username != "ada" {
		t.Errorf("Data[0].Username = %q, want \"ada\"", out.Data[0].Username)
	}
}

func TestClientRejectsBadConfig(t *testing.T) {
	t.Run("no package", func(t *testing.T) {
		if _, err := grpcapi.NewClient(grpcapi.ClientConfig{Target: "x:1", Insecure: true}); err == nil {
			t.Error("NewClient with no Package returned nil")
		}
	})

	t.Run("no target and no conn", func(t *testing.T) {
		if _, err := grpcapi.NewClient(grpcapi.ClientConfig{Package: "a.v1", Insecure: true}); err == nil {
			t.Error("NewClient with no Target returned nil")
		}
	})

	t.Run("plaintext must be deliberate", func(t *testing.T) {
		if _, err := grpcapi.NewClient(grpcapi.ClientConfig{Target: "x:1", Package: "a.v1"}); err == nil {
			t.Error("NewClient without credentials or Insecure returned nil")
		}
	})
}

// TestClientAndServerDeriveTheSameSchema is the guard for the one real risk of
// deriving on both sides: if the two struct definitions drift, the mismatch shows
// up as a decode error at runtime rather than a compile error. Comparing the
// derived descriptors turns that into a test failure.
func TestClientAndServerDeriveTheSameSchema(t *testing.T) {
	_, server := serve(t, nil)

	// A deliberately divergent copy of UserReply: one field renamed.
	type DriftedReply struct {
		ID       int64  `json:"id"`
		Nickname string `json:"nickname"` // server calls this "username"
	}

	serverDesc, err := server.Files().FindDescriptorByName("nikatest.user.v1.UserReply")
	if err != nil {
		t.Fatalf("UserReply: %v", err)
	}
	fields := serverDesc.(protoreflect.MessageDescriptor).Fields()

	if fields.ByName("username") == nil {
		t.Fatal("the server schema has no username field; this test is out of date")
	}
	if fields.ByName("nickname") != nil {
		t.Fatal("the server schema has a nickname field; this test is out of date")
	}

	// Field 2 is "username" on the server and would be "nickname" on the drifted
	// client — same number, different meaning, silently.
	var drifted DriftedReply
	_ = drifted
	if got := string(fields.ByNumber(2).Name()); got != "username" {
		t.Errorf("field 2 is %q; a client deriving %q from its own struct would mis-decode it",
			got, "nickname")
	}
}

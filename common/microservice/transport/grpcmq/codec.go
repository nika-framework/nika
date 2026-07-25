package grpcmq

import (
	"fmt"

	"google.golang.org/grpc/encoding"
)

// CodecName is the gRPC content-subtype this transport speaks. It appears on the
// wire as "application/grpc+nika-raw", so a proxy or a capture can tell these
// RPCs apart from protobuf ones.
const CodecName = "nika-raw"

// rawMessage is the only message type on this service. It carries the JSON
// envelope, unwrapped.
//
// A named struct rather than a bare []byte because gRPC dispatches the codec on
// the *dynamic type* of the value it is given, so the payload type must be
// unambiguous and impossible to confuse with some other []byte a middleware
// might pass through.
type rawMessage struct {
	body []byte
}

// rawCodec passes bytes through untouched.
//
// This is what removes protoc from the build. gRPC does not care what the message
// bytes mean; it needs a codec that can turn a Go value into bytes and back, and
// selects one by content-subtype. Registering one that does nothing means the
// payload on the wire is exactly the JSON envelope, inside a normal gRPC frame,
// with normal gRPC headers, deadlines, status codes and interceptors.
//
// The trade-off against real protobuf is deliberate and worth being explicit
// about:
//
//   - Lost: a schema, generated clients in other languages, field-level
//     compatibility checking, and protobuf's compactness.
//   - Gained: no codegen step, no .pb.go files to keep in sync with the envelope
//     definition, one wire format across every transport in this framework (the
//     same bytes travel over Redis, NATS, Kafka and AMQP), and a payload you can
//     read in a packet capture.
//
// This transport carries an internal, already-JSON envelope between Go services
// that share the microservice package, so the schema and codegen benefits have no
// one to serve. A public, polyglot API is the case where protobuf wins and this
// approach should not be used.
//
// The alternative — hand-marshalling a single protobuf `bytes` field with
// protowire — would add a length-prefix header to every message and buy nothing,
// since without a .proto file there is still no schema for anyone to generate
// from. The registered codec is simpler, so that is the route taken.
type rawCodec struct{}

// Compile-time proof we satisfy the interface gRPC looks up by name.
var _ encoding.Codec = rawCodec{}

func (rawCodec) Name() string { return CodecName }

// Marshal returns the payload as-is. gRPC wraps the slice in a non-pooled buffer
// whose Free is a no-op, so handing over the caller's backing array is safe.
func (rawCodec) Marshal(v any) ([]byte, error) {
	switch msg := v.(type) {
	case *rawMessage:
		if msg == nil {
			return nil, nil
		}
		return msg.body, nil
	case rawMessage:
		return msg.body, nil
	default:
		return nil, fmt.Errorf("grpcmq: codec %q cannot marshal %T", CodecName, v)
	}
}

// Unmarshal copies the frame into the message.
//
// The copy is required by the codec contract: gRPC frees the buffer as soon as
// Unmarshal returns, so retaining the slice would hand the decoder memory that
// may already have been recycled into another RPC. It is one memcpy against a
// JSON decode, which is not where the time goes.
func (rawCodec) Unmarshal(data []byte, v any) error {
	msg, ok := v.(*rawMessage)
	if !ok {
		return fmt.Errorf("grpcmq: codec %q cannot unmarshal into %T", CodecName, v)
	}
	if len(data) == 0 {
		// Distinguish "an empty message arrived" from "no message arrived": an
		// empty, non-nil body is a legitimate frame — it is what a fire-and-forget
		// Publish gets back.
		msg.body = []byte{}
		return nil
	}
	msg.body = make([]byte, len(data))
	copy(msg.body, data)
	return nil
}

// init registers the codec globally. encoding.RegisterCodec is documented as
// initialization-time only and not thread-safe, which is exactly what an init
// function guarantees.
func init() { encoding.RegisterCodec(rawCodec{}) }

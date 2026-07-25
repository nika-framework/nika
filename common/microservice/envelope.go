package microservice

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

// Envelope is the wire format every transport carries. Keeping one format across
// Redis, NATS, RabbitMQ, Kafka, gRPC and raw TCP is what lets a handler be moved
// between transports by editing a struct tag and nothing else.
type Envelope struct {
	// ID correlates a reply with its request. Always set by the client.
	ID string `json:"id"`

	// Pattern is the literal subject the client is addressing.
	Pattern string `json:"pattern"`

	// Data is the payload, kept raw so it is decoded exactly once — by the
	// handler's own DTO binding — rather than into map[string]any and back.
	Data json.RawMessage `json:"data,omitempty"`

	// Headers carry application metadata (tenant, trace id, auth token).
	Headers map[string]string `json:"headers,omitempty"`

	// ReplyTo is the transport-specific address a reply should be sent to. It is
	// empty for fire-and-forget events.
	ReplyTo string `json:"replyTo,omitempty"`

	// Status mirrors the HTTP status the handler produced, so a caller can tell
	// "not found" from "invalid payload" without parsing the body.
	Status int `json:"status,omitempty"`

	// Error is set on a reply when the handler failed.
	Error *EnvelopeError `json:"error,omitempty"`

	// SentAt lets a consumer drop messages that sat in a queue past their
	// usefulness, and makes end-to-end latency measurable.
	SentAt time.Time `json:"sentAt,omitempty"`
}

// EnvelopeError is a transport-level failure description.
type EnvelopeError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Details any    `json:"details,omitempty"`
}

func (e *EnvelopeError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("microservice: %s (code %d)", e.Message, e.Code)
}

// maxEnvelopeBytes caps a decoded envelope. Transports hand us bytes straight
// off the network, and an unbounded payload from an untrusted publisher is a
// trivial memory-exhaustion attack.
const maxEnvelopeBytes = 8 << 20 // 8 MiB

// NewEnvelope builds a request envelope, JSON-encoding payload unless it is
// already raw bytes or a json.RawMessage.
func NewEnvelope(pattern string, payload any) (*Envelope, error) {
	data, err := encodePayload(payload)
	if err != nil {
		return nil, err
	}

	return &Envelope{
		ID:      NewID(),
		Pattern: pattern,
		Data:    data,
		SentAt:  time.Now().UTC(),
	}, nil
}

// encodePayload turns a caller-supplied payload into JSON. Passing through
// pre-encoded bytes avoids a decode/encode round trip when relaying.
func encodePayload(payload any) (json.RawMessage, error) {
	switch v := payload.(type) {
	case nil:
		return nil, nil
	case json.RawMessage:
		return v, nil
	case []byte:
		// Treat valid JSON as already encoded; anything else is a byte blob and
		// must be encoded as a JSON string so the envelope stays valid JSON.
		if json.Valid(v) {
			return json.RawMessage(v), nil
		}
		return json.Marshal(v)
	case string:
		return json.Marshal(v)
	default:
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("microservice: cannot encode payload: %w", err)
		}
		return data, nil
	}
}

// Encode serialises the envelope for the wire.
func (e *Envelope) Encode() ([]byte, error) {
	return json.Marshal(e)
}

// DecodeEnvelope parses bytes from the wire, rejecting oversized frames before
// they are unmarshalled.
func DecodeEnvelope(raw []byte) (*Envelope, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("microservice: empty message")
	}
	if len(raw) > maxEnvelopeBytes {
		return nil, fmt.Errorf("microservice: message of %d bytes exceeds the %d byte limit", len(raw), maxEnvelopeBytes)
	}

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("microservice: malformed envelope: %w", err)
	}
	if env.Pattern == "" {
		return nil, fmt.Errorf("microservice: envelope has no pattern")
	}
	if err := Pattern(env.Pattern).Validate(); err != nil {
		return nil, fmt.Errorf("microservice: %w", err)
	}
	return &env, nil
}

// Bind decodes the envelope payload into out.
func (e *Envelope) Bind(out any) error {
	if len(e.Data) == 0 {
		return fmt.Errorf("microservice: envelope has no payload")
	}
	return json.Unmarshal(e.Data, out)
}

// Header returns a header value, or "" when unset.
func (e *Envelope) Header(key string) string {
	if e.Headers == nil {
		return ""
	}
	return e.Headers[key]
}

// WithHeader sets a header and returns the envelope for chaining.
func (e *Envelope) WithHeader(key, value string) *Envelope {
	if e.Headers == nil {
		e.Headers = make(map[string]string, 4)
	}
	e.Headers[key] = value
	return e
}

// NewID returns a 128-bit random hex identifier used for request/reply
// correlation. It must be unguessable: on transports where replies travel over a
// shared channel, a predictable id would let one client harvest another's reply.
func NewID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// crypto/rand failing is unrecoverable for a correlation id whose whole
		// job is uniqueness — better to fail loudly than to collide silently.
		panic("microservice: crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(buf[:])
}

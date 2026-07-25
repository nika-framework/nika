package microservice

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNewEnvelopeEncodesPayloads(t *testing.T) {
	tests := []struct {
		name    string
		payload any
		want    string
	}{
		{name: "nil", payload: nil, want: ""},
		{name: "struct", payload: struct {
			Name string `json:"name"`
		}{Name: "Ada"}, want: `{"name":"Ada"}`},
		{name: "map", payload: map[string]int{"n": 1}, want: `{"n":1}`},
		{name: "slice", payload: []string{"a", "b"}, want: `["a","b"]`},
		// A pre-encoded payload is passed through rather than re-encoded, which
		// avoids a decode/encode round trip when relaying.
		{name: "raw json", payload: json.RawMessage(`{"raw":true}`), want: `{"raw":true}`},
		{name: "json bytes", payload: []byte(`{"b":1}`), want: `{"b":1}`},
		// A plain string must become a JSON string, not be spliced in raw, or the
		// envelope would stop being valid JSON.
		{name: "string", payload: "hello", want: `"hello"`},
		{name: "number", payload: 42, want: `42`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env, err := NewEnvelope("user_created", test.payload)
			if err != nil {
				t.Fatalf("NewEnvelope returned %v", err)
			}
			if got := string(env.Data); got != test.want {
				t.Errorf("Data = %s, want %s", got, test.want)
			}
			if env.ID == "" {
				t.Error("the envelope has no correlation id")
			}
			if env.Pattern != "user_created" {
				t.Errorf("Pattern = %q, want \"user_created\"", env.Pattern)
			}
			if env.SentAt.IsZero() {
				t.Error("SentAt is zero, so end-to-end latency cannot be measured")
			}
		})
	}
}

func TestNonJSONBytesAreEncodedAsAString(t *testing.T) {
	// Arbitrary bytes must not be spliced into the envelope as-is: invalid UTF-8
	// would produce an envelope that fails to parse at the other end.
	env, err := NewEnvelope("blob", []byte{0xff, 0xfe, 0x00})
	if err != nil {
		t.Fatalf("NewEnvelope returned %v", err)
	}

	encoded, err := env.Encode()
	if err != nil {
		t.Fatalf("Encode returned %v", err)
	}
	if !json.Valid(encoded) {
		t.Errorf("the encoded envelope is not valid JSON: %s", encoded)
	}
}

// TestEnvelopeIDsAreUnguessable matters on transports where replies share a
// channel: a predictable id would let one client harvest another's reply.
func TestEnvelopeIDsAreUnguessable(t *testing.T) {
	seen := make(map[string]struct{}, 1000)
	for i := 0; i < 1000; i++ {
		id := NewID()
		if len(id) != 32 {
			t.Fatalf("NewID() = %q, want 32 hex characters (128 bits)", id)
		}
		if _, duplicate := seen[id]; duplicate {
			t.Fatalf("NewID() produced the duplicate %q after %d draws", id, i)
		}
		seen[id] = struct{}{}
	}
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	original, err := NewEnvelope("user_created", map[string]string{"name": "Ada"})
	if err != nil {
		t.Fatalf("NewEnvelope returned %v", err)
	}
	original.WithHeader("X-Tenant", "acme")
	original.ReplyTo = "reply:abc"

	encoded, err := original.Encode()
	if err != nil {
		t.Fatalf("Encode returned %v", err)
	}

	decoded, err := DecodeEnvelope(encoded)
	if err != nil {
		t.Fatalf("DecodeEnvelope returned %v", err)
	}

	if decoded.ID != original.ID {
		t.Errorf("ID = %q, want %q", decoded.ID, original.ID)
	}
	if decoded.Pattern != original.Pattern {
		t.Errorf("Pattern = %q, want %q", decoded.Pattern, original.Pattern)
	}
	if decoded.ReplyTo != original.ReplyTo {
		t.Errorf("ReplyTo = %q, want %q", decoded.ReplyTo, original.ReplyTo)
	}
	if decoded.Header("X-Tenant") != "acme" {
		t.Errorf("X-Tenant = %q, want \"acme\"", decoded.Header("X-Tenant"))
	}

	var payload map[string]string
	if err := decoded.Bind(&payload); err != nil {
		t.Fatalf("Bind returned %v", err)
	}
	if payload["name"] != "Ada" {
		t.Errorf("payload = %v, want name=Ada", payload)
	}
}

func TestDecodeEnvelopeRejectsBadInput(t *testing.T) {
	tests := []struct {
		name string
		raw  []byte
	}{
		{name: "empty", raw: nil},
		{name: "not json", raw: []byte("not json at all")},
		{name: "truncated", raw: []byte(`{"pattern":`)},
		// A pattern is required: without it there is nothing to route on, and a
		// zero-value pattern would fall through to whichever catch-all exists.
		{name: "no pattern", raw: []byte(`{"id":"abc","data":{}}`)},
		{name: "empty pattern", raw: []byte(`{"id":"abc","pattern":""}`)},
		{name: "pattern with a newline", raw: []byte(`{"id":"a","pattern":"user\ncreated"}`)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := DecodeEnvelope(test.raw); err == nil {
				t.Errorf("DecodeEnvelope(%q) = nil, want an error", test.raw)
			}
		})
	}
}

// TestDecodeEnvelopeRejectsOversizedFrames is the memory-exhaustion guard:
// transports hand us bytes straight off the network, and unmarshalling an
// unbounded payload from an untrusted publisher is a trivial DoS.
func TestDecodeEnvelopeRejectsOversizedFrames(t *testing.T) {
	oversized := []byte(`{"pattern":"x","data":"` + strings.Repeat("a", maxEnvelopeBytes) + `"}`)

	_, err := DecodeEnvelope(oversized)
	if err == nil {
		t.Fatal("DecodeEnvelope accepted an oversized frame")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("error = %v, want it to name the size limit", err)
	}
}

func TestBindRejectsAnEmptyPayload(t *testing.T) {
	env := &Envelope{Pattern: "user_created"}

	var out map[string]string
	if err := env.Bind(&out); err == nil {
		t.Error("Bind on an empty payload returned nil, want an error")
	}
}

func TestEnvelopeErrorImplementsError(t *testing.T) {
	var err error = &EnvelopeError{Code: 404, Message: "NOT_FOUND"}

	if !strings.Contains(err.Error(), "NOT_FOUND") {
		t.Errorf("Error() = %q, want it to include the message", err.Error())
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("Error() = %q, want it to include the code", err.Error())
	}

	// A nil *EnvelopeError must not panic when something logs it.
	var nilErr *EnvelopeError
	if nilErr.Error() != "" {
		t.Errorf("(*EnvelopeError)(nil).Error() = %q, want \"\"", nilErr.Error())
	}
}

func TestWithHeaderIsChainable(t *testing.T) {
	env := (&Envelope{Pattern: "x"}).
		WithHeader("A", "1").
		WithHeader("B", "2")

	if env.Header("A") != "1" || env.Header("B") != "2" {
		t.Errorf("headers = %v, want A=1 B=2", env.Headers)
	}
	if env.Header("missing") != "" {
		t.Errorf("Header(\"missing\") = %q, want \"\"", env.Header("missing"))
	}
}

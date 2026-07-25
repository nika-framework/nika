package microservice

import (
	"strings"
	"testing"
	"time"
)

// FuzzPatternMatch fuzzes the matcher because both of its inputs cross a trust
// boundary: the subject arrives from the network on every message, and the
// pattern arrives from a struct tag that may itself be generated.
//
// The properties asserted are the ones a matcher must never violate regardless
// of input: it terminates, it does not panic, and a literal pattern matches
// exactly itself.
func FuzzPatternMatch(f *testing.F) {
	seeds := []struct{ pattern, subject string }{
		{"user_created", "user_created"},
		{"user_*", "user_23"},
		{"user_?", "user_1"},
		{"*", ""},
		{"", ""},
		{"**", "abc"},
		{"*a*b*c*", "xaybzc"},
		{strings.Repeat("*a", 16), strings.Repeat("a", 32)},
		{"a?*b", "axxb"},
		{"\x00", "\x00"},
		{"日本_*", "日本_値"},
	}
	for _, seed := range seeds {
		f.Add(seed.pattern, seed.subject)
	}

	f.Fuzz(func(t *testing.T, pattern, subject string) {
		// Bound the work: the fuzzer will happily generate megabyte strings, and
		// the point here is correctness, not throughput.
		if len(pattern) > 1024 || len(subject) > 1024 {
			t.Skip()
		}

		done := make(chan bool, 1)
		go func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					done <- false
					t.Errorf("Pattern(%q).Match(%q) panicked: %v", pattern, subject, recovered)
				}
			}()
			done <- Pattern(pattern).Match(subject)
		}()

		var matched bool
		select {
		case matched = <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("Pattern(%q).Match(%q) did not terminate", pattern, subject)
		}

		// A pattern with no metacharacters is a literal, so matching must be
		// exact string equality — nothing about wildcard handling may leak into it.
		if !strings.ContainsAny(pattern, wildcardChars) {
			if want := pattern == subject; matched != want {
				t.Errorf("literal Pattern(%q).Match(%q) = %v, want %v", pattern, subject, matched, want)
			}
		}

		// A pattern always matches itself when it has no metacharacters, and a
		// lone star always matches everything.
		if pattern == "*" && !matched {
			t.Errorf("Pattern(\"*\") did not match %q", subject)
		}
	})
}

// FuzzDecodeEnvelope fuzzes the wire decoder, which is the framework's widest
// attack surface: every transport hands it bytes straight off the network.
//
// The contract is that it either returns a usable envelope or an error — never a
// panic, and never a partially-populated envelope that a later stage would
// dereference.
func FuzzDecodeEnvelope(f *testing.F) {
	f.Add([]byte(`{"id":"a","pattern":"user_created","data":{"name":"Ada"}}`))
	f.Add([]byte(`{"pattern":"user_*"}`))
	f.Add([]byte(`{"pattern":"x","headers":{"A":"1"},"replyTo":"r","status":200}`))
	f.Add([]byte(`{"pattern":"x","data":null}`))
	f.Add([]byte(`{"pattern":"x","error":{"code":404,"message":"NOT_FOUND"}}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`null`))
	f.Add([]byte(`[]`))
	f.Add([]byte(``))
	f.Add([]byte(`{"pattern":`))
	f.Add([]byte("\x00\x01\x02"))

	f.Fuzz(func(t *testing.T, raw []byte) {
		env, err := DecodeEnvelope(raw)

		if err != nil {
			if env != nil {
				t.Errorf("DecodeEnvelope returned both an envelope and the error %v", err)
			}
			return
		}

		if env == nil {
			t.Fatal("DecodeEnvelope returned a nil envelope and a nil error")
		}
		// A decoded envelope must always be routable: the pattern is what every
		// downstream stage keys on, so an empty one would fall through to
		// whichever catch-all happened to be registered.
		if env.Pattern == "" {
			t.Error("DecodeEnvelope accepted an envelope with no pattern")
		}
		if err := Pattern(env.Pattern).Validate(); err != nil {
			t.Errorf("DecodeEnvelope accepted the invalid pattern %q: %v", env.Pattern, err)
		}

		// A successfully decoded envelope must round-trip, or a relaying service
		// would silently corrupt messages it forwards.
		encoded, err := env.Encode()
		if err != nil {
			t.Fatalf("Encode of a decoded envelope failed: %v", err)
		}
		again, err := DecodeEnvelope(encoded)
		if err != nil {
			t.Fatalf("re-decoding a re-encoded envelope failed: %v", err)
		}
		if again.Pattern != env.Pattern || again.ID != env.ID {
			t.Errorf("round trip changed the envelope: %q/%q became %q/%q",
				env.ID, env.Pattern, again.ID, again.Pattern)
		}
	})
}

// FuzzPatternValidate pins the guarantee the rest of the framework relies on:
// anything Validate accepts is safe to put on a wire, into a broker subject and
// into a log line. A control character slipping through here would corrupt a
// broker's own framing or forge a log entry.
func FuzzPatternValidate(f *testing.F) {
	f.Add("user_created")
	f.Add("user_*")
	f.Add("")
	f.Add(" leading")
	f.Add("trailing ")
	f.Add("with\nnewline")
	f.Add("with\x00null")
	f.Add(strings.Repeat("a", maxPatternLen+1))

	f.Fuzz(func(t *testing.T, pattern string) {
		err := Pattern(pattern).Validate()
		if err != nil {
			return
		}

		// Anything Validate accepts must be safe to put on a wire and into a log.
		if pattern == "" {
			t.Error("Validate accepted an empty pattern")
		}
		if len(pattern) > maxPatternLen {
			t.Errorf("Validate accepted a %d-character pattern, over the %d limit",
				len(pattern), maxPatternLen)
		}
		if strings.ContainsAny(pattern, "\x00\r\n") {
			t.Errorf("Validate accepted the control character in %q", pattern)
		}
		if strings.TrimSpace(pattern) != pattern {
			t.Errorf("Validate accepted %q, which has surrounding whitespace", pattern)
		}
	})
}

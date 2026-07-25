package microservice

import (
	"strings"
	"testing"
)

func TestPatternMatch(t *testing.T) {
	tests := []struct {
		pattern string
		subject string
		want    bool
	}{
		// Literal patterns.
		{"user_created", "user_created", true},
		{"user_created", "user_updated", false},
		{"user_created", "user_created_at", false},
		{"user_created", "USER_CREATED", false}, // matching is case-sensitive
		{"", "", true},
		{"a", "", false},
		{"", "a", false},

		// The scenario the framework is built around.
		{"user_*", "user_23", true},
		{"user_*", "user_created", true},
		{"user_*", "user_", true}, // a trailing star may match nothing
		{"user_*", "user", false},
		{"user_*", "users", false},
		{"user_*", "admin_23", false},

		// Stars anywhere.
		{"*", "anything", true},
		{"*", "", true},
		{"*_created", "user_created", true},
		{"*_created", "created", false},
		{"user_*_v2", "user_23_v2", true},
		{"user_*_v2", "user__v2", true},
		{"user_*_v2", "user_23_v3", false},
		{"**", "abc", true},
		{"a**b", "ab", true},
		{"a**b", "axxxb", true},

		// Single-character wildcard.
		{"user_?", "user_1", true},
		{"user_?", "user_23", false},
		{"user_?", "user_", false},
		{"user_??", "user_23", true},
		{"?", "a", true},
		{"?", "", false},

		// Mixed.
		{"user_?_*", "user_1_created", true},
		{"user_?_*", "user_12_created", false},

		// Backtracking cases that a naive matcher gets wrong.
		{"*a", "aaa", true},
		{"*a*b", "xaybzb", true},
		{"a*b*c", "abc", true},
		{"a*b*c", "aXbYc", true},
		{"a*b*c", "aXbY", false},
		{"*abc*", "xxabcyy", true},
		{"*abc*", "xxabyy", false},
	}

	for _, test := range tests {
		t.Run(test.pattern+"→"+test.subject, func(t *testing.T) {
			if got := Pattern(test.pattern).Match(test.subject); got != test.want {
				t.Errorf("Pattern(%q).Match(%q) = %v, want %v",
					test.pattern, test.subject, got, test.want)
			}
		})
	}
}

// TestPatternMatchIsNotExponential guards the property that made the matcher
// hand-rolled rather than regexp-translated: subjects arrive from the network, so
// a pathological input must not blow up. A translated regexp with nested stars
// backtracks exponentially on exactly this shape.
func TestPatternMatchIsNotExponential(t *testing.T) {
	pattern := Pattern(strings.Repeat("*a", 24))
	subject := strings.Repeat("a", 40) + "b" // forces maximum backtracking, then fails

	done := make(chan bool, 1)
	go func() { done <- pattern.Match(subject) }()

	select {
	case got := <-done:
		if got {
			t.Errorf("Match returned true for a non-matching subject")
		}
	case <-timeoutAfterSeconds(5):
		t.Fatal("Match did not finish within 5s on an adversarial input")
	}
}

func TestPatternIsWildcard(t *testing.T) {
	tests := []struct {
		pattern string
		want    bool
	}{
		{"user_created", false},
		{"user_*", true},
		{"user_?", true},
		{"*", true},
		{"", false},
		{"a.b.c", false},
	}

	for _, test := range tests {
		if got := Pattern(test.pattern).IsWildcard(); got != test.want {
			t.Errorf("Pattern(%q).IsWildcard() = %v, want %v", test.pattern, got, test.want)
		}
	}
}

func TestPatternValidate(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		wantErr bool
	}{
		{name: "literal", pattern: "user_created"},
		{name: "wildcard", pattern: "user_*"},
		{name: "dotted", pattern: "user.created"},
		{name: "empty is rejected", pattern: "", wantErr: true},
		{name: "leading space is rejected", pattern: " user", wantErr: true},
		{name: "trailing space is rejected", pattern: "user ", wantErr: true},
		// A pattern crosses the network in every envelope; a control character
		// would corrupt a broker's own framing or a log line.
		{name: "newline is rejected", pattern: "user\ncreated", wantErr: true},
		{name: "null byte is rejected", pattern: "user\x00", wantErr: true},
		{name: "over-long is rejected", pattern: strings.Repeat("a", maxPatternLen+1), wantErr: true},
		{name: "at the length limit", pattern: strings.Repeat("a", maxPatternLen)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := Pattern(test.pattern).Validate()
			if test.wantErr && err == nil {
				t.Errorf("Pattern(%q).Validate() = nil, want an error", test.pattern)
			}
			if !test.wantErr && err != nil {
				t.Errorf("Pattern(%q).Validate() = %v, want nil", test.pattern, err)
			}
		})
	}
}

// TestSpecificityOrdering pins the precedence rule that decides which handler
// serves a subject two patterns both match.
func TestSpecificityOrdering(t *testing.T) {
	tests := []struct {
		name       string
		more, less string
	}{
		{name: "exact beats wildcard", more: "user_created", less: "user_*"},
		{name: "exact beats catch-all", more: "users", less: "*"},
		{name: "more literals win", more: "user_created_*", less: "user_*"},
		{name: "fewer wildcards win on a literal tie", more: "user_*", less: "us*r_*"},
		{name: "single-char wildcard is not privileged over star", more: "user_created", less: "user_?"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			more, less := rank(Pattern(test.more)), rank(Pattern(test.less))

			if !more.moreSpecificThan(less) {
				t.Errorf("%q should be more specific than %q", test.more, test.less)
			}
			if less.moreSpecificThan(more) {
				t.Errorf("%q should not be more specific than %q", test.less, test.more)
			}
		})
	}
}

// TestSpecificityIsTotalAndDeterministic guarantees that registration order
// never changes which handler wins.
func TestSpecificityIsTotalAndDeterministic(t *testing.T) {
	patterns := []Pattern{"user_*", "user_a*", "b*", "*", "user_created"}

	for _, a := range patterns {
		for _, b := range patterns {
			rankA, rankB := rank(a), rank(b)
			if a == b {
				if rankA.moreSpecificThan(rankB) {
					t.Errorf("%q is reported more specific than itself", a)
				}
				continue
			}
			// Exactly one direction must hold, or a sort of the wildcard list
			// would be unstable and dispatch would vary between runs.
			forward := rankA.moreSpecificThan(rankB)
			backward := rankB.moreSpecificThan(rankA)
			if forward == backward {
				t.Errorf("%q vs %q: ordering is not total (both=%v)", a, b, forward)
			}
		}
	}
}

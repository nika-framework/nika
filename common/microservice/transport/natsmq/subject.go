package natsmq

import (
	"fmt"
	"strings"

	"github.com/nika-framework/nika/common/microservice"
)

// catchAllToken is the NATS "everything below here" wildcard.
const catchAllToken = ">"

// natsIllegalChars are the characters a microservice.Pattern may not contain when
// it has to become a NATS subject token.
//
//	"."  is the token separator, so a pattern containing it would silently become
//	     two tokens and change what the broker matches.
//	">"  and "*" are NATS wildcards; a literal one in a pattern would turn an exact
//	     subscription into a wildcard subscription.
//	" "  and "\t" terminate a subject in the NATS protocol's line format.
const natsIllegalChars = ".> \t"

// validatePattern rejects patterns that cannot be expressed as a NATS subject.
//
// Failing here, at Listen time, is the point: the alternative is a subscription
// that the server happily accepts and that never matches anything, which presents
// as "my handler is never called" with nothing in any log.
func validatePattern(pattern string) error {
	if err := microservice.Pattern(pattern).Validate(); err != nil {
		return fmt.Errorf("natsmq: %w", err)
	}

	for _, c := range natsIllegalChars {
		if c == '*' {
			continue
		}
		if strings.ContainsRune(pattern, c) {
			return fmt.Errorf(
				"natsmq: pattern %q contains %q, which NATS reserves — use only characters legal in a single subject token",
				pattern, string(c))
		}
	}
	return nil
}

// subjectPlan decides what a set of handler patterns must subscribe to.
//
// This is the subtle part of the transport, because the two wildcard languages do
// not line up. NATS wildcards are token based: `*` matches exactly one
// dot-separated token and `>` matches all remaining tokens.
// microservice.Pattern's `*` is character based — it matches any run of characters,
// underscores included. So `user_*` is not a NATS wildcard at all: `user_created`
// is one single NATS token, and a subscription to `prefix.user_*` matches the
// literal three-character-suffix subject and nothing else.
//
// The resolution:
//
//   - Literal patterns map one-to-one onto exact subjects, and the broker filters.
//   - A wildcard pattern cannot be expressed as a subject, so the process instead
//     subscribes once to `prefix.>` and lets the core Router do character-level
//     matching locally. That means receiving every message published under the
//     prefix and discarding most of them — the unavoidable cost of character-level
//     wildcards on a token-based broker, and a good reason to prefer literal
//     patterns on this transport.
//   - When the catch-all is used, the literal subjects are deliberately *not* also
//     subscribed. `>` already covers them, and NATS delivers a message once per
//     matching subscription, so subscribing to both would run every literal
//     message's handler twice.
//
// subjects are returned unprefixed; the caller joins them to the prefix.
func subjectPlan(patterns []string) (subjects []string, catchAll bool, err error) {
	if len(patterns) == 0 {
		return nil, false, fmt.Errorf("natsmq: no patterns to subscribe to")
	}

	seen := make(map[string]struct{}, len(patterns))
	for _, pattern := range patterns {
		if err := validatePattern(pattern); err != nil {
			return nil, false, err
		}
		if microservice.Pattern(pattern).IsWildcard() {
			catchAll = true
			continue
		}
		if _, dup := seen[pattern]; dup {
			continue
		}
		seen[pattern] = struct{}{}
		subjects = append(subjects, pattern)
	}

	if catchAll {
		// One subscription that already covers every literal; see above.
		return nil, true, nil
	}
	if len(subjects) == 0 {
		return nil, false, fmt.Errorf("natsmq: no patterns to subscribe to")
	}
	return subjects, false, nil
}

// subjectFor is the NATS subject a literal pattern is published on. The prefix
// keeps unrelated services from colliding on a shared cluster, which matters more
// on NATS than elsewhere because a subject namespace is completely flat.
func subjectFor(prefix, pattern string) (string, error) {
	if microservice.Pattern(pattern).IsWildcard() {
		return "", fmt.Errorf("natsmq: cannot publish to wildcard pattern %q — send a literal subject", pattern)
	}
	if err := validatePattern(pattern); err != nil {
		return "", err
	}
	return prefix + "." + pattern, nil
}

// joinSubject prefixes an already-validated literal pattern. subjectPlan validates
// everything it returns, so this cannot fail.
func joinSubject(prefix, pattern string) string {
	return prefix + "." + pattern
}

// catchAllSubject is the subscription used when any wildcard pattern is registered.
func catchAllSubject(prefix string) string {
	return prefix + "." + catchAllToken
}

// validatePrefix rejects a prefix that is not a usable subject namespace.
func validatePrefix(prefix string) error {
	if prefix == "" {
		return fmt.Errorf("natsmq: prefix cannot be empty")
	}
	if strings.ContainsAny(prefix, "> \t\r\n") || strings.Contains(prefix, "*") {
		return fmt.Errorf("natsmq: prefix %q contains a character NATS reserves", prefix)
	}
	if strings.HasPrefix(prefix, ".") || strings.HasSuffix(prefix, ".") || strings.Contains(prefix, "..") {
		return fmt.Errorf("natsmq: prefix %q has an empty subject token", prefix)
	}
	return nil
}

package redismq

import (
	"fmt"
	"strings"

	"github.com/nika-framework/nika/common/microservice"
)

// replyNamespace is the channel segment reserved for request/reply inboxes. A
// handler pattern may not start with it, so no handler subscription can ever
// straddle another client's reply inbox.
const replyNamespace = "reply:"

// toRedisPattern translates a microservice.Pattern into a Redis glob pattern.
//
// The two languages overlap but are not identical. Both use `*` for "any run of
// characters" and `?` for "exactly one character", which is why PSUBSCRIBE can do
// the filtering for us at all. But Redis additionally treats `[`…`]` as a
// character class and `\` as an escape, while microservice.Pattern treats all
// three as ordinary characters.
//
// Handing an untranslated pattern to PSUBSCRIBE therefore silently changes its
// meaning: `item[1]` would stop matching the literal subject `item[1]` and start
// matching `item1`. Escaping the three Redis-only metacharacters keeps them
// literal, so a pattern means exactly the same thing at the broker as it does in
// microservice.Pattern.Match — which is the invariant the local ownership check in
// plan.accept relies on.
func toRedisPattern(pattern string) string {
	var b strings.Builder
	b.Grow(len(pattern) + 8)

	for i := 0; i < len(pattern); i++ {
		c := pattern[i]
		switch c {
		case '[', ']', '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		default:
			// `*` and `?` pass through deliberately: they are the wildcards we
			// want the broker to interpret.
			b.WriteByte(c)
		}
	}
	return b.String()
}

// escapeGlob escapes every Redis glob metacharacter, wildcards included. It is
// used for the operator-supplied prefix: a prefix is a literal namespace, so a `*`
// in it must not silently turn a subscription into a firehose over every other
// service sharing the instance.
func escapeGlob(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 8)

	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '*', '?', '[', ']', '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// messageChannel is the Redis channel a literal subject is published on.
//
// The prefix lets several services share one Redis instance without colliding,
// which they otherwise would: pub/sub has no vhost, no database scoping (PUBLISH
// is not affected by SELECT) and no namespacing of any kind.
func messageChannel(prefix, pattern string) (string, error) {
	if err := microservice.Pattern(pattern).Validate(); err != nil {
		return "", fmt.Errorf("redismq: %w", err)
	}
	if strings.HasPrefix(pattern, replyNamespace) {
		return "", fmt.Errorf(
			"redismq: pattern %q is reserved: %q is the request/reply namespace",
			pattern, replyNamespace)
	}
	return prefix + ":" + pattern, nil
}

// globSub pairs a PSUBSCRIBE pattern with the untranslated pattern it came from.
// The original is kept so ownership can be resolved locally with the same matcher
// the core Router uses, instead of reimplementing Redis's glob engine in Go.
type globSub struct {
	glob    string
	pattern microservice.Pattern
}

// plan is the subscription set for one Listen call, plus the information needed to
// collapse duplicate deliveries.
type plan struct {
	prefix   string
	channels []string // exact channels for SUBSCRIBE
	globs    []globSub
	literal  map[string]struct{}
}

// newPlan splits handler patterns into the exact channels to SUBSCRIBE and the
// globs to PSUBSCRIBE.
//
// The split matters for cost: PSUBSCRIBE makes Redis evaluate every registered
// glob against every published channel name, whereas SUBSCRIBE is a hash lookup.
// Routing literal subjects through PSUBSCRIBE would pay that per-message matching
// cost for no benefit.
//
// patterns is expected in the core Router's order — exact first, then wildcards
// most specific first — because plan.accept resolves an overlapping delivery to
// the first matching glob, and that has to agree with the handler the Router will
// pick.
func newPlan(prefix string, patterns []string) (*plan, error) {
	if prefix == "" {
		return nil, fmt.Errorf("redismq: prefix cannot be empty")
	}

	p := &plan{prefix: prefix, literal: make(map[string]struct{}, len(patterns))}
	seenGlob := make(map[string]struct{}, len(patterns))
	escapedPrefix := escapeGlob(prefix + ":")

	for _, pattern := range patterns {
		if err := microservice.Pattern(pattern).Validate(); err != nil {
			return nil, fmt.Errorf("redismq: %w", err)
		}
		if strings.HasPrefix(pattern, replyNamespace) {
			return nil, fmt.Errorf(
				"redismq: pattern %q is reserved: %q is the request/reply namespace",
				pattern, replyNamespace)
		}

		if microservice.Pattern(pattern).IsWildcard() {
			glob := escapedPrefix + toRedisPattern(pattern)
			if _, dup := seenGlob[glob]; dup {
				continue
			}
			seenGlob[glob] = struct{}{}
			p.globs = append(p.globs, globSub{glob: glob, pattern: microservice.Pattern(pattern)})
			continue
		}

		channel := prefix + ":" + pattern
		if _, dup := p.literal[channel]; dup {
			continue
		}
		p.literal[channel] = struct{}{}
		p.channels = append(p.channels, channel)
	}

	if len(p.channels) == 0 && len(p.globs) == 0 {
		return nil, fmt.Errorf("redismq: no patterns to subscribe to")
	}
	return p, nil
}

// accept reports whether a delivery should be handled, given the pattern Redis
// attributed it to ("" for a plain SUBSCRIBE delivery) and the channel it arrived
// on.
//
// This is the double-delivery guard, and it is not optional. Redis delivers a
// message once per *matching subscription*: a service registering both
// `user_created` and `user_*` gets the literal message twice — once as `message`
// from SUBSCRIBE and once as `pmessage` from PSUBSCRIBE — and two overlapping
// globs produce two `pmessage`s. Without collapsing them, every such message runs
// its handler twice, which for a non-idempotent handler means duplicate writes,
// duplicate emails, duplicate charges.
//
// Rather than trying to build a non-overlapping subscription set (impossible in
// general with character-level wildcards), each channel is assigned exactly one
// owning subscription and only that delivery is accepted. The exact channel wins
// when there is one, mirroring the Router's "exact beats wildcard" rule;
// otherwise the first matching glob wins, and because patterns arrive in
// specificity order that is the same glob whose handler the Router would choose.
func (p *plan) accept(deliveredPattern, channel string) bool {
	if _, exact := p.literal[channel]; exact {
		return deliveredPattern == ""
	}
	if deliveredPattern == "" {
		// A plain delivery on a channel we never SUBSCRIBEd to should be
		// impossible. Accept rather than drop: losing a message is worse than
		// handling a surprise one.
		return true
	}

	subject, ok := p.subject(channel)
	if !ok {
		return true
	}
	for _, g := range p.globs {
		if g.pattern.Match(subject) {
			return g.glob == deliveredPattern
		}
	}
	return true
}

// subject strips the namespace prefix from a channel name.
func (p *plan) subject(channel string) (string, bool) {
	prefix := p.prefix + ":"
	if !strings.HasPrefix(channel, prefix) {
		return "", false
	}
	return channel[len(prefix):], true
}

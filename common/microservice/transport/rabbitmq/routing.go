package rabbitmq

import (
	"fmt"
	"sort"
	"strings"

	"github.com/nika-framework/nika/common/microservice"
)

// AMQP 0-9-1 caps a routing or binding key at the length of a "short string".
const maxKeyBytes = 255

// A microservice.Pattern and an AMQP topic binding key look similar and are not.
//
//	AMQP topic keys are dot-separated *words*, and its metacharacters occupy a
//	whole word: "*" matches exactly one word and "#" matches zero or more words.
//	A word that merely *contains* a metacharacter — "user_*" — is not a wildcard
//	at all; RabbitMQ compares it literally, so binding "user_*" silently receives
//	nothing except a message literally routed to "user_*".
//
//	microservice.Pattern wildcards are character-level: "user_*" matches
//	"user_23" and "user_created", and "user_?" matches "user_1".
//
// There is therefore no AMQP binding key that expresses "any suffix within one
// word", which is the common case in this framework. The resolution is:
//
//   - a literal pattern binds to its own routing key, so the broker filters and
//     the queue only sees traffic it can handle;
//   - any wildcard pattern forces one catch-all "#" binding and the core Router
//     does the character-level filtering in-process.
//
// The cost of the catch-all is real and worth stating: the queue then receives
// every message published to the exchange, including subjects owned by other
// services sharing it, which the Router answers with ErrNoHandler. Give each
// service its own Exchange (or its own vhost) when that matters.
//
// Note on double bindings: binding the same queue to a topic exchange with both
// "#" and a literal key does NOT duplicate deliveries. The RabbitMQ
// exchange-to-exchange binding documentation states the guarantee for any
// routing topology: "for every queue to which a given message is routed, each
// queue will receive exactly one copy of that message"
// (https://www.rabbitmq.com/docs/e2e). So keeping both bindings would be safe.
// planBindings still drops the literal keys once a catch-all is required,
// because "#" already subsumes them — they are then pure bookkeeping in the
// broker's binding table, not a correctness requirement.

// toRoutingKey maps a literal subject onto the AMQP routing key it is published
// with. Subjects are always literal on the publish side; a wildcard here means
// the caller confused a handler pattern with a subject.
func toRoutingKey(pattern string) (key string, err error) {
	if err := validKey(pattern); err != nil {
		return "", err
	}
	if microservice.Pattern(pattern).IsWildcard() {
		return "", fmt.Errorf(
			"rabbitmq: cannot publish to wildcard subject %q — a published subject must be literal", pattern)
	}
	return pattern, nil
}

// toBindingKey maps a handler pattern onto the binding key that makes it
// reachable. A wildcard pattern collapses to the AMQP catch-all "#" because its
// character-level semantics have no AMQP equivalent; see the note above.
//
// The pattern must already have passed validKey.
func toBindingKey(pattern string) string {
	if microservice.Pattern(pattern).IsWildcard() {
		return "#"
	}
	return pattern
}

// bindingPlan is the set of binding keys a queue needs for a group of patterns.
type bindingPlan struct {
	// Keys are the binding keys to declare, sorted so the plan is deterministic
	// and comparable in tests and logs.
	Keys []string

	// CatchAll reports that at least one pattern was a wildcard and the plan
	// therefore binds "#", making the queue see all exchange traffic.
	CatchAll bool
}

// planBindings turns handler patterns into a binding plan, rejecting patterns
// AMQP cannot carry. It is pure so the translation is unit-testable without a
// broker — the part of an AMQP integration most likely to be wrong is exactly
// the part hardest to observe at runtime, since a bad binding fails by receiving
// nothing rather than by erroring.
func planBindings(patterns []string) (bindingPlan, error) {
	if len(patterns) == 0 {
		return bindingPlan{}, fmt.Errorf("rabbitmq: no patterns to bind")
	}

	var (
		plan bindingPlan
		seen = make(map[string]struct{}, len(patterns))
	)

	for _, pattern := range patterns {
		if err := validKey(pattern); err != nil {
			return bindingPlan{}, err
		}
		key := toBindingKey(pattern)
		if key == "#" {
			plan.CatchAll = true
			continue
		}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		plan.Keys = append(plan.Keys, key)
	}

	if plan.CatchAll {
		// "#" subsumes every literal key, so the literal bindings are redundant.
		plan.Keys = []string{"#"}
		return plan, nil
	}

	sort.Strings(plan.Keys)
	return plan, nil
}

// validKey rejects patterns that cannot be used as an AMQP key without changing
// its meaning.
func validKey(pattern string) error {
	if pattern == "" {
		return fmt.Errorf("rabbitmq: pattern cannot be empty")
	}
	if len(pattern) > maxKeyBytes {
		return fmt.Errorf("rabbitmq: pattern %q is %d bytes, over the %d byte AMQP key limit",
			pattern, len(pattern), maxKeyBytes)
	}
	if strings.ContainsRune(pattern, '.') {
		// A dot is AMQP's word separator, so "user.created" would be two words
		// and would start matching "*" bindings the author never intended.
		return fmt.Errorf(
			"rabbitmq: pattern %q contains '.', which is the AMQP topic word separator — use '_' instead", pattern)
	}
	if strings.ContainsRune(pattern, '#') {
		// "#" is AMQP's multi-word wildcard. Allowing it through would let a
		// pattern quietly subscribe to the whole exchange.
		return fmt.Errorf(
			"rabbitmq: pattern %q contains '#', which is the AMQP multi-word wildcard and is reserved", pattern)
	}
	if strings.ContainsAny(pattern, " \t\r\n\x00") {
		return fmt.Errorf("rabbitmq: pattern %q contains whitespace or a control character", pattern)
	}
	return nil
}

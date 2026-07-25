package microservice

import (
	"fmt"
	"strings"
)

// Pattern is a message subject declared by a handler, optionally containing
// wildcards:
//
//	"user_created"  matches only the literal subject
//	"user_*"        matches "user_23", "user_created", "user_"
//	"user_?"        matches "user_1" but not "user_23"
//
// A subject sent by a client is always literal; wildcards only ever appear on
// the receiving side.
type Pattern string

// wildcardChars are the metacharacters recognised in a Pattern.
const wildcardChars = "*?"

// IsWildcard reports whether p contains any wildcard metacharacter.
func (p Pattern) IsWildcard() bool {
	return strings.ContainsAny(string(p), wildcardChars)
}

// Validate rejects patterns that cannot match anything useful, so a typo in a
// struct tag fails at startup instead of silently never receiving messages.
func (p Pattern) Validate() error {
	s := string(p)
	if s == "" {
		return fmt.Errorf("pattern cannot be empty")
	}
	if len(s) > maxPatternLen {
		return fmt.Errorf("pattern %q is longer than %d characters", s, maxPatternLen)
	}
	if strings.ContainsAny(s, "\x00\r\n") {
		return fmt.Errorf("pattern %q contains a control character", s)
	}
	if strings.TrimSpace(s) != s {
		return fmt.Errorf("pattern %q has leading or trailing whitespace", s)
	}
	return nil
}

// maxPatternLen bounds pattern length. Patterns cross the network as part of
// every envelope, and an unbounded one is an amplification vector.
const maxPatternLen = 512

// Match reports whether subject satisfies the pattern.
//
// The implementation is an iterative backtracking matcher rather than a
// translated regular expression: it allocates nothing, runs in O(len(pattern) ×
// len(subject)) worst case with no catastrophic backtracking, and cannot be
// tricked into a ReDoS by a hostile subject — which matters because subjects
// arrive from the network.
func (p Pattern) Match(subject string) bool {
	pattern := string(p)

	var (
		pi, si         int
		starPi, starSi = -1, -1
	)

	for si < len(subject) {
		switch {
		case pi < len(pattern) && pattern[pi] == '?':
			pi++
			si++

		case pi < len(pattern) && pattern[pi] == '*':
			// Remember this star so we can extend its match on failure.
			starPi, starSi = pi, si
			pi++

		case pi < len(pattern) && pattern[pi] == subject[si]:
			pi++
			si++

		case starPi >= 0:
			// Backtrack: let the last star swallow one more character.
			starSi++
			pi, si = starPi+1, starSi

		default:
			return false
		}
	}

	// Trailing stars in the pattern may match the empty remainder.
	for pi < len(pattern) && pattern[pi] == '*' {
		pi++
	}
	return pi == len(pattern)
}

// specificity ranks two patterns so the most precise handler wins when several
// match the same subject. Without a deterministic ranking, a controller that
// declares both "user_created" and "user_*" would dispatch to whichever the map
// iteration happened to yield first.
//
// The ordering is: exact patterns beat wildcards; among wildcards, more literal
// characters beat fewer; a tie breaks toward fewer wildcards, then
// lexicographically so registration order never matters.
type specificity struct {
	wildcard bool
	literals int
	stars    int
	text     string
}

func rank(p Pattern) specificity {
	s := string(p)
	spec := specificity{text: s}
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '*', '?':
			spec.stars++
			spec.wildcard = true
		default:
			spec.literals++
		}
	}
	return spec
}

// moreSpecificThan reports whether a should be preferred over b.
func (a specificity) moreSpecificThan(b specificity) bool {
	if a.wildcard != b.wildcard {
		return !a.wildcard
	}
	if a.literals != b.literals {
		return a.literals > b.literals
	}
	if a.stars != b.stars {
		return a.stars < b.stars
	}
	return a.text < b.text
}

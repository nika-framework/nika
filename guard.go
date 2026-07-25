package nika

import (
	"fmt"
	"strings"
)

// GuardSpec is one guard invocation parsed from a `guard` tag.
type GuardSpec struct {
	Name string
	Args []string
}

// AddGuard registers a named guard factory usable from `guard` tags.
func (a *App) AddGuard(name string, guardFn GuardFunc) {
	name = strings.TrimSpace(name)
	if name == "" {
		panic("nika: guard name cannot be empty")
	}
	if guardFn == nil {
		panic(fmt.Sprintf("nika: guard %q cannot be nil", name))
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if _, exists := a.guards[name]; exists {
		panic(fmt.Sprintf("nika: guard %q is already registered", name))
	}
	a.guards[name] = guardFn
}

// Guard returns the factory registered under name. It is what lets adjacent
// layers — the microservice server, for one — apply the same guards to
// non-HTTP entry points as the router applies to routes.
func (a *App) Guard(name string) (GuardFunc, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	guardFn, exists := a.guards[name]
	return guardFn, exists
}

// HasGuard reports whether a guard is registered under name.
func (a *App) HasGuard(name string) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	_, exists := a.guards[name]
	return exists
}

// ParseGuardTag parses the contents of a `guard` struct tag into an ordered list
// of guard invocations.
//
// All of these are accepted:
//
//	guard:"Auth"                        → Auth with no arguments
//	guard:"Auth()"                      → same
//	guard:"Roles(admin, editor)"        → Roles with two arguments
//	guard:"Auth Roles(admin)"           → two guards, space separated
//	guard:"Auth,Roles(admin)"           → two guards, comma separated
//	guard:"Scope('user:read')"          → quoted argument keeps its comma/spaces
//
// The parser is hand-rolled rather than a regexp because the previous regexp
// silently dropped bare guard names (`guard:"Auth"` registered nothing, so an
// endpoint meant to be protected was served wide open) and split quoted
// arguments on their internal commas.
func ParseGuardTag(tag string) ([]GuardSpec, error) {
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return nil, nil
	}

	var (
		specs []GuardSpec
		name  strings.Builder
	)

	flushName := func() {
		if trimmed := strings.TrimSpace(name.String()); trimmed != "" {
			specs = append(specs, GuardSpec{Name: trimmed})
		}
		name.Reset()
	}

	for i := 0; i < len(tag); i++ {
		switch c := tag[i]; c {
		case '(':
			guardName := strings.TrimSpace(name.String())
			name.Reset()
			if guardName == "" {
				return nil, fmt.Errorf("invalid guard tag %q: '(' without a guard name", tag)
			}

			args, next, err := parseGuardArgs(tag, i+1)
			if err != nil {
				return nil, fmt.Errorf("invalid guard tag %q: %w", tag, err)
			}
			specs = append(specs, GuardSpec{Name: guardName, Args: args})
			i = next

		case ')':
			return nil, fmt.Errorf("invalid guard tag %q: unmatched ')'", tag)

		case ',', ' ', '\t':
			flushName()

		default:
			name.WriteByte(c)
		}
	}
	flushName()

	if len(specs) == 0 {
		return nil, fmt.Errorf("invalid guard tag %q: no guard name found", tag)
	}
	return specs, nil
}

// parseGuardArgs reads a comma-separated argument list starting at start (just
// past the opening parenthesis) and returns the arguments plus the index of the
// closing parenthesis.
func parseGuardArgs(tag string, start int) (args []string, closing int, err error) {
	var (
		current strings.Builder
		quote   byte
		hasArg  bool
	)

	appendArg := func() {
		value := current.String()
		if quote == 0 {
			value = strings.TrimSpace(value)
		}
		if value != "" || hasArg {
			args = append(args, value)
		}
		current.Reset()
		hasArg = false
	}

	for i := start; i < len(tag); i++ {
		c := tag[i]

		if quote != 0 {
			if c == quote {
				quote = 0
				hasArg = true
				continue
			}
			current.WriteByte(c)
			continue
		}

		switch c {
		case '\'', '"':
			quote = c
		case ',':
			appendArg()
		case ')':
			// A trailing empty segment means `Guard()`, which is no arguments.
			if strings.TrimSpace(current.String()) != "" || hasArg || len(args) > 0 {
				appendArg()
			}
			return args, i, nil
		default:
			current.WriteByte(c)
		}
	}

	return nil, 0, fmt.Errorf("unterminated guard argument list")
}

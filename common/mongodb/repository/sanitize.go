package repository

import (
	"fmt"
	"strings"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// MaxRegexLength bounds a client-supplied $regex pattern. Long patterns are both
// a readability problem and the raw material for catastrophic backtracking:
// MongoDB evaluates $regex with PCRE, which backtracks, so a pattern the server
// accepts can pin a CPU for minutes.
const MaxRegexLength = 200

// defaultAllowedOperators is the set of query operators that are safe to accept
// from an end user. They filter documents and nothing else.
//
// Deliberately absent: $where and $function (execute JavaScript on the server),
// $expr and $accumulator (evaluate arbitrary expressions), $nin combined with
// $exists (used to enumerate schemas), and every update operator. Those change
// what the query *is*, not merely which documents it selects.
var defaultAllowedOperators = map[string]struct{}{
	"$in":  {},
	"$gt":  {},
	"$gte": {},
	"$lt":  {},
	"$lte": {},
	"$ne":  {},

	"$regex": {},
	// $options only ever travels beside $regex and is rewritten, not trusted.
	"$options": {},
}

// forbiddenOperators can never be allowlisted, however the caller configures
// SanitizeUserFilter. Each one lets a filter execute code or arbitrary
// expressions server-side.
var forbiddenOperators = map[string]struct{}{
	"$where":       {},
	"$function":    {},
	"$expr":        {},
	"$accumulator": {},
	"$jsonSchema":  {},
	"$lookup":      {},
	"$graphLookup": {},
	"$merge":       {},
	"$out":         {},
}

// allowedRegexOptions are the PCRE flags a client may set.
//
// `x` (extended) and `s` (dotall) are excluded on purpose: `x` makes whitespace
// and `#` comments significant, so a pattern that looks inert can hide an
// expensive one, and `s` makes `.` cross newlines, which quietly widens every
// wildcard the application thought it had bounded.
const allowedRegexOptions = "im"

// SanitizeFilter rejects any operator syntax in a filter.
//
// Use it on a filter assembled from request data when the query shape is fixed
// by the application: it guarantees every key is a plain field path and every
// value is compared for equality. That closes the classic operator injection
// where a JSON body of {"password": {"$ne": null}} turns a credential check into
// "any user with a password", or {"$where": "sleep(5000)"} runs JavaScript on the
// database server.
//
// The returned filter is a copy; the input is not modified.
func SanitizeFilter(filter Filter) (Filter, error) {
	return sanitize(filter, nil)
}

// SanitizeUserFilter rejects operator syntax except for a small allowlist of
// selection-only operators.
//
// With no arguments it permits $in, $gt, $gte, $lt, $lte, $ne, and $regex (with
// the pattern length, backtracking, and $options restrictions described on
// MaxRegexLength and allowedRegexOptions). Pass explicit operator names to
// narrow or extend that set; the operators in forbiddenOperators stay rejected
// regardless.
func SanitizeUserFilter(filter Filter, allowedOperators ...string) (Filter, error) {
	allowed := defaultAllowedOperators
	if len(allowedOperators) > 0 {
		allowed = make(map[string]struct{}, len(allowedOperators)+1)
		for _, op := range allowedOperators {
			allowed[op] = struct{}{}
		}
		// $options is meaningless alone and always rewritten, so it rides along
		// whenever $regex is permitted.
		if _, ok := allowed["$regex"]; ok {
			allowed["$options"] = struct{}{}
		}
	}
	return sanitize(filter, allowed)
}

// sanitize walks the filter. A nil allowed set means "no operators at all".
func sanitize(filter Filter, allowed map[string]struct{}) (Filter, error) {
	if filter == nil {
		return Filter{}, nil
	}

	out := make(Filter, len(filter))
	for key, value := range filter {
		if err := checkKey(key, allowed, ""); err != nil {
			return nil, err
		}
		clean, err := sanitizeValue(value, allowed, key)
		if err != nil {
			return nil, err
		}
		out[key] = clean
	}

	return out, nil
}

func checkKey(key string, allowed map[string]struct{}, path string) error {
	where := key
	if path != "" {
		where = path + "." + key
	}

	if key == "" {
		return fmt.Errorf("filter %q: empty field name", where)
	}
	if strings.ContainsRune(key, 0) {
		return fmt.Errorf("filter %q: field name contains a NUL byte", where)
	}
	if !strings.HasPrefix(key, "$") {
		return nil
	}

	if _, forbidden := forbiddenOperators[key]; forbidden {
		return fmt.Errorf("filter %q: operator %s is never allowed", where, key)
	}
	// A top-level key is a field path, never an operator. The only operators
	// MongoDB accepts there restructure the query ($and/$or/$nor/$where/$expr),
	// so no allowlist can make one safe.
	if path == "" {
		return fmt.Errorf("filter %q: a top-level key must be a field name, not the operator %s", where, key)
	}
	if allowed == nil {
		return fmt.Errorf("filter %q: operators are not allowed here (use SanitizeUserFilter to permit a specific set)", where)
	}
	if _, ok := allowed[key]; !ok {
		return fmt.Errorf("filter %q: operator %s is not allowed", where, key)
	}
	return nil
}

func sanitizeValue(value any, allowed map[string]struct{}, path string) (any, error) {
	switch v := value.(type) {
	case nil:
		return nil, nil

	case bson.M:
		clean, err := sanitizeMap(v, allowed, path)
		if err != nil {
			return nil, err
		}
		return bson.M(clean), nil

	case map[string]any:
		clean, err := sanitizeMap(v, allowed, path)
		if err != nil {
			return nil, err
		}
		return clean, nil

	case bson.D:
		out := make(bson.D, 0, len(v))
		for _, e := range v {
			if err := checkKey(e.Key, allowed, path); err != nil {
				return nil, err
			}
			cleanVal, err := sanitizeValue(e.Value, allowed, path+"."+e.Key)
			if err != nil {
				return nil, err
			}
			out = append(out, bson.E{Key: e.Key, Value: cleanVal})
		}
		return out, nil

	case bson.A:
		out := make(bson.A, len(v))
		for i, item := range v {
			clean, err := sanitizeValue(item, allowed, path)
			if err != nil {
				return nil, err
			}
			out[i] = clean
		}
		return out, nil

	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			clean, err := sanitizeValue(item, allowed, path)
			if err != nil {
				return nil, err
			}
			out[i] = clean
		}
		return out, nil

	case primitive.Regex:
		if err := ValidateRegexPattern(v.Pattern); err != nil {
			return nil, fmt.Errorf("filter %q: %w", path, err)
		}
		return primitive.Regex{Pattern: v.Pattern, Options: sanitizeRegexOptions(v.Options)}, nil

	default:
		return value, nil
	}
}

// sanitizeMap walks a nested document, applying the $regex rules when it sees
// one.
func sanitizeMap(m map[string]any, allowed map[string]struct{}, path string) (map[string]any, error) {
	out := make(map[string]any, len(m))
	for key, value := range m {
		if err := checkKey(key, allowed, path); err != nil {
			return nil, err
		}

		child := key
		if path != "" {
			child = path + "." + key
		}

		switch key {
		case "$regex":
			if pattern, ok := value.(string); ok {
				if err := ValidateRegexPattern(pattern); err != nil {
					return nil, fmt.Errorf("filter %q: %w", child, err)
				}
				out[key] = pattern
				continue
			}
			if rx, ok := value.(primitive.Regex); ok {
				if err := ValidateRegexPattern(rx.Pattern); err != nil {
					return nil, fmt.Errorf("filter %q: %w", child, err)
				}
				out[key] = primitive.Regex{Pattern: rx.Pattern, Options: sanitizeRegexOptions(rx.Options)}
				continue
			}
			return nil, fmt.Errorf("filter %q: $regex must be a string pattern, got %T", child, value)

		case "$options":
			opts, ok := value.(string)
			if !ok {
				return nil, fmt.Errorf("filter %q: $options must be a string, got %T", child, value)
			}
			out[key] = sanitizeRegexOptions(opts)
			continue
		}

		clean, err := sanitizeValue(value, allowed, child)
		if err != nil {
			return nil, err
		}
		out[key] = clean
	}

	if _, hasOptions := out["$options"]; hasOptions {
		if _, hasRegex := out["$regex"]; !hasRegex {
			delete(out, "$options")
		}
	}

	return out, nil
}

// sanitizeRegexOptions keeps only the flags in allowedRegexOptions, so `x` and
// `s` cannot be smuggled in. Unknown flags are dropped rather than rejected:
// they carry no meaning to filter on.
func sanitizeRegexOptions(options string) string {
	var b strings.Builder
	seen := map[rune]struct{}{}
	for _, r := range options {
		if !strings.ContainsRune(allowedRegexOptions, r) {
			continue
		}
		if _, dup := seen[r]; dup {
			continue
		}
		seen[r] = struct{}{}
		b.WriteRune(r)
	}
	return b.String()
}

// ValidateRegexPattern rejects patterns that are too long or structurally prone
// to catastrophic backtracking.
//
// MongoDB matches $regex with PCRE, a backtracking engine, so `(a+)+$` against a
// long non-matching string is exponential — one request can saturate a database
// core. Go's own regexp is RE2 and cannot be used as an oracle here: it refuses
// valid PCRE constructs and never backtracks, so it would both over- and
// under-report. The checks below are structural instead.
func ValidateRegexPattern(pattern string) error {
	if len(pattern) > MaxRegexLength {
		return fmt.Errorf("regex pattern too long: %d characters (max %d)", len(pattern), MaxRegexLength)
	}

	if idx := strings.IndexAny(pattern, "\x00"); idx >= 0 {
		return fmt.Errorf("regex pattern contains a NUL byte")
	}

	// Backreferences make matching NP-hard in the general case; lookaround and
	// recursion multiply the backtracking search space.
	for _, construct := range []string{`(?=`, `(?!`, `(?<`, `(?R`, `(?P>`, `(?C`, `\g`} {
		if strings.Contains(pattern, construct) {
			return fmt.Errorf("regex pattern uses unsupported construct %q", construct)
		}
	}
	if hasBackreference(pattern) {
		return fmt.Errorf("regex pattern uses a backreference")
	}
	if err := checkNestedQuantifiers(pattern); err != nil {
		return err
	}
	if err := checkRepetitionBounds(pattern); err != nil {
		return err
	}
	return nil
}

// hasBackreference looks for \1 … \9 outside a character class.
func hasBackreference(pattern string) bool {
	for i := 0; i+1 < len(pattern); i++ {
		if pattern[i] != '\\' {
			continue
		}
		next := pattern[i+1]
		if next >= '1' && next <= '9' {
			return true
		}
		// Skip the escaped character so `\\1` is not read as a backreference.
		i++
	}
	return false
}

// checkNestedQuantifiers rejects a quantified group whose body is itself
// quantified or alternated — the `(a+)+` / `(a|aa)*` shape that makes
// backtracking blow up.
func checkNestedQuantifiers(pattern string) error {
	var stack []int // opening index of each currently open group

	for i := 0; i < len(pattern); i++ {
		switch pattern[i] {
		case '\\':
			i++ // an escaped character is a literal
		case '[':
			// Skip character classes: quantifier-looking bytes inside them are
			// literals.
			for i++; i < len(pattern); i++ {
				if pattern[i] == '\\' {
					i++
					continue
				}
				if pattern[i] == ']' {
					break
				}
			}
		case '(':
			stack = append(stack, i)
		case ')':
			if len(stack) == 0 {
				continue // unbalanced; PCRE will reject it
			}
			open := stack[len(stack)-1]
			stack = stack[:len(stack)-1]

			if i+1 >= len(pattern) || !isQuantifier(pattern[i+1]) {
				continue
			}
			body := pattern[open+1 : i]
			if containsQuantifierOrAlternation(body) {
				return fmt.Errorf("regex pattern has a quantified group with an inner quantifier or alternation (%q), which backtracks catastrophically", "("+body+")"+string(pattern[i+1]))
			}
		}
	}
	return nil
}

func isQuantifier(c byte) bool {
	return c == '*' || c == '+' || c == '{'
}

func containsQuantifierOrAlternation(body string) bool {
	for i := 0; i < len(body); i++ {
		switch body[i] {
		case '\\':
			i++
		case '[':
			for i++; i < len(body); i++ {
				if body[i] == '\\' {
					i++
					continue
				}
				if body[i] == ']' {
					break
				}
			}
		case '*', '+', '|', '{':
			return true
		}
	}
	return false
}

// maxRepetition caps an explicit {n,m} bound. A large bound expands into that
// many copies of the sub-pattern inside the engine.
const maxRepetition = 100

func checkRepetitionBounds(pattern string) error {
	for i := 0; i < len(pattern); i++ {
		if pattern[i] == '\\' {
			i++
			continue
		}
		if pattern[i] != '{' {
			continue
		}
		end := strings.IndexByte(pattern[i:], '}')
		if end < 0 {
			continue
		}
		body := pattern[i+1 : i+end]
		for _, part := range strings.Split(body, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			n := 0
			numeric := true
			for _, c := range part {
				if c < '0' || c > '9' {
					numeric = false
					break
				}
				n = n*10 + int(c-'0')
				if n > maxRepetition {
					break
				}
			}
			if numeric && n > maxRepetition {
				return fmt.Errorf("regex pattern repetition bound {%s} exceeds %d", body, maxRepetition)
			}
		}
		i += end
	}
	return nil
}

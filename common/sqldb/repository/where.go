package repository

import (
	"fmt"
	"reflect"
	"strings"
)

// Operator is the comparison used by a Cond.
//
// It is a closed integer enum rather than a string on purpose: an operator that
// could be supplied as text (`Cond{Op: request.Query("op")}`) is an injection
// point no amount of value binding can close, because the operator lands in the
// statement structure, not in a parameter.
type Operator uint8

const (
	OpEq Operator = iota // column = ?
	OpNotEq
	OpGT
	OpGTE
	OpLT
	OpLTE
	OpLike
	OpILike  // case-insensitive LIKE
	OpIn     // Value must be a slice or array
	OpNotIn  // Value must be a slice or array
	OpIsNull // Value ignored
	OpNotNull
	OpBetween // Value must be a slice or array of exactly two elements

	opCount // sentinel: any Operator >= opCount is invalid
)

// String renders the operator for error messages.
func (o Operator) String() string {
	switch o {
	case OpEq:
		return "="
	case OpNotEq:
		return "<>"
	case OpGT:
		return ">"
	case OpGTE:
		return ">="
	case OpLT:
		return "<"
	case OpLTE:
		return "<="
	case OpLike:
		return "LIKE"
	case OpILike:
		return "ILIKE"
	case OpIn:
		return "IN"
	case OpNotIn:
		return "NOT IN"
	case OpIsNull:
		return "IS NULL"
	case OpNotNull:
		return "IS NOT NULL"
	case OpBetween:
		return "BETWEEN"
	default:
		return fmt.Sprintf("Operator(%d)", uint8(o))
	}
}

// Cond is one WHERE predicate. Column is validated and quoted; Value is always
// bound as a parameter.
type Cond struct {
	Column string
	Op     Operator
	Value  any
}

// Condition constructors. They exist so call sites read as data rather than as
// hand-assembled SQL fragments.

func Eq(column string, value any) Cond    { return Cond{Column: column, Op: OpEq, Value: value} }
func NotEq(column string, value any) Cond { return Cond{Column: column, Op: OpNotEq, Value: value} }
func GT(column string, value any) Cond    { return Cond{Column: column, Op: OpGT, Value: value} }
func GTE(column string, value any) Cond   { return Cond{Column: column, Op: OpGTE, Value: value} }
func LT(column string, value any) Cond    { return Cond{Column: column, Op: OpLT, Value: value} }
func LTE(column string, value any) Cond   { return Cond{Column: column, Op: OpLTE, Value: value} }
func Like(column string, pattern string) Cond {
	return Cond{Column: column, Op: OpLike, Value: pattern}
}
func ILike(column string, pattern string) Cond {
	return Cond{Column: column, Op: OpILike, Value: pattern}
}
func IsNull(column string) Cond  { return Cond{Column: column, Op: OpIsNull} }
func NotNull(column string) Cond { return Cond{Column: column, Op: OpNotNull} }

// In builds an IN condition. An empty values slice yields an always-false
// predicate, never `IN ()`, which is a syntax error on every engine.
func In[V any](column string, values []V) Cond {
	return Cond{Column: column, Op: OpIn, Value: toAnySlice(values)}
}

// NotIn builds a NOT IN condition. An empty values slice yields an
// always-true predicate, matching set semantics ("not in the empty set").
func NotIn[V any](column string, values []V) Cond {
	return Cond{Column: column, Op: OpNotIn, Value: toAnySlice(values)}
}

// Between builds an inclusive range condition.
func Between(column string, low, high any) Cond {
	return Cond{Column: column, Op: OpBetween, Value: []any{low, high}}
}

func toAnySlice[V any](values []V) []any {
	out := make([]any, len(values))
	for i, v := range values {
		out[i] = v
	}
	return out
}

// buildConds renders conds into a clause (without the leading WHERE) using
// placeholders numbered from startIdx+1. It returns the clause, the bound
// arguments, and the last placeholder index consumed so callers can continue
// numbering (LIMIT/OFFSET, RETURNING, and so on).
func buildConds(d Dialect, conds []Cond, startIdx int) (string, []any, int, error) {
	if len(conds) == 0 {
		return "", nil, startIdx, nil
	}

	parts := make([]string, 0, len(conds))
	args := make([]any, 0, len(conds))
	idx := startIdx

	for _, c := range conds {
		if c.Op >= opCount {
			return "", nil, startIdx, fmt.Errorf("where: unknown operator %d for column %q", uint8(c.Op), c.Column)
		}
		col, err := d.QuoteValidated(c.Column)
		if err != nil {
			return "", nil, startIdx, fmt.Errorf("where: %w", err)
		}

		switch c.Op {
		case OpIsNull:
			parts = append(parts, col+" IS NULL")

		case OpNotNull:
			parts = append(parts, col+" IS NOT NULL")

		case OpIn, OpNotIn:
			// A nil Value here means the caller forgot to set it, which is not
			// the same statement as "the empty set" — say so instead of silently
			// emitting a constant predicate.
			if c.Value == nil {
				return "", nil, startIdx, fmt.Errorf("where: %s on %q requires a slice value, got nil", c.Op, c.Column)
			}
			values, err := elements(c.Value)
			if err != nil {
				return "", nil, startIdx, fmt.Errorf("where: %s on %q: %w", c.Op, c.Column, err)
			}
			if len(values) == 0 {
				// `IN ()` is invalid SQL, so degenerate to a constant predicate
				// that preserves set semantics.
				if c.Op == OpIn {
					parts = append(parts, "1 = 0")
				} else {
					parts = append(parts, "1 = 1")
				}
				continue
			}
			holders := make([]string, len(values))
			for i, v := range values {
				idx++
				holders[i] = d.placeholder(idx)
				args = append(args, v)
			}
			parts = append(parts, fmt.Sprintf("%s %s (%s)", col, c.Op, strings.Join(holders, ", ")))

		case OpBetween:
			values, err := elements(c.Value)
			if err != nil {
				return "", nil, startIdx, fmt.Errorf("where: BETWEEN on %q: %w", c.Column, err)
			}
			if len(values) != 2 {
				return "", nil, startIdx, fmt.Errorf("where: BETWEEN on %q needs exactly 2 bounds, got %d", c.Column, len(values))
			}
			low := d.placeholder(idx + 1)
			high := d.placeholder(idx + 2)
			idx += 2
			args = append(args, values[0], values[1])
			parts = append(parts, fmt.Sprintf("%s BETWEEN %s AND %s", col, low, high))

		case OpILike:
			idx++
			args = append(args, c.Value)
			// Only PostgreSQL has ILIKE. MySQL's default collations and SQLite's
			// LIKE are already case-insensitive for ASCII, but folding both sides
			// makes the behaviour identical across engines instead of
			// collation-dependent.
			if d == DialectPostgres {
				parts = append(parts, fmt.Sprintf("%s ILIKE %s", col, d.placeholder(idx)))
			} else {
				parts = append(parts, fmt.Sprintf("LOWER(%s) LIKE LOWER(%s)", col, d.placeholder(idx)))
			}

		default:
			// A nil value with an equality operator means IS NULL: `= NULL` is
			// never true in SQL, so binding nil would silently match nothing.
			if c.Value == nil {
				switch c.Op {
				case OpEq:
					parts = append(parts, col+" IS NULL")
					continue
				case OpNotEq:
					parts = append(parts, col+" IS NOT NULL")
					continue
				}
			}
			idx++
			args = append(args, c.Value)
			parts = append(parts, fmt.Sprintf("%s %s %s", col, c.Op, d.placeholder(idx)))
		}
	}

	return strings.Join(parts, " AND "), args, idx, nil
}

// buildWhereConds is buildConds with the WHERE keyword attached, or an empty
// string when conds produce no predicate.
func buildWhereConds(d Dialect, conds []Cond, startIdx int) (string, []any, int, error) {
	clause, args, idx, err := buildConds(d, conds, startIdx)
	if err != nil || clause == "" {
		return "", args, idx, err
	}
	return "WHERE " + clause, args, idx, nil
}

// elements expands a slice or array value into its members. A []byte is a
// single scalar (bytea/BLOB), not a list of numbers.
func elements(value any) ([]any, error) {
	if value == nil {
		return nil, nil
	}
	if b, ok := value.([]byte); ok {
		return []any{b}, nil
	}
	if vs, ok := value.([]any); ok {
		return vs, nil
	}

	rv := reflect.ValueOf(value)
	switch rv.Kind() {
	case reflect.Slice, reflect.Array:
		out := make([]any, rv.Len())
		for i := 0; i < rv.Len(); i++ {
			out[i] = rv.Index(i).Interface()
		}
		return out, nil
	default:
		return nil, fmt.Errorf("value must be a slice or array, got %T", value)
	}
}

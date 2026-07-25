package cache

import (
	"encoding"
	"encoding/json"
	"fmt"
)

// marshalValue renders an arbitrary cache value as the string every provider
// stores.
//
// The previous file provider did value.(string) — an unchecked type assertion,
// so caching an int, a struct or a nil crashed the whole process from inside a
// request handler. Anything that is not already a string, []byte or a
// self-encoding type is JSON-encoded, and an unencodable value returns an error
// instead of panicking.
//
// Encoding here rather than deferring to the driver also keeps Get symmetric
// across providers: the bytes a caller reads back are the bytes this function
// produced, whichever driver is configured.
func marshalValue(value any) (string, error) {
	switch v := value.(type) {
	case nil:
		return "", fmt.Errorf("cache: cannot store a nil value")
	case string:
		return v, nil
	case []byte:
		return string(v), nil
	case encoding.BinaryMarshaler:
		b, err := v.MarshalBinary()
		if err != nil {
			return "", fmt.Errorf("cache: marshal binary: %w", err)
		}
		return string(b), nil
	case encoding.TextMarshaler:
		b, err := v.MarshalText()
		if err != nil {
			return "", fmt.Errorf("cache: marshal text: %w", err)
		}
		return string(b), nil
	default:
		b, err := json.Marshal(value)
		if err != nil {
			return "", fmt.Errorf("cache: marshal value of type %T: %w", value, err)
		}
		return string(b), nil
	}
}

package validator

import (
	"errors"
	"fmt"
	"reflect"

	"github.com/go-playground/validator/v10"
)

// FieldError is one client-facing validation failure.
//
// Field and Message stay the first two fields, and keep their JSON names, so
// existing consumers of the error array are unaffected by the additions.
type FieldError struct {
	// Field is the name the client used, taken from the json tag — see
	// jsonFieldName.
	Field string `json:"field"`
	// Message is a human-readable, non-leaking explanation.
	Message string `json:"message"`
	// Namespace is the dotted path to the field, including slice indices
	// ("items[2].price"), which is the only way a client can locate a failure
	// inside a nested payload.
	Namespace string `json:"namespace,omitempty"`
	// Tag is the rule that failed ("required", "email"), for clients that want to
	// branch on the rule rather than parse Message.
	Tag string `json:"tag,omitempty"`
	// Param is the rule's argument ("8" for min=8), so a client can render its own
	// message.
	Param string `json:"param,omitempty"`
}

// genericMessage is returned for an error that is not a validation error at all.
const genericMessage = "The submitted data could not be validated"

// FormatErrors converts an error into a slice of FieldError.
//
// For validator.ValidationErrors it returns one entry per failed field. For any
// other non-nil error it returns a single generic entry rather than nil.
//
// The nil-for-unknown-errors behaviour it replaces was actively dangerous: every
// caller in this package treats a nil result as "the payload is valid", so an
// InvalidValidationError (passing a non-struct to Struct(), a common refactoring
// slip) or a wrapped error made a request sail through validation entirely. It now
// always reports a failure when handed a failure. A nil error still returns nil.
func FormatErrors(err error) []FieldError {
	if err == nil {
		return nil
	}

	var validationErrors validator.ValidationErrors
	if !errors.As(err, &validationErrors) {
		return []FieldError{{Field: "", Message: genericMessage}}
	}

	result := make([]FieldError, 0, len(validationErrors))
	for _, e := range validationErrors {
		result = append(result, FieldError{
			Field:     e.Field(),
			Message:   messageForTag(e),
			Namespace: e.Namespace(),
			Tag:       e.Tag(),
			Param:     e.Param(),
		})
	}

	return result
}

// isNumeric reports whether a Kind should be described with counts rather than
// character lengths.
func isNumeric(k reflect.Kind) bool {
	switch k {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return true
	}
	return false
}

// isCountable reports whether a Kind is measured in items.
func isCountable(k reflect.Kind) bool {
	switch k {
	case reflect.Slice, reflect.Array, reflect.Map:
		return true
	}
	return false
}

// messageForTag renders a client-facing message for a failed rule.
//
// The default branch deliberately does NOT return e.Error(). That string embeds
// the Go struct namespace and the validator's internals — for example
// "Key: 'CreateUserDto.Password' Error:Field validation for 'Password' failed on
// the 'strongpassword' tag" — which hands an API client the server's type names,
// field layout and custom rule names. The generic fallback below says which rule
// failed without disclosing any of that.
func messageForTag(e validator.FieldError) string {
	param := e.Param()

	switch e.Tag() {
	case "required":
		return "This field is required"
	case "required_if":
		return "This field is required for the submitted values"
	case "required_with":
		return "This field is required when the related field is present"
	case "excluded_with":
		return "This field must be omitted when the related field is present"
	case "email":
		return "Invalid email format"

	case "min":
		switch {
		case isNumeric(e.Kind()):
			return fmt.Sprintf("Must be at least %s", param)
		case isCountable(e.Kind()):
			return fmt.Sprintf("Must contain at least %s items", param)
		default:
			return fmt.Sprintf("Must be at least %s characters", param)
		}
	case "max":
		switch {
		case isNumeric(e.Kind()):
			return fmt.Sprintf("Must be at most %s", param)
		case isCountable(e.Kind()):
			return fmt.Sprintf("Must contain at most %s items", param)
		default:
			return fmt.Sprintf("Must be at most %s characters", param)
		}
	case "len":
		if isCountable(e.Kind()) {
			return fmt.Sprintf("Must contain exactly %s items", param)
		}
		return fmt.Sprintf("Must be exactly %s characters", param)

	case "gt":
		return fmt.Sprintf("Must be greater than %s", param)
	case "gte":
		return fmt.Sprintf("Must be greater than or equal to %s", param)
	case "lt":
		return fmt.Sprintf("Must be less than %s", param)
	case "lte":
		return fmt.Sprintf("Must be less than or equal to %s", param)
	case "eqfield":
		return "Must match the related field"
	case "nefield":
		return "Must differ from the related field"
	case "gtfield":
		return "Must be greater than the related field"
	case "ltfield":
		return "Must be less than the related field"

	case "oneof":
		return fmt.Sprintf("Must be one of: %s", param)
	case "number", "numeric":
		return "Must be a number"
	case "alpha":
		return "Must contain letters only"
	case "alphanum":
		return "Must contain letters and digits only"
	case "boolean":
		return "Must be true or false"
	case "datetime":
		return fmt.Sprintf("Must be a date in the format %s", param)
	case "url":
		return "Invalid URL format"
	case "uuid", "uuid4":
		return "Must be a valid UUID"
	case "jwt":
		return "Must be a valid token"
	case "json":
		return "Must be valid JSON"
	case "base64":
		return "Must be valid base64"
	case "hexadecimal":
		return "Must be a hexadecimal value"

	case "contains":
		return fmt.Sprintf("Must contain %q", param)
	case "startswith":
		return fmt.Sprintf("Must start with %q", param)
	case "endswith":
		return fmt.Sprintf("Must end with %q", param)
	case "unique":
		return "Must not contain duplicate values"
	case "dive":
		return "One or more items are invalid"

	case "ip":
		return "Must be a valid IP address"
	case "ipv4":
		return "Must be a valid IPv4 address"
	case "cidr":
		return "Must be a valid CIDR range"
	case "hostname":
		return "Must be a valid hostname"

	case "ir_mobile":
		return "Mobile number is not valid"
	case "objectid":
		return "ObjectId not valid"
	case "ir_national_code":
		return "National code is not valid"
	case "password_strong":
		return "Must be at least 8 characters and include an upper-case letter, a lower-case letter and a digit"
	case "slug":
		return "Must be lower-case letters, digits and single hyphens"
	case "no_html":
		return "Must not contain HTML"
	case "safe_filename":
		return "Must be a valid file name without path separators"

	default:
		// Name the rule, never the Go type or the raw validator string.
		if param != "" {
			return fmt.Sprintf("Failed the %q rule (%s)", e.Tag(), param)
		}
		return fmt.Sprintf("Failed the %q rule", e.Tag())
	}
}

package validator

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	"github.com/go-playground/validator/v10"
	"github.com/nika-framework/nika/common/response"
)

// ValidateStruct validates a struct and returns one FieldError per failure, or
// nil when it is valid.
//
// It resolves the instance through Instance(), so it no longer panics when Setup
// was never called.
func ValidateStruct(s interface{}) []FieldError {
	err := Instance().Struct(s)
	if err == nil {
		return nil
	}
	return FormatErrors(err)
}

// jsonTypeName describes a Go type using JSON vocabulary.
//
// json.UnmarshalTypeError carries the Go type, and reporting it verbatim would
// tell a client about the server's internal types ("[]dto.AddressInput",
// "*time.Time"). The client only needs to know what shape was expected.
func jsonTypeName(t reflect.Type) string {
	if t == nil {
		return "value"
	}
	switch t.Kind() {
	case reflect.String:
		return "string"
	case reflect.Bool:
		return "boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return "number"
	case reflect.Slice, reflect.Array:
		return "array"
	case reflect.Map, reflect.Struct:
		return "object"
	case reflect.Ptr:
		return jsonTypeName(t.Elem())
	default:
		return "value"
	}
}

// bindErrorMessage turns a binding failure into a message that is safe to send.
//
// err.Error() used to be forwarded straight to the client. Decoder errors carry
// Go type names, struct paths and byte offsets into internal types — for instance
// "json: cannot unmarshal string into Go struct field CreateUserDto.Profile.Age
// of type int" — which maps out the server's data model for free. Each known
// failure mode gets a message describing what the *client* did wrong.
func bindErrorMessage(err error) string {
	if errors.Is(err, io.EOF) {
		return "request body is empty"
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return "malformed JSON: unexpected end of input"
	}

	var syntaxErr *json.SyntaxError
	if errors.As(err, &syntaxErr) {
		return fmt.Sprintf("malformed JSON at byte %d", syntaxErr.Offset)
	}

	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		field := typeErr.Field
		if field == "" {
			field = "body"
		}
		return fmt.Sprintf("field %s expects a %s", field, jsonTypeName(typeErr.Type))
	}

	var numErr *json.UnmarshalFieldError
	if errors.As(err, &numErr) {
		return "one or more fields have an unexpected type"
	}

	return "request payload is not valid"
}

// respondBindError writes the right status and body for a binding failure and
// aborts the chain.
func respondBindError(c *gin.Context, code string, err error) bool {
	// The body limit middleware wraps the body in an http.MaxBytesReader, so an
	// oversized payload surfaces here. It is 413, not 400.
	var maxBytesErr *http.MaxBytesError
	if errors.As(err, &maxBytesErr) {
		response.JSONError(
			c,
			http.StatusRequestEntityTooLarge,
			"REQUEST_BODY_TOO_LARGE",
			fmt.Sprintf("request body too large (limit %d bytes)", maxBytesErr.Limit),
		)
		return false
	}

	// Gin's own binding validator runs `binding` tags during ShouldBind*. Route
	// those through FormatErrors so they get the same non-leaking treatment and
	// the same 422 as our own validation pass.
	var validationErrs validator.ValidationErrors
	if errors.As(err, &validationErrs) {
		response.UnprocessableEntity(c, "VALIDATION_ERROR", FormatErrors(err))
		return false
	}

	response.BadRequest(c, code, bindErrorMessage(err))
	return false
}

// BindAndValidate binds the JSON body and validates it.
// On failure, responds with an appropriate error and returns false.
func BindAndValidate(c *gin.Context, dto interface{}) bool {
	if err := c.ShouldBindJSON(dto); err != nil {
		return respondBindError(c, "INVALID_JSON", err)
	}

	if errs := ValidateStruct(dto); errs != nil {
		response.UnprocessableEntity(c, "VALIDATION_ERROR", errs)
		return false
	}

	return true
}

// BindAndValidateMicroservice is the microservice-side name for BindAndValidate.
//
// A message-transport handler is handed a real *gin.Context synthesized around
// the inbound message, with the message payload as the request body, so binding
// and validation are literally the same operation as for an HTTP request. It
// exists as a distinct name so transport handlers read as transport handlers and
// so the two paths can diverge later without touching call sites.
func BindAndValidateMicroservice(c *gin.Context, dto any) bool {
	return BindAndValidate(c, dto)
}

// BindAndValidateQuery binds query parameters and validates them.
func BindAndValidateQuery(c *gin.Context, dto interface{}) bool {
	if err := c.ShouldBindQuery(dto); err != nil {
		return respondBindError(c, "INVALID_QUERY", err)
	}

	if errs := ValidateStruct(dto); errs != nil {
		response.UnprocessableEntity(c, "VALIDATION_ERROR", errs)
		return false
	}

	return true
}

// BindAndValidateUri binds URI path parameters and validates them.
func BindAndValidateUri(c *gin.Context, dto interface{}) bool {
	if err := c.ShouldBindUri(dto); err != nil {
		return respondBindError(c, "INVALID_URI", err)
	}

	if errs := ValidateStruct(dto); errs != nil {
		response.UnprocessableEntity(c, "VALIDATION_ERROR", errs)
		return false
	}

	return true
}

// BindAndValidateForm binds an urlencoded or multipart form body and validates it.
func BindAndValidateForm(c *gin.Context, dto interface{}) bool {
	if err := c.ShouldBindWith(dto, binding.Form); err != nil {
		return respondBindError(c, "INVALID_FORM", err)
	}

	if errs := ValidateStruct(dto); errs != nil {
		response.UnprocessableEntity(c, "VALIDATION_ERROR", errs)
		return false
	}

	return true
}

// BindAndValidateHeader binds request headers and validates them.
func BindAndValidateHeader(c *gin.Context, dto interface{}) bool {
	if err := c.ShouldBindHeader(dto); err != nil {
		return respondBindError(c, "INVALID_HEADER", err)
	}

	if errs := ValidateStruct(dto); errs != nil {
		response.UnprocessableEntity(c, "VALIDATION_ERROR", errs)
		return false
	}

	return true
}

// Bind allocates a T, binds the JSON body into it and validates it, so a handler
// can write `dto, ok := validator.Bind[CreateUserDto](c)` instead of declaring the
// zero value and passing its address.
func Bind[T any](c *gin.Context) (*T, bool) {
	dto := new(T)
	if !BindAndValidate(c, dto) {
		return nil, false
	}
	return dto, true
}

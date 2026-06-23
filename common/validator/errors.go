package validator

import (
	"fmt"

	"github.com/go-playground/validator/v10"
)

type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// FormatErrors converts validator.ValidationErrors into a slice of FieldError.
func FormatErrors(err error) []FieldError {
	var result []FieldError

	validationErrors, ok := err.(validator.ValidationErrors)
	if !ok {
		return result
	}

	for _, e := range validationErrors {
		result = append(result, FieldError{
			Field:   e.Field(),
			Message: messageForTag(e),
		})
	}

	return result
}

func messageForTag(e validator.FieldError) string {
	switch e.Tag() {
	case "required":
		return "This field is required"
	case "email":
		return "Invalid email format"
	case "min":
		return fmt.Sprintf("Must be at least %s characters", e.Param())
	case "max":
		return fmt.Sprintf("Must be at most %s characters", e.Param())
	case "len":
		return fmt.Sprintf("Must be exactly %s characters", e.Param())
	case "gt":
		return fmt.Sprintf("Must be greater than %s", e.Param())
	case "gte":
		return fmt.Sprintf("Must be greater than or equal to %s", e.Param())
	case "lt":
		return fmt.Sprintf("Must be less than %s", e.Param())
	case "lte":
		return fmt.Sprintf("Must be less than or equal to %s", e.Param())
	case "oneof":
		return fmt.Sprintf("Must be one of: %s", e.Param())
	case "number":
		return "Must be a number"
	case "url":
		return "Invalid URL format"
	case "ir_mobile":
		return "Mobile number is not valid"
	case "objectid":
		return "ObjectId not valid"
	default:
		return e.Error()
	}
}

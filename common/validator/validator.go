package validator

import (
	"fmt"
	"regexp"

	"github.com/go-playground/validator/v10"
	"github.com/nika-framework/nika"
)

var (
	V               *validator.Validate
	irMobileRegex   = regexp.MustCompile(`^09\d{9}$`)
	objectIDRegex   = regexp.MustCompile(`^[a-f0-9]{24}$`)
)

// Setup creates a new Validator instance, registers custom validations,
// and registers it in the DI container.
func Setup(app *nika.App, options ...validator.Option) *validator.Validate {
	V = validator.New(options...)

	_ = V.RegisterValidation("ir_mobile", validateIRMobile)
	_ = V.RegisterValidation("objectid", validateObjectid)

	app.RegisterSingleton(V)

	fmt.Println("✅ Validator initialized")
	return V
}

// Set registers an additional custom validation tag.
func Set(tag string, fn validator.Func) error {
	return V.RegisterValidation(tag, fn)
}

func validateIRMobile(fl validator.FieldLevel) bool {
	return irMobileRegex.MatchString(fl.Field().String())
}

func validateObjectid(fl validator.FieldLevel) bool {
	return objectIDRegex.MatchString(fl.Field().String())
}

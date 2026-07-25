// Package validator wraps go-playground/validator with Nika's custom rules,
// client-facing error formatting, and gin binding helpers.
package validator

import (
	"reflect"
	"regexp"
	"strings"
	"sync"
	"unicode"

	"github.com/go-playground/validator/v10"
	"github.com/nika-framework/nika"
)

// V is the package-level validate instance.
//
// Deprecated: read it through Instance() instead. It is kept exported only so
// existing code compiles; direct access races with Setup, and a direct read
// before Setup yields nil, which is how ValidateStruct and Set used to panic with
// a nil dereference at the first request.
var V *validator.Validate

// instanceMu guards V. Setup takes the write lock; Instance takes the read lock
// and only upgrades when it has to construct the default.
var instanceMu sync.RWMutex

// Regexes are compiled once at package level: compiling per call turns every
// validated request into a regex-compilation, and MustCompile inside a request
// handler would panic on a bad pattern at the worst possible time.
var (
	irMobileRegex     = regexp.MustCompile(`^09\d{9}$`)
	objectIDRegex     = regexp.MustCompile(`^[a-f0-9]{24}$`)
	irNationalRegex   = regexp.MustCompile(`^\d{10}$`)
	slugRegex         = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	safeFilenameChars = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
)

// Instance returns the validate instance, creating a default one on first use.
//
// Lazily constructing beats panicking: a library-level nil dereference gives the
// caller a stack trace inside go-playground rather than a usable message, and a
// missing Setup call is a wiring slip, not a reason to fail every request.
func Instance() *validator.Validate {
	instanceMu.RLock()
	v := V
	instanceMu.RUnlock()
	if v != nil {
		return v
	}

	instanceMu.Lock()
	defer instanceMu.Unlock()
	if V == nil {
		V = newValidate()
	}
	return V
}

// Setup creates a new validate instance, registers Nika's custom validations, and
// registers it in the DI container.
func Setup(app *nika.App, options ...validator.Option) *validator.Validate {
	v := newValidate(options...)

	instanceMu.Lock()
	V = v
	instanceMu.Unlock()

	if app != nil {
		app.RegisterSingleton(v)
	}
	return v
}

// newValidate builds a fully configured instance: JSON field names plus every
// custom rule. Setup and Instance share it so a lazily created default behaves
// identically to an explicitly configured one.
func newValidate(options ...validator.Option) *validator.Validate {
	v := validator.New(options...)

	// Report the name the client actually sent. Without this, FieldError.Field is
	// the Go struct field ("FirstName") while the request body used the JSON name
	// ("first_name"), so a client cannot attach the error to the input it came
	// from and has to guess at the server's naming convention.
	v.RegisterTagNameFunc(jsonFieldName)

	for tag, fn := range customValidations {
		// The only error RegisterValidation returns is for an empty tag or nil
		// function, both of which are impossible for this fixed table.
		_ = v.RegisterValidation(tag, fn)
	}

	return v
}

// customValidations is the table of rules registered on every instance.
var customValidations = map[string]validator.Func{
	"ir_mobile":        validateIRMobile,
	"objectid":         validateObjectID,
	"ir_national_code": validateIRNationalCode,
	"password_strong":  validatePasswordStrong,
	"slug":             validateSlug,
	"no_html":          validateNoHTML,
	"safe_filename":    validateSafeFilename,
}

// Set registers an additional custom validation tag.
//
// It goes through Instance(), so calling it during package init or before Setup
// works instead of panicking on a nil V. A later Setup rebuilds the instance and
// drops tags registered this way, so register custom tags after Setup.
func Set(tag string, fn validator.Func) error {
	return Instance().RegisterValidation(tag, fn)
}

// jsonFieldName maps a struct field to the name used in the JSON payload.
func jsonFieldName(field reflect.StructField) string {
	tag := field.Tag.Get("json")
	if tag == "" {
		return field.Name
	}

	name := strings.SplitN(tag, ",", 2)[0]
	// `json:"-"` means the field is never serialized, so there is no wire name to
	// report; the Go name is the least confusing thing left.
	if name == "" || name == "-" {
		return field.Name
	}
	return name
}

func validateIRMobile(fl validator.FieldLevel) bool {
	return irMobileRegex.MatchString(fl.Field().String())
}

func validateObjectID(fl validator.FieldLevel) bool {
	return objectIDRegex.MatchString(fl.Field().String())
}

// validateIRNationalCode checks an Iranian national ID (کد ملی).
//
// A length-and-digits check alone accepts an enormous number of fabricated codes,
// so the real mod-11 checksum is applied. Repeated-digit codes such as
// "1111111111" satisfy the checksum arithmetic but are not issued, and are
// rejected explicitly.
func validateIRNationalCode(fl validator.FieldLevel) bool {
	code := fl.Field().String()
	if !irNationalRegex.MatchString(code) {
		return false
	}
	if allSameDigits(code) {
		return false
	}

	sum := 0
	for i := 0; i < 9; i++ {
		sum += int(code[i]-'0') * (10 - i)
	}
	remainder := sum % 11
	control := int(code[9] - '0')

	if remainder < 2 {
		return control == remainder
	}
	return control == 11-remainder
}

// allSameDigits reports whether every character of s is the same.
//
// Written as a loop because Go's RE2 engine has no backreferences, so the obvious
// `^(\d)\1{9}$` does not compile.
func allSameDigits(s string) bool {
	for i := 1; i < len(s); i++ {
		if s[i] != s[0] {
			return false
		}
	}
	return len(s) > 0
}

// validatePasswordStrong requires at least 8 characters with an upper-case
// letter, a lower-case letter and a digit.
//
// Implemented as a single pass over the runes rather than a regex with
// lookaheads: Go's RE2 has no lookahead, and the alternative — several
// alternation-heavy patterns over attacker-controlled input — is both slower and
// easy to get wrong. Length counts runes, so a non-ASCII passphrase is not
// penalised for its byte length.
func validatePasswordStrong(fl validator.FieldLevel) bool {
	password := fl.Field().String()

	var length int
	var hasUpper, hasLower, hasDigit bool
	for _, r := range password {
		length++
		switch {
		case unicode.IsUpper(r):
			hasUpper = true
		case unicode.IsLower(r):
			hasLower = true
		case unicode.IsDigit(r):
			hasDigit = true
		}
	}

	return length >= 8 && hasUpper && hasLower && hasDigit
}

func validateSlug(fl validator.FieldLevel) bool {
	return slugRegex.MatchString(fl.Field().String())
}

// validateNoHTML rejects angle brackets.
//
// This is a cheap input-shape check for fields that are never meant to contain
// markup (a display name, a city), not an XSS defence: output escaping at render
// time is what actually prevents XSS. Treat it as a way to reject obviously
// hostile input early, never as a sanitiser.
func validateNoHTML(fl validator.FieldLevel) bool {
	value := fl.Field().String()
	return !strings.ContainsAny(value, "<>")
}

// validateSafeFilename accepts only names that cannot escape a directory or
// confuse a shell or terminal.
//
// Rejects path separators and "..", which are how a user-supplied filename turns
// an upload into an arbitrary file write, and rejects control characters, which
// can hide the real extension from a log reader or terminal.
func validateSafeFilename(fl validator.FieldLevel) bool {
	name := fl.Field().String()
	if name == "" || name == "." || name == ".." {
		return false
	}
	if strings.Contains(name, "..") {
		return false
	}
	if strings.ContainsAny(name, `/\`) {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return false
		}
	}
	// Whitelist rather than blacklist: anything outside this set (spaces, colons,
	// wildcards, non-ASCII homoglyphs) has some filesystem or tool that treats it
	// specially.
	return safeFilenameChars.MatchString(name)
}

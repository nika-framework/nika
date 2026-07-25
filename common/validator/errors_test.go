package validator

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	govalidator "github.com/go-playground/validator/v10"
)

func TestFormatErrorsWithNilError(t *testing.T) {
	if got := FormatErrors(nil); got != nil {
		t.Fatalf("FormatErrors(nil) = %+v, want nil", got)
	}
}

// TestFormatErrorsWithNonValidationError covers the silent-pass bug: returning nil
// for an unknown error type made every caller — all of which treat nil as "valid"
// — accept the request.
func TestFormatErrorsWithNonValidationError(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"plain error", errors.New("database is on fire")},
		{"wrapped error", fmt.Errorf("outer: %w", errors.New("inner"))},
		{"invalid validation error", &govalidator.InvalidValidationError{}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := FormatErrors(tc.err)
			if len(got) != 1 {
				t.Fatalf("FormatErrors = %+v, want exactly one generic entry", got)
			}
			if got[0].Message != genericMessage {
				t.Fatalf("Message = %q, want %q", got[0].Message, genericMessage)
			}
			// The underlying error text must not reach the client.
			if strings.Contains(got[0].Message, "fire") || strings.Contains(got[0].Message, "inner") {
				t.Fatalf("Message leaks the internal error: %q", got[0].Message)
			}
		})
	}
}

func TestFormatErrorsUnwrapsAWrappedValidationError(t *testing.T) {
	resetInstance(t)
	Setup(nil)

	type dto struct {
		Name string `json:"name" validate:"required"`
	}

	err := Instance().Struct(dto{})
	wrapped := fmt.Errorf("binding failed: %w", err)

	got := FormatErrors(wrapped)
	if len(got) != 1 {
		t.Fatalf("FormatErrors = %+v, want 1", got)
	}
	if got[0].Field != "name" || got[0].Tag != "required" {
		t.Fatalf("got = %+v, want field=name tag=required", got[0])
	}
}

// dtoForTag builds a one-field struct on the fly so every message can be exercised
// through the real validator rather than a hand-made FieldError stub.
type messageCase struct {
	name    string
	dto     any
	wantMsg string
}

func TestMessageForTagCoversEveryDocumentedTag(t *testing.T) {
	resetInstance(t)
	Setup(nil)

	type reqStr struct {
		F string `json:"f" validate:"required"`
	}
	type reqIf struct {
		Kind string `json:"kind"`
		F    string `json:"f" validate:"required_if=Kind company"`
	}
	type reqWith struct {
		Other string `json:"other"`
		F     string `json:"f" validate:"required_with=Other"`
	}
	type exclWith struct {
		Other string `json:"other"`
		F     string `json:"f" validate:"excluded_with=Other"`
	}
	type email struct {
		F string `json:"f" validate:"email"`
	}
	type minStr struct {
		F string `json:"f" validate:"min=8"`
	}
	type minNum struct {
		F int `json:"f" validate:"min=8"`
	}
	type minSlice struct {
		F []string `json:"f" validate:"min=2"`
	}
	type maxStr struct {
		F string `json:"f" validate:"max=3"`
	}
	type maxNum struct {
		F int `json:"f" validate:"max=3"`
	}
	type maxSlice struct {
		F []string `json:"f" validate:"max=1"`
	}
	type lenStr struct {
		F string `json:"f" validate:"len=4"`
	}
	type lenSlice struct {
		F []int `json:"f" validate:"len=2"`
	}
	type gtN struct {
		F int `json:"f" validate:"gt=5"`
	}
	type gteN struct {
		F int `json:"f" validate:"gte=5"`
	}
	type ltN struct {
		F int `json:"f" validate:"lt=5"`
	}
	type lteN struct {
		F int `json:"f" validate:"lte=5"`
	}
	type eqField struct {
		A string `json:"a"`
		F string `json:"f" validate:"eqfield=A"`
	}
	type neField struct {
		A string `json:"a"`
		F string `json:"f" validate:"nefield=A"`
	}
	type gtField struct {
		A int `json:"a"`
		F int `json:"f" validate:"gtfield=A"`
	}
	type ltField struct {
		A int `json:"a"`
		F int `json:"f" validate:"ltfield=A"`
	}
	type oneOf struct {
		F string `json:"f" validate:"oneof=red green"`
	}
	type number struct {
		F string `json:"f" validate:"number"`
	}
	type numeric struct {
		F string `json:"f" validate:"numeric"`
	}
	type alpha struct {
		F string `json:"f" validate:"alpha"`
	}
	type alphanum struct {
		F string `json:"f" validate:"alphanum"`
	}
	type boolean struct {
		F string `json:"f" validate:"boolean"`
	}
	type datetime struct {
		F string `json:"f" validate:"datetime=2006-01-02"`
	}
	type urlT struct {
		F string `json:"f" validate:"url"`
	}
	type uuidT struct {
		F string `json:"f" validate:"uuid"`
	}
	type uuid4T struct {
		F string `json:"f" validate:"uuid4"`
	}
	type jwtT struct {
		F string `json:"f" validate:"jwt"`
	}
	type jsonT struct {
		F string `json:"f" validate:"json"`
	}
	type base64T struct {
		F string `json:"f" validate:"base64"`
	}
	type hexT struct {
		F string `json:"f" validate:"hexadecimal"`
	}
	type containsT struct {
		F string `json:"f" validate:"contains=abc"`
	}
	type startsWith struct {
		F string `json:"f" validate:"startswith=pre"`
	}
	type endsWith struct {
		F string `json:"f" validate:"endswith=post"`
	}
	type uniqueT struct {
		F []int `json:"f" validate:"unique"`
	}
	type ipT struct {
		F string `json:"f" validate:"ip"`
	}
	type ipv4T struct {
		F string `json:"f" validate:"ipv4"`
	}
	type cidrT struct {
		F string `json:"f" validate:"cidr"`
	}
	type hostnameT struct {
		F string `json:"f" validate:"hostname"`
	}
	type irMobile struct {
		F string `json:"f" validate:"ir_mobile"`
	}
	type objectID struct {
		F string `json:"f" validate:"objectid"`
	}
	type nationalCode struct {
		F string `json:"f" validate:"ir_national_code"`
	}
	type strongPassword struct {
		F string `json:"f" validate:"password_strong"`
	}
	type slugT struct {
		F string `json:"f" validate:"slug"`
	}
	type noHTML struct {
		F string `json:"f" validate:"no_html"`
	}
	type safeFilename struct {
		F string `json:"f" validate:"safe_filename"`
	}
	type unknownTag struct {
		F string `json:"f" validate:"unknown_rule=42"`
	}
	type unknownTagNoParam struct {
		F string `json:"f" validate:"unknown_rule_no_param"`
	}

	// Rules the framework does not know about must still produce a message, and
	// must never fall through to e.Error().
	if err := Set("unknown_rule", func(fl govalidator.FieldLevel) bool { return false }); err != nil {
		t.Fatalf("Set error = %v", err)
	}
	if err := Set("unknown_rule_no_param", func(fl govalidator.FieldLevel) bool { return false }); err != nil {
		t.Fatalf("Set error = %v", err)
	}

	cases := []messageCase{
		{"required", reqStr{}, "This field is required"},
		{"required_if", reqIf{Kind: "company"}, "This field is required for the submitted values"},
		{"required_with", reqWith{Other: "x"}, "This field is required when the related field is present"},
		{"excluded_with", exclWith{Other: "x", F: "y"}, "This field must be omitted when the related field is present"},
		{"email", email{F: "nope"}, "Invalid email format"},

		{"min on string", minStr{F: "abc"}, "Must be at least 8 characters"},
		{"min on number", minNum{F: 1}, "Must be at least 8"},
		{"min on slice", minSlice{F: []string{"a"}}, "Must contain at least 2 items"},
		{"max on string", maxStr{F: "abcd"}, "Must be at most 3 characters"},
		{"max on number", maxNum{F: 9}, "Must be at most 3"},
		{"max on slice", maxSlice{F: []string{"a", "b"}}, "Must contain at most 1 items"},
		{"len on string", lenStr{F: "ab"}, "Must be exactly 4 characters"},
		{"len on slice", lenSlice{F: []int{1}}, "Must contain exactly 2 items"},

		{"gt", gtN{F: 1}, "Must be greater than 5"},
		{"gte", gteN{F: 1}, "Must be greater than or equal to 5"},
		{"lt", ltN{F: 9}, "Must be less than 5"},
		{"lte", lteN{F: 9}, "Must be less than or equal to 5"},
		{"eqfield", eqField{A: "a", F: "b"}, "Must match the related field"},
		{"nefield", neField{A: "a", F: "a"}, "Must differ from the related field"},
		{"gtfield", gtField{A: 5, F: 1}, "Must be greater than the related field"},
		{"ltfield", ltField{A: 1, F: 5}, "Must be less than the related field"},

		{"oneof", oneOf{F: "blue"}, "Must be one of: red green"},
		{"number", number{F: "abc"}, "Must be a number"},
		{"numeric", numeric{F: "abc"}, "Must be a number"},
		{"alpha", alpha{F: "abc123"}, "Must contain letters only"},
		{"alphanum", alphanum{F: "abc-123"}, "Must contain letters and digits only"},
		{"boolean", boolean{F: "yes"}, "Must be true or false"},
		{"datetime", datetime{F: "31/12/2024"}, "Must be a date in the format 2006-01-02"},
		{"url", urlT{F: "not a url"}, "Invalid URL format"},
		{"uuid", uuidT{F: "abc"}, "Must be a valid UUID"},
		{"uuid4", uuid4T{F: "abc"}, "Must be a valid UUID"},
		{"jwt", jwtT{F: "abc"}, "Must be a valid token"},
		{"json", jsonT{F: "{oops"}, "Must be valid JSON"},
		{"base64", base64T{F: "!!!"}, "Must be valid base64"},
		{"hexadecimal", hexT{F: "zz"}, "Must be a hexadecimal value"},

		{"contains", containsT{F: "xyz"}, `Must contain "abc"`},
		{"startswith", startsWith{F: "xyz"}, `Must start with "pre"`},
		{"endswith", endsWith{F: "xyz"}, `Must end with "post"`},
		{"unique", uniqueT{F: []int{1, 1}}, "Must not contain duplicate values"},

		{"ip", ipT{F: "999.1.1.1"}, "Must be a valid IP address"},
		{"ipv4", ipv4T{F: "::1"}, "Must be a valid IPv4 address"},
		{"cidr", cidrT{F: "10.0.0.1"}, "Must be a valid CIDR range"},
		{"hostname", hostnameT{F: "not a host"}, "Must be a valid hostname"},

		{"ir_mobile", irMobile{F: "123"}, "Mobile number is not valid"},
		{"objectid", objectID{F: "123"}, "ObjectId not valid"},
		{"ir_national_code", nationalCode{F: "123"}, "National code is not valid"},
		{
			"password_strong",
			strongPassword{F: "weak"},
			"Must be at least 8 characters and include an upper-case letter, a lower-case letter and a digit",
		},
		{"slug", slugT{F: "Not A Slug"}, "Must be lower-case letters, digits and single hyphens"},
		{"no_html", noHTML{F: "<b>x</b>"}, "Must not contain HTML"},
		{"safe_filename", safeFilename{F: "../etc/passwd"}, "Must be a valid file name without path separators"},

		{"unknown tag with a param", unknownTag{F: "x"}, `Failed the "unknown_rule" rule (42)`},
		{"unknown tag without a param", unknownTagNoParam{F: "x"}, `Failed the "unknown_rule_no_param" rule`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			errs := ValidateStruct(tc.dto)
			if len(errs) == 0 {
				t.Fatalf("ValidateStruct(%T) = no errors, want a failure to inspect", tc.dto)
			}
			if errs[0].Message != tc.wantMsg {
				t.Fatalf("Message = %q, want %q", errs[0].Message, tc.wantMsg)
			}
			if errs[0].Field != "f" {
				t.Fatalf("Field = %q, want %q", errs[0].Field, "f")
			}
		})
	}
}

// TestMessagesNeverLeakGoInternals is the regression test for the information
// disclosure: messageForTag's default branch used to return e.Error(), which
// embeds the Go struct name, the Go field name and the validator's own phrasing.
func TestMessagesNeverLeakGoInternals(t *testing.T) {
	resetInstance(t)
	Setup(nil)

	type secretInternalDto struct {
		HashedPassword string `json:"password" validate:"custom_secret_rule"`
	}

	if err := Set("custom_secret_rule", func(fl govalidator.FieldLevel) bool { return false }); err != nil {
		t.Fatalf("Set error = %v", err)
	}

	errs := ValidateStruct(secretInternalDto{HashedPassword: "x"})
	if len(errs) != 1 {
		t.Fatalf("errors = %+v, want 1", errs)
	}

	forbidden := []string{
		"secretInternalDto",      // Go type name
		"HashedPassword",         // Go field name
		"Key:",                   // validator's own phrasing
		"Error:Field validation", // ditto
	}
	for _, needle := range forbidden {
		if strings.Contains(errs[0].Message, needle) {
			t.Fatalf("Message %q leaks %q", errs[0].Message, needle)
		}
	}
	if errs[0].Field != "password" {
		t.Fatalf("Field = %q, want the JSON name %q", errs[0].Field, "password")
	}
}

func TestFieldErrorJSONShapeIsBackwardCompatible(t *testing.T) {
	resetInstance(t)
	Setup(nil)

	type dto struct {
		Name string `json:"name" validate:"min=3"`
	}

	errs := ValidateStruct(dto{Name: "a"})
	if len(errs) != 1 {
		t.Fatalf("errors = %+v, want 1", errs)
	}

	// Field and Message must remain the first two struct fields with their original
	// JSON names so an existing client keeps working unchanged.
	typ := reflect.TypeOf(FieldError{})
	if typ.Field(0).Name != "Field" || typ.Field(0).Tag.Get("json") != "field" {
		t.Fatalf("field 0 = %s/%q, want Field/field", typ.Field(0).Name, typ.Field(0).Tag.Get("json"))
	}
	if typ.Field(1).Name != "Message" || typ.Field(1).Tag.Get("json") != "message" {
		t.Fatalf("field 1 = %s/%q, want Message/message", typ.Field(1).Name, typ.Field(1).Tag.Get("json"))
	}

	if errs[0].Param != "3" {
		t.Fatalf("Param = %q, want %q", errs[0].Param, "3")
	}
}

func TestIsNumericAndIsCountable(t *testing.T) {
	numeric := []any{int(1), int8(1), int16(1), int32(1), int64(1),
		uint(1), uint8(1), uint16(1), uint32(1), uint64(1), float32(1), float64(1)}
	for _, v := range numeric {
		if !isNumeric(reflect.ValueOf(v).Kind()) {
			t.Fatalf("isNumeric(%T) = false, want true", v)
		}
	}

	notNumeric := []any{"s", true, []int{}, map[string]int{}, time.Time{}}
	for _, v := range notNumeric {
		if isNumeric(reflect.ValueOf(v).Kind()) {
			t.Fatalf("isNumeric(%T) = true, want false", v)
		}
	}

	countable := []any{[]int{}, [2]int{}, map[string]int{}}
	for _, v := range countable {
		if !isCountable(reflect.ValueOf(v).Kind()) {
			t.Fatalf("isCountable(%T) = false, want true", v)
		}
	}
	if isCountable(reflect.String) {
		t.Fatal("isCountable(String) = true, want false")
	}
}

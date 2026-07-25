package validator

import (
	"reflect"
	"sync"
	"testing"

	govalidator "github.com/go-playground/validator/v10"
)

// resetInstance restores the package-level instance after a test mutates it, so
// tests do not leak configuration into each other.
func resetInstance(t *testing.T) {
	t.Helper()
	instanceMu.Lock()
	previous := V
	instanceMu.Unlock()

	t.Cleanup(func() {
		instanceMu.Lock()
		V = previous
		instanceMu.Unlock()
	})
}

func TestInstanceLazilyInitialisesInsteadOfPanicking(t *testing.T) {
	resetInstance(t)

	instanceMu.Lock()
	V = nil
	instanceMu.Unlock()

	// Before the fix, reading the nil global from a request handler panicked with a
	// nil dereference inside go-playground.
	got := Instance()
	if got == nil {
		t.Fatal("Instance() = nil, want a usable instance")
	}
	// A second call must hand back the same instance, not build a new one.
	if Instance() != got {
		t.Fatal("Instance() returned a different instance on the second call")
	}
	// The lazily created default must carry the custom rules, or a struct would
	// validate differently depending on whether Setup ran.
	type dto struct {
		Mobile string `json:"mobile" validate:"ir_mobile"`
	}
	if errs := ValidateStruct(dto{Mobile: "not-a-mobile"}); len(errs) != 1 {
		t.Fatalf("ValidateStruct errors = %d, want 1: the lazy default is missing the custom rules", len(errs))
	}
}

func TestSetWorksBeforeSetup(t *testing.T) {
	resetInstance(t)

	instanceMu.Lock()
	V = nil
	instanceMu.Unlock()

	// Set used to dereference the nil global.
	if err := Set("always_fails", func(fl govalidator.FieldLevel) bool { return false }); err != nil {
		t.Fatalf("Set error = %v, want nil", err)
	}

	type dto struct {
		Field string `json:"field" validate:"always_fails"`
	}
	errs := ValidateStruct(dto{Field: "anything"})
	if len(errs) != 1 || errs[0].Tag != "always_fails" {
		t.Fatalf("ValidateStruct errors = %+v, want one failure on always_fails", errs)
	}
}

func TestValidateStructDoesNotPanicWhenUninitialised(t *testing.T) {
	resetInstance(t)

	instanceMu.Lock()
	V = nil
	instanceMu.Unlock()

	type dto struct {
		Name string `json:"name" validate:"required"`
	}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("ValidateStruct panicked: %v", r)
		}
	}()

	if errs := ValidateStruct(dto{}); len(errs) != 1 {
		t.Fatalf("ValidateStruct errors = %+v, want one failure", errs)
	}
}

func TestSetupRegistersInDIContainerAndSwapsTheInstance(t *testing.T) {
	resetInstance(t)

	// Passing a nil app is supported so this can be exercised without booting a
	// whole application.
	v := Setup(nil)
	if v == nil {
		t.Fatal("Setup returned nil")
	}
	if Instance() != v {
		t.Fatal("Instance() does not return the instance Setup built")
	}
}

func TestInstanceIsSafeForConcurrentUse(t *testing.T) {
	resetInstance(t)

	instanceMu.Lock()
	V = nil
	instanceMu.Unlock()

	type dto struct {
		Email string `json:"email" validate:"required,email"`
	}

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if Instance() == nil {
				t.Error("Instance() = nil")
			}
			ValidateStruct(dto{Email: "nope"})
		}()
	}
	wg.Wait()
}

func TestJSONFieldNameMapping(t *testing.T) {
	type nested struct {
		Value string `json:"value"`
	}
	type sample struct {
		FirstName string  `json:"first_name"`
		WithOpts  string  `json:"with_opts,omitempty"`
		Skipped   string  `json:"-"`
		NoTag     string  ``
		EmptyName string  `json:",omitempty"`
		Nested    nested  `json:"nested"`
		Pointer   *nested `json:"pointer"`
	}

	typ := reflect.TypeOf(sample{})
	cases := []struct {
		field string
		want  string
	}{
		{"FirstName", "first_name"},
		{"WithOpts", "with_opts"},
		// `json:"-"` has no wire name, so the Go name is the only sensible answer.
		{"Skipped", "Skipped"},
		{"NoTag", "NoTag"},
		{"EmptyName", "EmptyName"},
		{"Nested", "nested"},
		{"Pointer", "pointer"},
	}

	for _, tc := range cases {
		t.Run(tc.field, func(t *testing.T) {
			field, ok := typ.FieldByName(tc.field)
			if !ok {
				t.Fatalf("no field %q", tc.field)
			}
			if got := jsonFieldName(field); got != tc.want {
				t.Fatalf("jsonFieldName(%s) = %q, want %q", tc.field, got, tc.want)
			}
		})
	}
}

func TestFieldErrorReportsTheJSONNameNotTheGoName(t *testing.T) {
	resetInstance(t)
	Setup(nil)

	type dto struct {
		FirstName string `json:"first_name" validate:"required"`
		LastName  string `json:"last_name,omitempty" validate:"required"`
	}

	errs := ValidateStruct(dto{})
	if len(errs) != 2 {
		t.Fatalf("errors = %d, want 2", len(errs))
	}

	// The old behaviour returned "FirstName", which no client could map back to
	// the input it submitted.
	want := map[string]bool{"first_name": true, "last_name": true}
	for _, e := range errs {
		if !want[e.Field] {
			t.Fatalf("Field = %q, want the JSON name (one of first_name, last_name)", e.Field)
		}
	}
}

func TestFieldErrorNamespaceLocatesNestedAndIndexedFields(t *testing.T) {
	resetInstance(t)
	Setup(nil)

	type item struct {
		Price int `json:"price" validate:"gt=0"`
	}
	type order struct {
		Items []item `json:"items" validate:"required,dive"`
	}

	errs := ValidateStruct(order{Items: []item{{Price: 5}, {Price: 0}}})
	if len(errs) != 1 {
		t.Fatalf("errors = %+v, want 1", errs)
	}
	if errs[0].Namespace != "order.items[1].price" {
		t.Fatalf("Namespace = %q, want %q", errs[0].Namespace, "order.items[1].price")
	}
	if errs[0].Field != "price" {
		t.Fatalf("Field = %q, want %q", errs[0].Field, "price")
	}
	if errs[0].Tag != "gt" || errs[0].Param != "0" {
		t.Fatalf("Tag/Param = %q/%q, want gt/0", errs[0].Tag, errs[0].Param)
	}
}

// runRule validates a single string field against tag and reports whether it
// passed.
func runRule(t *testing.T, tag, value string) bool {
	t.Helper()
	v := newValidate()
	err := v.Var(value, tag)
	return err == nil
}

func TestValidateIRMobile(t *testing.T) {
	cases := []struct {
		value string
		want  bool
	}{
		{"09123456789", true},
		{"09000000000", true},
		{"9123456789", false},   // missing leading zero
		{"0912345678", false},   // too short
		{"091234567890", false}, // too long
		{"08123456789", false},  // wrong prefix
		{"0912345678a", false},
		{"+989123456789", false},
		{"", false},
		{" 09123456789", false},
	}

	for _, tc := range cases {
		t.Run(tc.value, func(t *testing.T) {
			if got := runRule(t, "ir_mobile", tc.value); got != tc.want {
				t.Fatalf("ir_mobile(%q) = %v, want %v", tc.value, got, tc.want)
			}
		})
	}
}

func TestValidateObjectID(t *testing.T) {
	cases := []struct {
		value string
		want  bool
	}{
		{"507f1f77bcf86cd799439011", true},
		{"000000000000000000000000", true},
		{"507F1F77BCF86CD799439011", false}, // upper case is not accepted
		{"507f1f77bcf86cd79943901", false},  // 23 chars
		{"507f1f77bcf86cd7994390111", false},
		{"507f1f77bcf86cd79943901z", false},
		{"", false},
	}

	for _, tc := range cases {
		t.Run(tc.value, func(t *testing.T) {
			if got := runRule(t, "objectid", tc.value); got != tc.want {
				t.Fatalf("objectid(%q) = %v, want %v", tc.value, got, tc.want)
			}
		})
	}
}

func TestValidateIRNationalCode(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  bool
	}{
		// Checksums verified by hand against the mod-11 algorithm.
		{"valid, control digit equals remainder", "0499370899", true},
		{"valid, control digit is 11 minus remainder", "0790419904", true},
		{"valid with leading zeros", "0011115556", true},
		{"valid, remainder below two", "0000000061", true},
		{"valid, control digit zero", "0000000140", true},
		{"wrong control digit", "0499370898", false},
		{"all zeros", "0000000000", false},
		// "1111111111" satisfies the mod-11 arithmetic but is not an issued
		// code, so the repeated-digit rule must still reject it.
		{"all ones, checksum-valid but not issued", "1111111111", false},
		{"all nines", "9999999999", false},
		{"too short", "049937089", false},
		{"too long", "04993708999", false},
		{"non digits", "04993708a9", false},
		{"empty", "", false},
		{"with separators", "049-937-0899", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := runRule(t, "ir_national_code", tc.value); got != tc.want {
				t.Fatalf("ir_national_code(%q) = %v, want %v", tc.value, got, tc.want)
			}
		})
	}
}

// TestValidateIRNationalCodeChecksumIsActuallyChecked proves the rule is not just
// a length-and-digits test: for a fixed 9-digit prefix exactly one of the ten
// possible control digits may be accepted.
func TestValidateIRNationalCodeChecksumIsActuallyChecked(t *testing.T) {
	const prefix = "049937089"

	accepted := 0
	for digit := '0'; digit <= '9'; digit++ {
		if runRule(t, "ir_national_code", prefix+string(digit)) {
			accepted++
		}
	}
	if accepted != 1 {
		t.Fatalf("accepted %d of 10 control digits, want exactly 1", accepted)
	}
}

func TestValidatePasswordStrong(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  bool
	}{
		{"meets every requirement", "Passw0rd", true},
		{"long and mixed", "Sup3rSecretPhrase", true},
		{"symbols allowed", "Aa1!@#$%", true},
		{"non-ascii counted by rune", "Pässwörd1", true},
		{"seven characters", "Passw0r", false},
		{"no upper case", "passw0rd", false},
		{"no lower case", "PASSW0RD", false},
		{"no digit", "PasswordX", false},
		{"digits only", "12345678", false},
		{"empty", "", false},
		// Eight bytes but only four runes: length must be measured in runes.
		{"multibyte too short", "密码密码", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := runRule(t, "password_strong", tc.value); got != tc.want {
				t.Fatalf("password_strong(%q) = %v, want %v", tc.value, got, tc.want)
			}
		})
	}
}

func TestValidateSlug(t *testing.T) {
	cases := []struct {
		value string
		want  bool
	}{
		{"hello", true},
		{"hello-world", true},
		{"a-b-c-1-2-3", true},
		{"123", true},
		{"Hello", false},        // upper case
		{"hello_world", false},  // underscore
		{"-hello", false},       // leading hyphen
		{"hello-", false},       // trailing hyphen
		{"hello--world", false}, // doubled hyphen
		{"hello world", false},
		{"héllo", false},
		{"", false},
	}

	for _, tc := range cases {
		t.Run(tc.value, func(t *testing.T) {
			if got := runRule(t, "slug", tc.value); got != tc.want {
				t.Fatalf("slug(%q) = %v, want %v", tc.value, got, tc.want)
			}
		})
	}
}

func TestValidateNoHTML(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  bool
	}{
		{"plain text", "Sajjad Mohammadi", true},
		{"punctuation", "O'Brien & Sons, Ltd.", true},
		{"empty", "", true},
		{"script tag", "<script>alert(1)</script>", false},
		{"open bracket only", "a < b", false},
		{"close bracket only", "a > b", false},
		{"img onerror", `<img src=x onerror=alert(1)>`, false},
		{"encoded is not caught", "&lt;script&gt;", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := runRule(t, "no_html", tc.value); got != tc.want {
				t.Fatalf("no_html(%q) = %v, want %v", tc.value, got, tc.want)
			}
		})
	}
}

func TestValidateSafeFilename(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  bool
	}{
		{"simple", "report.pdf", true},
		{"with underscore and hyphen", "my_report-2024.pdf", true},
		{"no extension", "README", true},
		{"leading dot", ".gitignore", true},
		{"empty", "", false},
		{"dot", ".", false},
		{"dot dot", "..", false},
		{"parent traversal", "../etc/passwd", false},
		{"traversal in the middle", "a/../../b", false},
		{"forward slash", "dir/file.txt", false},
		{"back slash", `dir\file.txt`, false},
		{"absolute", "/etc/passwd", false},
		{"newline", "file\n.txt", false},
		{"null byte", "file\x00.txt", false},
		{"space", "my report.pdf", false},
		{"colon", "C:file.txt", false},
		{"wildcard", "*.pdf", false},
		{"double dot extension trick", "file..pdf", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := runRule(t, "safe_filename", tc.value); got != tc.want {
				t.Fatalf("safe_filename(%q) = %v, want %v", tc.value, got, tc.want)
			}
		})
	}
}

func TestEveryCustomRuleIsRegistered(t *testing.T) {
	v := newValidate()

	for tag := range customValidations {
		t.Run(tag, func(t *testing.T) {
			// Var panics with "Undefined validation function" for an unregistered
			// tag, so reaching this point at all proves registration happened.
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("tag %q is not registered: %v", tag, r)
				}
			}()
			_ = v.Var("some-value", tag)
		})
	}
}

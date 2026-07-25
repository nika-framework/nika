package validator

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/nika-framework/nika/common/response"
)

func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	m.Run()
}

// jsonBodyContext builds a context carrying body as a JSON request.
func jsonBodyContext(t *testing.T, body string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	return c, rec
}

// decodeError reads the error envelope out of a recorder.
func decodeError(t *testing.T, rec *httptest.ResponseRecorder) response.Error {
	t.Helper()
	var got response.Error
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("Unmarshal(%q) error = %v", rec.Body.String(), err)
	}
	return got
}

type createUser struct {
	FirstName string `json:"first_name" validate:"required,min=2"`
	Age       int    `json:"age" validate:"gte=0"`
	Email     string `json:"email" validate:"required,email"`
}

func TestBindAndValidate(t *testing.T) {
	resetInstance(t)
	Setup(nil)

	cases := []struct {
		name        string
		body        string
		wantOK      bool
		wantStatus  int
		wantType    string
		wantDetails string // substring expected in details, when details is a string
		forbidden   []string
	}{
		{
			name:       "valid payload",
			body:       `{"first_name":"Sajjad","age":30,"email":"a@b.com"}`,
			wantOK:     true,
			wantStatus: http.StatusOK,
		},
		{
			name:        "empty body",
			body:        ``,
			wantStatus:  http.StatusBadRequest,
			wantType:    "INVALID_JSON",
			wantDetails: "request body is empty",
			// io.EOF used to be forwarded verbatim as "EOF", which tells a client
			// nothing.
			forbidden: []string{"EOF"},
		},
		{
			name:        "malformed JSON",
			body:        `{"first_name": "x"`,
			wantStatus:  http.StatusBadRequest,
			wantType:    "INVALID_JSON",
			wantDetails: "unexpected end of input",
		},
		{
			name:        "syntax error mid document",
			body:        `{"first_name": }`,
			wantStatus:  http.StatusBadRequest,
			wantType:    "INVALID_JSON",
			wantDetails: "malformed JSON at byte",
		},
		{
			name:        "wrong type for a field",
			body:        `{"first_name":"x","age":"thirty","email":"a@b.com"}`,
			wantStatus:  http.StatusBadRequest,
			wantType:    "INVALID_JSON",
			wantDetails: "field age expects a number",
			// The decoder message names the Go struct and its Go field.
			forbidden: []string{"createUser", "Age", "cannot unmarshal"},
		},
		{
			name:       "fails validation",
			body:       `{"first_name":"S","age":-1,"email":"nope"}`,
			wantStatus: http.StatusUnprocessableEntity,
			wantType:   "VALIDATION_ERROR",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, rec := jsonBodyContext(t, tc.body)

			var dto createUser
			got := BindAndValidate(c, &dto)

			if got != tc.wantOK {
				t.Fatalf("BindAndValidate = %v, want %v (body: %s)", got, tc.wantOK, rec.Body.String())
			}
			if tc.wantOK {
				if c.IsAborted() {
					t.Fatal("context aborted on a successful bind")
				}
				if dto.FirstName != "Sajjad" || dto.Age != 30 {
					t.Fatalf("dto = %+v, want the decoded payload", dto)
				}
				return
			}

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			// Every failure path must stop the handler chain.
			if !c.IsAborted() {
				t.Fatal("IsAborted = false, want true: a failed bind must not fall through to the handler")
			}

			envelope := decodeError(t, rec)
			if envelope.Success {
				t.Fatal("Success = true, want false")
			}
			if envelope.Error == nil {
				t.Fatal("Error = nil, want an ErrorDetail")
			}
			if envelope.Error.Type != tc.wantType {
				t.Fatalf("Type = %q, want %q", envelope.Error.Type, tc.wantType)
			}

			body := rec.Body.String()
			if tc.wantDetails != "" && !strings.Contains(body, tc.wantDetails) {
				t.Fatalf("body %s does not contain %q", body, tc.wantDetails)
			}
			for _, needle := range tc.forbidden {
				if strings.Contains(body, needle) {
					t.Fatalf("body %s leaks %q", body, needle)
				}
			}
		})
	}
}

func TestBindAndValidateRejectsAnOversizedBodyWith413(t *testing.T) {
	resetInstance(t)
	Setup(nil)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := `{"first_name":"` + strings.Repeat("a", 512) + `","email":"a@b.com"}`
	c.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	// Mirrors what nika.BodyLimitMiddleware installs.
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 32)

	var dto createUser
	if BindAndValidate(c, &dto) {
		t.Fatal("BindAndValidate = true, want false")
	}
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusRequestEntityTooLarge, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "request body too large") {
		t.Fatalf("body = %s, want a size message", rec.Body.String())
	}
}

// TestBindAndValidateMicroservice asserts the transport-side entry point behaves
// exactly like the HTTP one, since a message handler is handed a synthesized
// context whose body is the message payload.
func TestBindAndValidateMicroservice(t *testing.T) {
	resetInstance(t)
	Setup(nil)

	cases := []struct {
		name       string
		body       string
		wantOK     bool
		wantStatus int
		wantType   string
	}{
		{"valid message", `{"first_name":"Ali","age":20,"email":"a@b.com"}`, true, http.StatusOK, ""},
		{"empty message", ``, false, http.StatusBadRequest, "INVALID_JSON"},
		{"malformed message", `{`, false, http.StatusBadRequest, "INVALID_JSON"},
		{"invalid message", `{"first_name":"","age":1,"email":"x"}`, false, http.StatusUnprocessableEntity, "VALIDATION_ERROR"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			microCtx, microRec := jsonBodyContext(t, tc.body)
			httpCtx, httpRec := jsonBodyContext(t, tc.body)

			var microDTO, httpDTO createUser
			gotMicro := BindAndValidateMicroservice(microCtx, &microDTO)
			gotHTTP := BindAndValidate(httpCtx, &httpDTO)

			if gotMicro != tc.wantOK {
				t.Fatalf("BindAndValidateMicroservice = %v, want %v", gotMicro, tc.wantOK)
			}
			if gotMicro != gotHTTP {
				t.Fatalf("microservice result %v differs from HTTP result %v", gotMicro, gotHTTP)
			}
			if microRec.Code != httpRec.Code {
				t.Fatalf("status %d differs from the HTTP path's %d", microRec.Code, httpRec.Code)
			}
			if !reflect.DeepEqual(microDTO, httpDTO) {
				t.Fatalf("bound dto %+v differs from the HTTP path's %+v", microDTO, httpDTO)
			}

			if tc.wantOK {
				if microDTO.FirstName != "Ali" {
					t.Fatalf("dto = %+v, want the decoded payload", microDTO)
				}
				return
			}
			if microRec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", microRec.Code, tc.wantStatus)
			}
			if got := decodeError(t, microRec); got.Error == nil || got.Error.Type != tc.wantType {
				t.Fatalf("error = %+v, want type %q", got.Error, tc.wantType)
			}
			if !microCtx.IsAborted() {
				t.Fatal("IsAborted = false, want true")
			}
		})
	}
}

func TestBindAndValidateQuery(t *testing.T) {
	resetInstance(t)
	Setup(nil)

	type query struct {
		Page    int    `form:"page" validate:"gte=1"`
		Keyword string `form:"q" validate:"required"`
	}

	cases := []struct {
		name       string
		rawQuery   string
		wantOK     bool
		wantStatus int
		wantType   string
	}{
		{"valid", "page=2&q=nika", true, http.StatusOK, ""},
		{"unparseable int", "page=abc&q=nika", false, http.StatusBadRequest, "INVALID_QUERY"},
		{"fails validation", "page=0&q=", false, http.StatusUnprocessableEntity, "VALIDATION_ERROR"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodGet, "/?"+tc.rawQuery, nil)

			var dto query
			if got := BindAndValidateQuery(c, &dto); got != tc.wantOK {
				t.Fatalf("BindAndValidateQuery = %v, want %v (body: %s)", got, tc.wantOK, rec.Body.String())
			}
			if tc.wantOK {
				return
			}
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if got := decodeError(t, rec); got.Error == nil || got.Error.Type != tc.wantType {
				t.Fatalf("error = %+v, want type %q", got.Error, tc.wantType)
			}
			if !c.IsAborted() {
				t.Fatal("IsAborted = false, want true")
			}
		})
	}
}

func TestBindAndValidateUri(t *testing.T) {
	resetInstance(t)
	Setup(nil)

	type params struct {
		ID string `uri:"id" validate:"objectid"`
	}

	cases := []struct {
		name   string
		id     string
		wantOK bool
	}{
		{"valid object id", "507f1f77bcf86cd799439011", true},
		{"invalid object id", "not-an-id", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodGet, "/users/"+tc.id, nil)
			c.Params = gin.Params{{Key: "id", Value: tc.id}}

			var dto params
			if got := BindAndValidateUri(c, &dto); got != tc.wantOK {
				t.Fatalf("BindAndValidateUri = %v, want %v (body: %s)", got, tc.wantOK, rec.Body.String())
			}
			if tc.wantOK {
				return
			}
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnprocessableEntity)
			}
			if !c.IsAborted() {
				t.Fatal("IsAborted = false, want true")
			}
		})
	}
}

func TestBindAndValidateForm(t *testing.T) {
	resetInstance(t)
	Setup(nil)

	type login struct {
		Username string `form:"username" validate:"required,slug"`
		Password string `form:"password" validate:"required,password_strong"`
	}

	cases := []struct {
		name       string
		form       url.Values
		wantOK     bool
		wantStatus int
	}{
		{
			name:   "valid",
			form:   url.Values{"username": {"sajjad"}, "password": {"Passw0rd"}},
			wantOK: true,
		},
		{
			name:       "weak password",
			form:       url.Values{"username": {"sajjad"}, "password": {"weak"}},
			wantStatus: http.StatusUnprocessableEntity,
		},
		{
			name:       "missing fields",
			form:       url.Values{},
			wantStatus: http.StatusUnprocessableEntity,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tc.form.Encode()))
			c.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

			var dto login
			if got := BindAndValidateForm(c, &dto); got != tc.wantOK {
				t.Fatalf("BindAndValidateForm = %v, want %v (body: %s)", got, tc.wantOK, rec.Body.String())
			}
			if tc.wantOK {
				if dto.Username != "sajjad" {
					t.Fatalf("dto = %+v, want the decoded form", dto)
				}
				return
			}
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if !c.IsAborted() {
				t.Fatal("IsAborted = false, want true")
			}
		})
	}
}

func TestBindAndValidateHeader(t *testing.T) {
	resetInstance(t)
	Setup(nil)

	type headers struct {
		APIVersion string `header:"X-Api-Version" validate:"required,oneof=v1 v2"`
	}

	cases := []struct {
		name   string
		value  string
		wantOK bool
	}{
		{"accepted version", "v2", true},
		{"unknown version", "v9", false},
		{"missing header", "", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.value != "" {
				c.Request.Header.Set("X-Api-Version", tc.value)
			}

			var dto headers
			if got := BindAndValidateHeader(c, &dto); got != tc.wantOK {
				t.Fatalf("BindAndValidateHeader = %v, want %v (body: %s)", got, tc.wantOK, rec.Body.String())
			}
			if !tc.wantOK && !c.IsAborted() {
				t.Fatal("IsAborted = false, want true")
			}
		})
	}
}

func TestBindGeneric(t *testing.T) {
	resetInstance(t)
	Setup(nil)

	t.Run("valid", func(t *testing.T) {
		c, _ := jsonBodyContext(t, `{"first_name":"Reza","age":33,"email":"r@e.com"}`)

		dto, ok := Bind[createUser](c)
		if !ok {
			t.Fatal("Bind = false, want true")
		}
		if dto == nil {
			t.Fatal("Bind returned a nil dto alongside ok")
		}
		if dto.FirstName != "Reza" || dto.Email != "r@e.com" {
			t.Fatalf("dto = %+v, want the decoded payload", *dto)
		}
	})

	t.Run("invalid returns nil", func(t *testing.T) {
		c, rec := jsonBodyContext(t, `{"first_name":"R"}`)

		dto, ok := Bind[createUser](c)
		if ok {
			t.Fatal("Bind = true, want false")
		}
		if dto != nil {
			t.Fatalf("Bind returned %+v, want nil on failure", dto)
		}
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnprocessableEntity)
		}
	})
}

func TestBindErrorMessage(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"eof", io.EOF, "request body is empty"},
		{"unexpected eof", io.ErrUnexpectedEOF, "malformed JSON: unexpected end of input"},
		{"syntax", &json.SyntaxError{Offset: 17}, "malformed JSON at byte 17"},
		{
			"type mismatch",
			&json.UnmarshalTypeError{Field: "age", Type: reflect.TypeOf(0)},
			"field age expects a number",
		},
		{
			"type mismatch without a field name",
			&json.UnmarshalTypeError{Type: reflect.TypeOf("")},
			"field body expects a string",
		},
		{"unknown", errRandom{}, "request payload is not valid"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := bindErrorMessage(tc.err); got != tc.want {
				t.Fatalf("bindErrorMessage = %q, want %q", got, tc.want)
			}
		})
	}
}

type errRandom struct{}

func (errRandom) Error() string { return "some internal detail nobody should see" }

func TestJSONTypeNameHidesGoTypes(t *testing.T) {
	type internalThing struct{ X int }

	cases := []struct {
		value any
		want  string
	}{
		{"", "string"},
		{true, "boolean"},
		{0, "number"},
		{int64(0), "number"},
		{uint8(0), "number"},
		{0.0, "number"},
		{[]internalThing{}, "array"},
		{[2]int{}, "array"},
		{map[string]int{}, "object"},
		{internalThing{}, "object"},
		{&internalThing{}, "object"},
	}

	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			got := jsonTypeName(reflect.TypeOf(tc.value))
			if got != tc.want {
				t.Fatalf("jsonTypeName(%T) = %q, want %q", tc.value, got, tc.want)
			}
			if strings.Contains(got, "internalThing") {
				t.Fatalf("jsonTypeName leaked the Go type: %q", got)
			}
		})
	}

	if got := jsonTypeName(nil); got != "value" {
		t.Fatalf("jsonTypeName(nil) = %q, want %q", got, "value")
	}
}

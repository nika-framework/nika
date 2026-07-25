package response

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	// InternalServerError logs on every call; keep the test output readable.
	// TestInternalServerErrorLogsTheError installs its own capturing handler.
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.Run()
}

func newContext(t *testing.T) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/resource", nil)
	return c, rec
}

func decodeMap(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("Unmarshal(%q) error = %v", rec.Body.String(), err)
	}
	return got
}

// TestErrorRespondersAbortTheChain is the regression test for the authorization
// bypass: BadRequest, NotFoundRequest, UnprocessableEntity and JSONError used to
// call c.JSON, which writes a body but does NOT stop Gin, so a guard that rejected
// a request still let the protected handler run.
func TestErrorRespondersAbortTheChain(t *testing.T) {
	responders := []struct {
		name       string
		call       func(*gin.Context)
		wantStatus int
	}{
		{"BadRequest", func(c *gin.Context) { BadRequest(c, "BAD_REQUEST", nil) }, http.StatusBadRequest},
		{"UnauthorizedRequest", func(c *gin.Context) { UnauthorizedRequest(c, "UNAUTHORIZED", nil) }, http.StatusUnauthorized},
		{"ForbiddenRequest", func(c *gin.Context) { ForbiddenRequest(c, "FORBIDDEN", nil) }, http.StatusForbidden},
		{"NotFoundRequest", func(c *gin.Context) { NotFoundRequest(c, "NOT_FOUND", nil) }, http.StatusNotFound},
		{"Conflict", func(c *gin.Context) { Conflict(c, "CONFLICT", nil) }, http.StatusConflict},
		{"UnprocessableEntity", func(c *gin.Context) { UnprocessableEntity(c, "VALIDATION_ERROR", nil) }, http.StatusUnprocessableEntity},
		{"TooManyRequests", func(c *gin.Context) { TooManyRequests(c, "RATE_LIMITED", nil) }, http.StatusTooManyRequests},
		{"ServiceUnavailable", func(c *gin.Context) { ServiceUnavailable(c, "UNAVAILABLE", nil) }, http.StatusServiceUnavailable},
		{"InternalServerError", func(c *gin.Context) { InternalServerError(c, errors.New("boom")) }, http.StatusInternalServerError},
		{"JSONError", func(c *gin.Context) { JSONError(c, http.StatusTeapot, "TEAPOT", nil) }, http.StatusTeapot},
	}

	for _, tc := range responders {
		t.Run(tc.name+" sets IsAborted", func(t *testing.T) {
			c, rec := newContext(t)
			tc.call(c)

			if !c.IsAborted() {
				t.Fatal("IsAborted = false, want true: an error response must stop the handler chain")
			}
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
		})

		t.Run(tc.name+" stops a real middleware chain", func(t *testing.T) {
			handlerRan := false

			router := gin.New()
			router.GET("/resource",
				func(c *gin.Context) { tc.call(c); c.Next() },
				func(c *gin.Context) { handlerRan = true; c.String(http.StatusOK, "protected data") },
			)

			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/resource", nil))

			if handlerRan {
				t.Fatal("the protected handler ran after the guard rejected the request")
			}
			if strings.Contains(rec.Body.String(), "protected data") {
				t.Fatalf("the protected payload leaked into the response: %s", rec.Body.String())
			}
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
		})
	}
}

func TestErrorEnvelopeShape(t *testing.T) {
	cases := []struct {
		name       string
		call       func(*gin.Context)
		wantStatus int
		wantType   string
	}{
		{"BadRequest", func(c *gin.Context) { BadRequest(c, "INVALID_JSON", "request body is empty") }, http.StatusBadRequest, "INVALID_JSON"},
		{"UnauthorizedRequest", func(c *gin.Context) { UnauthorizedRequest(c, "TOKEN_EXPIRED", nil) }, http.StatusUnauthorized, "TOKEN_EXPIRED"},
		{"ForbiddenRequest", func(c *gin.Context) { ForbiddenRequest(c, "INSUFFICIENT_SCOPE", nil) }, http.StatusForbidden, "INSUFFICIENT_SCOPE"},
		{"NotFoundRequest", func(c *gin.Context) { NotFoundRequest(c, "USER_NOT_FOUND", nil) }, http.StatusNotFound, "USER_NOT_FOUND"},
		{"Conflict", func(c *gin.Context) { Conflict(c, "EMAIL_TAKEN", nil) }, http.StatusConflict, "EMAIL_TAKEN"},
		{"UnprocessableEntity", func(c *gin.Context) {
			UnprocessableEntity(c, "VALIDATION_ERROR", []map[string]string{{"field": "email", "message": "Invalid email format"}})
		}, http.StatusUnprocessableEntity, "VALIDATION_ERROR"},
		{"TooManyRequests", func(c *gin.Context) { TooManyRequests(c, "RATE_LIMITED", nil) }, http.StatusTooManyRequests, "RATE_LIMITED"},
		{"ServiceUnavailable", func(c *gin.Context) { ServiceUnavailable(c, "DB_DOWN", nil) }, http.StatusServiceUnavailable, "DB_DOWN"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, rec := newContext(t)
			tc.call(c)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}

			var got Error
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("Unmarshal error = %v (body: %s)", err, rec.Body.String())
			}
			if got.Success {
				t.Fatal("success = true, want false")
			}
			if got.Error == nil {
				t.Fatal("error = nil, want an ErrorDetail")
			}
			if got.Error.Code != tc.wantStatus {
				t.Fatalf("error.code = %d, want %d", got.Error.Code, tc.wantStatus)
			}
			// Type is the machine-readable code; Message stays populated for
			// backward compatibility.
			if got.Error.Type != tc.wantType {
				t.Fatalf("error.type = %q, want %q", got.Error.Type, tc.wantType)
			}
			if got.Error.Message != tc.wantType {
				t.Fatalf("error.message = %q, want %q", got.Error.Message, tc.wantType)
			}
		})
	}
}

func TestErrorDetailOmitsAnEmptyType(t *testing.T) {
	// `omitempty` on Type keeps the payload identical to the pre-change shape when
	// no code is supplied.
	raw, err := json.Marshal(NewError(http.StatusBadRequest, "", nil))
	if err != nil {
		t.Fatalf("Marshal error = %v", err)
	}
	if strings.Contains(string(raw), `"type"`) {
		t.Fatalf("payload = %s, want no type key when it is empty", raw)
	}
	if strings.Contains(string(raw), `"details"`) {
		t.Fatalf("payload = %s, want no details key when it is nil", raw)
	}
}

func TestInternalServerErrorNeverEchoesTheError(t *testing.T) {
	secret := errors.New(`pq: password authentication failed for user "app" on host db-primary.internal:5432`)

	c, rec := newContext(t)
	InternalServerError(c, secret)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}

	body := rec.Body.String()
	for _, needle := range []string{"password", "db-primary.internal", "5432", "pq:", "app"} {
		if strings.Contains(body, needle) {
			t.Fatalf("body %s leaks %q from the internal error", body, needle)
		}
	}

	var got Error
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("Unmarshal error = %v", err)
	}
	if got.Error == nil || got.Error.Type != "INTERNAL_SERVER_ERROR" {
		t.Fatalf("error = %+v, want type INTERNAL_SERVER_ERROR", got.Error)
	}
	if got.Error.Details != nil {
		t.Fatalf("details = %v, want nil when no request id is present", got.Error.Details)
	}
}

func TestInternalServerErrorIncludesTheRequestID(t *testing.T) {
	c, rec := newContext(t)
	c.Writer.Header().Set(RequestIDHeader, "req-abc-123")

	InternalServerError(c, errors.New("boom"))

	var got Error
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("Unmarshal error = %v", err)
	}
	details, ok := got.Error.Details.(map[string]any)
	if !ok {
		t.Fatalf("details = %#v, want an object", got.Error.Details)
	}
	if details["request_id"] != "req-abc-123" {
		t.Fatalf("details.request_id = %v, want %q", details["request_id"], "req-abc-123")
	}
}

// TestInternalServerErrorLogsTheError is the other half of the contract: the
// detail is withheld from the client precisely because it goes to the log, so an
// operator can still diagnose the failure.
func TestInternalServerErrorLogsTheError(t *testing.T) {
	var captured bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&captured, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	c, _ := newContext(t)
	c.Writer.Header().Set(RequestIDHeader, "req-42")
	InternalServerError(c, errors.New("connection refused talking to payments"))

	logged := captured.String()
	for _, needle := range []string{"connection refused talking to payments", "req-42", "/resource", "GET"} {
		if !strings.Contains(logged, needle) {
			t.Fatalf("log %q is missing %q", logged, needle)
		}
	}
}

func TestInternalServerErrorToleratesANilError(t *testing.T) {
	c, rec := newContext(t)

	// A handler may pass a nil error after a failed assertion; that must not
	// become a second panic.
	InternalServerError(c, nil)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

func TestRequestIDHeaderMatchesTheFrameworkHeader(t *testing.T) {
	// This package must not import the root nika package (see the package doc), so
	// the constant is duplicated. Pin the value so the two cannot drift silently.
	if RequestIDHeader != "X-Request-ID" {
		t.Fatalf("RequestIDHeader = %q, want %q (must match nika.RequestIDHeader)", RequestIDHeader, "X-Request-ID")
	}
}

func TestSuccessResponders(t *testing.T) {
	cases := []struct {
		name       string
		call       func(*gin.Context)
		wantStatus int
		wantBody   func(t *testing.T, rec *httptest.ResponseRecorder)
	}{
		{
			name:       "Ok",
			call:       func(c *gin.Context) { Ok(c, NewResponse("fetched", map[string]int{"id": 1})) },
			wantStatus: http.StatusOK,
			wantBody: func(t *testing.T, rec *httptest.ResponseRecorder) {
				got := decodeMap(t, rec)
				if got["success"] != true || got["message"] != "fetched" {
					t.Fatalf("body = %v, want success/message set", got)
				}
			},
		},
		{
			name:       "OkByMsg",
			call:       func(c *gin.Context) { OkByMsg(c, "done") },
			wantStatus: http.StatusOK,
			wantBody: func(t *testing.T, rec *httptest.ResponseRecorder) {
				got := decodeMap(t, rec)
				if got["success"] != true || got["message"] != "done" {
					t.Fatalf("body = %v, want {success:true, message:done}", got)
				}
				if _, hasData := got["data"]; hasData {
					t.Fatalf("body = %v, want no data key", got)
				}
			},
		},
		{
			name:       "Create",
			call:       func(c *gin.Context) { Create(c, NewResponse("created", nil)) },
			wantStatus: http.StatusCreated,
		},
		{
			name:       "Update",
			call:       func(c *gin.Context) { Update(c, NewResponse("updated", nil)) },
			wantStatus: http.StatusAccepted,
		},
		{
			name:       "Accepted",
			call:       func(c *gin.Context) { Accepted(c, NewResponse("queued", nil)) },
			wantStatus: http.StatusAccepted,
		},
		{
			name:       "NoContent",
			call:       NoContent,
			wantStatus: http.StatusNoContent,
			wantBody: func(t *testing.T, rec *httptest.ResponseRecorder) {
				if rec.Body.Len() != 0 {
					t.Fatalf("body = %q, want it empty: a 204 must not carry a body", rec.Body.String())
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, rec := newContext(t)
			tc.call(c)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			// A success response must never abort: middleware after the handler
			// (logging, metrics) still has to run.
			if c.IsAborted() {
				t.Fatal("IsAborted = true, want false for a success response")
			}
			if tc.wantBody != nil {
				tc.wantBody(t, rec)
			}
		})
	}
}

func TestPaginated(t *testing.T) {
	cases := []struct {
		name           string
		total          int64
		page           int
		perPage        int
		wantPage       int
		wantPerPage    int
		wantTotalPages int
	}{
		{"exact multiple", 100, 1, 10, 1, 10, 10},
		{"partial last page", 95, 3, 10, 3, 10, 10},
		{"single item", 1, 1, 10, 1, 10, 1},
		{"empty result set", 0, 1, 10, 1, 10, 0},
		{"one per page", 7, 2, 1, 2, 1, 7},
		// Out-of-range inputs are clamped rather than producing a divide-by-zero or
		// a negative page count.
		{"zero page clamped", 20, 0, 10, 1, 10, 2},
		{"negative page clamped", 20, -5, 10, 1, 10, 2},
		{"zero per page clamped", 20, 1, 0, 1, 1, 20},
		{"negative per page clamped", 20, 1, -3, 1, 1, 20},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, rec := newContext(t)
			Paginated(c, []string{"a", "b"}, tc.total, tc.page, tc.perPage)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
			}

			var got PageResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("Unmarshal error = %v (body: %s)", err, rec.Body.String())
			}
			if !got.Success {
				t.Fatal("success = false, want true")
			}
			if got.Meta.Total != tc.total {
				t.Fatalf("meta.total = %d, want %d", got.Meta.Total, tc.total)
			}
			if got.Meta.Page != tc.wantPage {
				t.Fatalf("meta.page = %d, want %d", got.Meta.Page, tc.wantPage)
			}
			if got.Meta.PerPage != tc.wantPerPage {
				t.Fatalf("meta.per_page = %d, want %d", got.Meta.PerPage, tc.wantPerPage)
			}
			if got.Meta.TotalPages != tc.wantTotalPages {
				t.Fatalf("meta.total_pages = %d, want %d", got.Meta.TotalPages, tc.wantTotalPages)
			}
		})
	}
}

func TestPaginatedJSONKeys(t *testing.T) {
	c, rec := newContext(t)
	Paginated(c, []int{1, 2, 3}, 3, 1, 3)

	got := decodeMap(t, rec)
	meta, ok := got["meta"].(map[string]any)
	if !ok {
		t.Fatalf("meta = %#v, want an object", got["meta"])
	}
	for _, key := range []string{"total", "page", "per_page", "total_pages"} {
		if _, present := meta[key]; !present {
			t.Fatalf("meta is missing %q: %v", key, meta)
		}
	}
	// data must be present even when empty, so a client can iterate unconditionally.
	if _, present := got["data"]; !present {
		t.Fatalf("body is missing the data key: %v", got)
	}
}

func TestConstructors(t *testing.T) {
	t.Run("NewResponse", func(t *testing.T) {
		got := NewResponse("ok", 42)
		if !got.Success || got.Message != "ok" || got.Data != 42 {
			t.Fatalf("NewResponse = %+v", got)
		}
	})

	t.Run("BooleanSuccess", func(t *testing.T) {
		got := BooleanSuccess("done")
		if !got.Success || got.Message != "done" {
			t.Fatalf("BooleanSuccess = %+v", got)
		}
	})

	t.Run("NewError", func(t *testing.T) {
		got := NewError(http.StatusBadRequest, "BAD", "why")
		if got.Success {
			t.Fatal("Success = true, want false")
		}
		if got.Error.Code != http.StatusBadRequest {
			t.Fatalf("Code = %d, want %d", got.Error.Code, http.StatusBadRequest)
		}
		if got.Error.Type != "BAD" || got.Error.Message != "BAD" {
			t.Fatalf("Type/Message = %q/%q, want BAD/BAD", got.Error.Type, got.Error.Message)
		}
		if got.Error.Details != "why" {
			t.Fatalf("Details = %v, want %q", got.Error.Details, "why")
		}
	})
}

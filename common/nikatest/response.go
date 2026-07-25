package nikatest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
)

// Response is a completed response, ready to assert on.
//
// Every assertion returns the Response so they chain, and each reports failure
// with Errorf rather than Fatalf where it is safe to continue — a test that
// checks five things about one response should report all five failures in one
// run, not the first and then stop.
type Response struct {
	t        TB
	method   string
	path     string
	recorder *httptest.ResponseRecorder
	raw      []byte

	// decoded caches the parsed body so ten JSON assertions parse once.
	decoded    any
	decodedSet bool
	decodeErr  error
}

// Status returns the response status code.
func (r *Response) Status() int { return r.recorder.Code }

// BodyBytes returns the raw response body.
func (r *Response) BodyBytes() []byte { return r.raw }

// BodyString returns the response body as a string.
func (r *Response) BodyString() string { return string(r.raw) }

// HeaderValue returns a response header.
func (r *Response) HeaderValue(key string) string { return r.recorder.Header().Get(key) }

// Cookies returns the cookies the response set.
func (r *Response) Cookies() []*http.Cookie { return r.recorder.Result().Cookies() }

// Recorder exposes the underlying recorder.
func (r *Response) Recorder() *httptest.ResponseRecorder { return r.recorder }

// --- status ---------------------------------------------------------------

// ExpectStatus asserts an exact status code. On mismatch it prints the body,
// because a bare "expected 200, got 500" tells you nothing about the cause.
func (r *Response) ExpectStatus(want int) *Response {
	r.t.Helper()
	if r.recorder.Code != want {
		r.t.Errorf("%s: expected status %d (%s), got %d (%s)\n  body: %s",
			r.describe(), want, http.StatusText(want),
			r.recorder.Code, http.StatusText(r.recorder.Code), r.bodyForError())
	}
	return r
}

// ExpectOK asserts 200.
func (r *Response) ExpectOK() *Response { return r.ExpectStatus(http.StatusOK) }

// ExpectCreated asserts 201.
func (r *Response) ExpectCreated() *Response { return r.ExpectStatus(http.StatusCreated) }

// ExpectNoContent asserts 204 and an empty body.
func (r *Response) ExpectNoContent() *Response {
	r.t.Helper()
	r.ExpectStatus(http.StatusNoContent)
	if len(r.raw) != 0 {
		r.t.Errorf("%s: expected an empty body with 204, got %q", r.describe(), r.BodyString())
	}
	return r
}

// ExpectBadRequest asserts 400.
func (r *Response) ExpectBadRequest() *Response { return r.ExpectStatus(http.StatusBadRequest) }

// ExpectUnauthorized asserts 401.
func (r *Response) ExpectUnauthorized() *Response { return r.ExpectStatus(http.StatusUnauthorized) }

// ExpectForbidden asserts 403.
func (r *Response) ExpectForbidden() *Response { return r.ExpectStatus(http.StatusForbidden) }

// ExpectNotFound asserts 404.
func (r *Response) ExpectNotFound() *Response { return r.ExpectStatus(http.StatusNotFound) }

// ExpectUnprocessable asserts 422, the status the framework uses for validation
// failures.
func (r *Response) ExpectUnprocessable() *Response {
	return r.ExpectStatus(http.StatusUnprocessableEntity)
}

// ExpectStatusIn asserts the status is one of the given codes.
func (r *Response) ExpectStatusIn(want ...int) *Response {
	r.t.Helper()
	for _, code := range want {
		if r.recorder.Code == code {
			return r
		}
	}
	r.t.Errorf("%s: expected status in %v, got %d\n  body: %s",
		r.describe(), want, r.recorder.Code, r.bodyForError())
	return r
}

// ExpectSuccess asserts a 2xx status.
func (r *Response) ExpectSuccess() *Response {
	r.t.Helper()
	if r.recorder.Code < 200 || r.recorder.Code >= 300 {
		r.t.Errorf("%s: expected a 2xx status, got %d\n  body: %s",
			r.describe(), r.recorder.Code, r.bodyForError())
	}
	return r
}

// --- headers and cookies --------------------------------------------------

// ExpectHeader asserts an exact header value.
func (r *Response) ExpectHeader(key, want string) *Response {
	r.t.Helper()
	if got := r.recorder.Header().Get(key); got != want {
		r.t.Errorf("%s: expected header %s = %q, got %q", r.describe(), key, want, got)
	}
	return r
}

// ExpectHeaderContains asserts a header contains a substring — the right
// assertion for Content-Type, whose charset suffix varies.
func (r *Response) ExpectHeaderContains(key, want string) *Response {
	r.t.Helper()
	got := r.recorder.Header().Get(key)
	if !strings.Contains(got, want) {
		r.t.Errorf("%s: expected header %s to contain %q, got %q", r.describe(), key, want, got)
	}
	return r
}

// ExpectHeaderAbsent asserts a header was not sent — for checking that a
// hardening header was removed, or that a secret did not leak into a header.
func (r *Response) ExpectHeaderAbsent(key string) *Response {
	r.t.Helper()
	if got := r.recorder.Header().Get(key); got != "" {
		r.t.Errorf("%s: expected no %s header, got %q", r.describe(), key, got)
	}
	return r
}

// ExpectJSONContentType asserts the response is JSON.
func (r *Response) ExpectJSONContentType() *Response {
	return r.ExpectHeaderContains("Content-Type", "application/json")
}

// ExpectCookie asserts a cookie was set, and returns it for further checks.
func (r *Response) ExpectCookie(name string) *http.Cookie {
	r.t.Helper()
	for _, cookie := range r.Cookies() {
		if cookie.Name == name {
			return cookie
		}
	}
	r.t.Errorf("%s: expected a cookie named %q, got %v", r.describe(), name, cookieNames(r.Cookies()))
	return nil
}

// ExpectSecureCookie asserts a cookie is set with the flags a session cookie
// must carry. Getting these wrong is one of the most common real-world session
// vulnerabilities, so it deserves a first-class assertion.
func (r *Response) ExpectSecureCookie(name string) *Response {
	r.t.Helper()

	cookie := r.ExpectCookie(name)
	if cookie == nil {
		return r
	}
	if !cookie.HttpOnly {
		r.t.Errorf("%s: cookie %q is missing HttpOnly, so JavaScript can read it", r.describe(), name)
	}
	if !cookie.Secure {
		r.t.Errorf("%s: cookie %q is missing Secure, so it is sent over plain HTTP", r.describe(), name)
	}
	if cookie.SameSite == http.SameSiteNoneMode || cookie.SameSite == http.SameSiteDefaultMode {
		r.t.Errorf("%s: cookie %q has SameSite=%v, which does not protect against CSRF",
			r.describe(), name, cookie.SameSite)
	}
	return r
}

func cookieNames(cookies []*http.Cookie) []string {
	names := make([]string, 0, len(cookies))
	for _, cookie := range cookies {
		names = append(names, cookie.Name)
	}
	return names
}

// --- body content ---------------------------------------------------------

// ExpectBody asserts the body matches exactly.
func (r *Response) ExpectBody(want string) *Response {
	r.t.Helper()
	if got := r.BodyString(); got != want {
		r.t.Errorf("%s: body mismatch\n  want: %q\n  got:  %q", r.describe(), want, got)
	}
	return r
}

// ExpectContains asserts the body contains every given substring. This is the
// workhorse for content tests over rendered HTML or plain text.
func (r *Response) ExpectContains(wants ...string) *Response {
	r.t.Helper()
	body := r.BodyString()
	for _, want := range wants {
		if !strings.Contains(body, want) {
			r.t.Errorf("%s: expected the body to contain %q\n  body: %s", r.describe(), want, r.bodyForError())
		}
	}
	return r
}

// ExpectNotContains asserts the body contains none of the given substrings —
// how you assert that a password hash, a stack trace or an internal hostname did
// not leak into a response.
func (r *Response) ExpectNotContains(unwanted ...string) *Response {
	r.t.Helper()
	body := r.BodyString()
	for _, forbidden := range unwanted {
		if strings.Contains(body, forbidden) {
			r.t.Errorf("%s: the body should not contain %q but does\n  body: %s",
				r.describe(), forbidden, r.bodyForError())
		}
	}
	return r
}

// ExpectMatches asserts the body matches a regular expression.
func (r *Response) ExpectMatches(pattern string) *Response {
	r.t.Helper()

	re, err := regexp.Compile(pattern)
	if err != nil {
		r.t.Fatalf("nikatest: invalid pattern %q: %v", pattern, err)
		return r
	}
	if !re.Match(r.raw) {
		r.t.Errorf("%s: expected the body to match %q\n  body: %s", r.describe(), pattern, r.bodyForError())
	}
	return r
}

// ExpectEmpty asserts an empty body.
func (r *Response) ExpectEmpty() *Response {
	r.t.Helper()
	if len(r.raw) != 0 {
		r.t.Errorf("%s: expected an empty body, got %q", r.describe(), r.BodyString())
	}
	return r
}

// --- JSON -----------------------------------------------------------------

// JSON returns the decoded body, failing the test when it is not valid JSON.
func (r *Response) JSON() any {
	r.t.Helper()

	if !r.decodedSet {
		r.decodedSet = true
		// UseNumber keeps integers exact: encoding/json would otherwise turn an
		// int64 id into a float64 and 9007199254740993 would compare unequal to
		// itself.
		decoder := json.NewDecoder(strings.NewReader(string(r.raw)))
		decoder.UseNumber()
		r.decodeErr = decoder.Decode(&r.decoded)
	}
	if r.decodeErr != nil {
		r.t.Fatalf("%s: body is not valid JSON: %v\n  body: %s", r.describe(), r.decodeErr, r.bodyForError())
	}
	return r.decoded
}

// DecodeJSON unmarshals the body into out.
func (r *Response) DecodeJSON(out any) *Response {
	r.t.Helper()
	if err := json.Unmarshal(r.raw, out); err != nil {
		r.t.Fatalf("%s: cannot decode body into %T: %v\n  body: %s", r.describe(), out, err, r.bodyForError())
	}
	return r
}

// ExpectJSONEquals asserts the body is JSON-equal to want, ignoring key order
// and whitespace. want may be a JSON string or any marshalable value.
func (r *Response) ExpectJSONEquals(want any) *Response {
	r.t.Helper()

	wantValue, err := normalizeJSON(want)
	if err != nil {
		r.t.Fatalf("nikatest: expected value is not valid JSON: %v", err)
		return r
	}
	got := r.JSON()

	if !jsonEqual(got, wantValue) {
		r.t.Errorf("%s: JSON mismatch\n  want: %s\n  got:  %s",
			r.describe(), mustFormat(wantValue), mustFormat(got))
	}
	return r
}

// ExpectJSON asserts the body *contains* the given structure: every key in want
// must be present with an equal value, and extra keys in the response are
// ignored.
//
// This is almost always the assertion you want for an API test. Asserting the
// whole document makes the test fail every time an unrelated field is added,
// which trains people to update expectations without reading them.
func (r *Response) ExpectJSON(want any) *Response {
	r.t.Helper()

	wantValue, err := normalizeJSON(want)
	if err != nil {
		r.t.Fatalf("nikatest: expected value is not valid JSON: %v", err)
		return r
	}

	if diff := jsonSubset(r.JSON(), wantValue, ""); diff != "" {
		r.t.Errorf("%s: JSON subset mismatch: %s\n  got: %s", r.describe(), diff, mustFormat(r.JSON()))
	}
	return r
}

// ExpectJSONPath asserts the value at a dotted path equals want.
//
//	res.ExpectJSONPath("data.users.0.email", "ada@example.com")
func (r *Response) ExpectJSONPath(path string, want any) *Response {
	r.t.Helper()

	got, found := lookupPath(r.JSON(), path)
	if !found {
		r.t.Errorf("%s: no value at JSON path %q\n  got: %s", r.describe(), path, mustFormat(r.JSON()))
		return r
	}

	wantValue, err := normalizeJSON(want)
	if err != nil {
		r.t.Fatalf("nikatest: expected value for %q is not valid JSON: %v", path, err)
		return r
	}
	if !jsonEqual(got, wantValue) {
		r.t.Errorf("%s: at JSON path %q\n  want: %s\n  got:  %s",
			r.describe(), path, mustFormat(wantValue), mustFormat(got))
	}
	return r
}

// ExpectJSONPathExists asserts a path is present, whatever its value — for a
// generated id or token whose exact value cannot be predicted.
func (r *Response) ExpectJSONPathExists(paths ...string) *Response {
	r.t.Helper()
	for _, path := range paths {
		if _, found := lookupPath(r.JSON(), path); !found {
			r.t.Errorf("%s: expected JSON path %q to exist\n  got: %s",
				r.describe(), path, mustFormat(r.JSON()))
		}
	}
	return r
}

// ExpectJSONPathAbsent asserts a path is not present — how a test proves a
// sensitive field is not serialised, such as `data.password_hash`.
func (r *Response) ExpectJSONPathAbsent(paths ...string) *Response {
	r.t.Helper()
	for _, path := range paths {
		if value, found := lookupPath(r.JSON(), path); found {
			r.t.Errorf("%s: JSON path %q should be absent but is %s",
				r.describe(), path, mustFormat(value))
		}
	}
	return r
}

// ExpectJSONLen asserts the length of the array or object at a path.
func (r *Response) ExpectJSONLen(path string, want int) *Response {
	r.t.Helper()

	value := r.JSON()
	if path != "" {
		var found bool
		if value, found = lookupPath(value, path); !found {
			r.t.Errorf("%s: no value at JSON path %q", r.describe(), path)
			return r
		}
	}

	got, ok := jsonLen(value)
	if !ok {
		r.t.Errorf("%s: value at %q is a %T, which has no length", r.describe(), path, value)
		return r
	}
	if got != want {
		r.t.Errorf("%s: expected %d items at %q, got %d\n  value: %s",
			r.describe(), want, path, got, mustFormat(value))
	}
	return r
}

// --- framework response shape --------------------------------------------

// ExpectAPISuccess asserts the framework's success envelope: `success: true`
// with no error object.
func (r *Response) ExpectAPISuccess() *Response {
	r.t.Helper()
	r.ExpectSuccess()
	r.ExpectJSONPath("success", true)
	r.ExpectJSONPathAbsent("error")
	return r
}

// ExpectAPIError asserts the framework's error envelope with the given machine
// -readable code, as produced by response.BadRequest and friends.
func (r *Response) ExpectAPIError(code string) *Response {
	r.t.Helper()
	r.ExpectJSONPath("success", false)
	r.ExpectJSONPath("error.message", code)
	return r
}

// ExpectValidationError asserts a 422 whose details name every given field.
// Validation responses are the most-asserted shape in an API suite and the most
// tedious to check by hand.
func (r *Response) ExpectValidationError(fields ...string) *Response {
	r.t.Helper()

	r.ExpectStatus(http.StatusUnprocessableEntity)

	details, found := lookupPath(r.JSON(), "error.details")
	if !found {
		r.t.Errorf("%s: expected error.details in a validation response\n  got: %s",
			r.describe(), mustFormat(r.JSON()))
		return r
	}

	items, ok := details.([]any)
	if !ok {
		r.t.Errorf("%s: expected error.details to be an array, got %T", r.describe(), details)
		return r
	}

	failed := make(map[string]struct{}, len(items))
	for _, item := range items {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if name, ok := entry["field"].(string); ok {
			failed[name] = struct{}{}
		}
	}

	for _, field := range fields {
		if _, ok := failed[field]; !ok {
			r.t.Errorf("%s: expected a validation error for field %q, got errors for %v",
				r.describe(), field, keys(failed))
		}
	}
	return r
}

func keys(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	return out
}

// --- diagnostics ----------------------------------------------------------

// Debug logs the full response, for when an assertion fails and you want to see
// everything at once.
func (r *Response) Debug() *Response {
	r.t.Helper()
	r.t.Logf("%s\n  status: %d\n  headers: %v\n  body: %s",
		r.describe(), r.recorder.Code, r.recorder.Header(), r.BodyString())
	return r
}

func (r *Response) describe() string {
	return fmt.Sprintf("%s %s", r.method, r.path)
}

// bodyForError truncates the body so one large response does not bury the rest
// of the failure output.
func (r *Response) bodyForError() string {
	const maxLen = 2048
	if len(r.raw) <= maxLen {
		if len(r.raw) == 0 {
			return "(empty)"
		}
		return string(r.raw)
	}
	return string(r.raw[:maxLen]) + fmt.Sprintf("… (%d bytes total)", len(r.raw))
}

package nikatest

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
)

// Request builds one request against the app under test.
type Request struct {
	app     *App
	method  string
	path    string
	query   url.Values
	headers http.Header
	cookies []*http.Cookie
	body    io.Reader

	// buildErr defers a construction failure to Do(), so the fluent chain never
	// has to be interrupted by an error check.
	buildErr error
}

// GET starts a GET request.
func (a *App) GET(path string) *Request { return a.request(http.MethodGet, path) }

// POST starts a POST request.
func (a *App) POST(path string) *Request { return a.request(http.MethodPost, path) }

// PUT starts a PUT request.
func (a *App) PUT(path string) *Request { return a.request(http.MethodPut, path) }

// PATCH starts a PATCH request.
func (a *App) PATCH(path string) *Request { return a.request(http.MethodPatch, path) }

// DELETE starts a DELETE request.
func (a *App) DELETE(path string) *Request { return a.request(http.MethodDelete, path) }

// HEAD starts a HEAD request.
func (a *App) HEAD(path string) *Request { return a.request(http.MethodHead, path) }

// OPTIONS starts an OPTIONS request.
func (a *App) OPTIONS(path string) *Request { return a.request(http.MethodOptions, path) }

// Request starts a request with an arbitrary method.
func (a *App) Request(method, path string) *Request { return a.request(method, path) }

func (a *App) request(method, path string) *Request {
	req := &Request{
		app:     a,
		method:  method,
		path:    path,
		query:   url.Values{},
		headers: http.Header{},
	}
	for key, value := range a.defaultHeaders {
		req.headers.Set(key, value)
	}
	return req
}

// Query adds a query parameter.
func (r *Request) Query(key, value string) *Request {
	r.query.Add(key, value)
	return r
}

// Queries adds several query parameters.
func (r *Request) Queries(params map[string]string) *Request {
	for key, value := range params {
		r.query.Add(key, value)
	}
	return r
}

// Header sets a header, replacing any previous value.
func (r *Request) Header(key, value string) *Request {
	r.headers.Set(key, value)
	return r
}

// Headers sets several headers.
func (r *Request) Headers(headers map[string]string) *Request {
	for key, value := range headers {
		r.headers.Set(key, value)
	}
	return r
}

// BearerToken sets an Authorization header for this request only.
func (r *Request) BearerToken(token string) *Request {
	return r.Header("Authorization", "Bearer "+token)
}

// Cookie attaches a cookie.
func (r *Request) Cookie(name, value string) *Request {
	r.cookies = append(r.cookies, &http.Cookie{Name: name, Value: value})
	return r
}

// JSON sets a JSON body. A string or []byte is sent verbatim, which is how a
// test sends deliberately malformed JSON to exercise the error path.
func (r *Request) JSON(payload any) *Request {
	r.headers.Set("Content-Type", "application/json; charset=utf-8")

	switch v := payload.(type) {
	case nil:
		r.body = strings.NewReader("null")
	case string:
		r.body = strings.NewReader(v)
	case []byte:
		r.body = bytes.NewReader(v)
	case json.RawMessage:
		r.body = bytes.NewReader(v)
	default:
		encoded, err := json.Marshal(payload)
		if err != nil {
			r.buildErr = err
			return r
		}
		r.body = bytes.NewReader(encoded)
	}
	return r
}

// Form sets an application/x-www-form-urlencoded body.
func (r *Request) Form(fields map[string]string) *Request {
	values := url.Values{}
	for key, value := range fields {
		values.Set(key, value)
	}
	r.headers.Set("Content-Type", "application/x-www-form-urlencoded")
	r.body = strings.NewReader(values.Encode())
	return r
}

// Body sets a raw body with an explicit content type.
func (r *Request) Body(contentType string, body []byte) *Request {
	if contentType != "" {
		r.headers.Set("Content-Type", contentType)
	}
	r.body = bytes.NewReader(body)
	return r
}

// Text sets a text/plain body.
func (r *Request) Text(body string) *Request {
	return r.Body("text/plain; charset=utf-8", []byte(body))
}

// Multipart builds a multipart/form-data body, for file-upload endpoints.
//
//	app.POST("/avatar").Multipart(func(m *nikatest.Multipart) {
//	    m.Field("caption", "hello")
//	    m.File("avatar", "a.png", pngBytes)
//	}).Do()
func (r *Request) Multipart(build func(*Multipart)) *Request {
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	m := &Multipart{writer: writer}

	build(m)

	if m.err != nil {
		r.buildErr = m.err
		return r
	}
	if err := writer.Close(); err != nil {
		r.buildErr = err
		return r
	}

	r.headers.Set("Content-Type", writer.FormDataContentType())
	r.body = bytes.NewReader(buf.Bytes())
	return r
}

// Multipart collects fields and files for a multipart body.
type Multipart struct {
	writer *multipart.Writer
	err    error
}

// Field adds a form field.
func (m *Multipart) Field(name, value string) *Multipart {
	if m.err != nil {
		return m
	}
	m.err = m.writer.WriteField(name, value)
	return m
}

// File adds a file part.
func (m *Multipart) File(field, filename string, content []byte) *Multipart {
	if m.err != nil {
		return m
	}
	part, err := m.writer.CreateFormFile(field, filename)
	if err != nil {
		m.err = err
		return m
	}
	_, m.err = part.Write(content)
	return m
}

// Do executes the request and returns the response for assertions.
func (r *Request) Do() *Response {
	r.app.t.Helper()

	if r.buildErr != nil {
		r.app.t.Fatalf("nikatest: building %s %s failed: %v", r.method, r.path, r.buildErr)
		return nil
	}

	target := r.path
	if encoded := r.query.Encode(); encoded != "" {
		separator := "?"
		if strings.Contains(target, "?") {
			separator = "&"
		}
		target += separator + encoded
	}

	// Bound the request so a handler that deadlocks fails the test with a clear
	// message instead of hanging the suite until the go test timeout.
	ctx, cancel := context.WithTimeout(context.Background(), r.app.timeout)
	defer cancel()

	body := r.body
	if body == nil {
		body = http.NoBody
	}

	req, err := http.NewRequestWithContext(ctx, r.method, target, body)
	if err != nil {
		r.app.t.Fatalf("nikatest: building %s %s failed: %v", r.method, target, err)
		return nil
	}
	for key, values := range r.headers {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	for _, cookie := range r.cookies {
		req.AddCookie(cookie)
	}

	recorder := httptest.NewRecorder()
	r.app.app.Handler().ServeHTTP(recorder, req)

	return &Response{
		t:        r.app.t,
		method:   r.method,
		path:     target,
		recorder: recorder,
		raw:      recorder.Body.Bytes(),
	}
}

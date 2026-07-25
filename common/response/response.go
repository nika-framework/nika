// Package response builds the JSON envelope every Nika handler writes.
//
// It deliberately imports nothing but net/http, log/slog and gin. In particular
// it must NOT import the root nika package: response is a leaf that other common
// packages (validator, guards, rate limiting) depend on, and those packages
// already import nika, so an import here would close a cycle. The one value we
// need from the root package is the request-id header name, duplicated below as
// a literal.
package response

import (
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
)

// RequestIDHeader mirrors nika.RequestIDHeader. Kept as a literal to preserve
// this package's leaf position in the import graph; see the package comment.
const RequestIDHeader = "X-Request-ID"

// ErrorDetail detailed error structure for the frontend
type ErrorDetail struct {
	// Code is the HTTP status code. Retained for backward compatibility with
	// existing consumers even though it duplicates the outer HTTP status; new
	// clients should read Type, which is the stable machine-readable identifier.
	Code int `json:"code"`
	// Type is the machine-readable error code (e.g. "VALIDATION_ERROR",
	// "USER_NOT_FOUND"), populated from the message argument every responder
	// already receives — by convention a SCREAMING_SNAKE symbol, not prose.
	Type    string      `json:"type,omitempty"`
	Message string      `json:"message"`           // error message
	Details interface{} `json:"details,omitempty"` // details (populated only for validation errors)
}

type Error struct {
	Success bool         `json:"success"`
	Message string       `json:"message,omitempty"`
	Error   *ErrorDetail `json:"error,omitempty"`
}

type Response struct {
	Success bool        `json:"success"`
	Message string      `json:"message,omitempty"`
	Data    interface{} `json:"data,omitempty"`
}

type BoolResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
}

// Meta carries pagination information alongside a page of results.
type Meta struct {
	Total      int64 `json:"total"`
	Page       int   `json:"page"`
	PerPage    int   `json:"per_page"`
	TotalPages int   `json:"total_pages"`
}

// PageResponse is the envelope written by Paginated.
type PageResponse struct {
	Success bool        `json:"success"`
	Data    interface{} `json:"data"`
	Meta    Meta        `json:"meta"`
}

func NewResponse(message string, data interface{}) Response {
	return Response{
		Success: true,
		Message: message,
		Data:    data,
	}
}

func BooleanSuccess(message string) BoolResponse {
	return BoolResponse{
		Success: true,
		Message: message,
	}
}

// NewError constructs a new error Response. code is the HTTP status; message
// doubles as the machine-readable Type.
func NewError(code int, message string, details interface{}) Error {
	return Error{
		Success: false,
		Error: &ErrorDetail{
			Code:    code,
			Type:    message,
			Message: message,
			Details: details,
		},
	}
}

// OkByMsg writes a successful JSON response carrying only a message.
func OkByMsg(c *gin.Context, message string) {
	c.JSON(http.StatusOK, BooleanSuccess(message))
}

func Ok(c *gin.Context, data interface{}) {
	c.JSON(http.StatusOK, data)
}

func Create(c *gin.Context, data interface{}) {
	c.JSON(http.StatusCreated, data)
}

func Update(c *gin.Context, data interface{}) {
	c.JSON(http.StatusAccepted, data)
}

// Accepted writes a 202 for work that has been queued but not yet performed.
func Accepted(c *gin.Context, data interface{}) {
	c.JSON(http.StatusAccepted, data)
}

// NoContent writes a bare 204. It intentionally writes no body: some proxies
// and clients treat a body on a 204 as a framing error.
func NoContent(c *gin.Context) {
	c.Status(http.StatusNoContent)
	// Gin buffers the status until the end of the request. Flushing here means the
	// 204 is actually on the wire even when this is the last thing a handler does
	// outside a full router run.
	c.Writer.WriteHeaderNow()
}

// Paginated writes a page of results plus the metadata a client needs to render
// a pager. total is the number of matching rows overall, not len(data).
func Paginated(c *gin.Context, data interface{}, total int64, page, perPage int) {
	if page < 1 {
		page = 1
	}
	if perPage < 1 {
		perPage = 1
	}

	totalPages := 0
	if total > 0 {
		// Ceiling division done in int64 so a large total cannot overflow before
		// the narrowing conversion.
		totalPages = int((total + int64(perPage) - 1) / int64(perPage))
	}

	c.JSON(http.StatusOK, PageResponse{
		Success: true,
		Data:    data,
		Meta: Meta{
			Total:      total,
			Page:       page,
			PerPage:    perPage,
			TotalPages: totalPages,
		},
	})
}

// JSONError writes an error with an explicit status and aborts the handler
// chain.
//
// Every error responder in this package now aborts. Previously only
// UnauthorizedRequest and ForbiddenRequest did, so a guard rejecting a request
// via BadRequest or NotFoundRequest wrote the error body and then let Gin
// continue into the protected handler — which ran, and appended its own
// response to the already-written error. For an authorization guard that is an
// auth bypass. Making "responded with an error" and "stopped the chain" the same
// act matches what every call site already assumed.
func JSONError(c *gin.Context, statusCode int, message string, details interface{}) {
	c.AbortWithStatusJSON(statusCode, NewError(statusCode, message, details))
}

func BadRequest(c *gin.Context, message string, details interface{}) {
	JSONError(c, http.StatusBadRequest, message, details)
}

func UnauthorizedRequest(c *gin.Context, message string, details interface{}) {
	JSONError(c, http.StatusUnauthorized, message, details)
}

func ForbiddenRequest(c *gin.Context, message string, details interface{}) {
	JSONError(c, http.StatusForbidden, message, details)
}

func NotFoundRequest(c *gin.Context, message string, details interface{}) {
	JSONError(c, http.StatusNotFound, message, details)
}

// Conflict reports that the request collides with the current state of the
// resource: a duplicate unique key, or a lost optimistic-concurrency race.
func Conflict(c *gin.Context, message string, details interface{}) {
	JSONError(c, http.StatusConflict, message, details)
}

func UnprocessableEntity(c *gin.Context, message string, details interface{}) {
	JSONError(c, http.StatusUnprocessableEntity, message, details)
}

// TooManyRequests reports a rate-limit rejection.
func TooManyRequests(c *gin.Context, message string, details interface{}) {
	JSONError(c, http.StatusTooManyRequests, message, details)
}

// ServiceUnavailable reports that a dependency is down or the server is
// draining.
func ServiceUnavailable(c *gin.Context, message string, details interface{}) {
	JSONError(c, http.StatusServiceUnavailable, message, details)
}

// InternalServerError logs err server-side and returns a fixed generic body.
//
// The error is never echoed to the client: Go error strings routinely carry
// driver messages, SQL fragments, file paths and internal host names, and a 500
// is precisely where an attacker probes for them. The response instead carries
// the request id (when the request-id middleware set one) so an operator can
// join a user's report to the log line written here.
func InternalServerError(c *gin.Context, err error) {
	var requestID string
	if c.Writer != nil {
		requestID = c.Writer.Header().Get(RequestIDHeader)
	}

	attrs := make([]any, 0, 4)
	if c.Request != nil {
		attrs = append(attrs,
			slog.String("method", c.Request.Method),
			slog.String("path", c.Request.URL.Path),
		)
	}
	if requestID != "" {
		attrs = append(attrs, slog.String("request_id", requestID))
	}
	if err != nil {
		attrs = append(attrs, slog.Any("error", err))
	}
	slog.Error("internal server error", attrs...)

	var details any
	if requestID != "" {
		details = map[string]string{"request_id": requestID}
	}

	JSONError(c, http.StatusInternalServerError, "INTERNAL_SERVER_ERROR", details)
}

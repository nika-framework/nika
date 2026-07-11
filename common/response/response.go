package response

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// ErrorDetail detailed error structure for the frontend
type ErrorDetail struct {
	Code    int      `json:"code"`              // error code (e.g.: VALIDATION_ERROR, USER_NOT_FOUND)
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

func NewResponse(message string,data interface{}) Response {
	return Response{
		Success: true,
		Message: message,
		Data: data,
	}
}
func BooleanSuccess(message string) BoolResponse {
	return BoolResponse{
		Success: true,
		Message: message,
	}
}

// Error constructs a new error Response.
func NewError(code int, message string, details interface{}) Error {
	return Error{
		Success: false,
		Error: &ErrorDetail{
			Code:    code,
			Message: message,
			Details: details,
		},
	}
}

// JSONSuccess writes a successful JSON response to the gin context with the provided status code, data and message.
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
func JSONError(c *gin.Context, statusCode int, message string, details interface{}) {
	c.JSON(statusCode, NewError(statusCode, message, details))
}
func BadRequest(c *gin.Context, message string, details interface{}) {
	c.JSON(http.StatusBadRequest, NewError(http.StatusBadRequest, message, details))
}
func UnprocessableEntity(c *gin.Context, message string, details interface{}) {
	c.JSON(http.StatusUnprocessableEntity, NewError(http.StatusUnprocessableEntity, message, details))
}
func NotFoundRequest(c *gin.Context, message string, details interface{}) {
	c.JSON(http.StatusNotFound, NewError(http.StatusNotFound, message, details))
}
func UnauthorizedRequest(c *gin.Context, message string, details interface{}) {
	c.AbortWithStatusJSON(http.StatusUnauthorized, NewError(http.StatusUnauthorized, message, details))
}
func ForbiddenRequest(c *gin.Context, message string, details interface{}) {
	c.AbortWithStatusJSON(http.StatusForbidden, NewError(http.StatusForbidden, message, details))
}

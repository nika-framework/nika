package response

import (
	"github.com/gin-gonic/gin"
)

// ErrorDetail detailed error structure for the frontend
type ErrorDetail struct {
	Code    string      `json:"code"`              // error code (e.g.: VALIDATION_ERROR, USER_NOT_FOUND)
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
func NewError(code string, message string, details interface{}) Error {
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
	c.JSON(200, BooleanSuccess(message))
}
func Ok(c *gin.Context, data interface{}) {
	c.JSON(200, data)
}
func Create(c *gin.Context, data interface{}) {
	c.JSON(201, data)
}
func JSONError(c *gin.Context, statusCode int, code string, message string, details interface{}) {
	c.JSON(statusCode, NewError(code, message, details))
}
func BadRequest(c *gin.Context, code string, message string, details interface{}) {
	c.JSON(400, NewError(code, message, details))
}
func NotFoundRequest(c *gin.Context, code string, message string, details interface{}) {
	c.JSON(404, NewError(code, message, details))
}

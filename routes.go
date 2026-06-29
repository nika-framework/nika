package nika

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/gin-gonic/gin"
)

func (a *App) RegisterControllers(controllers ...interface{}) {
	for _, ctrl := range controllers {
		val := reflect.ValueOf(ctrl)
		if val.Kind() == reflect.Ptr {
			val = val.Elem()
		}
		typ := val.Type()

		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			tag := field.Tag.Get("route")
			if tag == "" {
				continue
			}

			parts := strings.SplitN(tag, ":", 2)
			if len(parts) != 2 {
				panic(fmt.Sprintf("Invalid route tag in %s", field.Name))
			}
			method := strings.ToUpper(parts[0])
			path := parts[1]

			if field.Type.Kind() != reflect.Func {
				panic(fmt.Sprintf("Field %s must be a function", field.Name))
			}

			fieldVal := val.Field(i)

			if !fieldVal.CanInterface() {
				panic(fmt.Sprintf("Route handler field %s must be exported (start with uppercase letter)", field.Name))
			}

			handlerFunc := val.Field(i).Interface().(func(*gin.Context))

			switch method {
			case "GET":
				a.engine.GET(path, handlerFunc)
			case "POST":
				a.engine.POST(path, handlerFunc)
			case "PATCH":
				a.engine.PATCH(path, handlerFunc)
			case "PUT":
				a.engine.PUT(path, handlerFunc)
			case "DELETE":
				a.engine.DELETE(path, handlerFunc)
			case "OPTIONS":
				a.engine.OPTIONS(path, handlerFunc)
			case "ANY":
				a.engine.Any(path, handlerFunc)
			default:
				panic(fmt.Sprintf("Unsupported method: %s", method))
			}
			// fmt.Printf("✅ Registered: %s %s -> %s\n", method, path, field.Name)
		}
	}
}

func (a *App) UseRoute() gin.IRouter {
	return a.engine
}
func (a *App) Group(relativePath string, handlers ...gin.HandlerFunc) gin.IRouter {
	a.engine.Group(relativePath, handlers...)
	return a.engine
}
func (a *App) GetGin() *gin.Engine {
	return a.engine
}
func (a *App) GET(relativePath string, handlers ...gin.HandlerFunc) {
	a.engine.GET(relativePath, handlers...)
}
func (a *App) POST(relativePath string, handlers ...gin.HandlerFunc) {
	a.engine.POST(relativePath, handlers...)
}
func (a *App) PATCH(relativePath string, handlers ...gin.HandlerFunc) {
	a.engine.PATCH(relativePath, handlers...)
}
func (a *App) PUT(relativePath string, handlers ...gin.HandlerFunc) {
	a.engine.PUT(relativePath, handlers...)
}
func (a *App) DELETE(relativePath string, handlers ...gin.HandlerFunc) {
	a.engine.DELETE(relativePath, handlers...)
}
func (a *App) Any(relativePath string, handlers ...gin.HandlerFunc) {
	a.engine.Any(relativePath, handlers...)
}
func (a *App) OPTIONS(relativePath string, handlers ...gin.HandlerFunc) {
	a.engine.OPTIONS(relativePath, handlers...)
}
func (a *App) HEAD(relativePath string, handlers ...gin.HandlerFunc) {
	a.engine.HEAD(relativePath, handlers...)
}

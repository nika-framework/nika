package nika

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/gin-gonic/gin"
)

// httpMethods is the set of methods a `route` tag may declare, mapped to the
// Gin registrar for each. A map lookup also gives a precise error for typos
// instead of Gin's generic panic.
var httpMethods = map[string]func(*gin.Engine, string, ...gin.HandlerFunc) gin.IRoutes{
	"GET":     (*gin.Engine).GET,
	"POST":    (*gin.Engine).POST,
	"PUT":     (*gin.Engine).PUT,
	"PATCH":   (*gin.Engine).PATCH,
	"DELETE":  (*gin.Engine).DELETE,
	"HEAD":    (*gin.Engine).HEAD,
	"OPTIONS": (*gin.Engine).OPTIONS,
	"ANY":     (*gin.Engine).Any,
}

// RegisterControllers reflects over each controller and registers every field
// carrying a `route:"METHOD:/path"` tag as an HTTP handler, prefixed by the
// middleware named in its optional `guard:"..."` tag.
//
// Fields tagged `transport:"..."` are message handlers owned by the
// microservice layer and are skipped here.
func (a *App) RegisterControllers(controllers ...any) {
	for _, ctrl := range controllers {
		a.registerController(ctrl)
		a.notifyControllerObservers(ctrl)
	}
}

func (a *App) registerController(ctrl any) {
	if ctrl == nil {
		panic("nika: cannot register a nil controller")
	}

	val := reflect.ValueOf(ctrl)
	if val.Kind() == reflect.Ptr {
		if val.IsNil() {
			panic("nika: cannot register a nil controller pointer")
		}
		val = val.Elem()
	}
	if val.Kind() != reflect.Struct {
		panic(fmt.Sprintf("nika: controller must be a struct or pointer to struct, got %s", val.Kind()))
	}
	typ := val.Type()

	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)

		// A field with no `route` tag is not an HTTP route. It may still be a
		// message handler, which the microservice server picks up from its own
		// `transport` tag — and a field carrying *both* tags is deliberately
		// served on both, so this must not skip it.
		tag := field.Tag.Get("route")
		if tag == "" {
			continue
		}

		method, path, err := parseRouteTag(tag)
		if err != nil {
			panic(fmt.Sprintf("nika: %s.%s: %v", typ.Name(), field.Name, err))
		}

		register, supported := httpMethods[method]
		if !supported {
			panic(fmt.Sprintf(
				"nika: %s.%s declares unsupported method %q (allowed: GET, POST, PUT, PATCH, DELETE, HEAD, OPTIONS, ANY)",
				typ.Name(), field.Name, method,
			))
		}

		handler, err := routeHandler(val.Field(i), field)
		if err != nil {
			panic(fmt.Sprintf("nika: %s.%s: %v", typ.Name(), field.Name, err))
		}

		guards, err := a.resolveGuards(field.Tag.Get("guard"))
		if err != nil {
			panic(fmt.Sprintf("nika: %s.%s: %v", typ.Name(), field.Name, err))
		}

		// Build the chain in one allocation: guards run in declaration order,
		// then the controller handler.
		handlers := make([]gin.HandlerFunc, 0, len(guards)+1)
		handlers = append(handlers, guards...)
		handlers = append(handlers, handler)

		a.trackRoute(method, path, typ.Name(), field.Name)
		register(a.engine, path, handlers...)
	}
}

// parseRouteTag splits a `route` tag into its method and path, rejecting the
// malformed shapes that would otherwise fail deep inside Gin's router.
func parseRouteTag(tag string) (method, path string, err error) {
	parts := strings.SplitN(tag, ":", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid route tag %q, expected `route:\"METHOD:/path\"`", tag)
	}

	method = strings.ToUpper(strings.TrimSpace(parts[0]))
	path = strings.TrimSpace(parts[1])

	if method == "" {
		return "", "", fmt.Errorf("invalid route tag %q: method is empty", tag)
	}
	if path == "" {
		return "", "", fmt.Errorf("invalid route tag %q: path is empty", tag)
	}
	if !strings.HasPrefix(path, "/") {
		return "", "", fmt.Errorf("invalid route path %q: must start with '/'", path)
	}
	// A path containing whitespace or a newline is always a typo, and a newline
	// would corrupt the router tree in confusing ways.
	if strings.ContainsAny(path, " \t\r\n") {
		return "", "", fmt.Errorf("invalid route path %q: contains whitespace", path)
	}

	return method, path, nil
}

// routeHandler extracts the gin handler from a tagged struct field, accepting
// both func(*gin.Context) and gin.HandlerFunc.
func routeHandler(fieldVal reflect.Value, field reflect.StructField) (gin.HandlerFunc, error) {
	if field.Type.Kind() != reflect.Func {
		return nil, fmt.Errorf("route field must be a func(*gin.Context), got %s", field.Type)
	}
	if !field.IsExported() {
		return nil, fmt.Errorf("route field must be exported (start with an uppercase letter)")
	}
	if fieldVal.IsNil() {
		return nil, fmt.Errorf("route handler is nil — assign it in the controller constructor")
	}
	if !fieldVal.CanInterface() {
		return nil, fmt.Errorf("route handler is not accessible via reflection")
	}

	switch fn := fieldVal.Interface().(type) {
	case gin.HandlerFunc:
		return fn, nil
	case func(*gin.Context):
		return fn, nil
	default:
		return nil, fmt.Errorf(
			"route handler must have signature func(*gin.Context), got %s",
			field.Type,
		)
	}
}

// resolveGuards turns a `guard` tag into the middleware chain it names.
func (a *App) resolveGuards(guardTag string) ([]gin.HandlerFunc, error) {
	specs, err := ParseGuardTag(guardTag)
	if err != nil {
		return nil, err
	}
	if len(specs) == 0 {
		return nil, nil
	}

	a.mu.RLock()
	defer a.mu.RUnlock()

	handlers := make([]gin.HandlerFunc, 0, len(specs))
	for _, spec := range specs {
		guardFn, exists := a.guards[spec.Name]
		if !exists {
			return nil, fmt.Errorf(
				"guard %q is not registered — call app.AddGuard(%q, ...) before loading modules",
				spec.Name, spec.Name,
			)
		}
		middleware := guardFn(spec.Args)
		if middleware == nil {
			return nil, fmt.Errorf("guard %q returned a nil middleware", spec.Name)
		}
		handlers = append(handlers, middleware)
	}
	return handlers, nil
}

// trackRoute records a method+path pair and reports a duplicate registration
// with the offending controller named. Gin panics on duplicates with only the
// path, which is hard to trace in a large module graph.
func (a *App) trackRoute(method, path, controller, fieldName string) {
	key := method + " " + path

	a.mu.Lock()
	defer a.mu.Unlock()
	if _, exists := a.routeMethod[key]; exists {
		panic(fmt.Sprintf(
			"nika: duplicate route %s registered again by %s.%s",
			key, controller, fieldName,
		))
	}
	a.routeMethod[key] = struct{}{}
}

// Routes returns every registered route as "METHOD /path".
func (a *App) Routes() []string {
	a.mu.RLock()
	defer a.mu.RUnlock()

	out := make([]string, 0, len(a.routeMethod))
	for route := range a.routeMethod {
		out = append(out, route)
	}
	return out
}

// UseRoute exposes the underlying router for manual registration.
func (a *App) UseRoute() gin.IRouter {
	return a.engine
}

// Group creates a route group. Unlike the previous implementation, it returns
// the group itself, so handlers registered on the result actually inherit the
// prefix and middleware.
func (a *App) Group(relativePath string, handlers ...gin.HandlerFunc) gin.IRouter {
	return a.engine.Group(relativePath, handlers...)
}

// GetGin returns the underlying Gin engine.
func (a *App) GetGin() *gin.Engine {
	return a.engine
}

// Handler returns the app as a net/http handler, which is what makes an app
// testable with httptest without binding a port.
func (a *App) Handler() *gin.Engine {
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

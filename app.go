package nika

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/gin-gonic/gin"
)

type App struct {
	engine    *gin.Engine
	container map[reflect.Type]interface{}
}

func NewApp() *App {
	return &App{
		engine:    gin.Default(),
		container: make(map[reflect.Type]interface{}),
	}
}

func (a *App) LoadModule(module Module) {
	for _, subModule := range module.Imports() {
		a.LoadModule(subModule)
	}

	for _, provider := range module.Providers() {
		fnType := reflect.TypeOf(provider)
		provVal := reflect.ValueOf(provider)

		var instance interface{}
		if provVal.Kind() == reflect.Func {
			instance = a.invokeConstructor(provider)
		} else {
			instance = provider
		}

		// register concrete type
		provType := reflect.TypeOf(instance)
		a.container[provType] = instance
		if provType.Kind() == reflect.Ptr {
			a.container[provType.Elem()] = instance
		}

		// register interface return type
		if fnType.Kind() == reflect.Func && fnType.NumOut() > 0 {
			outType := fnType.Out(0)
			if outType.Kind() == reflect.Interface {
				a.container[outType] = instance
			}
		}
	}

	for _, ctrl := range module.Controllers() {
		var finalCtrl interface{}
		ctrlVal := reflect.ValueOf(ctrl)

		if ctrlVal.Kind() == reflect.Func {
			finalCtrl = a.invokeConstructor(ctrl)
		} else {
			a.resolveDependencies(ctrl)
			finalCtrl = ctrl
		}

		a.RegisterControllers(finalCtrl)
	}
}

func (a *App) invokeConstructor(constructor interface{}) interface{} {
	fnType := reflect.TypeOf(constructor)

	if fnType.NumOut() == 0 {
		panic("Constructor must return a value (the controller)")
	}
	args := make([]reflect.Value, fnType.NumIn())

	for i := 0; i < fnType.NumIn(); i++ {
		requiredType := fnType.In(i)
		if dependency, exists := a.container[requiredType]; exists {
			args[i] = reflect.ValueOf(dependency)
		} else {
			panic(fmt.Sprintf("❌ DI Error: Cannot resolve '%s' for constructor", requiredType))
		}
	}
	results := reflect.ValueOf(constructor).Call(args)
	return results[0].Interface()
}

func (a *App) resolveDependencies(controller interface{}) {
	val := reflect.ValueOf(controller)
	if val.Kind() != reflect.Ptr || val.Elem().Kind() != reflect.Struct {
		panic("Controller must be a pointer to a struct")
	}
	val = val.Elem()
	typ := val.Type()

	for i := 0; i < val.NumField(); i++ {
		field := val.Field(i)
		fieldType := typ.Field(i)

		if field.Kind() == reflect.Func || !field.CanSet() {
			continue
		}

		requiredType := fieldType.Type
		if dependency, exists := a.container[requiredType]; exists {
			field.Set(reflect.ValueOf(dependency))
		}
	}
}

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
			default:
				panic(fmt.Sprintf("Unsupported method: %s", method))
			}
			fmt.Printf("✅ Registered: %s %s -> %s\n", method, path, field.Name)
		}
	}
}

func (a *App) RegisterSingleton(instance interface{}) {
	provType := reflect.TypeOf(instance)
	a.container[provType] = instance
	if provType.Kind() == reflect.Ptr {
		a.container[provType.Elem()] = instance
	}
}

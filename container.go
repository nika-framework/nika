package nika
 
import (
	"fmt"
	"reflect"
)
 
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
 
func (a *App) RegisterSingleton(instance interface{}) {
	provType := reflect.TypeOf(instance)
	a.container[provType] = instance
	if provType.Kind() == reflect.Ptr {
		a.container[provType.Elem()] = instance
	}
}
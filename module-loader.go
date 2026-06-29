package nika
 
import "reflect"
 
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
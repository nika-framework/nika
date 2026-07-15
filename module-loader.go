package nika

import "reflect"

func (a *App) LoadModule(module Module) {
	a.loadModule(module)
}

func (a *App) loadModule(module Module) map[reflect.Type]interface{} {
	moduleType := reflect.TypeOf(module)
	if moduleType == nil {
		panic("Module cannot be nil")
	}
	for moduleType.Kind() == reflect.Ptr {
		moduleType = moduleType.Elem()
	}

	if exports, loaded := a.moduleExports[moduleType]; loaded {
		return exports
	}
	if _, loading := a.loadingModules[moduleType]; loading {
		panic("Circular module import: " + moduleType.String())
	}
	a.loadingModules[moduleType] = struct{}{}
	defer delete(a.loadingModules, moduleType)

	container := cloneContainer(a.container)

	for _, subModule := range module.Imports() {
		for providerType, provider := range a.loadModule(subModule) {
			container[providerType] = provider
		}
	}

	for _, provider := range module.Providers() {
		fnType := reflect.TypeOf(provider)
		provVal := reflect.ValueOf(provider)

		var instance interface{}
		if provVal.Kind() == reflect.Func {
			instance = a.invokeConstructor(provider, container)
		} else {
			instance = provider
		}

		// register concrete type
		provType := reflect.TypeOf(instance)
		container[provType] = instance
		if provType.Kind() == reflect.Ptr {
			container[provType.Elem()] = instance
		}

		// register interface return type
		if fnType.Kind() == reflect.Func && fnType.NumOut() > 0 {
			outType := fnType.Out(0)
			if outType.Kind() == reflect.Interface {
				container[outType] = instance
			}
		}
	}

	for _, ctrl := range module.Controllers() {
		var finalCtrl interface{}
		ctrlVal := reflect.ValueOf(ctrl)

		if ctrlVal.Kind() == reflect.Func {
			finalCtrl = a.invokeConstructor(ctrl, container)
		} else {
			a.resolveDependencies(ctrl, container)
			finalCtrl = ctrl
		}

		a.RegisterControllers(finalCtrl)
	}

	exports := make(map[reflect.Type]interface{})
	for _, provider := range module.Exports() {
		exportType := exportedProviderType(provider)
		instance, exists := container[exportType]
		if !exists {
			panic("Module " + moduleType.String() + " exports an unavailable provider: " + exportType.String())
		}
		exports[exportType] = instance
	}

	a.moduleExports[moduleType] = exports
	return exports
}

func cloneContainer(container map[reflect.Type]interface{}) map[reflect.Type]interface{} {
	clone := make(map[reflect.Type]interface{}, len(container))
	for providerType, provider := range container {
		clone[providerType] = provider
	}
	return clone
}

func exportedProviderType(provider interface{}) reflect.Type {
	providerType := reflect.TypeOf(provider)
	if providerType == nil {
		panic("Exported provider cannot be nil")
	}
	if providerType.Kind() != reflect.Func {
		return providerType
	}
	if providerType.NumOut() == 0 {
		panic("Exported provider constructor must return a value")
	}
	return providerType.Out(0)
}

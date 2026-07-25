package nika

import (
	"fmt"
	"reflect"
)

// LoadModule bootstraps a module and everything it imports.
func (a *App) LoadModule(module Module) {
	a.loadModule(module)
}

// loadModule resolves a module's imports, instantiates its providers and
// controllers, and returns the providers it exports.
//
// Loading is single-threaded by design — it mutates the shared cycle-detection
// and memoisation maps — so it takes the app lock for the whole traversal. The
// container passed down to children is a snapshot, which is what keeps a
// module's private providers private.
func (a *App) loadModule(module Module) map[reflect.Type]any {
	if module == nil {
		panic("nika: module cannot be nil")
	}

	moduleType := reflect.TypeOf(module)
	if moduleType == nil {
		panic("nika: module cannot be nil")
	}
	for moduleType.Kind() == reflect.Ptr {
		moduleType = moduleType.Elem()
	}

	a.mu.Lock()
	if exports, loaded := a.moduleExports[moduleType]; loaded {
		a.mu.Unlock()
		return exports
	}
	if _, loading := a.loadingModules[moduleType]; loading {
		a.mu.Unlock()
		panic("nika: circular module import: " + moduleType.String())
	}
	a.loadingModules[moduleType] = struct{}{}
	container := cloneContainer(a.container)
	a.mu.Unlock()

	defer func() {
		a.mu.Lock()
		delete(a.loadingModules, moduleType)
		a.mu.Unlock()
	}()

	for _, subModule := range module.Imports() {
		for providerType, provider := range a.loadModule(subModule) {
			container[providerType] = provider
		}
	}

	for _, provider := range module.Providers() {
		a.registerProvider(container, provider, moduleType)
	}

	for _, ctrl := range module.Controllers() {
		if ctrl == nil {
			panic("nika: module " + moduleType.String() + " declares a nil controller")
		}

		var finalCtrl any
		if reflect.TypeOf(ctrl).Kind() == reflect.Func {
			finalCtrl = a.invokeConstructor(ctrl, container)
		} else {
			a.resolveDependencies(ctrl, container)
			finalCtrl = ctrl
		}

		a.RegisterControllers(finalCtrl)
	}

	exports := make(map[reflect.Type]any, len(module.Exports()))
	for _, provider := range module.Exports() {
		exportType := exportedProviderType(provider)
		instance, exists := container[exportType]
		if !exists {
			panic(fmt.Sprintf(
				"nika: module %s exports %s, which it neither provides nor imports",
				moduleType, exportType,
			))
		}
		exports[exportType] = instance
	}

	a.mu.Lock()
	a.moduleExports[moduleType] = exports
	a.mu.Unlock()

	return exports
}

// registerProvider instantiates one provider and indexes it in container under
// every type consumers may legitimately ask for: the concrete type, the pointed
// -to type, and the declared interface when the constructor returns one.
func (a *App) registerProvider(container map[reflect.Type]any, provider any, moduleType reflect.Type) {
	if provider == nil {
		panic("nika: module " + moduleType.String() + " declares a nil provider")
	}

	fnType := reflect.TypeOf(provider)

	instance := provider
	if fnType.Kind() == reflect.Func {
		instance = a.invokeConstructor(provider, container)
	}

	// Only the concrete type is indexed. Also indexing *T under T would let a
	// consumer declaring `func(repo Repository)` resolve a *Repository and then
	// panic inside reflect.Call on the type mismatch.
	container[reflect.TypeOf(instance)] = instance

	// A constructor declared as `func(...) SomeInterface` signals that consumers
	// should depend on the interface, so index it there too.
	if fnType.Kind() == reflect.Func && fnType.NumOut() > 0 {
		if outType := fnType.Out(0); outType.Kind() == reflect.Interface {
			container[outType] = instance
		}
	}
}

func cloneContainer(container map[reflect.Type]any) map[reflect.Type]any {
	clone := make(map[reflect.Type]any, len(container))
	for providerType, provider := range container {
		clone[providerType] = provider
	}
	return clone
}

// exportedProviderType maps an Exports() entry to the container key it refers
// to: the constructor's first return type, or the value's own type.
func exportedProviderType(provider any) reflect.Type {
	providerType := reflect.TypeOf(provider)
	if providerType == nil {
		panic("nika: exported provider cannot be nil")
	}
	if providerType.Kind() != reflect.Func {
		return providerType
	}
	if providerType.NumOut() == 0 {
		panic("nika: exported provider constructor must return a value")
	}
	return providerType.Out(0)
}

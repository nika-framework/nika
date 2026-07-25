package nika

import (
	"fmt"
	"reflect"
	"strings"
)

// errorType is compared against constructor return types so a provider may
// signal failure instead of panicking.
var errorType = reflect.TypeOf((*error)(nil)).Elem()

// invokeConstructor resolves every parameter of constructor from container and
// calls it, returning the first result.
//
// A constructor may return (T, error). Previously the error was discarded, so a
// provider that failed to connect was registered in a half-built state and the
// failure surfaced much later as a nil-pointer panic on the request path. Now a
// non-nil error aborts bootstrap with the provider named.
func (a *App) invokeConstructor(constructor any, container map[reflect.Type]any) any {
	fnType := reflect.TypeOf(constructor)
	if fnType == nil || fnType.Kind() != reflect.Func {
		panic("nika: provider constructor must be a function")
	}
	if fnType.NumOut() == 0 {
		panic(fmt.Sprintf("nika: constructor %s must return a value (the provider)", fnType))
	}
	if fnType.IsVariadic() {
		panic(fmt.Sprintf("nika: variadic constructor %s is not supported", fnType))
	}

	args := make([]reflect.Value, fnType.NumIn())
	for i := 0; i < fnType.NumIn(); i++ {
		requiredType := fnType.In(i)
		dependency, exists := container[requiredType]
		if !exists {
			dependency, exists = resolveByInterface(requiredType, container)
		}
		if !exists {
			panic(fmt.Sprintf(
				"❌ DI Error: cannot resolve %s (parameter %d of %s).%s\n   Register it as a provider in this module, or export it from an imported module.\n   Known types: %s",
				requiredType, i+1, fnType, pointerHint(requiredType, container), knownTypes(container),
			))
		}
		if dependency == nil {
			args[i] = reflect.Zero(requiredType)
			continue
		}
		args[i] = reflect.ValueOf(dependency)
	}

	results := reflect.ValueOf(constructor).Call(args)

	// Surface a constructor error immediately rather than registering a broken
	// provider.
	for i := 1; i < len(results); i++ {
		if results[i].Type() != errorType {
			continue
		}
		if err, ok := results[i].Interface().(error); ok && err != nil {
			panic(fmt.Sprintf("❌ DI Error: constructor %s failed: %v", fnType, err))
		}
	}

	// A typed nil — (*Service)(nil) boxed into an interface — is not == nil, so
	// the interface comparison alone would let a constructor that failed to build
	// anything register a value that panics on first use.
	if isNilValue(results[0]) {
		panic(fmt.Sprintf("❌ DI Error: constructor %s returned nil", fnType))
	}
	return results[0].Interface()
}

// isNilValue reports whether v holds nil, including a typed nil pointer.
func isNilValue(v reflect.Value) bool {
	if !v.IsValid() {
		return true
	}
	switch v.Kind() {
	case reflect.Ptr, reflect.Interface, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan:
		return v.IsNil()
	default:
		return false
	}
}

// resolveDependencies fills the exported struct fields of controller from the
// container by type. Fields with no matching provider are left untouched, which
// lets a controller mix injected collaborators with plain configuration.
func (a *App) resolveDependencies(controller any, container map[reflect.Type]any) {
	val := reflect.ValueOf(controller)
	if !val.IsValid() || val.Kind() != reflect.Ptr || val.IsNil() || val.Elem().Kind() != reflect.Struct {
		panic("nika: controller must be a non-nil pointer to a struct")
	}
	val = val.Elem()
	typ := val.Type()

	for i := 0; i < val.NumField(); i++ {
		field := val.Field(i)
		fieldType := typ.Field(i)

		// Route handlers are functions assigned by the controller itself, and
		// unexported fields cannot be set through reflection.
		if field.Kind() == reflect.Func || !field.CanSet() {
			continue
		}

		dependency, exists := container[fieldType.Type]
		if !exists {
			dependency, exists = resolveByInterface(fieldType.Type, container)
		}
		if exists && dependency != nil {
			field.Set(reflect.ValueOf(dependency))
		}
	}
}

// RegisterSingleton registers instance in the root container under its concrete
// type, plus every interface it satisfies that has already been asked for.
//
// A *T is deliberately NOT also indexed under T. Doing so used to look
// convenient, but the stored value is still a pointer, so a constructor
// declaring `func(db MongoDB)` would resolve it and then panic inside
// reflect.Call with a type mismatch — and had it worked, it would have handed out
// a *copy* of a singleton whose zero-copy identity (its mutex, its pool) is the
// whole point. Asking for T when *T is registered is now a clear startup error
// that names the fix.
func (a *App) RegisterSingleton(instance any) {
	if instance == nil {
		panic("nika: cannot register a nil singleton")
	}

	provType := reflect.TypeOf(instance)

	a.mu.Lock()
	defer a.mu.Unlock()
	a.container[provType] = instance
}

// RegisterSingletonAs registers instance under an explicit interface type, so
// consumers can depend on the interface rather than the implementation.
//
// It panics when instance does not satisfy the interface, turning a wiring
// mistake into a startup failure instead of a runtime type assertion.
func RegisterSingletonAs[Iface any](a *App, instance Iface) {
	ifaceType := reflect.TypeOf((*Iface)(nil)).Elem()

	a.mu.Lock()
	defer a.mu.Unlock()
	a.container[ifaceType] = instance

	if concrete := reflect.TypeOf(instance); concrete != nil {
		a.container[concrete] = instance
	}
}

// Resolve returns the provider registered for T, and whether it was found.
func Resolve[T any](a *App) (T, bool) {
	var zero T
	wanted := reflect.TypeOf((*T)(nil)).Elem()

	a.mu.RLock()
	instance, ok := a.container[wanted]
	if !ok {
		instance, ok = resolveByInterface(wanted, a.container)
	}
	a.mu.RUnlock()

	if !ok {
		return zero, false
	}
	typed, ok := instance.(T)
	if !ok {
		return zero, false
	}
	return typed, true
}

// MustResolve returns the provider registered for T, panicking when it is
// absent. Use it in bootstrap code where a missing provider is a programming
// error, not a runtime condition.
func MustResolve[T any](a *App) T {
	instance, ok := Resolve[T](a)
	if !ok {
		wanted := reflect.TypeOf((*T)(nil)).Elem()

		a.mu.RLock()
		hint := pointerHint(wanted, a.container)
		a.mu.RUnlock()

		panic(fmt.Sprintf("nika: no provider registered for %s%s", wanted, hint))
	}
	return instance
}

// pointerHint returns a suggestion when the caller asked for T but *T (or vice
// versa) is what is registered — by far the most common DI mistake, and one that
// a bare "cannot resolve" message leaves the developer to guess at.
func pointerHint(wanted reflect.Type, container map[reflect.Type]any) string {
	if wanted.Kind() == reflect.Ptr {
		if _, ok := container[wanted.Elem()]; ok {
			return fmt.Sprintf("\n   Hint: %s is registered — drop the pointer.", wanted.Elem())
		}
		return ""
	}
	if _, ok := container[reflect.PointerTo(wanted)]; ok {
		return fmt.Sprintf("\n   Hint: *%s is registered — depend on the pointer instead.", wanted)
	}
	return ""
}

// snapshotContainer copies the root container under a read lock. Module loading
// works on the copy so a module's local providers never leak into the root
// container or into sibling modules.
func (a *App) snapshotContainer() map[reflect.Type]any {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return cloneContainer(a.container)
}

// resolveByInterface satisfies an interface dependency from the single
// registered provider that implements it.
//
// Without this, declaring a provider as its concrete `*postgresUserRepo` and
// consuming it as `UserRepository` fails, and the fix — a constructor that
// returns the interface, or RegisterSingletonAs — is not obvious from the error.
// The binding is only attempted after an exact lookup misses, and only when
// exactly one candidate implements the interface: two candidates is genuinely
// ambiguous, and silently picking one would be worse than failing.
func resolveByInterface(wanted reflect.Type, container map[reflect.Type]any) (any, bool) {
	if wanted.Kind() != reflect.Interface {
		return nil, false
	}

	var (
		match any
		found int
	)
	for provType, instance := range container {
		// Skip interface keys: an instance is already indexed under its concrete
		// type, so considering both would double-count the same provider.
		if provType.Kind() == reflect.Interface || !provType.Implements(wanted) {
			continue
		}
		match = instance
		found++
		if found > 1 {
			return nil, false
		}
	}
	return match, found == 1
}

// knownTypes renders the container keys for DI error messages, so a failure
// tells the developer what *is* available instead of only what is missing.
func knownTypes(container map[reflect.Type]any) string {
	if len(container) == 0 {
		return "(none)"
	}
	names := make([]string, 0, len(container))
	for t := range container {
		names = append(names, t.String())
	}
	// Keep the message short; a long container listing buries the real error.
	const maxListed = 12
	if len(names) > maxListed {
		names = append(names[:maxListed], fmt.Sprintf("… and %d more", len(names)-maxListed))
	}
	return strings.Join(names, ", ")
}

package nika

import (
	"errors"
	"reflect"
	"testing"

	"github.com/gin-gonic/gin"
)

// --- fixtures -------------------------------------------------------------

type repository struct{ name string }

type service struct {
	Repo *repository
}

type greeter interface{ Greet() string }

type englishGreeter struct{}

func (englishGreeter) Greet() string { return "hello" }

// --- tests ----------------------------------------------------------------

func TestRegisterSingletonIndexesTheConcreteType(t *testing.T) {
	app := newTestApp()
	repo := &repository{name: "users"}
	app.RegisterSingleton(repo)

	if got, ok := Resolve[*repository](app); !ok || got != repo {
		t.Errorf("Resolve[*repository] = (%v, %v), want the registered instance", got, ok)
	}
	// A *T must NOT resolve as T: handing back a dereferenced copy of a
	// singleton silently duplicates its mutex and connection pool.
	if _, ok := Resolve[repository](app); ok {
		t.Error("Resolve[repository] resolved a registered *repository, which would hand out a copy of a singleton")
	}
}

// TestResolveHintsAtThePointerForm pins the error message, because "cannot
// resolve nika.repository" with a *nika.repository sitting in the container is
// the single most common wiring mistake.
func TestResolveHintsAtThePointerForm(t *testing.T) {
	app := newTestApp()
	app.RegisterSingleton(&repository{name: "users"})

	defer expectPanic(t, "*nika.repository is registered")
	MustResolve[repository](app)
}

func TestRegisterSingletonRejectsNil(t *testing.T) {
	app := newTestApp()
	defer expectPanic(t, "nil singleton")
	app.RegisterSingleton(nil)
}

func TestRegisterSingletonAsIndexesTheInterface(t *testing.T) {
	app := newTestApp()
	RegisterSingletonAs[greeter](app, englishGreeter{})

	got, ok := Resolve[greeter](app)
	if !ok {
		t.Fatal("Resolve[greeter] reported not found")
	}
	if got.Greet() != "hello" {
		t.Errorf("Greet() = %q, want \"hello\"", got.Greet())
	}
}

func TestMustResolvePanicsWithTheMissingType(t *testing.T) {
	app := newTestApp()
	defer expectPanic(t, "no provider registered for nika.repository")
	MustResolve[repository](app)
}

func TestInvokeConstructorInjectsDependencies(t *testing.T) {
	app := newTestApp()
	repo := &repository{name: "users"}

	built := app.invokeConstructor(
		func(r *repository) *service { return &service{Repo: r} },
		containerWith(repo),
	)

	svc, ok := built.(*service)
	if !ok {
		t.Fatalf("constructor returned %T, want *service", built)
	}
	if svc.Repo != repo {
		t.Error("the constructor did not receive the registered repository")
	}
}

// TestInvokeConstructorSurfacesTheError is the important one: a constructor
// declared as (T, error) used to have its error silently discarded, so a
// provider that failed to connect was registered half-built and the failure
// resurfaced much later as a nil dereference on the request path.
func TestInvokeConstructorSurfacesTheError(t *testing.T) {
	app := newTestApp()

	defer expectPanic(t, "cannot reach the database")
	app.invokeConstructor(
		func() (*repository, error) {
			return nil, errors.New("cannot reach the database")
		},
		containerWith(),
	)
}

func TestInvokeConstructorAcceptsANilError(t *testing.T) {
	app := newTestApp()

	built := app.invokeConstructor(
		func() (*repository, error) { return &repository{name: "ok"}, nil },
		containerWith(),
	)
	if repo, ok := built.(*repository); !ok || repo.name != "ok" {
		t.Errorf("constructor returned %#v, want a *repository named \"ok\"", built)
	}
}

func TestInvokeConstructorReportsAnUnresolvableDependency(t *testing.T) {
	app := newTestApp()

	defer expectPanic(t, "cannot resolve")
	app.invokeConstructor(
		func(r *repository) *service { return &service{Repo: r} },
		containerWith(),
	)
}

func TestInvokeConstructorRejectsBadShapes(t *testing.T) {
	t.Run("no return value", func(t *testing.T) {
		app := newTestApp()
		defer expectPanic(t, "must return a value")
		app.invokeConstructor(func() {}, containerWith())
	})

	t.Run("not a function", func(t *testing.T) {
		app := newTestApp()
		defer expectPanic(t, "must be a function")
		app.invokeConstructor("not a constructor", containerWith())
	})

	t.Run("variadic", func(t *testing.T) {
		app := newTestApp()
		defer expectPanic(t, "variadic")
		app.invokeConstructor(func(...*repository) *service { return nil }, containerWith())
	})

	t.Run("returns nil", func(t *testing.T) {
		app := newTestApp()
		defer expectPanic(t, "returned nil")
		app.invokeConstructor(func() *service { return nil }, containerWith())
	})
}

func TestResolveDependenciesFillsStructFields(t *testing.T) {
	app := newTestApp()
	repo := &repository{name: "users"}

	type controller struct {
		Repo   *repository
		List   func(*gin.Context) `route:"GET:/x"`
		Config string
	}

	ctrl := &controller{Config: "kept"}
	app.resolveDependencies(ctrl, containerWith(repo))

	if ctrl.Repo != repo {
		t.Error("the repository was not injected")
	}
	// Handler fields are assigned by the controller, and a field with no
	// registered provider must be left as it was rather than zeroed.
	if ctrl.Config != "kept" {
		t.Errorf("Config = %q, want \"kept\" — a field with no provider must not be overwritten", ctrl.Config)
	}
}

func TestResolveDependenciesRejectsNonPointer(t *testing.T) {
	app := newTestApp()
	defer expectPanic(t, "pointer to a struct")
	app.resolveDependencies(repository{}, containerWith())
}

// TestModuleProvidersAreScopedToTheirModule pins the isolation guarantee: a
// provider declared privately by one module must not leak into a sibling.
func TestModuleProvidersAreScopedToTheirModule(t *testing.T) {
	type privateProvider struct{ moduleSpec }

	app := newTestApp()
	app.LoadModule(privateProvider{moduleSpec{
		providers: []any{&repository{name: "private"}},
	}})

	if _, ok := Resolve[*repository](app); ok {
		t.Error("a module-private provider leaked into the root container")
	}
}

func TestConcurrentRegistrationIsRaceFree(t *testing.T) {
	// The container used to be a bare map written from RegisterSingleton with no
	// lock, which races as soon as bootstrap touches more than one goroutine.
	// Run with -race for this to mean anything.
	app := newTestApp()

	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func(i int) {
			defer func() { done <- struct{}{} }()
			app.RegisterSingleton(&repository{name: string(rune('a' + i))})
			_, _ = Resolve[*repository](app)
		}(i)
	}
	for i := 0; i < 8; i++ {
		<-done
	}
}

func TestControllerObserverReceivesPastAndFutureControllers(t *testing.T) {
	type first struct {
		A func(*gin.Context) `route:"GET:/a"`
	}
	type second struct {
		B func(*gin.Context) `route:"GET:/b"`
	}

	app := newTestApp()
	app.RegisterControllers(&first{A: okHandler("a")})

	var seen int
	app.OnController(func(any) error {
		seen++
		return nil
	})
	if seen != 1 {
		t.Errorf("the observer saw %d controllers on registration, want the 1 already registered", seen)
	}

	app.RegisterControllers(&second{B: okHandler("b")})
	if seen != 2 {
		t.Errorf("the observer saw %d controllers, want 2", seen)
	}
}

func TestControllerObserverErrorFailsBootstrap(t *testing.T) {
	type controller struct {
		A func(*gin.Context) `route:"GET:/a"`
	}

	app := newTestApp()
	app.OnController(func(any) error { return errors.New("bad message tag") })

	defer expectPanic(t, "bad message tag")
	app.RegisterControllers(&controller{A: okHandler("a")})
}

// --- helpers --------------------------------------------------------------

// containerWith builds the container map the DI internals take, indexing each
// instance the same way RegisterSingleton would.
func containerWith(instances ...any) map[reflect.Type]any {
	container := make(map[reflect.Type]any, len(instances)*2)
	for _, instance := range instances {
		provType := reflect.TypeOf(instance)
		container[provType] = instance
		if provType.Kind() == reflect.Ptr {
			container[provType.Elem()] = instance
		}
	}
	return container
}

// --- interface binding ----------------------------------------------------

type secondGreeter struct{}

func (secondGreeter) Greet() string { return "bonjour" }

// TestInterfaceIsSatisfiedByItsSoleImplementation removes a papercut: declaring a
// provider as its concrete type and consuming it as an interface is the normal
// way to write Go, and it used to fail with an error that did not suggest the fix.
func TestInterfaceIsSatisfiedByItsSoleImplementation(t *testing.T) {
	app := newTestApp()
	app.RegisterSingleton(englishGreeter{})

	got, ok := Resolve[greeter](app)
	if !ok {
		t.Fatal("Resolve[greeter] found nothing for a registered englishGreeter")
	}
	if got.Greet() != "hello" {
		t.Errorf("Greet() = %q, want \"hello\"", got.Greet())
	}
}

// TestAmbiguousInterfaceIsNotGuessed is the other half of the rule: with two
// implementations registered, picking one silently would be worse than failing,
// because which one you got would depend on map iteration order.
func TestAmbiguousInterfaceIsNotGuessed(t *testing.T) {
	app := newTestApp()
	app.RegisterSingleton(englishGreeter{})
	app.RegisterSingleton(secondGreeter{})

	if _, ok := Resolve[greeter](app); ok {
		t.Error("Resolve[greeter] picked one of two implementations instead of reporting ambiguity")
	}
}

// TestExplicitBindingWinsOverInference makes the precedence explicit: an
// interface registered on purpose must not be second-guessed.
func TestExplicitBindingWinsOverInference(t *testing.T) {
	app := newTestApp()
	app.RegisterSingleton(englishGreeter{})
	RegisterSingletonAs[greeter](app, secondGreeter{})

	got, ok := Resolve[greeter](app)
	if !ok {
		t.Fatal("Resolve[greeter] found nothing")
	}
	if got.Greet() != "bonjour" {
		t.Errorf("Greet() = %q, want the explicitly bound \"bonjour\"", got.Greet())
	}
}

func TestInterfaceInjectionIntoAControllerField(t *testing.T) {
	type controller struct {
		Greeter greeter
		Hello   func(*gin.Context) `route:"GET:/hello"`
	}

	app := newTestApp()
	ctrl := &controller{}
	app.resolveDependencies(ctrl, containerWith(englishGreeter{}))

	if ctrl.Greeter == nil {
		t.Fatal("the greeter field was not injected from its sole implementation")
	}
	if ctrl.Greeter.Greet() != "hello" {
		t.Errorf("Greet() = %q, want \"hello\"", ctrl.Greeter.Greet())
	}
}

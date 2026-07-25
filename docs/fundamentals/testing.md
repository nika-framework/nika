# Testing

`common/nikatest` boots a Nika application in-process and drives it through
`httptest` — no port binding, no server, no sleeping. It covers HTTP endpoints,
message handlers, response content and the DI graph itself.

```go
import "github.com/nika-framework/nika/common/nikatest"

func TestCreateUser(t *testing.T) {
    app := nikatest.New(t)
    nikatest.OverrideAs[UserRepository](app, &fakeUserRepo{})
    app.LoadModule(src.NewAppModule())

    app.POST("/users").
        JSON(map[string]any{"name": "Ada", "email": "ada@example.com"}).
        Do().
        ExpectCreated().
        ExpectJSONPath("data.name", "Ada").
        ExpectJSONPathExists("data.id")
}
```

Every assertion calls `t.Helper()`, so a failure is reported at the line in your
test rather than inside the harness.

## Booting the app

```go
app := nikatest.New(t)                          // fresh app, gin test mode
app := nikatest.New(t, nikatest.Options{        // with configuration
    Config:  nika.Config{SecurityHeaders: true},
    Timeout: 2 * time.Second,
})
app := nikatest.Wrap(t, existingApp)            // wrap a production bootstrap
```

`New` registers a `t.Cleanup` that runs the app's shutdown hooks, so a fake cache,
a memory transport or anything else opened during the test is always released — a
goroutine leaked here surfaces as a flake in an unrelated test.

Requests are bounded by `Options.Timeout` (10 s by default) so a deadlocked
handler fails the test instead of hanging the suite.

### Replacing dependencies

```go
app := nikatest.New(t)

// Under an interface — how a fake repository replaces the real one.
nikatest.OverrideAs[UserRepository](app, newFakeRepo())

// Under its concrete type.
app.Override(&fakeMailer{})

app.LoadModule(src.NewAppModule())
```

Overrides must be registered **before** `LoadModule`: module loading resolves
against a snapshot of the container taken at that moment, so a fake registered
afterwards is silently ignored.

To reach a provider after driving the API:

```go
mailer := nikatest.Resolve[*fakeMailer](app)
if len(mailer.sent) != 1 { ... }
```

### Guards

```go
app.StubGuard("Auth", "Admin")               // always allow
app.DenyGuard("Admin", http.StatusForbidden) // always reject
app.AddGuard("Auth", myRealGuard)            // the real thing
```

`DenyGuard` is how you prove a route really is protected:

```go
app := nikatest.New(t).DenyGuard("Admin", 403)
app.LoadModule(src.NewAppModule())

app.DELETE("/users/1").Do().
    ExpectForbidden().
    ExpectNotContains("deleted") // the handler must not have run
```

## Building requests

```go
app.GET("/users")
app.POST("/users")
app.PUT("/users/1")
app.PATCH("/users/1")
app.DELETE("/users/1")
app.HEAD("/users")
app.OPTIONS("/users")
app.Request("PROPFIND", "/x")
```

Then chain modifiers and finish with `.Do()`:

```go
app.GET("/users").
    Query("page", "2").
    Queries(map[string]string{"sort": "name", "order": "asc"}).
    Header("X-Tenant", "acme").
    BearerToken(token).
    Cookie("session", "abc").
    Do()
```

Headers set on the app apply to every request:

```go
app.BearerToken(token)          // all requests
app.Header("X-API-Version", "2")
```

### Bodies

```go
// JSON from any value
app.POST("/users").JSON(CreateUserDto{Name: "Ada"}).Do()

// Verbatim, to reach the parse-error path
app.POST("/users").JSON(`{"name": `).Do()

// Form encoded
app.POST("/login").Form(map[string]string{"email": "a@b.c", "password": "x"}).Do()

// Plain text or raw bytes
app.POST("/hook").Text("ping").Do()
app.POST("/hook").Body("application/xml", xmlBytes).Do()

// Multipart, for file uploads
app.POST("/avatar").Multipart(func(m *nikatest.Multipart) {
    m.Field("caption", "my avatar")
    m.File("avatar", "a.png", pngBytes)
}).Do()
```

## Asserting responses

### Status

```go
res.ExpectStatus(201)
res.ExpectOK()             // 200
res.ExpectCreated()        // 201
res.ExpectNoContent()      // 204 and an empty body
res.ExpectBadRequest()     // 400
res.ExpectUnauthorized()   // 401
res.ExpectForbidden()      // 403
res.ExpectNotFound()       // 404
res.ExpectUnprocessable()  // 422
res.ExpectSuccess()        // any 2xx
res.ExpectStatusIn(200, 204)
```

A status mismatch prints the body, because "expected 200, got 500" on its own
tells you nothing about the cause.

### JSON

```go
// Subset: every key given must match; extra keys in the response are ignored.
res.ExpectJSON(`{"success": true, "data": {"name": "Ada"}}`)

// Exact: no extra keys allowed.
res.ExpectJSONEquals(`{"success": true}`)

// Dotted paths, with array indices
res.ExpectJSONPath("data.users.0.email", "ada@example.com")
res.ExpectJSONPath("data.total", 42)
res.ExpectJSONPath("data.users.-1.id", "u9")   // negative counts from the end
res.ExpectJSONPathExists("data.id", "data.token")
res.ExpectJSONLen("data.users", 3)

// Decode into a struct
var body struct {
    Success bool   `json:"success"`
    Data    []User `json:"data"`
}
res.DecodeJSON(&body)
```

Prefer `ExpectJSON` over `ExpectJSONEquals`. Pinning a whole document makes the
test fail every time an unrelated field is added, which trains people to update
expectations without reading them.

Numbers compare by value, so an expected `42` matches a decoded `42`, `42.0` and
`json.Number("42")` alike. Integers are decoded with `UseNumber`, so a large
`int64` id keeps its precision instead of becoming a lossy `float64`.

### The framework's response envelope

```go
res.ExpectAPISuccess()                          // success:true, no error object
res.ExpectAPIError("USER_NOT_FOUND")            // error.message
res.ExpectValidationError("name", "email")      // 422 naming each field
```

`ExpectValidationError` checks the 422 status and that `error.details` names every
field you list — the most-asserted shape in an API suite and the most tedious to
check by hand.

### Content and headers

```go
res.ExpectContains("<h1>", "Welcome, Ada")
res.ExpectNotContains("<script>", "TODO")
res.ExpectMatches(`<h1>Welcome, \w+</h1>`)
res.ExpectBody("exact body")
res.ExpectEmpty()

res.ExpectHeader("Location", "/users/1")
res.ExpectHeaderContains("Content-Type", "text/html")
res.ExpectHeaderAbsent("X-Powered-By")
res.ExpectJSONContentType()
```

### Security assertions worth having

Two assertions catch bugs that are invisible until they are incidents:

```go
// A json:"-" dropped in a refactor leaks credentials.
res.ExpectJSONPathAbsent("data.password_hash").
    ExpectNotContains("$2a$")

// Session cookie flags — HttpOnly, Secure and a real SameSite.
res.ExpectSecureCookie("session")
```

### Diagnostics

```go
res.Debug()          // log status, headers and body
res.Status()          // int
res.BodyString()      // string
res.HeaderValue("X")  // string
res.Cookies()         // []*http.Cookie
```

## Content tests with golden files

For rendered output — an HTML page, a CSV export, a generated report — an inline
expectation is unreadable. Snapshot it instead:

```go
app.GET("/invoice/1").Do().
    ExpectOK().
    ExpectGolden("invoice_page")
```

Snapshots live in `testdata/golden/<name>.golden`. Record or refresh them with:

```bash
NIKA_UPDATE_GOLDEN=1 go test ./...
```

Then **read the diff before committing it**. A snapshot nobody reads is not a
test.

For JSON, `ExpectGoldenJSON` normalises to indented, key-sorted JSON first, so a
change in key order does not fail the test.

Generated ids and timestamps make a snapshot non-deterministic. Scrub them:

```go
res.ExpectGoldenScrubbed("user_created",
    nikatest.ScrubObjectID,
    nikatest.ScrubRFC3339,
)
```

Built-in scrubbers: `ScrubUUID`, `ScrubObjectID`, `ScrubRFC3339`, `ScrubJWT`. Add
your own with `nikatest.Scrub(pattern, replacement)`.

## Asserting the route surface

```go
app.ExpectRoute("GET", "/users").
    ExpectRoute("POST", "/users").
    ExpectNoRoute("GET", "/debug/pprof")

routes := app.Routes() // []string of "METHOD /path"
```

`ExpectNoRoute` is the cheapest way to catch a debug or admin endpoint that leaked
into a build.

## Testing message handlers

The message harness wires the in-memory transport, so tests exercise the real
dispatch path — router, guards, middleware, binding, validation, encoding — with
no broker:

```go
func TestUserMessages(t *testing.T) {
    ms := nikatest.NewMicroservice(t)
    nikatest.OverrideAs[UserRepository](ms.App(), newFakeRepo())
    ms.LoadModule(src.NewAppModule())

    ms.Send("user_created", CreateUserDto{Name: "Ada"}).
        ExpectStatus(201).
        ExpectNoError().
        ExpectJSONPath("data.name", "Ada")

    ms.Send("order_created", nil).ExpectNoHandler()
}
```

Controllers under test declare `transport:"memory"`, or use
`nikatest.TransportName`.

### Pinning pattern precedence

The behaviour most likely to regress is which handler wins when two patterns
match. Assert it directly:

```go
ms.ExpectPattern("user_created").
    ExpectPattern("user_*").
    ExpectRoutesTo("user_created", "user_created"). // exact beats wildcard
    ExpectRoutesTo("user_23", "user_*").
    ExpectRoutesTo("users", "users")
```

### Headers, raw payloads and clients

```go
// Exercise a guard that reads an envelope header
ms.SendWithHeaders("user_delete", nil, map[string]string{
    "Authorization": "Bearer admin",
}).ExpectOK()

// Reach the malformed-input path
ms.SendRaw("user_created", `{"name":`).ExpectBadRequest()

// Fire and forget
ms.Emit("user_created", dto)

// A real client, for testing code that calls out through *microservice.Client
client := ms.Client()
err := client.Send(ctx, "user_created", dto, &out)
```

Prefer `Send` over `Emit`: `Send` dispatches synchronously and returns the reply,
so there is nothing to wait for. A test that polls for a side effect after `Emit`
is a test that eventually flakes.

Replies support every `Response` assertion plus the envelope-level ones:

```go
reply.ExpectNoError()
reply.ExpectError("VALIDATION_ERROR")
reply.ExpectNoHandler()
reply.ExpectPattern("user_created")
reply.Envelope()   // the raw *microservice.Envelope
```

### One controller, both transports

If a handler carries both a `route` and a `transport` tag, assert that the two
entry points cannot drift apart:

```go
app := nikatest.New(t)
ms := nikatest.Attach(app)
ms.LoadModule(src.NewAppModule())

httpBody := app.GET("/users").Do().ExpectOK().BodyString()
msgBody := ms.Send("users", nil).ExpectOK().BodyString()

if httpBody != msgBody {
    t.Errorf("the HTTP and message responses differ")
}
```

## Unit-testing providers

Providers are plain structs; no harness is needed.

```go
func TestUserService_Create(t *testing.T) {
    repo := newFakeRepo()
    svc := src.NewUserService(repo)

    user, err := svc.Create(t.Context(), "Ada", "ada@example.com")
    if err != nil {
        t.Fatalf("Create returned %v", err)
    }
    if user.Name != "Ada" {
        t.Errorf("Name = %q, want \"Ada\"", user.Name)
    }
}
```

## Testing against a real database

Repository tests that need a live database belong behind a build tag, so the
default `go test ./...` stays fast and hermetic:

```go
//go:build integration

package repository_test

func TestUserRepository(t *testing.T) {
    dsn := os.Getenv("TEST_DATABASE_URL")
    if dsn == "" {
        t.Skip("TEST_DATABASE_URL is not set")
    }
    ...
}
```

```bash
go test ./...                    # unit tests only
go test -tags integration ./...  # with a database
```

The framework's own suite follows this convention: `sqldb_integration`,
`redis_integration`, `nats_integration`, `rabbitmq_integration` and
`kafka_integration`.

## Running the suite

```bash
go test ./...                              # everything
go test -race ./...                        # with the race detector
go test -run TestCreateUser ./...          # one test
NIKA_UPDATE_GOLDEN=1 go test ./...         # re-record snapshots
go test -cover ./...                       # coverage
```

Always run `-race` in CI. Several framework guarantees — the DI container's
locking, the router's resolution cache, the transports' correlation maps — are
only meaningfully checked with the detector on.

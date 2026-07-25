# Nika

Nika is a modern backend framework for Go, designed for scalability, clean
architecture, and developer productivity.

## Documents

[Click me](https://nika-framework.github.io/nika/)

## Example

```go
package main

import (
	"fmt"

	"github.com/nika-framework/nika"
)

func main() {
	app := nika.NewApp()

	rootModule := src.NewAppModule()
	app.LoadModule(rootModule)

	port := "3001"
	fmt.Printf("🚀 Nika is running on http://localhost:%s\n", port)
	app.Listen(":" + port)
}
```

`NewApp()` starts from hardened defaults: panic recovery, a 10 MiB request-body
cap, a header-read timeout, JSON error bodies for unmatched routes, and no
implicit trust in forwarding proxies (so `ClientIP()` cannot be spoofed and
IP-based rate limiting holds). Pass a `nika.Config` to change any of them.

`Listen` drains in-flight requests and runs your shutdown hooks on SIGINT and
SIGTERM.

## Controllers

Routes are struct tags:

```go
type UserController struct {
	List   func(*gin.Context) `route:"GET:/users"`
	Get    func(*gin.Context) `route:"GET:/users/:id"`
	Create func(*gin.Context) `route:"POST:/users"`
	Delete func(*gin.Context) `route:"DELETE:/users/:id" guard:"Auth Roles(admin)"`
}
```

## Microservices

The same handler serves a message subject by adding a transport and a pattern:

```go
type UserController struct {
	Create   func(*gin.Context) `transport:"redis" pattern:"user_created"`
	FindOne  func(*gin.Context) `transport:"redis" pattern:"user_*"`
	ListUser func(*gin.Context) `transport:"redis" pattern:"users"`
}
```

A client sends `user_created`, `user_23` and `users`; they reach `Create`,
`FindOne` and `ListUser`. Setup takes a transport and its options, and nothing
else:

```go
microservice.Setup(app, microservice.Config{
	Transport: redismq.MustNew(redismq.Options{URL: "redis://localhost:6379"}),
})
```

Transports: **Redis**, **NATS**, **RabbitMQ**, **Kafka**, **gRPC**, **TCP**, and
an in-memory one for tests and modular monoliths. Guards, binding and validation
behave exactly as they do over HTTP.

See [Microservices](docs/microservices/basics.md).

## Testing

```go
func TestCreateUser(t *testing.T) {
	app := nikatest.New(t)
	nikatest.OverrideAs[UserRepository](app, &fakeUserRepo{})
	app.LoadModule(src.NewAppModule())

	app.POST("/users").
		JSON(map[string]any{"name": "Ada", "email": "ada@example.com"}).
		Do().
		ExpectCreated().
		ExpectJSONPath("data.name", "Ada").
		ExpectJSONPathAbsent("data.password_hash")
}
```

Message handlers are tested the same way, with no broker running:

```go
ms := nikatest.NewMicroservice(t)
ms.LoadModule(src.NewAppModule())

ms.Send("user_created", dto).ExpectStatus(201).ExpectJSONPath("data.name", "Ada")
ms.ExpectRoutesTo("user_23", "user_*")
```

See [Testing](docs/fundamentals/testing.md).

## Run the tests

```bash
go test -race ./...
```

Integration tests that need a live broker or database are behind build tags and
do not run by default:

```bash
go test -tags redis_integration ./...
go test -tags sqldb_integration ./...
```

## Run the docs

```bash
mkdocs serve
```

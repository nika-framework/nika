package microservice_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/gin-gonic/gin"
	"github.com/nika-framework/nika"
	"github.com/nika-framework/nika/common/microservice"
)

// These examples are compiled by `go test`, so the API they show cannot drift
// away from the documentation without CI noticing.

type CreateUserDto struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// UserController is the shape described in the documentation: one struct whose
// fields declare a transport and a pattern.
type UserController struct {
	Create   func(*gin.Context) `transport:"memory" pattern:"user_created"`
	FindOne  func(*gin.Context) `transport:"memory" pattern:"user_*"`
	ListUser func(*gin.Context) `transport:"memory" pattern:"users"`
}

func newUserController() *UserController {
	return &UserController{
		Create: func(c *gin.Context) {
			var dto CreateUserDto
			if err := c.ShouldBindJSON(&dto); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{
					"success": false,
					"error":   gin.H{"code": 400, "message": "INVALID_JSON"},
				})
				return
			}
			c.JSON(http.StatusCreated, gin.H{
				"success": true,
				"data":    gin.H{"id": "u1", "name": dto.Name},
			})
		},

		FindOne: func(c *gin.Context) {
			// A wildcard handler reads the literal subject the client sent, which
			// is where the id lives.
			id := strings.TrimPrefix(microservice.PatternFrom(c), "user_")
			c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"id": id}})
		},

		ListUser: func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"success": true, "data": []gin.H{{"id": "u1"}}})
		},
	}
}

// Example shows a full server and client round trip. It uses the in-memory
// transport so it runs as part of the test suite; swap the constructor for
// redismq.MustNew, natsmq.MustNew and so on to reach a real broker.
func Example() {
	app := nika.NewApp(nika.Config{Mode: gin.TestMode, DisableGracefulShutdown: true})

	transport := microservice.NewMemory()

	// Setup takes the transport and its options, and nothing else.
	server, err := microservice.Setup(app, microservice.Config{Transport: transport})
	if err != nil {
		fmt.Println("setup:", err)
		return
	}

	app.RegisterControllers(newUserController())

	// Consumers start with the app, not at Setup: handlers must be registered
	// first.
	if err := app.Start(context.Background()); err != nil {
		fmt.Println("start:", err)
		return
	}
	defer server.Stop(context.Background())

	client := microservice.NewClient(transport)

	// An exact pattern reaches Create, even though "user_*" also matches it.
	var created struct {
		Data struct{ Name string } `json:"data"`
	}
	if err := client.Send(context.Background(), "user_created",
		CreateUserDto{Name: "Ada", Email: "ada@example.com"}, &created); err != nil {
		fmt.Println("create:", err)
		return
	}
	fmt.Println("created:", created.Data.Name)

	// A subject with no exact handler falls to the wildcard.
	var found struct {
		Data struct{ ID string } `json:"data"`
	}
	if err := client.Send(context.Background(), "user_23", nil, &found); err != nil {
		fmt.Println("find:", err)
		return
	}
	fmt.Println("found:", found.Data.ID)

	// Output:
	// created: Ada
	// found: 23
}

// ExampleClient_Send_errorHandling shows how to tell a rejection from a
// non-delivery, which is what decides whether retrying is safe.
func ExampleClient_Send_errorHandling() {
	app := nika.NewApp(nika.Config{Mode: gin.TestMode, DisableGracefulShutdown: true})
	transport := microservice.NewMemory()

	server, _ := microservice.Setup(app, microservice.Config{Transport: transport})
	app.RegisterControllers(newUserController())
	_ = app.Start(context.Background())
	defer server.Stop(context.Background())

	client := microservice.NewClient(transport)
	err := client.Send(context.Background(), "order_created", nil, nil)

	var envErr *microservice.EnvelopeError
	switch {
	case errors.As(err, &envErr):
		// The remote service answered. A 4xx must not be retried.
		fmt.Printf("service rejected the message: %d %s\n", envErr.Code, envErr.Message)
	case errors.Is(err, microservice.ErrTimeout):
		fmt.Println("nobody answered; retrying may be correct")
	case err != nil:
		fmt.Println("transport failure:", err)
	}

	// Output:
	// service rejected the message: 404 PATTERN_NOT_FOUND
}

// ExampleServer_Listen shows a worker process with no HTTP listener.
func ExampleServer_Listen() {
	app := nika.NewApp()

	server, err := microservice.Setup(app, microservice.Config{
		Transport: microservice.NewMemory(),

		// One handler at a time, for a queue whose ordering matters.
		Concurrency: 1,
	})
	if err != nil {
		return
	}

	app.RegisterControllers(newUserController())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Listen blocks until the signal arrives, then drains in-flight handlers and
	// closes the transport.
	_ = server.Listen(ctx)
}

// ExampleIsMessage shows one handler serving both an HTTP route and a message
// subject, which is what the two tags on one field are for.
func ExampleIsMessage() {
	type controller struct {
		Get func(*gin.Context) `route:"GET:/users/:id" transport:"memory" pattern:"user_*"`
	}

	handler := func(c *gin.Context) {
		// Over HTTP the id is a path parameter; over a message transport it is
		// part of the subject.
		id := c.Param("id")
		if microservice.IsMessage(c) {
			id = strings.TrimPrefix(microservice.PatternFrom(c), "user_")
		}
		c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"id": id}})
	}

	app := nika.NewApp(nika.Config{Mode: gin.TestMode, DisableGracefulShutdown: true})
	transport := microservice.NewMemory()

	server, _ := microservice.Setup(app, microservice.Config{Transport: transport})
	app.RegisterControllers(&controller{Get: handler})
	_ = app.Start(context.Background())
	defer server.Stop(context.Background())

	client := microservice.NewClient(transport)

	var reply struct {
		Data struct{ ID string } `json:"data"`
	}
	_ = client.Send(context.Background(), "user_77", nil, &reply)
	fmt.Println("from a message:", reply.Data.ID)

	// Output:
	// from a message: 77
}

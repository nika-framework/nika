package microservice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/nika-framework/nika"
)

// Client sends messages to a microservice. It takes a transport and its options
// and nothing more; from there, every call is a pattern plus a payload.
//
//	client := microservice.NewClient(redismq.New(redismq.Options{URL: "redis://localhost:6379"}))
//	defer client.Close()
//
//	client.Emit(ctx, "user_created", CreateUserDto{Name: "Ada"})   // fire and forget
//
//	var user User
//	err := client.Send(ctx, "user_23", nil, &user)                 // request/reply
type Client struct {
	publisher Publisher
	timeout   time.Duration
	headers   map[string]string
}

// ClientOption configures a Client.
type ClientOption func(*Client)

// WithTimeout sets the default request/reply timeout.
func WithTimeout(timeout time.Duration) ClientOption {
	return func(c *Client) {
		if timeout > 0 {
			c.timeout = timeout
		}
	}
}

// WithHeader adds a header sent with every message — a service identity, a
// tenant id, a trace parent.
func WithHeader(key, value string) ClientOption {
	return func(c *Client) {
		if c.headers == nil {
			c.headers = make(map[string]string, 4)
		}
		c.headers[key] = value
	}
}

// NewClient wraps a publisher.
func NewClient(publisher Publisher, opts ...ClientOption) *Client {
	client := &Client{
		publisher: publisher,
		timeout:   DefaultRequestTimeout,
	}
	for _, opt := range opts {
		opt(client)
	}
	return client
}

// SetupClient builds a client and registers it in the DI container so
// controllers can inject *microservice.Client, closing it when the app stops.
func SetupClient(app *nika.App, publisher Publisher, opts ...ClientOption) (*Client, error) {
	if app == nil {
		return nil, errors.New("microservice: app is required")
	}
	if publisher == nil {
		return nil, errors.New("microservice: publisher is required")
	}

	client := NewClient(publisher, opts...)
	app.RegisterSingleton(client)
	app.OnShutdown(func(context.Context) error { return client.Close() })
	return client, nil
}

// Transport returns the name of the underlying transport.
func (c *Client) Transport() string {
	if c.publisher == nil {
		return ""
	}
	return c.publisher.Name()
}

// Emit publishes an event and returns as soon as the transport accepts it. Use
// it when no answer is expected; nothing about delivery is guaranteed beyond
// what the transport itself guarantees.
func (c *Client) Emit(ctx context.Context, pattern string, payload any) error {
	env, err := c.envelope(pattern, payload)
	if err != nil {
		return err
	}
	return c.publisher.Publish(ctx, env)
}

// Request sends a message and returns the raw reply envelope, including its
// status and error, without interpreting them.
func (c *Client) Request(ctx context.Context, pattern string, payload any) (*Envelope, error) {
	env, err := c.envelope(pattern, payload)
	if err != nil {
		return nil, err
	}
	return c.publisher.Request(ctx, env, c.timeout)
}

// Send performs a request/reply exchange and decodes a successful reply into out.
//
// A reply carrying a handler error is returned as an *EnvelopeError, so a caller
// can distinguish "the remote service said no" from "the message never arrived"
// with errors.As — a distinction that decides whether retrying is safe.
func (c *Client) Send(ctx context.Context, pattern string, payload any, out any) error {
	reply, err := c.Request(ctx, pattern, payload)
	if err != nil {
		return err
	}
	if reply == nil {
		return fmt.Errorf("microservice: empty reply for pattern %q", pattern)
	}
	if reply.Error != nil {
		return reply.Error
	}
	if out == nil {
		return nil
	}
	if len(reply.Data) == 0 {
		return nil
	}
	if err := json.Unmarshal(reply.Data, out); err != nil {
		return fmt.Errorf("microservice: cannot decode reply for %q: %w", pattern, err)
	}
	return nil
}

// SendTo is Send with a per-call timeout, for the occasional slow operation that
// should not force a higher default on every other call.
func (c *Client) SendTo(ctx context.Context, pattern string, payload any, out any, timeout time.Duration) error {
	env, err := c.envelope(pattern, payload)
	if err != nil {
		return err
	}
	if timeout <= 0 {
		timeout = c.timeout
	}

	reply, err := c.publisher.Request(ctx, env, timeout)
	if err != nil {
		return err
	}
	if reply.Error != nil {
		return reply.Error
	}
	if out == nil || len(reply.Data) == 0 {
		return nil
	}
	return json.Unmarshal(reply.Data, out)
}

// Close releases the underlying transport.
func (c *Client) Close() error {
	if c.publisher == nil {
		return nil
	}
	return c.publisher.Close()
}

// envelope builds and validates an outbound envelope.
func (c *Client) envelope(pattern string, payload any) (*Envelope, error) {
	if c.publisher == nil {
		return nil, errors.New("microservice: client has no transport")
	}
	// A wildcard is a receive-side concept; sending to one would be ambiguous
	// about which of several services should answer.
	if Pattern(pattern).IsWildcard() {
		return nil, fmt.Errorf("microservice: cannot send to wildcard pattern %q — send a literal subject", pattern)
	}
	if err := Pattern(pattern).Validate(); err != nil {
		return nil, fmt.Errorf("microservice: %w", err)
	}

	env, err := NewEnvelope(pattern, payload)
	if err != nil {
		return nil, err
	}
	for key, value := range c.headers {
		env.WithHeader(key, value)
	}
	return env, nil
}

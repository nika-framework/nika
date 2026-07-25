package microservice

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// MemoryTransport is an in-process transport. It implements the full Transport
// contract — publish, request/reply, wildcard patterns — against a channel
// instead of a broker.
//
// It exists for two reasons. It makes the microservice layer testable without
// docker-compose, which is what keeps `go test ./...` fast and hermetic. And it
// is a legitimate production choice for a modular monolith: the same handlers and
// the same client calls work unchanged, so splitting a module out into its own
// service later means swapping one constructor.
type MemoryTransport struct {
	mu       sync.Mutex
	messages chan *deliverable
	closed   bool
	closeCh  chan struct{}

	// buffer sizes the queue. A full queue applies backpressure to publishers
	// rather than growing without bound.
	buffer int

	dispatchMu sync.RWMutex
	dispatch   Dispatcher

	// ready is closed once Listen has installed the dispatcher. Listen runs in a
	// supervisor goroutine, so a caller that publishes immediately after Start
	// would otherwise race it; waiting on a channel removes the race without a
	// sleep.
	ready     chan struct{}
	readyOnce sync.Once
}

// deliverable pairs a message with the channel its reply must go back on.
type deliverable struct {
	env   *Envelope
	reply chan *Envelope
}

// MemoryOptions configures a MemoryTransport.
type MemoryOptions struct {
	// Buffer is the queue depth. Defaults to 256.
	Buffer int
}

// NewMemory returns an in-process transport.
func NewMemory(opts ...MemoryOptions) *MemoryTransport {
	var options MemoryOptions
	if len(opts) > 0 {
		options = opts[0]
	}
	if options.Buffer <= 0 {
		options.Buffer = 256
	}

	return &MemoryTransport{
		messages: make(chan *deliverable, options.Buffer),
		closeCh:  make(chan struct{}),
		ready:    make(chan struct{}),
		buffer:   options.Buffer,
	}
}

// Name implements Listener and Publisher.
func (m *MemoryTransport) Name() string { return TransportMemory }

// Listen consumes queued messages until ctx is cancelled.
func (m *MemoryTransport) Listen(ctx context.Context, _ []string, dispatch Dispatcher) error {
	if dispatch == nil {
		return errors.New("microservice: memory transport needs a dispatcher")
	}

	m.dispatchMu.Lock()
	m.dispatch = dispatch
	m.dispatchMu.Unlock()
	m.readyOnce.Do(func() { close(m.ready) })

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-m.closeCh:
			return nil
		case msg := <-m.messages:
			m.handle(ctx, msg, dispatch)
		}
	}
}

// handle dispatches one message and, when a reply is expected, delivers it.
func (m *MemoryTransport) handle(ctx context.Context, msg *deliverable, dispatch Dispatcher) {
	reply, err := dispatch(ctx, msg.env)
	if msg.reply == nil {
		return
	}

	if reply == nil {
		reply = replyError(msg.env, 500, "DISPATCH_ERROR", errText(err))
	}

	// Never block on a caller that has already given up and stopped listening.
	select {
	case msg.reply <- reply:
	default:
	}
}

func errText(err error) string {
	if err == nil {
		return "handler produced no reply"
	}
	return err.Error()
}

// Publish enqueues an envelope without waiting for a reply.
func (m *MemoryTransport) Publish(ctx context.Context, env *Envelope) error {
	m.mu.Lock()
	closed := m.closed
	m.mu.Unlock()
	if closed {
		return ErrClosed
	}

	select {
	case m.messages <- &deliverable{env: env}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-m.closeCh:
		return ErrClosed
	}
}

// Request enqueues an envelope and waits for its reply.
func (m *MemoryTransport) Request(ctx context.Context, env *Envelope, timeout time.Duration) (*Envelope, error) {
	m.mu.Lock()
	closed := m.closed
	m.mu.Unlock()
	if closed {
		return nil, ErrClosed
	}

	if timeout <= 0 {
		timeout = DefaultRequestTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Buffered so the consumer's send never blocks even if we time out first.
	replyCh := make(chan *Envelope, 1)

	select {
	case m.messages <- &deliverable{env: env, reply: replyCh}:
	case <-ctx.Done():
		return nil, joinTimeout(ctx.Err())
	case <-m.closeCh:
		return nil, ErrClosed
	}

	select {
	case reply := <-replyCh:
		return reply, nil
	case <-ctx.Done():
		return nil, joinTimeout(ctx.Err())
	case <-m.closeCh:
		return nil, ErrClosed
	}
}

// joinTimeout maps a context deadline onto ErrTimeout so callers can test for a
// timeout without caring which layer produced it.
func joinTimeout(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrTimeout
	}
	return err
}

// Close stops the transport. It is safe to call more than once.
func (m *MemoryTransport) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	m.closed = true
	close(m.closeCh)
	return nil
}

// Dispatch runs a message straight through the registered dispatcher, bypassing
// the queue. Tests use it to assert a handler's behaviour synchronously, with no
// goroutine and no polling.
func (m *MemoryTransport) Dispatch(ctx context.Context, env *Envelope) (*Envelope, error) {
	if err := m.WaitReady(ctx); err != nil {
		return nil, err
	}

	m.dispatchMu.RLock()
	dispatch := m.dispatch
	m.dispatchMu.RUnlock()

	if dispatch == nil {
		return nil, errors.New("microservice: memory transport is not listening")
	}
	return dispatch(ctx, env)
}

// WaitReady blocks until the transport is consuming, or ctx expires.
//
// The error names the likely cause rather than reporting a bare deadline: by far
// the most common reason readiness never arrives is that no handler declared
// `transport:"memory"`, in which case the server never subscribes at all.
func (m *MemoryTransport) WaitReady(ctx context.Context) error {
	select {
	case <-m.ready:
		return nil
	case <-m.closeCh:
		return ErrClosed
	case <-ctx.Done():
		return fmt.Errorf(
			"microservice: memory transport never started consuming — is any handler tagged transport:%q, and was the app started? (%w)",
			TransportMemory, ctx.Err(),
		)
	}
}

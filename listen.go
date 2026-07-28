package nika

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// Listen serves HTTP on addr and blocks until the server stops.
//
// Unlike gin's Run, this configures the timeouts that make a public listener
// survivable — ReadHeaderTimeout in particular, without which a handful of
// Slowloris connections can hold every worker — and drains in-flight requests on
// SIGINT/SIGTERM instead of dropping them.
//
// It returns nil on a clean shutdown.
func (a *App) Listen(addr string) error {
	if err := a.Start(context.Background()); err != nil {
		return fmt.Errorf("nika: start hook failed: %w", err)
	}

	server := a.newServer(addr)

	a.serverMu.Lock()
	a.server = server
	a.serverMu.Unlock()

	fmt.Printf("\n***🚀 Nika is running on http://localhost%s *****\n", addr)

	if a.cfg.DisableGracefulShutdown {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}

	return a.serveWithGracefulShutdown(server, func() error { return server.ListenAndServe() })
}

// ListenTLS serves HTTPS on addr using the given certificate and key files.
func (a *App) ListenTLS(addr, certFile, keyFile string) error {
	if err := a.Start(context.Background()); err != nil {
		return fmt.Errorf("nika: start hook failed: %w", err)
	}

	server := a.newServer(addr)

	a.serverMu.Lock()
	a.server = server
	a.serverMu.Unlock()

	fmt.Printf("\n***🚀 Nika is running on https://localhost%s *****\n", addr)

	if a.cfg.DisableGracefulShutdown {
		if err := server.ListenAndServeTLS(certFile, keyFile); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}

	return a.serveWithGracefulShutdown(server, func() error {
		return server.ListenAndServeTLS(certFile, keyFile)
	})
}

// Serve serves on an already-open listener. Useful when the socket is supplied
// by a supervisor, or bound to port 0 in tests.
func (a *App) Serve(listener net.Listener) error {
	if err := a.Start(context.Background()); err != nil {
		return fmt.Errorf("nika: start hook failed: %w", err)
	}

	server := a.newServer(listener.Addr().String())

	a.serverMu.Lock()
	a.server = server
	a.serverMu.Unlock()

	if a.cfg.DisableGracefulShutdown {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}

	return a.serveWithGracefulShutdown(server, func() error { return server.Serve(listener) })
}

// newServer builds the http.Server from the effective config.
func (a *App) newServer(addr string) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           a.engine,
		ReadHeaderTimeout: a.cfg.ReadHeaderTimeout,
		ReadTimeout:       a.cfg.ReadTimeout,
		WriteTimeout:      a.cfg.WriteTimeout,
		IdleTimeout:       a.cfg.IdleTimeout,
		MaxHeaderBytes:    a.cfg.MaxHeaderBytes,
	}
}

// serveWithGracefulShutdown runs serve until it fails or a termination signal
// arrives, then drains connections and runs the shutdown hooks.
func (a *App) serveWithGracefulShutdown(server *http.Server, serve func() error) error {
	// Buffered so the goroutine can always exit even if nobody reads the error.
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- serve()
	}()

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signals)

	select {
	case err := <-serveErr:
		// The listener died on its own — for example the port was taken. Still
		// run the hooks so already-open resources are released.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), a.cfg.ShutdownTimeout)
		defer cancel()
		hookErr := a.runShutdownHooks(shutdownCtx)

		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return hookErr

	case sig := <-signals:
		fmt.Printf("\n⏳ %s received, draining connections (max %s)...\n", sig, a.cfg.ShutdownTimeout)
		return a.shutdown(server, serveErr)
	}
}

// shutdown stops accepting new connections, waits for in-flight requests, and
// then runs the shutdown hooks.
func (a *App) shutdown(server *http.Server, serveErr <-chan error) error {
	ctx, cancel := context.WithTimeout(context.Background(), a.cfg.ShutdownTimeout)
	defer cancel()

	shutdownErr := server.Shutdown(ctx)

	// Drain the serve goroutine so it cannot leak; it returns ErrServerClosed.
	select {
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) && shutdownErr == nil {
			shutdownErr = err
		}
	case <-time.After(time.Second):
	}

	hookErr := a.runShutdownHooks(ctx)

	if shutdownErr != nil {
		return fmt.Errorf("nika: graceful shutdown: %w", shutdownErr)
	}
	if hookErr != nil {
		return fmt.Errorf("nika: shutdown hook: %w", hookErr)
	}

	fmt.Println("✅ Nika stopped cleanly")
	return nil
}

// Shutdown stops a running server from another goroutine, draining in-flight
// requests within ctx and then running the shutdown hooks.
func (a *App) Shutdown(ctx context.Context) error {
	a.serverMu.Lock()
	server := a.server
	a.serverMu.Unlock()

	var shutdownErr error
	if server != nil {
		shutdownErr = server.Shutdown(ctx)
	}

	hookErr := a.runShutdownHooks(ctx)

	if shutdownErr != nil {
		return shutdownErr
	}
	return hookErr
}

// RunWorker starts the app's background work and blocks until SIGINT or SIGTERM,
// then drains it.
//
// It is the counterpart to Listen for a process that serves no HTTP: a message
// consumer, a scheduler, a queue worker. Without it such a process has to
// hand-roll signal handling, and the common mistake is to forget entirely — main
// returns as soon as the wiring is done, the process exits, and the consumers that
// were just registered never handle anything.
//
//	func main() {
//	    app := nika.NewApp()
//	    microservice.Setup(app, microservice.Config{Transport: transport})
//	    app.LoadModule(src.NewAppModule())
//	    app.RunWorker()
//	}
//
// Start hooks run first, so consumers only begin after every handler is
// registered. On a signal, the shutdown hooks run with a context bounded by
// Config.ShutdownTimeout, which is what closes broker connections and lets
// in-flight handlers finish.
func (a *App) RunWorker() error {
	return a.RunWorkerContext(context.Background())
}

// RunWorkerContext is RunWorker with a caller-supplied context. It stops when the
// context is cancelled or a termination signal arrives, whichever comes first.
func (a *App) RunWorkerContext(ctx context.Context) error {
	runCtx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := a.Start(runCtx); err != nil {
		return fmt.Errorf("nika: start hook failed: %w", err)
	}

	fmt.Println("\n***🚀 Nika worker is running — press Ctrl+C to stop *****")

	<-runCtx.Done()

	// Release the signal handler before draining, so a second Ctrl+C kills the
	// process instead of being swallowed by a shutdown that is taking too long.
	stop()

	fmt.Printf("\n⏳ draining (max %s)...\n", a.cfg.ShutdownTimeout)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), a.cfg.ShutdownTimeout)
	defer cancel()

	if err := a.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("nika: worker shutdown: %w", err)
	}

	fmt.Println("✅ Nika worker stopped cleanly")
	return nil
}

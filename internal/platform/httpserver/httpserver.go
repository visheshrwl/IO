// Package httpserver runs an HTTP server with production-safe timeouts and
// shuts it down gracefully when its context is cancelled.
//
// The zero-value http.Server has no timeouts at all: a single slow or idle
// client can hold a connection open indefinitely (a Slowloris). And
// http.ListenAndServe never returns until the process is killed, so an
// in-flight request is cut mid-response on every deploy. Run fixes both.
package httpserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// Default timeouts. They are deliberately conservative; a service that
// streams responses or accepts large uploads should raise WriteTimeout and
// ReadTimeout explicitly rather than disable them.
const (
	DefaultReadHeaderTimeout = 5 * time.Second
	DefaultReadTimeout       = 15 * time.Second
	DefaultWriteTimeout      = 15 * time.Second
	DefaultIdleTimeout       = 60 * time.Second
	DefaultShutdownTimeout   = 20 * time.Second
)

// Options configures Run. Only Addr and Handler are required.
type Options struct {
	Addr    string
	Handler http.Handler
	Logger  *slog.Logger

	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration

	// ShutdownTimeout bounds how long Run waits for in-flight requests to
	// finish after the context is cancelled before giving up.
	ShutdownTimeout time.Duration

	// BeforeShutdown runs once the context is cancelled, before the server
	// stops accepting connections. Use it to flip readiness to draining and
	// pause long enough for Kubernetes to remove the pod from the Service
	// endpoints, so no new request is routed here mid-shutdown.
	BeforeShutdown func()
}

func (o *Options) applyDefaults() {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.ReadHeaderTimeout == 0 {
		o.ReadHeaderTimeout = DefaultReadHeaderTimeout
	}
	if o.ReadTimeout == 0 {
		o.ReadTimeout = DefaultReadTimeout
	}
	if o.WriteTimeout == 0 {
		o.WriteTimeout = DefaultWriteTimeout
	}
	if o.IdleTimeout == 0 {
		o.IdleTimeout = DefaultIdleTimeout
	}
	if o.ShutdownTimeout == 0 {
		o.ShutdownTimeout = DefaultShutdownTimeout
	}
}

// Run starts the server and blocks until ctx is cancelled or the listener
// fails. On cancellation it stops accepting new connections and waits up to
// ShutdownTimeout for open requests to complete. A clean shutdown returns
// nil.
func Run(ctx context.Context, opts Options) error {
	opts.applyDefaults()

	srv := &http.Server{
		Addr:              opts.Addr,
		Handler:           opts.Handler,
		ReadHeaderTimeout: opts.ReadHeaderTimeout,
		ReadTimeout:       opts.ReadTimeout,
		WriteTimeout:      opts.WriteTimeout,
		IdleTimeout:       opts.IdleTimeout,
		ErrorLog:          slog.NewLogLogger(opts.Logger.Handler(), slog.LevelWarn),
	}

	serveErr := make(chan error, 1)
	go func() {
		opts.Logger.Info("http server listening", "addr", opts.Addr)
		err := srv.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		serveErr <- err
	}()

	select {
	case err := <-serveErr:
		if err != nil {
			return fmt.Errorf("httpserver: serve: %w", err)
		}
		return nil
	case <-ctx.Done():
		opts.Logger.Info("shutdown requested, draining connections", "timeout", opts.ShutdownTimeout)
	}

	if opts.BeforeShutdown != nil {
		opts.BeforeShutdown()
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), opts.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("httpserver: graceful shutdown: %w", err)
	}

	// Surface a listener error that raced the shutdown signal.
	if err := <-serveErr; err != nil {
		return fmt.Errorf("httpserver: serve: %w", err)
	}
	opts.Logger.Info("http server stopped")
	return nil
}

package httpx

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// ServerConfig is everything the server needs. Every timeout is required,
// because the zero value of http.Server means no timeout at all and a server
// with no read timeout is a server one slow client can hold open forever.
type ServerConfig struct {
	Addr              string
	Handler           http.Handler
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	ShutdownTimeout   time.Duration
	Logger            *slog.Logger
}

// Serve runs the server until the context is cancelled, then drains.
//
// Draining is bounded: in-flight requests get ShutdownTimeout to finish, and
// after that the process stops waiting. An unbounded drain turns one stuck
// request into a deployment that never completes.
func Serve(ctx context.Context, cfg ServerConfig) error {
	if err := cfg.validate(); err != nil {
		return err
	}

	server := &http.Server{
		Addr:              cfg.Addr,
		Handler:           cfg.Handler,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		// The base context carries no cancellation on purpose: cancelling it
		// would abort in-flight requests at shutdown rather than draining them.
		BaseContext: func(net.Listener) context.Context { return context.WithoutCancel(ctx) },
	}

	errCh := make(chan error, 1)
	go func() {
		cfg.Logger.InfoContext(ctx, "http server listening", slog.String("addr", cfg.Addr))
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("http server: %w", err)
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	cfg.Logger.InfoContext(context.WithoutCancel(ctx), "http server draining",
		slog.Duration("timeout", cfg.ShutdownTimeout))

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cfg.ShutdownTimeout)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		// Close is the honest fallback: the drain budget is spent and the
		// listener has to go, in-flight or not.
		_ = server.Close()
		return fmt.Errorf("http server drain exceeded its budget: %w", err)
	}
	return <-errCh
}

func (cfg ServerConfig) validate() error {
	switch {
	case cfg.Addr == "":
		return errors.New("the server needs an address")
	case cfg.Handler == nil:
		return errors.New("the server needs a handler")
	case cfg.Logger == nil:
		return errors.New("the server needs a logger")
	case cfg.ReadHeaderTimeout <= 0, cfg.ReadTimeout <= 0, cfg.WriteTimeout <= 0, cfg.IdleTimeout <= 0:
		return errors.New("every server timeout must be set")
	case cfg.ShutdownTimeout <= 0:
		return errors.New("the server needs a drain timeout")
	default:
		return nil
	}
}

// Package profiling exposes the Go runtime profiles on a private listener.
//
// The profiles are off by default and, when on, bound to loopback. They name
// every goroutine, inline the binary's own symbols, and let whoever can reach
// them take a thirty second cpu sample of a running process, which is both an
// information leak and a denial of service in one endpoint. Nothing about them
// belongs on a published port, so the address is not configurable: a profile is
// read through an exec into the container or through a port forward.
package profiling

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"strconv"
	"time"
)

// Config decides whether profiling runs and on which loopback port.
type Config struct {
	Enabled bool
	Port    int
}

// shutdownTimeout bounds how long a profile download delays a shutdown. A cpu
// profile runs for thirty seconds by default, and waiting for one is not a
// reason to keep a terminating process alive.
const shutdownTimeout = 2 * time.Second

// Serve runs the profiling listener until the context is cancelled. It returns
// immediately when profiling is disabled, so a caller can always start it.
func Serve(ctx context.Context, cfg Config, logger *slog.Logger) error {
	if !cfg.Enabled {
		return nil
	}
	if logger == nil {
		return errors.New("the profiler needs a logger")
	}
	if cfg.Port <= 0 || cfg.Port > 65535 {
		return fmt.Errorf("the profiling port %d is not a port", cfg.Port)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /debug/pprof/", pprof.Index)
	mux.HandleFunc("GET /debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("GET /debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("GET /debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("GET /debug/pprof/trace", pprof.Trace)

	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.Port))
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("cannot open the profiling listener: %w", err)
	}

	server := &http.Server{
		Handler: mux,
		// A cpu profile is a long read by design, so this listener has no write
		// timeout. The header timeout still applies, which is what keeps a
		// half-open connection from holding a goroutine forever.
		ReadHeaderTimeout: 5 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return context.WithoutCancel(ctx) },
	}

	errCh := make(chan error, 1)
	go func() {
		logger.WarnContext(ctx, "profiling is enabled", slog.String("addr", addr))
		if serveErr := server.Serve(listener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			errCh <- fmt.Errorf("profiling server: %w", serveErr)
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		// A profile still downloading is not a failure worth reporting up: the
		// process is going away either way.
		logger.WarnContext(shutdownCtx, "the profiling listener did not close cleanly",
			slog.String("error", err.Error()))
	}
	return <-errCh
}

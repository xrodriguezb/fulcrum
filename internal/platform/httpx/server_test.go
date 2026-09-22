package httpx_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/xrodriguezb/fulcrum/internal/platform/httpx"
	"github.com/xrodriguezb/fulcrum/internal/platform/logging"
)

func freeAddr(t *testing.T) string {
	t.Helper()

	var config net.ListenConfig
	listener, err := config.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a port: %v", err)
	}
	addr := listener.Addr().String()
	if closeErr := listener.Close(); closeErr != nil {
		t.Fatalf("release the port: %v", closeErr)
	}
	return addr
}

func serverConfig(t *testing.T, addr string, handler http.Handler) httpx.ServerConfig {
	t.Helper()
	return httpx.ServerConfig{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: time.Second,
		ReadTimeout:       2 * time.Second,
		WriteTimeout:      5 * time.Second,
		IdleTimeout:       2 * time.Second,
		ShutdownTimeout:   3 * time.Second,
		Logger:            logging.NewJSON(discard{}, 0),
	}
}

// A request that is in flight when the signal arrives has to finish. Dropping it
// would make every deployment a source of failed requests.
func TestServerDrainsInFlightRequests(t *testing.T) {
	t.Parallel()

	addr := freeAddr(t)
	released := make(chan struct{})
	entered := make(chan struct{})
	var once sync.Once
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		once.Do(func() { close(entered) })
		<-released
		w.WriteHeader(http.StatusOK)
	})

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- httpx.Serve(ctx, serverConfig(t, addr, handler)) }()

	waitForListener(t, addr)

	responses := make(chan int, 1)
	go func() {
		request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+addr+"/", nil)
		if err != nil {
			responses <- 0
			return
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			responses <- 0
			return
		}
		defer func() { _ = response.Body.Close() }()
		responses <- response.StatusCode
	}()

	// Wait until the request is actually inside the handler, then ask the server
	// to stop while it is still there.
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatalf("the request never reached the handler")
	}
	cancel()
	close(released)

	select {
	case status := <-responses:
		if status != http.StatusOK {
			t.Errorf("in flight request got status %d, want 200", status)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("the in flight request never completed")
	}

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Serve returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("the server did not return after draining")
	}
}

func TestServerRefusesIncompleteConfiguration(t *testing.T) {
	t.Parallel()

	cases := map[string]func(httpx.ServerConfig) httpx.ServerConfig{
		"no address":       func(c httpx.ServerConfig) httpx.ServerConfig { c.Addr = ""; return c },
		"no handler":       func(c httpx.ServerConfig) httpx.ServerConfig { c.Handler = nil; return c },
		"no read timeout":  func(c httpx.ServerConfig) httpx.ServerConfig { c.ReadTimeout = 0; return c },
		"no drain timeout": func(c httpx.ServerConfig) httpx.ServerConfig { c.ShutdownTimeout = 0; return c },
		"no logger":        func(c httpx.ServerConfig) httpx.ServerConfig { c.Logger = nil; return c },
	}

	base := serverConfig(t, "127.0.0.1:0", http.NotFoundHandler())
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := httpx.Serve(t.Context(), mutate(base)); err == nil {
				t.Errorf("a server with %s must not start", name)
			}
		})
	}
}

func TestReadinessReportsAFailingDependency(t *testing.T) {
	t.Parallel()

	failing := []httpx.Check{
		{Name: "postgres", Critical: true, Probe: func(context.Context) error {
			return errors.New("dial tcp 10.0.0.4:5432: connect: connection refused")
		}},
		{Name: "nats", Critical: false, Probe: func(context.Context) error { return nil }},
	}

	handler := httpx.Readiness(failing, time.Second, logging.NewJSON(discard{}, 0))
	recorder := newRecorder()
	request := mustRequest(t, http.MethodGet, "/readyz")
	handler(recorder, request)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", recorder.Code)
	}
	// The probe answers like every other error in this API, which means a
	// problem document and the matching content type.
	if got := recorder.Header().Get("Content-Type"); got != "application/problem+json" {
		t.Errorf("content type = %q, want application/problem+json", got)
	}
	if !contains(recorder.Body.String(), "SERVICE_UNAVAILABLE") {
		t.Errorf("the response does not carry the stable code: %s", recorder.Body.String())
	}
	if body := recorder.Body.String(); contains(body, "10.0.0.4") || contains(body, "dial tcp") {
		t.Errorf("readiness leaked the dependency error: %s", body)
	}
}

func TestReadinessPassesWhenEveryDependencyAnswers(t *testing.T) {
	t.Parallel()

	handler := httpx.Readiness([]httpx.Check{
		{Name: "postgres", Critical: true, Probe: func(context.Context) error { return nil }},
	}, time.Second, logging.NewJSON(discard{}, 0))

	recorder := newRecorder()
	handler(recorder, mustRequest(t, http.MethodGet, "/readyz"))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if !contains(recorder.Body.String(), `"postgres":"ok"`) {
		t.Errorf("the response does not report the check: %s", recorder.Body.String())
	}
}

// The api accepts orders while the broker is down, because the outbox decouples
// acceptance from publication. Reporting unready would make a load balancer
// remove the instance and cause the outage the design exists to prevent.
func TestReadinessStaysReadyWhenANonCriticalDependencyFails(t *testing.T) {
	t.Parallel()

	handler := httpx.Readiness([]httpx.Check{
		{Name: "postgres", Critical: true, Probe: func(context.Context) error { return nil }},
		{Name: "nats", Critical: false, Probe: func(context.Context) error {
			return errors.New("no servers available")
		}},
	}, time.Second, logging.NewJSON(discard{}, 0))

	recorder := newRecorder()
	handler(recorder, mustRequest(t, http.MethodGet, "/readyz"))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: a broker outage must not remove the instance from rotation", recorder.Code)
	}
	body := recorder.Body.String()
	if !contains(body, `"status":"degraded"`) {
		t.Errorf("the response does not report degradation: %s", body)
	}
	if !contains(body, `"nats":"degraded"`) {
		t.Errorf("the response does not name the affected dependency: %s", body)
	}
	if !contains(body, `"postgres":"ok"`) {
		t.Errorf("the response does not report the healthy dependency: %s", body)
	}
}

// Liveness must not depend on anything, because a liveness probe that fails
// during a database incident turns a degradation into a restart loop.
func TestLivenessIgnoresDependencies(t *testing.T) {
	t.Parallel()

	handler := httpx.Liveness(logging.NewJSON(discard{}, 0))
	recorder := newRecorder()
	handler(recorder, mustRequest(t, http.MethodGet, "/healthz"))

	if recorder.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", recorder.Code)
	}
}

func connectionCount(addr string) int {
	dialer := net.Dialer{Timeout: 200 * time.Millisecond}
	conn, err := dialer.DialContext(context.Background(), "tcp", addr)
	if err != nil {
		return 0
	}
	_ = conn.Close()
	return 1
}

func waitForListener(t *testing.T, addr string) {
	t.Helper()
	waitFor(t, func() bool { return connectionCount(addr) > 0 })
}

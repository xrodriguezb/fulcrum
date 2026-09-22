package profiling_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/xrodriguezb/fulcrum/internal/platform/logging"
	"github.com/xrodriguezb/fulcrum/internal/platform/profiling"
)

func testLogger() *slog.Logger { return logging.NewJSON(io.Discard, slog.LevelError) }

// Disabled is the default, and a disabled profiler must open nothing at all.
// A port that is listening while the configuration says it is not is the worst
// of both answers.
func TestDisabledProfilingOpensNoListener(t *testing.T) {
	t.Parallel()

	port := freePort(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- profiling.Serve(ctx, profiling.Config{Enabled: false, Port: port}, testLogger()) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve returned %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("a disabled profiler kept running")
	}

	dialer := net.Dialer{Timeout: 200 * time.Millisecond}
	if conn, err := dialer.DialContext(t.Context(), "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port))); err == nil {
		conn.Close()
		t.Errorf("something is listening on port %d while profiling is disabled", port)
	}
}

func TestEnabledProfilingServesTheRuntimeProfiles(t *testing.T) {
	t.Parallel()

	port := freePort(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- profiling.Serve(ctx, profiling.Config{Enabled: true, Port: port}, testLogger()) }()

	base := "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	waitForListener(t, base+"/debug/pprof/heap")

	for _, path := range []string{"/debug/pprof/heap", "/debug/pprof/goroutine", "/debug/pprof/allocs"} {
		response, err := get(t.Context(), base+path)
		if err != nil {
			t.Fatalf("GET %s returned %v", path, err)
		}
		if _, copyErr := io.Copy(io.Discard, response.Body); copyErr != nil {
			t.Errorf("reading %s returned %v", path, copyErr)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Errorf("GET %s answered %d, want 200", path, response.StatusCode)
		}
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Serve returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("the profiler did not shut down")
	}
}

// The profiles name goroutines, inline the binary's own symbols and let anyone
// who can reach them take a thirty second cpu sample of a production process.
// They belong on loopback and nowhere else.
func TestProfilingListensOnLoopbackOnly(t *testing.T) {
	t.Parallel()

	port := freePort(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	go func() { _ = profiling.Serve(ctx, profiling.Config{Enabled: true, Port: port}, testLogger()) }()
	waitForListener(t, "http://"+net.JoinHostPort("127.0.0.1", strconv.Itoa(port))+"/debug/pprof/heap")

	routable := routableAddress(t)
	dialer := net.Dialer{Timeout: 500 * time.Millisecond}
	conn, err := dialer.DialContext(t.Context(), "tcp", net.JoinHostPort(routable, strconv.Itoa(port)))
	if err == nil {
		conn.Close()
		t.Errorf("the profiler accepted a connection on %s, which is reachable from outside the host", routable)
	}
}

// get is a context carrying http.Get, which is what the linter asks for and
// what makes a hung profile endpoint fail the test rather than hang it.
func get(ctx context.Context, url string) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	return http.DefaultClient.Do(request)
}

func freePort(t *testing.T) int {
	t.Helper()

	var listenConfig net.ListenConfig
	listener, err := listenConfig.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("cannot reserve a port: %v", err)
	}
	defer listener.Close()

	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("the reserved address is not tcp")
	}
	return addr.Port
}

func waitForListener(t *testing.T, url string) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		response, err := get(t.Context(), url)
		if err == nil {
			_, _ = io.Copy(io.Discard, response.Body)
			response.Body.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("nothing answered on %s", url)
}

// routableAddress returns an address of this host that is not loopback, so the
// test can prove the profiler refuses it.
func routableAddress(t *testing.T) string {
	t.Helper()

	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatalf("cannot read the interface addresses: %v", err)
	}
	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok || ipNet.IP.IsLoopback() || ipNet.IP.To4() == nil {
			continue
		}
		return ipNet.IP.String()
	}
	t.Skip("this host has no routable ipv4 address to test against")
	return ""
}

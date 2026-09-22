package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The probe runs in a container health check, where its exit code is the whole
// interface. Nothing else observes it, so the codes are what the tests assert.
func TestProbeExitCodes(t *testing.T) {
	t.Parallel()

	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	// Cleanup rather than defer: a deferred close runs when this function
	// returns, which is before the parallel subtests below have executed, so the
	// server would be gone by the time they call it.
	t.Cleanup(healthy.Close)

	unhealthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(unhealthy.Close)

	cases := []struct {
		name string
		args []string
		want int
	}{
		{name: "healthy", args: []string{healthy.URL + "/readyz"}, want: 0},
		{name: "unhealthy", args: []string{unhealthy.URL + "/readyz"}, want: 1},
		{name: "unreachable", args: []string{"http://127.0.0.1:1/readyz"}, want: 1},
		{name: "no argument", args: nil, want: 2},
		{name: "two arguments", args: []string{"http://127.0.0.1:8080/a", "extra"}, want: 2},
		{name: "not a url", args: []string{"not-a-url"}, want: 2},
		{name: "wrong scheme", args: []string{"file:///etc/passwd"}, want: 2},
		// A health check that can be pointed at another host is a way to make a
		// container issue requests anywhere it can reach.
		{name: "another host", args: []string{"http://example.com/readyz"}, want: 2},
		{name: "metadata service", args: []string{"http://169.254.169.254/latest/meta-data/"}, want: 2},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := probe(tc.args); got != tc.want {
				t.Errorf("probe(%v) = %d, want %d", tc.args, got, tc.want)
			}
		})
	}
}

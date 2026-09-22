package httpx_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newRecorder is a thin alias so the intent reads the same in every test file.
func newRecorder() *httptest.ResponseRecorder { return httptest.NewRecorder() }

func mustRequest(t *testing.T, method, target string) *http.Request {
	t.Helper()
	return httptest.NewRequestWithContext(t.Context(), method, target, bytes.NewReader(nil))
}

func contains(haystack, needle string) bool { return strings.Contains(haystack, needle) }

// waitFor polls a condition rather than sleeping a fixed period, so a loaded
// machine does not turn a timing assumption into a flaky test.
func waitFor(t *testing.T, condition func() bool) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("condition was not met within the deadline")
}

package infra_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	inventoryapp "github.com/xrodriguezb/fulcrum/internal/inventory/app"
	"github.com/xrodriguezb/fulcrum/internal/ops/app"
	"github.com/xrodriguezb/fulcrum/internal/ops/infra"
	"github.com/xrodriguezb/fulcrum/internal/platform/errs"
	"github.com/xrodriguezb/fulcrum/internal/platform/logging"
)

type stubReader struct {
	mu       sync.Mutex
	snapshot app.Snapshot
	entries  []app.DeadLetter
	total    int
	err      error
	calls    int
}

func (s *stubReader) Snapshot(context.Context) (app.Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return s.snapshot, s.err
}

func (s *stubReader) DeadLetters(context.Context, int, int) ([]app.DeadLetter, int, error) {
	return s.entries, s.total, s.err
}

func (s *stubReader) setPending(pending int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshot.Outbox.Pending = pending
}

type stubInventory struct {
	items []inventoryapp.Item
	err   error
}

func (s *stubInventory) List(context.Context) ([]inventoryapp.Item, error) {
	return s.items, s.err
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

func newHandlers(reader app.Reader, inventory inventoryapp.Reader) *infra.Handlers {
	return infra.NewHandlers(reader, inventory, logging.NewJSON(discardWriter{}, 0))
}

func TestOutboxSnapshotIsServed(t *testing.T) {
	t.Parallel()

	reader := &stubReader{snapshot: app.Snapshot{
		Outbox:               app.OutboxHealth{Pending: 3, Failing: 1, Published: 40, OldestUnpublishedSec: 2.5},
		DeadLetters:          2,
		StuckIdempotencyKeys: 1,
		ObservedAt:           time.Now(),
	}}
	handlers := newHandlers(reader, &stubInventory{})

	recorder := httptest.NewRecorder()
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/ops/outbox", nil)
	handlers.Outbox(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("response is not json: %v", err)
	}
	outbox, _ := payload["outbox"].(map[string]any)
	if outbox["pending"] != float64(3) {
		t.Errorf("pending = %v, want 3", outbox["pending"])
	}
	if payload["stuck_idempotency_keys"] != float64(1) {
		t.Errorf("stuck keys = %v, want 1", payload["stuck_idempotency_keys"])
	}
}

func TestDeadLettersArePaginatedAndValidated(t *testing.T) {
	t.Parallel()

	reader := &stubReader{
		entries: []app.DeadLetter{{
			ID: "1", EventID: "2", ConsumerName: "order-projector", EventType: "order.created",
			Attempts: 5, FirstFailedAt: time.Now(), LastFailedAt: time.Now(),
			FailureReason: "the payload does not match the expected schema",
		}},
		total: 1,
	}
	handlers := newHandlers(reader, &stubInventory{})

	recorder := httptest.NewRecorder()
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/ops/dead-letters", nil)
	handlers.DeadLetters(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "order-projector") {
		t.Errorf("the entry is missing from the response: %s", recorder.Body.String())
	}

	bad := httptest.NewRecorder()
	request = httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/ops/dead-letters?limit=5000", nil)
	handlers.DeadLetters(bad, request)
	if bad.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for an out of range limit", bad.Code)
	}
}

func TestOperationalFailuresBecomeProblems(t *testing.T) {
	t.Parallel()

	reader := &stubReader{err: errs.Internal("read snapshot", nil)}
	handlers := newHandlers(reader, &stubInventory{})

	recorder := httptest.NewRecorder()
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/ops/outbox", nil)
	handlers.Outbox(recorder, request)

	if recorder.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", recorder.Code)
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/problem+json" {
		t.Errorf("content type = %q, want application/problem+json", got)
	}
}

func TestInventoryIsServed(t *testing.T) {
	t.Parallel()

	handlers := newHandlers(&stubReader{}, &stubInventory{items: []inventoryapp.Item{
		{SKU: "WIDGET-001", Available: 5, Reserved: 1, UnitPriceCents: 1050, Currency: "EUR", Version: 2},
	}})

	recorder := httptest.NewRecorder()
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/inventory", nil)
	handlers.Inventory(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), `"available":5`) {
		t.Errorf("availability missing from the response: %s", recorder.Body.String())
	}
}

// The stream has to send the current state immediately, send again when the
// state changes, and stop as soon as the client goes away.
func TestStreamSendsChangesAndStopsOnDisconnect(t *testing.T) {
	t.Parallel()

	reader := &stubReader{snapshot: app.Snapshot{Outbox: app.OutboxHealth{Pending: 1}}}
	handlers := newHandlers(reader, &stubInventory{})

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	// A ResponseRecorder is not safe to read while a handler writes to it, and
	// this handler runs for the length of the test, so the writer is one that
	// guards its buffer.
	recorder := newSyncRecorder()
	request := httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/v1/ops/stream", nil)

	done := make(chan struct{})
	go func() {
		defer close(done)
		handlers.Stream(recorder, request)
	}()

	waitFor(t, func() bool { return strings.Contains(recorder.body(), `"pending":1`) })

	reader.setPending(7)
	waitFor(t, func() bool { return strings.Contains(recorder.body(), `"pending":7`) })

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("the stream did not return after the client disconnected")
	}

	if got := recorder.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("content type = %q, want text/event-stream", got)
	}
}

// syncRecorder is a ResponseWriter whose buffer can be read while the handler is
// still writing to it.
type syncRecorder struct {
	mu      sync.Mutex
	buf     strings.Builder
	headers http.Header
	status  int
}

func newSyncRecorder() *syncRecorder {
	return &syncRecorder{headers: make(http.Header), status: http.StatusOK}
}

func (s *syncRecorder) Header() http.Header { return s.headers }

func (s *syncRecorder) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncRecorder) WriteHeader(status int) { s.status = status }

func (s *syncRecorder) Flush() {}

func (s *syncRecorder) body() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// waitFor polls a condition instead of sleeping for a fixed period, so the test
// is not slower than it has to be and not flaky when the machine is loaded.
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

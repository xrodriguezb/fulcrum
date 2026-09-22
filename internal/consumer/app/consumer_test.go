package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xrodriguezb/fulcrum/internal/consumer/app"
	outboxapp "github.com/xrodriguezb/fulcrum/internal/outbox/app"
	outboxdomain "github.com/xrodriguezb/fulcrum/internal/outbox/domain"
	"github.com/xrodriguezb/fulcrum/internal/platform/errs"
	"github.com/xrodriguezb/fulcrum/internal/platform/logging"
)

const consumerName = "order-projector"

func envelopeBytes(t *testing.T, id string) []byte {
	t.Helper()

	envelope := outboxdomain.Envelope{
		ID:            id,
		AggregateID:   "00000000-0000-4000-8000-000000000001",
		AggregateType: "order",
		EventType:     "order.created",
		EventVersion:  1,
		Payload:       json.RawMessage(`{"status":"pending"}`),
		CorrelationID: "corr-1",
		TraceID:       "4bf92f3577b34da6a3ce929d0e0e4736",
		OccurredAt:    time.Now().UTC(),
	}
	raw, err := envelope.MarshalWire()
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	return raw
}

// recordingAcker captures which of the three answers the consumer gave.
type recordingAcker struct {
	mu    sync.Mutex
	acks  int
	naks  int
	terms int
	delay time.Duration
	err   error
}

func (a *recordingAcker) Ack(context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.acks++
	return a.err
}

func (a *recordingAcker) Nak(_ context.Context, delay time.Duration) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.naks++
	a.delay = delay
	return a.err
}

func (a *recordingAcker) Term(context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.terms++
	return a.err
}

func (a *recordingAcker) counts() (int, int, int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.acks, a.naks, a.terms
}

// memoryDedup is the deduplication table, in memory.
type memoryDedup struct {
	mu   sync.Mutex
	seen map[string]bool
	err  error
}

func newDedup() *memoryDedup { return &memoryDedup{seen: make(map[string]bool)} }

func (d *memoryDedup) snapshot() map[string]bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make(map[string]bool, len(d.seen))
	for key, value := range d.seen {
		out[key] = value
	}
	return out
}

func (d *memoryDedup) restore(snapshot map[string]bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.seen = snapshot
}

func (d *memoryDedup) Claim(_ context.Context, consumer, eventID string) (bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.err != nil {
		return false, d.err
	}
	key := consumer + "|" + eventID
	if d.seen[key] {
		return false, nil
	}
	d.seen[key] = true
	return true, nil
}

type countingHandler struct {
	mu       sync.Mutex
	calls    int
	failures []error
}

func (h *countingHandler) Handle(context.Context, outboxdomain.Envelope) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls++
	if len(h.failures) > 0 {
		err := h.failures[0]
		h.failures = h.failures[1:]
		return err
	}
	return nil
}

func (h *countingHandler) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.calls
}

type memoryDeadLetters struct {
	mu      sync.Mutex
	entries []app.DeadLetterEntry
	err     error
}

func (d *memoryDeadLetters) Record(_ context.Context, entry app.DeadLetterEntry) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.err != nil {
		return d.err
	}
	d.entries = append(d.entries, entry)
	return nil
}

func (d *memoryDeadLetters) list() []app.DeadLetterEntry {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]app.DeadLetterEntry, len(d.entries))
	copy(out, d.entries)
	return out
}

// directTx runs the function and undoes the deduplication claim when it fails.
//
// Modelling the rollback matters: the whole design rests on the claim and the
// side effect sharing one transaction, so a fake that kept the claim after a
// failure would make a retry look like a duplicate and hide the bug this suite
// exists to catch. The real rollback is proven against PostgreSQL in the
// integration suite.
type directTx struct {
	dedup *memoryDedup
}

func (tx directTx) WithinTx(ctx context.Context, fn func(context.Context) error) error {
	var snapshot map[string]bool
	if tx.dedup != nil {
		snapshot = tx.dedup.snapshot()
	}

	if err := fn(ctx); err != nil {
		if tx.dedup != nil {
			tx.dedup.restore(snapshot)
		}
		return err
	}
	return nil
}

type fixedIDs struct {
	mu   sync.Mutex
	next int
}

func (f *fixedIDs) NewID() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	return "00000000-0000-4000-8000-00000000000" + string(rune('0'+f.next%10))
}

type emptySource struct{}

func (emptySource) Fetch(context.Context, int) ([]app.Message, error) { return nil, nil }

// asMemoryDedup lets the fake transaction manager undo a claim when the
// deduplicator under test is the in-memory one.
func asMemoryDedup(dedup app.Deduplicator) *memoryDedup {
	if typed, ok := dedup.(*memoryDedup); ok {
		return typed
	}
	return nil
}

func newConsumer(t *testing.T, handler app.Handler, dedup app.Deduplicator, dlq app.DeadLetters, maxAttempts int) *app.Consumer {
	t.Helper()

	consumer, err := app.New(app.Deps{
		Source:      emptySource{},
		Handler:     handler,
		Dedup:       dedup,
		DeadLetters: dlq,
		Tx:          directTx{dedup: asMemoryDedup(dedup)},
		IDs:         &fixedIDs{},
		Logger:      logging.NewJSON(io.Discard, slog.LevelError),
	}, app.Config{
		Name:        consumerName,
		MaxAttempts: maxAttempts,
		FetchBatch:  10,
		Backoff:     outboxapp.BackoffPolicy{Base: time.Millisecond, Cap: 10 * time.Millisecond},
	})
	if err != nil {
		t.Fatalf("build consumer: %v", err)
	}
	return consumer
}

// Duplicate delivery is normal traffic under at-least-once, so it must produce
// exactly one side effect and still be acknowledged.
func TestDuplicateDeliveryProducesOneSideEffect(t *testing.T) {
	t.Parallel()

	handler := &countingHandler{}
	dedup := newDedup()
	dlq := &memoryDeadLetters{}
	consumer := newConsumer(t, handler, dedup, dlq, 5)

	payload := envelopeBytes(t, "00000000-0000-4000-8000-000000000042")
	acker := &recordingAcker{}

	consumer.Process(t.Context(), app.Message{Payload: payload, Deliveries: 1, Ack: acker})
	consumer.Process(t.Context(), app.Message{Payload: payload, Deliveries: 2, Ack: acker})
	consumer.Process(t.Context(), app.Message{Payload: payload, Deliveries: 3, Ack: acker})

	if handler.count() != 1 {
		t.Errorf("the handler ran %d times, want exactly 1", handler.count())
	}
	acks, naks, terms := acker.counts()
	if acks != 3 {
		t.Errorf("acks = %d, want 3: every delivery must be answered", acks)
	}
	if naks != 0 || terms != 0 {
		t.Errorf("a duplicate must not be retried or terminated, got %d naks and %d terms", naks, terms)
	}
	if len(dlq.list()) != 0 {
		t.Errorf("a duplicate must not be dead lettered")
	}
}

// A permanent failure must not consume the retry budget: the same payload will
// fail the same way five times.
func TestPermanentFailureGoesStraightToTheDeadLetterQueue(t *testing.T) {
	t.Parallel()

	handler := &countingHandler{failures: []error{
		errs.Validation(errs.CodeEventPayloadInvalid, "The event data is not valid for this consumer.", nil),
	}}
	dlq := &memoryDeadLetters{}
	consumer := newConsumer(t, handler, newDedup(), dlq, 5)

	acker := &recordingAcker{}
	consumer.Process(t.Context(), app.Message{
		Payload:    envelopeBytes(t, "00000000-0000-4000-8000-000000000043"),
		Deliveries: 1,
		Ack:        acker,
	})

	entries := dlq.list()
	if len(entries) != 1 {
		t.Fatalf("dead letters = %d, want 1 on the first delivery", len(entries))
	}
	if entries[0].Attempts != 1 {
		t.Errorf("attempts = %d, want 1: a permanent failure must not exhaust the budget", entries[0].Attempts)
	}
	_, naks, terms := acker.counts()
	if naks != 0 {
		t.Errorf("a permanent failure must not be retried, got %d naks", naks)
	}
	if terms != 1 {
		t.Errorf("terms = %d, want 1", terms)
	}
}

// A transient failure has to be retried, with a delay, until it either succeeds
// or exhausts the budget.
func TestTransientFailureIsRetriedAndThenRecovers(t *testing.T) {
	t.Parallel()

	handler := &countingHandler{failures: []error{
		errs.Unavailable("the projection store is unreachable", nil),
	}}
	dlq := &memoryDeadLetters{}
	consumer := newConsumer(t, handler, newDedup(), dlq, 5)

	payload := envelopeBytes(t, "00000000-0000-4000-8000-000000000044")

	first := &recordingAcker{}
	consumer.Process(t.Context(), app.Message{Payload: payload, Deliveries: 1, Ack: first})

	_, naks, terms := first.counts()
	if naks != 1 {
		t.Errorf("naks = %d, want 1 for a transient failure", naks)
	}
	if terms != 0 {
		t.Errorf("a transient failure below the budget must not be terminated")
	}
	if len(dlq.list()) != 0 {
		t.Errorf("a transient failure below the budget must not be dead lettered")
	}

	// The redelivery succeeds. The deduplication row was rolled back with the
	// failed transaction, so the side effect runs this time.
	second := &recordingAcker{}
	consumer.Process(t.Context(), app.Message{Payload: payload, Deliveries: 2, Ack: second})

	acks, _, _ := second.counts()
	if acks != 1 {
		t.Errorf("acks = %d, want 1 after recovery", acks)
	}
	if handler.count() != 2 {
		t.Errorf("the handler ran %d times, want 2", handler.count())
	}
}

func TestTransientFailureIsDeadLetteredOnceTheBudgetIsSpent(t *testing.T) {
	t.Parallel()

	handler := &countingHandler{failures: []error{errs.Unavailable("still unreachable", nil)}}
	dlq := &memoryDeadLetters{}
	consumer := newConsumer(t, handler, newDedup(), dlq, 3)

	acker := &recordingAcker{}
	consumer.Process(t.Context(), app.Message{
		Payload:    envelopeBytes(t, "00000000-0000-4000-8000-000000000045"),
		Deliveries: 3,
		Ack:        acker,
	})

	entries := dlq.list()
	if len(entries) != 1 {
		t.Fatalf("dead letters = %d, want 1 once the budget is spent", len(entries))
	}
	if entries[0].Attempts != 3 {
		t.Errorf("attempts = %d, want 3", entries[0].Attempts)
	}
	if entries[0].CorrelationID != "corr-1" {
		t.Errorf("the entry lost the correlation id: %+v", entries[0])
	}
	if entries[0].TraceID == "" {
		t.Errorf("the entry lost the trace id")
	}
	_, _, terms := acker.counts()
	if terms != 1 {
		t.Errorf("terms = %d, want 1", terms)
	}
}

// The stored reason is read in a console. It must describe the failure without
// carrying the text of the error that caused it.
func TestDeadLetterReasonCarriesNoInfrastructureDetail(t *testing.T) {
	t.Parallel()

	cause := errors.New(`pq: relation "order_projections" does not exist (SQLSTATE 42P01) at /var/lib/postgresql/data`)
	handler := &countingHandler{failures: []error{errs.Internal("project the order", cause)}}
	dlq := &memoryDeadLetters{}
	consumer := newConsumer(t, handler, newDedup(), dlq, 3)

	consumer.Process(t.Context(), app.Message{
		Payload:    envelopeBytes(t, "00000000-0000-4000-8000-000000000046"),
		Deliveries: 1,
		Ack:        &recordingAcker{},
	})

	entries := dlq.list()
	if len(entries) != 1 {
		t.Fatalf("dead letters = %d, want 1", len(entries))
	}
	reason := entries[0].FailureReason
	for _, leak := range []string{"pq:", "SQLSTATE", "relation", "/var/lib", "order_projections"} {
		if strings.Contains(reason, leak) {
			t.Errorf("the stored reason leaked %q: %s", leak, reason)
		}
	}
	if reason == "" {
		t.Errorf("the stored reason is empty, which tells an operator nothing")
	}
	if !strings.Contains(reason, errs.CodeInternal) {
		t.Errorf("the reason should name the stable code, got %q", reason)
	}
}

// A payload that is not an envelope can never become one, so it is dead lettered
// on the first delivery with the raw bytes kept for diagnosis.
func TestUndecodablePayloadIsDeadLetteredImmediately(t *testing.T) {
	t.Parallel()

	handler := &countingHandler{}
	dlq := &memoryDeadLetters{}
	consumer := newConsumer(t, handler, newDedup(), dlq, 5)

	acker := &recordingAcker{}
	consumer.Process(t.Context(), app.Message{
		Payload:    []byte(`{"not":"an envelope"}`),
		Deliveries: 1,
		Ack:        acker,
	})

	entries := dlq.list()
	if len(entries) != 1 {
		t.Fatalf("dead letters = %d, want 1", len(entries))
	}
	if string(entries[0].Payload) != `{"not":"an envelope"}` {
		t.Errorf("the raw payload was not kept: %s", entries[0].Payload)
	}
	if handler.count() != 0 {
		t.Errorf("the handler must not run for an undecodable payload")
	}
	_, _, terms := acker.counts()
	if terms != 1 {
		t.Errorf("terms = %d, want 1", terms)
	}
}

// If the dead letter write fails, the message must not be terminated: leaving it
// for redelivery is the only way it is not lost silently.
func TestAFailedDeadLetterWriteLeavesTheMessageForRedelivery(t *testing.T) {
	t.Parallel()

	handler := &countingHandler{failures: []error{
		errs.Validation(errs.CodeEventPayloadInvalid, "not valid", nil),
	}}
	dlq := &memoryDeadLetters{err: errors.New("the database is unreachable")}
	consumer := newConsumer(t, handler, newDedup(), dlq, 3)

	acker := &recordingAcker{}
	consumer.Process(t.Context(), app.Message{
		Payload:    envelopeBytes(t, "00000000-0000-4000-8000-000000000047"),
		Deliveries: 1,
		Ack:        acker,
	})

	_, naks, terms := acker.counts()
	if terms != 0 {
		t.Errorf("the message was terminated although the dead letter write failed")
	}
	if naks != 1 {
		t.Errorf("naks = %d, want 1 so the message comes back", naks)
	}
}

func TestConsumerRejectsIncompleteWiring(t *testing.T) {
	t.Parallel()

	valid := app.Deps{
		Source: emptySource{}, Handler: &countingHandler{}, Dedup: newDedup(),
		DeadLetters: &memoryDeadLetters{}, Tx: directTx{}, IDs: &fixedIDs{},
		Logger: logging.NewJSON(io.Discard, slog.LevelError),
	}
	cfg := app.Config{Name: consumerName, MaxAttempts: 3, FetchBatch: 5}

	if _, err := app.New(app.Deps{}, cfg); err == nil {
		t.Errorf("a consumer without collaborators must not be built")
	}
	if _, err := app.New(valid, app.Config{Name: "", MaxAttempts: 3, FetchBatch: 5}); err == nil {
		t.Errorf("a consumer without a name must not be built")
	}
	if _, err := app.New(valid, app.Config{Name: consumerName, MaxAttempts: 0, FetchBatch: 5}); err == nil {
		t.Errorf("a consumer without an attempt budget must not be built")
	}
}

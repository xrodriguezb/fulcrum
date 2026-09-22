package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strconv"
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/xrodriguezb/fulcrum/internal/outbox/app"
	"github.com/xrodriguezb/fulcrum/internal/outbox/domain"
	"github.com/xrodriguezb/fulcrum/internal/platform/logging"
)

// TestMain fails the package when a goroutine outlives a test. The publisher
// starts a pool and a claimer on every run, so a missing wait or an unread
// channel would show up here rather than as a slow memory leak in production.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func testLogger() *slog.Logger { return logging.NewJSON(io.Discard, slog.LevelError) }

func envelope(n int) domain.Envelope {
	return domain.Envelope{
		ID:            "00000000-0000-4000-8000-" + pad(n),
		AggregateID:   "00000000-0000-4000-8000-000000000001",
		AggregateType: "order",
		EventType:     "order.created",
		EventVersion:  1,
		Payload:       json.RawMessage(`{"n":` + strconv.Itoa(n) + `}`),
		CorrelationID: "corr-1",
		OccurredAt:    time.Now(),
	}
}

func pad(n int) string {
	value := strconv.Itoa(n)
	for len(value) < 12 {
		value = "0" + value
	}
	return value
}

// fakeStore is a claimable outbox held in memory.
type fakeStore struct {
	mu        sync.Mutex
	pending   []app.Claimed
	published []string
	failures  []failure
	claimErr  error
	markErr   error
	claims    int
}

type failure struct {
	id          string
	reason      string
	nextAttempt time.Time
}

func (s *fakeStore) Claim(_ context.Context, batchSize int) ([]app.Claimed, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.claims++
	if s.claimErr != nil {
		return nil, s.claimErr
	}
	if len(s.pending) == 0 {
		return nil, nil
	}
	if batchSize > len(s.pending) {
		batchSize = len(s.pending)
	}
	batch := s.pending[:batchSize]
	s.pending = s.pending[batchSize:]
	return batch, nil
}

func (s *fakeStore) MarkPublished(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.markErr != nil {
		return s.markErr
	}
	s.published = append(s.published, id)
	return nil
}

func (s *fakeStore) MarkFailed(_ context.Context, id, reason string, nextAttemptAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures = append(s.failures, failure{id: id, reason: reason, nextAttempt: nextAttemptAt})
	return nil
}

func (s *fakeStore) publishedIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.published))
	copy(out, s.published)
	return out
}

func (s *fakeStore) failureList() []failure {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]failure, len(s.failures))
	copy(out, s.failures)
	return out
}

type fakeBroker struct {
	mu        sync.Mutex
	published []string
	err       error
	block     chan struct{}
	entered   chan struct{}
	once      sync.Once
}

func (b *fakeBroker) Publish(ctx context.Context, envelope domain.Envelope) error {
	if b.entered != nil {
		b.once.Do(func() { close(b.entered) })
	}
	if b.block != nil {
		select {
		case <-b.block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	if b.err != nil {
		return b.err
	}
	b.published = append(b.published, envelope.ID)
	return nil
}

func (b *fakeBroker) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.published)
}

func testConfig() app.PublisherConfig {
	return app.PublisherConfig{
		Workers:        3,
		BatchSize:      10,
		PollInterval:   5 * time.Millisecond,
		PublishTimeout: 2 * time.Second,
		DrainTimeout:   2 * time.Second,
		MaxAttempts:    5,
		Backoff:        app.BackoffPolicy{Base: time.Millisecond, Cap: 10 * time.Millisecond},
	}
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("condition was not met within the deadline")
}

func TestPublisherPublishesAndMarksEveryEvent(t *testing.T) {
	t.Parallel()

	store := &fakeStore{}
	for i := 1; i <= 25; i++ {
		store.pending = append(store.pending, app.Claimed{Envelope: envelope(i), Attempts: 1})
	}
	broker := &fakeBroker{}

	publisher, err := app.NewPublisher(store, broker, testConfig(), testLogger(), nil)
	if err != nil {
		t.Fatalf("NewPublisher returned %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- publisher.Run(ctx) }()

	waitFor(t, func() bool { return len(store.publishedIDs()) == 25 })
	cancel()

	select {
	case runErr := <-done:
		if runErr != nil {
			t.Errorf("Run returned %v", runErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("Run did not return after cancellation")
	}

	if broker.count() != 25 {
		t.Errorf("broker received %d events, want 25", broker.count())
	}
	if len(store.failureList()) != 0 {
		t.Errorf("no failure was expected, got %v", store.failureList())
	}
}

// A broker that is down must produce backoff and a recorded reason, and must
// never produce a published mark.
func TestBrokerFailureSchedulesARetryAndMarksNothingPublished(t *testing.T) {
	t.Parallel()

	store := &fakeStore{pending: []app.Claimed{{Envelope: envelope(1), Attempts: 2}}}
	broker := &fakeBroker{err: errors.New("no servers available for subject")}

	publisher, err := app.NewPublisher(store, broker, testConfig(), testLogger(), nil)
	if err != nil {
		t.Fatalf("NewPublisher returned %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- publisher.Run(ctx) }()

	waitFor(t, func() bool { return len(store.failureList()) == 1 })
	cancel()
	<-done

	if got := store.publishedIDs(); len(got) != 0 {
		t.Errorf("an event was marked published despite a broker failure: %v", got)
	}
	recorded := store.failureList()[0]
	if recorded.reason == "" {
		t.Errorf("the failure reason was not recorded")
	}
	if !recorded.nextAttempt.After(time.Now().Add(-time.Second)) {
		t.Errorf("the next attempt was scheduled in the past: %v", recorded.nextAttempt)
	}
}

// A database that refuses the claim must not kill the worker: the process
// staying up is what lets it recover when the database comes back.
func TestClaimFailureDoesNotStopThePublisher(t *testing.T) {
	t.Parallel()

	store := &fakeStore{claimErr: errors.New("connection refused")}
	broker := &fakeBroker{}

	publisher, err := app.NewPublisher(store, broker, testConfig(), testLogger(), nil)
	if err != nil {
		t.Fatalf("NewPublisher returned %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- publisher.Run(ctx) }()

	waitFor(t, func() bool {
		store.mu.Lock()
		defer store.mu.Unlock()
		return store.claims >= 2
	})

	store.mu.Lock()
	store.claimErr = nil
	store.pending = []app.Claimed{{Envelope: envelope(9), Attempts: 1}}
	store.mu.Unlock()

	waitFor(t, func() bool { return len(store.publishedIDs()) == 1 })
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("Run did not return after cancellation")
	}
}

// Cancelling while a publish is in flight must let that publish finish and be
// marked, and must still return inside the drain budget.
func TestShutdownDrainsAnInFlightPublish(t *testing.T) {
	t.Parallel()

	store := &fakeStore{pending: []app.Claimed{{Envelope: envelope(1), Attempts: 1}}}
	broker := &fakeBroker{block: make(chan struct{}), entered: make(chan struct{})}

	cfg := testConfig()
	cfg.Workers = 1
	publisher, err := app.NewPublisher(store, broker, cfg, testLogger(), nil)
	if err != nil {
		t.Fatalf("NewPublisher returned %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- publisher.Run(ctx) }()

	select {
	case <-broker.entered:
	case <-time.After(5 * time.Second):
		t.Fatalf("the publish never started")
	}

	cancel()
	close(broker.block)

	select {
	case runErr := <-done:
		if runErr != nil {
			t.Errorf("Run returned %v", runErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("Run did not return inside the drain budget")
	}

	if got := store.publishedIDs(); len(got) != 1 {
		t.Errorf("the in flight publish was not marked: %v", got)
	}
	if len(store.failureList()) != 0 {
		t.Errorf("a drained publish must not be recorded as a failure: %v", store.failureList())
	}
}

// Backpressure: with one worker blocked, the claimer must stop claiming once the
// bounded channel is full, rather than pulling the whole table into memory.
func TestClaimingStopsWhenPublishersAreSaturated(t *testing.T) {
	t.Parallel()

	store := &fakeStore{}
	for i := 1; i <= 200; i++ {
		store.pending = append(store.pending, app.Claimed{Envelope: envelope(i), Attempts: 1})
	}
	broker := &fakeBroker{block: make(chan struct{}), entered: make(chan struct{})}

	cfg := testConfig()
	cfg.Workers = 2
	cfg.BatchSize = 5
	publisher, err := app.NewPublisher(store, broker, cfg, testLogger(), nil)
	if err != nil {
		t.Fatalf("NewPublisher returned %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- publisher.Run(ctx) }()

	select {
	case <-broker.entered:
	case <-time.After(5 * time.Second):
		t.Fatalf("the publish never started")
	}

	// Give the claimer time to do whatever it is going to do while the workers
	// are stuck, then assert it did not empty the store.
	time.Sleep(100 * time.Millisecond)

	store.mu.Lock()
	remaining := len(store.pending)
	store.mu.Unlock()

	// Workers plus channel capacity plus one batch bounds what can be in flight.
	maxInFlight := cfg.Workers + cfg.Workers + cfg.BatchSize
	if remaining < 200-maxInFlight {
		t.Errorf("the claimer drained %d rows while publishers were blocked, which is more than the %d the pipeline can hold",
			200-remaining, maxInFlight)
	}

	cancel()
	close(broker.block)
	<-done
}

func TestPublisherRejectsIncompleteConfiguration(t *testing.T) {
	t.Parallel()

	store := &fakeStore{}
	broker := &fakeBroker{}

	cases := map[string]func(app.PublisherConfig) app.PublisherConfig{
		"no workers":      func(c app.PublisherConfig) app.PublisherConfig { c.Workers = 0; return c },
		"no batch size":   func(c app.PublisherConfig) app.PublisherConfig { c.BatchSize = 0; return c },
		"no poll":         func(c app.PublisherConfig) app.PublisherConfig { c.PollInterval = 0; return c },
		"no drain budget": func(c app.PublisherConfig) app.PublisherConfig { c.DrainTimeout = 0; return c },
		"no timeout":      func(c app.PublisherConfig) app.PublisherConfig { c.PublishTimeout = 0; return c },
	}

	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := app.NewPublisher(store, broker, mutate(testConfig()), testLogger(), nil); err == nil {
				t.Errorf("a publisher with %s must not be built", name)
			}
		})
	}

	if _, err := app.NewPublisher(nil, broker, testConfig(), testLogger(), nil); err == nil {
		t.Errorf("a publisher without a store must not be built")
	}
}

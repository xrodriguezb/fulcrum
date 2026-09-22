package app_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/xrodriguezb/fulcrum/internal/outbox/app"
	"github.com/xrodriguezb/fulcrum/internal/outbox/domain"
)

// The blocked broker in publisher_test.go proves the pipeline stops when the
// broker stops. A broker that is merely slow is the harder and far more common
// failure: it keeps accepting, so nothing looks broken, and a publisher without
// real backpressure answers by claiming faster than it can publish.
//
// slowBroker accepts every publish after a fixed delay and records the highest
// concurrency it ever saw, which is what makes the worker bound assertable
// rather than inferred from timing.
type slowBroker struct {
	latency time.Duration

	mu        sync.Mutex
	published []string
	inFlight  int
	peak      int
	started   int
}

func (b *slowBroker) Publish(ctx context.Context, envelope domain.Envelope) error {
	b.mu.Lock()
	b.inFlight++
	b.started++
	if b.inFlight > b.peak {
		b.peak = b.inFlight
	}
	b.mu.Unlock()

	defer func() {
		b.mu.Lock()
		b.inFlight--
		b.mu.Unlock()
	}()

	timer := time.NewTimer(b.latency)
	defer timer.Stop()

	select {
	case <-timer.C:
	case <-ctx.Done():
		return ctx.Err()
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	b.published = append(b.published, envelope.ID)
	return nil
}

func (b *slowBroker) stats() (started, peak int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.started, b.peak
}

func (b *slowBroker) publishedIDs() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, len(b.published))
	copy(out, b.published)
	return out
}

// A slow broker must not turn into an unbounded read of the outbox. The claimer
// may run ahead by at most the channel capacity plus one batch, and no more
// publishes may be in flight than there are workers.
func TestSlowBrokerKeepsTheClaimerBehindTheWorkers(t *testing.T) {
	t.Parallel()

	const total = 200
	store := &fakeStore{}
	for i := 1; i <= total; i++ {
		store.pending = append(store.pending, app.Claimed{Envelope: envelope(i), Attempts: 1})
	}
	broker := &slowBroker{latency: 30 * time.Millisecond}

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

	// Long enough for an unbounded claimer to have emptied the table many times
	// over: two hundred rows at a batch of five is forty claims, and a claim in
	// this store takes microseconds.
	time.Sleep(300 * time.Millisecond)

	store.mu.Lock()
	remaining := len(store.pending)
	store.mu.Unlock()

	started, peak := broker.stats()

	// Workers holding one each, the channel holding Workers more, and one batch
	// claimed but not yet handed over.
	inFlightBound := cfg.Workers + cfg.Workers + cfg.BatchSize
	if claimed := total - remaining; claimed > started+inFlightBound {
		t.Errorf("the claimer took %d rows while only %d publishes had started, which is more than the %d the pipeline can hold",
			claimed, started, inFlightBound)
	}
	if peak > cfg.Workers {
		t.Errorf("%d publishes ran at once with %d workers", peak, cfg.Workers)
	}

	cancel()
	<-done
}

// Slowness is not loss. Every event still reaches the broker once and is marked
// once, and nothing is recorded as a failure on the way.
func TestSlowBrokerPublishesEveryEventOnce(t *testing.T) {
	t.Parallel()

	const total = 40
	store := &fakeStore{}
	for i := 1; i <= total; i++ {
		store.pending = append(store.pending, app.Claimed{Envelope: envelope(i), Attempts: 1})
	}
	broker := &slowBroker{latency: 5 * time.Millisecond}

	cfg := testConfig()
	cfg.Workers = 4
	publisher, err := app.NewPublisher(store, broker, cfg, testLogger(), nil)
	if err != nil {
		t.Fatalf("NewPublisher returned %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- publisher.Run(ctx) }()

	waitFor(t, func() bool { return len(store.publishedIDs()) == total })
	cancel()
	<-done

	seen := make(map[string]int, total)
	for _, id := range broker.publishedIDs() {
		seen[id]++
	}
	if len(seen) != total {
		t.Errorf("the broker received %d distinct events, want %d", len(seen), total)
	}
	for id, times := range seen {
		if times != 1 {
			t.Errorf("event %s was published %d times", id, times)
		}
	}
	if failures := store.failureList(); len(failures) != 0 {
		t.Errorf("a slow broker must not produce failures: %v", failures)
	}
}

// Shutdown against a slow broker has to finish inside the drain budget, and the
// publishes that were in flight have to be marked rather than abandoned.
func TestSlowBrokerDrainsInsideItsBudget(t *testing.T) {
	t.Parallel()

	store := &fakeStore{}
	for i := 1; i <= 20; i++ {
		store.pending = append(store.pending, app.Claimed{Envelope: envelope(i), Attempts: 1})
	}
	broker := &slowBroker{latency: 150 * time.Millisecond}

	cfg := testConfig()
	cfg.Workers = 2
	cfg.BatchSize = 2
	cfg.DrainTimeout = time.Second
	publisher, err := app.NewPublisher(store, broker, cfg, testLogger(), nil)
	if err != nil {
		t.Fatalf("NewPublisher returned %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- publisher.Run(ctx) }()

	// Cancel while publishes are in flight rather than between them.
	waitFor(t, func() bool { started, _ := broker.stats(); return started >= cfg.Workers })
	started := time.Now()
	cancel()

	select {
	case runErr := <-done:
		if runErr != nil {
			t.Errorf("Run returned %v", runErr)
		}
	case <-time.After(cfg.DrainTimeout + 2*time.Second):
		t.Fatalf("Run did not return inside the drain budget")
	}

	if elapsed := time.Since(started); elapsed > cfg.DrainTimeout+time.Second {
		t.Errorf("draining took %s against a budget of %s", elapsed, cfg.DrainTimeout)
	}
	if published := len(store.publishedIDs()); published < cfg.Workers {
		t.Errorf("only %d of the in flight publishes were marked, want at least %d", published, cfg.Workers)
	}
	if failures := store.failureList(); len(failures) != 0 {
		t.Errorf("a drained publish must not be recorded as a failure: %v", failures)
	}
}

// A broker slower than the publish timeout is a broker that has effectively
// stopped. The event must be scheduled for another attempt, never dropped and
// never marked published.
func TestBrokerSlowerThanTheTimeoutSchedulesAnotherAttempt(t *testing.T) {
	t.Parallel()

	store := &fakeStore{pending: []app.Claimed{{Envelope: envelope(1), Attempts: 1}}}
	broker := &slowBroker{latency: time.Second}
	began := time.Now()

	cfg := testConfig()
	cfg.Workers = 1
	cfg.PublishTimeout = 20 * time.Millisecond
	publisher, err := app.NewPublisher(store, broker, cfg, testLogger(), nil)
	if err != nil {
		t.Fatalf("NewPublisher returned %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- publisher.Run(ctx) }()

	waitFor(t, func() bool { return len(store.failureList()) > 0 })
	cancel()
	<-done

	if published := store.publishedIDs(); len(published) != 0 {
		t.Errorf("an event the broker never acknowledged was marked published: %v", published)
	}

	failures := store.failureList()
	if got := failures[0].id; got != envelope(1).ID {
		t.Errorf("the failure was recorded against %s, want %s", got, envelope(1).ID)
	}
	// The schedule is asserted against the start of the test rather than against
	// now: the backoff is full jitter, so a delay drawn near zero is correct
	// behaviour, and asserting that the instant is still ahead of the clock
	// would be asserting that the draw was lucky.
	if failures[0].nextAttempt.Before(began) {
		t.Errorf("the next attempt was scheduled at %s, before the attempt itself", failures[0].nextAttempt)
	}
	if failures[0].reason == "" {
		t.Errorf("a failure an operator has to read was recorded without a reason")
	}
}

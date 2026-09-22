//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go/jetstream"

	outboxapp "github.com/xrodriguezb/fulcrum/internal/outbox/app"
	outboxdomain "github.com/xrodriguezb/fulcrum/internal/outbox/domain"
	outboxinfra "github.com/xrodriguezb/fulcrum/internal/outbox/infra"
	"github.com/xrodriguezb/fulcrum/internal/platform/config"
	"github.com/xrodriguezb/fulcrum/internal/platform/logging"
	"github.com/xrodriguezb/fulcrum/internal/platform/messaging"
	"github.com/xrodriguezb/fulcrum/internal/platform/postgres"
)

func seedOutbox(t *testing.T, pool *pgxpool.Pool, count int) []string {
	t.Helper()

	const insert = `
INSERT INTO outbox_events
  (id, aggregate_id, aggregate_type, event_type, event_version, payload,
   correlation_id, trace_id, occurred_at, next_attempt_at)
VALUES ($1, $2, 'order', 'order.created', 1, $3, $4, NULL, now(), now())`

	ids := make([]string, 0, count)
	for i := 1; i <= count; i++ {
		id := uuidFromCounter(100000 + i)
		payload := json.RawMessage(fmt.Sprintf(`{"n":%d}`, i))
		if _, err := pool.Exec(t.Context(), insert, id, uuidFromCounter(200000+i), []byte(payload),
			fmt.Sprintf("corr-%d", i)); err != nil {
			t.Fatalf("seed outbox: %v", err)
		}
		ids = append(ids, id)
	}
	return ids
}

func publishedCount(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(t.Context(),
		`SELECT count(*) FROM outbox_events WHERE published_at IS NOT NULL`).Scan(&count); err != nil {
		t.Fatalf("count published: %v", err)
	}
	return count
}

func publisherConfig() outboxapp.PublisherConfig {
	return outboxapp.PublisherConfig{
		Workers:        4,
		BatchSize:      10,
		PollInterval:   10 * time.Millisecond,
		PublishTimeout: 5 * time.Second,
		DrainTimeout:   5 * time.Second,
		MaxAttempts:    5,
		Backoff:        outboxapp.BackoffPolicy{Base: 5 * time.Millisecond, Cap: 50 * time.Millisecond},
	}
}

// countingBroker records what it received so the test can assert that no event
// reached the broker twice.
type countingBroker struct {
	mu    sync.Mutex
	seen  map[string]int
	delay time.Duration
	err   error
}

func newCountingBroker() *countingBroker {
	return &countingBroker{seen: make(map[string]int)}
}

func (b *countingBroker) Publish(ctx context.Context, envelope outboxdomain.Envelope) error {
	if b.delay > 0 {
		select {
		case <-time.After(b.delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	if b.err != nil {
		return b.err
	}
	b.seen[envelope.ID]++
	return nil
}

func (b *countingBroker) duplicates() []string {
	b.mu.Lock()
	defer b.mu.Unlock()

	var repeated []string
	for id, count := range b.seen {
		if count > 1 {
			repeated = append(repeated, id)
		}
	}
	return repeated
}

func (b *countingBroker) total() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.seen)
}

// Two publisher instances against one database must divide the work rather than
// duplicate it, and neither may leave events behind.
func TestTwoPublisherInstancesShareTheOutboxWithoutDuplicating(t *testing.T) {
	pool := newPool(t)
	const events = 120
	seedOutbox(t, pool, events)

	broker := newCountingBroker()
	broker.delay = time.Millisecond
	logger := logging.NewJSON(io.Discard, slog.LevelError)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	var running sync.WaitGroup
	for range 2 {
		manager := postgres.NewTxManager(pool)
		publisher, err := outboxapp.NewPublisher(
			outboxinfra.NewClaimer(manager, 30*time.Second), broker, publisherConfig(), logger, nil)
		if err != nil {
			t.Fatalf("build publisher: %v", err)
		}
		running.Add(1)
		go func() {
			defer running.Done()
			if runErr := publisher.Run(ctx); runErr != nil {
				t.Errorf("publisher returned %v", runErr)
			}
		}()
	}

	waitForCondition(t, func() bool { return publishedCount(t, pool) == events })
	cancel()
	running.Wait()

	if duplicates := broker.duplicates(); len(duplicates) > 0 {
		t.Errorf("%d events were published more than once, first %v", len(duplicates), duplicates[0])
	}
	if broker.total() != events {
		t.Errorf("the broker saw %d distinct events, want %d", broker.total(), events)
	}

	var unpublished int
	if err := pool.QueryRow(t.Context(),
		`SELECT count(*) FROM outbox_events WHERE published_at IS NULL`).Scan(&unpublished); err != nil {
		t.Fatalf("count unpublished: %v", err)
	}
	if unpublished != 0 {
		t.Errorf("%d events were starved", unpublished)
	}
}

// A row another transaction holds must be skipped, not waited on. That is the
// property that makes a second instance useful instead of harmful.
func TestClaimSkipsRowsLockedByAnotherTransaction(t *testing.T) {
	pool := newPool(t)
	ids := seedOutbox(t, pool, 3)

	holder, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = holder.Rollback(t.Context()) }()

	if _, err := holder.Exec(t.Context(),
		`SELECT id FROM outbox_events WHERE id = $1 FOR UPDATE`, ids[0]); err != nil {
		t.Fatalf("lock a row: %v", err)
	}

	claimer := outboxinfra.NewClaimer(postgres.NewTxManager(pool), 30*time.Second)
	claimCtx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()

	claimed, err := claimer.Claim(claimCtx, 10)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(claimed) != 2 {
		t.Fatalf("claimed %d events, want 2 with one row locked elsewhere", len(claimed))
	}
	for _, entry := range claimed {
		if entry.Envelope.ID == ids[0] {
			t.Errorf("the locked row was claimed")
		}
		if entry.Attempts != 1 {
			t.Errorf("attempts = %d, want 1 after the first claim", entry.Attempts)
		}
	}
}

// A broker that refuses must leave the row unpublished, with the attempt counted,
// the reason recorded and the next attempt in the future.
func TestBrokerFailureIsRecordedOnTheRow(t *testing.T) {
	pool := newPool(t)
	ids := seedOutbox(t, pool, 1)

	broker := newCountingBroker()
	broker.err = fmt.Errorf("no responders available")

	manager := postgres.NewTxManager(pool)
	publisher, err := outboxapp.NewPublisher(
		outboxinfra.NewClaimer(manager, 30*time.Second), broker, publisherConfig(),
		logging.NewJSON(io.Discard, slog.LevelError), nil)
	if err != nil {
		t.Fatalf("build publisher: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = publisher.Run(ctx)
	}()

	waitForCondition(t, func() bool {
		var attempts int
		if err := pool.QueryRow(t.Context(),
			`SELECT attempts FROM outbox_events WHERE id = $1`, ids[0]).Scan(&attempts); err != nil {
			return false
		}
		return attempts >= 1
	})
	cancel()
	<-done

	var (
		attempts    int
		lastError   *string
		publishedAt *time.Time
		nextAttempt time.Time
	)
	if err := pool.QueryRow(t.Context(),
		`SELECT attempts, last_error, published_at, next_attempt_at FROM outbox_events WHERE id = $1`,
		ids[0]).Scan(&attempts, &lastError, &publishedAt, &nextAttempt); err != nil {
		t.Fatalf("read the row: %v", err)
	}

	if publishedAt != nil {
		t.Errorf("the row was marked published despite the broker refusing it")
	}
	if attempts < 1 {
		t.Errorf("attempts = %d, want at least 1", attempts)
	}
	if lastError == nil || *lastError == "" {
		t.Errorf("last_error was not recorded")
	}
	if !nextAttempt.After(time.Now().Add(-time.Second)) {
		t.Errorf("next_attempt_at = %v, which is not in the future", nextAttempt)
	}
}

// Losing the database mid run must not kill the worker, and must not leave the
// claimed rows in a state that hides them from the next run.
func TestPublisherSurvivesTheDatabaseGoingAway(t *testing.T) {
	shared := newPool(t)
	seedOutbox(t, shared, 5)

	// A pool of its own, so closing it does not disturb the rest of the suite.
	ownPool, err := postgres.NewPool(t.Context(), config.PostgresConfig{
		URL:             containerDSN,
		MaxConns:        4,
		MinConns:        1,
		MaxConnLifetime: time.Minute,
		MaxConnIdleTime: time.Minute,
		ConnectTimeout:  5 * time.Second,
	})
	if err != nil {
		t.Fatalf("open a second pool: %v", err)
	}

	broker := newCountingBroker()
	broker.delay = 20 * time.Millisecond

	publisher, err := outboxapp.NewPublisher(
		outboxinfra.NewClaimer(postgres.NewTxManager(ownPool), 30*time.Second), broker, publisherConfig(),
		logging.NewJSON(io.Discard, slog.LevelError), nil)
	if err != nil {
		t.Fatalf("build publisher: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- publisher.Run(ctx) }()

	waitForCondition(t, func() bool { return broker.total() > 0 })
	ownPool.Close()

	// The publisher must still be running: give it time to hit the closed pool
	// repeatedly, then stop it and assert it exited cleanly.
	time.Sleep(200 * time.Millisecond)
	cancel()

	select {
	case runErr := <-done:
		if runErr != nil {
			t.Errorf("Run returned %v after the database went away", runErr)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("the publisher did not return after the database went away")
	}

	var stuck int
	if err := shared.QueryRow(t.Context(),
		`SELECT count(*) FROM outbox_events WHERE published_at IS NULL AND next_attempt_at > now() + interval '1 hour'`).
		Scan(&stuck); err != nil {
		t.Fatalf("count stuck rows: %v", err)
	}
	if stuck != 0 {
		t.Errorf("%d rows were scheduled beyond reach of the next run", stuck)
	}
}

// The real broker, with the real stream, publishing real envelopes that a
// consumer can read back.
func TestPublishingToJetStreamStoresTheEvent(t *testing.T) {
	pool := newPool(t)
	ids := seedOutbox(t, pool, 3)

	cfg := natsConfig(t)
	client, err := messaging.Connect(t.Context(), cfg)
	if err != nil {
		t.Fatalf("connect to nats: %v", err)
	}
	defer client.Close()

	publisher, err := outboxinfra.EnsureStream(t.Context(), client.Conn(), cfg)
	if err != nil {
		t.Fatalf("ensure the stream: %v", err)
	}

	manager := postgres.NewTxManager(pool)
	service, err := outboxapp.NewPublisher(
		outboxinfra.NewClaimer(manager, 30*time.Second), publisher, publisherConfig(),
		logging.NewJSON(io.Discard, slog.LevelError), nil)
	if err != nil {
		t.Fatalf("build publisher: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = service.Run(ctx)
	}()

	waitForCondition(t, func() bool { return publishedCount(t, pool) == len(ids) })
	cancel()
	<-done

	js, err := jetstream.New(client.Conn())
	if err != nil {
		t.Fatalf("jetstream context: %v", err)
	}
	stream, err := js.Stream(t.Context(), cfg.StreamName)
	if err != nil {
		t.Fatalf("read the stream: %v", err)
	}
	info, err := stream.Info(t.Context())
	if err != nil {
		t.Fatalf("stream info: %v", err)
	}
	if info.State.Msgs < uint64(len(ids)) {
		t.Errorf("the stream holds %d messages, want at least %d", info.State.Msgs, len(ids))
	}
}

func waitForCondition(t *testing.T, condition func() bool) {
	t.Helper()

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("condition was not met within the deadline")
}

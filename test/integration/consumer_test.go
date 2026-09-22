//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	consumerapp "github.com/xrodriguezb/fulcrum/internal/consumer/app"
	consumerinfra "github.com/xrodriguezb/fulcrum/internal/consumer/infra"
	orderapp "github.com/xrodriguezb/fulcrum/internal/order/app"
	orderinfra "github.com/xrodriguezb/fulcrum/internal/order/infra"
	outboxapp "github.com/xrodriguezb/fulcrum/internal/outbox/app"
	outboxdomain "github.com/xrodriguezb/fulcrum/internal/outbox/domain"
	outboxinfra "github.com/xrodriguezb/fulcrum/internal/outbox/infra"
	"github.com/xrodriguezb/fulcrum/internal/platform/config"
	"github.com/xrodriguezb/fulcrum/internal/platform/errs"
	"github.com/xrodriguezb/fulcrum/internal/platform/idgen"
	"github.com/xrodriguezb/fulcrum/internal/platform/logging"
	"github.com/xrodriguezb/fulcrum/internal/platform/messaging"
)

// pipeline is the whole write path plus the publisher and the consumer, wired
// against the real database and the real broker.
type pipeline struct {
	harness   handlerHarness
	consumer  *consumerapp.Consumer
	publisher *outboxapp.Publisher
	client    *messaging.Client
	subject   string
}

func newPipeline(t *testing.T, consumerName string, handler consumerapp.Handler) pipeline {
	t.Helper()

	h := newHandler(t)
	cfg := natsConfig(t)
	// A stream per test keeps one test's messages out of another's consumer.
	cfg.StreamName = "FULCRUM_" + strings.ToUpper(strings.ReplaceAll(consumerName, "-", "_"))
	cfg.SubjectPrefix = "fulcrum." + consumerName + ".events"

	client, err := messaging.Connect(t.Context(), cfg)
	if err != nil {
		t.Fatalf("connect to nats: %v", err)
	}
	t.Cleanup(client.Close)

	target, err := outboxinfra.EnsureStream(t.Context(), client.Conn(), cfg)
	if err != nil {
		t.Fatalf("ensure the stream: %v", err)
	}

	logger := logging.NewJSON(io.Discard, slog.LevelError)
	publisher, err := outboxapp.NewPublisher(
		outboxinfra.NewClaimer(h.manager, 30*time.Second), target, publisherConfig(), logger, nil)
	if err != nil {
		t.Fatalf("build publisher: %v", err)
	}

	consumerCfg := consumerConfig(consumerName)
	source, err := consumerinfra.NewSource(t.Context(), client.Conn(), cfg, consumerCfg)
	if err != nil {
		t.Fatalf("create the durable consumer: %v", err)
	}

	if handler == nil {
		confirm, confirmErr := orderapp.NewConfirmOrderHandler(orderinfra.NewRepository(h.manager), time.Now)
		if confirmErr != nil {
			t.Fatalf("wire the confirmation handler: %v", confirmErr)
		}
		handler = confirm
	}

	consumer, err := consumerapp.New(consumerapp.Deps{
		Source:      source,
		Handler:     handler,
		Dedup:       consumerinfra.NewDeduplicator(h.manager),
		DeadLetters: consumerinfra.NewDeadLetterStore(h.manager),
		Tx:          h.manager,
		IDs:         idgen.UUID{},
		Clock:       time.Now,
		Logger:      logger,
	}, consumerapp.Config{
		Name:        consumerCfg.Name,
		MaxAttempts: consumerCfg.MaxAttempts,
		FetchBatch:  consumerCfg.FetchBatch,
		Backoff:     outboxapp.BackoffPolicy{Base: 5 * time.Millisecond, Cap: 50 * time.Millisecond},
	})
	if err != nil {
		t.Fatalf("build consumer: %v", err)
	}

	return pipeline{harness: h, consumer: consumer, publisher: publisher, client: client, subject: cfg.SubjectPrefix}
}

func consumerConfig(name string) config.ConsumerConfig {
	return config.ConsumerConfig{
		Name:        name,
		MaxAttempts: 3,
		FetchBatch:  10,
		AckWait:     2 * time.Second,
		RetryBase:   5 * time.Millisecond,
		RetryCap:    50 * time.Millisecond,
	}
}

// run starts the publisher and the consumer and returns a stop function.
func (p pipeline) run(t *testing.T) func() {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{}, 2)

	go func() {
		defer func() { done <- struct{}{} }()
		_ = p.publisher.Run(ctx)
	}()
	go func() {
		defer func() { done <- struct{}{} }()
		_ = p.consumer.Run(ctx)
	}()

	return func() {
		cancel()
		for range 2 {
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Errorf("a pipeline component did not stop")
			}
		}
	}
}

func orderStatus(t *testing.T, p pipeline, orderID string) string {
	t.Helper()

	var status string
	if err := p.harness.pool.QueryRow(t.Context(),
		`SELECT status FROM orders WHERE id = $1`, orderID).Scan(&status); err != nil {
		t.Fatalf("read order status: %v", err)
	}
	return status
}

// The whole path: an order is created, its event is published, the consumer
// processes it, and the order reaches confirmed exactly once.
func TestOrderIsConfirmedByTheConsumer(t *testing.T) {
	p := newPipeline(t, "confirm-flow", nil)
	seedInventory(t, p.harness.pool, "WIDGET-001", 5, 1000)

	result, err := p.harness.handler.Handle(t.Context(), command("consume-1",
		orderapp.CommandLine{SKU: "WIDGET-001", Quantity: 1}))
	if err != nil {
		t.Fatalf("create order: %v", err)
	}

	stop := p.run(t)
	defer stop()

	waitForCondition(t, func() bool { return orderStatus(t, p, result.View.ID) == "confirmed" })

	var processed int
	if err := p.harness.pool.QueryRow(t.Context(),
		`SELECT count(*) FROM processed_events WHERE consumer_name = $1`, "confirm-flow").Scan(&processed); err != nil {
		t.Fatalf("count processed events: %v", err)
	}
	if processed != 1 {
		t.Errorf("processed events = %d, want 1", processed)
	}
	if got := countRows(t, p.harness.pool, "dead_letter_events"); got != 0 {
		t.Errorf("dead letters = %d, want 0", got)
	}
}

// The same event delivered twice must leave one deduplication row and one
// confirmed order, and must not be dead lettered.
func TestDuplicateDeliveryLeavesOneEffect(t *testing.T) {
	p := newPipeline(t, "dedup-flow", nil)
	seedInventory(t, p.harness.pool, "WIDGET-001", 5, 1000)

	result, err := p.harness.handler.Handle(t.Context(), command("consume-2",
		orderapp.CommandLine{SKU: "WIDGET-001", Quantity: 1}))
	if err != nil {
		t.Fatalf("create order: %v", err)
	}

	stop := p.run(t)
	waitForCondition(t, func() bool { return orderStatus(t, p, result.View.ID) == "confirmed" })
	stop()

	// Republish the same envelope by resetting the outbox row. The consumer sees
	// an event it has already processed, which is exactly what a redelivery is.
	if _, err := p.harness.pool.Exec(t.Context(),
		`UPDATE outbox_events SET published_at = NULL, next_attempt_at = now(), claimed_at = NULL`); err != nil {
		t.Fatalf("reset the outbox row: %v", err)
	}

	stop = p.run(t)
	defer stop()

	waitForCondition(t, func() bool {
		var published int
		if err := p.harness.pool.QueryRow(t.Context(),
			`SELECT count(*) FROM outbox_events WHERE published_at IS NOT NULL`).Scan(&published); err != nil {
			return false
		}
		return published == 1
	})

	// Give the consumer time to handle the redelivery before asserting.
	time.Sleep(500 * time.Millisecond)

	var processed int
	if err := p.harness.pool.QueryRow(t.Context(),
		`SELECT count(*) FROM processed_events WHERE consumer_name = $1`, "dedup-flow").Scan(&processed); err != nil {
		t.Fatalf("count processed events: %v", err)
	}
	if processed != 1 {
		t.Errorf("processed events = %d, want 1 after a redelivery", processed)
	}
	if got := countRows(t, p.harness.pool, "dead_letter_events"); got != 0 {
		t.Errorf("a redelivery must not be dead lettered, got %d entries", got)
	}
	if status := orderStatus(t, p, result.View.ID); status != "confirmed" {
		t.Errorf("status = %q, want confirmed", status)
	}
}

// A payload that is not an envelope is dead lettered on the first delivery, with
// context an operator can act on and nothing an operator should not see.
func TestPoisonMessageLandsInTheDeadLetterQueue(t *testing.T) {
	p := newPipeline(t, "poison-flow", nil)

	stop := p.run(t)
	defer stop()

	if err := p.client.Conn().Publish(p.subject+".order.created", []byte(`{"this":"is not an envelope"}`)); err != nil {
		t.Fatalf("publish a poison message: %v", err)
	}
	if err := p.client.Conn().Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	waitForCondition(t, func() bool { return countRows(t, p.harness.pool, "dead_letter_events") == 1 })

	var (
		reason   string
		attempts int
		payload  []byte
	)
	if err := p.harness.pool.QueryRow(t.Context(),
		`SELECT failure_reason, attempts, payload FROM dead_letter_events`).Scan(&reason, &attempts, &payload); err != nil {
		t.Fatalf("read the dead letter: %v", err)
	}

	if attempts != 1 {
		t.Errorf("attempts = %d, want 1: an undecodable payload must not consume the budget", attempts)
	}
	for _, leak := range []string{"pq:", "SQLSTATE", "goroutine", "/Users/", "json:"} {
		if strings.Contains(reason, leak) {
			t.Errorf("the stored reason leaked %q: %s", leak, reason)
		}
	}
	if !strings.Contains(payloadText(t, payload), "is not an envelope") {
		t.Errorf("the raw payload was not kept for diagnosis: %s", payload)
	}
}

// A transient failure is retried and, once the budget is spent, dead lettered
// with the operator context intact.
func TestTransientFailureExhaustsTheBudgetAndDeadLetters(t *testing.T) {
	failing := &alwaysTransient{}
	p := newPipeline(t, "retry-flow", failing)
	seedInventory(t, p.harness.pool, "WIDGET-001", 5, 1000)

	if _, err := p.harness.handler.Handle(t.Context(), command("consume-3",
		orderapp.CommandLine{SKU: "WIDGET-001", Quantity: 1})); err != nil {
		t.Fatalf("create order: %v", err)
	}

	stop := p.run(t)
	defer stop()

	waitForCondition(t, func() bool { return countRows(t, p.harness.pool, "dead_letter_events") == 1 })

	var (
		attempts      int
		correlationID *string
		eventType     string
	)
	if err := p.harness.pool.QueryRow(t.Context(),
		`SELECT attempts, correlation_id, event_type FROM dead_letter_events`).
		Scan(&attempts, &correlationID, &eventType); err != nil {
		t.Fatalf("read the dead letter: %v", err)
	}

	if attempts < 3 {
		t.Errorf("attempts = %d, want at least the configured budget of 3", attempts)
	}
	if correlationID == nil || *correlationID == "" {
		t.Errorf("the entry lost the correlation id")
	}
	if eventType != "order.created" {
		t.Errorf("event type = %q, want order.created", eventType)
	}
	if failing.calls() < 3 {
		t.Errorf("the handler ran %d times, want at least 3 retries", failing.calls())
	}
}

func payloadText(t *testing.T, payload []byte) string {
	t.Helper()

	var wrapped map[string]any
	if err := json.Unmarshal(payload, &wrapped); err == nil {
		if raw, ok := wrapped["raw"].(string); ok {
			return raw
		}
	}
	return string(payload)
}

// alwaysTransient fails every delivery with a retryable error.
type alwaysTransient struct {
	mu    sync.Mutex
	count int
}

func (a *alwaysTransient) Handle(context.Context, outboxdomain.Envelope) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.count++
	return errs.Unavailable("the projection store is unreachable", nil)
}

func (a *alwaysTransient) calls() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.count
}

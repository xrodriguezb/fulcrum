package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/xrodriguezb/fulcrum/internal/outbox/domain"
	"github.com/xrodriguezb/fulcrum/internal/platform/errs"
	"github.com/xrodriguezb/fulcrum/internal/platform/logging"
)

// Claimed is one event taken from the outbox, with the attempt count the claim
// just incremented.
type Claimed struct {
	Envelope domain.Envelope
	Attempts int
}

// Store is the outbox as the publisher sees it.
type Store interface {
	// Claim takes up to batchSize due events and marks them claimed. Two
	// publishers claiming at once must never receive the same event.
	Claim(ctx context.Context, batchSize int) ([]Claimed, error)
	// MarkPublished records a successful publication.
	MarkPublished(ctx context.Context, id string) error
	// MarkFailed records a failure and schedules the next attempt.
	MarkFailed(ctx context.Context, id, reason string, nextAttemptAt time.Time) error
}

// Broker publishes an envelope. The implementation owns durability.
type Broker interface {
	Publish(ctx context.Context, envelope domain.Envelope) error
}

// PublisherMetrics records what the publisher is doing. It is an interface so
// the orchestration has no dependency on a metrics library.
type PublisherMetrics interface {
	PublishSucceeded()
	PublishFailed(permanent bool)
	ObserveBatch(size int)
}

// NoopMetrics satisfies PublisherMetrics without recording anything.
type NoopMetrics struct{}

// PublishSucceeded does nothing.
func (NoopMetrics) PublishSucceeded() {}

// PublishFailed does nothing.
func (NoopMetrics) PublishFailed(bool) {}

// ObserveBatch does nothing.
func (NoopMetrics) ObserveBatch(int) {}

// PublisherConfig is the publisher's operating envelope.
type PublisherConfig struct {
	Workers        int
	BatchSize      int
	PollInterval   time.Duration
	PublishTimeout time.Duration
	DrainTimeout   time.Duration
	MaxAttempts    int
	Backoff        BackoffPolicy
}

// Publisher moves events from the outbox to the broker.
//
// The shape is a claimer feeding a fixed pool of publishers through a bounded
// channel. The bound is what makes backpressure real: when the broker is slow,
// the send blocks, the claimer stops claiming, and rows stay in the database
// where they are durable, instead of accumulating in memory where a restart
// loses them.
type Publisher struct {
	store   Store
	broker  Broker
	cfg     PublisherConfig
	logger  *slog.Logger
	metrics PublisherMetrics
}

// NewPublisher validates the wiring. A publisher with a zero worker count or no
// drain budget would look like it works and quietly do nothing.
func NewPublisher(store Store, broker Broker, cfg PublisherConfig, logger *slog.Logger, metrics PublisherMetrics) (*Publisher, error) {
	switch {
	case store == nil:
		return nil, errors.New("the publisher needs an outbox store")
	case broker == nil:
		return nil, errors.New("the publisher needs a broker")
	case logger == nil:
		return nil, errors.New("the publisher needs a logger")
	case cfg.Workers <= 0:
		return nil, errors.New("the publisher needs at least one worker")
	case cfg.BatchSize <= 0:
		return nil, errors.New("the publisher needs a positive batch size")
	case cfg.PollInterval <= 0:
		return nil, errors.New("the publisher needs a positive poll interval")
	case cfg.DrainTimeout <= 0:
		return nil, errors.New("the publisher needs a drain timeout")
	case cfg.PublishTimeout <= 0:
		return nil, errors.New("the publisher needs a publish timeout")
	}
	if metrics == nil {
		metrics = NoopMetrics{}
	}
	return &Publisher{store: store, broker: broker, cfg: cfg, logger: logger, metrics: metrics}, nil
}

// Run claims and publishes until the context is cancelled, then drains.
//
// On cancellation the claimer stops immediately and the workers are given
// DrainTimeout to finish what they already hold. An event that cannot be
// finished in that budget is simply left unpublished: it is still in the
// database, its claim has no hold on it, and the next run will take it again.
// Nothing is lost and nothing is marked twice.
func (p *Publisher) Run(ctx context.Context) error {
	work := make(chan Claimed, p.cfg.Workers)

	var workers sync.WaitGroup
	for i := 0; i < p.cfg.Workers; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			p.work(ctx, work)
		}()
	}

	claimErr := p.claimLoop(ctx, work)
	close(work)

	drained := make(chan struct{})
	go func() {
		workers.Wait()
		close(drained)
	}()

	drainTimer := time.NewTimer(p.cfg.DrainTimeout)
	defer drainTimer.Stop()

	select {
	case <-drained:
		p.logger.InfoContext(context.WithoutCancel(ctx), "publisher drained")
	case <-drainTimer.C:
		// The budget is spent. In-flight publishes keep running on their own
		// contexts, and whatever they did not mark stays claimable.
		p.logger.WarnContext(context.WithoutCancel(ctx), "publisher drain budget expired",
			slog.Duration("budget", p.cfg.DrainTimeout))
	}
	return claimErr
}

// claimLoop is the only goroutine that talks to the claim statement, so the
// batch size is also the upper bound on rows claimed and not yet handled.
func (p *Publisher) claimLoop(ctx context.Context, work chan<- Claimed) error {
	ticker := time.NewTicker(p.cfg.PollInterval)
	defer ticker.Stop()

	for {
		batch, err := p.store.Claim(ctx, p.cfg.BatchSize)
		switch {
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			return nil
		case err != nil:
			// A failing database is a reason to wait, not to exit: the worker
			// process staying up is what lets it recover on its own.
			p.logger.ErrorContext(ctx, "cannot claim outbox events", slog.String("error", err.Error()))
			if !sleep(ctx, p.cfg.PollInterval) {
				return nil
			}
			continue
		}

		p.metrics.ObserveBatch(len(batch))

		for _, claimed := range batch {
			select {
			case work <- claimed:
			case <-ctx.Done():
				// The event was claimed and never handed to a worker. It stays
				// unpublished in the database and the next run claims it again.
				return nil
			}
		}

		if len(batch) == 0 {
			select {
			case <-ctx.Done():
				return nil
			case <-ticker.C:
			}
			continue
		}

		// A full batch means there is probably more waiting, so the next claim
		// happens immediately rather than after the poll interval.
		if len(batch) < p.cfg.BatchSize {
			select {
			case <-ctx.Done():
				return nil
			case <-ticker.C:
			}
		}
		if ctx.Err() != nil {
			return nil
		}
	}
}

func (p *Publisher) work(ctx context.Context, work <-chan Claimed) {
	for claimed := range work {
		p.publish(ctx, claimed)
	}
}

// publish sends one event and records the outcome.
func (p *Publisher) publish(ctx context.Context, claimed Claimed) {
	// The event's identifiers become the log context, so a publish line can be
	// joined to the request that produced the event and to the consumer that
	// processed it.
	ctx = logging.WithCorrelationID(ctx, claimed.Envelope.CorrelationID)
	if claimed.Envelope.TraceID != "" {
		ctx = logging.WithTraceID(ctx, claimed.Envelope.TraceID)
	}

	// The publish itself is bounded but does not inherit cancellation: a
	// shutdown must not abort a publish that is already in flight, because the
	// broker may have accepted it and the row would then be marked failed.
	publishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), p.cfg.PublishTimeout)
	defer cancel()

	if err := p.broker.Publish(publishCtx, claimed.Envelope); err != nil {
		p.recordFailure(ctx, claimed, err)
		return
	}

	// Marking also runs on a context cancellation cannot reach. An event that
	// was published and not marked would be published again, which the consumer
	// tolerates, but doing it on every shutdown is avoidable noise.
	markCtx, markCancel := context.WithTimeout(context.WithoutCancel(ctx), p.cfg.PublishTimeout)
	defer markCancel()

	if err := p.store.MarkPublished(markCtx, claimed.Envelope.ID); err != nil {
		p.logger.ErrorContext(markCtx, "published event could not be marked",
			slog.String("event_id", claimed.Envelope.ID),
			slog.String("error", err.Error()))
		p.metrics.PublishFailed(false)
		return
	}
	p.metrics.PublishSucceeded()
	p.logger.InfoContext(markCtx, "event published",
		slog.String("event_id", claimed.Envelope.ID),
		slog.String("event_type", claimed.Envelope.EventType),
		slog.String("aggregate_id", claimed.Envelope.AggregateID))
}

func (p *Publisher) recordFailure(ctx context.Context, claimed Claimed, cause error) {
	delay := p.cfg.Backoff.Delay(claimed.Attempts)
	nextAttempt := time.Now().Add(delay)

	exhausted := p.cfg.MaxAttempts > 0 && claimed.Attempts >= p.cfg.MaxAttempts
	if exhausted {
		// The event is not dropped. It keeps its place in the outbox with the
		// capped delay, and the operations console shows it as failing, because
		// silently discarding a business event is worse than retrying forever.
		nextAttempt = time.Now().Add(p.cfg.Backoff.Window(claimed.Attempts))
		p.logger.ErrorContext(ctx, "outbox event has exhausted its attempt budget",
			slog.String("event_id", claimed.Envelope.ID),
			slog.Int("attempts", claimed.Attempts),
			slog.String("error", cause.Error()))
	} else {
		p.logger.WarnContext(ctx, "publish failed, scheduling a retry",
			slog.String("event_id", claimed.Envelope.ID),
			slog.Int("attempts", claimed.Attempts),
			slog.Duration("delay", delay),
			slog.String("error", cause.Error()))
	}
	p.metrics.PublishFailed(exhausted)

	markCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), p.cfg.PublishTimeout)
	defer cancel()

	// The stored reason is for an operator. It keeps the cause, since the outbox
	// is not exposed to clients, but it is bounded so one broken event cannot
	// fill the table with megabytes of driver text.
	reason := truncate(fmt.Sprintf("%s: %v", errs.CodeOf(cause), cause), 500)
	if err := p.store.MarkFailed(markCtx, claimed.Envelope.ID, reason, nextAttempt); err != nil {
		p.logger.ErrorContext(markCtx, "failed publish could not be recorded",
			slog.String("event_id", claimed.Envelope.ID),
			slog.String("error", err.Error()))
	}
}

// sleep waits for the given duration and reports whether it completed rather
// than being cancelled. A bare time.Sleep in a loop is how a worker ends up
// ignoring a shutdown signal for its whole interval.
func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}

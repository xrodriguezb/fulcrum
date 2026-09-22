// Package app holds the consumer: the loop that reads events, the deduplication
// that makes at-least-once delivery produce one effect, and the policy that
// decides between retrying and dead lettering.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	outbox "github.com/xrodriguezb/fulcrum/internal/outbox/app"
	outboxdomain "github.com/xrodriguezb/fulcrum/internal/outbox/domain"
	"github.com/xrodriguezb/fulcrum/internal/platform/errs"
)

// Message is one delivery from the broker, still undecoded.
//
// The payload is raw on purpose: decoding is a step that can fail, and a payload
// that cannot be decoded is a permanent failure the consumer has to handle
// rather than a message the broker should keep redelivering.
type Message struct {
	Payload    []byte
	Deliveries int
	Ack        Acker
}

// Acker is how the consumer tells the broker what happened.
type Acker interface {
	// Ack accepts the message: it will not be redelivered.
	Ack(ctx context.Context) error
	// Nak asks for redelivery after a delay.
	Nak(ctx context.Context, delay time.Duration) error
	// Term rejects the message permanently, without redelivery.
	Term(ctx context.Context) error
}

// Source pulls messages from the broker.
type Source interface {
	Fetch(ctx context.Context, batch int) ([]Message, error)
}

// Handler performs the business side effect of an event. It runs inside the
// transaction that also records the deduplication row, so its work and the
// record of that work commit together.
type Handler interface {
	Handle(ctx context.Context, envelope outboxdomain.Envelope) error
}

// Deduplicator records which events this consumer has already processed.
type Deduplicator interface {
	// Claim inserts the deduplication row and reports whether this delivery is
	// the first. It must run inside the caller's transaction.
	Claim(ctx context.Context, consumerName, eventID string) (bool, error)
}

// DeadLetters stores events that will not be retried again.
type DeadLetters interface {
	Record(ctx context.Context, entry DeadLetterEntry) error
}

// DeadLetterEntry is what an operator needs in order to act.
type DeadLetterEntry struct {
	ID            string
	EventID       string
	ConsumerName  string
	EventType     string
	Payload       []byte
	Attempts      int
	FirstFailedAt time.Time
	LastFailedAt  time.Time
	FailureReason string
	CorrelationID string
	TraceID       string
}

// TxManager runs a function inside a database transaction.
type TxManager interface {
	WithinTx(ctx context.Context, fn func(ctx context.Context) error) error
}

// IDGenerator produces dead letter identifiers.
type IDGenerator interface {
	NewID() string
}

// Metrics records consumer outcomes.
type Metrics interface {
	Processed(eventType string)
	Failed(eventType, reasonClass string)
	DeadLettered(eventType string)
}

// NoopMetrics satisfies Metrics without recording anything.
type NoopMetrics struct{}

// Processed does nothing.
func (NoopMetrics) Processed(string) {}

// Failed does nothing.
func (NoopMetrics) Failed(string, string) {}

// DeadLettered does nothing.
func (NoopMetrics) DeadLettered(string) {}

// Config is the consumer's operating envelope.
type Config struct {
	Name         string
	MaxAttempts  int
	FetchBatch   int
	Backoff      outbox.BackoffPolicy
	DrainTimeout time.Duration
}

// Consumer processes events exactly once in effect.
type Consumer struct {
	source      Source
	handler     Handler
	dedup       Deduplicator
	deadLetters DeadLetters
	tx          TxManager
	ids         IDGenerator
	clock       func() time.Time
	cfg         Config
	logger      *slog.Logger
	metrics     Metrics
}

// Deps are the consumer's collaborators.
type Deps struct {
	Source      Source
	Handler     Handler
	Dedup       Deduplicator
	DeadLetters DeadLetters
	Tx          TxManager
	IDs         IDGenerator
	Clock       func() time.Time
	Logger      *slog.Logger
	Metrics     Metrics
}

// New validates the wiring.
func New(deps Deps, cfg Config) (*Consumer, error) {
	switch {
	case deps.Source == nil:
		return nil, errors.New("the consumer needs a message source")
	case deps.Handler == nil:
		return nil, errors.New("the consumer needs a handler")
	case deps.Dedup == nil:
		return nil, errors.New("the consumer needs a deduplicator")
	case deps.DeadLetters == nil:
		return nil, errors.New("the consumer needs a dead letter store")
	case deps.Tx == nil:
		return nil, errors.New("the consumer needs a transaction manager")
	case deps.IDs == nil:
		return nil, errors.New("the consumer needs an id generator")
	case deps.Logger == nil:
		return nil, errors.New("the consumer needs a logger")
	case cfg.Name == "":
		return nil, errors.New("the consumer needs a name")
	case cfg.MaxAttempts <= 0:
		return nil, errors.New("the consumer needs a positive attempt budget")
	case cfg.FetchBatch <= 0:
		return nil, errors.New("the consumer needs a positive fetch batch")
	}

	clock := deps.Clock
	if clock == nil {
		clock = time.Now
	}
	metrics := deps.Metrics
	if metrics == nil {
		metrics = NoopMetrics{}
	}

	return &Consumer{
		source:      deps.Source,
		handler:     deps.Handler,
		dedup:       deps.Dedup,
		deadLetters: deps.DeadLetters,
		tx:          deps.Tx,
		ids:         deps.IDs,
		clock:       clock,
		cfg:         cfg,
		logger:      deps.Logger,
		metrics:     metrics,
	}, nil
}

// Run pulls and processes until the context is cancelled.
func (c *Consumer) Run(ctx context.Context) error {
	for {
		if ctx.Err() != nil {
			return nil
		}

		messages, err := c.source.Fetch(ctx, c.cfg.FetchBatch)
		switch {
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			return nil
		case err != nil:
			// A broker that cannot be read is a reason to wait, not to exit.
			c.logger.ErrorContext(ctx, "cannot fetch events", slog.String("error", err.Error()))
			if !wait(ctx, time.Second) {
				return nil
			}
			continue
		}

		for _, message := range messages {
			c.Process(ctx, message)
		}
	}
}

// Process handles one delivery and tells the broker what happened.
//
// Every path ends in exactly one of ack, nak or term. A message that is left
// unanswered is redelivered after the acknowledgement wait, which looks like a
// hang rather than a failure.
func (c *Consumer) Process(ctx context.Context, message Message) {
	envelope, err := outboxdomain.UnmarshalWire(message.Payload)
	if err != nil {
		// A payload the consumer cannot read will never become readable. It goes
		// to the dead letter queue on the first delivery rather than after five.
		c.deadLetter(ctx, deadLetterInput{
			eventID:      "",
			eventType:    "unknown",
			payload:      message.Payload,
			attempts:     message.Deliveries,
			reason:       "The event payload could not be decoded as an event envelope.",
			terminateErr: err,
		}, message.Ack)
		return
	}

	processErr := c.tx.WithinTx(ctx, func(txCtx context.Context) error {
		first, claimErr := c.dedup.Claim(txCtx, c.cfg.Name, envelope.ID)
		if claimErr != nil {
			return claimErr
		}
		if !first {
			// The event was processed before. The side effect is skipped and the
			// delivery is accepted: that is what makes at-least-once delivery
			// safe rather than merely tolerable.
			c.logger.InfoContext(txCtx, "duplicate delivery ignored",
				slog.String("event_id", envelope.ID),
				slog.String("event_type", envelope.EventType))
			return nil
		}
		return c.handler.Handle(txCtx, envelope)
	})

	if processErr == nil {
		c.metrics.Processed(envelope.EventType)
		c.acknowledge(ctx, message.Ack, envelope)
		return
	}

	transient := errs.IsTransient(processErr)
	c.metrics.Failed(envelope.EventType, errs.KindOf(processErr).String())

	exhausted := message.Deliveries >= c.cfg.MaxAttempts
	if !transient || exhausted {
		reason := describeFailure(processErr, transient, exhausted)
		c.deadLetter(ctx, deadLetterInput{
			eventID:       envelope.ID,
			eventType:     envelope.EventType,
			payload:       message.Payload,
			attempts:      message.Deliveries,
			reason:        reason,
			correlationID: envelope.CorrelationID,
			traceID:       envelope.TraceID,
			terminateErr:  processErr,
		}, message.Ack)
		return
	}

	delay := c.cfg.Backoff.Delay(message.Deliveries)
	c.logger.WarnContext(ctx, "transient failure, asking for redelivery",
		slog.String("event_id", envelope.ID),
		slog.Int("deliveries", message.Deliveries),
		slog.Duration("delay", delay),
		slog.String("error", processErr.Error()))

	nakCtx, cancelNak := ackContext(ctx)
	defer cancelNak()
	if nakErr := message.Ack.Nak(nakCtx, delay); nakErr != nil {
		c.logger.ErrorContext(ctx, "cannot request redelivery",
			slog.String("event_id", envelope.ID),
			slog.String("error", nakErr.Error()))
	}
}

func (c *Consumer) acknowledge(ctx context.Context, acker Acker, envelope outboxdomain.Envelope) {
	ackCtx, cancel := ackContext(ctx)
	defer cancel()

	if err := acker.Ack(ackCtx); err != nil {
		// The effect is committed. A failed acknowledgement means the broker
		// will redeliver, which the deduplication row already handles.
		c.logger.WarnContext(ctx, "cannot acknowledge a processed event",
			slog.String("event_id", envelope.ID),
			slog.String("error", err.Error()))
	}
}

type deadLetterInput struct {
	eventID       string
	eventType     string
	payload       []byte
	attempts      int
	reason        string
	correlationID string
	traceID       string
	terminateErr  error
}

func (c *Consumer) deadLetter(ctx context.Context, input deadLetterInput, acker Acker) {
	now := c.clock()
	eventID := input.eventID
	if eventID == "" {
		// A payload that could not be decoded has no event id of its own, so the
		// entry carries a generated one and the raw payload for diagnosis.
		eventID = c.ids.NewID()
	}

	entry := DeadLetterEntry{
		ID:            c.ids.NewID(),
		EventID:       eventID,
		ConsumerName:  c.cfg.Name,
		EventType:     input.eventType,
		Payload:       input.payload,
		Attempts:      max(input.attempts, 1),
		FirstFailedAt: now,
		LastFailedAt:  now,
		FailureReason: input.reason,
		CorrelationID: input.correlationID,
		TraceID:       input.traceID,
	}

	// The full error goes to the log, where an operator can see the cause. The
	// stored reason is a sentence, because the dead letter queue is read in a
	// console and a driver message there is noise at best and a leak at worst.
	c.logger.ErrorContext(ctx, "event dead lettered",
		slog.String("event_id", eventID),
		slog.String("event_type", input.eventType),
		slog.Int("attempts", entry.Attempts),
		slog.String("error", errorText(input.terminateErr)))

	recordCtx, cancelRecord := ackContext(ctx)
	defer cancelRecord()

	if err := c.deadLetters.Record(recordCtx, entry); err != nil {
		// Recording failed, so the message must not be terminated: leaving it to
		// be redelivered is the only way it is not silently lost.
		c.logger.ErrorContext(ctx, "cannot record a dead letter",
			slog.String("event_id", eventID),
			slog.String("error", err.Error()))
		if nakErr := acker.Nak(recordCtx, c.cfg.Backoff.Delay(entry.Attempts)); nakErr != nil {
			c.logger.ErrorContext(ctx, "cannot request redelivery after a failed dead letter",
				slog.String("event_id", eventID), slog.String("error", nakErr.Error()))
		}
		return
	}

	c.metrics.DeadLettered(input.eventType)
	termCtx, cancelTerm := ackContext(ctx)
	defer cancelTerm()
	if err := acker.Term(termCtx); err != nil {
		c.logger.ErrorContext(ctx, "cannot terminate a dead lettered event",
			slog.String("event_id", eventID), slog.String("error", err.Error()))
	}
}

// describeFailure produces the operator-facing summary stored in the queue.
//
// It is built from the classification, never from the error text, so a driver
// message, a query or a path cannot reach a console through this field.
func describeFailure(err error, transient, exhausted bool) string {
	code := errs.CodeOf(err)
	switch {
	case exhausted && transient:
		return fmt.Sprintf("The event failed repeatedly and exhausted its retry budget (%s).", code)
	case !transient:
		return fmt.Sprintf("The event failed in a way that retrying cannot fix (%s).", code)
	default:
		return fmt.Sprintf("The event could not be processed (%s).", code)
	}
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// ackTimeout bounds every acknowledgement call.
const ackTimeout = 5 * time.Second

// ackContext detaches acknowledgement from cancellation. A shutdown must not
// stop the consumer from telling the broker about work it has already
// committed, or that work is redelivered for no reason.
func ackContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), ackTimeout)
}

func wait(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

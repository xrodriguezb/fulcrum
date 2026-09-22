package infra

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/xrodriguezb/fulcrum/internal/outbox/domain"
	"github.com/xrodriguezb/fulcrum/internal/platform/config"
	"github.com/xrodriguezb/fulcrum/internal/platform/errs"
	"github.com/xrodriguezb/fulcrum/internal/platform/telemetry"
)

// msgIDHeader lets the broker reject a duplicate of a message it already has.
// It is an optimisation, not the correctness mechanism: the consumer's
// deduplication table is what actually makes at-least-once delivery produce one
// effect, because broker level deduplication has a finite window.
const msgIDHeader = "Nats-Msg-Id"

// JetStreamPublisher publishes envelopes to a durable stream.
type JetStreamPublisher struct {
	stream  jetstream.JetStream
	subject string
	timeout time.Duration
}

// EnsureStream creates or updates the stream this system publishes to and
// returns a publisher bound to it.
//
// The stream is created by the publisher rather than by an operator script
// because the stream configuration is part of the delivery contract: retention,
// storage and duplicate window decide what at-least-once actually means here.
func EnsureStream(ctx context.Context, conn *nats.Conn, cfg config.NATSConfig) (*JetStreamPublisher, error) {
	js, err := jetstream.New(conn)
	if err != nil {
		return nil, errs.Unavailable("cannot open a jetstream context", err)
	}

	subjects := cfg.SubjectPrefix + ".>"
	_, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     cfg.StreamName,
		Subjects: []string{subjects},
		// File storage, because the point of choosing JetStream over core NATS
		// is that an event survives a broker restart.
		Storage:   jetstream.FileStorage,
		Retention: jetstream.LimitsPolicy,
		MaxAge:    7 * 24 * time.Hour,
		Discard:   jetstream.DiscardOld,
		// The duplicate window covers a publisher retrying the same message id
		// after a timeout. It is deliberately short: the consumer owns
		// deduplication, this only spares it the work.
		Duplicates: 2 * time.Minute,
	})
	if err != nil {
		return nil, errs.Unavailable("cannot create the event stream", err)
	}

	return &JetStreamPublisher{stream: js, subject: cfg.SubjectPrefix, timeout: cfg.PublishTimeout}, nil
}

// Publish sends one envelope and waits for the broker to acknowledge it.
//
// Waiting for the acknowledgement is the whole point: a fire and forget publish
// would let the publisher mark a row published that the broker never stored.
func (p *JetStreamPublisher) Publish(ctx context.Context, envelope domain.Envelope) error {
	ctx, span := telemetry.Tracer("fulcrum/outbox").Start(ctx, "publish "+envelope.EventType,
		trace.WithSpanKind(trace.SpanKindProducer),
		trace.WithAttributes(
			attribute.String("messaging.system", "nats"),
			attribute.String("messaging.destination.name", envelope.Subject(p.subject)),
			attribute.String("event.type", envelope.EventType),
		))
	defer span.End()

	payload, err := envelope.MarshalWire()
	if err != nil {
		// A malformed envelope is permanent. Retrying it forever would be a
		// worker spending its life on a row that can never succeed.
		return errs.Validation(errs.CodeEventPayloadInvalid, "The event envelope is not publishable.", err)
	}

	message := &nats.Msg{
		Subject: envelope.Subject(p.subject),
		Data:    payload,
		Header: nats.Header{
			msgIDHeader:        []string{envelope.ID},
			"X-Correlation-Id": []string{envelope.CorrelationID},
			"X-Event-Type":     []string{envelope.EventType},
			"X-Event-Version":  []string{fmt.Sprintf("%d", envelope.EventVersion)},
		},
	}
	if envelope.TraceID != "" {
		message.Header.Set("X-Trace-Id", envelope.TraceID)
	}

	// The trace continues into the consumer through the standard header, so one
	// order can be followed across three processes in a trace backend.
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(message.Header))

	publishCtx := ctx
	if p.timeout > 0 {
		var cancel context.CancelFunc
		publishCtx, cancel = context.WithTimeout(ctx, p.timeout)
		defer cancel()
	}

	if _, err := p.stream.PublishMsg(publishCtx, message); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "publish failed")
		if errors.Is(err, context.DeadlineExceeded) {
			return errs.Timeout("the broker did not acknowledge the publish", err)
		}
		return errs.Unavailable("the broker rejected the publish", err)
	}
	return nil
}

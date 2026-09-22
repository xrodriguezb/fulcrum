package infra

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/xrodriguezb/fulcrum/internal/consumer/app"
	"github.com/xrodriguezb/fulcrum/internal/platform/config"
	"github.com/xrodriguezb/fulcrum/internal/platform/errs"
)

// fetchTimeout bounds a pull. A pull that waits forever would make shutdown
// depend on a message arriving.
const fetchTimeout = 2 * time.Second

// Source pulls messages from a durable JetStream consumer.
//
// The consumer is durable and explicit-ack: the broker keeps track of what this
// consumer has acknowledged, so a restart resumes rather than replays, and an
// unacknowledged message comes back after the acknowledgement wait.
type Source struct {
	consumer jetstream.Consumer
}

// NewSource creates or updates the durable consumer and returns a source.
func NewSource(ctx context.Context, conn *nats.Conn, cfg config.NATSConfig, consumerCfg config.ConsumerConfig) (*Source, error) {
	js, err := jetstream.New(conn)
	if err != nil {
		return nil, errs.Unavailable("cannot open a jetstream context", err)
	}

	stream, err := js.Stream(ctx, cfg.StreamName)
	if err != nil {
		return nil, errs.Unavailable("cannot open the event stream", err)
	}

	consumer, err := stream.CreateOrUpdateConsumer(ctx, jetstream.ConsumerConfig{
		Durable:   consumerCfg.Name,
		AckPolicy: jetstream.AckExplicitPolicy,
		AckWait:   consumerCfg.AckWait,
		// MaxDeliver is left generous because the retry budget is enforced by
		// the consumer, which can tell a transient failure from a permanent one.
		// Letting the broker decide would dead letter a payload that is simply
		// arriving while the database is briefly unavailable.
		MaxDeliver:    consumerCfg.MaxAttempts * 10,
		FilterSubject: cfg.SubjectPrefix + ".>",
		DeliverPolicy: jetstream.DeliverAllPolicy,
	})
	if err != nil {
		return nil, errs.Unavailable("cannot create the durable consumer", err)
	}

	return &Source{consumer: consumer}, nil
}

// Fetch pulls up to batch messages, waiting briefly when the stream is empty.
func (s *Source) Fetch(ctx context.Context, batch int) ([]app.Message, error) {
	batches, err := s.consumer.Fetch(batch, jetstream.FetchMaxWait(fetchTimeout))
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, nats.ErrTimeout) {
			return nil, nil
		}
		return nil, errs.Unavailable("cannot fetch events", err)
	}

	var messages []app.Message
	for msg := range batches.Messages() {
		metadata, metaErr := msg.Metadata()
		deliveries := 1
		if metaErr == nil {
			// The delivery count is bounded before the conversion: a value that
			// large can only come from a corrupted header, and clamping keeps it
			// from wrapping into a negative attempt count.
			deliveries = clampDeliveries(metadata.NumDelivered)
		}
		messages = append(messages, app.Message{
			Payload:    msg.Data(),
			Deliveries: deliveries,
			Ack:        acker{msg: msg},
		})
	}
	if batches.Error() != nil {
		return messages, errs.Unavailable("the fetch ended with an error", batches.Error())
	}

	if ctx.Err() != nil {
		return messages, nil
	}
	return messages, nil
}

// maxDeliveries bounds the delivery count the consumer will act on.
const maxDeliveries = 1_000_000

func clampDeliveries(value uint64) int {
	if value == 0 {
		return 1
	}
	if value > maxDeliveries {
		return maxDeliveries
	}
	return int(value)
}

// acker adapts a JetStream message to the consumer's acknowledgement port.
type acker struct {
	msg jetstream.Msg
}

// Ack accepts the message.
func (a acker) Ack(context.Context) error {
	if err := a.msg.Ack(); err != nil {
		return fmt.Errorf("acknowledge: %w", err)
	}
	return nil
}

// Nak asks for redelivery after the given delay, which is how the consumer's
// own backoff reaches the broker.
func (a acker) Nak(_ context.Context, delay time.Duration) error {
	if err := a.msg.NakWithDelay(delay); err != nil {
		return fmt.Errorf("request redelivery: %w", err)
	}
	return nil
}

// Term rejects the message permanently. It is used only after the event has been
// recorded in the dead letter queue, so terminating it loses nothing.
func (a acker) Term(context.Context) error {
	if err := a.msg.Term(); err != nil {
		return fmt.Errorf("terminate: %w", err)
	}
	return nil
}

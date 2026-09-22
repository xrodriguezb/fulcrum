// Package domain defines the event envelope: the shape every event takes on the
// wire, independent of the aggregate that produced it and of the database row it
// was stored in.
package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ErrInvalidEnvelope means the envelope cannot be published as it stands.
var ErrInvalidEnvelope = errors.New("invalid event envelope")

// Envelope is one event, ready to be stored and published.
//
// Payload is the event's own data. It is never a database row: a consumer that
// depends on the shape of a table is a consumer that breaks on the next
// migration.
type Envelope struct {
	ID            string
	AggregateID   string
	AggregateType string
	EventType     string
	EventVersion  int
	Payload       json.RawMessage
	CorrelationID string
	TraceID       string
	OccurredAt    time.Time
}

// wireEnvelope is the published shape. It is a separate type so that renaming a
// field on Envelope cannot silently change what consumers receive.
type wireEnvelope struct {
	ID            string          `json:"id"`
	Type          string          `json:"type"`
	Version       int             `json:"version"`
	OccurredAt    string          `json:"occurred_at"`
	CorrelationID string          `json:"correlation_id"`
	TraceID       string          `json:"trace_id"`
	AggregateID   string          `json:"aggregate_id"`
	AggregateType string          `json:"aggregate_type"`
	Data          json.RawMessage `json:"data"`
}

// MarshalWire renders the envelope in the published format.
func (e Envelope) MarshalWire() ([]byte, error) {
	if err := e.Validate(); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(wireEnvelope{
		ID:            e.ID,
		Type:          e.EventType,
		Version:       e.EventVersion,
		OccurredAt:    e.OccurredAt.UTC().Format(time.RFC3339Nano),
		CorrelationID: e.CorrelationID,
		TraceID:       e.TraceID,
		AggregateID:   e.AggregateID,
		AggregateType: e.AggregateType,
		Data:          e.Payload,
	})
	if err != nil {
		return nil, fmt.Errorf("encode envelope: %w", err)
	}
	return encoded, nil
}

// UnmarshalWire parses the published format back into an envelope, which is what
// the consumer receives.
func UnmarshalWire(raw []byte) (Envelope, error) {
	var wire wireEnvelope
	decoder := json.NewDecoder(newTrimReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return Envelope{}, fmt.Errorf("%w: %w", ErrInvalidEnvelope, err)
	}

	occurredAt, err := time.Parse(time.RFC3339Nano, wire.OccurredAt)
	if err != nil {
		return Envelope{}, fmt.Errorf("%w: occurred_at is not an rfc3339 timestamp: %w", ErrInvalidEnvelope, err)
	}

	envelope := Envelope{
		ID:            wire.ID,
		AggregateID:   wire.AggregateID,
		AggregateType: wire.AggregateType,
		EventType:     wire.Type,
		EventVersion:  wire.Version,
		Payload:       wire.Data,
		CorrelationID: wire.CorrelationID,
		TraceID:       wire.TraceID,
		OccurredAt:    occurredAt,
	}
	if err := envelope.Validate(); err != nil {
		return Envelope{}, err
	}
	return envelope, nil
}

// Validate reports whether the envelope carries everything a consumer needs.
func (e Envelope) Validate() error {
	switch {
	case e.ID == "":
		return fmt.Errorf("%w: id is required", ErrInvalidEnvelope)
	case e.AggregateID == "":
		return fmt.Errorf("%w: aggregate_id is required", ErrInvalidEnvelope)
	case e.AggregateType == "":
		return fmt.Errorf("%w: aggregate_type is required", ErrInvalidEnvelope)
	case e.EventType == "":
		return fmt.Errorf("%w: type is required", ErrInvalidEnvelope)
	case e.EventVersion <= 0:
		return fmt.Errorf("%w: version must be positive", ErrInvalidEnvelope)
	case len(e.Payload) == 0:
		return fmt.Errorf("%w: data is required", ErrInvalidEnvelope)
	case e.CorrelationID == "":
		return fmt.Errorf("%w: correlation_id is required", ErrInvalidEnvelope)
	case e.OccurredAt.IsZero():
		return fmt.Errorf("%w: occurred_at is required", ErrInvalidEnvelope)
	default:
		return nil
	}
}

// Subject returns the broker subject for this event, derived from the event type
// so a consumer can subscribe to a family without a mapping table.
func (e Envelope) Subject(prefix string) string {
	return prefix + "." + e.EventType
}

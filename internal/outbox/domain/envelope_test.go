package domain_test

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xrodriguezb/fulcrum/internal/outbox/domain"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// The envelope is a published contract. This golden file fails whenever its
// shape changes, which forces the change to be either reverted or accompanied by
// a version bump and a note for consumers.
func TestEnvelopeWireFormatIsStable(t *testing.T) {
	t.Parallel()

	envelope := domain.Envelope{
		ID:            "6f1a2b3c-4d5e-4f60-8172-839405a6b7c8",
		AggregateID:   "0b7c6f4e-9d3a-4f1b-8c2d-5a6e7f8b9c0d",
		AggregateType: "order",
		EventType:     "order.created",
		EventVersion:  1,
		Payload:       json.RawMessage(`{"customer_id":"11111111-2222-4333-8444-555555555555","total_cents":2100}`),
		CorrelationID: "corr-1",
		TraceID:       "4bf92f3577b34da6a3ce929d0e0e4736",
		OccurredAt:    time.Date(2026, time.September, 21, 10, 0, 0, 0, time.UTC),
	}

	encoded, err := envelope.MarshalWire()
	if err != nil {
		t.Fatalf("MarshalWire returned %v", err)
	}

	golden := filepath.Join("testdata", "order_created_v1.json")
	if *update {
		if err := os.WriteFile(golden, encoded, 0o600); err != nil {
			t.Fatalf("write golden file: %v", err)
		}
	}

	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read golden file: %v", err)
	}
	if string(encoded) != string(want) {
		t.Errorf("the envelope wire format changed.\ngot:  %s\nwant: %s\n"+
			"If this change is intended, bump the event version and update the golden file with -update.",
			encoded, want)
	}
}

func TestEnvelopeValidation(t *testing.T) {
	t.Parallel()

	valid := domain.Envelope{
		ID:            "6f1a2b3c-4d5e-4f60-8172-839405a6b7c8",
		AggregateID:   "0b7c6f4e-9d3a-4f1b-8c2d-5a6e7f8b9c0d",
		AggregateType: "order",
		EventType:     "order.created",
		EventVersion:  1,
		Payload:       json.RawMessage(`{}`),
		CorrelationID: "corr-1",
		OccurredAt:    time.Now(),
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("a complete envelope must validate, got %v", err)
	}

	cases := map[string]func(domain.Envelope) domain.Envelope{
		"missing id":             func(e domain.Envelope) domain.Envelope { e.ID = ""; return e },
		"missing aggregate id":   func(e domain.Envelope) domain.Envelope { e.AggregateID = ""; return e },
		"missing event type":     func(e domain.Envelope) domain.Envelope { e.EventType = ""; return e },
		"zero version":           func(e domain.Envelope) domain.Envelope { e.EventVersion = 0; return e },
		"empty payload":          func(e domain.Envelope) domain.Envelope { e.Payload = nil; return e },
		"missing correlation id": func(e domain.Envelope) domain.Envelope { e.CorrelationID = ""; return e },
		"zero occurred at":       func(e domain.Envelope) domain.Envelope { e.OccurredAt = time.Time{}; return e },
		// The stores that key events by these two fields hold them in uuid
		// columns, so an identifier of the wrong shape is rejected here rather
		// than by an insert the consumer cannot recover from.
		"id that is not a uuid": func(e domain.Envelope) domain.Envelope { e.ID = "not-a-uuid"; return e },
		"id missing a group":    func(e domain.Envelope) domain.Envelope { e.ID = "6f1a2b3c-4d5e-4f60-8172"; return e },
		"id with a non hex digit": func(e domain.Envelope) domain.Envelope {
			e.ID = "6f1a2b3g-4d5e-4f60-8172-839405a6b7c8"
			return e
		},
		"aggregate id that is not a uuid": func(e domain.Envelope) domain.Envelope {
			e.AggregateID = "order-42"
			return e
		},
	}

	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := mutate(valid).Validate(); err == nil {
				t.Errorf("an envelope with %s must not validate", name)
			}
		})
	}
}

// Subjects are derived from the event type so that a consumer can subscribe to a
// family of events without the publisher maintaining a mapping table.
func TestSubject(t *testing.T) {
	t.Parallel()

	envelope := domain.Envelope{EventType: "order.created"}
	if got := envelope.Subject("fulcrum.events"); got != "fulcrum.events.order.created" {
		t.Errorf("Subject = %q, want fulcrum.events.order.created", got)
	}
}

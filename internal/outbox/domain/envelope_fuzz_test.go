package domain_test

import (
	"testing"

	"github.com/xrodriguezb/fulcrum/internal/outbox/domain"
)

// FuzzUnmarshalWire feeds arbitrary bytes to the one decoder that reads
// untrusted input.
//
// Everything else in this system parses data the system itself wrote. This
// function parses whatever arrives on a broker subject, which anything with
// publish rights can write. It must reject or accept, never panic, and anything
// it accepts must survive a round trip unchanged: an envelope that decodes into
// something that re-encodes differently would mean the consumer acts on a
// document the publisher did not send.
func FuzzUnmarshalWire(f *testing.F) {
	seeds := []string{
		`{"id":"6f1a2b3c-4d5e-4f60-8172-839405a6b7c8","type":"order.created","version":1,` +
			`"occurred_at":"2026-09-21T10:00:00Z","correlation_id":"corr-1","trace_id":"",` +
			`"aggregate_id":"0b7c6f4e-9d3a-4f1b-8c2d-5a6e7f8b9c0d","aggregate_type":"order","data":{}}`,
		`{"id":"","type":"","version":0,"occurred_at":"","correlation_id":"","trace_id":"",` +
			`"aggregate_id":"","aggregate_type":"","data":null}`,
		`{"this":"is not an envelope"}`,
		`not json at all`,
		``,
		`{`,
		`[]`,
	}
	for _, seed := range seeds {
		f.Add([]byte(seed))
	}

	f.Fuzz(func(t *testing.T, payload []byte) {
		envelope, err := domain.UnmarshalWire(payload)
		if err != nil {
			// Rejection is the expected outcome for almost everything.
			return
		}

		// Anything accepted has to be publishable again, because the consumer
		// treats it as a fact the publisher stated.
		encoded, marshalErr := envelope.MarshalWire()
		if marshalErr != nil {
			t.Fatalf("an accepted envelope could not be re-encoded: %v (%q)", marshalErr, payload)
		}

		again, err := domain.UnmarshalWire(encoded)
		if err != nil {
			t.Fatalf("a re-encoded envelope was rejected: %v (%q)", err, encoded)
		}

		if again.ID != envelope.ID || again.EventType != envelope.EventType ||
			again.EventVersion != envelope.EventVersion || again.AggregateID != envelope.AggregateID ||
			again.CorrelationID != envelope.CorrelationID {
			t.Fatalf("a round trip changed the envelope:\nfirst:  %+v\nsecond: %+v", envelope, again)
		}
		if !again.OccurredAt.Equal(envelope.OccurredAt) {
			t.Fatalf("a round trip changed occurred_at: %v then %v", envelope.OccurredAt, again.OccurredAt)
		}
	})
}

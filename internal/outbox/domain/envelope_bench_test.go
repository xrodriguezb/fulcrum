package domain_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/xrodriguezb/fulcrum/internal/outbox/domain"
)

// Every published event is marshalled once and every consumed event is
// unmarshalled once, so these two costs are paid per event on both sides of the
// broker.
func BenchmarkEnvelopeRoundTrip(b *testing.B) {
	envelope := domain.Envelope{
		ID:            "6f1a2b3c-4d5e-4f60-8172-839405a6b7c8",
		AggregateID:   "0b7c6f4e-9d3a-4f1b-8c2d-5a6e7f8b9c0d",
		AggregateType: "order",
		EventType:     "order.created",
		EventVersion:  1,
		Payload: json.RawMessage(`{"customer_id":"11111111-2222-4333-8444-555555555555",` +
			`"status":"pending","total_cents":2100,"currency":"EUR",` +
			`"lines":[{"sku":"WIDGET-001","quantity":2,"unit_price_cents":1050}]}`),
		CorrelationID: "corr-1",
		TraceID:       "4bf92f3577b34da6a3ce929d0e0e4736",
		OccurredAt:    time.Date(2026, time.September, 21, 10, 0, 0, 0, time.UTC),
	}

	encoded, err := envelope.MarshalWire()
	if err != nil {
		b.Fatalf("MarshalWire returned %v", err)
	}

	b.Run("marshal", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, err := envelope.MarshalWire(); err != nil {
				b.Fatalf("MarshalWire returned %v", err)
			}
		}
	})

	b.Run("unmarshal", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(encoded)))
		for b.Loop() {
			if _, err := domain.UnmarshalWire(encoded); err != nil {
				b.Fatalf("UnmarshalWire returned %v", err)
			}
		}
	})
}

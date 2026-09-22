package domain

import "time"

// Event is a fact that has already happened. Events are recorded by the
// aggregate and published by the application layer through the outbox.
type Event interface {
	// EventType is the stable wire name, for example order.created.
	EventType() string
	// AggregateID identifies the order the fact is about.
	AggregateID() string
	// OccurredAt is when the fact became true, not when it was published.
	OccurredAt() time.Time
	// EventVersion is the schema version of the payload. It exists from the
	// first release so that adding a field later is a version bump rather than
	// a silent change of shape.
	EventVersion() int
}

// EventLine is the line representation carried on the wire. It is deliberately
// not the Line value object: an event payload must not change shape because an
// internal type was refactored.
type EventLine struct {
	SKU            string `json:"sku"`
	Quantity       int    `json:"quantity"`
	UnitPriceCents int64  `json:"unit_price_cents"`
}

// OrderCreated states that an order exists and its inventory is reserved.
type OrderCreated struct {
	orderID    string
	occurredAt time.Time

	CustomerID string      `json:"customer_id"`
	Status     string      `json:"status"`
	TotalCents int64       `json:"total_cents"`
	Currency   string      `json:"currency"`
	Lines      []EventLine `json:"lines"`
	CreatedAt  time.Time   `json:"created_at"`
}

func newOrderCreated(order *Order, now time.Time) OrderCreated {
	lines := make([]EventLine, 0, len(order.lines))
	for _, line := range order.lines {
		lines = append(lines, EventLine{
			SKU:            line.SKU().String(),
			Quantity:       line.Quantity().Int(),
			UnitPriceCents: line.UnitPrice().Cents(),
		})
	}
	return OrderCreated{
		orderID:    order.id.String(),
		occurredAt: now,
		CustomerID: order.customerID.String(),
		Status:     order.status.String(),
		TotalCents: order.total.Cents(),
		Currency:   order.total.Currency(),
		Lines:      lines,
		CreatedAt:  order.createdAt,
	}
}

// EventType returns order.created.
func (e OrderCreated) EventType() string { return "order.created" }

// AggregateID returns the order identifier.
func (e OrderCreated) AggregateID() string { return e.orderID }

// OccurredAt returns when the order was created.
func (e OrderCreated) OccurredAt() time.Time { return e.occurredAt }

// EventVersion returns the payload schema version.
func (e OrderCreated) EventVersion() int { return 1 }

// OrderConfirmed states that downstream processing of an order completed.
type OrderConfirmed struct {
	orderID    string
	occurredAt time.Time
}

// EventType returns order.confirmed.
func (e OrderConfirmed) EventType() string { return "order.confirmed" }

// AggregateID returns the order identifier.
func (e OrderConfirmed) AggregateID() string { return e.orderID }

// OccurredAt returns when the order was confirmed.
func (e OrderConfirmed) OccurredAt() time.Time { return e.occurredAt }

// EventVersion returns the payload schema version.
func (e OrderConfirmed) EventVersion() int { return 1 }

// OrderCancelled states that an order will not be fulfilled.
type OrderCancelled struct {
	orderID    string
	occurredAt time.Time

	Reason string `json:"reason"`
}

// EventType returns order.cancelled.
func (e OrderCancelled) EventType() string { return "order.cancelled" }

// AggregateID returns the order identifier.
func (e OrderCancelled) AggregateID() string { return e.orderID }

// OccurredAt returns when the order was cancelled.
func (e OrderCancelled) OccurredAt() time.Time { return e.occurredAt }

// EventVersion returns the payload schema version.
func (e OrderCancelled) EventVersion() int { return 1 }

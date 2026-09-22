package domain

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// MaxOrderLines bounds how much work a single order can ask for. It exists in
// the domain because it is a business rule, and it is mirrored by the request
// validation at the edge so an oversized order is rejected before a transaction
// is opened.
const MaxOrderLines = 50

// Status is the state of an order. The set is closed and transitions are
// checked, so an order can never be in a state the system does not model.
type Status int

const (
	// StatusPending is the state an order is created in. Inventory is already
	// reserved at this point: pending describes the downstream work, not the stock.
	StatusPending Status = iota
	// StatusConfirmed is set when the consumer has processed order.created.
	StatusConfirmed
	// StatusCancelled is terminal.
	StatusCancelled
)

// String renders the status as it appears in the API and the database.
func (s Status) String() string {
	switch s {
	case StatusPending:
		return "pending"
	case StatusConfirmed:
		return "confirmed"
	case StatusCancelled:
		return "cancelled"
	default:
		return "unknown"
	}
}

// ParseStatus converts the stored representation back into a status.
func ParseStatus(raw string) (Status, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "pending":
		return StatusPending, nil
	case "confirmed":
		return StatusConfirmed, nil
	case "cancelled":
		return StatusCancelled, nil
	default:
		return StatusPending, fmt.Errorf("%w: %q is not an order status", ErrInvalidTransition, raw)
	}
}

// Line is one reserved item within an order.
type Line struct {
	sku       SKU
	quantity  Quantity
	unitPrice Money
}

// NewLine builds an order line from validated value objects.
func NewLine(sku SKU, quantity Quantity, unitPrice Money) (Line, error) {
	if sku.IsZero() {
		return Line{}, fmt.Errorf("%w: a line needs a sku", ErrInvalidSKU)
	}
	if unitPrice.IsZero() {
		return Line{}, fmt.Errorf("%w: a line needs a price", ErrInvalidAmount)
	}
	return Line{sku: sku, quantity: quantity, unitPrice: unitPrice}, nil
}

// SKU returns the line's stock keeping unit.
func (l Line) SKU() SKU { return l.sku }

// Quantity returns the reserved count.
func (l Line) Quantity() Quantity { return l.quantity }

// UnitPrice returns the price of a single unit.
func (l Line) UnitPrice() Money { return l.unitPrice }

// Subtotal returns the price of the whole line.
func (l Line) Subtotal() (Money, error) {
	return l.unitPrice.Times(l.quantity.Int())
}

// Order is the aggregate root. Every field is private: the only way to change an
// order is through a method that checks the transition first.
type Order struct {
	id         OrderID
	customerID CustomerID
	status     Status
	lines      []Line
	total      Money
	createdAt  time.Time
	updatedAt  time.Time
	events     []Event
}

// NewOrder validates the order invariants, computes the total and records the
// creation event. Lines come back sorted by sku: reserving in a fixed order is
// what prevents two orders that touch the same pair of items in opposite
// sequence from deadlocking each other in the database.
func NewOrder(id OrderID, customerID CustomerID, lines []Line, now time.Time) (*Order, error) {
	if id.IsZero() {
		return nil, fmt.Errorf("%w: an order needs an id", ErrInvalidOrderID)
	}
	if customerID.IsZero() {
		return nil, fmt.Errorf("%w: an order needs a customer", ErrInvalidCustomerID)
	}
	if len(lines) == 0 {
		return nil, ErrNoLines
	}
	if len(lines) > MaxOrderLines {
		return nil, fmt.Errorf("%w: %d lines, the limit is %d", ErrTooManyLines, len(lines), MaxOrderLines)
	}

	sorted := make([]Line, len(lines))
	copy(sorted, lines)
	slices.SortFunc(sorted, func(a, b Line) int {
		return strings.Compare(a.SKU().String(), b.SKU().String())
	})

	seen := make(map[string]struct{}, len(sorted))
	for _, line := range sorted {
		key := line.SKU().String()
		if _, duplicate := seen[key]; duplicate {
			return nil, fmt.Errorf("%w: %s", ErrDuplicateSKU, key)
		}
		seen[key] = struct{}{}
	}

	total, err := sumLines(sorted)
	if err != nil {
		return nil, err
	}

	order := &Order{
		id:         id,
		customerID: customerID,
		status:     StatusPending,
		lines:      sorted,
		total:      total,
		createdAt:  now,
		updatedAt:  now,
	}
	order.record(newOrderCreated(order, now))
	return order, nil
}

func sumLines(lines []Line) (Money, error) {
	first, err := lines[0].Subtotal()
	if err != nil {
		return Money{}, err
	}
	total := first
	for _, line := range lines[1:] {
		subtotal, subErr := line.Subtotal()
		if subErr != nil {
			return Money{}, subErr
		}
		total, err = total.Add(subtotal)
		if err != nil {
			return Money{}, err
		}
	}
	return total, nil
}

// State is the persisted shape of an order, used to rebuild the aggregate.
type State struct {
	ID         OrderID
	CustomerID CustomerID
	Status     Status
	Lines      []Line
	Total      Money
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Rehydrate rebuilds an order that already exists. It emits no events, because
// the events for this order were recorded when it was created.
func Rehydrate(state State) (*Order, error) {
	if state.ID.IsZero() {
		return nil, fmt.Errorf("%w: rehydration needs an id", ErrInvalidOrderID)
	}
	if state.CustomerID.IsZero() {
		return nil, fmt.Errorf("%w: rehydration needs a customer", ErrInvalidCustomerID)
	}
	if len(state.Lines) == 0 {
		return nil, ErrNoLines
	}
	if state.Total.IsZero() {
		return nil, fmt.Errorf("%w: rehydration needs a total", ErrInvalidAmount)
	}

	lines := make([]Line, len(state.Lines))
	copy(lines, state.Lines)
	slices.SortFunc(lines, func(a, b Line) int {
		return strings.Compare(a.SKU().String(), b.SKU().String())
	})

	return &Order{
		id:         state.ID,
		customerID: state.CustomerID,
		status:     state.Status,
		lines:      lines,
		total:      state.Total,
		createdAt:  state.CreatedAt,
		updatedAt:  state.UpdatedAt,
	}, nil
}

// Confirm moves a pending order to confirmed.
//
// A second confirmation reports ErrAlreadyConfirmed rather than
// ErrInvalidTransition. Delivery is at-least-once, so a redelivered
// order.created event is an expected occurrence and the consumer has to be able
// to tell it apart from a genuine protocol violation.
func (o *Order) Confirm(now time.Time) error {
	switch o.status {
	case StatusPending:
		o.status = StatusConfirmed
		o.updatedAt = now
		o.record(OrderConfirmed{orderID: o.id.String(), occurredAt: now})
		return nil
	case StatusConfirmed:
		return ErrAlreadyConfirmed
	case StatusCancelled:
		return fmt.Errorf("%w: cannot confirm a cancelled order", ErrInvalidTransition)
	default:
		return fmt.Errorf("%w: unknown state", ErrInvalidTransition)
	}
}

// Cancel moves a pending or confirmed order to cancelled, which is terminal.
func (o *Order) Cancel(now time.Time, reason string) error {
	switch o.status {
	case StatusPending, StatusConfirmed:
		o.status = StatusCancelled
		o.updatedAt = now
		o.record(OrderCancelled{orderID: o.id.String(), occurredAt: now, Reason: reason})
		return nil
	case StatusCancelled:
		return ErrAlreadyCancelled
	default:
		return fmt.Errorf("%w: unknown state", ErrInvalidTransition)
	}
}

// ID returns the order identifier.
func (o *Order) ID() OrderID { return o.id }

// CustomerID returns the owning customer.
func (o *Order) CustomerID() CustomerID { return o.customerID }

// Status returns the current state.
func (o *Order) Status() Status { return o.status }

// Total returns the order total.
func (o *Order) Total() Money { return o.total }

// CreatedAt returns the creation instant.
func (o *Order) CreatedAt() time.Time { return o.createdAt }

// UpdatedAt returns the instant of the last state change.
func (o *Order) UpdatedAt() time.Time { return o.updatedAt }

// Lines returns a copy of the order lines, sorted by sku. The copy is what keeps
// the aggregate's invariants from depending on callers behaving.
func (o *Order) Lines() []Line {
	out := make([]Line, len(o.lines))
	copy(out, o.lines)
	return out
}

// PullEvents returns the events recorded since the last call and clears the
// buffer, so an event cannot be published twice from one aggregate instance.
func (o *Order) PullEvents() []Event {
	if len(o.events) == 0 {
		return nil
	}
	events := o.events
	o.events = nil
	return events
}

func (o *Order) record(event Event) {
	o.events = append(o.events, event)
}

package app

import (
	"context"
	"errors"
	"time"

	"github.com/xrodriguezb/fulcrum/internal/order/domain"
	outboxdomain "github.com/xrodriguezb/fulcrum/internal/outbox/domain"
	"github.com/xrodriguezb/fulcrum/internal/platform/errs"
)

// ConfirmOrderHandler is the side effect of order.created.
//
// It is a real state change rather than a log line, because the consumer's
// deduplication guarantee is only meaningful if there is something to duplicate.
type ConfirmOrderHandler struct {
	orders Repository
	clock  Clock
}

// NewConfirmOrderHandler wires the confirmation use case.
func NewConfirmOrderHandler(orders Repository, clock Clock) (*ConfirmOrderHandler, error) {
	if orders == nil {
		return nil, errors.New("the confirm order handler needs an order repository")
	}
	if clock == nil {
		clock = time.Now
	}
	return &ConfirmOrderHandler{orders: orders, clock: clock}, nil
}

// Handle moves the order to confirmed.
//
// An order that is already confirmed is success, not a failure: at-least-once
// delivery means a redelivered event is ordinary traffic, and the deduplication
// row is rolled back with any transaction that fails, so a retry legitimately
// arrives at an order the previous attempt had already moved.
func (h *ConfirmOrderHandler) Handle(ctx context.Context, envelope outboxdomain.Envelope) error {
	if envelope.EventType != "order.created" {
		// A consumer that silently ignores an unexpected event type is a
		// consumer that hides a routing mistake. This is permanent by
		// classification, so it goes to the dead letter queue.
		return errs.Validation(errs.CodeEventVersionUnsupported,
			"This consumer does not handle that event type.", nil)
	}
	if envelope.EventVersion != 1 {
		return errs.Validation(errs.CodeEventVersionUnsupported,
			"This consumer does not handle that event version.", nil)
	}

	orderID, err := domain.NewOrderID(envelope.AggregateID)
	if err != nil {
		return errs.Validation(errs.CodeEventPayloadInvalid,
			"The event does not carry a usable order identifier.", err)
	}

	order, err := h.orders.ByID(ctx, orderID)
	if err != nil {
		return err
	}

	switch confirmErr := order.Confirm(h.clock()); {
	case confirmErr == nil:
	case errors.Is(confirmErr, domain.ErrAlreadyConfirmed):
		return nil
	default:
		return errs.Precondition(errs.CodeOrderInvalidState,
			"The order cannot be confirmed from its current state.", confirmErr)
	}

	return h.orders.UpdateStatus(ctx, order.ID(), order.Status(), order.UpdatedAt())
}

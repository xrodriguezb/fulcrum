package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	idempotency "github.com/xrodriguezb/fulcrum/internal/idempotency/app"
	inventory "github.com/xrodriguezb/fulcrum/internal/inventory/app"
	"github.com/xrodriguezb/fulcrum/internal/order/domain"
	outbox "github.com/xrodriguezb/fulcrum/internal/outbox/app"
	outboxdomain "github.com/xrodriguezb/fulcrum/internal/outbox/domain"
	"github.com/xrodriguezb/fulcrum/internal/platform/errs"
)

// CommandLine is one requested line. The price is deliberately absent: it comes
// from inventory, because a caller that sends its own price can choose it.
type CommandLine struct {
	SKU      string `json:"sku"`
	Quantity int    `json:"quantity"`
}

// CreateOrderCommand is a request to create an order.
type CreateOrderCommand struct {
	IdempotencyKey string
	RawBody        []byte
	CustomerID     string
	Lines          []CommandLine
	CorrelationID  string
	TraceID        string
}

// OrderLineView is the published representation of a line.
type OrderLineView struct {
	SKU            string `json:"sku"`
	Quantity       int    `json:"quantity"`
	UnitPriceCents int64  `json:"unit_price_cents"`
}

// OrderView is the published representation of an order. It matches the Order
// schema in api/openapi.yaml, and it is what gets stored for idempotent replay,
// so the bytes a retry receives are the bytes the first caller received.
type OrderView struct {
	ID         string          `json:"id"`
	CustomerID string          `json:"customer_id"`
	Status     string          `json:"status"`
	TotalCents int64           `json:"total_cents"`
	Currency   string          `json:"currency"`
	Lines      []OrderLineView `json:"lines"`
	CreatedAt  time.Time       `json:"created_at"`
}

// CreateOrderResult is what the transport layer turns into a response.
type CreateOrderResult struct {
	Status   int
	Body     []byte
	View     OrderView
	Replayed bool
}

// CreateOrderDeps are the collaborators of the use case. They are a struct
// rather than eight positional arguments so that adding one is a compile error
// at the wiring site and nowhere else.
type CreateOrderDeps struct {
	Tx           TxManager
	Orders       Repository
	Inventory    inventory.Reserver
	Outbox       outbox.Writer
	Idempotency  idempotency.Store
	Clock        Clock
	IDs          IDGenerator
	KeyTTL       time.Duration
	MaxKeyLength int
}

// CreateOrderHandler reserves inventory, persists an order and records the
// event, exactly once per idempotency key.
type CreateOrderHandler struct {
	deps CreateOrderDeps
}

// NewCreateOrderHandler validates the wiring. A partially built handler must not
// be able to exist: every missing collaborator is a nil dereference in
// production and a clear error here.
func NewCreateOrderHandler(deps CreateOrderDeps) (*CreateOrderHandler, error) {
	switch {
	case deps.Tx == nil:
		return nil, errors.New("create order handler needs a transaction manager")
	case deps.Orders == nil:
		return nil, errors.New("create order handler needs an order repository")
	case deps.Inventory == nil:
		return nil, errors.New("create order handler needs an inventory reserver")
	case deps.Outbox == nil:
		return nil, errors.New("create order handler needs an outbox writer")
	case deps.Idempotency == nil:
		return nil, errors.New("create order handler needs an idempotency store")
	case deps.Clock == nil:
		return nil, errors.New("create order handler needs a clock")
	case deps.IDs == nil:
		return nil, errors.New("create order handler needs an id generator")
	case deps.MaxKeyLength <= 0:
		return nil, errors.New("create order handler needs a positive key length limit")
	}
	return &CreateOrderHandler{deps: deps}, nil
}

// FingerprintOf returns the canonical fingerprint of a command's body. It is
// exported so that tests and the operations tooling can compute the same value
// the handler does.
func FingerprintOf(cmd CreateOrderCommand) ([]byte, error) {
	return idempotency.Fingerprint(cmd.RawBody)
}

// Handle runs the create order flow.
//
// The idempotency claim commits before the business transaction opens. Putting
// it inside would mean two concurrent duplicates both begin, neither sees the
// other's uncommitted row, and the loser blocks on the primary key until the
// winner commits, by which point it has already reserved inventory of its own.
// See ADR 0005.
func (h *CreateOrderHandler) Handle(ctx context.Context, cmd CreateOrderCommand) (CreateOrderResult, error) {
	if err := h.validateKey(cmd.IdempotencyKey); err != nil {
		return CreateOrderResult{}, err
	}

	fingerprint, err := idempotency.Fingerprint(cmd.RawBody)
	if err != nil {
		return CreateOrderResult{}, errs.Validation(errs.CodeValidationFailed,
			"The request body is not a single json document.", err)
	}

	now := h.deps.Clock()
	claim, err := h.deps.Idempotency.Claim(ctx, cmd.IdempotencyKey, fingerprint, now.Add(h.deps.KeyTTL))
	if err != nil {
		return CreateOrderResult{}, err
	}
	if !claim.Claimed {
		return h.replay(claim.Existing, fingerprint)
	}

	result, err := h.execute(ctx, cmd, now)
	if err != nil {
		// The key is released on a context that cancellation cannot reach:
		// a client that disconnected mid-request must not leave its key stuck
		// in_progress until the ttl expires.
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if failErr := h.deps.Idempotency.Fail(releaseCtx, cmd.IdempotencyKey, err.Error()); failErr != nil {
			return CreateOrderResult{}, errors.Join(err, failErr)
		}
		return CreateOrderResult{}, err
	}
	return result, nil
}

func (h *CreateOrderHandler) validateKey(key string) error {
	if key == "" {
		return errs.Validation(errs.CodeIdempotencyKeyRequired,
			"The Idempotency-Key header is required.", nil)
	}
	if err := idempotency.ValidateKey(key, h.deps.MaxKeyLength); err != nil {
		return errs.Validation(errs.CodeValidationFailed, "The Idempotency-Key header is not usable.", err)
	}
	return nil
}

// replay decides what a duplicate request receives.
func (h *CreateOrderHandler) replay(existing *idempotency.Record, fingerprint []byte) (CreateOrderResult, error) {
	if existing == nil {
		return CreateOrderResult{}, errs.Internal("idempotency claim reported no record",
			errors.New("a losing claim must carry the existing record"))
	}

	// A key reused for a different request is a client bug, and answering it
	// with the first request's response would hide that bug behind a success.
	if !bytes.Equal(existing.Fingerprint, fingerprint) {
		return CreateOrderResult{}, errs.Precondition(errs.CodeIdempotencyKeyReuse,
			"This Idempotency-Key was already used with a different request body.", nil)
	}

	switch existing.Status {
	case idempotency.StatusCompleted:
		var view OrderView
		if err := json.Unmarshal(existing.ResponseBody, &view); err != nil {
			return CreateOrderResult{}, errs.Internal("decode stored response", err)
		}
		return CreateOrderResult{
			Status:   existing.ResponseStatus,
			Body:     existing.ResponseBody,
			View:     view,
			Replayed: true,
		}, nil

	case idempotency.StatusInProgress:
		return CreateOrderResult{}, errs.Conflict(errs.CodeIdempotencyInProgress,
			"A request with this Idempotency-Key is still in progress.", nil)

	case idempotency.StatusFailed:
		// The store takes over a failed key on claim, so reaching here means the
		// record changed between the claim and this read.
		return CreateOrderResult{}, errs.Conflict(errs.CodeIdempotencyInProgress,
			"A request with this Idempotency-Key is being retried.", nil)

	default:
		return CreateOrderResult{}, errs.Internal("unknown idempotency status",
			fmt.Errorf("status %q", existing.Status))
	}
}

// execute performs the business transaction: reserve, persist, record, complete.
func (h *CreateOrderHandler) execute(ctx context.Context, cmd CreateOrderCommand, now time.Time) (CreateOrderResult, error) {
	var result CreateOrderResult

	err := h.deps.Tx.WithinTx(ctx, func(txCtx context.Context) error {
		reserved, reserveErr := h.deps.Inventory.Reserve(txCtx, toReservationRequests(cmd.Lines))
		if reserveErr != nil {
			return reserveErr
		}

		order, buildErr := h.buildOrder(cmd, reserved, now)
		if buildErr != nil {
			return buildErr
		}

		if saveErr := h.deps.Orders.Save(txCtx, order); saveErr != nil {
			return saveErr
		}

		envelopes, envelopeErr := h.envelopes(order, cmd)
		if envelopeErr != nil {
			return envelopeErr
		}
		if appendErr := h.deps.Outbox.Append(txCtx, envelopes...); appendErr != nil {
			return appendErr
		}

		view := ViewOf(order)
		body, marshalErr := json.Marshal(view)
		if marshalErr != nil {
			return errs.Internal("encode order response", marshalErr)
		}

		// Completing the key inside this transaction is what makes the stored
		// response and the work it describes commit together.
		if completeErr := h.deps.Idempotency.Complete(txCtx, cmd.IdempotencyKey, 201, body, order.ID().String()); completeErr != nil {
			return completeErr
		}

		result = CreateOrderResult{Status: 201, Body: body, View: view}
		return nil
	})
	if err != nil {
		return CreateOrderResult{}, err
	}
	return result, nil
}

func (h *CreateOrderHandler) buildOrder(cmd CreateOrderCommand, reserved []inventory.ReservedLine, now time.Time) (*domain.Order, error) {
	orderID, err := domain.NewOrderID(h.deps.IDs.NewID())
	if err != nil {
		return nil, errs.Internal("generated order id is not valid", err)
	}
	customerID, err := domain.NewCustomerID(cmd.CustomerID)
	if err != nil {
		return nil, errs.Validation(errs.CodeValidationFailed, "The customer id is not valid.", err)
	}

	lines := make([]domain.Line, 0, len(reserved))
	for _, item := range reserved {
		sku, skuErr := domain.NewSKU(item.SKU)
		if skuErr != nil {
			return nil, errs.Validation(errs.CodeValidationFailed, "The sku is not valid.", skuErr)
		}
		quantity, quantityErr := domain.NewQuantity(item.Quantity)
		if quantityErr != nil {
			return nil, errs.Validation(errs.CodeValidationFailed, "The quantity is not valid.", quantityErr)
		}
		price, priceErr := domain.NewMoney(item.UnitPriceCents, item.Currency)
		if priceErr != nil {
			return nil, errs.Internal("inventory returned an unusable price", priceErr)
		}
		line, lineErr := domain.NewLine(sku, quantity, price)
		if lineErr != nil {
			return nil, errs.Validation(errs.CodeValidationFailed, "The order line is not valid.", lineErr)
		}
		lines = append(lines, line)
	}

	order, err := domain.NewOrder(orderID, customerID, lines, now)
	if err != nil {
		return nil, errs.Validation(errs.CodeValidationFailed, "The order is not valid.", err)
	}
	return order, nil
}

// envelopes turns the aggregate's recorded events into outbox envelopes.
func (h *CreateOrderHandler) envelopes(order *domain.Order, cmd CreateOrderCommand) ([]outboxdomain.Envelope, error) {
	events := order.PullEvents()
	envelopes := make([]outboxdomain.Envelope, 0, len(events))

	for _, event := range events {
		payload, err := json.Marshal(event)
		if err != nil {
			return nil, errs.Internal("encode domain event", err)
		}

		eventID, err := domain.NewOrderID(h.deps.IDs.NewID())
		if err != nil {
			return nil, errs.Internal("generated event id is not valid", err)
		}

		envelopes = append(envelopes, outboxdomain.Envelope{
			ID:            eventID.String(),
			AggregateID:   event.AggregateID(),
			AggregateType: "order",
			EventType:     event.EventType(),
			EventVersion:  event.EventVersion(),
			Payload:       payload,
			CorrelationID: cmd.CorrelationID,
			TraceID:       cmd.TraceID,
			OccurredAt:    event.OccurredAt(),
		})
	}
	return envelopes, nil
}

func toReservationRequests(lines []CommandLine) []inventory.ReservationRequest {
	requests := make([]inventory.ReservationRequest, 0, len(lines))
	for _, line := range lines {
		requests = append(requests, inventory.ReservationRequest{SKU: line.SKU, Quantity: line.Quantity})
	}
	return requests
}

// ViewOf renders an aggregate for the wire. The read endpoints use it too, so
// one order has one representation regardless of which handler produced it.
func ViewOf(order *domain.Order) OrderView {
	lines := make([]OrderLineView, 0, len(order.Lines()))
	for _, line := range order.Lines() {
		lines = append(lines, OrderLineView{
			SKU:            line.SKU().String(),
			Quantity:       line.Quantity().Int(),
			UnitPriceCents: line.UnitPrice().Cents(),
		})
	}
	return OrderView{
		ID:         order.ID().String(),
		CustomerID: order.CustomerID().String(),
		Status:     order.Status().String(),
		TotalCents: order.Total().Cents(),
		Currency:   order.Total().Currency(),
		Lines:      lines,
		CreatedAt:  order.CreatedAt().UTC(),
	}
}

//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	idempotencyinfra "github.com/xrodriguezb/fulcrum/internal/idempotency/infra"
	inventoryinfra "github.com/xrodriguezb/fulcrum/internal/inventory/infra"
	orderapp "github.com/xrodriguezb/fulcrum/internal/order/app"
	orderinfra "github.com/xrodriguezb/fulcrum/internal/order/infra"
	outboxapp "github.com/xrodriguezb/fulcrum/internal/outbox/app"
	outboxdomain "github.com/xrodriguezb/fulcrum/internal/outbox/domain"
	outboxinfra "github.com/xrodriguezb/fulcrum/internal/outbox/infra"
	"github.com/xrodriguezb/fulcrum/internal/platform/errs"
	"github.com/xrodriguezb/fulcrum/internal/platform/postgres"
)

const testCustomer = "11111111-2222-4333-8444-555555555555"

type sequentialIDs struct {
	mu   sync.Mutex
	next int
}

func (s *sequentialIDs) NewID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	// Deterministic identifiers make a failing test readable.
	return uuidFromCounter(s.next)
}

func uuidFromCounter(n int) string {
	const hex = "0123456789abcdef"
	digits := make([]byte, 12)
	for i := len(digits) - 1; i >= 0; i-- {
		digits[i] = hex[n%16]
		n /= 16
	}
	return "00000000-0000-4000-8000-" + string(digits)
}

type handlerHarness struct {
	pool    *pgxpool.Pool
	manager *postgres.TxManager
	handler *orderapp.CreateOrderHandler
	outbox  *outboxinfra.Writer
}

func newHandler(t *testing.T, options ...func(*orderapp.CreateOrderDeps)) handlerHarness {
	t.Helper()

	pool := newPool(t)
	manager := postgres.NewTxManager(pool)
	writer := outboxinfra.NewWriter(manager)

	deps := orderapp.CreateOrderDeps{
		Tx:           manager,
		Orders:       orderinfra.NewRepository(manager),
		Inventory:    inventoryinfra.NewReserver(manager),
		Outbox:       writer,
		Idempotency:  idempotencyinfra.NewStore(manager),
		Clock:        time.Now,
		IDs:          &sequentialIDs{},
		KeyTTL:       time.Hour,
		MaxKeyLength: 255,
	}
	for _, option := range options {
		option(&deps)
	}

	handler, err := orderapp.NewCreateOrderHandler(deps)
	if err != nil {
		t.Fatalf("build handler: %v", err)
	}
	return handlerHarness{pool: pool, manager: manager, handler: handler, outbox: writer}
}

func command(key string, lines ...orderapp.CommandLine) orderapp.CreateOrderCommand {
	body, _ := json.Marshal(map[string]any{"customer_id": testCustomer, "lines": lines})
	return orderapp.CreateOrderCommand{
		IdempotencyKey: key,
		RawBody:        body,
		CustomerID:     testCustomer,
		Lines:          lines,
		CorrelationID:  "corr-" + key,
		TraceID:        "4bf92f3577b34da6a3ce929d0e0e4736",
	}
}

func countRows(t *testing.T, pool *pgxpool.Pool, table string) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM "+table).Scan(&count); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return count
}

func TestCreateOrderReservesPersistsAndRecordsTheEvent(t *testing.T) {
	h := newHandler(t)
	seedInventory(t, h.pool, "WIDGET-001", 5, 1050)
	seedInventory(t, h.pool, "GADGET-002", 5, 200)

	result, err := h.handler.Handle(t.Context(), command("key-create",
		orderapp.CommandLine{SKU: "GADGET-002", Quantity: 3},
		orderapp.CommandLine{SKU: "WIDGET-001", Quantity: 2},
	))
	if err != nil {
		t.Fatalf("Handle returned %v", err)
	}
	if result.Status != 201 {
		t.Errorf("status = %d, want 201", result.Status)
	}
	if result.Replayed {
		t.Errorf("a first request is not a replay")
	}
	if result.View.TotalCents != 2*1050+3*200 {
		t.Errorf("total = %d, want %d", result.View.TotalCents, 2*1050+3*200)
	}
	if len(result.View.Lines) != 2 || result.View.Lines[0].SKU != "GADGET-002" {
		t.Errorf("lines are not sorted by sku: %+v", result.View.Lines)
	}
	// Prices come from inventory, never from the caller.
	if result.View.Lines[1].UnitPriceCents != 1050 {
		t.Errorf("unit price = %d, want the inventory price 1050", result.View.Lines[1].UnitPriceCents)
	}

	available, reserved, _ := readStock(t, h.pool, "WIDGET-001")
	if available != 3 || reserved != 2 {
		t.Errorf("stock after reservation: available %d reserved %d, want 3 and 2", available, reserved)
	}

	if got := countRows(t, h.pool, "orders"); got != 1 {
		t.Errorf("orders = %d, want 1", got)
	}
	if got := countRows(t, h.pool, "order_lines"); got != 2 {
		t.Errorf("order lines = %d, want 2", got)
	}

	var (
		eventType     string
		aggregateID   string
		correlationID string
		payload       []byte
	)
	const query = `SELECT event_type, aggregate_id::text, correlation_id, payload FROM outbox_events`
	if err := h.pool.QueryRow(t.Context(), query).Scan(&eventType, &aggregateID, &correlationID, &payload); err != nil {
		t.Fatalf("read outbox: %v", err)
	}
	if eventType != "order.created" {
		t.Errorf("event type = %q, want order.created", eventType)
	}
	if aggregateID != result.View.ID {
		t.Errorf("aggregate id = %q, want the order id %q", aggregateID, result.View.ID)
	}
	if correlationID != "corr-key-create" {
		t.Errorf("correlation id = %q, want the one from the command", correlationID)
	}
	if len(payload) == 0 {
		t.Errorf("the event carries no payload")
	}
}

// Every row of the idempotency behaviour matrix.
func TestIdempotencyMatrix(t *testing.T) {
	t.Run("duplicate with the same fingerprint replays the stored response", func(t *testing.T) {
		h := newHandler(t)
		seedInventory(t, h.pool, "WIDGET-001", 5, 1000)

		first, err := h.handler.Handle(t.Context(), command("key-replay",
			orderapp.CommandLine{SKU: "WIDGET-001", Quantity: 1}))
		if err != nil {
			t.Fatalf("first request: %v", err)
		}

		second, err := h.handler.Handle(t.Context(), command("key-replay",
			orderapp.CommandLine{SKU: "WIDGET-001", Quantity: 1}))
		if err != nil {
			t.Fatalf("duplicate request: %v", err)
		}
		if !second.Replayed {
			t.Errorf("the duplicate must be reported as a replay")
		}
		if string(second.Body) != string(first.Body) {
			t.Errorf("the replayed body differs from the original:\n%s\n%s", second.Body, first.Body)
		}
		if got := countRows(t, h.pool, "orders"); got != 1 {
			t.Errorf("orders = %d, want 1: the duplicate created a second order", got)
		}
		available, _, _ := readStock(t, h.pool, "WIDGET-001")
		if available != 4 {
			t.Errorf("available = %d, want 4: the duplicate reserved again", available)
		}
	})

	t.Run("duplicate with a different fingerprint is rejected", func(t *testing.T) {
		h := newHandler(t)
		seedInventory(t, h.pool, "WIDGET-001", 5, 1000)

		if _, err := h.handler.Handle(t.Context(), command("key-reuse",
			orderapp.CommandLine{SKU: "WIDGET-001", Quantity: 1})); err != nil {
			t.Fatalf("first request: %v", err)
		}

		_, err := h.handler.Handle(t.Context(), command("key-reuse",
			orderapp.CommandLine{SKU: "WIDGET-001", Quantity: 2}))
		if errs.CodeOf(err) != errs.CodeIdempotencyKeyReuse {
			t.Errorf("code = %q, want %q (%v)", errs.CodeOf(err), errs.CodeIdempotencyKeyReuse, err)
		}
		if errs.HTTPStatus(errs.KindOf(err)) != 422 {
			t.Errorf("status = %d, want 422", errs.HTTPStatus(errs.KindOf(err)))
		}
	})

	t.Run("duplicate while the first is in flight is refused", func(t *testing.T) {
		h := newHandler(t)
		seedInventory(t, h.pool, "WIDGET-001", 5, 1000)

		store := idempotencyinfra.NewStore(h.manager)
		if _, err := store.Claim(t.Context(), "key-inflight", mustFingerprint(t, command("key-inflight",
			orderapp.CommandLine{SKU: "WIDGET-001", Quantity: 1})), time.Now().Add(time.Hour)); err != nil {
			t.Fatalf("pre-claim: %v", err)
		}

		_, err := h.handler.Handle(t.Context(), command("key-inflight",
			orderapp.CommandLine{SKU: "WIDGET-001", Quantity: 1}))
		if errs.CodeOf(err) != errs.CodeIdempotencyInProgress {
			t.Errorf("code = %q, want %q (%v)", errs.CodeOf(err), errs.CodeIdempotencyInProgress, err)
		}
		if errs.HTTPStatus(errs.KindOf(err)) != 409 {
			t.Errorf("status = %d, want 409", errs.HTTPStatus(errs.KindOf(err)))
		}
	})

	t.Run("a failed attempt can be retried", func(t *testing.T) {
		failing := &failingOutbox{fail: true}
		h := newHandler(t, func(d *orderapp.CreateOrderDeps) {
			failing.inner = d.Outbox
			d.Outbox = failing
		})
		seedInventory(t, h.pool, "WIDGET-001", 5, 1000)

		if _, err := h.handler.Handle(t.Context(), command("key-retry",
			orderapp.CommandLine{SKU: "WIDGET-001", Quantity: 1})); err == nil {
			t.Fatalf("the first attempt was supposed to fail")
		}

		failing.fail = false
		result, err := h.handler.Handle(t.Context(), command("key-retry",
			orderapp.CommandLine{SKU: "WIDGET-001", Quantity: 1}))
		if err != nil {
			t.Fatalf("the retry after a failed attempt was refused: %v", err)
		}
		if result.Replayed {
			t.Errorf("a retry after a failure is a new attempt, not a replay")
		}
		if got := countRows(t, h.pool, "orders"); got != 1 {
			t.Errorf("orders = %d, want 1", got)
		}
	})

	t.Run("an expired key is treated as a first request", func(t *testing.T) {
		h := newHandler(t, func(d *orderapp.CreateOrderDeps) { d.KeyTTL = -time.Minute })
		seedInventory(t, h.pool, "WIDGET-001", 5, 1000)

		if _, err := h.handler.Handle(t.Context(), command("key-expired",
			orderapp.CommandLine{SKU: "WIDGET-001", Quantity: 1})); err != nil {
			t.Fatalf("first request: %v", err)
		}
		second, err := h.handler.Handle(t.Context(), command("key-expired",
			orderapp.CommandLine{SKU: "WIDGET-001", Quantity: 1}))
		if err != nil {
			t.Fatalf("request with an expired key: %v", err)
		}
		if second.Replayed {
			t.Errorf("an expired key must not replay")
		}
		if got := countRows(t, h.pool, "orders"); got != 2 {
			t.Errorf("orders = %d, want 2", got)
		}
	})

	t.Run("a missing or malformed key is rejected", func(t *testing.T) {
		h := newHandler(t)
		seedInventory(t, h.pool, "WIDGET-001", 5, 1000)

		_, err := h.handler.Handle(t.Context(), command("",
			orderapp.CommandLine{SKU: "WIDGET-001", Quantity: 1}))
		if errs.CodeOf(err) != errs.CodeIdempotencyKeyRequired {
			t.Errorf("code = %q, want %q (%v)", errs.CodeOf(err), errs.CodeIdempotencyKeyRequired, err)
		}

		long := make([]byte, 300)
		for i := range long {
			long[i] = 'k'
		}
		_, err = h.handler.Handle(t.Context(), command(string(long),
			orderapp.CommandLine{SKU: "WIDGET-001", Quantity: 1}))
		if errs.HTTPStatus(errs.KindOf(err)) != 400 {
			t.Errorf("status = %d, want 400 (%v)", errs.HTTPStatus(errs.KindOf(err)), err)
		}
	})
}

// The row that cannot be argued from first principles. Two identical requests
// race; exactly one order may exist afterwards.
func TestConcurrentDuplicatesCreateExactlyOneOrder(t *testing.T) {
	h := newHandler(t)
	seedInventory(t, h.pool, "WIDGET-001", 50, 1000)

	const attempts = 16
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		created  int
		replayed int
		refused  int
		other    []error
		start    = make(chan struct{})
	)

	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			result, err := h.handler.Handle(t.Context(), command("key-race",
				orderapp.CommandLine{SKU: "WIDGET-001", Quantity: 1}))

			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil && result.Replayed:
				replayed++
			case err == nil:
				created++
			case errs.CodeOf(err) == errs.CodeIdempotencyInProgress:
				refused++
			default:
				other = append(other, err)
			}
		}()
	}
	close(start)
	wg.Wait()

	if len(other) > 0 {
		t.Fatalf("unexpected error, first of %d: %v", len(other), other[0])
	}
	if created != 1 {
		t.Errorf("created = %d, want exactly 1", created)
	}
	if replayed+refused != attempts-1 {
		t.Errorf("replayed %d plus refused %d, want %d", replayed, refused, attempts-1)
	}
	if got := countRows(t, h.pool, "orders"); got != 1 {
		t.Errorf("orders = %d, want 1", got)
	}
	available, reserved, _ := readStock(t, h.pool, "WIDGET-001")
	if available != 49 || reserved != 1 {
		t.Errorf("stock: available %d reserved %d, want 49 and 1", available, reserved)
	}
}

// If anything after the reservation fails, the reservation has to go with it.
func TestFailureAfterReservationPersistsNothing(t *testing.T) {
	failing := &failingOutbox{fail: true}
	h := newHandler(t, func(d *orderapp.CreateOrderDeps) {
		failing.inner = d.Outbox
		d.Outbox = failing
	})
	seedInventory(t, h.pool, "WIDGET-001", 5, 1000)

	_, err := h.handler.Handle(t.Context(), command("key-rollback",
		orderapp.CommandLine{SKU: "WIDGET-001", Quantity: 2}))
	if err == nil {
		t.Fatalf("the attempt was supposed to fail")
	}

	available, reserved, _ := readStock(t, h.pool, "WIDGET-001")
	if available != 5 || reserved != 0 {
		t.Errorf("the reservation survived the rollback: available %d reserved %d", available, reserved)
	}
	if got := countRows(t, h.pool, "orders"); got != 0 {
		t.Errorf("orders = %d, want 0", got)
	}
	if got := countRows(t, h.pool, "outbox_events"); got != 0 {
		t.Errorf("outbox events = %d, want 0", got)
	}
}

// The outbox row must be written by the same transaction that persists the
// order, which is what makes "the order exists but the event does not"
// unreachable.
func TestOutboxRowSharesTheBusinessTransaction(t *testing.T) {
	h := newHandler(t)
	seedInventory(t, h.pool, "WIDGET-001", 5, 1000)

	err := h.manager.WithinTx(t.Context(), func(txCtx context.Context) error {
		if appendErr := h.outbox.Append(txCtx, outboxdomain.Envelope{
			ID:            uuidFromCounter(999),
			AggregateID:   uuidFromCounter(998),
			AggregateType: "order",
			EventType:     "order.created",
			EventVersion:  1,
			Payload:       json.RawMessage(`{"probe":true}`),
			CorrelationID: "corr-probe",
			OccurredAt:    time.Now(),
		}); appendErr != nil {
			return appendErr
		}
		return errAbort
	})
	if !errors.Is(err, errAbort) {
		t.Fatalf("WithinTx error = %v, want the deliberate abort", err)
	}

	if got := countRows(t, h.pool, "outbox_events"); got != 0 {
		t.Errorf("outbox events = %d after an aborted transaction, want 0", got)
	}
}

var errAbort = errors.New("deliberate abort")

// failingOutbox lets a test fail the write that happens after the reservation.
type failingOutbox struct {
	inner outboxapp.Writer
	fail  bool
}

func (f *failingOutbox) Append(ctx context.Context, envelopes ...outboxdomain.Envelope) error {
	if f.fail {
		return errors.New("the outbox is unavailable")
	}
	return f.inner.Append(ctx, envelopes...)
}

func mustFingerprint(t *testing.T, cmd orderapp.CreateOrderCommand) []byte {
	t.Helper()
	fingerprint, err := orderapp.FingerprintOf(cmd)
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	return fingerprint
}

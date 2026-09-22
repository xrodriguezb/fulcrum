package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/xrodriguezb/fulcrum/internal/order/domain"
)

const (
	testOrderID    = "0b7c6f4e-9d3a-4f1b-8c2d-5a6e7f8b9c0d"
	testCustomerID = "11111111-2222-4333-8444-555555555555"
)

var testTime = time.Date(2026, time.September, 21, 10, 0, 0, 0, time.UTC)

func mustSKU(t *testing.T, raw string) domain.SKU {
	t.Helper()
	sku, err := domain.NewSKU(raw)
	if err != nil {
		t.Fatalf("NewSKU(%q) returned %v", raw, err)
	}
	return sku
}

func mustLine(t *testing.T, sku string, quantity int, cents int64) domain.Line {
	t.Helper()
	q, err := domain.NewQuantity(quantity)
	if err != nil {
		t.Fatalf("NewQuantity(%d) returned %v", quantity, err)
	}
	price, err := domain.NewMoney(cents, "EUR")
	if err != nil {
		t.Fatalf("NewMoney returned %v", err)
	}
	line, err := domain.NewLine(mustSKU(t, sku), q, price)
	if err != nil {
		t.Fatalf("NewLine returned %v", err)
	}
	return line
}

func mustOrder(t *testing.T, lines ...domain.Line) *domain.Order {
	t.Helper()
	id, err := domain.NewOrderID(testOrderID)
	if err != nil {
		t.Fatalf("NewOrderID returned %v", err)
	}
	customer, err := domain.NewCustomerID(testCustomerID)
	if err != nil {
		t.Fatalf("NewCustomerID returned %v", err)
	}
	order, err := domain.NewOrder(id, customer, lines, testTime)
	if err != nil {
		t.Fatalf("NewOrder returned %v", err)
	}
	return order
}

func TestNewOrderComputesTheTotal(t *testing.T) {
	t.Parallel()

	order := mustOrder(t,
		mustLine(t, "WIDGET-001", 2, 1050),
		mustLine(t, "GADGET-002", 3, 200),
	)

	if got := order.Total().Cents(); got != 2*1050+3*200 {
		t.Errorf("Total = %d cents, want %d", got, 2*1050+3*200)
	}
	if order.Total().Currency() != "EUR" {
		t.Errorf("Total currency = %q, want EUR", order.Total().Currency())
	}
	if order.Status() != domain.StatusPending {
		t.Errorf("a new order is %v, want pending", order.Status())
	}
	if !order.CreatedAt().Equal(testTime) {
		t.Errorf("CreatedAt = %v, want %v", order.CreatedAt(), testTime)
	}
}

// Reserving in SKU order is what prevents two orders touching the same pair of
// items in opposite sequence from deadlocking. The aggregate owns that ordering
// so no caller can forget it.
func TestNewOrderSortsLinesBySKU(t *testing.T) {
	t.Parallel()

	order := mustOrder(t,
		mustLine(t, "ZETA-001", 1, 100),
		mustLine(t, "ALPHA-001", 1, 100),
		mustLine(t, "MIKE-001", 1, 100),
	)

	want := []string{"ALPHA-001", "MIKE-001", "ZETA-001"}
	lines := order.Lines()
	if len(lines) != len(want) {
		t.Fatalf("Lines() returned %d lines, want %d", len(lines), len(want))
	}
	for i, sku := range want {
		if lines[i].SKU().String() != sku {
			t.Errorf("Lines()[%d] = %q, want %q", i, lines[i].SKU().String(), sku)
		}
	}
}

func TestNewOrderRejectsBrokenInput(t *testing.T) {
	t.Parallel()

	id, err := domain.NewOrderID(testOrderID)
	if err != nil {
		t.Fatalf("NewOrderID returned %v", err)
	}
	customer, err := domain.NewCustomerID(testCustomerID)
	if err != nil {
		t.Fatalf("NewCustomerID returned %v", err)
	}

	tooMany := make([]domain.Line, 0, domain.MaxOrderLines+1)
	for i := 0; i <= domain.MaxOrderLines; i++ {
		sku := "SKU-" + string(rune('A'+i%26)) + string(rune('A'+i/26))
		tooMany = append(tooMany, mustLine(t, sku, 1, 100))
	}

	usdLine := func() domain.Line {
		q, qErr := domain.NewQuantity(1)
		if qErr != nil {
			t.Fatalf("NewQuantity returned %v", qErr)
		}
		price, pErr := domain.NewMoney(100, "USD")
		if pErr != nil {
			t.Fatalf("NewMoney returned %v", pErr)
		}
		line, lErr := domain.NewLine(mustSKU(t, "DOLLAR-001"), q, price)
		if lErr != nil {
			t.Fatalf("NewLine returned %v", lErr)
		}
		return line
	}()

	cases := []struct {
		name    string
		lines   []domain.Line
		wantErr error
	}{
		{name: "no lines", lines: nil, wantErr: domain.ErrNoLines},
		{name: "too many lines", lines: tooMany, wantErr: domain.ErrTooManyLines},
		{
			name:    "duplicate sku",
			lines:   []domain.Line{mustLine(t, "WIDGET-001", 1, 100), mustLine(t, "widget-001", 2, 100)},
			wantErr: domain.ErrDuplicateSKU,
		},
		{
			name:    "mixed currencies",
			lines:   []domain.Line{mustLine(t, "WIDGET-001", 1, 100), usdLine},
			wantErr: domain.ErrCurrencyMismatch,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := domain.NewOrder(id, customer, tc.lines, testTime)
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("NewOrder error = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestNewOrderEmitsOrderCreated(t *testing.T) {
	t.Parallel()

	order := mustOrder(t, mustLine(t, "WIDGET-001", 2, 1050))

	events := order.PullEvents()
	if len(events) != 1 {
		t.Fatalf("PullEvents returned %d events, want 1", len(events))
	}

	created, ok := events[0].(domain.OrderCreated)
	if !ok {
		t.Fatalf("event is %T, want domain.OrderCreated", events[0])
	}
	if created.EventType() != "order.created" {
		t.Errorf("EventType = %q, want order.created", created.EventType())
	}
	if created.AggregateID() != testOrderID {
		t.Errorf("AggregateID = %q, want %q", created.AggregateID(), testOrderID)
	}
	if !created.OccurredAt().Equal(testTime) {
		t.Errorf("OccurredAt = %v, want %v", created.OccurredAt(), testTime)
	}
	if len(created.Lines) != 1 || created.Lines[0].SKU != "WIDGET-001" {
		t.Errorf("the event must carry the order lines, got %+v", created.Lines)
	}

	if remaining := order.PullEvents(); len(remaining) != 0 {
		t.Errorf("PullEvents must drain the buffer, got %d events on the second call", len(remaining))
	}
}

func TestStateMachine(t *testing.T) {
	t.Parallel()

	t.Run("pending to confirmed", func(t *testing.T) {
		t.Parallel()
		order := mustOrder(t, mustLine(t, "WIDGET-001", 1, 100))
		order.PullEvents()

		if err := order.Confirm(testTime.Add(time.Minute)); err != nil {
			t.Fatalf("Confirm returned %v", err)
		}
		if order.Status() != domain.StatusConfirmed {
			t.Errorf("status = %v, want confirmed", order.Status())
		}
		events := order.PullEvents()
		if len(events) != 1 {
			t.Fatalf("Confirm emitted %d events, want 1", len(events))
		}
		if events[0].EventType() != "order.confirmed" {
			t.Errorf("event type = %q, want order.confirmed", events[0].EventType())
		}
	})

	t.Run("confirming twice is reported separately from an illegal move", func(t *testing.T) {
		t.Parallel()
		order := mustOrder(t, mustLine(t, "WIDGET-001", 1, 100))
		if err := order.Confirm(testTime); err != nil {
			t.Fatalf("Confirm returned %v", err)
		}
		err := order.Confirm(testTime)
		if !errors.Is(err, domain.ErrAlreadyConfirmed) {
			t.Fatalf("second Confirm error = %v, want ErrAlreadyConfirmed", err)
		}
		if errors.Is(err, domain.ErrInvalidTransition) {
			t.Errorf("a redelivered confirmation must not look like an illegal transition")
		}
	})

	t.Run("cancel from pending and from confirmed", func(t *testing.T) {
		t.Parallel()
		for _, confirmFirst := range []bool{false, true} {
			order := mustOrder(t, mustLine(t, "WIDGET-001", 1, 100))
			if confirmFirst {
				if err := order.Confirm(testTime); err != nil {
					t.Fatalf("Confirm returned %v", err)
				}
			}
			if err := order.Cancel(testTime, "operator request"); err != nil {
				t.Fatalf("Cancel returned %v", err)
			}
			if order.Status() != domain.StatusCancelled {
				t.Errorf("status = %v, want cancelled", order.Status())
			}
		}
	})

	t.Run("a cancelled order cannot be confirmed", func(t *testing.T) {
		t.Parallel()
		order := mustOrder(t, mustLine(t, "WIDGET-001", 1, 100))
		if err := order.Cancel(testTime, "operator request"); err != nil {
			t.Fatalf("Cancel returned %v", err)
		}
		if err := order.Confirm(testTime); !errors.Is(err, domain.ErrInvalidTransition) {
			t.Errorf("Confirm on a cancelled order error = %v, want ErrInvalidTransition", err)
		}
		if err := order.Cancel(testTime, "again"); !errors.Is(err, domain.ErrAlreadyCancelled) {
			t.Errorf("second Cancel error = %v, want ErrAlreadyCancelled", err)
		}
	})
}

// The aggregate must not hand out anything a caller can mutate, or its
// invariants hold only until someone edits a slice.
func TestLinesAreNotAliased(t *testing.T) {
	t.Parallel()

	order := mustOrder(t, mustLine(t, "WIDGET-001", 1, 100), mustLine(t, "GADGET-002", 1, 100))

	lines := order.Lines()
	lines[0] = lines[1]

	if order.Lines()[0].SKU().String() != "GADGET-002" {
		t.Errorf("the aggregate exposed its own slice, mutation leaked back in")
	}
}

// Rehydration is how a repository rebuilds an order that already exists. It must
// not emit events: those were published when the order was first created.
func TestRehydrateDoesNotEmitEvents(t *testing.T) {
	t.Parallel()

	id, err := domain.NewOrderID(testOrderID)
	if err != nil {
		t.Fatalf("NewOrderID returned %v", err)
	}
	customer, err := domain.NewCustomerID(testCustomerID)
	if err != nil {
		t.Fatalf("NewCustomerID returned %v", err)
	}
	total, err := domain.NewMoney(100, "EUR")
	if err != nil {
		t.Fatalf("NewMoney returned %v", err)
	}

	order, err := domain.Rehydrate(domain.State{
		ID:         id,
		CustomerID: customer,
		Status:     domain.StatusConfirmed,
		Lines:      []domain.Line{mustLine(t, "WIDGET-001", 1, 100)},
		Total:      total,
		CreatedAt:  testTime,
		UpdatedAt:  testTime.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("Rehydrate returned %v", err)
	}
	if len(order.PullEvents()) != 0 {
		t.Errorf("rehydration must not emit events")
	}
	if order.Status() != domain.StatusConfirmed {
		t.Errorf("status = %v, want confirmed", order.Status())
	}
	if _, err := domain.Rehydrate(domain.State{ID: id}); err == nil {
		t.Errorf("rehydrating from incomplete state must fail")
	}
}

func TestStatusParsing(t *testing.T) {
	t.Parallel()

	for _, status := range []domain.Status{domain.StatusPending, domain.StatusConfirmed, domain.StatusCancelled} {
		parsed, err := domain.ParseStatus(status.String())
		if err != nil {
			t.Fatalf("ParseStatus(%q) returned %v", status.String(), err)
		}
		if parsed != status {
			t.Errorf("ParseStatus(%q) = %v, want %v", status.String(), parsed, status)
		}
	}
	if _, err := domain.ParseStatus("shipped"); err == nil {
		t.Errorf("ParseStatus must reject a status the aggregate does not model")
	}
}

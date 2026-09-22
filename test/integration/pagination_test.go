//go:build integration

package integration

import (
	"testing"

	opsinfra "github.com/xrodriguezb/fulcrum/internal/ops/infra"
	orderapp "github.com/xrodriguezb/fulcrum/internal/order/app"
	orderinfra "github.com/xrodriguezb/fulcrum/internal/order/infra"
)

// The reported total is the size of the collection, not the size of the page.
//
// A console that has paged past the end still has to render the pager, and a
// total of zero tells it the collection is empty. The count therefore cannot be
// carried on the rows of the page, because a page beyond the end has none.
func TestPageReportsTheCollectionTotalBeyondTheLastPage(t *testing.T) {
	h := newHandler(t)
	seedInventory(t, h.pool, "WIDGET-001", 10, 1000)

	for _, key := range []string{"page-1", "page-2", "page-3"} {
		if _, err := h.handler.Handle(t.Context(), command(key,
			orderapp.CommandLine{SKU: "WIDGET-001", Quantity: 1})); err != nil {
			t.Fatalf("create order %s: %v", key, err)
		}
	}

	repository := orderinfra.NewRepository(h.manager)

	orders, total, err := repository.Page(t.Context(), 2, 0)
	if err != nil {
		t.Fatalf("read the first page: %v", err)
	}
	if len(orders) != 2 {
		t.Errorf("first page held %d orders, want 2", len(orders))
	}
	if total != 3 {
		t.Errorf("total on the first page = %d, want 3", total)
	}

	orders, total, err = repository.Page(t.Context(), 2, 50)
	if err != nil {
		t.Fatalf("read a page past the end: %v", err)
	}
	if len(orders) != 0 {
		t.Errorf("a page past the end held %d orders, want none", len(orders))
	}
	if total != 3 {
		t.Errorf("total past the last page = %d, want 3: the collection is not empty", total)
	}
}

// The dead letter listing reports the collection total for the same reason the
// order listing does: the console pages it and needs a pager it can trust.
func TestDeadLetterPageReportsTheCollectionTotalBeyondTheLastPage(t *testing.T) {
	h := newHandler(t)

	const insert = `
INSERT INTO dead_letter_events
  (id, event_id, consumer_name, event_type, payload, attempts, first_failed_at, last_failed_at, failure_reason)
VALUES (gen_random_uuid(), gen_random_uuid(), 'audit-consumer', 'order.created', '{}', 1, now(), now(), 'seeded for the pager test')`
	for range 3 {
		if _, err := h.pool.Exec(t.Context(), insert); err != nil {
			t.Fatalf("seed a dead letter: %v", err)
		}
	}

	reader := opsinfra.NewReader(h.manager)

	entries, total, err := reader.DeadLetters(t.Context(), 2, 0)
	if err != nil {
		t.Fatalf("read the first page: %v", err)
	}
	if len(entries) != 2 || total != 3 {
		t.Errorf("first page held %d entries with total %d, want 2 and 3", len(entries), total)
	}

	entries, total, err = reader.DeadLetters(t.Context(), 2, 50)
	if err != nil {
		t.Fatalf("read a page past the end: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("a page past the end held %d entries, want none", len(entries))
	}
	if total != 3 {
		t.Errorf("total past the last page = %d, want 3", total)
	}
}

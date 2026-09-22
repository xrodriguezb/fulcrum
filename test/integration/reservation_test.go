//go:build integration

package integration

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	inventoryapp "github.com/xrodriguezb/fulcrum/internal/inventory/app"
	inventoryinfra "github.com/xrodriguezb/fulcrum/internal/inventory/infra"
	"github.com/xrodriguezb/fulcrum/internal/platform/errs"
	"github.com/xrodriguezb/fulcrum/internal/platform/postgres"
)

// seedInventory inserts one stock position.
func seedInventory(t *testing.T, pool *pgxpool.Pool, sku string, available int, unitPriceCents int64) {
	t.Helper()

	const insert = `
INSERT INTO inventory_items (sku, available, reserved, unit_price_cents, currency, version)
VALUES ($1, $2, 0, $3, 'EUR', 1)
ON CONFLICT (sku) DO UPDATE SET available = EXCLUDED.available, reserved = 0, version = 1`
	if _, err := pool.Exec(t.Context(), insert, sku, available, unitPriceCents); err != nil {
		t.Fatalf("seed inventory: %v", err)
	}
}

func readStock(t *testing.T, pool *pgxpool.Pool, sku string) (available, reserved, version int) {
	t.Helper()

	const query = `SELECT available, reserved, version FROM inventory_items WHERE sku = $1`
	if err := pool.QueryRow(t.Context(), query, sku).Scan(&available, &reserved, &version); err != nil {
		t.Fatalf("read stock for %s: %v", sku, err)
	}
	return available, reserved, version
}

// TestReservationCannotOversell is the claim the whole repository exists to
// prove. Five units, two hundred concurrent buyers, every one of them asking for
// one unit. Exactly five may win.
func TestReservationCannotOversell(t *testing.T) {
	const (
		sku         = "WIDGET-001"
		stock       = 5
		contenders  = 200
		perAttempt  = 1
		wantSuccess = stock / perAttempt
	)

	pool := newPool(t)
	seedInventory(t, pool, sku, stock, 1000)

	manager := postgres.NewTxManager(pool)
	reserver := inventoryinfra.NewReserver(manager)

	var (
		start     = make(chan struct{})
		wg        sync.WaitGroup
		mu        sync.Mutex
		successes int
		conflicts int
		other     []error
	)

	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// The barrier is what makes this a contention test rather than two
			// hundred sequential reservations.
			<-start

			err := manager.WithinTx(t.Context(), func(txCtx context.Context) error {
				_, reserveErr := reserver.Reserve(txCtx, []inventoryapp.ReservationRequest{
					{SKU: sku, Quantity: perAttempt},
				})
				return reserveErr
			})

			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				successes++
			case errs.CodeOf(err) == errs.CodeInventoryInsufficient:
				conflicts++
			default:
				other = append(other, err)
			}
		}()
	}

	close(start)
	wg.Wait()

	if len(other) > 0 {
		t.Fatalf("unexpected errors during contention, first of %d: %v", len(other), other[0])
	}
	if successes != wantSuccess {
		t.Errorf("successes = %d, want %d", successes, wantSuccess)
	}
	if conflicts != contenders-wantSuccess {
		t.Errorf("conflicts = %d, want %d", conflicts, contenders-wantSuccess)
	}

	available, reserved, _ := readStock(t, pool, sku)
	if available != 0 {
		t.Errorf("available = %d, want 0", available)
	}
	if reserved != stock {
		t.Errorf("reserved = %d, want %d", reserved, stock)
	}
	if available+reserved != stock {
		t.Errorf("units were created or destroyed: available %d plus reserved %d, want %d", available, reserved, stock)
	}
}

// A reservation that cannot be satisfied must change nothing, including the
// version counter, so an operator reading the row cannot mistake a refusal for
// an update.
func TestRefusedReservationLeavesStockUntouched(t *testing.T) {
	pool := newPool(t)
	seedInventory(t, pool, "SCARCE-001", 2, 500)

	manager := postgres.NewTxManager(pool)
	reserver := inventoryinfra.NewReserver(manager)

	_, before, versionBefore := readStock(t, pool, "SCARCE-001")

	err := manager.WithinTx(t.Context(), func(txCtx context.Context) error {
		_, reserveErr := reserver.Reserve(txCtx, []inventoryapp.ReservationRequest{
			{SKU: "SCARCE-001", Quantity: 3},
		})
		return reserveErr
	})
	if errs.CodeOf(err) != errs.CodeInventoryInsufficient {
		t.Fatalf("error code = %q, want %q (%v)", errs.CodeOf(err), errs.CodeInventoryInsufficient, err)
	}

	available, reserved, version := readStock(t, pool, "SCARCE-001")
	if available != 2 || reserved != before || version != versionBefore {
		t.Errorf("a refused reservation changed the row: available %d reserved %d version %d",
			available, reserved, version)
	}
}

// A multi-line reservation is all or nothing: if the second line cannot be
// satisfied, the first must not stay reserved.
func TestPartialReservationIsRolledBack(t *testing.T) {
	pool := newPool(t)
	seedInventory(t, pool, "PLENTY-001", 10, 100)
	seedInventory(t, pool, "SCARCE-002", 1, 100)

	manager := postgres.NewTxManager(pool)
	reserver := inventoryinfra.NewReserver(manager)

	err := manager.WithinTx(t.Context(), func(txCtx context.Context) error {
		_, reserveErr := reserver.Reserve(txCtx, []inventoryapp.ReservationRequest{
			{SKU: "PLENTY-001", Quantity: 2},
			{SKU: "SCARCE-002", Quantity: 5},
		})
		return reserveErr
	})
	if errs.CodeOf(err) != errs.CodeInventoryInsufficient {
		t.Fatalf("error code = %q, want %q (%v)", errs.CodeOf(err), errs.CodeInventoryInsufficient, err)
	}

	available, reserved, _ := readStock(t, pool, "PLENTY-001")
	if available != 10 || reserved != 0 {
		t.Errorf("the satisfiable line stayed reserved: available %d reserved %d", available, reserved)
	}
}

func TestReservationReportsAnUnknownSKU(t *testing.T) {
	pool := newPool(t)
	manager := postgres.NewTxManager(pool)
	reserver := inventoryinfra.NewReserver(manager)

	err := manager.WithinTx(t.Context(), func(txCtx context.Context) error {
		_, reserveErr := reserver.Reserve(txCtx, []inventoryapp.ReservationRequest{
			{SKU: "GHOST-001", Quantity: 1},
		})
		return reserveErr
	})
	if errs.KindOf(err) != errs.KindNotFound {
		t.Errorf("kind = %v, want not found (%v)", errs.KindOf(err), err)
	}
}

// Two orders that touch the same pair of items in opposite sequence would
// deadlock if each reserved in its own order. The reserver sorts by sku, so this
// runs clean. Without the sort this test is the one that fails, intermittently,
// which is why the ordering lives in code and not in a comment.
func TestConcurrentMultiSKUReservationsDoNotDeadlock(t *testing.T) {
	pool := newPool(t)
	seedInventory(t, pool, "PAIR-AAA", 200, 100)
	seedInventory(t, pool, "PAIR-BBB", 200, 100)

	manager := postgres.NewTxManager(pool)
	reserver := inventoryinfra.NewReserver(manager)

	const rounds = 60
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		failures []error
		start    = make(chan struct{})
	)

	attempt := func(first, second string) {
		defer wg.Done()
		<-start
		err := manager.WithinTx(t.Context(), func(txCtx context.Context) error {
			_, reserveErr := reserver.Reserve(txCtx, []inventoryapp.ReservationRequest{
				{SKU: first, Quantity: 1},
				{SKU: second, Quantity: 1},
			})
			return reserveErr
		})
		if err != nil {
			mu.Lock()
			failures = append(failures, err)
			mu.Unlock()
		}
	}

	for i := 0; i < rounds; i++ {
		wg.Add(2)
		go attempt("PAIR-AAA", "PAIR-BBB")
		go attempt("PAIR-BBB", "PAIR-AAA")
	}
	close(start)
	wg.Wait()

	for _, err := range failures {
		if strings.Contains(strings.ToLower(err.Error()), "deadlock") {
			t.Fatalf("a deadlock was detected, reservation ordering is not holding: %v", err)
		}
		t.Errorf("unexpected reservation failure: %v", err)
	}
}

// The CHECK constraints must never fire in correct operation. This test
// deliberately bypasses the application path to prove the backstop is real.
func TestCheckConstraintRejectsNegativeStock(t *testing.T) {
	pool := newPool(t)
	seedInventory(t, pool, "GUARD-001", 1, 100)

	_, err := pool.Exec(t.Context(), `UPDATE inventory_items SET available = -1 WHERE sku = 'GUARD-001'`)
	if err == nil {
		t.Fatalf("the database accepted negative availability")
	}
	if !strings.Contains(err.Error(), "inventory_available_non_negative") {
		t.Errorf("error does not name the constraint: %v", err)
	}

	_, err = pool.Exec(t.Context(), `UPDATE inventory_items SET reserved = -1 WHERE sku = 'GUARD-001'`)
	if err == nil {
		t.Fatalf("the database accepted negative reserved units")
	}
	if !strings.Contains(err.Error(), "inventory_reserved_non_negative") {
		t.Errorf("error does not name the constraint: %v", err)
	}

	var available, reserved int
	if err := pool.QueryRow(t.Context(),
		`SELECT available, reserved FROM inventory_items WHERE sku = 'GUARD-001'`).Scan(&available, &reserved); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if available != 1 || reserved != 0 {
		t.Errorf("the rejected writes changed the row: available %d reserved %d", available, reserved)
	}
	if !errors.Is(err, nil) {
		t.Errorf("unexpected error state")
	}
}

//go:build integration

package integration

import (
	"context"
	"errors"
	"testing"

	"github.com/xrodriguezb/fulcrum/internal/platform/postgres"
)

var errDeliberate = errors.New("deliberate failure")

// A transaction manager that commits on the error path, or that leaves a
// connection holding an open transaction, is the kind of defect that only shows
// up under load. These cases pin the behaviour down.
func TestTxManagerRollsBackOnError(t *testing.T) {
	pool := newPool(t)
	ctx := t.Context()
	manager := postgres.NewTxManager(pool)

	err := manager.WithinTx(ctx, func(txCtx context.Context) error {
		if _, execErr := manager.Executor(txCtx).Exec(txCtx,
			`INSERT INTO inventory_items (sku, available, reserved, unit_price_cents, currency, version)
			 VALUES ('ROLLBACK-001', 5, 0, 100, 'EUR', 1)`); execErr != nil {
			return execErr
		}
		return errDeliberate
	})

	if !errors.Is(err, errDeliberate) {
		t.Fatalf("WithinTx error = %v, want the deliberate failure", err)
	}

	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM inventory_items WHERE sku = 'ROLLBACK-001'`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Errorf("the failed transaction left %d rows behind", count)
	}
}

func TestTxManagerCommitsOnSuccess(t *testing.T) {
	pool := newPool(t)
	ctx := t.Context()
	manager := postgres.NewTxManager(pool)

	err := manager.WithinTx(ctx, func(txCtx context.Context) error {
		_, execErr := manager.Executor(txCtx).Exec(txCtx,
			`INSERT INTO inventory_items (sku, available, reserved, unit_price_cents, currency, version)
			 VALUES ('COMMIT-001', 5, 0, 100, 'EUR', 1)`)
		return execErr
	})
	if err != nil {
		t.Fatalf("WithinTx returned %v", err)
	}

	var available int
	if err := pool.QueryRow(ctx, `SELECT available FROM inventory_items WHERE sku = 'COMMIT-001'`).Scan(&available); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if available != 5 {
		t.Errorf("available = %d, want 5", available)
	}
}

// A panic must not leave a transaction open on a pooled connection, because the
// connection goes back to the pool and the next caller inherits it.
func TestTxManagerRollsBackOnPanic(t *testing.T) {
	pool := newPool(t)
	ctx := t.Context()
	manager := postgres.NewTxManager(pool)

	func() {
		defer func() {
			if recovered := recover(); recovered == nil {
				t.Errorf("the panic must be propagated to the caller")
			}
		}()

		_ = manager.WithinTx(ctx, func(txCtx context.Context) error {
			if _, execErr := manager.Executor(txCtx).Exec(txCtx,
				`INSERT INTO inventory_items (sku, available, reserved, unit_price_cents, currency, version)
				 VALUES ('PANIC-001', 5, 0, 100, 'EUR', 1)`); execErr != nil {
				t.Errorf("insert: %v", execErr)
			}
			panic("deliberate panic inside a transaction")
		})
	}()

	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM inventory_items WHERE sku = 'PANIC-001'`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Errorf("the panicking transaction left %d rows behind", count)
	}
}

// Nesting happens when a use case that already runs in a transaction calls a
// collaborator that also asks for one. Opening a second transaction on a second
// connection would break atomicity silently, so the inner call joins the outer.
func TestNestedWithinTxJoinsTheOuterTransaction(t *testing.T) {
	pool := newPool(t)
	ctx := t.Context()
	manager := postgres.NewTxManager(pool)

	err := manager.WithinTx(ctx, func(outerCtx context.Context) error {
		if _, execErr := manager.Executor(outerCtx).Exec(outerCtx,
			`INSERT INTO inventory_items (sku, available, reserved, unit_price_cents, currency, version)
			 VALUES ('NESTED-001', 5, 0, 100, 'EUR', 1)`); execErr != nil {
			return execErr
		}
		if innerErr := manager.WithinTx(outerCtx, func(innerCtx context.Context) error {
			_, execErr := manager.Executor(innerCtx).Exec(innerCtx,
				`UPDATE inventory_items SET available = 7 WHERE sku = 'NESTED-001'`)
			return execErr
		}); innerErr != nil {
			return innerErr
		}
		return errDeliberate
	})
	if !errors.Is(err, errDeliberate) {
		t.Fatalf("WithinTx error = %v, want the deliberate failure", err)
	}

	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM inventory_items WHERE sku = 'NESTED-001'`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Errorf("the inner transaction committed independently, %d rows survived the outer rollback", count)
	}
}

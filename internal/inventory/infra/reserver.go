// Package infra implements the inventory ports against PostgreSQL.
package infra

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/xrodriguezb/fulcrum/internal/inventory/app"
	"github.com/xrodriguezb/fulcrum/internal/inventory/domain"
	"github.com/xrodriguezb/fulcrum/internal/platform/errs"
	"github.com/xrodriguezb/fulcrum/internal/platform/postgres"
)

// reserveStatement is the whole concurrency control of this system.
//
// The guard `available >= $2` makes the read and the write one atomic step, so
// two transactions cannot both observe enough stock and then both take it. Zero
// rows affected means the guard rejected the request, which is a business
// outcome and not an error condition. See ADR 0003 for the alternatives.
const reserveStatement = `
UPDATE inventory_items
SET    available  = available - $2,
       reserved   = reserved + $2,
       version    = version + 1,
       updated_at = now()
WHERE  sku = $1
  AND  available >= $2
RETURNING available, reserved, version, unit_price_cents, currency`

// Reserver reserves inventory inside the caller's transaction.
type Reserver struct {
	tx *postgres.TxManager
}

// NewReserver builds a reserver over the transaction manager.
func NewReserver(tx *postgres.TxManager) *Reserver {
	return &Reserver{tx: tx}
}

// Reserve takes every requested quantity or none of them.
//
// Requests are sorted by sku before execution. Two orders that touch the same
// pair of items in opposite sequence would otherwise take row locks in opposite
// order and deadlock, and the database would resolve that by killing one of
// them at random. Sorting turns that into a wait.
func (r *Reserver) Reserve(ctx context.Context, requests []app.ReservationRequest) ([]app.ReservedLine, error) {
	if len(requests) == 0 {
		return nil, errs.Validation(errs.CodeValidationFailed, "A reservation needs at least one line.", nil)
	}

	ordered, err := normalise(requests)
	if err != nil {
		return nil, err
	}

	executor := r.tx.Executor(ctx)
	reserved := make([]app.ReservedLine, 0, len(ordered))

	for _, request := range ordered {
		var line app.ReservedLine
		line.SKU = request.SKU
		line.Quantity = request.Quantity

		row := executor.QueryRow(ctx, reserveStatement, request.SKU, request.Quantity)
		scanErr := row.Scan(&line.Available, &line.Reserved, &line.Version, &line.UnitPriceCents, &line.Currency)
		switch {
		case scanErr == nil:
			reserved = append(reserved, line)
		case errors.Is(scanErr, pgx.ErrNoRows):
			// No row matched. Either the sku does not exist, or it exists and
			// the guard refused. The two are different answers to the caller.
			return nil, r.explainMiss(ctx, request)
		default:
			return nil, errs.Internal("reserve inventory", fmt.Errorf("reserve %s: %w", request.SKU, scanErr))
		}
	}

	return reserved, nil
}

// List returns every stock position, ordered by sku so the console is stable.
func (r *Reserver) List(ctx context.Context) ([]app.Item, error) {
	const query = `
SELECT sku, available, reserved, unit_price_cents, currency, version
FROM   inventory_items
ORDER  BY sku`

	rows, err := r.tx.Executor(ctx).Query(ctx, query)
	if err != nil {
		return nil, errs.Internal("list inventory", err)
	}
	defer rows.Close()

	var items []app.Item
	for rows.Next() {
		var item app.Item
		if scanErr := rows.Scan(&item.SKU, &item.Available, &item.Reserved,
			&item.UnitPriceCents, &item.Currency, &item.Version); scanErr != nil {
			return nil, errs.Internal("scan inventory", scanErr)
		}
		items = append(items, item)
	}
	if rows.Err() != nil {
		return nil, errs.Internal("iterate inventory", rows.Err())
	}
	return items, nil
}

// explainMiss turns zero affected rows into the right domain answer.
func (r *Reserver) explainMiss(ctx context.Context, request app.ReservationRequest) error {
	const exists = `SELECT available FROM inventory_items WHERE sku = $1`

	var available int
	err := r.tx.Executor(ctx).QueryRow(ctx, exists, request.SKU).Scan(&available)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return errs.NotFound(errs.CodeValidationFailed,
			fmt.Sprintf("Unknown sku %s.", request.SKU), domain.ErrInvalidSKU)
	case err != nil:
		return errs.Internal("classify reservation miss", err)
	default:
		return errs.Conflict(errs.CodeInventoryInsufficient,
			"The requested quantity is no longer available.",
			fmt.Errorf("%w: %s has %d available, %d requested",
				domain.ErrInsufficientInventory, request.SKU, available, request.Quantity))
	}
}

// normalise validates every request through the domain types and returns them
// sorted by sku with duplicates rejected.
func normalise(requests []app.ReservationRequest) ([]app.ReservationRequest, error) {
	out := make([]app.ReservationRequest, 0, len(requests))
	seen := make(map[string]struct{}, len(requests))

	for _, request := range requests {
		sku, err := domain.NewSKU(request.SKU)
		if err != nil {
			return nil, errs.Validation(errs.CodeValidationFailed, "The sku is not valid.", err)
		}
		quantity, err := domain.NewQuantity(request.Quantity)
		if err != nil {
			return nil, errs.Validation(errs.CodeValidationFailed, "The quantity is not valid.", err)
		}
		if _, duplicate := seen[sku.String()]; duplicate {
			return nil, errs.Validation(errs.CodeValidationFailed,
				fmt.Sprintf("The sku %s appears more than once.", sku.String()), nil)
		}
		seen[sku.String()] = struct{}{}
		out = append(out, app.ReservationRequest{SKU: sku.String(), Quantity: quantity.Int()})
	}

	slices.SortFunc(out, func(a, b app.ReservationRequest) int {
		return strings.Compare(a.SKU, b.SKU)
	})
	return out, nil
}

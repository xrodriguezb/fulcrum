// Package infra implements the order ports against PostgreSQL.
package infra

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/xrodriguezb/fulcrum/internal/order/domain"
	"github.com/xrodriguezb/fulcrum/internal/platform/errs"
	"github.com/xrodriguezb/fulcrum/internal/platform/postgres"
)

// Repository stores order aggregates.
type Repository struct {
	tx *postgres.TxManager
}

// NewRepository builds a repository over the transaction manager.
func NewRepository(tx *postgres.TxManager) *Repository {
	return &Repository{tx: tx}
}

// Save writes the order and its lines. It uses the caller's executor, so the
// order, its reservation and its outbox event share one transaction.
func (r *Repository) Save(ctx context.Context, order *domain.Order) error {
	const insertOrder = `
INSERT INTO orders (id, customer_id, status, total_cents, currency, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)`

	executor := r.tx.Executor(ctx)
	_, err := executor.Exec(ctx, insertOrder,
		order.ID().String(), order.CustomerID().String(), order.Status().String(),
		order.Total().Cents(), order.Total().Currency(), order.CreatedAt(), order.UpdatedAt())
	if err != nil {
		return errs.Internal("insert order", err)
	}

	const insertLine = `
INSERT INTO order_lines (order_id, sku, quantity, unit_price_cents)
VALUES ($1, $2, $3, $4)`

	// Lines are already sorted by sku by the aggregate, and inserting them in
	// that order keeps the write pattern identical to the reservation order.
	for _, line := range order.Lines() {
		if _, lineErr := executor.Exec(ctx, insertLine,
			order.ID().String(), line.SKU().String(), line.Quantity().Int(), line.UnitPrice().Cents()); lineErr != nil {
			return errs.Internal("insert order line", lineErr)
		}
	}
	return nil
}

// UpdateStatus persists a state transition.
func (r *Repository) UpdateStatus(ctx context.Context, id domain.OrderID, status domain.Status, updatedAt time.Time) error {
	const statement = `UPDATE orders SET status = $2, updated_at = $3 WHERE id = $1`

	tag, err := r.tx.Executor(ctx).Exec(ctx, statement, id.String(), status.String(), updatedAt)
	if err != nil {
		return errs.Internal("update order status", err)
	}
	if tag.RowsAffected() == 0 {
		return errs.NotFound(errs.CodeOrderNotFound, "The order does not exist.",
			fmt.Errorf("order %s not found", id.String()))
	}
	return nil
}

// ByID loads one order with its lines.
func (r *Repository) ByID(ctx context.Context, id domain.OrderID) (*domain.Order, error) {
	const query = `
SELECT id, customer_id, status, total_cents, currency, created_at, updated_at
FROM   orders
WHERE  id = $1`

	executor := r.tx.Executor(ctx)

	var row orderRow
	err := executor.QueryRow(ctx, query, id.String()).Scan(
		&row.id, &row.customerID, &row.status, &row.totalCents, &row.currency, &row.createdAt, &row.updatedAt)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, errs.NotFound(errs.CodeOrderNotFound, "The order does not exist.", err)
	case err != nil:
		return nil, errs.Internal("read order", err)
	}

	lines, err := r.linesFor(ctx, id.String())
	if err != nil {
		return nil, err
	}
	return row.toAggregate(lines)
}

// Page lists orders newest first and reports the total count, which the console
// needs to render pagination.
func (r *Repository) Page(ctx context.Context, limit, offset int) ([]*domain.Order, int, error) {
	const query = `
SELECT id, customer_id, status, total_cents, currency, created_at, updated_at, count(*) OVER () AS total
FROM   orders
ORDER  BY created_at DESC, id DESC
LIMIT  $1 OFFSET $2`

	executor := r.tx.Executor(ctx)
	rows, err := executor.Query(ctx, query, limit, offset)
	if err != nil {
		return nil, 0, errs.Internal("list orders", err)
	}
	defer rows.Close()

	var (
		collected []orderRow
		total     int
	)
	for rows.Next() {
		var row orderRow
		if scanErr := rows.Scan(&row.id, &row.customerID, &row.status, &row.totalCents,
			&row.currency, &row.createdAt, &row.updatedAt, &total); scanErr != nil {
			return nil, 0, errs.Internal("scan order", scanErr)
		}
		collected = append(collected, row)
	}
	if rows.Err() != nil {
		return nil, 0, errs.Internal("iterate orders", rows.Err())
	}

	orders := make([]*domain.Order, 0, len(collected))
	for _, row := range collected {
		lines, linesErr := r.linesFor(ctx, row.id)
		if linesErr != nil {
			return nil, 0, linesErr
		}
		order, buildErr := row.toAggregate(lines)
		if buildErr != nil {
			return nil, 0, buildErr
		}
		orders = append(orders, order)
	}
	return orders, total, nil
}

func (r *Repository) linesFor(ctx context.Context, orderID string) ([]domain.Line, error) {
	const query = `
SELECT sku, quantity, unit_price_cents
FROM   order_lines
WHERE  order_id = $1
ORDER  BY sku`

	rows, err := r.tx.Executor(ctx).Query(ctx, query, orderID)
	if err != nil {
		return nil, errs.Internal("read order lines", err)
	}
	defer rows.Close()

	var lines []domain.Line
	for rows.Next() {
		var (
			rawSKU    string
			quantity  int
			unitCents int64
		)
		if scanErr := rows.Scan(&rawSKU, &quantity, &unitCents); scanErr != nil {
			return nil, errs.Internal("scan order line", scanErr)
		}

		sku, skuErr := domain.NewSKU(rawSKU)
		if skuErr != nil {
			return nil, errs.Internal("stored sku is not valid", skuErr)
		}
		parsedQuantity, quantityErr := domain.NewQuantity(quantity)
		if quantityErr != nil {
			return nil, errs.Internal("stored quantity is not valid", quantityErr)
		}
		price, priceErr := domain.NewMoney(unitCents, "EUR")
		if priceErr != nil {
			return nil, errs.Internal("stored price is not valid", priceErr)
		}
		line, lineErr := domain.NewLine(sku, parsedQuantity, price)
		if lineErr != nil {
			return nil, errs.Internal("stored line is not valid", lineErr)
		}
		lines = append(lines, line)
	}
	if rows.Err() != nil {
		return nil, errs.Internal("iterate order lines", rows.Err())
	}
	return lines, nil
}

// orderRow is the raw shape of a row, kept separate from the aggregate so that
// scanning never has to construct a half-built order.
type orderRow struct {
	id         string
	customerID string
	status     string
	totalCents int64
	currency   string
	createdAt  time.Time
	updatedAt  time.Time
}

func (row orderRow) toAggregate(lines []domain.Line) (*domain.Order, error) {
	id, err := domain.NewOrderID(row.id)
	if err != nil {
		return nil, errs.Internal("stored order id is not valid", err)
	}
	customerID, err := domain.NewCustomerID(row.customerID)
	if err != nil {
		return nil, errs.Internal("stored customer id is not valid", err)
	}
	status, err := domain.ParseStatus(row.status)
	if err != nil {
		return nil, errs.Internal("stored status is not valid", err)
	}
	total, err := domain.NewMoney(row.totalCents, row.currency)
	if err != nil {
		return nil, errs.Internal("stored total is not valid", err)
	}

	order, err := domain.Rehydrate(domain.State{
		ID:         id,
		CustomerID: customerID,
		Status:     status,
		Lines:      lines,
		Total:      total,
		CreatedAt:  row.createdAt,
		UpdatedAt:  row.updatedAt,
	})
	if err != nil {
		return nil, errs.Internal("rehydrate order", err)
	}
	return order, nil
}

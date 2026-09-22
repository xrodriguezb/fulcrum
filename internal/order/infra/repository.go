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
//
// The count is its own statement rather than a window over the page. Carrying it
// on the rows meant a page beyond the last one reported a total of zero, because
// there were no rows to carry it, and it also made every request pay for the
// whole table: a twenty row page over 71237 orders touched 62422 buffers and
// spilled to a temporary file, 32ms, against 9ms and 1071 buffers for the two
// statements. The two reads are not one snapshot, so a concurrent insert can
// leave the total one ahead of the page. For a listing that is already a view of
// a moving table, that is worth the cost it removes.
func (r *Repository) Page(ctx context.Context, limit, offset int) ([]*domain.Order, int, error) {
	const countQuery = `SELECT count(*) FROM orders`
	const query = `
SELECT id, customer_id, status, total_cents, currency, created_at, updated_at
FROM   orders
ORDER  BY created_at DESC, id DESC
LIMIT  $1 OFFSET $2`

	executor := r.tx.Executor(ctx)

	var total int
	if err := executor.QueryRow(ctx, countQuery).Scan(&total); err != nil {
		return nil, 0, errs.Internal("count orders", err)
	}

	rows, err := executor.Query(ctx, query, limit, offset)
	if err != nil {
		return nil, 0, errs.Internal("list orders", err)
	}
	defer rows.Close()

	var collected []orderRow
	for rows.Next() {
		var row orderRow
		if scanErr := rows.Scan(&row.id, &row.customerID, &row.status, &row.totalCents,
			&row.currency, &row.createdAt, &row.updatedAt); scanErr != nil {
			return nil, 0, errs.Internal("scan order", scanErr)
		}
		collected = append(collected, row)
	}
	if rows.Err() != nil {
		return nil, 0, errs.Internal("iterate orders", rows.Err())
	}

	ids := make([]string, 0, len(collected))
	for _, row := range collected {
		ids = append(ids, row.id)
	}

	// One query for every line on the page, rather than one per order. A page of
	// a hundred orders was a hundred and one round trips, which is the kind of
	// cost that only appears once the list has real data in it.
	linesByOrder, err := r.linesForAll(ctx, ids)
	if err != nil {
		return nil, 0, err
	}

	orders := make([]*domain.Order, 0, len(collected))
	for _, row := range collected {
		order, buildErr := row.toAggregate(linesByOrder[row.id])
		if buildErr != nil {
			return nil, 0, buildErr
		}
		orders = append(orders, order)
	}
	return orders, total, nil
}

// linesForAll loads the lines of several orders in one round trip.
func (r *Repository) linesForAll(ctx context.Context, orderIDs []string) (map[string][]domain.Line, error) {
	if len(orderIDs) == 0 {
		return map[string][]domain.Line{}, nil
	}

	const query = `
SELECT order_id::text, sku, quantity, unit_price_cents
FROM   order_lines
WHERE  order_id = ANY($1)
ORDER  BY order_id, sku`

	rows, err := r.tx.Executor(ctx).Query(ctx, query, orderIDs)
	if err != nil {
		return nil, errs.Internal("read order lines", err)
	}
	defer rows.Close()

	byOrder := make(map[string][]domain.Line, len(orderIDs))
	for rows.Next() {
		var (
			orderID   string
			rawSKU    string
			quantity  int
			unitCents int64
		)
		if scanErr := rows.Scan(&orderID, &rawSKU, &quantity, &unitCents); scanErr != nil {
			return nil, errs.Internal("scan order line", scanErr)
		}
		line, lineErr := toLine(rawSKU, quantity, unitCents)
		if lineErr != nil {
			return nil, lineErr
		}
		byOrder[orderID] = append(byOrder[orderID], line)
	}
	if rows.Err() != nil {
		return nil, errs.Internal("iterate order lines", rows.Err())
	}
	return byOrder, nil
}

// toLine rebuilds one line from stored values, rejecting anything the domain
// would not have produced.
func toLine(rawSKU string, quantity int, unitCents int64) (domain.Line, error) {
	sku, err := domain.NewSKU(rawSKU)
	if err != nil {
		return domain.Line{}, errs.Internal("stored sku is not valid", err)
	}
	parsedQuantity, err := domain.NewQuantity(quantity)
	if err != nil {
		return domain.Line{}, errs.Internal("stored quantity is not valid", err)
	}
	price, err := domain.NewMoney(unitCents, "EUR")
	if err != nil {
		return domain.Line{}, errs.Internal("stored price is not valid", err)
	}
	line, err := domain.NewLine(sku, parsedQuantity, price)
	if err != nil {
		return domain.Line{}, errs.Internal("stored line is not valid", err)
	}
	return line, nil
}

func (r *Repository) linesFor(ctx context.Context, orderID string) ([]domain.Line, error) {
	byOrder, err := r.linesForAll(ctx, []string{orderID})
	if err != nil {
		return nil, err
	}
	return byOrder[orderID], nil
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

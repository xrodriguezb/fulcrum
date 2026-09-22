// Package app holds the order use cases and the ports they depend on. It knows
// about the domain and about other contexts' application ports, never about any
// infrastructure.
package app

import (
	"context"
	"time"

	"github.com/xrodriguezb/fulcrum/internal/order/domain"
)

// Repository persists and loads order aggregates.
type Repository interface {
	// Save persists a new order and its lines inside the caller's transaction.
	Save(ctx context.Context, order *domain.Order) error
	// UpdateStatus persists a state change of an existing order.
	UpdateStatus(ctx context.Context, id domain.OrderID, status domain.Status, updatedAt time.Time) error
	// ByID loads one order, or reports not found.
	ByID(ctx context.Context, id domain.OrderID) (*domain.Order, error)
	// Page lists orders newest first.
	Page(ctx context.Context, limit, offset int) ([]*domain.Order, int, error)
}

// TxManager runs a function inside a database transaction.
type TxManager interface {
	WithinTx(ctx context.Context, fn func(ctx context.Context) error) error
	// InTransaction reports whether the context is already inside one. The
	// create order use case refuses to run in that case, because its idempotency
	// claim has to commit before the business transaction opens and would
	// silently join an outer one instead.
	InTransaction(ctx context.Context) bool
}

// Clock returns the current time. The domain has no clock of its own, so the
// application supplies one and the tests can supply a fixed one.
type Clock func() time.Time

// IDGenerator produces identifiers. The domain validates identifiers but never
// creates them, which keeps it deterministic.
type IDGenerator interface {
	NewID() string
}

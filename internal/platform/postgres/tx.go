package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Executor is the shared surface of a pool and a transaction. Repositories take
// one of these, so the same repository method works inside a transaction and
// outside it without knowing which it got.
type Executor interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type txContextKey struct{}

// TxManager runs work inside a database transaction and makes that transaction
// available to everything called underneath it.
//
// The transaction travels in the context rather than in every signature. The
// alternative, threading a transaction handle through each call, spreads the
// persistence model across the application layer and makes a use case impossible
// to read without knowing which of its collaborators need a handle.
type TxManager struct {
	pool *pgxpool.Pool
}

// NewTxManager builds a transaction manager over a pool.
func NewTxManager(pool *pgxpool.Pool) *TxManager {
	return &TxManager{pool: pool}
}

// Executor returns the transaction bound to the context, or the pool when the
// caller is not inside one.
func (m *TxManager) Executor(ctx context.Context) Executor {
	if tx, ok := ctx.Value(txContextKey{}).(pgx.Tx); ok {
		return tx
	}
	return m.pool
}

// InTransaction reports whether the context is already inside a transaction.
func (m *TxManager) InTransaction(ctx context.Context) bool {
	_, ok := ctx.Value(txContextKey{}).(pgx.Tx)
	return ok
}

// WithinTx runs fn inside a transaction, committing when it returns nil and
// rolling back otherwise.
//
// A call made while a transaction is already open joins that transaction rather
// than starting a second one. Starting a second transaction on a second
// connection would silently break atomicity: the outer rollback would leave the
// inner work committed.
//
// A panic rolls back and is re-raised, because a pooled connection that goes
// back to the pool with an open transaction poisons the next caller.
func (m *TxManager) WithinTx(ctx context.Context, fn func(ctx context.Context) error) (err error) {
	if m.InTransaction(ctx) {
		return fn(ctx)
	}

	tx, err := m.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}

	committed := false
	defer func() {
		if recovered := recover(); recovered != nil {
			// The rollback runs on a context cancellation cannot reach, so a
			// panic during a cancelled request still releases the connection.
			_ = tx.Rollback(context.WithoutCancel(ctx))
			panic(recovered)
		}
		if !committed {
			if rollbackErr := tx.Rollback(context.WithoutCancel(ctx)); rollbackErr != nil &&
				!errors.Is(rollbackErr, pgx.ErrTxClosed) {
				err = errors.Join(err, fmt.Errorf("rollback: %w", rollbackErr))
			}
		}
	}()

	if err = fn(context.WithValue(ctx, txContextKey{}, tx)); err != nil {
		return err
	}

	if commitErr := tx.Commit(ctx); commitErr != nil {
		return fmt.Errorf("commit transaction: %w", commitErr)
	}
	committed = true
	return nil
}

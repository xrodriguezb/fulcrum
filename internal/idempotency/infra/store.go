// Package infra implements the idempotency store against PostgreSQL.
package infra

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/xrodriguezb/fulcrum/internal/idempotency/app"
	"github.com/xrodriguezb/fulcrum/internal/platform/errs"
	"github.com/xrodriguezb/fulcrum/internal/platform/postgres"
)

// claimStatement inserts the claim, and takes over a record that is either
// expired or left behind by a failed attempt.
//
// The WHERE clause on the conflict branch is what distinguishes a live duplicate
// from a dead one: without it, a retry after a rolled back attempt would be
// refused until the ttl expired, and an expired key would be honoured forever.
const claimStatement = `
INSERT INTO idempotency_keys (key, request_fingerprint, status, expires_at)
VALUES ($1, $2, 'in_progress', $3)
ON CONFLICT (key) DO UPDATE
SET request_fingerprint = EXCLUDED.request_fingerprint,
    status              = 'in_progress',
    response_status     = NULL,
    response_body       = NULL,
    order_id            = NULL,
    created_at          = now(),
    completed_at        = NULL,
    expires_at          = EXCLUDED.expires_at
WHERE idempotency_keys.expires_at <= now()
   OR idempotency_keys.status = 'failed'
RETURNING key`

const selectStatement = `
SELECT key, request_fingerprint, status, coalesce(response_status, 0), response_body,
       coalesce(order_id::text, ''), created_at, completed_at, expires_at
FROM   idempotency_keys
WHERE  key = $1`

// Store is the PostgreSQL implementation of the idempotency store.
type Store struct {
	tx *postgres.TxManager
}

// NewStore builds a store over the transaction manager.
func NewStore(tx *postgres.TxManager) *Store {
	return &Store{tx: tx}
}

// Claim attempts to take ownership of a key.
func (s *Store) Claim(ctx context.Context, key string, fingerprint []byte, expiresAt time.Time) (app.ClaimResult, error) {
	executor := s.tx.Executor(ctx)

	var claimed string
	err := executor.QueryRow(ctx, claimStatement, key, fingerprint, expiresAt).Scan(&claimed)
	switch {
	case err == nil:
		return app.ClaimResult{Claimed: true}, nil
	case errors.Is(err, pgx.ErrNoRows):
		existing, getErr := s.Get(ctx, key)
		if getErr != nil {
			return app.ClaimResult{}, getErr
		}
		return app.ClaimResult{Claimed: false, Existing: existing}, nil
	default:
		return app.ClaimResult{}, errs.Internal("claim idempotency key", err)
	}
}

// Complete stores the response that a retry will replay.
func (s *Store) Complete(ctx context.Context, key string, responseStatus int, responseBody []byte, orderID string) error {
	const statement = `
UPDATE idempotency_keys
SET    status          = 'completed',
       response_status = $2,
       response_body   = $3,
       order_id        = $4,
       completed_at    = now()
WHERE  key = $1`

	tag, err := s.tx.Executor(ctx).Exec(ctx, statement, key, responseStatus, responseBody, nullableUUID(orderID))
	if err != nil {
		return errs.Internal("complete idempotency key", err)
	}
	if tag.RowsAffected() == 0 {
		return errs.Internal("complete idempotency key", fmt.Errorf("%w: %s", app.ErrKeyNotFound, key))
	}
	return nil
}

// Fail marks an attempt as failed so a retry may proceed.
//
// The reason is stored for operators, truncated, and never echoed to a client:
// it can carry the text of an infrastructure error.
//
// Only a claim that is still in progress can be failed. A commit that reached
// the server and then lost its acknowledgement returns an error to a caller
// whose work is committed, and the caller releases the key on the way out.
// Without the guard that release would mark a successful request as failed and
// destroy the response stored for replay, and the next claim takes over a failed
// key: the retry would create a second order against stock already reserved.
func (s *Store) Fail(ctx context.Context, key, reason string) error {
	const statement = `
UPDATE idempotency_keys
SET    status       = 'failed',
       completed_at = now(),
       response_body = json_build_object('reason', left($2, 500))::text
WHERE  key = $1
  AND  status = 'in_progress'`

	if _, err := s.tx.Executor(ctx).Exec(ctx, statement, key, reason); err != nil {
		return errs.Internal("fail idempotency key", err)
	}
	return nil
}

// Get returns the record for a key.
func (s *Store) Get(ctx context.Context, key string) (*app.Record, error) {
	var (
		record      app.Record
		status      string
		body        []byte
		completedAt *time.Time
	)

	err := s.tx.Executor(ctx).QueryRow(ctx, selectStatement, key).Scan(
		&record.Key, &record.Fingerprint, &status, &record.ResponseStatus, &body,
		&record.OrderID, &record.CreatedAt, &completedAt, &record.ExpiresAt)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, fmt.Errorf("%w: %s", app.ErrKeyNotFound, key)
	case err != nil:
		return nil, errs.Internal("read idempotency key", err)
	}

	record.Status = app.Status(status)
	record.ResponseBody = body
	record.CompletedAt = completedAt
	return &record, nil
}

// DeleteExpired removes records past their expiry.
func (s *Store) DeleteExpired(ctx context.Context, now time.Time) (int64, error) {
	const statement = `DELETE FROM idempotency_keys WHERE expires_at <= $1`

	tag, err := s.tx.Executor(ctx).Exec(ctx, statement, now)
	if err != nil {
		return 0, errs.Internal("sweep idempotency keys", err)
	}
	return tag.RowsAffected(), nil
}

// CountInProgressOlderThan reports how many claims are stuck.
func (s *Store) CountInProgressOlderThan(ctx context.Context, cutoff time.Time) (int, error) {
	const statement = `SELECT count(*) FROM idempotency_keys WHERE status = 'in_progress' AND created_at < $1`

	var count int
	if err := s.tx.Executor(ctx).QueryRow(ctx, statement, cutoff).Scan(&count); err != nil {
		return 0, errs.Internal("count stuck idempotency keys", err)
	}
	return count, nil
}

// nullableUUID keeps an empty order id out of a uuid column.
func nullableUUID(value string) any {
	if value == "" {
		return nil
	}
	return value
}

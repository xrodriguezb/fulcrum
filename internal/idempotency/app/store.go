package app

import (
	"context"
	"errors"
	"time"
)

// Status is the lifecycle of a claimed key.
type Status string

const (
	// StatusInProgress means a request holds the key and has not finished.
	StatusInProgress Status = "in_progress"
	// StatusCompleted means the stored response can be replayed.
	StatusCompleted Status = "completed"
	// StatusFailed means the attempt did not finish and a retry may proceed.
	StatusFailed Status = "failed"
)

// ErrKeyNotFound means no record exists for the key.
var ErrKeyNotFound = errors.New("idempotency key not found")

// Record is the stored state of one key.
type Record struct {
	Key            string
	Fingerprint    []byte
	Status         Status
	ResponseStatus int
	ResponseBody   []byte
	OrderID        string
	CreatedAt      time.Time
	CompletedAt    *time.Time
	ExpiresAt      time.Time
}

// ClaimResult reports whether this caller owns the work. When it does not, the
// existing record explains why: another request is in flight, or a response is
// already stored, or the fingerprint does not match.
type ClaimResult struct {
	Claimed  bool
	Existing *Record
}

// Store persists idempotency keys.
//
// Claim has to commit on its own, before the business transaction starts.
// Putting the claim inside that transaction does not work: two concurrent
// duplicates would both begin, neither would see the other's uncommitted row,
// and one would block on the primary key until the other committed, by which
// point it has already done the work it was meant to skip. See ADR 0005.
type Store interface {
	// Claim inserts an in_progress record, or reports the existing one.
	Claim(ctx context.Context, key string, fingerprint []byte, expiresAt time.Time) (ClaimResult, error)
	// Complete stores the response so a retry can replay it. It runs inside the
	// business transaction, so the stored response and the work it describes
	// commit together.
	Complete(ctx context.Context, key string, responseStatus int, responseBody []byte, orderID string) error
	// Fail releases the key after a failed attempt, in its own transaction.
	Fail(ctx context.Context, key, reason string) error
	// Get returns a record, or ErrKeyNotFound.
	Get(ctx context.Context, key string) (*Record, error)
	// DeleteExpired removes records past their expiry and returns how many.
	DeleteExpired(ctx context.Context, now time.Time) (int64, error)
	// CountInProgressOlderThan reports keys that are stuck, which the operations
	// console surfaces. A crash between the claim and the business transaction
	// leaves exactly this trace.
	CountInProgressOlderThan(ctx context.Context, cutoff time.Time) (int, error)
}

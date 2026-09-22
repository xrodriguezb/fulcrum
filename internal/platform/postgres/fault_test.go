package postgres_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/xrodriguezb/fulcrum/internal/platform/errs"
	"github.com/xrodriguezb/fulcrum/internal/platform/postgres"
)

// The classification decides the status a client sees and the alert an operator
// reads, so each case names the answer it stands for.
func TestFaultSeparatesAnUnreachableDatabaseFromABrokenQuery(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		err  error
		want errs.Kind
	}{
		"admin shutdown": {
			err:  &pgconn.PgError{Code: "57P01", Message: "terminating connection due to administrator command"},
			want: errs.KindUnavailable,
		},
		"connection failure class": {
			err:  &pgconn.PgError{Code: "08006", Message: "connection failure"},
			want: errs.KindUnavailable,
		},
		"too many connections": {
			err:  &pgconn.PgError{Code: "53300", Message: "too many clients already"},
			want: errs.KindUnavailable,
		},
		"deadline waiting on the database": {
			err:  context.DeadlineExceeded,
			want: errs.KindUnavailable,
		},
		"unique violation": {
			err:  &pgconn.PgError{Code: "23505", Message: "duplicate key value"},
			want: errs.KindInternal,
		},
		"undefined column": {
			err:  &pgconn.PgError{Code: "42703", Message: "column does not exist"},
			want: errs.KindInternal,
		},
		"check constraint": {
			err:  &pgconn.PgError{Code: "23514", Message: "violates check constraint"},
			want: errs.KindInternal,
		},
		"an error with no driver information": {
			err:  errors.New("something else"),
			want: errs.KindInternal,
		},
		"a cancelled caller is not an outage": {
			err:  context.Canceled,
			want: errs.KindInternal,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := postgres.Fault("do the thing", tc.err)
			if errs.KindOf(got) != tc.want {
				t.Errorf("kind = %v, want %v", errs.KindOf(got), tc.want)
			}
			if !errors.Is(got, tc.err) {
				t.Errorf("the cause was not preserved: %v", got)
			}
		})
	}
}

// Only a transient classification is retried, so the two kinds must not blur.
func TestFaultMakesOnlyUnreachableFailuresTransient(t *testing.T) {
	t.Parallel()

	unreachable := postgres.Fault("claim", &pgconn.PgError{Code: "57P03", Message: "cannot connect now"})
	if !errs.IsTransient(unreachable) {
		t.Errorf("a database that cannot be reached must be retryable")
	}

	broken := postgres.Fault("claim", &pgconn.PgError{Code: "22P02", Message: "invalid input syntax"})
	if errs.IsTransient(broken) {
		t.Errorf("a query the database refuses must not be retried")
	}
}

// The public message is the same sentence whichever statement failed. An
// operation name is written for an operator reading a log, and a client that
// receives it learns the shape of the internals and nothing it can act on.
func TestFaultKeepsTheOperationOutOfThePublicMessage(t *testing.T) {
	t.Parallel()

	err := postgres.Fault("claim idempotency key",
		&pgconn.PgError{Code: "57P01", Message: "terminating connection"})

	message := errs.PublicMessage(err)
	for _, internal := range []string{"claim", "idempotency", "terminating", "57P01"} {
		if strings.Contains(strings.ToLower(message), internal) {
			t.Errorf("the public message leaked %q: %s", internal, message)
		}
	}
	if !strings.Contains(err.Error(), "claim idempotency key") {
		t.Errorf("the operation must survive for the log: %s", err.Error())
	}
}

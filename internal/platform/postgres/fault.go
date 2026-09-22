package postgres

import (
	"context"
	"errors"
	"fmt"
	"net"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/xrodriguezb/fulcrum/internal/platform/errs"
)

// Fault classifies a driver error and wraps it with the operation that produced
// it.
//
// The distinction it draws is the one a caller acts on. A database that cannot
// be reached is a dependency failure: the request may well succeed on retry, the
// transport answers 503, and an operator reading the numbers sees an outage. A
// constraint violation, a syntax error or a scan into the wrong type is a defect
// in this program: retrying it will fail the same way, the transport answers 500,
// and the number belongs in a different alert.
//
// Classifying everything as internal, which is what the repositories did, told
// every client not to retry during a database blip and made an outage look like
// a bug in the order path.
func Fault(op string, err error) error {
	if unreachable(err) {
		// The operation travels in the cause, where the log can read it. The
		// caller gets the same sentence for every unreachable dependency: an
		// operation name is internal vocabulary, and naming the statement that
		// failed tells a client nothing it can act on.
		return errs.E(errs.KindUnavailable, errs.CodeServiceUnavailable,
			"The service is temporarily unable to handle this request.",
			fmt.Errorf("%s: %w", op, err))
	}
	return errs.Internal(op, err)
}

// unreachable reports whether err says the database could not be reached or
// could not serve the request, rather than refusing it.
func unreachable(err error) bool {
	if err == nil {
		return false
	}
	// A deadline spent waiting on the database is an availability problem. A
	// cancellation is not: it means the caller went away.
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	var connectErr *pgconn.ConnectError
	if errors.As(err, &connectErr) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && len(pgErr.Code) >= 2 {
		// The SQLSTATE class, not the individual code: the whole class carries
		// the same answer, and listing codes would go stale.
		switch pgErr.Code[:2] {
		case "08", // connection exception
			"53", // insufficient resources, including too many connections
			"57", // operator intervention, including admin and crash shutdown
			"58": // system error outside PostgreSQL itself
			return true
		}
	}
	return false
}

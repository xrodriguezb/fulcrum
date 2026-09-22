package app

import (
	"math/rand/v2"
	"time"
)

// maxShift bounds the exponent so that a row with a large attempt count cannot
// shift a duration into overflow. Beyond this the window is the cap anyway.
const maxShift = 32

// BackoffPolicy computes the delay before the next attempt.
//
// The jitter is full: the delay is drawn uniformly from the whole window rather
// than from a band around it. Partial jitter still leaves every failing worker
// retrying at roughly the same moment, so a broker that comes back up receives
// the entire backlog in one burst.
type BackoffPolicy struct {
	Base time.Duration
	Cap  time.Duration
}

// Window is the upper bound of the delay for a given attempt. It is exported so
// a test can assert the distribution rather than infer it.
func (p BackoffPolicy) Window(attempt int) time.Duration {
	base := p.Base
	if base <= 0 {
		base = 100 * time.Millisecond
	}
	limit := p.Cap
	if limit <= 0 {
		limit = 30 * time.Second
	}

	shift := attempt - 1
	if shift < 0 {
		shift = 0
	}
	if shift > maxShift {
		shift = maxShift
	}

	window := base << uint(shift)
	if window <= 0 || window > limit {
		return limit
	}
	return window
}

// Delay returns the wait before the next attempt.
func (p BackoffPolicy) Delay(attempt int) time.Duration {
	window := p.Window(attempt)
	if window <= 0 {
		return 0
	}
	return time.Duration(rand.Int64N(int64(window) + 1)) //nolint:gosec // jitter does not need a cryptographic source
}

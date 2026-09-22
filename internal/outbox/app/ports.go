// Package app holds the outbox use cases and the ports that other contexts use
// to write events.
package app

import (
	"context"

	"github.com/xrodriguezb/fulcrum/internal/outbox/domain"
)

// Writer appends events to the outbox.
//
// An implementation must run inside the caller's transaction. That is the whole
// point of the pattern: the event and the state change it describes commit
// together, so neither can exist without the other.
type Writer interface {
	Append(ctx context.Context, envelopes ...domain.Envelope) error
}

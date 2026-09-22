// Package profiling exposes the Go runtime profiles on a private listener.
package profiling

import (
	"context"
	"log/slog"
)

// Config decides whether profiling runs and where.
type Config struct {
	Enabled bool
	Port    int
}

// Serve is not implemented yet.
func Serve(ctx context.Context, cfg Config, logger *slog.Logger) error {
	_, _, _ = ctx, cfg, logger
	return nil
}

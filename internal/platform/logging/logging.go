// Package logging configures structured logging and carries the identifiers that
// make a single order traceable across the API, the outbox and the consumer.
//
// Identifiers live in the context rather than in function signatures because
// every layer needs them and none of them are business inputs.
package logging

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"os"

	"github.com/xrodriguezb/fulcrum/internal/platform/config"
)

type contextKey int

const (
	requestIDKey contextKey = iota
	correlationIDKey
	traceIDKey
)

// Attribute names. They are constants because dashboards and log queries are
// written against them.
const (
	AttrRequestID     = "request_id"
	AttrCorrelationID = "correlation_id"
	AttrTraceID       = "trace_id"
)

// contextHandler copies the identifiers found in the context onto every record.
// Doing it in the handler rather than at each call site is what makes the
// correlation guarantee hold even for code that forgets to pass attributes.
type contextHandler struct {
	inner slog.Handler
}

func (h contextHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h contextHandler) Handle(ctx context.Context, record slog.Record) error {
	if id := RequestID(ctx); id != "" {
		record.AddAttrs(slog.String(AttrRequestID, id))
	}
	if id := CorrelationID(ctx); id != "" {
		record.AddAttrs(slog.String(AttrCorrelationID, id))
	}
	if id := TraceID(ctx); id != "" {
		record.AddAttrs(slog.String(AttrTraceID, id))
	}
	return h.inner.Handle(ctx, record) //nolint:wrapcheck // a handler must return the inner error unchanged
}

func (h contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return contextHandler{inner: h.inner.WithAttrs(attrs)}
}

func (h contextHandler) WithGroup(name string) slog.Handler {
	return contextHandler{inner: h.inner.WithGroup(name)}
}

// NewJSON builds a JSON logger. Production uses it; the tests use it too, so
// what is asserted is what ships.
func NewJSON(w io.Writer, level slog.Level) *slog.Logger {
	return slog.New(contextHandler{inner: slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level})})
}

// NewText builds a human readable logger for local development.
func NewText(w io.Writer, level slog.Level) *slog.Logger {
	return slog.New(contextHandler{inner: slog.NewTextHandler(w, &slog.HandlerOptions{Level: level})})
}

// New builds the logger a service should use for the given configuration.
func New(cfg config.Config) *slog.Logger {
	logger := NewJSON(os.Stdout, cfg.LogLevel)
	if cfg.IsDevelopment() {
		logger = NewText(os.Stdout, cfg.LogLevel)
	}
	return logger.With(slog.String("service", cfg.ServiceName), slog.String("env", string(cfg.Env)))
}

// WithRequestID returns a context carrying the per-request identifier.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

// WithCorrelationID returns a context carrying the identifier that survives
// across process boundaries, including into the outbox event and the consumer.
func WithCorrelationID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, correlationIDKey, id)
}

// WithTraceID returns a context carrying the trace identifier.
func WithTraceID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, traceIDKey, id)
}

// RequestID returns the per-request identifier, or an empty string.
func RequestID(ctx context.Context) string { return stringValue(ctx, requestIDKey) }

// CorrelationID returns the cross-process identifier, or an empty string.
func CorrelationID(ctx context.Context) string { return stringValue(ctx, correlationIDKey) }

// TraceID returns the trace identifier, or an empty string.
func TraceID(ctx context.Context) string { return stringValue(ctx, traceIDKey) }

func stringValue(ctx context.Context, key contextKey) string {
	if ctx == nil {
		return ""
	}
	value, ok := ctx.Value(key).(string)
	if !ok {
		return ""
	}
	return value
}

// redactedKeyPrefix is short enough to be useless to an attacker reading logs and
// long enough to make collisions between two live keys improbable.
const redactedKeyPrefix = 12

// RedactKey turns a client-supplied idempotency key into a value that is safe to
// log. The key itself can encode customer identifiers, so only a hash of it is
// ever written, while the hash stays stable so two lines about the same request
// can still be joined.
func RedactKey(key string) string {
	if key == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(key))
	return "sha256:" + hex.EncodeToString(sum[:])[:redactedKeyPrefix]
}

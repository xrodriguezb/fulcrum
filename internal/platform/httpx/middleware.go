package httpx

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/xrodriguezb/fulcrum/internal/platform/errs"
	"github.com/xrodriguezb/fulcrum/internal/platform/logging"
)

// maxClientIdentifierLength bounds what a caller can put into a log field. A
// client supplied identifier is convenient for correlation and dangerous
// unbounded, so an unusable value is replaced rather than rejected: the request
// is fine, only its label is not.
const maxClientIdentifierLength = 128

// MiddlewareConfig is the wiring of the chain.
type MiddlewareConfig struct {
	Logger         *slog.Logger
	Metrics        *Metrics
	MaxBodyBytes   int64
	AllowedOrigins []string
}

// Middleware wraps a handler.
type Middleware func(http.Handler) http.Handler

// Chain builds the middleware stack in the order that matters.
//
// The order is not arbitrary and it is not a preference:
//
//  1. Recovery is outermost, so a panic in any other middleware still produces a
//     response instead of a dropped connection.
//  2. Identifiers come next, so every log line and every problem document below
//     them can be correlated, including the ones the chain itself writes.
//  3. Logging and metrics wrap the handler so they observe the final status,
//     including statuses produced by the middleware inside them.
//  4. The body limit precedes content type and the handler, so an oversized
//     body is refused before anything reads it.
//  5. CORS and security headers are innermost, so they apply to every response
//     including the rejections above.
func Chain(cfg MiddlewareConfig) (Middleware, error) {
	switch {
	case cfg.Logger == nil:
		return nil, errors.New("the middleware chain needs a logger")
	case cfg.Metrics == nil:
		return nil, errors.New("the middleware chain needs a metrics registry")
	case cfg.MaxBodyBytes <= 0:
		return nil, errors.New("the middleware chain needs a positive body limit")
	}

	middlewares := []Middleware{
		recovery(cfg.Logger),
		identifiers(),
		observability(cfg.Logger, cfg.Metrics),
		bodyLimit(cfg.MaxBodyBytes, cfg.Logger),
		contentType(cfg.Logger),
		cors(cfg.AllowedOrigins),
		securityHeaders(),
	}

	return func(next http.Handler) http.Handler {
		handler := next
		for i := len(middlewares) - 1; i >= 0; i-- {
			handler = middlewares[i](handler)
		}
		return handler
	}, nil
}

func recovery(logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			defer func() {
				if recovered := recover(); recovered != nil {
					// The panic value is for the operator. The caller gets a
					// generic internal error, because a panic message routinely
					// contains a pointer, a query or a path.
					logger.ErrorContext(ctx, "handler panicked",
						slog.Any("panic", recovered),
						slog.String("method", r.Method),
						slog.String("path", r.URL.Path))
					WriteProblem(w, r, errs.Internal("handler panicked", errors.New("panic recovered")), logger)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

func identifiers() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requestID := safeIdentifier(r.Header.Get("X-Request-Id"))
			correlationID := safeIdentifier(r.Header.Get("X-Correlation-Id"))
			if correlationID == "" {
				// One identifier follows the work across processes. When the
				// caller does not supply one, the request id becomes it.
				correlationID = requestID
			}

			ctx := logging.WithRequestID(r.Context(), requestID)
			ctx = logging.WithCorrelationID(ctx, correlationID)
			if traceID := safeIdentifier(r.Header.Get("X-Trace-Id")); traceID != "" {
				ctx = logging.WithTraceID(ctx, traceID)
			}

			w.Header().Set("X-Request-Id", requestID)
			w.Header().Set("X-Correlation-Id", correlationID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func observability(logger *slog.Logger, metrics *Metrics) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			started := time.Now()
			recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

			metrics.inFlight.Inc()
			defer metrics.inFlight.Dec()

			next.ServeHTTP(recorder, r)

			elapsed := time.Since(started)
			route := routeLabel(r)
			metrics.observe(route, r.Method, recorder.status, elapsed.Seconds())

			logger.InfoContext(r.Context(), "request",
				slog.String("method", r.Method),
				slog.String("route", route),
				slog.Int("status", recorder.status),
				slog.Int64("duration_ms", elapsed.Milliseconds()),
				slog.Int64("bytes", recorder.written))
		})
	}
}

func bodyLimit(limit int64, logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Body == nil || r.ContentLength == 0 {
				next.ServeHTTP(w, r)
				return
			}
			// The declared length is checked first so an oversized request is
			// refused without reading it, and MaxBytesReader still guards a
			// request that lies about its length.
			if r.ContentLength > limit {
				WriteProblem(w, r, errs.E(errs.KindTooLarge, errs.CodeRequestTooLarge,
					"The request body is larger than the limit.", nil), logger)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, limit)
			next.ServeHTTP(w, r)
		})
	}
}

func contentType(logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost && r.Method != http.MethodPut && r.Method != http.MethodPatch {
				next.ServeHTTP(w, r)
				return
			}

			mediaType, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";")
			if strings.TrimSpace(strings.ToLower(mediaType)) != "application/json" {
				WriteProblem(w, r, errs.E(errs.KindUnsupported, errs.CodeUnsupportedMediaType,
					"The request body must be application/json.", nil), logger)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func cors(allowedOrigins []string) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			// An allowlist, never a wildcard. The console sends an idempotency
			// key and reads operational data, so the set of sites allowed to
			// call this API is a deployment decision, not a default.
			if origin != "" && slices.Contains(allowedOrigins, origin) {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Vary", "Origin")
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers",
					"Content-Type, Idempotency-Key, X-Request-Id, X-Correlation-Id")
				w.Header().Set("Access-Control-Max-Age", "600")
			}

			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func securityHeaders() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			header := w.Header()
			header.Set("X-Content-Type-Options", "nosniff")
			header.Set("X-Frame-Options", "DENY")
			header.Set("Referrer-Policy", "no-referrer")
			// The API serves json and never a document, so the strictest policy
			// is also the correct one.
			header.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
			next.ServeHTTP(w, r)
		})
	}
}

// statusRecorder captures what was actually sent, since the handler may write
// any status and the observability middleware has to report the real one.
type statusRecorder struct {
	http.ResponseWriter
	status  int
	written int64
	wrote   bool
}

func (s *statusRecorder) WriteHeader(status int) {
	if !s.wrote {
		s.status = status
		s.wrote = true
	}
	s.ResponseWriter.WriteHeader(status)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if !s.wrote {
		s.wrote = true
	}
	n, err := s.ResponseWriter.Write(b)
	s.written += int64(n)
	return n, err //nolint:wrapcheck // a ResponseWriter must return the underlying error unchanged
}

// Flush forwards to the underlying writer when it supports flushing, which the
// server sent events endpoint depends on.
func (s *statusRecorder) Flush() {
	if flusher, ok := s.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// routeLabel returns the matched route pattern rather than the request path, so
// that an order id never becomes part of a metric label.
func routeLabel(r *http.Request) string {
	if pattern := r.Pattern; pattern != "" {
		// The pattern is "METHOD /path/{id}", and the method is already a label.
		if _, path, found := strings.Cut(pattern, " "); found {
			return path
		}
		return pattern
	}
	return "unmatched"
}

// safeIdentifier returns a client supplied identifier when it is safe to log, or
// a generated one when it is missing or unusable.
func safeIdentifier(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > maxClientIdentifierLength {
		return newIdentifier()
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return newIdentifier()
		}
	}
	return value
}

func newIdentifier() string {
	var buf [16]byte
	// rand.Read from crypto/rand cannot fail in the current runtime, and an
	// identifier is not a security boundary here: it only has to be unique.
	_, _ = rand.Read(buf[:])
	return hex.EncodeToString(buf[:])
}

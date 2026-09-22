// Package httpx holds the transport plumbing: the middleware chain, the problem
// details writer and the server lifecycle. Handlers live with their contexts;
// what they have in common lives here.
package httpx

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/xrodriguezb/fulcrum/internal/platform/errs"
	"github.com/xrodriguezb/fulcrum/internal/platform/logging"
)

// problemBaseURI namespaces the problem types. The URIs are identifiers, not
// links to fetch: RFC 9457 allows that, and inventing a documentation site that
// does not exist would be worse than a stable identifier.
const problemBaseURI = "https://fulcrum.dev/problems/"

// retryAfterSeconds is what a client is told to wait when a request with the
// same idempotency key is still in flight. It is short because the situation it
// describes is short: another request is mid-transaction.
const retryAfterSeconds = "1"

// Problem is an RFC 9457 problem document, extended with the stable application
// code and the trace id.
type Problem struct {
	Type     string `json:"type"`
	Title    string `json:"title"`
	Status   int    `json:"status"`
	Code     string `json:"code"`
	Detail   string `json:"detail,omitempty"`
	Instance string `json:"instance,omitempty"`
	TraceID  string `json:"trace_id,omitempty"`
}

// WriteProblem translates an error into a response.
//
// This is the only place where an error becomes a status code. A switch in a
// handler would drift from a switch in another handler, and the one that drifts
// is always the one on the path nobody tests.
func WriteProblem(w http.ResponseWriter, r *http.Request, err error, logger *slog.Logger) {
	kind := errs.KindOf(err)
	status := errs.HTTPStatus(kind)
	code := errs.CodeOf(err)
	ctx := r.Context()

	// Anything unexpected is logged in full, with the cause, because that text
	// is the only record of what happened. The response gets none of it.
	if status >= http.StatusInternalServerError {
		logger.ErrorContext(ctx, "request failed",
			slog.String("error", err.Error()),
			slog.String("code", code),
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path))
	} else {
		logger.InfoContext(ctx, "request rejected",
			slog.String("code", code),
			slog.String("kind", kind.String()),
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path))
	}

	problem := Problem{
		Type:     problemBaseURI + slug(code),
		Title:    title(code, status),
		Status:   status,
		Code:     code,
		Detail:   errs.PublicMessage(err),
		Instance: r.URL.Path,
		TraceID:  logging.TraceID(ctx),
	}

	if code == errs.CodeIdempotencyInProgress {
		w.Header().Set("Retry-After", retryAfterSeconds)
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)

	if encodeErr := json.NewEncoder(w).Encode(problem); encodeErr != nil {
		// The status line is already written, so there is nothing left to tell
		// the client. The operator still gets the record.
		logger.ErrorContext(ctx, "cannot write problem document", slog.String("error", encodeErr.Error()))
	}
}

// slug turns a stable code into the path segment of a problem type.
func slug(code string) string {
	return strings.ToLower(strings.ReplaceAll(code, "_", "-"))
}

// title is a short human readable summary. It is derived from the code so that
// adding a code cannot leave a response without a title.
func title(code string, status int) string {
	switch code {
	case errs.CodeValidationFailed:
		return "Validation failed"
	case errs.CodeOrderNotFound:
		return "Order not found"
	case errs.CodeOrderInvalidState:
		return "Order is in an invalid state"
	case errs.CodeInventoryInsufficient:
		return "Insufficient inventory"
	case errs.CodeIdempotencyKeyRequired:
		return "Idempotency key required"
	case errs.CodeIdempotencyKeyReuse:
		return "Idempotency key reused"
	case errs.CodeIdempotencyInProgress:
		return "Request in progress"
	case errs.CodeMethodNotAllowed:
		return "Method not allowed"
	case errs.CodeRequestTooLarge:
		return "Request too large"
	case errs.CodeUnsupportedMediaType:
		return "Unsupported media type"
	case errs.CodeServiceUnavailable:
		return "Service unavailable"
	case errs.CodeInternal:
		return "Internal error"
	default:
		return http.StatusText(status)
	}
}

// WriteJSON writes a successful response with the standard headers.
func WriteJSON(w http.ResponseWriter, r *http.Request, status int, payload any, logger *slog.Logger) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		logger.ErrorContext(r.Context(), "cannot write response body", slog.String("error", err.Error()))
	}
}

// WriteRaw writes bytes that were produced earlier, which is how an idempotent
// replay returns exactly what the first caller received.
func WriteRaw(w http.ResponseWriter, r *http.Request, status int, body []byte, logger *slog.Logger) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if _, err := w.Write(body); err != nil {
		logger.ErrorContext(r.Context(), "cannot write response body", slog.String("error", err.Error()))
	}
}

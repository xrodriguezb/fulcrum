// Package errs is the error taxonomy shared by every layer. Errors are
// classified once, at the point where the failure is understood, and every
// boundary above that point reads the classification instead of re-deriving it
// from a message. String matching on error text is what this package exists to
// make unnecessary.
package errs

import (
	"errors"
	"fmt"
	"net/http"
)

// Kind is the coarse category of a failure. It decides retry behaviour and HTTP
// status, so adding one is a deliberate act that forces both decisions.
type Kind int

const (
	// KindNone means no error is present.
	KindNone Kind = iota
	// KindValidation means the caller sent something the system will never accept.
	KindValidation
	// KindNotFound means the addressed resource does not exist.
	KindNotFound
	// KindConflict means the request lost a race or contradicts current state.
	KindConflict
	// KindPrecondition means a required state was not met before the operation.
	KindPrecondition
	// KindMethodNotAllowed means the path exists but not for this method.
	KindMethodNotAllowed
	// KindUnsupported means the request format or media type is not accepted.
	KindUnsupported
	// KindTooLarge means the request exceeded a declared limit.
	KindTooLarge
	// KindTimeout means an operation exceeded its deadline and may succeed later.
	KindTimeout
	// KindUnavailable means a dependency is down and the operation may succeed later.
	KindUnavailable
	// KindInternal means the failure was not anticipated.
	KindInternal
)

// Stable application error codes. These appear in HTTP problem documents and in
// the operations console, so they are part of the public contract.
const (
	CodeValidationFailed  = "VALIDATION_FAILED"
	CodeOrderNotFound     = "ORDER_NOT_FOUND"
	CodeOrderInvalidState = "ORDER_INVALID_STATE"

	CodeInventoryInsufficient = "INVENTORY_INSUFFICIENT"

	CodeIdempotencyKeyRequired  = "IDEMPOTENCY_KEY_REQUIRED"
	CodeIdempotencyKeyReuse     = "IDEMPOTENCY_KEY_REUSE"
	CodeIdempotencyInProgress   = "IDEMPOTENCY_REQUEST_IN_PROGRESS"
	CodeMethodNotAllowed        = "METHOD_NOT_ALLOWED"
	CodeRequestTooLarge         = "REQUEST_TOO_LARGE"
	CodeUnsupportedMediaType    = "UNSUPPORTED_MEDIA_TYPE"
	CodeInternal                = "INTERNAL_ERROR"
	CodeServiceUnavailable      = "SERVICE_UNAVAILABLE"
	CodeEventPayloadInvalid     = "EVENT_PAYLOAD_INVALID"
	CodeEventVersionUnsupported = "EVENT_VERSION_UNSUPPORTED"
)

// genericPublicMessage is returned for anything the system did not classify,
// because an unclassified error is exactly the case where the text is most
// likely to carry a hostname, a path or a query.
const genericPublicMessage = "The request could not be completed."

// String returns the stable lowercase name of the kind, used in logs and metric
// labels. The set is closed, so the label cardinality is bounded.
func (k Kind) String() string {
	switch k {
	case KindNone:
		return "none"
	case KindValidation:
		return "validation"
	case KindNotFound:
		return "not_found"
	case KindConflict:
		return "conflict"
	case KindPrecondition:
		return "precondition"
	case KindMethodNotAllowed:
		return "method_not_allowed"
	case KindUnsupported:
		return "unsupported"
	case KindTooLarge:
		return "too_large"
	case KindTimeout:
		return "timeout"
	case KindUnavailable:
		return "unavailable"
	case KindInternal:
		return "internal"
	default:
		return "internal"
	}
}

// AllKinds lists every kind. The HTTP mapping test walks it, so a new kind
// cannot be added without a status decision.
func AllKinds() []Kind {
	return []Kind{
		KindNone, KindValidation, KindNotFound, KindConflict, KindPrecondition,
		KindMethodNotAllowed, KindUnsupported, KindTooLarge, KindTimeout,
		KindUnavailable, KindInternal,
	}
}

// Error is a classified error. Message is safe to return to a caller. The
// wrapped cause is kept for logs and never rendered into a response.
type Error struct {
	Kind    Kind
	Code    string
	Message string
	cause   error
}

// E builds a classified error. A nil cause is allowed: not every failure has one.
func E(kind Kind, code, message string, cause error) *Error {
	return &Error{Kind: kind, Code: code, Message: message, cause: cause}
}

// Error renders the full internal text, cause included. This string is for logs.
func (e *Error) Error() string {
	if e.cause == nil {
		return fmt.Sprintf("%s: %s", e.Code, e.Message)
	}
	return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.cause)
}

// Unwrap keeps errors.Is and errors.As working through the classification.
func (e *Error) Unwrap() error { return e.cause }

// KindOf reports the classification of err, defaulting to internal so that an
// unclassified failure is never accidentally treated as a client mistake.
func KindOf(err error) Kind {
	if err == nil {
		return KindNone
	}
	var classified *Error
	if errors.As(err, &classified) {
		return classified.Kind
	}
	return KindInternal
}

// CodeOf reports the stable code of err, defaulting to the internal code.
func CodeOf(err error) string {
	if err == nil {
		return ""
	}
	var classified *Error
	if errors.As(err, &classified) && classified.Code != "" {
		return classified.Code
	}
	return CodeInternal
}

// PublicMessage returns text that is safe to send to a caller. Anything
// unclassified, and anything classified as internal, collapses to a generic
// sentence, because those are the errors whose text was written for an operator.
func PublicMessage(err error) string {
	if err == nil {
		return ""
	}
	var classified *Error
	if !errors.As(err, &classified) {
		return genericPublicMessage
	}
	if classified.Kind == KindInternal || classified.Message == "" {
		return genericPublicMessage
	}
	return classified.Message
}

// IsTransient reports whether retrying err could plausibly succeed. Unclassified
// errors are treated as permanent: retrying a failure nobody understood is how a
// single bad message turns into a retry storm.
func IsTransient(err error) bool {
	if err == nil {
		return false
	}
	switch KindOf(err) {
	case KindTimeout, KindUnavailable:
		return true
	case KindNone, KindValidation, KindNotFound, KindConflict, KindPrecondition,
		KindMethodNotAllowed, KindUnsupported, KindTooLarge, KindInternal:
		return false
	default:
		return false
	}
}

// HTTPStatus maps a kind to the status the transport layer returns.
func HTTPStatus(kind Kind) int {
	switch kind {
	case KindNone:
		return http.StatusOK
	case KindValidation:
		return http.StatusBadRequest
	case KindNotFound:
		return http.StatusNotFound
	case KindConflict:
		return http.StatusConflict
	case KindPrecondition:
		return http.StatusUnprocessableEntity
	case KindMethodNotAllowed:
		return http.StatusMethodNotAllowed
	case KindUnsupported:
		return http.StatusUnsupportedMediaType
	case KindTooLarge:
		return http.StatusRequestEntityTooLarge
	case KindTimeout:
		return http.StatusGatewayTimeout
	case KindUnavailable:
		return http.StatusServiceUnavailable
	case KindInternal:
		return http.StatusInternalServerError
	default:
		return http.StatusInternalServerError
	}
}

// Convenience constructors for the kinds used most often. They exist so that a
// call site states the classification in one line and cannot forget the code.

// Validation builds a validation error.
func Validation(code, message string, cause error) *Error {
	return E(KindValidation, code, message, cause)
}

// NotFound builds a not found error.
func NotFound(code, message string, cause error) *Error {
	return E(KindNotFound, code, message, cause)
}

// Conflict builds a conflict error.
func Conflict(code, message string, cause error) *Error {
	return E(KindConflict, code, message, cause)
}

// Precondition builds a precondition failure.
func Precondition(code, message string, cause error) *Error {
	return E(KindPrecondition, code, message, cause)
}

// Unavailable builds a dependency failure that may succeed on retry.
func Unavailable(message string, cause error) *Error {
	return E(KindUnavailable, CodeServiceUnavailable, message, cause)
}

// Timeout builds a deadline failure that may succeed on retry.
func Timeout(message string, cause error) *Error {
	return E(KindTimeout, CodeServiceUnavailable, message, cause)
}

// Internal builds an unanticipated failure. The message is for operators.
func Internal(message string, cause error) *Error {
	return E(KindInternal, CodeInternal, message, cause)
}

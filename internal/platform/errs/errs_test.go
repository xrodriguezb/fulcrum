package errs_test

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/xrodriguezb/fulcrum/internal/platform/errs"
)

var errDomain = errors.New("order line quantity must be positive")

func TestKindAndCodeTravelThroughWrapping(t *testing.T) {
	t.Parallel()

	base := errs.E(errs.KindValidation, errs.CodeValidationFailed, "the request is not valid", errDomain)
	wrapped := fmt.Errorf("create order: %w", base)
	deeper := fmt.Errorf("handler: %w", wrapped)

	if got := errs.KindOf(deeper); got != errs.KindValidation {
		t.Errorf("KindOf = %v, want %v", got, errs.KindValidation)
	}
	if got := errs.CodeOf(deeper); got != errs.CodeValidationFailed {
		t.Errorf("CodeOf = %q, want %q", got, errs.CodeValidationFailed)
	}
	if !errors.Is(deeper, errDomain) {
		t.Errorf("the causal chain was broken, errors.Is could not reach the domain error")
	}
}

func TestKindOfUnclassifiedErrorIsInternal(t *testing.T) {
	t.Parallel()

	if got := errs.KindOf(errors.New("something nobody classified")); got != errs.KindInternal {
		t.Errorf("KindOf = %v, want %v for an unclassified error", got, errs.KindInternal)
	}
	if got := errs.CodeOf(errors.New("something nobody classified")); got != errs.CodeInternal {
		t.Errorf("CodeOf = %q, want %q for an unclassified error", got, errs.CodeInternal)
	}
	if got := errs.KindOf(nil); got != errs.KindNone {
		t.Errorf("KindOf(nil) = %v, want %v", got, errs.KindNone)
	}
}

func TestPublicMessageNeverExposesTheCause(t *testing.T) {
	t.Parallel()

	cause := errors.New(`pq: relation "orders" does not exist at /var/lib/postgresql/data`)
	err := errs.E(errs.KindInternal, errs.CodeInternal, "The request could not be completed.", cause)

	public := errs.PublicMessage(err)
	for _, leak := range []string{"pq:", "relation", "/var/lib"} {
		if strings.Contains(public, leak) {
			t.Errorf("public message %q leaked %q from the cause", public, leak)
		}
	}
	if !strings.Contains(err.Error(), "relation") {
		t.Errorf("the internal error text must keep the cause for logs, got %q", err.Error())
	}
}

func TestPublicMessageOfUnclassifiedErrorIsGeneric(t *testing.T) {
	t.Parallel()

	public := errs.PublicMessage(errors.New("dial tcp 10.0.0.4:5432: connect: connection refused"))
	if strings.Contains(public, "10.0.0.4") || strings.Contains(public, "dial tcp") {
		t.Errorf("public message leaked infrastructure detail: %q", public)
	}
	if public == "" {
		t.Errorf("public message must never be empty")
	}
}

func TestTransientClassificationDrivesRetries(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "unavailable is transient", err: errs.E(errs.KindUnavailable, errs.CodeInternal, "broker unreachable", nil), want: true},
		{name: "timeout is transient", err: errs.E(errs.KindTimeout, errs.CodeInternal, "deadline exceeded", nil), want: true},
		{name: "validation is permanent", err: errs.E(errs.KindValidation, errs.CodeValidationFailed, "bad payload", nil), want: false},
		{name: "conflict is permanent", err: errs.E(errs.KindConflict, errs.CodeInventoryInsufficient, "no stock", nil), want: false},
		{name: "not found is permanent", err: errs.E(errs.KindNotFound, errs.CodeOrderNotFound, "gone", nil), want: false},
		{name: "wrapped unavailable stays transient", err: fmt.Errorf("publish: %w", errs.E(errs.KindUnavailable, errs.CodeInternal, "broker unreachable", nil)), want: true},
		{name: "unclassified is not retried blindly", err: errors.New("who knows"), want: false},
		{name: "nil is not transient", err: nil, want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := errs.IsTransient(tc.err); got != tc.want {
				t.Errorf("IsTransient = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAsExposesTheClassifiedError(t *testing.T) {
	t.Parallel()

	err := fmt.Errorf("repository: %w", errs.E(errs.KindNotFound, errs.CodeOrderNotFound, "order not found", sql.ErrNoRows))

	var classified *errs.Error
	if !errors.As(err, &classified) {
		t.Fatalf("errors.As could not extract the classified error")
	}
	if classified.Code != errs.CodeOrderNotFound {
		t.Errorf("Code = %q, want %q", classified.Code, errs.CodeOrderNotFound)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("the infrastructure cause must remain reachable for diagnosis")
	}
}

// The mapper from kind to HTTP status lives with the taxonomy so that a new kind
// cannot be introduced without deciding what it means on the wire.
func TestEveryKindHasAStatus(t *testing.T) {
	t.Parallel()

	for _, kind := range errs.AllKinds() {
		status := errs.HTTPStatus(kind)
		if status < 200 || status > 599 {
			t.Errorf("kind %v maps to status %d, which is not a valid HTTP status", kind, status)
		}
	}
}

func TestKindStringIsStable(t *testing.T) {
	t.Parallel()

	if errs.KindValidation.String() != "validation" {
		t.Errorf("KindValidation.String() = %q, want %q", errs.KindValidation.String(), "validation")
	}
	if errs.KindNone.String() != "none" {
		t.Errorf("KindNone.String() = %q, want %q", errs.KindNone.String(), "none")
	}
}

// Package domain holds the order aggregate and its value objects. It depends on
// nothing but the standard library: no driver, no transport, no logging, no
// telemetry. test/architecture_test.go enforces that, because a boundary that is
// only described in a document is a boundary that erodes.
package domain

import "errors"

// Validation failures of value objects. They are sentinels so that callers
// classify with errors.Is rather than by inspecting a message.
var (
	// ErrInvalidSKU means the string is not a usable stock keeping unit.
	ErrInvalidSKU = errors.New("invalid sku")
	// ErrInvalidQuantity means the quantity is outside the accepted range.
	ErrInvalidQuantity = errors.New("invalid quantity")
	// ErrInvalidAmount means a monetary amount is negative or the factor was.
	ErrInvalidAmount = errors.New("invalid amount")
	// ErrInvalidCurrency means the currency is not a three letter code.
	ErrInvalidCurrency = errors.New("invalid currency")
	// ErrCurrencyMismatch means two amounts in different currencies were combined.
	ErrCurrencyMismatch = errors.New("currency mismatch")
	// ErrInvalidOrderID means the order identifier is not a canonical UUID.
	ErrInvalidOrderID = errors.New("invalid order id")
	// ErrInvalidCustomerID means the customer identifier is not a canonical UUID.
	ErrInvalidCustomerID = errors.New("invalid customer id")
)

// Aggregate invariants.
var (
	// ErrNoLines means an order was constructed without any line.
	ErrNoLines = errors.New("an order needs at least one line")
	// ErrTooManyLines means an order exceeded the line count bound.
	ErrTooManyLines = errors.New("too many order lines")
	// ErrDuplicateSKU means the same sku appeared twice in one order.
	ErrDuplicateSKU = errors.New("duplicate sku in one order")
	// ErrInvalidTransition means the requested state change is not permitted.
	ErrInvalidTransition = errors.New("invalid order state transition")
	// ErrAlreadyConfirmed means the order is already in the target state. It is
	// separate from ErrInvalidTransition because a consumer that sees a
	// redelivered event must be able to treat it as success.
	ErrAlreadyConfirmed = errors.New("order is already confirmed")
	// ErrAlreadyCancelled means the order was already cancelled.
	ErrAlreadyCancelled = errors.New("order is already cancelled")
)

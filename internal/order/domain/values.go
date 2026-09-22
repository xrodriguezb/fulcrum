package domain

import (
	"fmt"
	"strings"
)

// skuMinLength and skuMaxLength match the pattern published in the API contract.
// Keeping the bounds here as well means a contract change that is not reflected
// in the domain fails a test rather than reaching production.
const (
	skuMinLength = 3
	skuMaxLength = 32

	quantityMin = 1
	quantityMax = 1000

	currencyLength = 3
)

// SKU is a validated stock keeping unit. The zero value is unusable on purpose:
// a value object that can be constructed by accident is not a value object.
type SKU struct {
	value string
}

// NewSKU validates and normalises a stock keeping unit.
func NewSKU(raw string) (SKU, error) {
	normalised := strings.ToUpper(strings.TrimSpace(raw))
	if len(normalised) < skuMinLength || len(normalised) > skuMaxLength {
		return SKU{}, fmt.Errorf("%w: length must be between %d and %d", ErrInvalidSKU, skuMinLength, skuMaxLength)
	}
	if !isSKUStart(rune(normalised[0])) {
		return SKU{}, fmt.Errorf("%w: must start with a letter or a digit", ErrInvalidSKU)
	}
	for _, r := range normalised {
		if !isSKUStart(r) && r != '-' {
			return SKU{}, fmt.Errorf("%w: only letters, digits and dashes are allowed", ErrInvalidSKU)
		}
	}
	return SKU{value: normalised}, nil
}

func isSKUStart(r rune) bool {
	return (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}

// String renders the sku. The zero value renders as an empty string.
func (s SKU) String() string { return s.value }

// IsZero reports whether the sku was never constructed through NewSKU.
func (s SKU) IsZero() bool { return s.value == "" }

// Quantity is a bounded positive count of units.
type Quantity struct {
	value int
}

// NewQuantity validates a unit count.
func NewQuantity(value int) (Quantity, error) {
	if value < quantityMin || value > quantityMax {
		return Quantity{}, fmt.Errorf("%w: must be between %d and %d", ErrInvalidQuantity, quantityMin, quantityMax)
	}
	return Quantity{value: value}, nil
}

// Int returns the quantity as a plain integer.
func (q Quantity) Int() int { return q.value }

// Money is a non-negative amount in minor units, tagged with its currency.
// Amounts are integers because binary floating point cannot represent a cent.
type Money struct {
	cents    int64
	currency string
}

// NewMoney validates an amount and normalises the currency code.
func NewMoney(cents int64, currency string) (Money, error) {
	if cents < 0 {
		return Money{}, fmt.Errorf("%w: an amount cannot be negative", ErrInvalidAmount)
	}
	code := strings.ToUpper(strings.TrimSpace(currency))
	if len(code) != currencyLength {
		return Money{}, fmt.Errorf("%w: must be a three letter code", ErrInvalidCurrency)
	}
	for _, r := range code {
		if r < 'A' || r > 'Z' {
			return Money{}, fmt.Errorf("%w: must contain letters only", ErrInvalidCurrency)
		}
	}
	return Money{cents: cents, currency: code}, nil
}

// Cents returns the amount in minor units.
func (m Money) Cents() int64 { return m.cents }

// Currency returns the normalised currency code.
func (m Money) Currency() string { return m.currency }

// IsZero reports whether the amount was never constructed.
func (m Money) IsZero() bool { return m.currency == "" }

// Add sums two amounts of the same currency.
func (m Money) Add(other Money) (Money, error) {
	if m.currency != other.currency {
		return Money{}, fmt.Errorf("%w: %s and %s", ErrCurrencyMismatch, m.currency, other.currency)
	}
	return Money{cents: m.cents + other.cents, currency: m.currency}, nil
}

// Times multiplies an amount by a non-negative factor.
func (m Money) Times(factor int) (Money, error) {
	if factor < 0 {
		return Money{}, fmt.Errorf("%w: a factor cannot be negative", ErrInvalidAmount)
	}
	return Money{cents: m.cents * int64(factor), currency: m.currency}, nil
}

// OrderID identifies an order. It is parsed, never generated here: generating an
// identifier would pull a dependency into the domain and make it non-deterministic.
type OrderID struct {
	value string
}

// NewOrderID validates a canonical UUID string.
func NewOrderID(raw string) (OrderID, error) {
	normalised, ok := normaliseUUID(raw)
	if !ok {
		return OrderID{}, fmt.Errorf("%w: %q is not a canonical uuid", ErrInvalidOrderID, raw)
	}
	return OrderID{value: normalised}, nil
}

// String renders the identifier.
func (o OrderID) String() string { return o.value }

// IsZero reports whether the identifier was never constructed.
func (o OrderID) IsZero() bool { return o.value == "" }

// CustomerID identifies the customer an order belongs to.
type CustomerID struct {
	value string
}

// NewCustomerID validates a canonical UUID string.
func NewCustomerID(raw string) (CustomerID, error) {
	normalised, ok := normaliseUUID(raw)
	if !ok {
		return CustomerID{}, fmt.Errorf("%w: %q is not a canonical uuid", ErrInvalidCustomerID, raw)
	}
	return CustomerID{value: normalised}, nil
}

// String renders the identifier.
func (c CustomerID) String() string { return c.value }

// IsZero reports whether the identifier was never constructed.
func (c CustomerID) IsZero() bool { return c.value == "" }

// uuidGroupLengths is the 8-4-4-4-12 shape of a canonical UUID.
var uuidGroupLengths = [5]int{8, 4, 4, 4, 12}

// normaliseUUID accepts a canonical UUID in either case and returns it in lower
// case. A hand written check keeps the domain free of a uuid dependency, and the
// shape is fixed enough that parsing it is six lines.
func normaliseUUID(raw string) (string, bool) {
	candidate := strings.ToLower(strings.TrimSpace(raw))
	groups := strings.Split(candidate, "-")
	if len(groups) != len(uuidGroupLengths) {
		return "", false
	}
	for i, group := range groups {
		if len(group) != uuidGroupLengths[i] {
			return "", false
		}
		for _, r := range group {
			isHex := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')
			if !isHex {
				return "", false
			}
		}
	}
	return candidate, true
}

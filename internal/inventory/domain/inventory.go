// Package domain models inventory as this bounded context understands it.
//
// It defines its own SKU and Quantity rather than importing the order context's
// versions. The two contexts agree on the format today and may not tomorrow, and
// a shared type would turn a change in one context into a change in both.
// Translation happens in the application layer, where it is visible.
package domain

import (
	"errors"
	"fmt"
	"strings"
)

const (
	skuMinLength = 3
	skuMaxLength = 32

	quantityMin = 1
	quantityMax = 1000
)

// Domain errors of the inventory context.
var (
	// ErrInvalidSKU means the string is not a usable stock keeping unit.
	ErrInvalidSKU = errors.New("invalid sku")
	// ErrInvalidQuantity means the quantity is outside the accepted range.
	ErrInvalidQuantity = errors.New("invalid quantity")
	// ErrInvalidStock means the stock figures are impossible.
	ErrInvalidStock = errors.New("invalid stock level")
	// ErrInsufficientInventory means the request exceeds what is available. It
	// is the domain translation of a reservation that matched zero rows.
	ErrInsufficientInventory = errors.New("insufficient inventory")
	// ErrNothingReserved means a release asked for more than is held.
	ErrNothingReserved = errors.New("not enough reserved units to release")
)

// SKU is a validated stock keeping unit.
type SKU struct {
	value string
}

// NewSKU validates and normalises a stock keeping unit.
func NewSKU(raw string) (SKU, error) {
	normalised := strings.ToUpper(strings.TrimSpace(raw))
	if len(normalised) < skuMinLength || len(normalised) > skuMaxLength {
		return SKU{}, fmt.Errorf("%w: length must be between %d and %d", ErrInvalidSKU, skuMinLength, skuMaxLength)
	}
	for i, r := range normalised {
		alphanumeric := (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
		if alphanumeric {
			continue
		}
		if r == '-' && i > 0 {
			continue
		}
		return SKU{}, fmt.Errorf("%w: only letters, digits and dashes are allowed", ErrInvalidSKU)
	}
	return SKU{value: normalised}, nil
}

// String renders the sku.
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

// Item is the stock position of one sku.
//
// The methods here are the readable statement of the rule. The authoritative
// enforcement is a conditional UPDATE in the database, because that is the only
// place where concurrent callers can be arbitrated. See ADR 0003.
type Item struct {
	sku       SKU
	available int
	reserved  int
	version   int
}

// NewItem builds a stock position, rejecting figures that cannot be true.
func NewItem(sku SKU, available, reserved, version int) (*Item, error) {
	if sku.IsZero() {
		return nil, fmt.Errorf("%w: an item needs a sku", ErrInvalidSKU)
	}
	if available < 0 {
		return nil, fmt.Errorf("%w: available cannot be negative", ErrInvalidStock)
	}
	if reserved < 0 {
		return nil, fmt.Errorf("%w: reserved cannot be negative", ErrInvalidStock)
	}
	if version < 1 {
		return nil, fmt.Errorf("%w: version starts at one", ErrInvalidStock)
	}
	return &Item{sku: sku, available: available, reserved: reserved, version: version}, nil
}

// Reserve moves units from available to reserved, or refuses.
func (i *Item) Reserve(quantity Quantity) error {
	if quantity.Int() > i.available {
		return fmt.Errorf("%w: %s has %d available and %d were requested",
			ErrInsufficientInventory, i.sku.String(), i.available, quantity.Int())
	}
	i.available -= quantity.Int()
	i.reserved += quantity.Int()
	i.version++
	return nil
}

// Release moves units back from reserved to available.
func (i *Item) Release(quantity Quantity) error {
	if quantity.Int() > i.reserved {
		return fmt.Errorf("%w: %s holds %d reserved and %d were released",
			ErrNothingReserved, i.sku.String(), i.reserved, quantity.Int())
	}
	i.reserved -= quantity.Int()
	i.available += quantity.Int()
	i.version++
	return nil
}

// SKU returns the item's stock keeping unit.
func (i *Item) SKU() SKU { return i.sku }

// Available returns the units that can still be reserved.
func (i *Item) Available() int { return i.available }

// Reserved returns the units held for existing orders.
func (i *Item) Reserved() int { return i.reserved }

// Version returns the optimistic concurrency counter as the database holds it.
func (i *Item) Version() int { return i.version }

// Package app holds the inventory use cases and the ports other contexts use to
// reach them. The order context depends on this package, never on the
// infrastructure that implements it.
package app

import "context"

// ReservationRequest asks for a quantity of one sku.
type ReservationRequest struct {
	SKU      string
	Quantity int
}

// ReservedLine is what was actually reserved, including the price the inventory
// context holds. Prices come from here rather than from the client, because a
// client that sends its own price is a client that can choose it.
type ReservedLine struct {
	SKU            string
	Quantity       int
	UnitPriceCents int64
	Currency       string
	Available      int
	Reserved       int
	Version        int
}

// Item is a stock position as the operations console reads it.
type Item struct {
	SKU            string
	Available      int
	Reserved       int
	UnitPriceCents int64
	Currency       string
	Version        int
}

// Reserver moves units from available to reserved, atomically, or refuses.
//
// Implementations must run inside the caller's transaction so that a
// reservation and the order it belongs to commit together.
type Reserver interface {
	Reserve(ctx context.Context, requests []ReservationRequest) ([]ReservedLine, error)
}

// Reader exposes stock positions for the operations console.
type Reader interface {
	List(ctx context.Context) ([]Item, error)
}

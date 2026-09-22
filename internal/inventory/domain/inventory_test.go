package domain_test

import (
	"errors"
	"testing"

	"github.com/xrodriguezb/fulcrum/internal/inventory/domain"
)

func mustItem(t *testing.T, sku string, available, reserved int) *domain.Item {
	t.Helper()
	parsed, err := domain.NewSKU(sku)
	if err != nil {
		t.Fatalf("NewSKU(%q) returned %v", sku, err)
	}
	item, err := domain.NewItem(parsed, available, reserved, 1)
	if err != nil {
		t.Fatalf("NewItem returned %v", err)
	}
	return item
}

func TestReserveMovesUnitsFromAvailableToReserved(t *testing.T) {
	t.Parallel()

	item := mustItem(t, "WIDGET-001", 5, 0)
	quantity, err := domain.NewQuantity(3)
	if err != nil {
		t.Fatalf("NewQuantity returned %v", err)
	}

	if err := item.Reserve(quantity); err != nil {
		t.Fatalf("Reserve returned %v", err)
	}
	if item.Available() != 2 {
		t.Errorf("Available = %d, want 2", item.Available())
	}
	if item.Reserved() != 3 {
		t.Errorf("Reserved = %d, want 3", item.Reserved())
	}
	if item.Version() != 2 {
		t.Errorf("Version = %d, want 2 after one change", item.Version())
	}
}

// This mirrors the guard in the reservation SQL. The model and the database
// enforce the same rule, and the integration suite proves the database side.
func TestReserveRefusesToOversell(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		available int
		request   int
		wantErr   error
	}{
		{name: "exactly the stock on hand", available: 5, request: 5},
		{name: "one more than the stock on hand", available: 5, request: 6, wantErr: domain.ErrInsufficientInventory},
		{name: "nothing available", available: 0, request: 1, wantErr: domain.ErrInsufficientInventory},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			item := mustItem(t, "WIDGET-001", tc.available, 0)
			quantity, err := domain.NewQuantity(tc.request)
			if err != nil {
				t.Fatalf("NewQuantity returned %v", err)
			}

			err = item.Reserve(quantity)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("Reserve error = %v, want %v", err, tc.wantErr)
				}
				if item.Available() != tc.available {
					t.Errorf("a refused reservation changed availability to %d, want %d", item.Available(), tc.available)
				}
				return
			}
			if err != nil {
				t.Fatalf("Reserve returned %v", err)
			}
			if item.Available() != tc.available-tc.request {
				t.Errorf("Available = %d, want %d", item.Available(), tc.available-tc.request)
			}
		})
	}
}

func TestReleaseReturnsUnitsToAvailable(t *testing.T) {
	t.Parallel()

	item := mustItem(t, "WIDGET-001", 2, 3)
	quantity, err := domain.NewQuantity(3)
	if err != nil {
		t.Fatalf("NewQuantity returned %v", err)
	}

	if err := item.Release(quantity); err != nil {
		t.Fatalf("Release returned %v", err)
	}
	if item.Available() != 5 || item.Reserved() != 0 {
		t.Errorf("after release: available %d reserved %d, want 5 and 0", item.Available(), item.Reserved())
	}

	tooMuch, err := domain.NewQuantity(1)
	if err != nil {
		t.Fatalf("NewQuantity returned %v", err)
	}
	if err := item.Release(tooMuch); !errors.Is(err, domain.ErrNothingReserved) {
		t.Errorf("releasing more than is reserved must fail, got %v", err)
	}
}

func TestNewItemRejectsImpossibleStock(t *testing.T) {
	t.Parallel()

	sku, err := domain.NewSKU("WIDGET-001")
	if err != nil {
		t.Fatalf("NewSKU returned %v", err)
	}

	cases := []struct {
		name      string
		available int
		reserved  int
		version   int
	}{
		{name: "negative available", available: -1, reserved: 0, version: 1},
		{name: "negative reserved", available: 0, reserved: -1, version: 1},
		{name: "version below one", available: 0, reserved: 0, version: 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := domain.NewItem(sku, tc.available, tc.reserved, tc.version); !errors.Is(err, domain.ErrInvalidStock) {
				t.Errorf("NewItem error = %v, want ErrInvalidStock", err)
			}
		})
	}
}

func TestSKUAndQuantityValidation(t *testing.T) {
	t.Parallel()

	if _, err := domain.NewSKU("no"); !errors.Is(err, domain.ErrInvalidSKU) {
		t.Errorf("NewSKU error = %v, want ErrInvalidSKU", err)
	}
	if _, err := domain.NewQuantity(0); !errors.Is(err, domain.ErrInvalidQuantity) {
		t.Errorf("NewQuantity error = %v, want ErrInvalidQuantity", err)
	}
	sku, err := domain.NewSKU(" widget-001 ")
	if err != nil {
		t.Fatalf("NewSKU returned %v", err)
	}
	if sku.String() != "WIDGET-001" {
		t.Errorf("sku normalisation = %q, want WIDGET-001", sku.String())
	}
}

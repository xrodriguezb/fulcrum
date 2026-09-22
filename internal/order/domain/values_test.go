package domain_test

import (
	"errors"
	"testing"

	"github.com/xrodriguezb/fulcrum/internal/order/domain"
)

func TestNewSKU(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		input   string
		want    string
		wantErr error
	}{
		{name: "canonical", input: "WIDGET-001", want: "WIDGET-001"},
		{name: "digits only", input: "12345", want: "12345"},
		{name: "surrounding space is trimmed", input: "  WIDGET-001  ", want: "WIDGET-001"},
		{name: "lower case is normalised", input: "widget-001", want: "WIDGET-001"},
		{name: "empty", input: "", wantErr: domain.ErrInvalidSKU},
		{name: "too short", input: "AB", wantErr: domain.ErrInvalidSKU},
		{name: "too long", input: "A123456789012345678901234567890123", wantErr: domain.ErrInvalidSKU},
		{name: "leading dash", input: "-WIDGET", wantErr: domain.ErrInvalidSKU},
		{name: "illegal character", input: "WIDGET_001", wantErr: domain.ErrInvalidSKU},
		{name: "whitespace inside", input: "WIDGET 001", wantErr: domain.ErrInvalidSKU},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sku, err := domain.NewSKU(tc.input)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("NewSKU(%q) error = %v, want %v", tc.input, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewSKU(%q) returned %v", tc.input, err)
			}
			if sku.String() != tc.want {
				t.Errorf("NewSKU(%q) = %q, want %q", tc.input, sku.String(), tc.want)
			}
		})
	}
}

func TestSKUZeroValueIsNotUsable(t *testing.T) {
	t.Parallel()

	var sku domain.SKU
	if sku.IsZero() != true {
		t.Errorf("the zero SKU must report itself as zero")
	}
	if sku.String() != "" {
		t.Errorf("the zero SKU renders as %q, want empty", sku.String())
	}
}

func TestNewQuantity(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		input   int
		wantErr error
	}{
		{name: "one", input: 1},
		{name: "maximum", input: 1000},
		{name: "zero", input: 0, wantErr: domain.ErrInvalidQuantity},
		{name: "negative", input: -1, wantErr: domain.ErrInvalidQuantity},
		{name: "above maximum", input: 1001, wantErr: domain.ErrInvalidQuantity},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			quantity, err := domain.NewQuantity(tc.input)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("NewQuantity(%d) error = %v, want %v", tc.input, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewQuantity(%d) returned %v", tc.input, err)
			}
			if quantity.Int() != tc.input {
				t.Errorf("NewQuantity(%d).Int() = %d", tc.input, quantity.Int())
			}
		})
	}
}

func TestMoneyArithmetic(t *testing.T) {
	t.Parallel()

	tenEUR, err := domain.NewMoney(1000, "EUR")
	if err != nil {
		t.Fatalf("NewMoney returned %v", err)
	}
	fiveEUR, err := domain.NewMoney(500, "EUR")
	if err != nil {
		t.Fatalf("NewMoney returned %v", err)
	}

	sum, err := tenEUR.Add(fiveEUR)
	if err != nil {
		t.Fatalf("Add returned %v", err)
	}
	if sum.Cents() != 1500 {
		t.Errorf("Add produced %d cents, want 1500", sum.Cents())
	}

	product, err := fiveEUR.Times(3)
	if err != nil {
		t.Fatalf("Times returned %v", err)
	}
	if product.Cents() != 1500 {
		t.Errorf("Times produced %d cents, want 1500", product.Cents())
	}

	if _, err := tenEUR.Times(-1); !errors.Is(err, domain.ErrInvalidAmount) {
		t.Errorf("multiplying by a negative factor must fail, got %v", err)
	}
}

func TestMoneyRejectsMixedCurrencies(t *testing.T) {
	t.Parallel()

	eur, err := domain.NewMoney(1000, "EUR")
	if err != nil {
		t.Fatalf("NewMoney returned %v", err)
	}
	usd, err := domain.NewMoney(1000, "USD")
	if err != nil {
		t.Fatalf("NewMoney returned %v", err)
	}

	if _, err := eur.Add(usd); !errors.Is(err, domain.ErrCurrencyMismatch) {
		t.Errorf("adding across currencies must fail with ErrCurrencyMismatch, got %v", err)
	}
}

func TestNewMoneyValidates(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		cents    int64
		currency string
		wantErr  error
	}{
		{name: "zero is allowed", cents: 0, currency: "EUR"},
		{name: "lower case currency is normalised", cents: 1, currency: "eur"},
		{name: "negative amount", cents: -1, currency: "EUR", wantErr: domain.ErrInvalidAmount},
		{name: "short currency", cents: 1, currency: "EU", wantErr: domain.ErrInvalidCurrency},
		{name: "long currency", cents: 1, currency: "EURO", wantErr: domain.ErrInvalidCurrency},
		{name: "non alphabetic currency", cents: 1, currency: "E1R", wantErr: domain.ErrInvalidCurrency},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			money, err := domain.NewMoney(tc.cents, tc.currency)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("NewMoney(%d, %q) error = %v, want %v", tc.cents, tc.currency, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewMoney(%d, %q) returned %v", tc.cents, tc.currency, err)
			}
			if money.Currency() != "EUR" && tc.currency != "EUR" && tc.currency != "eur" {
				t.Errorf("currency not normalised, got %q", money.Currency())
			}
		})
	}
}

// Identifiers are validated rather than generated in the domain, so that the
// domain stays deterministic and free of infrastructure concerns.
func TestNewOrderID(t *testing.T) {
	t.Parallel()

	const valid = "0b7c6f4e-9d3a-4f1b-8c2d-5a6e7f8b9c0d"

	id, err := domain.NewOrderID(valid)
	if err != nil {
		t.Fatalf("NewOrderID returned %v", err)
	}
	if id.String() != valid {
		t.Errorf("NewOrderID round trip = %q, want %q", id.String(), valid)
	}

	upper, err := domain.NewOrderID("0B7C6F4E-9D3A-4F1B-8C2D-5A6E7F8B9C0D")
	if err != nil {
		t.Fatalf("NewOrderID with upper case returned %v", err)
	}
	if upper.String() != valid {
		t.Errorf("identifiers must normalise to lower case, got %q", upper.String())
	}

	for _, invalid := range []string{"", "not-a-uuid", "0b7c6f4e9d3a4f1b8c2d5a6e7f8b9c0d", valid + "0"} {
		if _, err := domain.NewOrderID(invalid); !errors.Is(err, domain.ErrInvalidOrderID) {
			t.Errorf("NewOrderID(%q) error = %v, want ErrInvalidOrderID", invalid, err)
		}
	}
}

func TestNewCustomerID(t *testing.T) {
	t.Parallel()

	const valid = "11111111-2222-4333-8444-555555555555"

	id, err := domain.NewCustomerID(valid)
	if err != nil {
		t.Fatalf("NewCustomerID returned %v", err)
	}
	if id.String() != valid {
		t.Errorf("NewCustomerID round trip = %q", id.String())
	}
	if _, err := domain.NewCustomerID("nope"); !errors.Is(err, domain.ErrInvalidCustomerID) {
		t.Errorf("NewCustomerID rejects malformed input with ErrInvalidCustomerID, got %v", err)
	}
}

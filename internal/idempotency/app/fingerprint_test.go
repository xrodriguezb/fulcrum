package app_test

import (
	"bytes"
	"testing"

	"github.com/xrodriguezb/fulcrum/internal/idempotency/app"
)

// Two bodies that mean the same thing must fingerprint the same, or a client
// that reorders its JSON keys between retries would be told its key was reused.
func TestFingerprintIsStableAcrossEquivalentEncodings(t *testing.T) {
	t.Parallel()

	equivalent := []string{
		`{"customer_id":"c1","lines":[{"sku":"A-001","quantity":2}]}`,
		`{"lines":[{"sku":"A-001","quantity":2}],"customer_id":"c1"}`,
		"{\n  \"customer_id\" : \"c1\",\n  \"lines\" : [ { \"quantity\" : 2, \"sku\" : \"A-001\" } ]\n}",
	}

	first, err := app.Fingerprint([]byte(equivalent[0]))
	if err != nil {
		t.Fatalf("Fingerprint returned %v", err)
	}

	for _, body := range equivalent[1:] {
		other, err := app.Fingerprint([]byte(body))
		if err != nil {
			t.Fatalf("Fingerprint(%q) returned %v", body, err)
		}
		if !bytes.Equal(first, other) {
			t.Errorf("equivalent bodies produced different fingerprints:\n%q\n%q", equivalent[0], body)
		}
	}
}

// Array order is meaning, not formatting. Two lines in a different sequence are
// the same order, but the canonicaliser must not be the thing that decides that:
// reordering an array changes the document, so it changes the fingerprint.
func TestFingerprintDistinguishesDifferentDocuments(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		a    string
		b    string
	}{
		{
			name: "different value",
			a:    `{"quantity":2}`,
			b:    `{"quantity":3}`,
		},
		{
			name: "different key",
			a:    `{"quantity":2}`,
			b:    `{"qty":2}`,
		},
		{
			name: "array order",
			a:    `{"lines":[{"sku":"A-001"},{"sku":"B-002"}]}`,
			b:    `{"lines":[{"sku":"B-002"},{"sku":"A-001"}]}`,
		},
		{
			name: "number and string",
			a:    `{"quantity":2}`,
			b:    `{"quantity":"2"}`,
		},
		{
			name: "null and missing",
			a:    `{"note":null}`,
			b:    `{}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, err := app.Fingerprint([]byte(tc.a))
			if err != nil {
				t.Fatalf("Fingerprint(%q) returned %v", tc.a, err)
			}
			b, err := app.Fingerprint([]byte(tc.b))
			if err != nil {
				t.Fatalf("Fingerprint(%q) returned %v", tc.b, err)
			}
			if bytes.Equal(a, b) {
				t.Errorf("%q and %q produced the same fingerprint", tc.a, tc.b)
			}
		})
	}
}

func TestFingerprintRejectsMalformedJSON(t *testing.T) {
	t.Parallel()

	for _, body := range []string{``, `{`, `{"a":}`, `{"a":1}{"b":2}`, `not json`} {
		if _, err := app.Fingerprint([]byte(body)); err == nil {
			t.Errorf("Fingerprint(%q) returned no error", body)
		}
	}
}

func TestFingerprintLength(t *testing.T) {
	t.Parallel()

	sum, err := app.Fingerprint([]byte(`{"a":1}`))
	if err != nil {
		t.Fatalf("Fingerprint returned %v", err)
	}
	if len(sum) != 32 {
		t.Errorf("fingerprint is %d bytes, want 32 for sha-256", len(sum))
	}
}

func TestValidateKey(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		key     string
		max     int
		wantErr bool
	}{
		{name: "ordinary key", key: "6f1a2b3c-checkout-1", max: 255},
		{name: "empty", key: "", max: 255, wantErr: true},
		{name: "blank", key: "   ", max: 255, wantErr: true},
		{name: "too long", key: string(make([]byte, 300)), max: 255, wantErr: true},
		{name: "control character", key: "abc\ndef", max: 255, wantErr: true},
		{name: "non printable", key: "abc\x00def", max: 255, wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := app.ValidateKey(tc.key, tc.max)
			if tc.wantErr && err == nil {
				t.Errorf("ValidateKey(%q) returned no error", tc.key)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("ValidateKey(%q) returned %v", tc.key, err)
			}
		})
	}
}

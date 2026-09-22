package app_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/xrodriguezb/fulcrum/internal/idempotency/app"
)

// FuzzFingerprintIsStableUnderReencoding checks the property the whole
// idempotency contract rests on: a body that is re-encoded by a client library,
// a proxy or a retry must fingerprint identically.
//
// The oracle is Go's own encoder. Decoding and re-encoding a document changes
// its bytes, reorders its object keys and drops its whitespace, which is exactly
// the transformation a retry can apply. If the two fingerprints ever differ, a
// legitimate retry is answered with 422 and told it reused its key.
func FuzzFingerprintIsStableUnderReencoding(f *testing.F) {
	seeds := []string{
		`{"customer_id":"c1","lines":[{"sku":"A-001","quantity":2}]}`,
		`{"lines":[{"quantity":2,"sku":"A-001"}],"customer_id":"c1"}`,
		`{"a":1,"b":[1,2,3],"c":{"d":null,"e":false}}`,
		`{"unicode":"éü中","escaped":"line\nbreak\ttab"}`,
		`{"big":123456789012345678901234567890,"float":1.5,"exp":1e10}`,
		`{"nested":{"deep":{"deeper":{"deepest":[{"x":1}]}}}}`,
		`[]`,
		`{}`,
		`null`,
		`123`,
		`"a string"`,
	}
	for _, seed := range seeds {
		f.Add([]byte(seed))
	}

	f.Fuzz(func(t *testing.T, body []byte) {
		first, err := app.Fingerprint(body)
		if err != nil {
			// Anything that is not a single json document is rejected, which is
			// a valid outcome and not a property violation.
			return
		}

		var document any
		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.UseNumber()
		if err := decoder.Decode(&document); err != nil {
			t.Fatalf("Fingerprint accepted a body the decoder rejects: %q", body)
		}

		reencoded, err := json.Marshal(document)
		if err != nil {
			t.Fatalf("a decoded document could not be re-encoded: %v", err)
		}

		second, err := app.Fingerprint(reencoded)
		if err != nil {
			t.Fatalf("Fingerprint rejected its own re-encoding: %v (%q)", err, reencoded)
		}

		if !bytes.Equal(first, second) {
			t.Fatalf("re-encoding changed the fingerprint.\noriginal:   %q\nre-encoded: %q", body, reencoded)
		}

		if len(first) != 32 {
			t.Fatalf("fingerprint is %d bytes, want 32", len(first))
		}
	})
}

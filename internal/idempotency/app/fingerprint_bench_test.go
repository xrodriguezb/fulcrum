package app_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/xrodriguezb/fulcrum/internal/idempotency/app"
)

// Fingerprinting happens on every order creation, before any database work, so
// its cost is paid by every request including the ones that are about to be
// refused. These benchmarks exist to keep that cost visible: the canonical
// encoder walks the whole document and sorts every object's keys, which is
// linear but not free.
func BenchmarkFingerprint(b *testing.B) {
	sizes := []int{1, 10, 50}

	for _, lines := range sizes {
		body := []byte(orderBodyWithLines(lines))
		b.Run(fmt.Sprintf("lines=%d", lines), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(body)))
			for b.Loop() {
				if _, err := app.Fingerprint(body); err != nil {
					b.Fatalf("Fingerprint returned %v", err)
				}
			}
		})
	}
}

func orderBodyWithLines(lines int) string {
	var builder strings.Builder
	builder.WriteString(`{"customer_id":"11111111-2222-4333-8444-555555555555","lines":[`)
	for i := range lines {
		if i > 0 {
			builder.WriteByte(',')
		}
		fmt.Fprintf(&builder, `{"sku":"WIDGET-%03d","quantity":%d}`, i, i%5+1)
	}
	builder.WriteString(`]}`)
	return builder.String()
}

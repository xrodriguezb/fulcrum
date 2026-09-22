package infra

import "bytes"

// newReader exists so the strict decoder reads from a byte slice without the
// handler having to build a reader inline at every call site.
func newReader(body []byte) *bytes.Reader {
	return bytes.NewReader(body)
}

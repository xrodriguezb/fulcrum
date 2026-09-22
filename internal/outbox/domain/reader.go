package domain

import "bytes"

// newTrimReader returns a reader over raw with surrounding whitespace removed.
// The broker can deliver a payload with trailing newlines, and a strict decoder
// would otherwise reject a message that is perfectly well formed.
func newTrimReader(raw []byte) *bytes.Reader {
	return bytes.NewReader(bytes.TrimSpace(raw))
}

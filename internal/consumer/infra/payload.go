package infra

import (
	"encoding/json"
	"unicode/utf8"
)

// isJSON reports whether the bytes are a JSON value the database will accept.
func isJSON(payload []byte) bool {
	return json.Valid(payload)
}

// wrapAsJSON turns arbitrary bytes into a JSON document so the evidence survives
// in a jsonb column. Invalid UTF-8 is described rather than stored, because a
// byte sequence that is not text cannot be put inside a JSON string.
func wrapAsJSON(payload []byte) []byte {
	if !utf8.Valid(payload) {
		encoded, _ := json.Marshal(map[string]any{
			"undecodable": true,
			"bytes":       len(payload),
		})
		return encoded
	}

	encoded, err := json.Marshal(map[string]string{"raw": string(payload)})
	if err != nil {
		return []byte(`{"undecodable":true}`)
	}
	return encoded
}

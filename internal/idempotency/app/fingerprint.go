// Package app holds the idempotency use cases: fingerprinting a request,
// claiming a key, and completing or failing the claim.
package app

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"unicode"
)

// ErrMalformedBody means the request body is not a single JSON document.
var ErrMalformedBody = errors.New("request body is not a single json document")

// ErrInvalidKey means the idempotency key is unusable.
var ErrInvalidKey = errors.New("invalid idempotency key")

// Fingerprint returns the SHA-256 of a canonical encoding of the body.
//
// Canonical means object keys sorted and insignificant whitespace removed, so
// that a client re-encoding its retry is still recognised as the same request.
// Arrays are left in their original order on purpose: element order is part of
// what a JSON document says, and sorting it here would make a reordered request
// look like a replay of the original.
func Fingerprint(body []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()

	var document any
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrMalformedBody, err)
	}
	// A body with trailing content is two documents, and accepting it would let
	// two different requests share a fingerprint.
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w: trailing content after the json value", ErrMalformedBody)
	}

	var canonical bytes.Buffer
	if err := writeCanonical(&canonical, document); err != nil {
		return nil, err
	}

	sum := sha256.Sum256(canonical.Bytes())
	return sum[:], nil
}

func writeCanonical(out *bytes.Buffer, value any) error {
	switch typed := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)

		out.WriteByte('{')
		for i, key := range keys {
			if i > 0 {
				out.WriteByte(',')
			}
			encoded, err := json.Marshal(key)
			if err != nil {
				return fmt.Errorf("encode object key: %w", err)
			}
			out.Write(encoded)
			out.WriteByte(':')
			if err := writeCanonical(out, typed[key]); err != nil {
				return err
			}
		}
		out.WriteByte('}')
		return nil

	case []any:
		out.WriteByte('[')
		for i, element := range typed {
			if i > 0 {
				out.WriteByte(',')
			}
			if err := writeCanonical(out, element); err != nil {
				return err
			}
		}
		out.WriteByte(']')
		return nil

	case json.Number:
		// Numbers keep their literal form. Converting through float64 would make
		// 1 and 1.0 identical and would lose precision on large integers.
		out.WriteString(typed.String())
		return nil

	case string:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return fmt.Errorf("encode string: %w", err)
		}
		out.Write(encoded)
		return nil

	case bool:
		out.WriteString(strconv.FormatBool(typed))
		return nil

	case nil:
		out.WriteString("null")
		return nil

	default:
		return fmt.Errorf("%w: unexpected value of type %T", ErrMalformedBody, value)
	}
}

// ValidateKey checks that a client supplied key is usable as a primary key and
// as a log field. Control characters are rejected because they end up in
// operator-facing output.
func ValidateKey(key string, maxLength int) error {
	if len(key) == 0 {
		return fmt.Errorf("%w: the key is empty", ErrInvalidKey)
	}
	if len(key) > maxLength {
		return fmt.Errorf("%w: the key is longer than %d bytes", ErrInvalidKey, maxLength)
	}

	blank := true
	for _, r := range key {
		if unicode.IsControl(r) {
			return fmt.Errorf("%w: the key contains a control character", ErrInvalidKey)
		}
		if !unicode.IsSpace(r) {
			blank = false
		}
	}
	if blank {
		return fmt.Errorf("%w: the key is blank", ErrInvalidKey)
	}
	return nil
}

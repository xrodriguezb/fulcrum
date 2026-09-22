// Package idgen generates identifiers. It exists so that the domain can validate
// identifiers without depending on a uuid library, and so that a test can supply
// a deterministic generator.
package idgen

import "github.com/google/uuid"

// UUID generates random version 4 identifiers.
type UUID struct{}

// NewID returns a new identifier in canonical form.
func (UUID) NewID() string {
	return uuid.NewString()
}

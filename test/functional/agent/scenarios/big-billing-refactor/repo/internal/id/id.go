// Package id makes identifiers.
package id

import (
	"crypto/rand"
	"encoding/hex"
)

// New returns a random 128-bit identifier as 32 hex characters.
func New() string {
	var b [16]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

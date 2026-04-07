package id

import (
	"crypto/rand"
	"encoding/base64"
)

// New generates a base64url-encoded random ID (16 bytes = 128 bits of entropy).
// Result is 22 characters, no padding.
func New() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

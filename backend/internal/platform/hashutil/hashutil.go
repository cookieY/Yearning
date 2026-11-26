// Package hashutil provides canonical SHA-256 hex digest helpers.
package hashutil

import (
	"crypto/sha256"
	"encoding/hex"
)

// SHA256Hex returns the lowercase hexadecimal SHA-256 digest of b.
func SHA256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// SHA256HexString returns the lowercase hexadecimal SHA-256 digest of s.
func SHA256HexString(s string) string {
	return SHA256Hex([]byte(s))
}

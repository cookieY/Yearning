package legacy

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
)

func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func pbkdf2Key(password, salt []byte, iter, keyLen int) []byte {
	key, err := pbkdf2.Key(sha256.New, string(password), salt, iter, keyLen)
	if err != nil {
		return nil
	}
	return key
}

func encodeHex(b []byte) string { return hex.EncodeToString(b) }

func hmacEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

var _ = hmac.New // Retain hmac import for future domain-separated hash extensions

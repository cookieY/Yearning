package review

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

func jsonDecode(s string, out any) error {
	return json.Unmarshal([]byte(s), out)
}

func jsonEncode(v any) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// HashToken computes the SHA-256 hex digest of a session token (stored in auth_sessions.token_hash).
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

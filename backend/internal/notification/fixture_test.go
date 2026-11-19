//go:build integration

// fixture_test.go test environment helpers (env/keyring).
package notification

import (
	"fmt"
	"os"
	"testing"
)

func goGetenv(key string) string { return os.Getenv(key) }

// writeTestKeyring writes a test keyring (5-purpose keys; matching B6/B11 fixture conventions).
func writeTestKeyring(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	doc := fmt.Sprintf(`{"contract_version":1,"deployment_id":"b12-itest","keys":[
		{"key_id":"pay-1","purpose":"payload","status":"encrypt_decrypt","created_at":"2026-08-25T00:00:00Z","key_base64":"%s"},
		{"key_id":"fp-1","purpose":"sql_fingerprint","status":"encrypt_decrypt","created_at":"2026-08-25T00:00:00Z","key_base64":"%s"},
		{"key_id":"aff-1","purpose":"cursor_affinity","status":"encrypt_decrypt","created_at":"2026-08-25T00:00:00Z","key_base64":"%s"}]}`,
		b64(make32(1)), b64(make32(2)), b64(make32(3)))
	path := dir + "/keyring.json"
	if err := os.WriteFile(path, []byte(doc), 0o400); err != nil {
		t.Fatal(err)
	}
	return path
}

func make32(seed byte) []byte {
	out := make([]byte, 32)
	for i := range out {
		out[i] = seed
	}
	return out
}

const b64Alpha = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"

func b64(b []byte) string {
	var out []byte
	for i := 0; i < len(b); i += 3 {
		var chunk [3]byte
		n := copy(chunk[:], b[i:])
		var v uint32
		for j := 0; j < n; j++ {
			v |= uint32(chunk[j]) << (8 * (2 - j))
		}
		for j := 0; j < 4; j++ {
			if j <= n {
				out = append(out, b64Alpha[(v>>(6*(3-j)))&0x3f])
			} else {
				out = append(out, '=')
			}
		}
	}
	return string(out)
}

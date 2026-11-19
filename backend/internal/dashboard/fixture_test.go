//go:build integration

// fixture_test.go test environment helpers.
package dashboard

import (
	"fmt"
	"os"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/crypto"
)

func osGetenv(k string) string { return os.Getenv(k) }

func dsnOf(password string) string {
	host := os.Getenv("YEARNING_TEST_DATABASE_HOST")
	if host == "" {
		host = "localhost"
	}
	port := os.Getenv("YEARNING_TEST_DATABASE_PORT")
	if port == "" {
		port = "54330"
	}
	user := os.Getenv("YEARNING_TEST_DATABASE_USER")
	if user == "" {
		user = "yearning"
	}
	name := os.Getenv("YEARNING_TEST_DATABASE_DBNAME")
	if name == "" {
		name = "yearning_v4"
	}
	return fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=disable", host, port, user, password, name)
}

// writeKeyring writes a test keyring.
func writeKeyring(t *testing.T) string {
	t.Helper()
	doc := fmt.Sprintf(`{"contract_version":1,"deployment_id":"b12-itest","keys":[
		{"key_id":"pay-1","purpose":"payload","status":"encrypt_decrypt","created_at":"2026-08-25T00:00:00Z","key_base64":"%s"}]}`,
		b64(make32(1)))
	path := t.TempDir() + "/keyring.json"
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

var _ = crypto.PurposePayload

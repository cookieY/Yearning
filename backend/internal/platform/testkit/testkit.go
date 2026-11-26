// Package testkit centralizes deterministic test keyrings, environment helpers,
// and shared PostgreSQL integration connection/lock primitives across backend tests.
package testkit

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/crypto"
)

// EnvOr returns the value of environment variable key when non-empty, or fallback otherwise.
func EnvOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// Key32 returns a deterministic 32-byte key slice derived from seed.
// Different seeds produce distinct 32-byte materials so multi-purpose keyrings
// satisfy cross-purpose domain separation.
func Key32(seed byte) []byte {
	out := make([]byte, 32)
	for i := range out {
		out[i] = seed + byte(i)
	}
	return out
}

// WriteKeyring writes a mode-0400 keyring file covering all contracted AEAD and HMAC
// purposes (payload, oidc_state, identifier_token, settings_impact_token,
// sql_fingerprint, cursor_affinity) into t.TempDir() and returns its path.
func WriteKeyring(t testing.TB) string {
	t.Helper()
	purposes := []struct {
		id      string
		purpose string
		seed    byte
	}{
		{id: "pay-1", purpose: crypto.PurposePayload, seed: 1},
		{id: "state-1", purpose: crypto.HMACPurposeOIDCState, seed: 17},
		{id: "ident-1", purpose: "identifier_token", seed: 33},
		{id: "sit-1", purpose: "settings_impact_token", seed: 49},
		{id: "fp-1", purpose: "sql_fingerprint", seed: 65},
		{id: "aff-1", purpose: "cursor_affinity", seed: 81},
	}
	keys := make([]map[string]any, 0, len(purposes))
	for _, p := range purposes {
		keys = append(keys, map[string]any{
			"key_id":     p.id,
			"purpose":    p.purpose,
			"status":     string(crypto.StatusEncryptDecrypt),
			"created_at": "2026-08-25T00:00:00Z",
			"key_base64": base64.StdEncoding.EncodeToString(Key32(p.seed)),
		})
	}
	doc := map[string]any{
		"contract_version": 1,
		"deployment_id":    "yearningx-testkit",
		"keys":             keys,
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("testkit: marshal keyring: %v", err)
	}
	path := filepath.Join(t.TempDir(), "keyring.json")
	if err := os.WriteFile(path, raw, 0o400); err != nil {
		t.Fatalf("testkit: write keyring: %v", err)
	}
	return path
}

// NewCrypter loads a full 6-purpose temporary keyring and returns its Crypter.
func NewCrypter(t testing.TB) *crypto.Crypter {
	t.Helper()
	ring, err := crypto.LoadKeyring(WriteKeyring(t))
	if err != nil {
		t.Fatalf("testkit: load keyring: %v", err)
	}
	crypter, err := crypto.NewCrypter(ring)
	if err != nil {
		t.Fatalf("testkit: new crypter: %v", err)
	}
	return crypter
}

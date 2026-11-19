// rawreveal_seal_test.go SEC-0042 regression: raw evidence envelope write and read sides use the exact
// same convention so raw reveal succeeds on real Runner evidence (using fake keyprovider local keyring).
package application

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/crypto"
)

// newSealTestCrypter constructs a test keyring with payload (AEAD) and sql_fingerprint (HMAC)
// purposes (same recipe as package privacy tests; isolated deployment separates the AAD binding domain).
func newSealTestCrypter(t *testing.T) *crypto.Crypter {
	t.Helper()
	dir := t.TempDir()
	mk := func(seed byte) string {
		b := make([]byte, 32)
		for i := range b {
			b[i] = byte(i) + seed
		}
		return base64.StdEncoding.EncodeToString(b)
	}
	doc := map[string]any{
		"contract_version": 1,
		"deployment_id":    "test-deploy-rawreveal",
		"keys": []map[string]any{
			{"key_id": "payload-1", "purpose": "payload", "status": "encrypt_decrypt",
				"created_at": "2026-08-25T00:00:00Z", "key_base64": mk(1)},
			{"key_id": "fprint-1", "purpose": "sql_fingerprint", "status": "encrypt_decrypt",
				"created_at": "2026-08-25T00:00:00Z", "key_base64": mk(2)},
		},
	}
	raw, _ := json.Marshal(doc)
	path := filepath.Join(dir, "keyring.json")
	if err := os.WriteFile(path, raw, 0o400); err != nil {
		t.Fatal(err)
	}
	ring, err := crypto.LoadKeyring(path)
	if err != nil {
		t.Fatalf("LoadKeyring: %v", err)
	}
	c, err := crypto.NewCrypter(ring)
	if err != nil {
		t.Fatalf("NewCrypter: %v", err)
	}
	return c
}

// TestSEC0042RawEvidenceSealRevealRoundTrip write side (sealRawEvidence, shared by
// AgentRunner/FakeRunner) and read side (RevealRawEvidence with the same convention:
// open with factID AAD + verify HMAC(raw)) round-trip successfully.
func TestSEC0042RawEvidenceSealRevealRoundTrip(t *testing.T) {
	c := newSealTestCrypter(t)
	factID := "aaaa1111-0000-4000-8000-000000000001"
	// raw differs from the normalized fact (L7: raw preserves real identifiers).
	raw := []byte(`{"schema":"app_db","table":"users","tables":["users","order_items"]}`)
	env, hash, err := sealRawEvidence(c, factID, raw)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	// Read side uses the same convention: open with AAD = factID.
	plaintext, err := openPayload(c, "evidence_raw_payloads", factID, "raw_payload_ciphertext", env)
	if err != nil {
		t.Fatalf("reveal open (factID AAD) must succeed: %v", err)
	}
	if string(plaintext) != string(raw) {
		t.Fatalf("roundtrip payload mismatch")
	}
	// Read side uses the same convention: payload_hash = HMAC(raw).
	mac, err := c.HMAC(sqlHMACPurpose, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(hex.EncodeToString(mac), hash) {
		t.Fatalf("payload_hash mismatch: %s vs %s", hex.EncodeToString(mac), hash)
	}
}

// TestSEC0042OldWriteSideAADWouldFailRead legacy behavior regression anchor: when the write side used
// the raw row's own ID as AAD (or the normalized fact HMAC as payload_hash), the read side inevitably
// failed authentication or hash verification — proving raw reveal always failed closed prior to the fix,
// and preventing the write side from regressing to inline AAD assembly.
func TestSEC0042OldWriteSideAADWouldFailRead(t *testing.T) {
	c := newSealTestCrypter(t)
	factID := "bbbb2222-0000-4000-8000-000000000002"
	rawID := "cccc3333-0000-4000-8000-000000000003"
	raw := []byte(`{"table":"users"}`)
	normalized := []byte(`{"table":"table_1"}`)
	// Legacy write-side shape: AAD PrimaryKey = rawID.
	oldEnv, err := c.Seal(crypto.PurposePayload, raw, crypto.AAD{
		DeploymentID: c.DeploymentID(),
		Table:        "evidence_raw_payloads", PrimaryKey: rawID,
		Column: "raw_payload_ciphertext", Purpose: crypto.PurposePayload,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := openPayload(c, "evidence_raw_payloads", factID, "raw_payload_ciphertext", oldEnv); err == nil {
		t.Fatal("old rawID-bound AAD must fail the factID read path (fail closed)")
	}
	// Legacy payload_hash shape: stored the normalized fact HMAC, which mismatches read-side HMAC(raw).
	_, normalizedMAC, err := sealPayload(c, "evidence_facts", factID, "normalized_fact_ciphertext", normalized)
	if err != nil {
		t.Fatal(err)
	}
	mac, err := c.HMAC(sqlHMACPurpose, raw)
	if err != nil {
		t.Fatal(err)
	}
	if strings.EqualFold(hex.EncodeToString(mac), normalizedMAC) {
		t.Fatal("test construction broken: raw and normalized HMACs must differ")
	}
}

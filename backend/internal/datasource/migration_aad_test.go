package datasource

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cookieY/YearningX/backend/internal/crypto"
	legacy "github.com/cookieY/YearningX/backend/internal/migration/legacy"
)

// RCP-20260831-LEGACY-MIGRATION-AAD Option A acceptance: credential and TLS rows sealed by the
// migration Crypter with migration AAD are decrypted by the real runtime readers in this package
// (Factory.OpenCredential / openTLSMaterial, the shared decryption path used by query/execution/factory).
// See transforms_domain.go for migration AAD binding; mismatched AAD must fail authentication.

const (
	migTestDeploy = "deploy-mig-consumer"
	migTestKeyID  = "mig-key-1"
)

var migTestKey = []byte(strings.Repeat("q", 32))

func migRuntimeCrypter(t *testing.T) *crypto.Crypter {
	t.Helper()
	kr := map[string]any{
		"contract_version": 1,
		"deployment_id":    migTestDeploy,
		"keys": []map[string]any{{
			"key_id": migTestKeyID, "purpose": "payload",
			"status": "encrypt_decrypt", "created_at": "2026-01-01T00:00:00Z",
			"key_base64": base64.StdEncoding.EncodeToString(migTestKey),
		}},
	}
	raw, err := json.Marshal(kr)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "keyring.json")
	if err := os.WriteFile(p, raw, 0o400); err != nil {
		t.Fatal(err)
	}
	ring, err := crypto.LoadKeyring(p)
	if err != nil {
		t.Fatalf("load keyring: %v", err)
	}
	c, err := crypto.NewCrypter(ring)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func migFactory(t *testing.T) *Factory {
	t.Helper()
	f, err := NewFactory(migRuntimeCrypter(t))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// TestMigrationCredentialOpensAtRuntime verifies that a migration-sealed credential row
// decrypts username/password via Factory.OpenCredential (the runtime reader).
func TestMigrationCredentialOpensAtRuntime(t *testing.T) {
	c, err := legacy.NewCrypter(migTestKey, migTestKeyID, migTestDeploy, nil)
	if err != nil {
		t.Fatal(err)
	}
	credPlain, err := json.Marshal(map[string]string{"username": "mig-user", "password": "mig-pass"})
	if err != nil {
		t.Fatal(err)
	}
	rowID := "11111111-1111-4111-8111-111111111111"
	sealed, err := c.Seal(credPlain, c.AAD("datasource_credentials", rowID, "credential", "datasource_credential"))
	if err != nil {
		t.Fatal(err)
	}
	row := &DatasourceCredentialModel{
		ID: rowID, DatasourceID: "ds-1", Purpose: "execution",
		CredentialCiphertext: sealed.Ciphertext, CredentialNonce: sealed.Nonce,
		CredentialKeyID: sealed.KeyID, CredentialAlgorithm: sealed.Algorithm,
		Enabled: true,
	}
	payload, err := migFactory(t).OpenCredential(row)
	if err != nil {
		t.Fatalf("runtime reader rejected migration-sealed credential: %v", err)
	}
	if payload.Username != "mig-user" || payload.Password != "mig-pass" {
		t.Fatalf("payload mismatch: %+v", payload)
	}

	// Negative substitution case: placing the ciphertext under another row's AAD (cross-record swap) must fail authentication.
	other := *row
	other.ID = "22222222-2222-4222-8222-222222222222"
	if _, err := migFactory(t).OpenCredential(&other); err == nil {
		t.Fatal("cross-record swap must fail at runtime reader")
	}
}

// TestMigrationTLSMaterialOpensAtRuntime verifies that migration-sealed concatenated TLS PEM
// is decrypted and split via openTLSMaterial (the shared runtime reader path for query/execution/factory).
func TestMigrationTLSMaterialOpensAtRuntime(t *testing.T) {
	c, err := legacy.NewCrypter(migTestKey, migTestKeyID, migTestDeploy, nil)
	if err != nil {
		t.Fatal(err)
	}
	ca := "-----BEGIN CERTIFICATE-----\n" + strings.Repeat("Q", 64) + "\n-----END CERTIFICATE-----\n"
	cert := "-----BEGIN CERTIFICATE-----\n" + strings.Repeat("W", 64) + "\n-----END CERTIFICATE-----\n"
	keyPEM := "-----BEGIN PRIVATE KEY-----\n" + strings.Repeat("E", 64) + "\n-----END PRIVATE KEY-----\n"
	bundle := []byte(ca + "\n" + cert + "\n" + keyPEM)
	rowID := "33333333-3333-4333-8333-333333333333"
	sealed, err := c.Seal(bundle, c.AAD("datasource_tls_materials", rowID, "material", "datasource_tls"))
	if err != nil {
		t.Fatal(err)
	}
	kid, alg := sealed.KeyID, sealed.Algorithm
	row := &DatasourceTLSMaterialModel{
		ID: rowID, DatasourceID: "ds-1", Purpose: "execution", VerifyServer: true,
		MaterialCiphertext: sealed.Ciphertext, MaterialNonce: sealed.Nonce,
		MaterialKeyID: &kid, MaterialAlgorithm: &alg,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	mat, err := migFactory(t).openTLSMaterial(row)
	if err != nil {
		t.Fatalf("runtime reader rejected migration-sealed TLS material: %v", err)
	}
	if mat == nil {
		t.Fatal("nil material")
	}
	if len(mat.CAPEM) == 0 || len(mat.CertPEM) == 0 || len(mat.KeyPEM) == 0 {
		t.Fatalf("PEM split mismatch: CA=%d cert=%d key=%d", len(mat.CAPEM), len(mat.CertPEM), len(mat.KeyPEM))
	}
}

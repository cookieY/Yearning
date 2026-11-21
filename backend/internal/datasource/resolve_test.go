package datasource

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/cookieY/YearningX/backend/internal/crypto"
)

func generateTestCertPEM(t *testing.T, cn string) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func generateTestPKCS1KeyPEM(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
}

func TestSplitTLSMaterial_CanonicalRulesAndLastKeyWins(t *testing.T) {
	caPEM := generateTestCertPEM(t, "ca.local")
	clientPEM := generateTestCertPEM(t, "client.local")
	firstKey := generateTestPKCS1KeyPEM(t)
	secondKey := generateTestPKCS1KeyPEM(t)

	// 1. CA-only bundle (no private key) keeps all certs in CAPEM.
	caOnly, err := SplitTLSMaterial(true, "db.local", bytes.Join([][]byte{caPEM, clientPEM}, nil))
	if err != nil {
		t.Fatalf("CA-only SplitTLSMaterial: %v", err)
	}
	if len(caOnly.CertPEM) != 0 || len(caOnly.KeyPEM) != 0 || !bytes.Contains(caOnly.CAPEM, caPEM) || !bytes.Contains(caOnly.CAPEM, clientPEM) {
		t.Fatalf("unexpected CA-only split result: %+v", caOnly)
	}

	// 2. Single client cert + key leaves CAPEM empty (pure mTLS client material).
	singleMTLS, err := SplitTLSMaterial(false, "", bytes.Join([][]byte{clientPEM, firstKey}, nil))
	if err != nil {
		t.Fatalf("single-cert mTLS SplitTLSMaterial: %v", err)
	}
	if len(singleMTLS.CAPEM) != 0 || !bytes.Equal(singleMTLS.CertPEM, clientPEM) || !bytes.Equal(singleMTLS.KeyPEM, firstKey) {
		t.Fatalf("unexpected single-cert mTLS result: capem=%d cert=%v key=%v", len(singleMTLS.CAPEM), len(singleMTLS.CertPEM), len(singleMTLS.KeyPEM))
	}

	// 3. Multiple private keys: last private key wins (never concatenated), last cert is CertPEM, prior certs form CAPEM.
	multi, err := SplitTLSMaterial(true, "srv.local", bytes.Join([][]byte{caPEM, firstKey, clientPEM, secondKey}, nil))
	if err != nil {
		t.Fatalf("multi-key SplitTLSMaterial: %v", err)
	}
	if !bytes.Equal(multi.KeyPEM, secondKey) {
		t.Fatalf("expected last private key to win without concatenation")
	}
	if !bytes.Equal(multi.CertPEM, clientPEM) || !bytes.Equal(multi.CAPEM, caPEM) {
		t.Fatalf("expected CA cert in CAPEM and last cert in CertPEM")
	}
}

func TestOpenCredentialAndTLSEnvelopes(t *testing.T) {
	c := migRuntimeCrypter(t)

	// Valid credential envelope round-trip.
	credID := "cred-row-1"
	env, err := c.Seal(crypto.PurposePayload, []byte(`{"username":"ro_user","password":"secret"}`), crypto.AAD{
		DeploymentID: c.DeploymentID(),
		Table:        "datasource_credentials",
		PrimaryKey:   credID,
		Column:       "credential",
		Purpose:      "datasource_credential",
	})
	if err != nil {
		t.Fatal(err)
	}
	gotCred, err := OpenCredentialEnvelope(c, &DatasourceCredentialModel{
		ID:                   credID,
		CredentialCiphertext: env.Ciphertext,
		CredentialNonce:      env.Nonce,
		CredentialKeyID:      env.KeyID,
		CredentialAlgorithm:  env.Algorithm,
	})
	if err != nil || gotCred.Username != "ro_user" || gotCred.Password != "secret" {
		t.Fatalf("unexpected OpenCredentialEnvelope result: %+v err=%v", gotCred, err)
	}

	// Wrong row PrimaryKey fails AAD verification.
	if _, err := OpenCredentialEnvelope(c, &DatasourceCredentialModel{
		ID:                   "tampered-id",
		CredentialCiphertext: env.Ciphertext,
		CredentialNonce:      env.Nonce,
		CredentialKeyID:      env.KeyID,
	}); err == nil {
		t.Fatal("expected AAD mismatch failure on tampered credential ID")
	}

	// Nil / empty TLS material returns (nil, nil).
	if mat, err := OpenTLSEnvelope(c, nil); err != nil || mat != nil {
		t.Fatalf("expected nil,nil for nil TLS row, got %+v err=%v", mat, err)
	}
	emptyKeyID := ""
	if mat, err := OpenTLSEnvelope(c, &DatasourceTLSMaterialModel{ID: "tls-empty", MaterialKeyID: &emptyKeyID}); err != nil || mat != nil {
		t.Fatalf("expected nil,nil for empty TLS row, got %+v err=%v", mat, err)
	}

	// Guard checks on ResolveCredential / ResolveTLSMaterial.
	if _, err := ResolveCredential(context.Background(), nil, c, "ds-1", CredentialResolveOptions{Purposes: []string{"review"}}); err == nil {
		t.Fatal("expected error for nil db")
	}
	if _, err := ResolveTLSMaterial(context.Background(), nil, c, "ds-1"); err == nil {
		t.Fatal("expected error for nil db")
	}
	if _, err := OpenCredentialEnvelope(c, nil); !errors.Is(err, ErrCredentialMissing) {
		t.Fatalf("expected ErrCredentialMissing for nil credential row, got %v", err)
	}
}

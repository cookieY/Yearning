package application

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/datasource"
)

func newTestCrypterForQueryBootstrap(t *testing.T) *crypto.Crypter {
	t.Helper()
	makeKey := func(b byte) string {
		buf := make([]byte, 32)
		for i := range buf {
			buf[i] = b
		}
		return base64.StdEncoding.EncodeToString(buf)
	}
	keyPath := t.TempDir() + "/keyring.json"
	doc := fmt.Sprintf(`{"contract_version":1,"deployment_id":"q-boot","keys":[
		{"key_id":"pay-1","purpose":"payload","status":"encrypt_decrypt","created_at":"2026-08-25T00:00:00Z","key_base64":"%s"},
		{"key_id":"id-1","purpose":"identifier_token","status":"encrypt_decrypt","created_at":"2026-08-25T00:00:00Z","key_base64":"%s"},
		{"key_id":"fp-1","purpose":"sql_fingerprint","status":"encrypt_decrypt","created_at":"2026-08-25T00:00:00Z","key_base64":"%s"},
		{"key_id":"cur-1","purpose":"cursor_affinity","status":"encrypt_decrypt","created_at":"2026-08-25T00:00:00Z","key_base64":"%s"}]}`,
		makeKey(1), makeKey(2), makeKey(3), makeKey(4))
	if err := os.WriteFile(keyPath, []byte(doc), 0o400); err != nil {
		t.Fatal(err)
	}
	ring, err := crypto.LoadKeyring(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	crypter, err := crypto.NewCrypter(ring)
	if err != nil {
		t.Fatal(err)
	}
	return crypter
}

func TestIdentityBootstrapPureHelpers(t *testing.T) {
	if isValidIdentityFingerprint("") {
		t.Fatal("empty string must not be valid")
	}
	if isValidIdentityFingerprint(strings.Repeat("A", 64)) {
		t.Fatal("uppercase hex must not be valid")
	}
	if isValidIdentityFingerprint(strings.Repeat("g", 64)) {
		t.Fatal("non-hex must not be valid")
	}
	valid := "0123456789abcdef" + strings.Repeat("a0", 24)
	if !isValidIdentityFingerprint(valid) {
		t.Fatalf("expected %q to be valid", valid)
	}

	crypter := newTestCrypterForQueryBootstrap(t)
	if _, err := computeDatasourceIdentityFingerprint(nil, nil, ""); err == nil {
		t.Fatal("expected error on nil crypter")
	}
	ds := &datasource.DatasourceModel{
		Engine: "postgresql", Host: "db.internal", Port: 5432,
	}
	if _, err := computeDatasourceIdentityFingerprint(crypter, ds, ""); err == nil {
		t.Fatal("expected error on empty instance marker")
	}
	fp, err := computeDatasourceIdentityFingerprint(crypter, ds, "pg:sys-123")
	if err != nil || !isValidIdentityFingerprint(fp) {
		t.Fatalf("computeDatasourceIdentityFingerprint: fp=%q err=%v", fp, err)
	}
	factory, err := datasource.NewFactory(crypter)
	if err != nil {
		t.Fatal(err)
	}
	wantFP, err := factory.IdentityFingerprint(ds, "pg:sys-123")
	if err != nil || fp != wantFP {
		t.Fatalf("fingerprint parity mismatch: got=%q want=%q err=%v", fp, wantFP, err)
	}

	// Guard branches on nil / non-UUID arguments must be safe no-ops.
	var nilSvc *Services
	nilSvc.ensureDatasourcesIdentity(context.Background(), uuid.NewString(), uuid.NewString(), nil)
	emptySvc := &Services{Crypter: crypter}
	emptySvc.ensureDatasourcesIdentity(context.Background(), "bad-uuid", uuid.NewString(), nil)
	emptySvc.bootstrapSingleDatasourceIdentity(context.Background(), nil)
	emptySvc.bootstrapSingleDatasourceIdentity(context.Background(), &datasource.DatasourceModel{Enabled: false})
	emptySvc.bootstrapSingleDatasourceIdentity(context.Background(), &datasource.DatasourceModel{
		Enabled: true, Host: "db.internal", Port: 3306, CompatibilityMode: "oracle",
	})
}

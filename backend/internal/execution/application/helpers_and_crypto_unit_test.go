package application

import (
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/datasource"
	"github.com/cookieY/YearningX/backend/internal/execution/domain"
	"github.com/cookieY/YearningX/backend/internal/platform/hashutil"
	"github.com/cookieY/YearningX/backend/internal/platform/ptr"
	"github.com/cookieY/YearningX/backend/internal/platform/txutil"
)

func TestExecutionApplicationUnitHelpers(t *testing.T) {
	if _, err := NewServices(nil, nil, nil); err == nil {
		t.Fatal("expected error on nil NewServices dependencies")
	}

	if txutil.IsSerializationFailure(nil) {
		t.Fatal("expected false on nil error")
	}
	if !txutil.IsSerializationFailure(&pgconn.PgError{Code: "40001"}) {
		t.Fatal("expected true on 40001")
	}
	if !txutil.IsSerializationFailure(&pgconn.PgError{Code: "40P01"}) {
		t.Fatal("expected true on 40P01")
	}
	if !txutil.IsUniqueViolation(gorm.ErrDuplicatedKey) {
		t.Fatal("expected true on gorm.ErrDuplicatedKey")
	}
	if !txutil.IsUniqueViolation(&pgconn.PgError{Code: "23505"}) {
		t.Fatal("expected true on 23505")
	}
	if txutil.IsUniqueViolation(&pgconn.PgError{Code: "23503"}) {
		t.Fatal("expected false on 23503")
	}

	if id1, id2 := newID(), newID(); id1 == "" || id2 == "" || id1 == id2 {
		t.Fatalf("newID returned non-unique or empty IDs: %q, %q", id1, id2)
	}
	if ts := nowUTC(); ts.Location() != time.UTC || ts.IsZero() {
		t.Fatalf("nowUTC expected non-zero UTC time, got %v", ts)
	}
	act := auditlog.UserActor("u-1", "alice")
	if act.Kind != "user" || act.UserID != "u-1" || act.Username != "alice" {
		t.Fatalf("auditlog.UserActor mismatch: %+v", act)
	}
	if h := txutil.RequestHash("a", "b"); h == "" || h != txutil.RequestHash("a", "b") || h == txutil.RequestHash("b", "a") {
		t.Fatalf("txutil.RequestHash determinism/order check failed: %q", h)
	}
	if h := hashutil.SHA256Hex([]byte("SELECT 1")); len(h) != 64 {
		t.Fatalf("SHA256Hex expected 64-char hex, got %q", h)
	}

	if ptr.Deref[string](nil) != "" {
		t.Fatal("expected empty string on nil ptr.Deref")
	}
	sVal := "hello"
	if ptr.Deref(&sVal) != "hello" {
		t.Fatal("expected 'hello' on non-nil ptr.Deref")
	}
	if *ptr.To("x") != "x" {
		t.Fatal("ptr.To mismatch")
	}

	if canonicalOverrides(nil) != "" {
		t.Fatal("expected empty canonicalOverrides on nil")
	}
	if got := canonicalOverrides(map[string]any{"b": 2, "a": 1}); got != "a=1;b=2" {
		t.Fatalf("canonicalOverrides got %q", got)
	}
	sets := map[string]any{"col_b": 20, "col_a": 10}
	if got := setClause(sets); got != "col_a = ?, col_b = ?" {
		t.Fatalf("setClause got %q", got)
	}
	if vals := setValues(sets); len(vals) != 2 || vals[0] != 10 || vals[1] != 20 {
		t.Fatalf("setValues got %v", vals)
	}

	if deName(errors.New("plain")) != "" {
		t.Fatalf("deName plain got %q", deName(errors.New("plain")))
	}
	if deName(domain.ErrIllegal()) == "" {
		t.Fatal("deName domain error empty")
	}
	if domainError(errors.New("plain")) != nil {
		t.Fatal("expected nil domainError for plain error")
	}
	if domainError(domain.ErrIllegal()) == nil {
		t.Fatal("expected non-nil domainError")
	}

	// splitTLSPEM with cert + key + CA
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("ca-bytes")})
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("cert-bytes")})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("key-bytes")})
	combined := append(append([]byte{}, caPEM...), certPEM...)
	combined = append(combined, keyPEM...)
	mat, err := datasource.SplitTLSMaterial(true, "db.local", combined)
	if err != nil || mat == nil || len(mat.CertPEM) == 0 || len(mat.KeyPEM) == 0 || len(mat.CAPEM) == 0 {
		t.Fatalf("SplitTLSMaterial: mat=%+v err=%v", mat, err)
	}

	// Crypter-backed helpers on Services
	dir := t.TempDir()
	mk := func(seed byte) string {
		out := make([]byte, 32)
		for i := range out {
			out[i] = seed
		}
		return base64.StdEncoding.EncodeToString(out)
	}
	doc := fmt.Sprintf(`{"contract_version":1,"deployment_id":"exec-cov-test","keys":[
	{"key_id":"pay-1","purpose":"payload","status":"encrypt_decrypt","created_at":"2026-08-25T00:00:00Z","key_base64":"%s"},
	{"key_id":"fp-1","purpose":"sql_fingerprint","status":"encrypt_decrypt","created_at":"2026-08-25T00:00:00Z","key_base64":"%s"}]}`,
		mk(1), mk(4))
	krPath := filepath.Join(dir, "keyring.json")
	if err := os.WriteFile(krPath, []byte(doc), 0o400); err != nil {
		t.Fatal(err)
	}
	ring, err := crypto.LoadKeyring(krPath)
	if err != nil {
		t.Fatal(err)
	}
	crypter, err := crypto.NewCrypter(ring)
	if err != nil {
		t.Fatal(err)
	}

	svc := &Services{
		Crypter:  crypter,
		instance: "test-node/1",
		OscBinaries: map[string]*OscBinaryIdentity{
			"gh-ost": {Version: "1.1.6", SHA256: "abc", Path: "in-process"},
		},
	}
	if svc.instanceOwner() != "test-node/1" {
		t.Fatalf("instanceOwner got %q", svc.instanceOwner())
	}
	svc.SetCancelWatchdogDelayForTest(25 * time.Millisecond)
	if svc.cancelWatchdogDelay() != 25*time.Millisecond {
		t.Fatalf("cancelWatchdogDelay override got %v", svc.cancelWatchdogDelay())
	}
	svc.SetCancelWatchdogDelayForTest(0)
	if svc.cancelWatchdogDelay() <= 0 {
		t.Fatalf("default cancelWatchdogDelay must be positive, got %v", svc.cancelWatchdogDelay())
	}

	if svc.oscBinaryFor(domain.KindGhOst) == nil {
		t.Fatal("expected non-nil oscBinaryFor gh_ost")
	}
	if svc.oscBinaryFor(domain.KindPtOsc) != nil {
		t.Fatal("expected nil oscBinaryFor unregistered KindPtOsc")
	}
	if (&Services{}).oscBinaryFor(domain.KindGhOst) != nil {
		t.Fatal("expected nil oscBinaryFor empty Services")
	}

	ct, nonce, keyID, err := svc.sealErrorDetail("att-1", 1, []byte("err detail"))
	if err != nil {
		t.Fatalf("sealErrorDetail: %v", err)
	}
	plain, err := svc.openErrorDetail("att-1", 1, ct, nonce, keyID)
	if err != nil || string(plain) != "err detail" {
		t.Fatalf("openErrorDetail: plain=%q err=%v", string(plain), err)
	}

	macHex, err := svc.hmacSQL([]byte("SELECT 1;"))
	if err != nil || macHex == "" {
		t.Fatalf("hmacSQL: mac=%q err=%v", macHex, err)
	}
	env, err := crypter.Seal(crypto.PurposePayload, []byte("SELECT 1;"), crypto.AAD{
		DeploymentID: crypter.DeploymentID(),
		Table:        "order_sql_payloads",
		PrimaryKey:   "ord-1",
		Column:       "sql_ciphertext",
		Purpose:      crypto.PurposePayload,
	})
	if err != nil {
		t.Fatal(err)
	}
	sqlBytes, err := svc.openOrderSQL("ord-1", &orderSQLRow{
		SQLCiphertext: env.Ciphertext,
		SQLNonce:      env.Nonce,
		SQLKeyID:      env.KeyID,
		SQLAlgorithm:  env.Algorithm,
		PlaintextHMAC: macHex,
	})
	if err != nil || string(sqlBytes) != "SELECT 1;" {
		t.Fatalf("openOrderSQL: sql=%q err=%v", string(sqlBytes), err)
	}
	if _, err := svc.openOrderSQL("ord-1", &orderSQLRow{
		SQLCiphertext: []byte("corrupted"),
		SQLNonce:      env.Nonce,
		SQLKeyID:      env.KeyID,
		SQLAlgorithm:  env.Algorithm,
		PlaintextHMAC: macHex,
	}); err == nil {
		t.Fatal("expected error on corrupted ciphertext in openOrderSQL")
	}

	capErr := domain.ErrSystemCapacity()
	if capErr.Code != domain.CodeSystemCapacity || capErr.Name != "SYSTEM_CAPACITY_EXHAUSTED" {
		t.Fatalf("ErrSystemCapacity = %+v, want 1012 SYSTEM_CAPACITY_EXHAUSTED", capErr)
	}
}

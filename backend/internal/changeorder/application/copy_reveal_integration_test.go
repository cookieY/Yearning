//go:build integration

// copy_reveal_integration_test.go SEC-0035 ownership verification integration test:
// The referenced source reveal in RecordSQLCopy must be issued by a
// workorder.sql_revealed audit record for the current user and work order — foreign user
// reveals, foreign order reveals, and fabricated/empty revealIDs all return
// RESOURCE_NOT_FOUND without writing a copy audit.
//
// Requires a real PostgreSQL instance (YEARNING_TEST_DATABASE_*; shares the database
// with the httpapi fixture, mutually excluded via the same file lock). Compiles when
// local DB is unavailable and runs in CI.
package application

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	"github.com/cookieY/YearningX/backend/internal/crypto"
)

type copyFx struct {
	db  *gorm.DB
	svc *Services
	seq int
}

func newCopyFx(t *testing.T) *copyFx {
	t.Helper()
	acquireCopyLock(t)
	db, err := gorm.Open(postgres.Open(copyDSN(t)), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		t.Fatalf("postgres connect: %v", err)
	}
	if sqlDB, serr := db.DB(); serr == nil {
		t.Cleanup(func() { _ = sqlDB.Close() })
	}
	for _, tbl := range []string{"change_orders", "audit_events"} {
		if err := db.Exec("truncate table " + tbl).Error; err != nil {
			t.Fatalf("truncate %s: %v", tbl, err)
		}
	}
	dir := t.TempDir()
	mk := func(seed byte) string {
		out := make([]byte, 32)
		for i := range out {
			out[i] = seed
		}
		return base64.StdEncoding.EncodeToString(out)
	}
	doc := fmt.Sprintf(`{"contract_version":1,"deployment_id":"copy-itest","keys":[
	{"key_id":"pay-1","purpose":"payload","status":"encrypt_decrypt","created_at":"2026-08-25T00:00:00Z","key_base64":"%s"},
	{"key_id":"fp-1","purpose":"sql_fingerprint","status":"encrypt_decrypt","created_at":"2026-08-25T00:00:00Z","key_base64":"%s"}]}`,
		mk(1), mk(4))
	path := filepath.Join(dir, "keyring.json")
	if err := os.WriteFile(path, []byte(doc), 0o400); err != nil {
		t.Fatal(err)
	}
	ring, err := crypto.LoadKeyring(path)
	if err != nil {
		t.Fatal(err)
	}
	crypter, err := crypto.NewCrypter(ring)
	if err != nil {
		t.Fatal(err)
	}
	recorder, err := auditlog.NewRecorder(db)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := NewServices(db, crypter, recorder)
	if err != nil {
		t.Fatal(err)
	}
	return &copyFx{db: db, svc: svc}
}

func acquireCopyLock(t *testing.T) {
	t.Helper()
	path := os.TempDir() + "/yearningx-itest-shared.lock"
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return
	}
	t.Cleanup(func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	})
}

func copyDSN(t *testing.T) string {
	t.Helper()
	password := os.Getenv("YEARNING_TEST_DATABASE_PASSWORD")
	if password == "" {
		t.Skip("integration PostgreSQL not configured: set YEARNING_TEST_DATABASE_PASSWORD")
	}
	env := func(key, def string) string {
		if v := os.Getenv(key); v != "" {
			return v
		}
		return def
	}
	return fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
		env("YEARNING_TEST_DATABASE_HOST", "localhost"),
		env("YEARNING_TEST_DATABASE_PORT", "54330"),
		env("YEARNING_TEST_DATABASE_USER", "yearning"),
		password,
		env("YEARNING_TEST_DATABASE_DBNAME", "yearning_v4"),
		env("YEARNING_TEST_DATABASE_SSLMODE", "disable"))
}

func repeatHex64(s string) string {
	out := make([]byte, 0, 64)
	for len(out) < 64 {
		out = append(out, s...)
	}
	return string(out[:64])
}

// seedCopyOrder creates a minimal work order (submitter=userID).
func (fx *copyFx) seedCopyOrder(t *testing.T, userID string) string {
	t.Helper()
	fx.seq++
	orderID := uuid.NewString()
	if err := fx.db.Exec(`
		insert into change_orders (id, display_number, submitter_user_id_snapshot,
			submitter_username_snapshot, submitter_display_name_snapshot, title,
			description, state, current_stage_position, sql_hmac, snapshot_hash,
			version, submitted_at)
		values (?, ?, ?, 'u1', 'User One', 'copy-itest', '',
			'running', 1, ?, ?, 1, now())`,
		orderID, fmt.Sprintf("YR-20260102-%06d", fx.seq), userID,
		repeatHex64("ab"), repeatHex64("cd")).Error; err != nil {
		t.Fatal(err)
	}
	return orderID
}

// seedRevealAudit issues a sql_revealed audit record (actor's reveal on order;
// expires_at is constrained by audit_retention_exact = occurred_at + 90 days).
func (fx *copyFx) seedRevealAudit(t *testing.T, actorID, orderID, revealID string) {
	t.Helper()
	if err := fx.db.Exec(`
		insert into audit_events (id, event_type, actor_kind, actor_id,
			actor_username_snapshot, resource_type, resource_id, action, outcome,
			metadata, occurred_at, expires_at)
		values (?, 'workorder.sql_revealed', 'user', ?, 'u1', 'change_order', ?,
			'reveal', 'succeeded', ?::jsonb, now(), now() + interval '90 days')`,
		uuid.NewString(), actorID, orderID,
		fmt.Sprintf(`{"reveal_id":%q,"purpose":"review"}`, revealID)).Error; err != nil {
		t.Fatal(err)
	}
}

func (fx *copyFx) copyAudits(t *testing.T, orderID string) int {
	t.Helper()
	var n int
	if err := fx.db.Raw(
		`select count(*) from audit_events where event_type = 'workorder.sql_copied'
		  and resource_id = ?`, orderID).Scan(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRecordSQLCopyVerifiesRevealOwnership(t *testing.T) {
	fx := newCopyFx(t)
	user1, user2 := uuid.NewString(), uuid.NewString()
	order1 := fx.seedCopyOrder(t, user1)
	order2 := fx.seedCopyOrder(t, user1)
	r1 := uuid.NewString() // Legitimate reveal by user1 on order1
	fx.seedRevealAudit(t, user1, order1, r1)
	rOther := uuid.NewString() // Reveal by user2 on order1 (another user)
	fx.seedRevealAudit(t, user2, order1, rOther)
	actor1 := Actor{UserID: user1, Username: "u1"}

	// Legitimate: same user and same order reveal -> audit persisted.
	if err := fx.svc.RecordSQLCopy(context.Background(), actor1, RequestMeta{}, order1, r1); err != nil {
		t.Fatalf("legitimate copy must pass: %v", err)
	}
	if n := fx.copyAudits(t, order1); n != 1 {
		t.Fatalf("copy audits=%d want 1", n)
	}

	// Foreign user reveal / foreign order reveal / fabricated reveal / empty reveal — all rejected with zero new audit records.
	denied := []struct {
		name     string
		orderID  string
		revealID string
	}{
		{"foreign_user_reveal", order1, rOther},
		{"foreign_order_reveal", order2, r1},
		{"fabricated_reveal", order1, uuid.NewString()},
		{"empty_reveal", order1, ""},
	}
	for _, tc := range denied {
		t.Run(tc.name, func(t *testing.T) {
			before := fx.copyAudits(t, tc.orderID)
			if err := fx.svc.RecordSQLCopy(context.Background(), actor1, RequestMeta{}, tc.orderID, tc.revealID); err == nil {
				t.Fatal("copy with foreign reveal must be rejected")
			}
			if after := fx.copyAudits(t, tc.orderID); after != before {
				t.Fatalf("rejected copy must not write audit: %d -> %d", before, after)
			}
		})
	}
}

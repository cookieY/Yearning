//go:build integration

package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/cookieY/YearningX/backend/internal/platform/addrguard"
)

func acquireCLIIntegrationLock(t *testing.T) {
	t.Helper()
	for _, name := range []string{"/yearningx-itest-shared.lock", "/yearningx-b13-assistant-test.lock"} {
		f, err := os.OpenFile(os.TempDir()+name, os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			t.Fatalf("open lock %s: %v", name, err)
		}
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
			_ = f.Close()
			t.Fatalf("flock %s: %v", name, err)
		}
		file := f
		t.Cleanup(func() {
			_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
			_ = file.Close()
		})
	}
}

func makeCLITestKey32(seed byte) string {
	b := make([]byte, 32)
	for i := range b {
		b[i] = seed + byte(i)
	}
	return base64.StdEncoding.EncodeToString(b)
}

func TestCLICoverageMigrateAndReadinessIntegration(t *testing.T) {
	acquireCLIIntegrationLock(t)
	t.Cleanup(addrguard.SetLoopbackBypassForTest(true))

	pw := os.Getenv("YEARNING_TEST_DATABASE_PASSWORD")
	host := os.Getenv("YEARNING_TEST_DATABASE_HOST")
	if host == "" {
		host = "127.0.0.1"
	}
	port := os.Getenv("YEARNING_TEST_DATABASE_PORT")
	if port == "" {
		port = "54332"
	}
	user := os.Getenv("YEARNING_TEST_DATABASE_USER")
	if user == "" {
		user = "yearning"
	}
	dbName := os.Getenv("YEARNING_TEST_DATABASE_DBNAME")
	if dbName == "" {
		dbName = "yearning_b13_test"
	}
	caFile := os.Getenv("YEARNING_TEST_PG_CA_FILE")
	if dsnEnv := os.Getenv("YEARNING_TEST_PG_DSN"); dsnEnv != "" {
		if u, err := url.Parse(dsnEnv); err == nil {
			if u.Hostname() != "" {
				host = u.Hostname()
			}
			if u.Port() != "" {
				port = u.Port()
			}
			if u.User != nil {
				if u.User.Username() != "" {
					user = u.User.Username()
				}
				if p, ok := u.User.Password(); ok && p != "" && pw == "" {
					pw = p
				}
			}
			if trimmed := strings.TrimPrefix(u.Path, "/"); trimmed != "" {
				dbName = trimmed
			}
			if caFile == "" {
				caFile = u.Query().Get("sslrootcert")
			}
		}
	}
	if pw == "" {
		t.Skip("PostgreSQL test password not configured (set YEARNING_TEST_DATABASE_PASSWORD or YEARNING_TEST_PG_DSN)")
	}

	dsn := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=disable", host, port, user, pw, dbName)
	gormDB, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		t.Skipf("PostgreSQL test DB not reachable: %v", err)
	}
	sqlDB, err := gormDB.DB()
	if err != nil || sqlDB.Ping() != nil {
		t.Skip("PostgreSQL test DB ping failed")
	}
	defer sqlDB.Close()

	ctx := context.Background()
	// Exercise schemaLedgerHealthy, databaseRoleSelfCheck, and gormSchemaLedger.MaxAppliedVersion
	_ = schemaLedgerHealthy(ctx, gormDB)
	_ = databaseRoleSelfCheck(ctx, gormDB)
	_, _ = (gormSchemaLedger{db: gormDB}).MaxAppliedVersion(ctx)

	// Prepare migration config file for runMigrate schema & legacy snapshot commands
	tmp := t.TempDir()
	pwFile := filepath.Join(tmp, "pg_pw.txt")
	if err := os.WriteFile(pwFile, []byte(pw), 0o600); err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(tmp, "enc_key.txt")
	if err := os.WriteFile(keyFile, []byte("0123456789abcdef0123456789abcdef"), 0o600); err != nil {
		t.Fatal(err)
	}
	legSecretFile := filepath.Join(tmp, "leg_secret.txt")
	if err := os.WriteFile(legSecretFile, []byte("1234567890123456"), 0o600); err != nil {
		t.Fatal(err)
	}

	targetTLSJSON := `{"mode":"disable"}`
	if caFile != "" {
		if _, err := os.Stat(caFile); err == nil {
			targetTLSJSON = fmt.Sprintf(`{"mode":"verify_ca","server_name":"f12-pg.local","ca_file":%q}`, caFile)
		}
	}

	migCfgPath := filepath.Join(tmp, "migration-config.json")
	migCfgJSON := fmt.Sprintf(`{
  "source": {
    "host": "127.0.0.1",
    "port": 3306,
    "database": "yearning_legacy",
    "username": "root",
    "password": { "file": %q },
    "tls": { "mode": "verify_ca", "server_name": "legacy.invalid" },
    "legacy_secret_key": { "file": %q }
  },
  "target": {
    "host": %q,
    "port": %s,
    "database": %q,
    "username": %q,
    "password": { "file": %q },
    "tls": %s
  },
  "encryption": {
    "key_id": "pay-1",
    "key": { "file": %q },
    "deployment_id": "cli-cov-test"
  },
  "source_timezone": "UTC",
  "batch_rows": 100
}`, pwFile, legSecretFile, host, port, dbName, user, pwFile, targetTLSJSON, keyFile)
	if err := os.WriteFile(migCfgPath, []byte(migCfgJSON), 0o600); err != nil {
		t.Fatal(err)
	}

	migrationsDir := "../../../db/migrations"
	catalogPath := "../../../api/contracts/postgresql-column-catalog.json"
	evidencePath := "../../../api/migration/legacy-current-status-evidence.json"
	fieldMappingPath := "../../../api/migration/legacy-field-mapping.json"

	for _, verb := range []string{"status", "preflight", "plan", "apply"} {
		if err := runMigrate(ctx, []string{
			verb,
			"--config", migCfgPath,
			"--migrations-dir", migrationsDir,
			"--catalog", catalogPath,
		}); err != nil {
			t.Fatalf("runMigrate %s failed: %v", verb, err)
		}
	}
	_ = runMigrate(ctx, []string{
		"verify",
		"--config", migCfgPath,
		"--migrations-dir", migrationsDir,
		"--catalog", catalogPath,
	})

	// Legacy verb validation & snapshot inspect/dry-run
	if err := runMigrate(ctx, []string{"inspect", "--config", migCfgPath}); err == nil {
		t.Fatal("expected error when --report-dir is missing for inspect")
	}
	snapDir := "../../../testdata/migration/source-snapshot"
	repDir := filepath.Join(tmp, "reports")
	_ = runMigrate(ctx, []string{
		"inspect",
		"--config", migCfgPath,
		"--report-dir", repDir,
		"--source-snapshot", snapDir,
		"--evidence", evidencePath,
		"--field-mapping", fieldMappingPath,
	})
	_ = runMigrate(ctx, []string{
		"dry-run",
		"--config", migCfgPath,
		"--report-dir", repDir,
		"--source-snapshot", snapDir,
		"--evidence", evidencePath,
		"--field-mapping", fieldMappingPath,
	})
	_ = runMigrate(ctx, []string{
		"apply",
		"--config", migCfgPath,
		"--report-dir", repDir,
		"--run-id", "0198da00-0000-7000-8000-000000000099",
		"--source-snapshot", snapDir,
		"--evidence", evidencePath,
		"--field-mapping", fieldMappingPath,
	})
	_ = runMigrate(ctx, []string{
		"verify",
		"--config", migCfgPath,
		"--report-dir", repDir,
		"--run-id", "0198da00-0000-7000-8000-000000000099",
		"--source-snapshot", snapDir,
		"--evidence", evidencePath,
		"--field-mapping", fieldMappingPath,
	})

	// Exercise runServeMigrationMode with bootstrap admin password file
	adminPwFile := filepath.Join(tmp, "admin_pw.txt")
	if err := os.WriteFile(adminPwFile, []byte("ValidAdminPassword123!"), 0o600); err != nil {
		t.Fatal(err)
	}
	shortPwFile := filepath.Join(tmp, "short_pw.txt")
	if err := os.WriteFile(shortPwFile, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = bootstrapBuiltinAdmin(ctx, nil, filepath.Join(tmp, "missing.txt"))
	_ = bootstrapBuiltinAdmin(ctx, nil, shortPwFile)

	cctx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	_ = runServe(cctx, []string{
		"--migration-mode",
		"--config", migCfgPath,
		"--bootstrap-admin-password-file", adminPwFile,
		"--listen", "127.0.0.1:0",
	})

	// Exercise full runServe with a hermetic temporary keyring
	keyringDoc := fmt.Sprintf(`{"contract_version":1,"deployment_id":"local","keys":[
		{"key_id":"pay-1","purpose":"payload","status":"encrypt_decrypt","created_at":"2026-11-01T00:00:00Z","key_base64":%q},
		{"key_id":"state-1","purpose":"oidc_state","status":"encrypt_decrypt","created_at":"2026-11-01T00:00:00Z","key_base64":%q},
		{"key_id":"ident-1","purpose":"identifier_token","status":"encrypt_decrypt","created_at":"2026-11-01T00:00:00Z","key_base64":%q},
		{"key_id":"fp-1","purpose":"sql_fingerprint","status":"encrypt_decrypt","created_at":"2026-11-01T00:00:00Z","key_base64":%q},
		{"key_id":"aff-1","purpose":"cursor_affinity","status":"encrypt_decrypt","created_at":"2026-11-01T00:00:00Z","key_base64":%q}
	]}`, makeCLITestKey32(1), makeCLITestKey32(2), makeCLITestKey32(3), makeCLITestKey32(4), makeCLITestKey32(5))
	keyringPath := filepath.Join(tmp, "keyring.json")
	if err := os.WriteFile(keyringPath, []byte(keyringDoc), 0o400); err != nil {
		t.Fatal(err)
	}

	t.Setenv("YEARNING_ENVIRONMENT", "development")
	t.Setenv("YEARNING_DATABASE_HOST", host)
	t.Setenv("YEARNING_DATABASE_PORT", port)
	t.Setenv("YEARNING_DATABASE_DBNAME", dbName)
	t.Setenv("YEARNING_DATABASE_USER", user)
	t.Setenv("YEARNING_DATABASE_PASSWORD", pw)
	t.Setenv("YEARNING_DATABASE_SSLMODE", "disable")
	t.Setenv("YEARNING_SESSION_SECURE", "false")
	t.Setenv("YEARNING_KEY_PROVIDER_KIND", "file")
	t.Setenv("YEARNING_KEY_PROVIDER_PATH", keyringPath)
	t.Setenv("YEARNING_SCHEMA_MIGRATIONS_DIR", migrationsDir)
	sctx, scancel := context.WithTimeout(ctx, 120*time.Millisecond)
	defer scancel()
	_ = runServe(sctx, []string{
		"--listen", "127.0.0.1:0",
		"--bootstrap-admin-password-file", adminPwFile,
	})
}

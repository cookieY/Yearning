package testenv

import (
	"fmt"
	"strings"
	"testing"
)

type spyReporter struct {
	skipped string
	fataled string
}

func (s *spyReporter) Helper() {}

func (s *spyReporter) Skipf(format string, args ...any) {
	s.skipped = fmt.Sprintf(format, args...)
}

func (s *spyReporter) Fatalf(format string, args ...any) {
	s.fataled = fmt.Sprintf(format, args...)
}

func TestSkipOrFatalRespectsRequireIntegration(t *testing.T) {
	skipSpy := &spyReporter{}
	skipOrFatalWith(false, skipSpy, "missing db %s", "meta")
	if skipSpy.skipped != "missing db meta" || skipSpy.fataled != "" {
		t.Fatalf("expected Skipf only when require=false, got %+v", skipSpy)
	}

	failSpy := &spyReporter{}
	skipOrFatalWith(true, failSpy, "missing db %s", "meta")
	if !strings.Contains(failSpy.fataled, "YEARNING_REQUIRE_INTEGRATION=1") || !strings.Contains(failSpy.fataled, "missing db meta") || failSpy.skipped != "" {
		t.Fatalf("expected Fatalf with marker when require=true, got %+v", failSpy)
	}

	t.Setenv("YEARNING_REQUIRE_INTEGRATION", "")
	if RequireIntegration() {
		t.Fatal("expected RequireIntegration()=false when unset")
	}
	t.Setenv("YEARNING_REQUIRE_INTEGRATION", "1")
	if !RequireIntegration() {
		t.Fatal("expected RequireIntegration()=true for 1")
	}
	t.Setenv("YEARNING_REQUIRE_INTEGRATION", "TRUE")
	if !RequireIntegration() {
		t.Fatal("expected RequireIntegration()=true for TRUE")
	}
}

func TestResolveMetaPGCanonicalTakesPrecedenceOverLegacy(t *testing.T) {
	t.Setenv("YEARNING_TEST_DATABASE_HOST", "legacy-host")
	t.Setenv("YEARNING_TEST_DATABASE_PORT", "54330")
	t.Setenv("YEARNING_TEST_DATABASE_USER", "legacy-user")
	t.Setenv("YEARNING_TEST_DATABASE_PASSWORD", "legacy-pw")
	t.Setenv("YEARNING_TEST_DATABASE_DBNAME", "legacy_db")

	cfg, ok := ResolveMetaPG()
	if !ok || cfg.Host != "legacy-host" || cfg.Port != 54330 || cfg.Password != "legacy-pw" || cfg.DBName != "legacy_db" {
		t.Fatalf("expected legacy fallback resolution, got ok=%v cfg=%+v", ok, cfg)
	}

	t.Setenv("YEARNING_TEST_META_HOST", "127.0.0.1")
	t.Setenv("YEARNING_TEST_META_PORT", "54339")
	t.Setenv("YEARNING_TEST_META_USER", "meta_user")
	t.Setenv("YEARNING_TEST_META_PASSWORD", "meta_pw")
	t.Setenv("YEARNING_TEST_META_DBNAME", "yearning_v4")
	t.Setenv("YEARNING_TEST_META_CA_FILE", "/tmp/ca.crt")

	cfg, ok = ResolveMetaPG()
	if !ok || cfg.Host != "127.0.0.1" || cfg.Port != 54339 || cfg.User != "meta_user" || cfg.Password != "meta_pw" || cfg.DBName != "yearning_v4" || cfg.CAFile != "/tmp/ca.crt" {
		t.Fatalf("expected canonical env vars to override legacy vars, got ok=%v cfg=%+v", ok, cfg)
	}
	if !strings.Contains(cfg.LibpqDSN(), "sslrootcert=/tmp/ca.crt") {
		t.Fatalf("expected sslrootcert in LibpqDSN, got %s", cfg.LibpqDSN())
	}
}

func TestResolveMetaPGParsesURLDSN(t *testing.T) {
	t.Setenv("YEARNING_TEST_META_PASSWORD", "")
	t.Setenv("YEARNING_TEST_DATABASE_PASSWORD", "")
	t.Setenv("YEARNING_B13_TEST_DB_PASSWORD", "")
	t.Setenv("YEARNING_TEST_META_DSN", "postgres://u1:secret@db.internal:5440/mydb?sslmode=require")

	cfg, ok := ResolveMetaPG()
	if !ok {
		t.Fatal("expected DSN password to satisfy configuration")
	}
	if cfg.Host != "db.internal" || cfg.Port != 5440 || cfg.User != "u1" || cfg.Password != "secret" || cfg.DBName != "mydb" || cfg.SSLMode != "require" {
		t.Fatalf("unexpected DSN parse result: %+v", cfg)
	}
}

func TestResolveTargetPGAndMySQL(t *testing.T) {
	t.Setenv("YEARNING_TEST_TARGET_PG_HOST", "127.0.0.1")
	t.Setenv("YEARNING_TEST_TARGET_PG_PORT", "54338")
	t.Setenv("YEARNING_TEST_TARGET_PG_USER", "yearning")
	t.Setenv("YEARNING_TEST_TARGET_PG_PASSWORD", "pg-pw")
	t.Setenv("YEARNING_TEST_TARGET_PG_DBNAME", "yearning")

	pg, ok := ResolveTargetPG()
	if !ok || pg.Port != 54338 || pg.Password != "pg-pw" {
		t.Fatalf("unexpected target PG config: ok=%v cfg=%+v", ok, pg)
	}

	t.Setenv("YEARNING_TEST_TARGET_MYSQL_HOST", "127.0.0.1")
	t.Setenv("YEARNING_TEST_TARGET_MYSQL_PORT", "33309")
	t.Setenv("YEARNING_TEST_TARGET_MYSQL_USER", "root")
	t.Setenv("YEARNING_TEST_TARGET_MYSQL_PASSWORD", "mysql-pw")
	t.Setenv("YEARNING_TEST_TARGET_MYSQL_DBNAME", "yearning")

	my, ok := ResolveTargetMySQL()
	if !ok || my.Port != 33309 || my.Password != "mysql-pw" {
		t.Fatalf("unexpected target MySQL config: ok=%v cfg=%+v", ok, my)
	}
}

package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/config"
	"github.com/cookieY/YearningX/backend/internal/platform/addrguard"
)

func TestCLIUnitHelpers(t *testing.T) {
	// run subcommand dispatch
	if err := run(nil); err == nil {
		t.Fatal("expected error on empty args")
	}
	if err := run([]string{"unknown-cmd"}); err == nil {
		t.Fatal("expected error on unknown subcommand")
	}
	if err := run([]string{"version"}); err != nil {
		t.Fatalf("unexpected error on version: %v", err)
	}

	// promptForReset & flagArgsAfterReset & promptNewPassword (non-TTY in tests)
	if !promptForReset([]string{"--config", "a.yaml", "--reset-admin-password"}) {
		t.Fatal("expected promptForReset true")
	}
	if promptForReset([]string{"serve"}) {
		t.Fatal("expected promptForReset false")
	}
	rem := flagArgsAfterReset([]string{"--reset-admin-password", "--config", "a.yaml"})
	if len(rem) != 2 || rem[0] != "--config" {
		t.Fatalf("unexpected flagArgsAfterReset: %v", rem)
	}
	if _, err := promptNewPassword(); err == nil {
		t.Fatal("expected non-TTY error from promptNewPassword")
	}
	if err := runResetAdminPassword(context.Background(), []string{"--bad-flag"}); err == nil {
		t.Fatal("expected flag parse error")
	}

	// applyInstanceDefaults
	var cfg config.Config
	applyInstanceDefaults(&cfg)
	if cfg.Environment != "development" || cfg.Observability.InstanceID == "" || cfg.Observability.DeploymentID != "local" {
		t.Fatalf("unexpected applyInstanceDefaults: %+v", cfg)
	}

	// openRetentionPool branches
	if _, _, err := openRetentionPool("://bad-dsn"); err == nil {
		t.Fatal("expected parse error on bad DSN")
	}
	restoreNoBypass := addrguard.SetLoopbackBypassForTest(false)
	if _, _, err := openRetentionPool("postgres://u:p@127.0.0.1:5432/db"); err == nil {
		restoreNoBypass()
		t.Fatal("expected addrguard error on loopback without bypass")
	}
	restoreNoBypass()
	t.Cleanup(addrguard.SetLoopbackBypassForTest(true))
	db, closeFn, err := openRetentionPool("postgres://u:p@127.0.0.1:5432/db?sslmode=disable")
	if err != nil || db == nil || closeFn == nil {
		t.Fatalf("expected openRetentionPool success with loopback bypass, got err=%v", err)
	}
	_ = closeFn()

	// writeReport, emitJSON, exitCodeError
	tmp := t.TempDir()
	if err := writeReport(filepath.Join(tmp, "sub"), "rep.json", map[string]any{"ok": true}); err != nil {
		t.Fatalf("writeReport failed: %v", err)
	}
	emitJSON(map[string]any{"hello": "world"})
	if exitError(2, "bad usage").Error() != "bad usage" {
		t.Fatal("unexpected exitCodeError string")
	}

	// runMigrate validation error branches
	ctx := context.Background()
	if err := runMigrate(ctx, nil); err == nil {
		t.Fatal("expected error on empty migrate args")
	}
	if err := runMigrate(ctx, []string{"status", "--bad-flag"}); err == nil {
		t.Fatal("expected error on bad flag")
	}
	if err := runMigrate(ctx, []string{"unknown-verb"}); err == nil {
		t.Fatal("expected error on unknown verb")
	}
	if err := runMigrate(ctx, []string{"status"}); err == nil {
		t.Fatal("expected error on missing --config")
	}
	if err := runMigrate(ctx, []string{"status", "--config", "/nonexistent/cfg.json"}); err == nil {
		t.Fatal("expected error on nonexistent config")
	}

	// runServe & runMock validation error branches
	if err := runServe(ctx, []string{"--bad-flag"}); err == nil {
		t.Fatal("expected error on bad serve flag")
	}
	if err := runServe(ctx, []string{"--migration-mode"}); err == nil {
		t.Fatal("expected error on migration-mode without --config")
	}
	if err := runServe(ctx, []string{"--migration-mode", "--config", "/nonexistent/m.json"}); err == nil {
		t.Fatal("expected error on migration-mode with missing config")
	}
	if err := runMock(ctx, []string{"--bad-flag"}); err == nil {
		t.Fatal("expected error on bad mock flag")
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if err := runMock(cctx, []string{
		"--openapi", "../../../api/openapi/yearning-v4.yaml",
		"--scenarios", "../../../testdata/development/mock-scenarios.json",
		"--listen", "127.0.0.1:0",
	}); err != nil {
		t.Fatalf("runMock with pre-canceled ctx expected clean shutdown, got: %v", err)
	}
}

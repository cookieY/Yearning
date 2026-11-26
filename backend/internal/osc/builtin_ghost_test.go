package osc

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/github/gh-ost/go/base"
)

type fakeInspector struct {
	ghost, changelog, triggers, socket bool
}

func (f *fakeInspector) Inspect(ctx context.Context, db, table string) (bool, bool, bool, bool, error) {
	return f.ghost, f.changelog, f.triggers, f.socket, nil
}

func TestApplyJobToMigrationContext(t *testing.T) {
	dir := t.TempDir()
	chunk := 500
	lag := 2000
	retries := 3
	maxLoad := "Threads_running=25"
	argv, _, err := BuildGhOstArgv(PlanInput{
		Database: "demo",
		Table:    "orders",
		Alter:    "ADD COLUMN tag VARCHAR(32) NULL",
		Host:     "127.0.0.1",
		Port:     54334,
		User:     "yr",
	}, dir, &Overrides{
		ChunkSize: &chunk,
		MaxLag:    &lag,
		Retries:   &retries,
		MaxLoad:   &maxLoad,
	})
	if err != nil {
		t.Fatalf("BuildGhOstArgv: %v", err)
	}
	job := &Job{
		BinaryPath:   BuiltinInProcessGhOstPath,
		BinarySHA256: BuiltinInProcessGhOstSHA256,
		Tool:         ToolGhOst,
		Argv:         argv,
		Dir:          dir,
		Database:     "demo",
		Table:        "orders",
	}
	mctx := base.NewMigrationContext()
	if err := applyJobToMigrationContext(mctx, job, "s3cret-pass"); err != nil {
		t.Fatalf("applyJobToMigrationContext: %v", err)
	}
	if mctx.DatabaseName != "demo" || mctx.OriginalTableName != "orders" {
		t.Fatalf("db=%s table=%s", mctx.DatabaseName, mctx.OriginalTableName)
	}
	if mctx.InspectorConnectionConfig.Key.Hostname != "127.0.0.1" || mctx.InspectorConnectionConfig.Key.Port != 54334 {
		t.Fatalf("endpoint=%s:%d", mctx.InspectorConnectionConfig.Key.Hostname, mctx.InspectorConnectionConfig.Key.Port)
	}
	if mctx.CliUser != "yr" || mctx.CliPassword != "s3cret-pass" {
		t.Fatalf("credentials not applied")
	}
	if atomic.LoadInt64(&mctx.ChunkSize) != 500 || atomic.LoadInt64(&mctx.MaxLagMillisecondsThrottleThreshold) != 2000 {
		t.Fatalf("chunk=%d lag=%d", mctx.ChunkSize, mctx.MaxLagMillisecondsThrottleThreshold)
	}
	if mctx.MaxRetries() != 3 {
		t.Fatalf("retries=%d want 3", mctx.MaxRetries())
	}
}

func TestInProcessRunnerRejectsPtOscAndBadHash(t *testing.T) {
	r := NewRunner()
	dir := t.TempDir()
	if _, err := r.Run(context.Background(), &Job{
		BinaryPath:   BuiltinInProcessGhOstPath,
		BinarySHA256: BuiltinInProcessGhOstSHA256,
		Tool:         ToolPtOsc,
		Dir:          dir,
	}, Hooks{}); err == nil || !strings.Contains(err.Error(), "pt-osc is not supported") {
		t.Fatalf("expected pt-osc rejection, got %v", err)
	}
	if _, err := r.Run(context.Background(), &Job{
		BinaryPath:   BuiltinInProcessGhOstPath,
		BinarySHA256: strings.Repeat("0", 64),
		Tool:         ToolGhOst,
		Dir:          dir,
	}, Hooks{}); err == nil || !strings.Contains(err.Error(), "hash mismatch") {
		t.Fatalf("expected hash mismatch rejection, got %v", err)
	}
}

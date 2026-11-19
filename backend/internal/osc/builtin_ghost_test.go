package osc

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/github/gh-ost/go/base"
	_ "github.com/go-sql-driver/mysql"
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

func TestInProcessGhOstLiveMySQLMigration(t *testing.T) {
	const (
		host = "127.0.0.1"
		port = 54334
		user = "yr"
		pass = "f12-mysql-902d734b79df"
		db   = "demo"
	)
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?timeout=2s", user, pass, host, port, db)
	conn, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Skipf("mysql open skipped: %v", err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := conn.PingContext(ctx); err != nil {
		t.Skipf("live mysql not reachable at %s:%d: %v", host, port, err)
	}

	table := fmt.Sprintf("ghost_inproc_%d", time.Now().UnixNano()%1000000)
	_, _ = conn.Exec(fmt.Sprintf("DROP TABLE IF EXISTS `%s`", table))
	if _, err := conn.Exec(fmt.Sprintf(
		"CREATE TABLE `%s` (id INT PRIMARY KEY AUTO_INCREMENT, name VARCHAR(64) NOT NULL)", table,
	)); err != nil {
		t.Fatalf("create table: %v", err)
	}
	defer func() {
		_, _ = conn.Exec(fmt.Sprintf("DROP TABLE IF EXISTS `%s`", table))
	}()
	if _, err := conn.Exec(fmt.Sprintf(
		"INSERT INTO `%s` (name) VALUES ('row1'), ('row2'), ('row3')", table,
	)); err != nil {
		t.Fatalf("seed rows: %v", err)
	}

	dir := t.TempDir()
	chunk := 100
	argv, _, err := BuildGhOstArgv(PlanInput{
		Database: db,
		Table:    table,
		Alter:    "ADD COLUMN tag VARCHAR(32) NULL",
		Host:     host,
		Port:     port,
		User:     user,
	}, dir, &Overrides{ChunkSize: &chunk})
	if err != nil {
		t.Fatalf("BuildGhOstArgv: %v", err)
	}

	var beats, progs int64
	job := &Job{
		BinaryPath:   BuiltinInProcessGhOstPath,
		BinarySHA256: BuiltinInProcessGhOstSHA256,
		ToolVersion:  BuiltinGhOstVersion,
		Tool:         ToolGhOst,
		Argv:         argv,
		Dir:          dir,
		Password:     pass,
		Inspector:    &fakeInspector{},
		Database:     db,
		Table:        table,
	}

	out, err := NewRunner().Run(context.Background(), job, Hooks{
		OnHeartbeat: func(time.Time) { atomic.AddInt64(&beats, 1) },
		OnProgress:  func(string, int, int64) { atomic.AddInt64(&progs, 1) },
	})
	if err != nil {
		t.Fatalf("in-process Runner.Run: %v", err)
	}
	if out.Phase != PhaseSucceeded || out.ExitCode != 0 || out.ProcessLost {
		t.Fatalf("out=%+v", out)
	}
	if out.ProgressBps != 10000 || out.RowsCopied != 3 {
		t.Fatalf("progress=%d rows=%d want 10000/3", out.ProgressBps, out.RowsCopied)
	}
	if out.ResidualState != ResidualNone {
		t.Fatalf("residual=%s want none", out.ResidualState)
	}
	if beats == 0 || progs == 0 {
		t.Fatalf("expected heartbeat and progress callbacks, got beats=%d progs=%d", beats, progs)
	}
	if job.Password != "" || strings.Contains(out.StdoutTail, pass) {
		t.Fatal("password was not scrubbed or leaked to tail")
	}

	var colCount int
	if err := conn.QueryRow(
		"SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = ? AND table_name = ? AND column_name = 'tag'",
		db, table,
	).Scan(&colCount); err != nil {
		t.Fatalf("query column: %v", err)
	}
	if colCount != 1 {
		t.Fatalf("expected migrated column tag, got count=%d", colCount)
	}
}

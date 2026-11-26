//go:build integration

package osc

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/cookieY/YearningX/backend/internal/platform/addrguard"
	"github.com/cookieY/YearningX/backend/internal/platform/testenv"
)

func TestInProcessGhOstLiveMySQLMigration(t *testing.T) {
	cfg := testenv.RequireTargetMySQL(t, testenv.WithDefaultPort(33309))
	host := cfg.Host
	port := cfg.Port
	user := cfg.User
	pass := cfg.Password
	db := cfg.DBName

	dsn := fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?timeout=2s", user, pass, host, port, db)
	conn, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("mysql open: %v", err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := conn.PingContext(ctx); err != nil {
		t.Fatalf("live mysql not reachable at %s:%d: %v", host, port, err)
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

	t.Cleanup(addrguard.SetLoopbackBypassForTest(true))
	var beats, progs int64
	job := &Job{
		BinaryPath:           BuiltinInProcessGhOstPath,
		BinarySHA256:         BuiltinInProcessGhOstSHA256,
		ToolVersion:          BuiltinGhOstVersion,
		Tool:                 ToolGhOst,
		Argv:                 argv,
		PinnedHost:           host,
		AssumeMasterHostname: net.JoinHostPort(host, strconv.Itoa(port)),
		Dir:                  dir,
		Password:             pass,
		Inspector:            &fakeInspector{},
		Database:             db,
		Table:                table,
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

package database

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestDiagnosticsRoutineSnapshotSQLSkipsText(t *testing.T) {
	if strings.Contains(strings.ToUpper(mysqlSnapshotWithoutTextSQL), "DIGEST_TEXT") {
		t.Fatal("MySQL routine snapshot SQL must not select DIGEST_TEXT")
	}
	if !strings.Contains(pgSnapshotWithoutTextSQL, "pg_stat_statements(false)") {
		t.Fatal("PostgreSQL routine snapshot SQL must call pg_stat_statements(false)")
	}
	if strings.Contains(pgSnapshotWithoutTextSQL, "s.query,") || strings.Contains(pgSnapshotWithoutTextSQL, "pg_stat_statements(true)") {
		t.Fatal("PostgreSQL routine snapshot SQL must not load query text")
	}
	for _, sqlText := range []string{
		mysqlProbePerfSchemaSQL, mysqlProbeConsumerSQL, mysqlProbeCountSQL,
		mysqlSnapshotWithoutTextSQL, mysqlTemplateByDigestAndSchemaSQL, mysqlTemplateByDigestFallbackSQL,
		pgProbeExtensionSQL, pgProbeCountSQL, pgSnapshotWithoutTextSQL, pgTemplateByQueryIDSQL,
	} {
		upper := strings.ToUpper(sqlText)
		for _, banned := range []string{"CREATE EXTENSION", "SET GLOBAL", "TRUNCATE", "PG_STAT_STATEMENTS_RESET", "ALTER SYSTEM"} {
			if strings.Contains(upper, banned) {
				t.Fatalf("diagnostics SQL contains forbidden mutating command %q", banned)
			}
		}
	}
}

func TestDiagnosticsRejectsExecutionPurposePool(t *testing.T) {
	execPool := &PurposePool{engine: "postgresql", purpose: PurposeExecution}
	adapter := NewPostgreSQLAdapter(Endpoint{Host: "127.0.0.1", Port: 5432, CompatibilityMod: "postgresql"}, AdapterOptions{})

	if _, err := ProbeStatementStats(context.Background(), adapter, execPool); !errors.Is(err, ErrReadOnlyViolation) {
		t.Fatalf("expected ErrReadOnlyViolation for ProbeStatementStats on PurposeExecution, got %v", err)
	}
	if _, err := ReadStatementStatsSnapshot(context.Background(), adapter, execPool, 200); !errors.Is(err, ErrReadOnlyViolation) {
		t.Fatalf("expected ErrReadOnlyViolation for ReadStatementStatsSnapshot on PurposeExecution, got %v", err)
	}
	if _, err := ReadStatementTemplate(context.Background(), adapter, execPool, "12345", "app"); !errors.Is(err, ErrReadOnlyViolation) {
		t.Fatalf("expected ErrReadOnlyViolation for ReadStatementTemplate on PurposeExecution, got %v", err)
	}
}

func TestClassifyDiagnosticsProbeError(t *testing.T) {
	cases := []struct {
		engine string
		err    error
		want   StatsProbeState
	}{
		{"mysql", errors.New("Error 1142 (42000): SELECT command denied to user 'ro'@'%' for table 'events_statements_summary_by_digest'"), StatsProbeInsufficientPriv},
		{"mysql", errors.New("Error 1146 (42S02): Table 'performance_schema.events_statements_summary_by_digest' doesn't exist"), StatsProbeUnsupportedEngine},
		{"postgresql", errors.New("ERROR: pg_stat_statements must be loaded via shared_preload_libraries (SQLSTATE 55000)"), StatsProbeDisabled},
		{"postgresql", errors.New("ERROR: permission denied for function pg_stat_statements (SQLSTATE 42501)"), StatsProbeInsufficientPriv},
		{"postgresql", errors.New("ERROR: column s.total_exec_time does not exist (SQLSTATE 42703)"), StatsProbeUnsupportedEngine},
	}
	for _, tc := range cases {
		got, _ := classifyDiagnosticsProbeError(tc.engine, tc.err)
		if got != tc.want {
			t.Fatalf("classifyDiagnosticsProbeError(%s, %v) = %q, want %q", tc.engine, tc.err, got, tc.want)
		}
	}
}

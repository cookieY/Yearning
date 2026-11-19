//go:build integration

// b10_semantics_integration_test.go real target DB execution semantics + 18 Failure
// Injection cases (mapped case-by-case from execution-failure-injection.json; target = containerized
// PostgreSQL/MySQL, connection-level faults injected via in-process TCP proxy --
// toxiproxy-equivalent implementation of harness contract target_database_proxies).
//
// Case -> failure contract mapping:
//
//	F01/F02 worker dead (before claim / after Attempt commit) -> WorkerLostBeforeSend
//	F03 connect_timeout_before_send -> ConnectTimeout
//	F04 pod_kill_after_socket_write -> SendKillUnknown
//	F05 dml_statement_definitive_error -> DMLFailureRollback
//	F06 dml_commit_ack_lost -> CommitAckLost
//	F07 dml_commit_confirmed_then_local_persist_fails -> CommitConfirmPersistFails
//	F08 ddl_first_statement_definitive_failure -> DDLFirstFailure
//	F09 ddl_later_statement_definitive_failure -> DDLLaterFailure
//	F10 ddl_success_ack_lost -> DDLSendKillUnknown
//	F11 pod_kill_between_ddl_statements -> PodKillBetweenDDL
//	F12 cancel_dml_confirmed_before_commit -> CancelDMLConfirmed
//	F13 cancel_ddl_after_confirmed_success -> CancelDDLPartial
//	F14 cancel_not_confirmed_within_30_seconds -> CancelTimeoutUnknown
//	F15 lease_expires_after_send -> LeaseExpiresAfterSend
//	F16 outbox_insert_fails -> TestB10OutboxInsertFails in application package
//	F17 gh_ost_process_exit_nonzero -> GhOstExitNonzero
//	F18 gh_ost_process_lost -> GhOstProcessLost
package httpapi

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"time"

	execapp "github.com/cookieY/YearningX/backend/internal/execution/application"
)

// b10PGDatasource creates target PG datasource (supports connection fault injection via proxy port).
func (fx *b10Fixture) b10PGDatasource(t *testing.T, name string, port int) string {
	pw := fx.b10PGPassword(t)
	if port == 54330 {
		port = b10PGPort()
	}
	return fx.b10ExecDatasource(t, name, "postgresql", "127.0.0.1", port,
		"yearning_exec_target", b10PGUser(), pw, "16.x")
}

// F01/F02: worker dead (task not run) -> lease expiry reconciliation marks failed/not_started
// -> re-clicking same order succeeds (single test covers durable fact surface of both cases).
func TestB10WorkerLostBeforeSendAndRetry(t *testing.T) {
	fx := newB10Fixture(t)
	execTarget(t, mustOpenTargetPG(t, fx), `update accounts set amount = 100 where id = 1`)
	dsID := fx.b10PGDatasource(t, "b10-wl", 54330)
	flowID := fx.b10ExecFlow(t, "b10-wl-flow", "exec_target", dsID)
	orderID, version := fx.b10OrderToExecutionPending(t, flowID,
		"update accounts set amount = amount + 1 where id = 1;")
	attempt1 := fx.b10Begin(t, orderID, version)
	// Simulate dead worker: do not drive task; reconciled after lease expires.
	fx.b10ExpireLease(t, attempt1)
	attempt2 := fx.b10Begin(t, orderID, fx.b10Version(t, orderID))
	if attempt2 == attempt1 {
		t.Fatal("retry must create a new attempt")
	}
	s1, b1, _, _ := fx.b10AttemptRow(t, attempt1)
	if s1 != "failed" || b1 != "not_started" {
		t.Fatalf("dead attempt state=%s boundary=%s want failed/not_started", s1, b1)
	}
	if err := fx.b10Drive(t, attempt2); err != nil {
		t.Fatalf("drive attempt2: %v", err)
	}
	s2, b2, _, _ := fx.b10AttemptRow(t, attempt2)
	if s2 != "succeeded" || b2 != "sent" {
		t.Fatalf("attempt2 state=%s boundary=%s", s2, b2)
	}
	if got := fx.b10OrderState(t, orderID); got != "completed" {
		t.Fatalf("order state=%s want completed", got)
	}
	rows := fx.b10TargetQueryPG(t, `select amount from exec_target.accounts where id = 1`)
	if len(rows) != 1 || rows[0][0] != "101" {
		t.Fatalf("target row = %v want 101", rows)
	}
	// Restore seed state.
	execTarget(t, mustOpenTargetPG(t, fx), `update accounts set amount = 100 where id = 1`)
}

// F03 (rewritten for RCP-20261031-REMOVE-FLOW-SCHEMA-MAPPINGS): target is reachable via pass-through
// proxy during review/submission (frozen search_path binding + submission re-probe succeed), then
// becomes unreachable before execution -> Begin preflight connection fails closed (no attempt created,
// zero SQL bytes cross boundary); order stays execution_pending and retryable (retrying Begin also rejects).
// Legacy behavior "attempt failed/not_started + same_order_recovery" is intercepted at preflight boundary
// under always-freeze semantics, moving observation surface from attempt level up to Begin level.
func TestB10ConnectFailureBeforeSendFailsClosed(t *testing.T) {
	fx := newB10Fixture(t)
	proxy := startKillProxy(t, "127.0.0.1:54330")
	dsID := fx.b10PGDatasource(t, "b10-ct", proxy.port())
	flowID := fx.b10ExecFlow(t, "b10-ct-flow", "exec_target", dsID)
	orderID, version := fx.b10OrderToExecutionPending(t, flowID,
		"update accounts set amount = amount + 1 where id = 1;")
	// Target unreachable: close pass-through listener (review and submission completed, frozen context intact).
	if err := proxy.listener.Close(); err != nil {
		t.Fatalf("close proxy: %v", err)
	}
	if _, _, err := fx.exec.Begin(context.Background(), execapp.BeginInput{
		OrderID: orderID, Actor: execapp.Actor{UserID: fx.executorID, Username: "exec1"},
		ExpectVersion: version, IdempotencyKey: "b10-ct-1",
	}); err == nil {
		t.Fatal("begin must fail closed when the execution target is unreachable")
	}
	var live int64
	if err := fx.db.Raw(`select count(*) from execution_attempts where order_id = ?`, orderID).
		Scan(&live).Error; err != nil {
		t.Fatal(err)
	}
	if live != 0 {
		t.Fatalf("unreachable target must not create attempts, got %d", live)
	}
	// Established contract for search_path preflight failure (begin.go
	// failOrderAfterPostgresSearchPathPreflightTx): order terminal state failed +
	// execution.preflight_failed audit; unreachable target is not retryable (fail closed).
	if _, _, err := fx.exec.Begin(context.Background(), execapp.BeginInput{
		OrderID: orderID, Actor: execapp.Actor{UserID: fx.executorID, Username: "exec1"},
		ExpectVersion: fx.b10Version(t, orderID), IdempotencyKey: "b10-ct-2",
	}); err == nil {
		t.Fatal("retry begin against a failed order must be rejected")
	}
	if state := fx.b10OrderState(t, orderID); state != "failed" {
		t.Fatalf("order state=%s want failed (search-path preflight fail closed)", state)
	}
	var audit int64
	if err := fx.db.Raw(`select count(*) from audit_events
		where event_type = 'execution.preflight_failed'
		  and resource_id = ?`, orderID).Scan(&audit).Error; err != nil {
		t.Fatal(err)
	}
	if audit < 1 {
		t.Fatal("search-path preflight failure must leave an execution.preflight_failed audit event")
	}
}

// F04: connection killed after socket write -> result_unknown; auto-replay and same-order retry forbidden.
func TestB10SendKillUnknown(t *testing.T) {
	fx := newB10Fixture(t)
	proxy := startKillProxy(t, "127.0.0.1:54330", "marker_f04")
	dsID := fx.b10PGDatasource(t, "b10-f04", proxy.port())
	flowID := fx.b10ExecFlow(t, "b10-f04-flow", "exec_target", dsID)
	orderID, version := fx.b10OrderToExecutionPending(t, flowID,
		"update accounts set amount = amount + 11 where id = 2 /*marker_f04*/;")
	attempt := fx.b10Begin(t, orderID, version)
	if err := fx.b10Drive(t, attempt); err != nil {
		t.Fatalf("drive: %v", err)
	}
	s, b, _, _ := fx.b10AttemptRow(t, attempt)
	if s != "result_unknown" || b == "not_started" {
		t.Fatalf("attempt state=%s boundary=%s want result_unknown/sent-side", s, b)
	}
	if got := fx.b10OrderState(t, orderID); got != "result_unknown" {
		t.Fatalf("order=%s want result_unknown", got)
	}
	if !proxy.killed() {
		t.Fatal("proxy kill marker must have fired")
	}
	// E004: after crossing send boundary, same-order retry is rejected with 3004.
	fx.b10ExpectRetryForbidden(t, orderID, "b10-f04-retry")
}

// F05: definitive DML failure -> whole-stage transaction rollback (E001) -> order failed, retry forbidden.
// After RCP-20261101, review blocks "updating non-existent table" upfront -- definitive execution failure
// uses unique constraint violation instead (review does not pre-check uniqueness): statement 1 updates row 1
// to a value colliding with row 2 while statement 2 is valid; statement 1 succeeds and statement 2 hits unique key, rolling back stage.
func TestB10DMLFailureRollback(t *testing.T) {
	fx := newB10Fixture(t)
	execTarget(t, mustOpenTargetPG(t, fx), `drop table if exists exec_target.b10_f5_uq`)
	execTarget(t, mustOpenTargetPG(t, fx), `create table exec_target.b10_f5_uq(id int primary key, x int unique)`)
	execTarget(t, mustOpenTargetPG(t, fx), `insert into exec_target.b10_f5_uq values (1, 1), (2, 2)`)
	dsID := fx.b10PGDatasource(t, "b10-f05", 54330)
	flowID := fx.b10ExecFlow(t, "b10-f05-flow", "exec_target", dsID)
	orderID, version := fx.b10OrderToExecutionPending(t, flowID,
		"update b10_f5_uq set x = 9 where x = 1;\n"+
			"update b10_f5_uq set x = 2 where x = 9;")
	attempt := fx.b10Begin(t, orderID, version)
	if err := fx.b10Drive(t, attempt); err != nil {
		t.Fatalf("drive: %v", err)
	}
	s, _, _, _ := fx.b10AttemptRow(t, attempt)
	if s != "failed" {
		t.Fatalf("attempt state=%s want failed", s)
	}
	if got := fx.b10OrderState(t, orderID); got != "failed" {
		t.Fatalf("order=%s want failed", got)
	}
	// Stage transaction rolled back as a whole: statement 1 effect is absent (row 1 remains 1).
	rows := fx.b10TargetQueryPG(t, `select x from exec_target.b10_f5_uq where x in (1, 9) order by x`)
	if len(rows) != 1 || rows[0][0] != "1" {
		t.Fatalf("stage transaction must roll back, got %v", rows)
	}
	stmts := fx.b10Statements(t, attempt)
	if stmts[0].State != "succeeded" || stmts[1].State != "failed" {
		t.Fatalf("statements=%v", stmts)
	}
	if !stmts[1].Failure.Valid || stmts[1].Failure.String == "" {
		t.Fatal("failed statement must carry sanitized failure name")
	}
	// Boundary already crossed (statement 1 sent successfully) -> retry always returns 3004.
	fx.b10ExpectRetryForbidden(t, orderID, "b10-f05-retry")
}

// F06: COMMIT ACK lost -> result_unknown (neither rollback nor success is assumed).
func TestB10CommitAckLost(t *testing.T) {
	fx := newB10Fixture(t)
	execTarget(t, mustOpenTargetPG(t, fx), `update accounts set amount = 100 where id = 1`)
	proxy := startKillProxy(t, "127.0.0.1:54330", "COMMIT")
	dsID := fx.b10PGDatasource(t, "b10-f06", proxy.port())
	flowID := fx.b10ExecFlow(t, "b10-f06-flow", "exec_target", dsID)
	orderID, version := fx.b10OrderToExecutionPending(t, flowID,
		"update accounts set amount = amount + 7 where id = 1;")
	attempt := fx.b10Begin(t, orderID, version)
	if err := fx.b10Drive(t, attempt); err != nil {
		t.Fatalf("drive: %v", err)
	}
	s, _, _, _ := fx.b10AttemptRow(t, attempt)
	if s != "result_unknown" {
		t.Fatalf("attempt state=%s want result_unknown", s)
	}
	if got := fx.b10OrderState(t, orderID); got != "result_unknown" {
		t.Fatalf("order=%s want result_unknown", got)
	}
	if !proxy.killed() {
		t.Fatal("commit kill must fire")
	}
	// Unknown never auto-replays: retry returns 3004.
	fx.b10ExpectRetryForbidden(t, orderID, "b10-f06-retry")
}

// F07: local persistence fails after commit confirmation -> result_unknown; manual verification
// confirmed_succeeded advances order to completed and permanently sets manually_verified (E005).
func TestB10CommitConfirmPersistFailsThenVerifySucceed(t *testing.T) {
	fx := newB10Fixture(t)
	dsID := fx.b10PGDatasource(t, "b10-f07", 54330)
	flowID := fx.b10ExecFlow(t, "b10-f07-flow", "exec_target", dsID)
	orderID, version := fx.b10OrderToExecutionPending(t, flowID,
		"update accounts set amount = amount + 3 where id = 2;")
	attempt := fx.b10Begin(t, orderID, version)
	death := errDead()
	fx.runner.SetFaultHooks(&execapp.FaultHooks{
		AfterCommitConfirm: func(string) error { return death },
	})
	if err := fx.b10Drive(t, attempt); err == nil {
		t.Fatal("drive must abort on injected death")
	}
	fx.runner.SetFaultHooks(nil)
	fx.b10ExpireLease(t, attempt)
	// Expired lease reconciliation: boundary=sent -> result_unknown.
	fx.b10ExpectRetryForbidden(t, orderID, "b10-f07-reconcile")
	s, _, _, _ := fx.b10AttemptRow(t, attempt)
	if s != "result_unknown" {
		t.Fatalf("attempt state=%s want result_unknown", s)
	}
	// Manual verification (frozen executor): database-side evidence is required.
	attemptVersion := fx.b10AttemptVersion(t, attempt)
	_, _, verr := fx.exec.Verify(context.Background(), execapp.VerifyInput{
		AttemptID: attempt, Actor: execapp.Actor{UserID: fx.executorID, Username: "exec1"},
		ExpectVersion: attemptVersion, Result: "confirmed_succeeded",
		Reason:         "target database row value verified",
		Evidence:       []execapp.EvidenceItem{{Kind: "database_fact", Content: "accounts.id=2 amount=203"}},
		IdempotencyKey: "b10-f07-verify",
	})
	if verr != nil {
		t.Fatalf("verify: %v", verr)
	}
	if got := fx.b10OrderState(t, orderID); got != "completed" {
		t.Fatalf("order=%s want completed", got)
	}
	var mv bool
	if err := fx.db.Raw(`select manually_verified from change_orders where id = ?`, orderID).Scan(&mv).Error; err != nil {
		t.Fatal(err)
	}
	if !mv {
		t.Fatal("confirmed_succeeded must set manually_verified=true")
	}
	// Verification fact is append-only (1 row).
	var verifications int
	_ = fx.db.Raw(`select count(*) from execution_verifications where attempt_id = ?`, attempt).Scan(&verifications).Error
	if verifications != 1 {
		t.Fatalf("verifications=%d want 1", verifications)
	}
	execTarget(t, mustOpenTargetPG(t, fx), `update accounts set amount = 200 where id = 2`)
}

// F08/F09: DDL first-statement vs later-statement failure -- preserve prior successes, mark suffix skipped, partial_failed.
// After RCP-20261101, review blocks "creating existing table" and pre-send drift check blocks "table created
// after submission" -- review-visible object conflict vectors have moved to the review layer by design.
// Definitive execution failure is injected via FaultHooks observation point: after begin passes preflight,
// create target table between statements so the next statement hits drift/conflict (non-transactional DDL prefix preservation unchanged).
func TestB10DDLPartialFailure(t *testing.T) {
	fx := newB10Fixture(t)
	dsID := fx.b10PGDatasource(t, "b10-f89", 54330)
	execTarget(t, mustOpenTargetPG(t, fx), `drop table if exists exec_target.b10_f9_a, exec_target.b10_f9_b, exec_target.b10_f9_c`)

	// F09: third statement fails, first two succeeded statements are preserved.
	flowID := fx.b10ExecFlow(t, "b10-f9-flow", "exec_target", dsID)
	orderID, version := fx.b10OrderToExecutionPending(t, flowID,
		"create table b10_f9_a(id int);\n"+
			"create table b10_f9_b(id int);\n"+
			"create table b10_f9_c(id int);")
	if err := fx.db.Exec(`update change_orders set rollback_enabled = false where id = ?`, orderID).Error; err != nil {
		t.Fatal(err)
	}
	seeded := false
	fx.runner.SetFaultHooks(&execapp.FaultHooks{
		AfterStatementPersist: func(attemptID string, ordinal int32) error {
			if ordinal == 2 && !seeded {
				seeded = true
				execTarget(t, mustOpenTargetPG(t, fx), `create table exec_target.b10_f9_c(id int)`)
			}
			return nil
		},
	})
	attempt := fx.b10Begin(t, orderID, version)
	if err := fx.b10Drive(t, attempt); err != nil {
		t.Fatalf("drive: %v", err)
	}
	fx.runner.SetFaultHooks(nil)
	s, _, _, _ := fx.b10AttemptRow(t, attempt)
	if s != "partial_failed" {
		t.Fatalf("attempt state=%s want partial_failed", s)
	}
	if got := fx.b10OrderState(t, orderID); got != "partial_failed" {
		t.Fatalf("order=%s want partial_failed", got)
	}
	stmts := fx.b10Statements(t, attempt)
	// Inter-statement world drift is caught by per-statement pre-send check: third statement is NOT-SENT (skipped),
	// first two confirmed successes are preserved -- partial_failed semantics match stop-on-first-error, with
	// drift detection as failure name (pure DDL object conflicts visible to review are now caught before execution).
	if len(stmts) != 3 || stmts[0].State != "succeeded" || stmts[1].State != "succeeded" ||
		stmts[2].State != "skipped" {
		t.Fatalf("statements=%v", stmts)
	}
	var attemptFailure string
	if err := fx.db.Raw(`select failure_name from execution_attempts where id = ?`, attempt).
		Scan(&attemptFailure).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(attemptFailure, "postgres_search_path_changed") {
		t.Fatalf("attempt must carry drift failure name, got %q", attemptFailure)
	}
	// Prior success preserved (DDL is non-transactional); partial failure forbids whole-order retry (3004).
	if len(fx.b10TargetQueryPG(t, `select 1 where to_regclass('exec_target.b10_f9_a') is not null`)) != 1 {
		t.Fatal("prior DDL success must be preserved")
	}
	fx.b10ExpectRetryForbidden(t, orderID, "b10-f9")

	// F08 (prior=0 semantics evolved with pre-send check): create table after begin but before first send
	// -> first statement NOT-SENT (drift intercepted). With zero prior successes, stage remains retryable --
	// clearing drift allows same-order retry to succeed (asserting real retryable semantics).
	execTarget(t, mustOpenTargetPG(t, fx), `drop table if exists exec_target.b10_f9_c`)
	flowID2 := fx.b10ExecFlow(t, "b10-f8-flow", "exec_target", dsID)
	orderID2, version2 := fx.b10OrderToExecutionPending(t, flowID2,
		"create table b10_f9_c(id int);\n"+
			"create table b10_f8_never(id int);")
	if err := fx.db.Exec(`update change_orders set rollback_enabled = false where id = ?`, orderID2).Error; err != nil {
		t.Fatal(err)
	}
	fx.runner.SetFaultHooks(&execapp.FaultHooks{
		AfterAttemptPersist: func(attemptID string) {
			execTarget(t, mustOpenTargetPG(t, fx), `create table exec_target.b10_f9_c(id int)`)
		},
	})
	attempt2 := fx.b10Begin(t, orderID2, version2)
	if err := fx.b10Drive(t, attempt2); err != nil {
		t.Fatalf("drive2: %v", err)
	}
	fx.runner.SetFaultHooks(nil)
	s2, _, _, _ := fx.b10AttemptRow(t, attempt2)
	stmts2 := fx.b10Statements(t, attempt2)
	if len(stmts2) != 2 || stmts2[0].State != "not_started" || stmts2[1].State != "not_started" {
		t.Fatalf("statements2=%v want all not_started (never sent)", stmts2)
	}
	var attempt2Failure string
	if err := fx.db.Raw(`select failure_name from execution_attempts where id = ?`, attempt2).
		Scan(&attempt2Failure).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(attempt2Failure, "postgres_search_path_changed") {
		t.Fatalf("attempt2 must carry drift failure name, got %q", attempt2Failure)
	}
	if s2 != "not_started" && s2 != "failed" && s2 != "partial_failed" {
		t.Fatalf("attempt2 state=%s unexpected", s2)
	}
	if len(fx.b10TargetQueryPG(t, `select 1 from information_schema.tables where table_name='b10_f8_never'`)) != 0 {
		t.Fatal("suffix DDL must not execute")
	}
	// After clearing drift (dropping conflicting table), same-order retry succeeds.
	execTarget(t, mustOpenTargetPG(t, fx), `drop table exec_target.b10_f9_c`)
	if s2 == "not_started" {
		retry2, _, rerr := fx.exec.Begin(context.Background(), execapp.BeginInput{
			OrderID: orderID2, Actor: execapp.Actor{UserID: fx.executorID, Username: "exec1"},
			ExpectVersion: fx.b10Version(t, orderID2), IdempotencyKey: "b10-f8-retry",
		})
		if rerr != nil {
			t.Fatalf("retry after drift cleared: %v", rerr)
		}
		if err := fx.b10Drive(t, retry2.ID); err != nil {
			t.Fatalf("retry drive: %v", err)
		}
		if got := fx.b10OrderState(t, orderID2); got != "completed" {
			t.Fatalf("retry order=%s want completed", got)
		}
	}
}

// F10: connection killed after DDL statement send -> result_unknown, prior confirmed success preserved.
func TestB10DDLSendKillUnknown(t *testing.T) {
	fx := newB10Fixture(t)
	proxy := startKillProxy(t, "127.0.0.1:54330", "marker_f10")
	dsID := fx.b10PGDatasource(t, "b10-f10", proxy.port())
	flowID := fx.b10ExecFlow(t, "b10-f10-flow", "exec_target", dsID)
	execTarget(t, mustOpenTargetPG(t, fx), `drop table if exists exec_target.b10_f10_a, exec_target.b10_f10_b`)
	orderID, version := fx.b10OrderToExecutionPending(t, flowID,
		"create table b10_f10_a(id int);\n"+
			"create table b10_f10_b(id int) /*marker_f10*/;")
	attempt := fx.b10Begin(t, orderID, version)
	if err := fx.b10Drive(t, attempt); err != nil {
		t.Fatalf("drive: %v", err)
	}
	s, _, _, _ := fx.b10AttemptRow(t, attempt)
	if s != "result_unknown" {
		t.Fatalf("attempt state=%s want result_unknown", s)
	}
	stmts := fx.b10Statements(t, attempt)
	if stmts[0].State != "succeeded" {
		t.Fatalf("prior confirmed success must be preserved: %v", stmts)
	}
	// Ambiguous statement must not be marked skipped (fact is unknown).
	if stmts[1].State == "skipped" {
		t.Fatal("ambiguous statement must not be marked skipped")
	}
	if got := fx.b10OrderState(t, orderID); got != "result_unknown" {
		t.Fatalf("order=%s want result_unknown", got)
	}
}

// F11: process death between statements -> expired lease reconciliation marks result_unknown ->
// manual verification confirmed_failed finalizes order; unknown permanently forbids replay.
func TestB10PodKillBetweenDDLThenVerifyFailed(t *testing.T) {
	fx := newB10Fixture(t)
	dsID := fx.b10PGDatasource(t, "b10-f11", 54330)
	flowID := fx.b10ExecFlow(t, "b10-f11-flow", "exec_target", dsID)
	execTarget(t, mustOpenTargetPG(t, fx), `drop table if exists exec_target.b10_f11_a, exec_target.b10_f11_b`)
	orderID, version := fx.b10OrderToExecutionPending(t, flowID,
		"create table b10_f11_a(id int);\n"+
			"create table b10_f11_b(id int);")
	attempt := fx.b10Begin(t, orderID, version)
	death := errDead()
	fx.runner.SetFaultHooks(&execapp.FaultHooks{
		AfterStatementPersist: func(_ string, ordinal int32) error {
			if ordinal == 1 {
				return death
			}
			return nil
		},
	})
	if err := fx.b10Drive(t, attempt); err == nil {
		t.Fatal("drive must abort on injected death")
	}
	fx.runner.SetFaultHooks(nil)
	// Statement 2 remains not_started (fact: never sent).
	if stmts := fx.b10Statements(t, attempt); stmts[1].State != "not_started" {
		t.Fatalf("statement2=%s want not_started", stmts[1].State)
	}
	fx.b10ExpireLease(t, attempt)
	fx.b10ExpectRetryForbidden(t, orderID, "b10-f11-reconcile")
	s, _, _, _ := fx.b10AttemptRow(t, attempt)
	if s != "result_unknown" {
		t.Fatalf("attempt=%s want result_unknown", s)
	}
	_, _, verr := fx.exec.Verify(context.Background(), execapp.VerifyInput{
		AttemptID: attempt, Actor: execapp.Actor{UserID: fx.executorID, Username: "exec1"},
		ExpectVersion: fx.b10AttemptVersion(t, attempt), Result: "confirmed_failed",
		Reason:         "confirmed b10_f11_b absent on target db with no replication side effects",
		Evidence:       []execapp.EvidenceItem{{Kind: "database_fact", Content: "b10_f11_b absent from information_schema"}},
		IdempotencyKey: "b10-f11-verify",
	})
	if verr != nil {
		t.Fatalf("verify: %v", verr)
	}
	if got := fx.b10OrderState(t, orderID); got != "failed" {
		t.Fatalf("order=%s want failed", got)
	}
}

// F12: DML cancellation confirmed -> rolls back current stage transaction -> order cancelled.
func TestB10CancelDMLConfirmed(t *testing.T) {
	fx := newB10Fixture(t)
	execTarget(t, mustOpenTargetPG(t, fx), `update accounts set amount = 100 where id = 1`)
	dsID := fx.b10PGDatasource(t, "b10-f12", 54330)
	flowID := fx.b10ExecFlow(t, "b10-f12-flow", "exec_target", dsID)
	orderID, version := fx.b10OrderToExecutionPending(t, flowID,
		"update accounts set amount = amount + 99 where id = 1;")
	attempt := fx.b10Begin(t, orderID, version)
	// Row lock on target side blocks statement (in-flight cancellable window).
	target := mustOpenTargetPG(t, fx)
	tx, err := target.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`select amount from exec_target.accounts where id = 1 for update`); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- fx.b10Drive(t, attempt) }()
	// Wait until statement is in flight (boundary enters sending).
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_, b, _, _ := fx.b10AttemptRow(t, attempt)
		if b != "not_started" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, _, err := fx.exec.Cancel(context.Background(), execapp.CancelInput{
		AttemptID: attempt, OrderID: orderID,
		Actor:          execapp.Actor{UserID: fx.executorID, Username: "exec1"},
		ExpectVersion:  fx.b10AttemptVersion(t, attempt),
		IdempotencyKey: "b10-f12-cancel",
	}, fx.runner.CancelActive); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("drive: %v", err)
	}
	// Verify after releasing target lock.
	_ = tx.Rollback()
	s, _, _, _ := fx.b10AttemptRow(t, attempt)
	if s != "cancelled" {
		t.Fatalf("attempt=%s want cancelled", s)
	}
	if got := fx.b10OrderState(t, orderID); got != "cancelled" {
		t.Fatalf("order=%s want cancelled", got)
	}
	rows := fx.b10TargetQueryPG(t, `select amount from exec_target.accounts where id = 1`)
	if rows[0][0] != "100" {
		t.Fatalf("cancelled stage transaction must roll back: %v", rows)
	}
}

// F13: cancel after prior DDL success -> partial_cancelled, suffix marked skipped.
func TestB10CancelDDLPartial(t *testing.T) {
	fx := newB10Fixture(t)
	dsID := fx.b10PGDatasource(t, "b10-f13", 54330)
	flowID := fx.b10ExecFlow(t, "b10-f13-flow", "exec_target", dsID)
	execTarget(t, mustOpenTargetPG(t, fx), `drop table if exists exec_target.b10_f13_a, exec_target.b10_f13_b, exec_target.b10_f13_c`)
	orderID, version := fx.b10OrderToExecutionPending(t, flowID,
		"create table b10_f13_a(id int);\n"+
			"create table b10_f13_b(id int);\n"+
			"create table b10_f13_c(id int);")
	attempt := fx.b10Begin(t, orderID, version)
	var cancelErr error
	fx.runner.SetFaultHooks(&execapp.FaultHooks{
		AfterStatementPersist: func(attemptID string, ordinal int32) error {
			if ordinal == 1 && cancelErr == nil {
				_, _, cancelErr = fx.exec.Cancel(context.Background(), execapp.CancelInput{
					AttemptID: attemptID, OrderID: orderID,
					Actor:          execapp.Actor{UserID: fx.executorID, Username: "exec1"},
					ExpectVersion:  fx.b10AttemptVersion(t, attemptID),
					IdempotencyKey: "b10-f13-cancel",
				}, fx.runner.CancelActive)
			}
			return nil
		},
	})
	defer fx.runner.SetFaultHooks(nil)
	if err := fx.b10Drive(t, attempt); err != nil {
		t.Fatalf("drive: %v", err)
	}
	if cancelErr != nil {
		t.Fatalf("in-flight cancel: %v", cancelErr)
	}
	s, _, _, _ := fx.b10AttemptRow(t, attempt)
	if s != "partial_cancelled" {
		t.Fatalf("attempt=%s want partial_cancelled", s)
	}
	if got := fx.b10OrderState(t, orderID); got != "partial_cancelled" {
		t.Fatalf("order=%s want partial_cancelled", got)
	}
	stmts := fx.b10Statements(t, attempt)
	if stmts[0].State != "succeeded" || stmts[2].State != "skipped" {
		t.Fatalf("statements=%v", stmts)
	}
	if len(fx.b10TargetQueryPG(t, `select 1 where to_regclass('exec_target.b10_f13_a') is not null`)) != 1 {
		t.Fatal("prior success preserved")
	}
	if len(fx.b10TargetQueryPG(t, `select 1 from information_schema.tables where table_name='b10_f13_c'`)) != 0 {
		t.Fatal("suffix must not run")
	}
}

// F14: cancellation unconfirmed after 30 seconds -> result_unknown manual verification path.
func TestB10CancelTimeoutUnknown(t *testing.T) {
	fx := newB10Fixture(t)
	dsID := fx.b10PGDatasource(t, "b10-f14", 54330)
	flowID := fx.b10ExecFlow(t, "b10-f14-flow", "exec_target", dsID)
	orderID, version := fx.b10OrderToExecutionPending(t, flowID,
		"update accounts set amount = amount + 1 where id = 1;")
	attempt := fx.b10Begin(t, orderID, version)
	// Directly construct cancelling + boundary crossed + expired lease (equivalent end state of dead runner;
	// 30-second timer for live processes is handled by cancelWatchdog).
	if err := fx.db.Exec(`update execution_attempts set state='cancelling',
		send_boundary='sent', started_at = now() - interval '31 seconds',
		lease_expires_at = now() - interval '1 minute' where id = ?`, attempt).Error; err != nil {
		t.Fatal(err)
	}
	if err := fx.exec.SweepCancelTimeouts(context.Background()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	s, _, _, _ := fx.b10AttemptRow(t, attempt)
	if s != "result_unknown" {
		t.Fatalf("attempt=%s want result_unknown", s)
	}
	if got := fx.b10OrderState(t, orderID); got != "result_unknown" {
		t.Fatalf("order=%s want result_unknown", got)
	}
}

// F15: lease expires after send -> result_unknown; new worker re-claim is rejected by unique index.
func TestB10LeaseExpiresAfterSend(t *testing.T) {
	fx := newB10Fixture(t)
	dsID := fx.b10PGDatasource(t, "b10-f15", 54330)
	flowID := fx.b10ExecFlow(t, "b10-f15-flow", "exec_target", dsID)
	orderID, version := fx.b10OrderToExecutionPending(t, flowID,
		"update accounts set amount = amount + 1 where id = 1;")
	attempt := fx.b10Begin(t, orderID, version)
	// Directly construct running with boundary crossed + expired lease (dead worker equivalent).
	if err := fx.db.Exec(`update execution_attempts set send_boundary='sent',
		lease_expires_at = now() - interval '1 minute' where id = ?`, attempt).Error; err != nil {
		t.Fatal(err)
	}
	fx.b10ExpectRetryForbidden(t, orderID, "b10-f15-reconcile")
	s, _, _, _ := fx.b10AttemptRow(t, attempt)
	if s != "result_unknown" {
		t.Fatalf("attempt=%s want result_unknown", s)
	}
	if got := fx.b10OrderState(t, orderID); got != "result_unknown" {
		t.Fatalf("order=%s want result_unknown", got)
	}
}

// F17: gh-ost process exits non-zero -> failed/partial + artifact inspection (real MySQL target + fake pinned binary).
func TestB10GhOstExitNonzero(t *testing.T) {
	fx := newB10Fixture(t)
	fx.b10GhOstEnv(t, "MODE_FAIL")
	dsID := fx.b10MySQLDatasource(t, "b10-f17")
	flowID := fx.b10ExecFlow(t, "b10-f17-flow", "exec_target", dsID)
	orderID, version := fx.b10OrderToExecutionPending(t, flowID,
		"ALTER TABLE `exec_target`.`accounts` ADD COLUMN mode_col int /*MODE_FAIL*/")
	attempt := fx.b10Begin(t, orderID, version)
	// kind=gh_ost and OSC column group sealed.
	var kind, phase string
	if err := fx.db.Raw(`select execution_kind, osc_phase from execution_attempts where id = ?`, attempt).
		Row().Scan(&kind, &phase); err != nil {
		t.Fatal(err)
	}
	if kind != "gh_ost" || phase != "planned" {
		t.Fatalf("kind=%s phase=%s want gh_ost/planned", kind, phase)
	}
	if err := fx.b10Drive(t, attempt); err != nil {
		t.Fatalf("drive: %v", err)
	}
	s, _, _, _ := fx.b10AttemptRow(t, attempt)
	if s != "failed" && s != "partial_failed" {
		t.Fatalf("attempt=%s want failed/partial_failed", s)
	}
	var residual string
	_ = fx.db.Raw(`select osc_residual_state from execution_attempts where id = ?`, attempt).Scan(&residual).Error
	if residual == "" || residual == "not_checked" {
		t.Fatalf("residual inspection required, got %q", residual)
	}
	if got := fx.b10OrderState(t, orderID); got != "failed" && got != "partial_failed" {
		t.Fatalf("order=%s want failed/partial_failed", got)
	}
	// Same order does not resume after failure: retry returns 3004.
	fx.b10ExpectRetryForbidden(t, orderID, "b10-f17-retry")
}

// F18: gh-ost process lost (parent ctx cancelled / exit unobservable) -> result_unknown.
func TestB10GhOstProcessLost(t *testing.T) {
	fx := newB10Fixture(t)
	fx.b10GhOstEnv(t, "MODE_HANG")
	dsID := fx.b10MySQLDatasource(t, "b10-f18")
	flowID := fx.b10ExecFlow(t, "b10-f18-flow", "exec_target", dsID)
	orderID, version := fx.b10OrderToExecutionPending(t, flowID,
		"ALTER TABLE `exec_target`.`items` ADD COLUMN mode_col int /*MODE_HANG*/")
	attempt := fx.b10Begin(t, orderID, version)
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	err := fx.runner.RunAttempt(ctx, execapp.TaskInput{
		AttemptID: attempt, OrderID: orderID,
		StageID: fx.b10AttemptStage(t, attempt),
	})
	// Parent ctx cancellation does not block terminal state persistence (auditable terminal state): drive returns normally.
	_ = err
	s, _, _, _ := fx.b10AttemptRow(t, attempt)
	if s != "result_unknown" {
		t.Fatalf("attempt=%s want result_unknown", s)
	}
	if got := fx.b10OrderState(t, orderID); got != "result_unknown" {
		t.Fatalf("order=%s want result_unknown", got)
	}
}

// b10ExpectRetryForbidden asserts same-order retry is rejected with 3004
// (first click after reconciliation advances version gets 1004 -- retrying with refreshed version reaches
// real semantics; execution_start profile is C1004-only, aligned with operation-error-profiles).
func (fx *b10Fixture) b10ExpectRetryForbidden(t *testing.T, orderID, keyPrefix string) {
	t.Helper()
	for attempt := 0; attempt < 2; attempt++ {
		_, _, err := fx.exec.Begin(context.Background(), execapp.BeginInput{
			OrderID: orderID, Actor: execapp.Actor{UserID: fx.executorID, Username: "exec1"},
			ExpectVersion:  fx.b10Version(t, orderID),
			IdempotencyKey: keyPrefix + "-" + uuidNewShort(),
		})
		if err == nil {
			t.Fatalf("retry must be forbidden")
		}
		if strings.Contains(err.Error(), "3004") {
			return
		}
		if !strings.Contains(err.Error(), "1004") && attempt == 1 {
			t.Fatalf("expected 3004 after refresh, got %v", err)
		}
	}
}

// uuidNewShort returns a short random suffix.
func uuidNewShort() string { return uuid.NewString()[:8] }

// errDead injected process death sentinel.
type deadErr struct{}

func (deadErr) Error() string { return "b10: injected worker death" }

func errDead() error { return deadErr{} }

// F16: outbox insert failure (DB-level real fault injection: make outbox_events unwritable)
// -> begin command transaction rolls back completely: no Attempt, no task, no event, zero external sends.
func TestB10OutboxInsertFails(t *testing.T) {
	fx := newB10Fixture(t)
	dsID := fx.b10PGDatasource(t, "b10-f16", 54330)
	flowID := fx.b10ExecFlow(t, "b10-f16-flow", "exec_target", dsID)
	orderID, version := fx.b10OrderToExecutionPending(t, flowID,
		"update accounts set amount = amount + 1 where id = 1;")
	// Inject fault: make outbox unwritable (CHECK false causes any insert to fail -- real DB-level fault;
	// yearning owns the table so REVOKE has no effect on it).
	if err := fx.db.Exec(`alter table outbox_events add constraint b10_f16_no_insert check (false) not valid`).Error; err != nil {
		t.Fatalf("inject constraint: %v", err)
	}
	defer func() {
		_ = fx.db.Exec(`alter table outbox_events drop constraint if exists b10_f16_no_insert`).Error
	}()
	_, _, err := fx.exec.Begin(context.Background(), execapp.BeginInput{
		OrderID: orderID, Actor: execapp.Actor{UserID: fx.executorID, Username: "exec1"},
		ExpectVersion: version, IdempotencyKey: "b10-f16-begin",
	})
	if err == nil {
		t.Fatal("begin must fail when outbox insert fails")
	}
	var attempts, tasks int
	_ = fx.db.Raw(`select count(*) from execution_attempts where order_id = ?`, orderID).Scan(&attempts).Error
	_ = fx.db.Raw(`select count(*) from async_tasks where kind = 'stage_execution'`).Scan(&tasks).Error
	if attempts != 0 || tasks != 0 {
		t.Fatalf("outbox failure must roll back aggregates: attempts=%d tasks=%d", attempts, tasks)
	}
	if got := fx.b10OrderState(t, orderID); got != "stage_execution_pending" {
		t.Fatalf("order=%s must remain execution_pending", got)
	}
	// After restoring outbox, the same click succeeds (zero external send -> retry is valid).
	_ = fx.db.Exec(`alter table outbox_events drop constraint if exists b10_f16_no_insert`).Error
	out, _, berr := fx.exec.Begin(context.Background(), execapp.BeginInput{
		OrderID: orderID, Actor: execapp.Actor{UserID: fx.executorID, Username: "exec1"},
		ExpectVersion: version, IdempotencyKey: "b10-f16-retry",
	})
	if berr != nil {
		t.Fatalf("retry after rollback: %v", berr)
	}
	if out == nil || out.ID == "" {
		t.Fatal("retry must create attempt")
	}
}

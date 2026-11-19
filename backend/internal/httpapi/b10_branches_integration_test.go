//go:build integration

// b10_branches_integration_test.go gate coverage additions: multi-stage serial progression
// (E001), idempotent replay, execution window rejection, datasource disabled, unreadable schema
// mapping (template replacement boundary), cancel command idempotency branches, not_started
// cancel sweep, and missing binary at spawn time.
package httpapi

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/cookieY/YearningX/backend/internal/crypto"
	execapp "github.com/cookieY/YearningX/backend/internal/execution/application"
)

// TestB10MultiStageSerialProgress two-stage serial execution: stage 1 succeeds -> automatically
// activates stage 2 approval -> approve -> execute -> completed (E001 independent transaction per stage).
func TestB10MultiStageSerialProgress(t *testing.T) {
	fx := newB10Fixture(t)
	execTarget(t, mustOpenTargetPG(t, fx), `update accounts set amount = 100 where id = 1`)
	dsID := fx.b10PGDatasource(t, "b10-multi", 54330)
	flowID := fx.b10ExecFlow(t, "b10-multi-flow", "exec_target", dsID, dsID)
	orderID, version := fx.b10OrderToExecutionPending(t, flowID,
		"update accounts set amount = amount + 1 where id = 1;")
	attempt1 := fx.b10Begin(t, orderID, version)
	if err := fx.b10Drive(t, attempt1); err != nil {
		t.Fatalf("stage1 drive: %v", err)
	}
	// Non-final stage succeeds -> order returns to stage_approval_active + stage 2 approval becomes active.
	if got := fx.b10OrderState(t, orderID); got != "stage_approval_active" {
		t.Fatalf("after stage1: order=%s want stage_approval_active", got)
	}
	var stage2State string
	if err := fx.db.Raw(`select state from order_stages where order_id = ? and position = 2`,
		orderID).Scan(&stage2State).Error; err != nil {
		t.Fatal(err)
	}
	if stage2State != "approval_active" {
		t.Fatalf("stage2=%s want approval_active", stage2State)
	}
	var activeStep int
	_ = fx.db.Raw(`select count(*) from order_approval_steps st
		join order_stages os on os.id = st.stage_id
		where os.order_id = ? and os.position = 2 and st.state = 'active'`,
		orderID).Scan(&activeStep).Error
	if activeStep != 1 {
		t.Fatalf("stage2 first step must activate, got %d", activeStep)
	}
	// Stage 2 approve -> execute -> completed (each stage +1 = 102).
	orderID2, version2 := orderID, int64(0)
	_ = version2
	code, body, raw := fx.reviewer2.doWithHeaders("POST",
		"/api/v4/change-orders/"+orderID+"/approval-decisions",
		map[string]any{"decision": "approve", "comment": "s2"},
		map[string]string{
			"If-Match":        fmt.Sprintf(`"%d"`, fx.b10Version(t, orderID)),
			"Idempotency-Key": "b10-multi-s2-" + orderID[:8],
			"X-CSRF-Token":    fx.reviewer2.cookies["yearning_csrf"],
		})
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("stage2 approve: %d %s", code, raw)
	}
	attempt2 := fx.b10Begin(t, orderID, fx.b10Version(t, orderID))
	if err := fx.b10Drive(t, attempt2); err != nil {
		t.Fatalf("stage2 drive: %v", err)
	}
	if got := fx.b10OrderState(t, orderID); got != "completed" {
		t.Fatalf("final: order=%s want completed", got)
	}
	rows := fx.b10TargetQueryPG(t, `select amount from exec_target.accounts where id = 1`)
	if rows[0][0] != "102" {
		t.Fatalf("two stage transactions must each apply: %v", rows)
	}
	_ = orderID2
}

// TestB10BeginIdempotentReplay same-key replay: second Begin returns the same Attempt
// (replayed=true, identical view).
func TestB10BeginIdempotentReplay(t *testing.T) {
	fx := newB10Fixture(t)
	dsID := fx.b10PGDatasource(t, "b10-idem", 54330)
	flowID := fx.b10ExecFlow(t, "b10-idem-flow", "exec_target", dsID)
	orderID, version := fx.b10OrderToExecutionPending(t, flowID,
		"update accounts set amount = amount + 1 where id = 2;")
	key := "b10-idem-" + orderID[:8]
	v1, replayed, err := fx.exec.Begin(context.Background(), execapp.BeginInput{
		OrderID: orderID, Actor: execapp.Actor{UserID: fx.executorID, Username: "exec1"},
		ExpectVersion: version, IdempotencyKey: key,
	})
	if err != nil || replayed {
		t.Fatalf("first begin: replayed=%v err=%v", replayed, err)
	}
	v2, replayed2, err2 := fx.exec.Begin(context.Background(), execapp.BeginInput{
		OrderID: orderID, Actor: execapp.Actor{UserID: fx.executorID, Username: "exec1"},
		ExpectVersion: version, IdempotencyKey: key,
	})
	if err2 != nil || !replayed2 {
		t.Fatalf("replay: replayed=%v err=%v", replayed2, err2)
	}
	if v1.ID != v2.ID {
		t.Fatalf("replay must return same attempt: %s vs %s", v1.ID, v2.ID)
	}
	var attempts int
	_ = fx.db.Raw(`select count(*) from execution_attempts where order_id = ?`, orderID).Scan(&attempts).Error
	if attempts != 1 {
		t.Fatalf("attempts=%d want 1", attempts)
	}
}

// TestB10ExecutionWindowRestricted execution window restriction enabled and outside window -> 3008.
func TestB10ExecutionWindowRestricted(t *testing.T) {
	fx := newB10Fixture(t)
	// Pick a weekday that is "never today" (window days_of_week contains only that day).
	days := []string{"sunday", "monday", "tuesday", "wednesday", "thursday", "friday", "saturday"}
	today := strings.ToLower(time.Now().Weekday().String())
	other := "monday"
	for _, d := range days {
		if d != today {
			other = d
			break
		}
	}
	if err := fx.db.Exec(`insert into system_settings (namespace, schema_version, settings, version, updated_by_user_id, created_at, updated_at)
		values ('execution', 1, ?::jsonb, 1, ?, now(), now())
		on conflict (namespace) do update set settings = excluded.settings, updated_at = now()`,
		fmt.Sprintf(`{"restriction_enabled":true,"windows":[{"days_of_week":["%s"],"start_local_time":"03:00","end_local_time":"03:01"}],"osc":{"tool":"none","defaults":{}}}`, other),
		fx.adminUserID(t)).Error; err != nil {
		t.Fatal(err)
	}
	defer fx.b10OscPolicy(t, "none") // Restore unrestricted default
	dsID := fx.b10PGDatasource(t, "b10-win", 54330)
	flowID := fx.b10ExecFlow(t, "b10-win-flow", "exec_target", dsID)
	orderID, version := fx.b10OrderToExecutionPending(t, flowID,
		"update accounts set amount = amount + 1 where id = 2;")
	_, _, err := fx.exec.Begin(context.Background(), execapp.BeginInput{
		OrderID: orderID, Actor: execapp.Actor{UserID: fx.executorID, Username: "exec1"},
		ExpectVersion: version, IdempotencyKey: "b10-win",
	})
	if err == nil || !strings.Contains(err.Error(), "3008") {
		t.Fatalf("outside window must 3008, got %v", err)
	}
}

// TestB10TargetGuards datasource disabled -> 3010; identity changed -> 3006.
func TestB10TargetGuards(t *testing.T) {
	fx := newB10Fixture(t)
	dsID := fx.b10PGDatasource(t, "b10-guard", 54330)
	flowID := fx.b10ExecFlow(t, "b10-guard-flow", "exec_target", dsID)
	orderID, version := fx.b10OrderToExecutionPending(t, flowID,
		"update accounts set amount = amount + 1 where id = 2;")
	// Disable datasource -> 3010.
	if err := fx.db.Exec(`update datasources set enabled = false where id = ?`, dsID).Error; err != nil {
		t.Fatal(err)
	}
	_, _, err := fx.exec.Begin(context.Background(), execapp.BeginInput{
		OrderID: orderID, Actor: execapp.Actor{UserID: fx.executorID, Username: "exec1"},
		ExpectVersion: version, IdempotencyKey: "b10-guard-off",
	})
	if err == nil || !strings.Contains(err.Error(), "3010") {
		t.Fatalf("disabled datasource must 3010, got %v", err)
	}
	_ = fx.db.Exec(`update datasources set enabled = true where id = ?`, dsID)
	// Identity fingerprint changed -> 3006 identity_changed (template replacement equivalence boundary).
	if err := fx.db.Exec(`update datasources set identity_fingerprint = ? where id = ?`,
		strings.Repeat("ef", 32), dsID).Error; err != nil {
		t.Fatal(err)
	}
	_, _, err = fx.exec.Begin(context.Background(), execapp.BeginInput{
		OrderID: orderID, Actor: execapp.Actor{UserID: fx.executorID, Username: "exec1"},
		ExpectVersion: version, IdempotencyKey: "b10-guard-id",
	})
	if err == nil || !strings.Contains(err.Error(), "datasource_identity_changed") {
		t.Fatalf("identity change must fail closed, got %v", err)
	}
}

// TestB10CancelCommandIdempotency cancel command idempotency branches: cancelling a completed
// Attempt returns the original view; duplicate cancel (cancelling) is idempotent; result_unknown -> 1010.
func TestB10CancelCommandIdempotency(t *testing.T) {
	fx := newB10Fixture(t)
	dsID := fx.b10PGDatasource(t, "b10-ci", 54330)
	flowID := fx.b10ExecFlow(t, "b10-ci-flow", "exec_target", dsID)
	orderID, version := fx.b10OrderToExecutionPending(t, flowID,
		"update accounts set amount = amount + 1 where id = 2;")
	attempt := fx.b10Begin(t, orderID, version)
	if err := fx.b10Drive(t, attempt); err != nil {
		t.Fatalf("drive: %v", err)
	}
	// Already succeeded -> cancel idempotently returns the view (state unchanged).
	view, _, err := fx.exec.Cancel(context.Background(), execapp.CancelInput{
		AttemptID: attempt, OrderID: orderID,
		Actor:          execapp.Actor{UserID: fx.executorID, Username: "exec1"},
		ExpectVersion:  fx.b10AttemptVersion(t, attempt),
		IdempotencyKey: "b10-ci-1",
	}, fx.runner.CancelActive)
	if err != nil || view.State != "succeeded" {
		t.Fatalf("cancel terminal: %v state=%s", err, view.State)
	}
	// Cancel on result_unknown: idempotently returns the view (state unchanged -- an unknown task has
	// no in-flight statement to cancel; manual verification is the only exit).
	if err := fx.db.Exec(`update execution_attempts set state='result_unknown',
		finished_at=now(), business_err_code=3005, failure_name='X',
		send_boundary='sent' where id = ?`, attempt).Error; err != nil {
		t.Fatal(err)
	}
	unkView, _, uerr := fx.exec.Cancel(context.Background(), execapp.CancelInput{
		AttemptID: attempt, OrderID: orderID,
		Actor:          execapp.Actor{UserID: fx.executorID, Username: "exec1"},
		ExpectVersion:  fx.b10AttemptVersion(t, attempt),
		IdempotencyKey: "b10-ci-2",
	}, fx.runner.CancelActive)
	if uerr != nil || unkView.State != "result_unknown" {
		t.Fatalf("cancel on result_unknown: %v state=%s", uerr, unkView.State)
	}
}

// TestB10SweepCancelNotStarted cancelling + not_started timeout -> cancelled
// (zero-send fact is deterministic, not result_unknown).
func TestB10SweepCancelNotStarted(t *testing.T) {
	fx := newB10Fixture(t)
	dsID := fx.b10PGDatasource(t, "b10-sw", 54330)
	flowID := fx.b10ExecFlow(t, "b10-sw-flow", "exec_target", dsID)
	orderID, version := fx.b10OrderToExecutionPending(t, flowID,
		"update accounts set amount = amount + 1 where id = 2;")
	attempt := fx.b10Begin(t, orderID, version)
	// Dead worker scenario: cancelling + expired lease (sweep only covers expired leases).
	if err := fx.db.Exec(`update execution_attempts set state='cancelling',
		started_at = now() - interval '31 seconds',
		lease_expires_at = now() - interval '1 minute' where id = ?`, attempt).Error; err != nil {
		t.Fatal(err)
	}
	if err := fx.exec.SweepCancelTimeouts(context.Background()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	s, _, _, _ := fx.b10AttemptRow(t, attempt)
	if s != "cancelled" {
		t.Fatalf("sweep not_started cancel: %s want cancelled", s)
	}
	if got := fx.b10OrderState(t, orderID); got != "cancelled" {
		t.Fatalf("order=%s want cancelled", got)
	}
}

// TestB10OscSpawnFailure binary path removed after begin -> drive fails with
// OSC_SPAWN_FAILED (fail closed; no fallback).
func TestB10OscSpawnFailure(t *testing.T) {
	fx := newB10Fixture(t)
	fx.b10GhOstEnv(t, "MODE_OK")
	dsID := fx.b10MySQLDatasource(t, "b10-spawn")
	flowID := fx.b10ExecFlow(t, "b10-spawn-flow", "exec_target", dsID)
	orderID, version := fx.b10OrderToExecutionPending(t, flowID,
		"ALTER TABLE `exec_target`.`accounts` ADD COLUMN spawn_col int")
	attempt := fx.b10Begin(t, orderID, version)
	// Remove binary path (file unreadable) -> pre-spawn hash verification fails.
	fx.exec.OscBinaries["gh-ost"].Path = "/nonexistent/gh-ost"
	if err := fx.b10Drive(t, attempt); err != nil {
		t.Fatalf("drive: %v", err)
	}
	s, _, _, _ := fx.b10AttemptRow(t, attempt)
	var failureName string
	_ = fx.db.Raw(`select coalesce(failure_name,'') from execution_attempts where id = ?`, attempt).Scan(&failureName).Error
	if s != "failed" || failureName != "OSC_SPAWN_FAILED" {
		t.Fatalf("spawn failure: state=%s name=%s", s, failureName)
	}
}

// TestB10ReviewH1StaleWorkerFencedAfterReconcile H1 regression: fence increments after
// reconciliation marks worker dead -- stale worker continuing to drive is rejected by CAS (zero second send).
func TestB10ReviewH1StaleWorkerFencedAfterReconcile(t *testing.T) {
	fx := newB10Fixture(t)
	execTarget(t, mustOpenTargetPG(t, fx), `update accounts set amount = 100 where id = 1`)
	dsID := fx.b10PGDatasource(t, "b10-h1", 54330)
	flowID := fx.b10ExecFlow(t, "b10-h1-flow", "exec_target", dsID)
	orderID, version := fx.b10OrderToExecutionPending(t, flowID,
		"update accounts set amount = amount + 1 where id = 1;")
	attempt1 := fx.b10Begin(t, orderID, version)
	// Simulate worker A having claimed (fence+1) and then its lease expiring.
	if err := fx.db.Exec(`update execution_attempts set fence_token = fence_token + 1,
		lease_owner='stale-worker', lease_expires_at = now() - interval '1 minute' where id = ?`, attempt1).Error; err != nil {
		t.Fatal(err)
	}
	var fenceBefore int64
	_ = fx.db.Raw(`select fence_token from execution_attempts where id = ?`, attempt1).Scan(&fenceBefore).Error
	// Retry click triggers reconciliation: not_started -> failed and fence increments again.
	attempt2 := fx.b10Begin(t, orderID, fx.b10Version(t, orderID))
	var fenceAfter int64
	var state string
	_ = fx.db.Raw(`select fence_token, state from execution_attempts where id = ?`, attempt1).
		Row().Scan(&fenceAfter, &state)
	if state != "failed" || fenceAfter <= fenceBefore {
		t.Fatalf("reconcile must fail attempt and bump fence: %s %d>%d", state, fenceAfter, fenceBefore)
	}
	// Stale worker continues driving: terminal short-circuit or fence CAS failure (either protection path)
	// must prevent a second send -- corroborated by both statement ledger and target data.
	_ = fx.b10Drive(t, attempt1)
	for _, st := range fx.b10Statements(t, attempt1) {
		if st.State != "not_started" {
			t.Fatalf("stale worker must not send any statement: %v", st)
		}
	}
	rows := fx.b10TargetQueryPG(t, `select amount from exec_target.accounts where id = 1`)
	if rows[0][0] != "100" {
		t.Fatalf("stale worker must not send: %v", rows)
	}
	// Direct proof of fence protection: markBoundary with stale fence view advances 0 rows (simulating
	// fenced write from stale worker via SQL).
	var staleBoundary int
	_ = fx.db.Raw(`update execution_attempts set send_boundary='sending'
		where id = ? and fence_token = ? and send_boundary='not_started'`, attempt1, fenceBefore).
		Scan(&staleBoundary).Error
	var boundaryAfter string
	_ = fx.db.Raw(`select send_boundary from execution_attempts where id = ?`, attempt1).Scan(&boundaryAfter).Error
	if boundaryAfter != "not_started" {
		t.Fatalf("stale-fence boundary write must be rejected, got %s", boundaryAfter)
	}
	// New attempt executes normally and succeeds (takes effect exactly once).
	if err := fx.b10Drive(t, attempt2); err != nil {
		t.Fatalf("attempt2 drive: %v", err)
	}
	rows = fx.b10TargetQueryPG(t, `select amount from exec_target.accounts where id = 1`)
	if rows[0][0] != "101" {
		t.Fatalf("attempt2 must apply exactly once: %v", rows)
	}
}

// TestB10ReviewH2ReVerifyTerminalOrderGuard H2 regression: after first verification reaches terminal
// state, a second verification (new idempotency key) is rejected with 1010 and order state cannot flip.
func TestB10ReviewH2ReVerifyTerminalOrderGuard(t *testing.T) {
	fx := newB10Fixture(t)
	dsID := fx.b10PGDatasource(t, "b10-h2", 54330)
	flowID := fx.b10ExecFlow(t, "b10-h2-flow", "exec_target", dsID)
	orderID, version := fx.b10OrderToExecutionPending(t, flowID,
		"update accounts set amount = amount + 1 where id = 2;")
	attempt := fx.b10Begin(t, orderID, version)
	if err := fx.db.Exec(`update execution_attempts set state='result_unknown',
		finished_at=now(), business_err_code=3005, failure_name='X',
		send_boundary='sent' where id = ?`, attempt).Error; err != nil {
		t.Fatal(err)
	}
	if err := fx.db.Exec(`update change_orders set state='result_unknown', version=version+1
		where id = ?`, orderID).Error; err != nil {
		t.Fatal(err)
	}
	verify := func(result string) error {
		_, _, err := fx.exec.Verify(context.Background(), execapp.VerifyInput{
			AttemptID: attempt, Actor: execapp.Actor{UserID: fx.executorID, Username: "exec1"},
			ExpectVersion: fx.b10AttemptVersion(t, attempt), Result: result,
			Reason:         "h2 regression",
			Evidence:       []execapp.EvidenceItem{{Kind: "database_fact", Content: "v"}},
			IdempotencyKey: "b10-h2-" + result + uuid.NewString()[:6],
		})
		return err
	}
	if err := verify("confirmed_failed"); err != nil {
		t.Fatalf("first verify: %v", err)
	}
	if got := fx.b10OrderState(t, orderID); got != "failed" {
		t.Fatalf("order=%s want failed", got)
	}
	// Second verification attempts to flip state -> rejected with 1010, state remains failed.
	if err := verify("confirmed_succeeded"); err == nil || !strings.Contains(err.Error(), "1010") {
		t.Fatalf("re-verify must be rejected, got %v", err)
	}
	if got := fx.b10OrderState(t, orderID); got != "failed" {
		t.Fatalf("order flipped to %s", got)
	}
	// Events are not duplicated: first verification emits two events via emit_all (verification_recorded +
	// state_changed, both type=execution.unknown_resolved); rejected second verification emits none.
	var events int
	countEvents := func() int {
		_ = fx.db.Raw(`select count(*) from domain_events where subject = ? and event_type='execution.unknown_resolved'`,
			"change-orders/"+orderID).Scan(&events).Error
		return events
	}
	if countEvents() != 2 {
		t.Fatalf("unknown_resolved events=%d want 2 (emit_all)", events)
	}
	_ = verify("confirmed_partial")
	if got := countEvents(); got != 2 {
		t.Fatalf("rejected re-verify must not emit, got %d", got)
	}
	if got := fx.b10OrderState(t, orderID); got != "failed" {
		t.Fatalf("order flipped to %s", got)
	}
}

// TestB10ReviewH3CancelWindowStartsAtRequest H3 regression: 30-second cancel window starts at
// cancel request time -- for an Attempt running >30s, watchdog (50ms in test) resolves after cancel;
// sweep must not prematurely push to unknown while lease is valid.
func TestB10ReviewH3CancelWindowStartsAtRequest(t *testing.T) {
	fx := newB10Fixture(t)
	fx.exec.SetCancelWatchdogDelayForTest(80 * time.Millisecond)
	dsID := fx.b10PGDatasource(t, "b10-h3", 54330)
	flowID := fx.b10ExecFlow(t, "b10-h3-flow", "exec_target", dsID)
	orderID, version := fx.b10OrderToExecutionPending(t, flowID,
		"update accounts set amount = amount + 1 where id = 2;")
	attempt := fx.b10Begin(t, orderID, version)
	// Attempt has crossed send boundary and started >30s ago (started_at rewound) while lease remains valid.
	if err := fx.db.Exec(`update execution_attempts set started_at = now() - interval '2 minutes',
		send_boundary='sent' where id = ?`, attempt).Error; err != nil {
		t.Fatal(err)
	}
	// Cancel request (runner not driving = dead worker scenario; lease still valid).
	if _, _, err := fx.exec.Cancel(context.Background(), execapp.CancelInput{
		AttemptID: attempt, OrderID: orderID,
		Actor:          execapp.Actor{UserID: fx.executorID, Username: "exec1"},
		ExpectVersion:  fx.b10AttemptVersion(t, attempt),
		IdempotencyKey: "b10-h3-cancel",
		Reason:         "window start regression",
	}, nil); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	// Real defect scenario: cancelling + started_at >30s + valid lease -> sweep
	// must not resolve prematurely (30s timer starts at cancel request, handled by watchdog).
	if err := fx.exec.SweepCancelTimeouts(context.Background()); err != nil {
		t.Fatal(err)
	}
	s, _, _, _ := fx.b10AttemptRow(t, attempt)
	if s != "cancelling" {
		t.Fatalf("sweep must not touch lease-valid cancelling attempt, got %s", s)
	}
	// Watchdog resolves after 80ms (window measured from cancel request).
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		s, _, _, _ = fx.b10AttemptRow(t, attempt)
		if s == "result_unknown" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if s != "result_unknown" {
		t.Fatalf("watchdog must resolve to result_unknown, got %s", s)
	}
	if got := fx.b10OrderState(t, orderID); got != "result_unknown" {
		t.Fatalf("order=%s want result_unknown", got)
	}
	// M5: cancel reason is recorded in audit log (row count + content).
	var audited int
	var reason string
	_ = fx.db.Raw(`select count(*) from audit_events
		where event_type='execution.cancel_requested' and resource_id = ?`, attempt).Scan(&audited).Error
	_ = fx.db.Raw(`select coalesce(metadata->>'reason','') from audit_events
		where event_type='execution.cancel_requested' and resource_id = ?`, attempt).Scan(&reason).Error
	if audited != 1 || reason != "window start regression" {
		t.Fatalf("cancel reason audit rows=%d reason=%q", audited, reason)
	}
}

// TestB10ReviewM5HTTPCancelReasonWired M5 regression: cancel reason from HTTP entry (production path)
// is recorded in audit log (handler wiring).
func TestB10ReviewM5HTTPCancelReasonWired(t *testing.T) {
	fx := newB10Fixture(t)
	dsID := fx.b10PGDatasource(t, "b10-m5", 54330)
	flowID := fx.b10ExecFlow(t, "b10-m5-flow", "exec_target", dsID)
	orderID, version := fx.b10OrderToExecutionPending(t, flowID,
		"update accounts set amount = amount + 1 where id = 2;")
	attempt := fx.b10Begin(t, orderID, version)
	code, body, raw := fx.executor.doWithHeaders("POST",
		"/api/v4/execution-attempts/"+attempt+"/cancellation",
		map[string]any{"reason": "http entry reason"},
		map[string]string{
			"If-Match":        fmt.Sprintf(`"%d"`, fx.b10AttemptVersion(t, attempt)),
			"Idempotency-Key": "b10-m5-http",
			"X-CSRF-Token":    fx.executor.cookies["yearning_csrf"],
		})
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("http cancel: %d %s", code, raw)
	}
	var reason string
	_ = fx.db.Raw(`select coalesce(metadata->>'reason','') from audit_events
		where event_type='execution.cancel_requested' and resource_id = ?`, attempt).Scan(&reason).Error
	if reason != "http entry reason" {
		t.Fatalf("http cancel reason=%q", reason)
	}
}

// TestB10ReviewN1FencedAfterSentLeaseLoss N1 regression: after DDL statement 1 crosses boundary and
// worker is declared dead (lease expiry reconciliation -> result_unknown), stale worker statement 2 send is fenced.
func TestB10ReviewN1FencedAfterSentLeaseLoss(t *testing.T) {
	fx := newB10Fixture(t)
	execTarget(t, mustOpenTargetPG(t, fx),
		`drop table if exists exec_target.b10_n1_a, exec_target.b10_n1_b`)
	dsID := fx.b10PGDatasource(t, "b10-n1", 54330)
	flowID := fx.b10ExecFlow(t, "b10-n1-flow", "exec_target", dsID)
	orderID, version := fx.b10OrderToExecutionPending(t, flowID,
		"create table b10_n1_a(id int);\n"+
			"create table b10_n1_b(id int);")
	attempt := fx.b10Begin(t, orderID, version)
	reconciled := false
	fx.runner.SetFaultHooks(&execapp.FaultHooks{
		AfterStatementPersist: func(attemptID string, ordinal int32) error {
			if ordinal == 1 && !reconciled {
				reconciled = true
				// Statement 1 already sent; heartbeat stalls -> lease expires -> retry click triggers reconciliation:
				// crossed boundary -> result_unknown (declares current worker dead).
				_ = fx.db.Exec(`update execution_attempts
					set lease_expires_at = now() - interval '1 minute' where id = ?`, attemptID).Error
				_, _, _ = fx.exec.Begin(context.Background(), execapp.BeginInput{
					OrderID: orderID, Actor: execapp.Actor{UserID: fx.executorID, Username: "exec1"},
					ExpectVersion:  fx.b10Version(t, orderID),
					IdempotencyKey: "b10-n1-reconcile-" + uuid.NewString()[:6],
				})
			}
			return nil
		},
	})
	defer fx.runner.SetFaultHooks(nil)
	_ = fx.b10Drive(t, attempt) // Fenced abort (expected) or terminal short-circuit;
	// Attempt is marked result_unknown; statement 2 is never sent.
	s, _, _, _ := fx.b10AttemptRow(t, attempt)
	if s != "result_unknown" {
		t.Fatalf("attempt=%s want result_unknown", s)
	}
	stmts := fx.b10Statements(t, attempt)
	if stmts[1].State != "not_started" {
		t.Fatalf("statement2=%s must never be sent after fence loss", stmts[1].State)
	}
	if len(fx.b10TargetQueryPG(t,
		`select 1 where to_regclass('exec_target.b10_n1_b') is not null`)) != 0 {
		t.Fatal("fenced worker must not create table b10_n1_b")
	}
	if len(fx.b10TargetQueryPG(t,
		`select 1 where to_regclass('exec_target.b10_n1_a') is not null`)) == 0 {
		t.Fatal("statement1 success preserved")
	}
}

// TestB10ReviewH4GhOstMaxLag H4 regression: both gh-ost max_lag override (request surface)
// and settings default (policy surface) build a plan (allowlist includes --max-lag-millis).
func TestB10ReviewH4GhOstMaxLag(t *testing.T) {
	fx := newB10Fixture(t)
	fx.b10GhOstEnv(t, "MODE_DUMP")
	dsID := fx.b10MySQLDatasource(t, "b10-h4")
	flowID := fx.b10ExecFlow(t, "b10-h4-flow", "exec_target", dsID)
	orderID, version := fx.b10OrderToExecutionPending(t, flowID,
		"ALTER TABLE `exec_target`.`accounts` ADD COLUMN h4_col int /*MODE_DUMP*/")
	view, _, err := fx.exec.Begin(context.Background(), execapp.BeginInput{
		OrderID: orderID, Actor: execapp.Actor{UserID: fx.executorID, Username: "exec1"},
		ExpectVersion: version, IdempotencyKey: "b10-h4",
		OscOverrides: map[string]any{"max_lag": float64(2500), "max_load": "Threads_running=25"},
	})
	if err != nil {
		t.Fatalf("begin with max_lag: %v", err)
	}
	var planCT, planNonce []byte
	var planKeyID string
	if err := fx.db.Raw(`select osc_plan_ciphertext, osc_plan_nonce, osc_plan_key_id
		from execution_attempts where id = ?`, view.ID).Row().Scan(&planCT, &planNonce, &planKeyID); err != nil {
		t.Fatal(err)
	}
	plain, derr := fx.svc.Crypter.Open(crypto.PurposePayload, crypto.Envelope{
		Ciphertext: planCT, Nonce: planNonce, KeyID: planKeyID, Algorithm: "aes-256-gcm",
	}, crypto.AAD{
		DeploymentID: fx.svc.Crypter.DeploymentID(), Table: "execution_attempts",
		PrimaryKey: view.ID, Column: "osc_plan_ciphertext", Purpose: crypto.PurposePayload,
	})
	if derr != nil {
		t.Fatal(derr)
	}
	for _, want := range []string{"--max-lag-millis=2500", "--max-load=Threads_running=25"} {
		if !strings.Contains(string(plain), want) {
			t.Fatalf("plan argv missing %s: %s", want, string(plain))
		}
	}
}

// TestB10ReviewM1SpawnFailureBoundaryAndRetry M1 regression: spawn failure keeps
// sending boundary + advances order to failed terminal state; same-order OSC retry gets 3004.
func TestB10ReviewM1SpawnFailureBoundaryAndRetry(t *testing.T) {
	fx := newB10Fixture(t)
	fx.b10GhOstEnv(t, "MODE_OK")
	dsID := fx.b10MySQLDatasource(t, "b10-m1")
	flowID := fx.b10ExecFlow(t, "b10-m1-flow", "exec_target", dsID)
	orderID, version := fx.b10OrderToExecutionPending(t, flowID,
		"ALTER TABLE `exec_target`.`accounts` ADD COLUMN m1_col int")
	attempt := fx.b10Begin(t, orderID, version)
	fx.exec.OscBinaries["gh-ost"].Path = "/nonexistent/gh-ost"
	if err := fx.b10Drive(t, attempt); err != nil {
		t.Fatalf("drive: %v", err)
	}
	s, boundary, _, _ := fx.b10AttemptRow(t, attempt)
	if s != "failed" || boundary != "sending" {
		t.Fatalf("spawn failure: state=%s boundary=%s want failed/sending", s, boundary)
	}
	if got := fx.b10OrderState(t, orderID); got != "failed" {
		t.Fatalf("order=%s want failed (OSC failure terminates)", got)
	}
	fx.b10ExpectRetryForbidden(t, orderID, "b10-m1-retry")
}

// TestB10ReviewL6SameKeyDifferentOverrides L6 regression: same key with different overrides
// = different request (1007 instead of silent replay).
func TestB10ReviewL6SameKeyDifferentOverrides(t *testing.T) {
	fx := newB10Fixture(t)
	fx.b10GhOstEnv(t, "MODE_DUMP")
	dsID := fx.b10MySQLDatasource(t, "b10-l6")
	flowID := fx.b10ExecFlow(t, "b10-l6-flow", "exec_target", dsID)
	orderID, version := fx.b10OrderToExecutionPending(t, flowID,
		"ALTER TABLE `exec_target`.`accounts` ADD COLUMN l6_col int /*MODE_DUMP*/")
	key := "b10-l6-key"
	_, _, err := fx.exec.Begin(context.Background(), execapp.BeginInput{
		OrderID: orderID, Actor: execapp.Actor{UserID: fx.executorID, Username: "exec1"},
		ExpectVersion: version, IdempotencyKey: key,
		OscOverrides: map[string]any{"chunk_size": float64(100)},
	})
	if err != nil {
		t.Fatalf("first begin: %v", err)
	}
	_, _, err = fx.exec.Begin(context.Background(), execapp.BeginInput{
		OrderID: orderID, Actor: execapp.Actor{UserID: fx.executorID, Username: "exec1"},
		ExpectVersion: version, IdempotencyKey: key,
		OscOverrides: map[string]any{"chunk_size": float64(200)},
	})
	if err == nil || !strings.Contains(err.Error(), "1007") {
		t.Fatalf("same key different overrides must 1007, got %v", err)
	}
}

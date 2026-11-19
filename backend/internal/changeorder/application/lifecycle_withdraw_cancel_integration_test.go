//go:build integration

// lifecycle_withdraw_cancel_integration_test.go DB-level behavior for withdraw-linked cancellation
// (SEC-0029 / RCP-20261021-WITHDRAW-CANCELS-EXECUTION):
// withdrawing a running order and setting its active Attempt to cancelling take effect atomically
// in the same transaction; uncrossed send boundaries converge to cancelled via existing reconciliation,
// while crossed send boundaries converge to result_unknown (without rewriting the order's terminal state);
// withdrawing a terminal order is idempotent; Attempts already in the cancellation channel are not
// re-cancelled; normal withdrawals (without Attempts) are unaffected.
//
// Reuses the fixture in copy_reveal_integration_test.go (same package and tag; shared DB mutually
// excluded via the same file lock). Requires a real PostgreSQL instance (YEARNING_TEST_DATABASE_*);
// compiles when local DB is unavailable and runs in CI.
package application

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	execapp "github.com/cookieY/YearningX/backend/internal/execution/application"
)

type withdrawFx struct {
	db  *gorm.DB
	svc *Services
	seq int
}

func newWithdrawFx(t *testing.T) *withdrawFx {
	t.Helper()
	// Reuse the existing package fixture (file lock mutex + truncate + Crypter/Recorder wiring).
	cfx := newCopyFx(t)
	return &withdrawFx{db: cfx.db, svc: cfx.svc}
}

func (fx *withdrawFx) nextDisplayNumber() string {
	fx.seq++
	n := fx.seq
	digits := []byte{}
	for {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
		if n == 0 {
			break
		}
	}
	for len(digits) < 6 {
		digits = append([]byte{'0'}, digits...)
	}
	return "YR-20261021-" + string(digits)
}

// sweepService constructs the execution domain reconciliation service (reusing the fixture DB/Crypter/Recorder;
// only calls the existing SweepCancelTimeouts without modifying execution domain code).
func (fx *withdrawFx) sweepService(t *testing.T) *execapp.Services {
	t.Helper()
	svc, err := execapp.NewServices(fx.db, fx.svc.Crypter, fx.svc.Recorder)
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

// withdrawSweep triggers the existing cancellation reconciliation fallback (return value is not asserted:
// residual rows from other tests in the shared DB may report per-row errors; this test only asserts convergence of its own rows).
func withdrawSweep(t *testing.T, fx *withdrawFx) {
	t.Helper()
	_ = fx.sweepService(t).SweepCancelTimeouts(context.Background())
}

// seedOrder creates a minimal work order; terminal states automatically populate terminal_at (order_terminal_time).
func (fx *withdrawFx) seedOrder(t *testing.T, userID, state string) string {
	t.Helper()
	orderID := uuid.NewString()
	terminal := "null"
	switch state {
	case "running", "result_unknown", "submitted", "stage_approval_active",
		"stage_execution_pending", "scheduled", "blocked_datasource_unavailable":
	default:
		terminal = "now()"
	}
	if err := fx.db.Exec(`
		insert into change_orders (id, display_number, submitter_user_id_snapshot,
			submitter_username_snapshot, submitter_display_name_snapshot, title,
			description, state, current_stage_position, sql_hmac, snapshot_hash,
			version, submitted_at, terminal_at)
		values (?, ?, ?, 'u1', 'User One', 'withdraw-cancel-itest', '',
			?, 1, ?, ?, 1, now(), `+terminal+`)`,
		orderID, fx.nextDisplayNumber(), userID, state,
		repeatHex64("ab"), repeatHex64("cd")).Error; err != nil {
		t.Fatal(err)
	}
	return orderID
}

// seedStage creates a minimal stage; terminal stages populate terminal_at (order_stage_time_consistency).
func (fx *withdrawFx) seedStage(t *testing.T, orderID, state string) string {
	t.Helper()
	stageID := uuid.NewString()
	terminalAt := "null"
	switch state {
	case "succeeded", "failed", "partial_failed", "cancelled", "partial_cancelled":
		terminalAt = "now()"
	}
	if err := fx.db.Exec(`
		insert into order_stages (id, order_id, position, datasource_id,
			datasource_name_snapshot, datasource_engine_snapshot,
			datasource_identity_fingerprint, stage_review_snapshot_id,
			review_snapshot_hash, review_gate_passed,
			target_snapshot_ciphertext, target_snapshot_nonce,
			target_snapshot_key_id, target_snapshot_algorithm,
			state, activated_at, terminal_at)
		values (?, ?, 1, ?, 'ds-itest', 'mysql', ?, ?, ?, true,
			''::bytea, ''::bytea, 'k1', 'aes-256-gcm', ?, now(), `+terminalAt+`)`,
		stageID, orderID, uuid.NewString(), repeatHex64("ef"),
		uuid.NewString(), repeatHex64("12"), state).Error; err != nil {
		t.Fatal(err)
	}
	return stageID
}

// seedAttempt creates a minimal execution Attempt (paired lease; running/cancelling semantics require
// non-null started_at and null finished_at; terminal states require error code + name pair).
func (fx *withdrawFx) seedAttempt(t *testing.T, orderID, stageID, state, boundary string) string {
	t.Helper()
	attemptID := uuid.NewString()
	started, finished := "now()", "null"
	switch state {
	case "succeeded", "failed", "partial_failed", "cancelled",
		"partial_cancelled", "result_unknown":
		finished = "now()"
	case "created":
		started = "null"
	}
	errCode, errName := "null", "null"
	switch state {
	case "cancelled", "result_unknown", "failed", "partial_failed", "partial_cancelled":
		errCode, errName = "1010", "'ITEST_TERMINAL'"
	}
	if err := fx.db.Exec(`
		insert into execution_attempts (id, order_id, stage_id,
			initiated_by_user_id_snapshot, initiated_by_username_snapshot,
			execution_kind, state, send_boundary, fence_token,
			lease_owner, lease_expires_at, started_at, finished_at,
			business_err_code, failure_name, version, created_at)
		values (?, ?, ?, ?, 'executor1', 'dml', ?, ?, 1,
			'itest-owner', now() + interval '1 hour', `+started+`, `+finished+`,
			`+errCode+`, `+errName+`, 1, now())`,
		attemptID, orderID, stageID, uuid.NewString(), state, boundary).Error; err != nil {
		t.Fatal(err)
	}
	return attemptID
}

// expireLease simulates runner process death by expiring the lease (precondition for existing reconciliation pickup).
func (fx *withdrawFx) expireLease(t *testing.T, attemptID string) {
	t.Helper()
	if err := fx.db.Exec(`
		update execution_attempts set lease_expires_at = now() - interval '1 second'
		where id = ?`, attemptID).Error; err != nil {
		t.Fatal(err)
	}
}

func (fx *withdrawFx) orderState(t *testing.T, orderID string) (string, int64) {
	t.Helper()
	var row struct {
		State   string
		Version int64
	}
	if err := fx.db.Raw(
		`select state, version from change_orders where id = ?`, orderID).
		Scan(&row).Error; err != nil {
		t.Fatal(err)
	}
	return row.State, row.Version
}

func (fx *withdrawFx) attemptRow(t *testing.T, attemptID string) (state string, version int64) {
	t.Helper()
	var row struct {
		State   string
		Version int64
	}
	if err := fx.db.Raw(
		`select state, version from execution_attempts where id = ?`, attemptID).
		Scan(&row).Error; err != nil {
		t.Fatal(err)
	}
	return row.State, row.Version
}

func (fx *withdrawFx) stageState(t *testing.T, stageID string) string {
	t.Helper()
	var state string
	if err := fx.db.Raw(`select state from order_stages where id = ?`, stageID).
		Scan(&state).Error; err != nil {
		t.Fatal(err)
	}
	return state
}

// cancelAudits counts withdraw-linked cancellation audits for the Attempt (distinguished by the linkage marker
// from records produced by the frozen executor user cancel command).
func (fx *withdrawFx) cancelAudits(t *testing.T, attemptID string) int {
	t.Helper()
	var n int
	if err := fx.db.Raw(`
		select count(*) from audit_events
		where event_type = 'execution.cancel_requested' and resource_id = ?
		  and metadata->>'linkage' = 'change_order.withdrawn'`, attemptID).
		Scan(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

func withdrawOrder(t *testing.T, svc *Services, userID, orderID string, version int64) (*OrderView, error) {
	t.Helper()
	view, _, err := svc.Withdraw(context.Background(), Actor{UserID: userID, Username: "u1"},
		RequestMeta{}, LifecycleInput{
			OrderID: orderID, ExpectVersion: version,
			Reason: "itest withdraw", IdempotencyKey: uuid.NewString(),
		})
	return view, err
}

func TestWithdrawRunningLinksCancelConvergesCancelled(t *testing.T) {
	fx := newWithdrawFx(t)
	userID := uuid.NewString()
	orderID := fx.seedOrder(t, userID, "running")
	stageID := fx.seedStage(t, orderID, "running")
	attemptID := fx.seedAttempt(t, orderID, stageID, "running", "not_started")

	view, err := withdrawOrder(t, fx.svc, userID, orderID, 1)
	if err != nil {
		t.Fatalf("withdraw: %v", err)
	}
	if view.State != "withdrawn_after_partial_execution" {
		t.Fatalf("order state %q want withdrawn_after_partial_execution", view.State)
	}
	// Linkage: Attempt enters the cancellation channel in the same transaction; withdraw audit carries the linkage marker.
	if state, _ := fx.attemptRow(t, attemptID); state != "cancelling" {
		t.Fatalf("attempt state %q want cancelling", state)
	}
	if n := fx.cancelAudits(t, attemptID); n != 1 {
		t.Fatalf("linkage cancel audits=%d want 1", n)
	}

	// Existing reconciliation fallback convergence: expired lease + SweepCancelTimeouts -> uncrossed boundary -> cancelled.
	fx.expireLease(t, attemptID)
	withdrawSweep(t, fx)
	if state, _ := fx.attemptRow(t, attemptID); state != "cancelled" {
		t.Fatalf("attempt state %q want cancelled after sweep", state)
	}
	if state := fx.stageState(t, stageID); state != "cancelled" {
		t.Fatalf("stage state %q want cancelled", state)
	}
	// Reconciliation does not rewrite the work order terminal state (withdrawn_after_partial_execution is preserved).
	if state, _ := fx.orderState(t, orderID); state != "withdrawn_after_partial_execution" {
		t.Fatalf("order state mutated by sweep: %q", state)
	}
}

func TestWithdrawRunningLinksCancelConvergesResultUnknown(t *testing.T) {
	fx := newWithdrawFx(t)
	userID := uuid.NewString()
	orderID := fx.seedOrder(t, userID, "running")
	stageID := fx.seedStage(t, orderID, "running")
	attemptID := fx.seedAttempt(t, orderID, stageID, "running", "sent")

	if _, err := withdrawOrder(t, fx.svc, userID, orderID, 1); err != nil {
		t.Fatalf("withdraw: %v", err)
	}
	if state, _ := fx.attemptRow(t, attemptID); state != "cancelling" {
		t.Fatalf("attempt state %q want cancelling", state)
	}

	// Send boundary crossed: unconfirmed cancellation converges to result_unknown via existing adjudication
	// (manual verification path); work order terminal state is not rewritten.
	fx.expireLease(t, attemptID)
	withdrawSweep(t, fx)
	if state, _ := fx.attemptRow(t, attemptID); state != "result_unknown" {
		t.Fatalf("attempt state %q want result_unknown after sweep", state)
	}
	if state := fx.stageState(t, stageID); state != "result_unknown" {
		t.Fatalf("stage state %q want result_unknown", state)
	}
	if state, _ := fx.orderState(t, orderID); state != "withdrawn_after_partial_execution" {
		t.Fatalf("order state %q want withdrawn_after_partial_execution", state)
	}
}

func TestWithdrawTerminalOrderIsIdempotent(t *testing.T) {
	fx := newWithdrawFx(t)
	userID := uuid.NewString()
	orderID := fx.seedOrder(t, userID, "withdrawn")
	stageID := fx.seedStage(t, orderID, "succeeded")
	attemptID := fx.seedAttempt(t, orderID, stageID, "succeeded", "sent")

	if _, err := withdrawOrder(t, fx.svc, userID, orderID, 1); err == nil {
		t.Fatal("withdraw on terminal order must be rejected")
	}
	if state, version := fx.orderState(t, orderID); state != "withdrawn" || version != 1 {
		t.Fatalf("terminal order mutated: state=%s version=%d", state, version)
	}
	if state, _ := fx.attemptRow(t, attemptID); state != "succeeded" {
		t.Fatalf("terminal attempt mutated: %s", state)
	}
	if n := fx.cancelAudits(t, attemptID); n != 0 {
		t.Fatalf("terminal attempt must not be re-cancelled: audits=%d", n)
	}
}

func TestWithdrawSkipsAlreadyCancellingAttempt(t *testing.T) {
	fx := newWithdrawFx(t)
	userID := uuid.NewString()
	orderID := fx.seedOrder(t, userID, "running")
	stageID := fx.seedStage(t, orderID, "running")
	attemptID := fx.seedAttempt(t, orderID, stageID, "cancelling", "not_started")

	if _, err := withdrawOrder(t, fx.svc, userID, orderID, 1); err != nil {
		t.Fatalf("withdraw: %v", err)
	}
	// Already in cancellation channel: do not re-issue cancellation (version does not advance, no new linked audit).
	if _, version := fx.attemptRow(t, attemptID); version != 1 {
		t.Fatalf("cancelling attempt version %d want 1 (no re-cancel)", version)
	}
	if n := fx.cancelAudits(t, attemptID); n != 0 {
		t.Fatalf("cancelling attempt must not get a second cancel audit: %d", n)
	}
	if state, _ := fx.orderState(t, orderID); state != "withdrawn_after_partial_execution" {
		t.Fatalf("order state %q", state)
	}
}

func TestWithdrawPlainWithoutAttemptsUnaffected(t *testing.T) {
	fx := newWithdrawFx(t)
	userID := uuid.NewString()
	// submitted source (no stage, no Attempt): normal withdrawal is unaffected by linkage.
	// execution_pending source without frozen executors materializes to invalid per W008, which is
	// existing behavior covered by b9 httpapi integration tests.
	orderID := fx.seedOrder(t, userID, "submitted")

	view, err := withdrawOrder(t, fx.svc, userID, orderID, 1)
	if err != nil {
		t.Fatalf("withdraw: %v", err)
	}
	if view.State != "withdrawn" {
		t.Fatalf("order state %q want withdrawn", view.State)
	}
}

// attemptLease reads the Attempt lease expiration timestamp (assertion surface for the bounded window written by linkage).
func (fx *withdrawFx) attemptLease(t *testing.T, attemptID string) time.Time {
	t.Helper()
	var ts time.Time
	if err := fx.db.Raw(
		`select lease_expires_at from execution_attempts where id = ?`, attemptID).
		Scan(&ts).Error; err != nil {
		t.Fatal(err)
	}
	return ts
}

// TestWithdrawCancellingAdjudicatedDespiteLiveHeartbeat review loop closure revision
// (RCP-20261021-WITHDRAW-CANCELS-EXECUTION fallback): setting cancelling via withdraw linkage
// simultaneously writes a 30s bounded lease — even if the Runner heartbeat previously kept renewing
// the lease (Attempt holds a far-future lease while worker is alive), existing SweepCancelTimeouts
// picks it up and adjudicates once the linked lease window expires, without relying on Runner process
// death; uncrossed send boundary -> cancelled, crossed boundary -> result_unknown (manual verification path).
// Runner-side behavior where heartbeat skips renewing cancelling rows is covered by
// TestRenewLeaseSkipsCancelling in the execution package; this test uses "lease expired" to represent
// the state 30 seconds after that mechanism.
func TestWithdrawCancellingAdjudicatedDespiteLiveHeartbeat(t *testing.T) {
	cases := []struct {
		name      string
		boundary  string
		wantState string
		wantStage string
	}{
		{"zero_send_converges_cancelled", "not_started", "cancelled", "cancelled"},
		{"boundary_crossed_converges_result_unknown", "sent", "result_unknown", "result_unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newWithdrawFx(t)
			userID := uuid.NewString()
			orderID := fx.seedOrder(t, userID, "running")
			stageID := fx.seedStage(t, orderID, "running")
			// Live heartbeat renewal state: running Attempt holds a far-future lease (seedAttempt
			// writes now()+1h, equivalent to continuous renewal by a live worker).
			attemptID := fx.seedAttempt(t, orderID, stageID, "running", tc.boundary)

			if _, err := withdrawOrder(t, fx.svc, userID, orderID, 1); err != nil {
				t.Fatalf("withdraw: %v", err)
			}
			if state, _ := fx.attemptRow(t, attemptID); state != "cancelling" {
				t.Fatalf("attempt state %q want cancelling", state)
			}
			// Linkage writes a 30s bounded lease: overwrites the far-future lease renewed by heartbeat (30±5s window).
			if out := time.Until(fx.attemptLease(t, attemptID)); out < 25*time.Second || out > 35*time.Second {
				t.Fatalf("linked lease window %.0fs want ~30s", out.Seconds())
			}
			// Within the window (lease not yet expired), reconciliation does not pick it up — the 30s confirmation window is respected.
			withdrawSweep(t, fx)
			if state, _ := fx.attemptRow(t, attemptID); state != "cancelling" {
				t.Fatalf("attempt adjudicated before lease expiry: %s", state)
			}
			// Window expired (heartbeat does not renew cancelling rows, no process extends the lease) -> reconciliation adjudicates.
			fx.expireLease(t, attemptID)
			withdrawSweep(t, fx)
			if state, _ := fx.attemptRow(t, attemptID); state != tc.wantState {
				t.Fatalf("attempt state %q want %s", state, tc.wantState)
			}
			if state := fx.stageState(t, stageID); state != tc.wantStage {
				t.Fatalf("stage state %q want %s", state, tc.wantStage)
			}
			// Reconciliation does not rewrite the work order terminal state (withdrawn_after_partial_execution is preserved).
			if state, _ := fx.orderState(t, orderID); state != "withdrawn_after_partial_execution" {
				t.Fatalf("order state %q want withdrawn_after_partial_execution", state)
			}
		})
	}
}

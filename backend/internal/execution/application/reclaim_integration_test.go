//go:build integration

// reclaim_integration_test.go SEC-0001/SEC-0060 reclaim adjudication integration tests:
// covers the reclaim dimension of the four acceptance cases in execution-failure-injection.json —
//
//	pod_kill_after_socket_write_before_response
//	dml_commit_confirmed_then_local_persist_fails
//	ddl_success_ack_lost
//	lease_expires_after_send
//
// Each case asserts three invariants: when an Attempt that has crossed the send boundary is driven
// a second time (task reclaim / Execute re-entry), there is at most one external send, the Attempt
// converges to result_unknown, and there is no automatic replay (zero rewrites to the statement ledger).
// Zero sends are proven structurally by the fixture: no datasources rows are created, so any attempt
// to replay from the beginning would fail in openStageTarget due to the missing datasource row —
// if a regression replayed from ordinal 1, the statement ledger would be advanced to failed/not_started
// and fail the test.
//
// Requires a real PostgreSQL instance (YEARNING_TEST_DATABASE_* environment variables; shares the
// database with the httpapi integration fixture, mutually excluded via the same file lock).
// Compiles when local DB is unavailable and runs in CI.
package application

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/execution/domain"
	"github.com/cookieY/YearningX/backend/internal/tasks"
)

// reclaimFx minimal execution domain fixture (metadata DB + Services + Runner; no target DB/datasource).
type reclaimFx struct {
	db     *gorm.DB
	svc    *Services
	runner *Runner
	seq    atomic.Int64
}

// acquireReclaimLock cross-process shared DB mutex (uses the same lock file name as the httpapi fixture).
func acquireReclaimLock(t *testing.T) {
	t.Helper()
	path := os.TempDir() + "/yearningx-itest-shared.lock"
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return // Degrade to unlocked when lock is unavailable (safe for single-package test runs)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return
	}
	t.Cleanup(func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	})
}

func reclaimDSN(t *testing.T) string {
	t.Helper()
	password := os.Getenv("YEARNING_TEST_DATABASE_PASSWORD")
	if password == "" {
		t.Skip("integration PostgreSQL not configured: set YEARNING_TEST_DATABASE_PASSWORD")
	}
	env := func(key, def string) string {
		if v := os.Getenv(key); v != "" {
			return v
		}
		return def
	}
	return fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
		env("YEARNING_TEST_DATABASE_HOST", "localhost"),
		env("YEARNING_TEST_DATABASE_PORT", "54330"),
		env("YEARNING_TEST_DATABASE_USER", "yearning"),
		password,
		env("YEARNING_TEST_DATABASE_DBNAME", "yearning_v4"),
		env("YEARNING_TEST_DATABASE_SSLMODE", "disable"))
}

func newReclaimFx(t *testing.T) *reclaimFx {
	t.Helper()
	acquireReclaimLock(t)
	db, err := gorm.Open(postgres.Open(reclaimDSN(t)), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		t.Fatalf("postgres connect: %v", err)
	}
	if sqlDB, serr := db.DB(); serr == nil {
		t.Cleanup(func() { _ = sqlDB.Close() })
	}
	for _, tbl := range []string{
		"execution_statements", "execution_attempts", "order_stages", "change_orders",
		"domain_events", "outbox_events", "audit_events", "idempotency_records",
	} {
		if err := db.Exec("truncate table " + tbl).Error; err != nil {
			t.Fatalf("truncate %s: %v", tbl, err)
		}
	}
	// Keyring (same format as the httpapi fixture; test-only keys).
	mk := func(seed byte) string { return base64.StdEncoding.EncodeToString(bytesSeed(seed)) }
	dir := t.TempDir()
	doc := fmt.Sprintf(`{"contract_version":1,"deployment_id":"reclaim-itest","keys":[
	{"key_id":"pay-1","purpose":"payload","status":"encrypt_decrypt","created_at":"2026-08-25T00:00:00Z","key_base64":"%s"},
	{"key_id":"state-1","purpose":"oidc_state","status":"encrypt_decrypt","created_at":"2026-08-25T00:00:00Z","key_base64":"%s"},
	{"key_id":"ident-1","purpose":"identifier_token","status":"encrypt_decrypt","created_at":"2026-08-25T00:00:00Z","key_base64":"%s"},
	{"key_id":"fp-1","purpose":"sql_fingerprint","status":"encrypt_decrypt","created_at":"2026-08-25T00:00:00Z","key_base64":"%s"},
	{"key_id":"aff-1","purpose":"cursor_affinity","status":"encrypt_decrypt","created_at":"2026-08-25T00:00:00Z","key_base64":"%s"}]}`,
		mk(1), mk(2), mk(3), mk(4), mk(5))
	path := filepath.Join(dir, "keyring.json")
	if err := os.WriteFile(path, []byte(doc), 0o400); err != nil {
		t.Fatal(err)
	}
	ring, err := crypto.LoadKeyring(path)
	if err != nil {
		t.Fatal(err)
	}
	crypter, err := crypto.NewCrypter(ring)
	if err != nil {
		t.Fatal(err)
	}
	recorder, err := auditlog.NewRecorder(db)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := NewServices(db, crypter, recorder)
	if err != nil {
		t.Fatal(err)
	}
	return &reclaimFx{db: db, svc: svc, runner: NewRunner(svc)}
}

func bytesSeed(seed byte) []byte {
	out := make([]byte, 32)
	for i := range out {
		out[i] = seed
	}
	return out
}

// seedOrder creates a minimal running work order + stage (omits datasource row = structural proof of zero sends).
func (fx *reclaimFx) seedOrder(t *testing.T) (orderID, stageID string) {
	t.Helper()
	orderID, stageID = uuid.NewString(), uuid.NewString()
	n := fx.seq.Add(1)
	mustExec(t, fx.db, `
		insert into change_orders (id, display_number, submitter_user_id_snapshot,
			submitter_username_snapshot, submitter_display_name_snapshot, title,
			description, state, current_stage_position, sql_hmac, snapshot_hash,
			version, submitted_at)
		values (?, ?, ?, 'exec1', 'Exec One', 'reclaim-itest', '',
			'running', 1, ?, ?, 1, now())`,
		orderID, fmt.Sprintf("YR-20260101-%06d", n), uuid.NewString(),
		repeatHex("ab"), repeatHex("cd"))
	mustExec(t, fx.db, `
		insert into order_stages (id, order_id, position, datasource_id,
			datasource_name_snapshot, datasource_engine_snapshot,
			datasource_identity_fingerprint, stage_review_snapshot_id,
			review_snapshot_hash, review_gate_passed, target_snapshot_ciphertext,
			target_snapshot_nonce, target_snapshot_key_id, target_snapshot_algorithm,
			state, version, activated_at)
		values (?, ?, 1, ?, 'reclaim-ds', 'postgresql', ?, ?, ?, true, ?, ?, 'pay-1',
			'aes-256-gcm', 'running', 1, now())`,
		stageID, orderID, uuid.NewString(), repeatHex("ef"), uuid.NewString(),
		repeatHex("01"), []byte{0}, []byte{0})
	return orderID, stageID
}

func repeatHex(s string) string {
	out := make([]byte, 0, 64)
	for len(out) < 64 {
		out = append(out, s...)
	}
	return string(out[:64])
}

type reclaimStmt struct {
	ordinal int32
	state   string
}

// seedAttempt creates execution_attempts + statement ledger rows. When expired=true, rewinds the
// lease to expired (equivalent to time elapsing after worker death).
func (fx *reclaimFx) seedAttempt(t *testing.T, orderID, stageID, state, boundary,
	kind string, fence int64, owner string, expired bool, stmts []reclaimStmt) string {
	t.Helper()
	attemptID := uuid.NewString()
	lease := "now() + interval '10 minutes'"
	if expired {
		lease = "now() - interval '1 minute'"
	}
	mustExec(t, fx.db, `
		insert into execution_attempts (id, order_id, stage_id,
			initiated_by_user_id_snapshot, initiated_by_username_snapshot,
			execution_kind, state, send_boundary, fence_token, lease_owner,
			lease_expires_at, started_at, version, created_at)
		values (?, ?, ?, ?, 'exec1', ?, ?, ?, ?, ?, `+lease+`, now(), 1, now())`,
		attemptID, orderID, stageID, uuid.NewString(), kind, state, boundary, fence, owner)
	for _, st := range stmts {
		var affected any
		var finished any
		if st.state == "succeeded" {
			v := int64(1)
			affected = v
			finished = time.Now().UTC()
		}
		mustExec(t, fx.db, `
			insert into execution_statements (id, attempt_id, ordinal, statement_kind,
				state, affected_row_count, started_at, finished_at)
			values (?, ?, ?, ?, ?, ?, now(), ?)`,
			uuid.NewString(), attemptID, st.ordinal, kind, st.state, affected, finished)
	}
	return attemptID
}

func mustExec(t *testing.T, db *gorm.DB, sql string, args ...any) {
	t.Helper()
	if err := db.Exec(sql, args...).Error; err != nil {
		t.Fatalf("exec %q: %v", sql[:40], err)
	}
}

type attemptSnapshot struct {
	state    string
	boundary string
	fence    int64
}

func (fx *reclaimFx) attemptRow(t *testing.T, attemptID string) attemptSnapshot {
	t.Helper()
	var s attemptSnapshot
	if err := fx.db.Raw(
		`select state, send_boundary, fence_token from execution_attempts where id = ?`,
		attemptID).Row().Scan(&s.state, &s.boundary, &s.fence); err != nil {
		t.Fatal(err)
	}
	return s
}

func (fx *reclaimFx) orderState(t *testing.T, orderID string) string {
	t.Helper()
	var v string
	if err := fx.db.Raw(`select state from change_orders where id = ?`, orderID).Scan(&v).Error; err != nil {
		t.Fatal(err)
	}
	return v
}

// statementStates returns statement ledger states (ordered by ordinal).
func (fx *reclaimFx) statementStates(t *testing.T, attemptID string) []string {
	t.Helper()
	rows, err := fx.db.Raw(
		`select state from execution_statements where attempt_id = ? order by ordinal`,
		attemptID).Rows()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		out = append(out, v)
	}
	return out
}

// TestReplayForbiddenAfterSendBoundary Covers all four acceptance cases: an Attempt that has
// crossed the send boundary is driven a second time (= Runner.Execute/RunAttempt re-entry after
// task reclaim) -> at most one external send (fixture has no datasource, so replay would inevitably
// touch the ledger), attempt -> result_unknown, zero rewrites to the statement ledger (no automatic
// replay), and fence never regresses.
func TestReplayForbiddenAfterSendBoundary(t *testing.T) {
	cases := []struct {
		name     string
		kind     string
		boundary string
		stmts    []reclaimStmt
		wantStmt []string
	}{
		{
			// Process dies after socket write and before response arrival: durable fact = sending.
			name: "pod_kill_after_socket_write_before_response", kind: "dml",
			boundary: "sending",
			stmts:    []reclaimStmt{{1, "not_started"}},
			wantStmt: []string{"not_started"},
		},
		{
			// Reclaimed after DML commit was confirmed but local terminal state persistence failed (statement facts already persisted).
			name: "dml_commit_confirmed_then_local_persist_fails", kind: "dml",
			boundary: "sent",
			stmts:    []reclaimStmt{{1, "succeeded"}},
			wantStmt: []string{"succeeded"},
		},
		{
			// ACK lost after DDL statement 2 was sent: prior confirmed successes are preserved, and
			// unexecuted statements remain not_started (never resent).
			name: "ddl_success_ack_lost", kind: "ddl",
			boundary: "sent",
			stmts:    []reclaimStmt{{1, "succeeded"}, {2, "not_started"}},
			wantStmt: []string{"succeeded", "not_started"},
		},
		{
			// Task lease expires after send and is reclaimed by another worker: must not execute
			// after new_worker_claim (adjudication surface).
			name: "lease_expires_after_send", kind: "dml",
			boundary: "sent",
			stmts:    []reclaimStmt{{1, "not_started"}},
			wantStmt: []string{"not_started"},
		},
	}
	fx := newReclaimFx(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			orderID, stageID := fx.seedOrder(t)
			attemptID := fx.seedAttempt(t, orderID, stageID, "running", tc.boundary,
				tc.kind, 2, "dead-worker-1", true, tc.stmts)
			fenceBefore := fx.attemptRow(t, attemptID).fence
			// Equivalent to task reclaim: another replica drives the same Attempt again.
			err := fx.runner.RunAttempt(context.Background(), TaskInput{
				AttemptID: attemptID, OrderID: orderID, StageID: stageID,
			})
			if err != nil {
				t.Fatalf("reclaim adjudication must succeed, got %v", err)
			}
			snap := fx.attemptRow(t, attemptID)
			if snap.state != string(domain.AttemptResultUnknown) {
				t.Fatalf("attempt state=%s want result_unknown", snap.state)
			}
			if snap.fence <= fenceBefore {
				t.Fatalf("fence regressed: %d -> %d", fenceBefore, snap.fence)
			}
			if got := fx.orderState(t, orderID); got != "result_unknown" {
				t.Fatalf("order state=%s want result_unknown", got)
			}
			got := fx.statementStates(t, attemptID)
			if len(got) != len(tc.wantStmt) {
				t.Fatalf("statements=%v want %v", got, tc.wantStmt)
			}
			for i := range got {
				if got[i] != tc.wantStmt[i] {
					t.Fatalf("statement[%d]=%s want %s (replayed?) full=%v", i, got[i], tc.wantStmt[i], got)
				}
			}
			// result_unknown is terminal: driving it again idempotently returns nil without rewriting history.
			if err := fx.runner.RunAttempt(context.Background(), TaskInput{
				AttemptID: attemptID, OrderID: orderID, StageID: stageID,
			}); err != nil {
				t.Fatalf("terminal re-drive must be idempotent nil, got %v", err)
			}
			if s2 := fx.attemptRow(t, attemptID); s2.state != string(domain.AttemptResultUnknown) {
				t.Fatalf("terminal state mutated on re-drive: %+v", s2)
			}
		})
	}
}

// TestLiveAttemptLeaseNotStealable SEC-0001(b): an active worker's Attempt lease cannot be
// stolen by task reclaim — the claim is rejected (ErrConcurrent) and the Attempt remains untouched;
// when invoked via tasks.Execute, it translates to crash semantics (task does not enter a terminal state and converges later).
func TestLiveAttemptLeaseNotStealable(t *testing.T) {
	fx := newReclaimFx(t)
	orderID, stageID := fx.seedOrder(t)
	attemptID := fx.seedAttempt(t, orderID, stageID, "running", "sent",
		"dml", 2, "live-worker-1", false, []reclaimStmt{{1, "succeeded"}})
	before := fx.attemptRow(t, attemptID)

	err := fx.runner.RunAttempt(context.Background(), TaskInput{
		AttemptID: attemptID, OrderID: orderID, StageID: stageID,
	})
	// domain.ErrConcurrent() constructs a fresh pointer each time (without Is/Unwrap), so errors.Is
	// is always false — check via errors.As + Code (matching the Execute translation branch in run.go).
	var de *domain.Error
	if err == nil || !errors.As(err, &de) || de.Code != domain.CodeConcurrent {
		t.Fatalf("claim must be refused with ErrConcurrent, got %v", err)
	}
	after := fx.attemptRow(t, attemptID)
	if after != before {
		t.Fatalf("live attempt must stay untouched: %+v -> %+v", before, after)
	}

	// tasks.Execute path: translates to ErrWorkerCrashed (does not persist terminal task state).
	raw, merr := json.Marshal(TaskInput{AttemptID: attemptID, OrderID: orderID, StageID: stageID})
	if merr != nil {
		t.Fatal(merr)
	}
	err = fx.runner.Execute(context.Background(), fx.db, raw, nil, nil)
	if err == nil || !errors.Is(err, tasks.ErrWorkerCrashed) {
		t.Fatalf("Execute must translate claim contest into ErrWorkerCrashed, got %v", err)
	}
	if s2 := fx.attemptRow(t, attemptID); s2 != before {
		t.Fatalf("attempt mutated via Execute path: %+v -> %+v", before, s2)
	}
}

// TestExecuteRespectsTaskFence SEC-0001(c): when the task fence is lost (async_tasks row
// was reclaimed), Execute yields without claiming or sending.
func TestExecuteRespectsTaskFence(t *testing.T) {
	fx := newReclaimFx(t)
	orderID, stageID := fx.seedOrder(t)
	attemptID := fx.seedAttempt(t, orderID, stageID, "running", "not_started",
		"dml", 1, "api-pod", false, []reclaimStmt{{1, "not_started"}})
	before := fx.attemptRow(t, attemptID)

	raw, merr := json.Marshal(TaskInput{AttemptID: attemptID, OrderID: orderID, StageID: stageID})
	if merr != nil {
		t.Fatal(merr)
	}
	err := fx.runner.Execute(context.Background(), fx.db, raw,
		func() error { return tasks.ErrFenceLost }, nil)
	if err == nil || !errors.Is(err, tasks.ErrFenceLost) {
		t.Fatalf("Execute must surface task fence loss, got %v", err)
	}
	if after := fx.attemptRow(t, attemptID); after != before {
		t.Fatalf("attempt must stay untouched on task fence loss: %+v -> %+v", before, after)
	}
}

// TestTaskInputBindingEnforced SEC-0060: task payload order/stage mismatches the Attempt
// row (async_tasks.input was tampered with or mislinked) -> fail closed, reject, and write an audit
// record without claiming or sending.
func TestTaskInputBindingEnforced(t *testing.T) {
	fx := newReclaimFx(t)
	orderID, stageID := fx.seedOrder(t)
	attemptID := fx.seedAttempt(t, orderID, stageID, "running", "not_started",
		"dml", 1, "api-pod", false, []reclaimStmt{{1, "not_started"}})
	before := fx.attemptRow(t, attemptID)

	err := fx.runner.RunAttempt(context.Background(), TaskInput{
		AttemptID: attemptID, OrderID: uuid.NewString(), StageID: stageID,
	})
	// Same as above: check domain errors via errors.As + Code (pointers are never equal).
	var de *domain.Error
	if err == nil || !errors.As(err, &de) || de.Code != domain.CodeIllegalTransition {
		t.Fatalf("binding mismatch must fail closed, got %v", err)
	}
	if after := fx.attemptRow(t, attemptID); after != before {
		t.Fatalf("attempt must stay untouched on binding mismatch: %+v -> %+v", before, after)
	}
	var audits int
	if err := fx.db.Raw(
		`select count(*) from audit_events where event_type = 'execution.task_binding_mismatch'
		  and resource_id = ?`, attemptID).Scan(&audits).Error; err != nil {
		t.Fatal(err)
	}
	if audits != 1 {
		t.Fatalf("binding mismatch audits=%d want 1", audits)
	}
}

// TestReclaimCancellingZeroSendFinalizesCancelled: cancellation request arrives before any
// send and the worker dies (cancelling + not_started + expired lease) -> reclaim converges to
// cancelled without opening a target connection (statements remain not_started).
func TestReclaimCancellingZeroSendFinalizesCancelled(t *testing.T) {
	fx := newReclaimFx(t)
	orderID, stageID := fx.seedOrder(t)
	attemptID := fx.seedAttempt(t, orderID, stageID, "cancelling", "not_started",
		"dml", 2, "dead-worker-1", true, []reclaimStmt{{1, "not_started"}})

	if err := fx.runner.RunAttempt(context.Background(), TaskInput{
		AttemptID: attemptID, OrderID: orderID, StageID: stageID,
	}); err != nil {
		t.Fatalf("cancel finalize: %v", err)
	}
	if s := fx.attemptRow(t, attemptID); s.state != string(domain.AttemptCancelled) {
		t.Fatalf("attempt state=%s want cancelled", s.state)
	}
	if got := fx.orderState(t, orderID); got != "cancelled" {
		t.Fatalf("order state=%s want cancelled", got)
	}
	if got := fx.statementStates(t, attemptID); len(got) != 1 || got[0] != "not_started" {
		t.Fatalf("statements=%v want [not_started] (zero send)", got)
	}
}

// TestFreshAttemptClaimFromBeginHandshake: fence=1 (begin handoff, never claimed by a
// runner) + valid lease owned by the API instance -> claim is allowed; with no datasource row,
// terminates as failed/not_started (provably zero sends, preserving retry eligibility on the same order).
func TestFreshAttemptClaimFromBeginHandshake(t *testing.T) {
	fx := newReclaimFx(t)
	orderID, stageID := fx.seedOrder(t)
	attemptID := fx.seedAttempt(t, orderID, stageID, "running", "not_started",
		"dml", 1, "api-pod", false, []reclaimStmt{{1, "not_started"}})

	if err := fx.runner.RunAttempt(context.Background(), TaskInput{
		AttemptID: attemptID, OrderID: orderID, StageID: stageID,
	}); err != nil {
		t.Fatalf("fresh claim must be allowed: %v", err)
	}
	s := fx.attemptRow(t, attemptID)
	if s.state != string(domain.AttemptFailed) || s.boundary != string(domain.BoundaryNotStarted) {
		t.Fatalf("attempt=%+v want failed/not_started (datasource absent)", s)
	}
	if got := fx.orderState(t, orderID); got != "running" {
		t.Fatalf("zero-send failure must keep order retryable, got %s", got)
	}
}

// leaseExpiry reads the Attempt lease expiration timestamp (assertion surface for heartbeat renewal).
func (fx *reclaimFx) leaseExpiry(t *testing.T, attemptID string) time.Time {
	t.Helper()
	var ts time.Time
	if err := fx.db.Raw(
		`select lease_expires_at from execution_attempts where id = ?`, attemptID).
		Scan(&ts).Error; err != nil {
		t.Fatal(err)
	}
	return ts
}

// TestRenewLeaseSkipsCancelling Review loop closure revision (RCP-20261021-WITHDRAW-
// CANCELS-EXECUTION fallback): heartbeat renewal must not extend the lease of a cancelling row —
// when cancellation is ineffective (missing KILL privilege / unresponsive gh-ost / hung execution
// goroutine) while the heartbeat goroutine remains alive, the Attempt lease expires naturally
// according to the bounded window written by the cancellation initiator and is adjudicated by
// SweepCancelTimeouts, never staying stuck in cancelling indefinitely via lease renewal;
// renewing running rows and dual fence/owner checks against false renewal remain as regression anchors.
func TestRenewLeaseSkipsCancelling(t *testing.T) {
	fx := newReclaimFx(t)
	ctx := context.Background()
	owner := fx.svc.instanceOwner()

	// running row: fence + owner match -> renews lease to LeaseDuration (60s) window.
	runOrder, runStage := fx.seedOrder(t)
	runID := fx.seedAttempt(t, runOrder, runStage, "running", "not_started",
		"dml", 2, owner, false, nil)
	fx.svc.renewLease(ctx, runID, 2)
	if out := time.Until(fx.leaseExpiry(t, runID)); out < 50*time.Second {
		t.Fatalf("running lease not renewed: %.0fs out, want ~60s", out.Seconds())
	}

	// cancelling row: same fence and owner -> 0 rows affected, lease retains its original window without renewal.
	cancOrder, cancStage := fx.seedOrder(t)
	cancID := fx.seedAttempt(t, cancOrder, cancStage, "cancelling", "not_started",
		"dml", 2, owner, false, nil)
	before := fx.leaseExpiry(t, cancID)
	fx.svc.renewLease(ctx, cancID, 2)
	if after := fx.leaseExpiry(t, cancID); !after.Equal(before) {
		t.Fatalf("cancelling lease must not be renewed: %v -> %v", before, after)
	}

	// Fence mismatch -> 0 rows affected (dual condition prevents cross-worker false renewal, regression anchor).
	fx.svc.renewLease(ctx, runID, 99)
	if out := time.Until(fx.leaseExpiry(t, runID)); out < 50*time.Second {
		t.Fatalf("fence mismatch must not rewrite lease, got %.0fs out", out.Seconds())
	}
}

//go:build integration

// b10_http_integration_test.go B10 HTTP surface: execution eligibility (W006: frozen executor only,
// admin not exempted -> 403), read-surface actor relationships, statement pagination, manual verification
// endpoint with required evidence, and zero auto-execution upon approval completion.
package httpapi

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/cookieY/YearningX/backend/internal/crypto"
	execapp "github.com/cookieY/YearningX/backend/internal/execution/application"
)

// b10HTTPBegin triggers execution via HTTP; returns (code, body, raw).
func (fx *b10Fixture) b10HTTPBegin(t *testing.T, cl testClient, orderID string, version int64) (int, map[string]any, string) {
	t.Helper()
	return cl.doWithHeaders("POST", "/api/v4/change-orders/"+orderID+"/execution-attempts", nil, map[string]string{
		"If-Match":        fmt.Sprintf(`"%d"`, version),
		"Idempotency-Key": "b10-http-" + orderID[:8] + "-" + fmt.Sprint(version),
		"X-CSRF-Token":    cl.cookies["yearning_csrf"],
	})
}

// TestB10HTTPExecutorPermissions W006: only frozen executor can click execute;
// non-executors (submitter / reviewer / unrelated user / builtin admin) all receive 403.
func TestB10HTTPExecutorPermissions(t *testing.T) {
	fx := newB10Fixture(t)
	dsID := fx.b10PGDatasource(t, "b10-perm", 54330)
	flowID := fx.b10ExecFlow(t, "b10-perm-flow", "exec_target", dsID)
	orderID, version := fx.b10OrderToExecutionPending(t, flowID,
		"update accounts set amount = amount + 1 where id = 1;")
	for _, tc := range []struct {
		name string
		cl   testClient
	}{
		{"submitter-not-executor", fx.owner},
		{"reviewer-not-executor", fx.reviewer1},
		{"unrelated-user", fx.intruder},
		{"builtin-admin", fx.admin},
	} {
		code, _, raw := fx.b10HTTPBegin(t, tc.cl, orderID, version)
		if code != 403 {
			t.Fatalf("%s: expected 403, got %d %s", tc.name, code, raw)
		}
	}
	// Frozen executor click succeeds.
	code, body, raw := fx.b10HTTPBegin(t, fx.executor, orderID, version)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("executor begin: %d %s", code, raw)
	}
	attemptID := body["data"].(map[string]any)["id"].(string)
	var count int
	_ = fx.db.Raw(`select count(*) from execution_attempts where order_id = ?`, orderID).Scan(&count).Error
	if count != 1 {
		t.Fatalf("attempts=%d after permission matrix", count)
	}
	_ = fx.b10Drive(t, attemptID)
	if got := fx.b10OrderState(t, orderID); got != "completed" {
		t.Fatalf("order=%s want completed", got)
	}
}

// TestB10HTTPApprovalNeverExecutes W006: final approval produces no execution task
// or Attempt (waits for explicit click by executor).
func TestB10HTTPApprovalNeverExecutes(t *testing.T) {
	fx := newB10Fixture(t)
	dsID := fx.b10PGDatasource(t, "b10-noauto", 54330)
	flowID := fx.b10ExecFlow(t, "b10-noauto-flow", "exec_target", dsID)
	orderID, _ := fx.b10OrderToExecutionPending(t, flowID,
		"update accounts set amount = amount + 1 where id = 1;")
	var attempts, tasks int
	_ = fx.db.Raw(`select count(*) from execution_attempts where order_id = ?`, orderID).Scan(&attempts).Error
	_ = fx.db.Raw(`select count(*) from async_tasks where kind = 'stage_execution'`).Scan(&tasks).Error
	if attempts != 0 || tasks != 0 {
		t.Fatalf("approval must never auto-execute: attempts=%d tasks=%d", attempts, tasks)
	}
	if got := fx.b10OrderState(t, orderID); got != "stage_execution_pending" {
		t.Fatalf("order=%s", got)
	}
}

// TestB10HTTPAttemptReadAndStatements read surface: visible to related actors (submitter),
// 403 for unrelated users; statement ledger pagination and Page envelope.
func TestB10HTTPAttemptReadAndStatements(t *testing.T) {
	fx := newB10Fixture(t)
	dsID := fx.b10PGDatasource(t, "b10-read", 54330)
	flowID := fx.b10ExecFlow(t, "b10-read-flow", "exec_target", dsID)
	orderID, version := fx.b10OrderToExecutionPending(t, flowID,
		"update accounts set amount = amount + 1 where id = 1;\n"+
			"update accounts set amount = amount + 2 where id = 2;")
	attemptID := fx.b10Begin(t, orderID, version)
	if err := fx.b10Drive(t, attemptID); err != nil {
		t.Fatalf("drive: %v", err)
	}
	// Submitter (order stakeholder) can read Attempt.
	code, body, raw := fx.owner.do("GET", "/api/v4/execution-attempts/"+attemptID, nil, true)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("submitter get attempt: %d %s", code, raw)
	}
	data := body["data"].(map[string]any)
	if data["state"] != "succeeded" || data["send_boundary"] != "sent" {
		t.Fatalf("attempt view: %v", data)
	}
	// Unrelated user gets 403 (does not leak existence).
	code, _, _ = fx.intruder.do("GET", "/api/v4/execution-attempts/"+attemptID, nil, true)
	if code != 403 {
		t.Fatalf("intruder get attempt: %d want 403", code)
	}
	// Statement pagination: limit=1 -> has_more.
	code, body, raw = fx.owner.do("GET", "/api/v4/execution-attempts/"+attemptID+"/statements?limit=1", nil, true)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("statements: %d %s", code, raw)
	}
	page := body["data"].(map[string]any)["page"].(map[string]any)
	items := body["data"].(map[string]any)["items"].([]any)
	if len(items) != 1 || page["has_more"] != true {
		t.Fatalf("page envelope: %v %v", items, page)
	}
	next := page["next_cursor"].(string)
	if next == "" {
		t.Fatal("next_cursor required")
	}
	// Second page fetches final item.
	code, body, raw = fx.owner.do("GET",
		"/api/v4/execution-attempts/"+attemptID+"/statements?limit=1&after="+next, nil, true)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("statements page2: %d %s", code, raw)
	}
	items = body["data"].(map[string]any)["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("page2 items=%d", len(items))
	}
	// Invalid cursor -> 1001.
	code, body, _ = fx.owner.do("GET",
		"/api/v4/execution-attempts/"+attemptID+"/statements?after=abc", nil, true)
	if code != 200 || errCode(body) != 1001 {
		t.Fatalf("bad cursor: %d %v", code, body["err_code"])
	}
}

// TestB10HTTPVerificationEndpoint manual verification endpoint: evidence required (3012),
// non-executor gets 403, still_unknown continues blocking.
func TestB10HTTPVerificationEndpoint(t *testing.T) {
	fx := newB10Fixture(t)
	dsID := fx.b10PGDatasource(t, "b10-verify", 54330)
	flowID := fx.b10ExecFlow(t, "b10-verify-flow", "exec_target", dsID)
	orderID, version := fx.b10OrderToExecutionPending(t, flowID,
		"update accounts set amount = amount + 1 where id = 1;")
	attempt := fx.b10Begin(t, orderID, version)
	// Directly construct result_unknown (connection-level failure path already covered in F04).
	if err := fx.db.Exec(`update execution_attempts set state='result_unknown',
		finished_at=now(), business_err_code=3005, failure_name='TEST_UNKNOWN',
		send_boundary='sent' where id = ?`, attempt).Error; err != nil {
		t.Fatal(err)
	}
	if err := fx.db.Exec(`update change_orders set state='result_unknown', version=version+1
		where id = ?`, orderID).Error; err != nil {
		t.Fatal(err)
	}
	av := fx.b10AttemptVersion(t, attempt)
	call := 0
	verCall := func(cl testClient, payload map[string]any, version int64) (int, map[string]any, string) {
		call++
		return cl.doWithHeaders("POST", "/api/v4/execution-attempts/"+attempt+"/verifications",
			payload, map[string]string{
				"If-Match":        fmt.Sprintf(`"%d"`, version),
				"Idempotency-Key": fmt.Sprintf("b10-ver-%d-%d", version, call),
				"X-CSRF-Token":    cl.cookies["yearning_csrf"],
			})
	}
	// Missing evidence -> 3012.
	code, body, raw := verCall(fx.executor, map[string]any{
		"result": "still_unknown", "reason": "not confirmed yet", "evidence": []any{},
	}, av)
	if code != 200 || errCode(body) != 3012 {
		t.Fatalf("missing evidence: %d %s", code, raw)
	}
	// Non-executor -> 403.
	code, _, raw = verCall(fx.admin, map[string]any{
		"result": "confirmed_failed", "reason": "admin has no verify permission",
		"evidence": []any{map[string]any{"kind": "text", "content": "x"}},
	}, av)
	if code != 403 {
		t.Fatalf("admin verify: %d %s", code, raw)
	}
	// Frozen executor still_unknown -> order remains in result_unknown and stays blocked.
	code, body, raw = verCall(fx.executor, map[string]any{
		"result": "still_unknown", "reason": "still checking database",
		"evidence": []any{map[string]any{"kind": "database_fact", "content": "row value uncertain"}},
	}, av)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("still_unknown verify: %d %s", code, raw)
	}
	if got := fx.b10OrderState(t, orderID); got != "result_unknown" {
		t.Fatalf("order=%s want still result_unknown", got)
	}
	// Second verification: confirmed_failed finalizes order (new Attempt version).
	code, body, raw = verCall(fx.executor, map[string]any{
		"result": "confirmed_failed", "reason": "confirmed statement did not take effect",
		"evidence": []any{map[string]any{"kind": "database_fact", "content": "amount=100"}},
	}, fx.b10AttemptVersion(t, attempt))
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("confirmed_failed verify: %d %s", code, raw)
	}
	if got := fx.b10OrderState(t, orderID); got != "failed" {
		t.Fatalf("order=%s want failed", got)
	}
	// Verification facts are append-only: 2 records.
	var n int
	_ = fx.db.Raw(`select count(*) from execution_verifications where attempt_id = ?`, attempt).Scan(&n).Error
	if n != 2 {
		t.Fatalf("verifications=%d want 2", n)
	}
	// Timeline includes unknown_resolved events (emitted twice via emit_all).
	var events int
	_ = fx.db.Raw(`select count(*) from domain_events where subject = ? and event_type = 'execution.unknown_resolved'`,
		"change-orders/"+orderID).Scan(&events).Error
	if events < 2 {
		t.Fatalf("unknown_resolved events=%d want >=2 (emit_all)", events)
	}
}

// TestB10HTTPNotFrozenExecutorOnLiveAttempt dual-executor eligibility recognizes only frozen snapshot:
// cancel requests from other users on a live Attempt return 403.
func TestB10HTTPNotFrozenExecutorOnLiveAttempt(t *testing.T) {
	fx := newB10Fixture(t)
	dsID := fx.b10PGDatasource(t, "b10-cancel403", 54330)
	flowID := fx.b10ExecFlow(t, "b10-cancel403-flow", "exec_target", dsID)
	orderID, version := fx.b10OrderToExecutionPending(t, flowID,
		"update accounts set amount = amount + 1 where id = 1;")
	attempt := fx.b10Begin(t, orderID, version)
	av := fx.b10AttemptVersion(t, attempt)
	for _, cl := range []testClient{fx.owner, fx.admin} {
		code, _, raw := cl.doWithHeaders("POST",
			"/api/v4/execution-attempts/"+attempt+"/cancellation", map[string]any{"reason": "x"},
			map[string]string{
				"If-Match":        fmt.Sprintf(`"%d"`, av),
				"Idempotency-Key": "b10-c403-" + fmt.Sprint(av),
				"X-CSRF-Token":    cl.cookies["yearning_csrf"],
			})
		if code != 403 {
			t.Fatalf("cancel by non-executor: %d %s", code, raw)
		}
	}
	_ = strings.TrimSpace("")
}

// TestB10OscTypedOverrides HTTP surface: unknown key rejected with 1001; valid override enters
// sealed plan argv (--chunk-size=900 overrides settings default 250).
func TestB10OscTypedOverrides(t *testing.T) {
	fx := newB10Fixture(t)
	fx.b10GhOstEnv(t, "MODE_DUMP")
	dsID := fx.b10MySQLDatasource(t, "b10-ovr")
	flowID := fx.b10ExecFlow(t, "b10-ovr-flow", "exec_target", dsID)
	orderID, version := fx.b10OrderToExecutionPending(t, flowID,
		"ALTER TABLE `exec_target`.`accounts` ADD COLUMN ovr_col int /*MODE_DUMP*/")
	// Unknown key -> 1001 (not silently dropped).
	_, _, err := fx.exec.Begin(context.Background(), execapp.BeginInput{
		OrderID: orderID, Actor: execapp.Actor{UserID: fx.executorID, Username: "exec1"},
		ExpectVersion: version, IdempotencyKey: "b10-ovr-bad",
		OscOverrides: map[string]any{"drop_ghost_table": true},
	})
	if err == nil || !strings.Contains(err.Error(), "1001") {
		t.Fatalf("unknown override key must 1001, got %v", err)
	}
	// Injection value -> 1001.
	_, _, err = fx.exec.Begin(context.Background(), execapp.BeginInput{
		OrderID: orderID, Actor: execapp.Actor{UserID: fx.executorID, Username: "exec1"},
		ExpectVersion: version, IdempotencyKey: "b10-ovr-inj",
		OscOverrides: map[string]any{"max_load": "Threads_running=1; rm -rf /"},
	})
	if err == nil || !strings.Contains(err.Error(), "1001") {
		t.Fatalf("injection override must 1001, got %v", err)
	}
	// Valid override replaces settings default (250 -> 900) and enters process argv.
	view, _, err := fx.exec.Begin(context.Background(), execapp.BeginInput{
		OrderID: orderID, Actor: execapp.Actor{UserID: fx.executorID, Username: "exec1"},
		ExpectVersion: version, IdempotencyKey: "b10-ovr-ok",
		OscOverrides: map[string]any{"chunk_size": float64(900)},
	})
	if err != nil {
		t.Fatalf("begin with overrides: %v", err)
	}
	// Sealed plan argv contains override value (server-side assembly evidence).
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
		t.Fatalf("decrypt plan: %v", derr)
	}
	if !strings.Contains(string(plain), "--chunk-size=900") {
		t.Fatalf("override must reach sealed plan argv: %s", string(plain))
	}
	if strings.Contains(string(plain), "--chunk-size=250") {
		t.Fatal("override must win over settings default")
	}
	// Password never enters plan.
	if strings.Contains(string(plain), "b10-root-pw") {
		t.Fatal("credential leaked into sealed plan")
	}
	// Fake binary executes successfully (argv actually reaches process).
	if err := fx.b10Drive(t, view.ID); err != nil {
		t.Fatalf("drive: %v", err)
	}
	if s, _, _, _ := fx.b10AttemptRow(t, view.ID); s != "succeeded" {
		t.Fatalf("attempt=%s want succeeded", s)
	}
}

// TestB10OscPolicyNoneDirectDDL policy none: native MySQL single ALTER runs as direct DDL
// (bypasses OSC); unavailable policy tool -> fail closed without fallback.
func TestB10OscPolicyBoundaries(t *testing.T) {
	fx := newB10Fixture(t)
	fx.b10PtOscEnv(t, "MODE_PTCOPY_OK")
	dsID := fx.b10MySQLDatasource(t, "b10-pol")
	// Policy pt-osc: fake binary success path (progress line parsing + succeeded terminal state).
	flowID := fx.b10ExecFlow(t, "b10-pol-flow", "exec_target", dsID)
	orderID, version := fx.b10OrderToExecutionPending(t, flowID,
		"ALTER TABLE `exec_target`.`items` ADD COLUMN pol_col int /*MODE_PTCOPY_OK*/")
	attempt := fx.b10Begin(t, orderID, version)
	var kind string
	_ = fx.db.Raw(`select execution_kind from execution_attempts where id = ?`, attempt).Scan(&kind).Error
	if kind != "pt_osc" {
		t.Fatalf("kind=%s want pt_osc", kind)
	}
	if err := fx.b10Drive(t, attempt); err != nil {
		t.Fatalf("drive pt-osc: %v", err)
	}
	s, _, _, _ := fx.b10AttemptRow(t, attempt)
	if s != "succeeded" {
		t.Fatalf("pt-osc attempt=%s want succeeded", s)
	}
	var bps int
	_ = fx.db.Raw(`select osc_progress_basis_points from execution_attempts where id = ?`, attempt).Scan(&bps).Error
	if bps != 10000 {
		t.Fatalf("pt-osc progress=%d want 10000", bps)
	}
	if got := fx.b10OrderState(t, orderID); got != "completed" {
		t.Fatalf("order=%s want completed", got)
	}

	// Policy none: ALTER executes as direct DDL (column name carries random suffix for cross-run idempotency).
	fx.b10OscPolicy(t, "none")
	noneCol := "none_col_" + uuid.NewString()[:6]
	flowID2 := fx.b10ExecFlow(t, "b10-none-flow", "exec_target", dsID)
	orderID2, version2 := fx.b10OrderToExecutionPending(t, flowID2,
		"ALTER TABLE `exec_target`.`items` ADD COLUMN "+noneCol+" int")
	attempt2 := fx.b10Begin(t, orderID2, version2)
	var kind2 string
	_ = fx.db.Raw(`select execution_kind from execution_attempts where id = ?`, attempt2).Scan(&kind2).Error
	if kind2 != "ddl" {
		t.Fatalf("policy none kind=%s want ddl", kind2)
	}
	if err := fx.b10Drive(t, attempt2); err != nil {
		t.Fatalf("drive direct ddl: %v", err)
	}
	if s2, _, _, _ := fx.b10AttemptRow(t, attempt2); s2 != "succeeded" {
		t.Fatalf("direct ddl attempt=%s", s2)
	}
	// Column added on target side (real DDL took effect).
	cols := fx.b10TargetQueryMySQL(t,
		`select count(*) from information_schema.columns where table_schema='exec_target' and table_name='items' and column_name='`+noneCol+`'`)
	if len(cols) != 1 || cols[0][0] != "1" {
		t.Fatalf("direct DDL must alter target: %v", cols)
	}

	// Policy gh-ost with missing binary coordinates -> fail closed 3011 (no fallback to direct DDL).
	fx.b10OscPolicy(t, "gh-ost")
	savedBinaries := fx.exec.OscBinaries
	fx.exec.OscBinaries = map[string]*execapp.OscBinaryIdentity{}
	flowID3 := fx.b10ExecFlow(t, "b10-unavail-flow", "exec_target", dsID)
	orderID3, version3 := fx.b10OrderToExecutionPending(t, flowID3,
		"ALTER TABLE `exec_target`.`items` ADD COLUMN un_col_"+uuid.NewString()[:6]+" int")
	_, _, err := fx.exec.Begin(context.Background(), execapp.BeginInput{
		OrderID: orderID3, Actor: execapp.Actor{UserID: fx.executorID, Username: "exec1"},
		ExpectVersion: version3, IdempotencyKey: "b10-unavail",
	})
	if err == nil || !strings.Contains(err.Error(), "3011") {
		t.Fatalf("unavailable osc tool must 3011 fail closed, got %v", err)
	}
	var attempts int
	_ = fx.db.Raw(`select count(*) from execution_attempts where order_id = ?`, orderID3).Scan(&attempts).Error
	if attempts != 0 {
		t.Fatalf("fail-closed begin must not create attempt, got %d", attempts)
	}
	fx.exec.OscBinaries = savedBinaries
}

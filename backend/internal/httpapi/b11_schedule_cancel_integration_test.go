//go:build integration

// b11_schedule_cancel_integration_test.go schedule cancellation (owner decision 2026-10-30):
// any frozen executor can cancel (not limited to the scheduler), returning order to stage_execution_pending
// (eligible for rescheduling or immediate execution), recording timeline/audit trail, mutually exclusive with
// due claim via CAS, idempotent replay, and crash-safety proof that schedules and cancellations survive
// process crash/restart -- all schedule/cancel state is persisted in DB and each consumer instance reads purely from tables.
package httpapi

import (
	"fmt"
	"strings"
	"testing"
	"time"

	execapp "github.com/cookieY/YearningX/backend/internal/execution/application"
)

// b11DualExecutorPendingOrder creates an execution pipeline up to execution_pending with two frozen
// stage executors (exec1 = scheduler, exec2 = second frozen executor).
func (fx *b11Fixture) b11DualExecutorPendingOrder(t *testing.T) (string, int64, testClient) {
	t.Helper()
	exec2 := testClient{t: t, engine: fx.engine, cookies: map[string]string{}}
	exec2ID := fx.adminCreateUserB6(t, "exec2", "exec2-pass-123")
	if _, body, _ := exec2.login("exec2", "exec2-pass-123"); errCode(body) != 0 {
		t.Fatal("exec2 login failed")
	}
	execTarget(t, mustOpenTargetPG(t, fx.b10Fixture), `update accounts set amount = 100 where id = 1`)
	dsID := fx.b10PGDatasource(t, "b11-cancel", 54330)
	stage := map[string]any{
		"position": 1, "datasource_id": dsID,
		"approval_steps": []map[string]any{{
			"position": 1,
			"actors":   []map[string]any{{"user_id": fx.reviewer1ID}, {"user_id": fx.reviewer2ID}},
		}},
		"execution_actors": []map[string]any{{"user_id": fx.executorID}, {"user_id": exec2ID}},
	}
	flowID := fx.b9CreateFlow(t, "b11-cancel-flow", []map[string]any{stage})
	fx.b9CreateGrant(t, fx.ownerID, flowID)
	orderID, version := fx.b10OrderToExecutionPending(t, flowID,
		"update accounts set amount = amount + 1 where id = 1;")
	return orderID, version, exec2
}

// b11CancelScheduleHTTP cancels execution schedule over HTTP.
func (fx *b11Fixture) b11CancelScheduleHTTP(t *testing.T, cl testClient, orderID string, version int64, key string) (int, map[string]any, string) {
	t.Helper()
	return cl.doWithHeaders("POST", "/api/v4/change-orders/"+orderID+"/execution-schedules/cancellation",
		nil, map[string]string{
			"Idempotency-Key": key,
			"If-Match":        fmt.Sprintf(`"%d"`, version),
			"X-CSRF-Token":    cl.cookies["yearning_csrf"],
		})
}

// b11OrderVersion reads current change order version.
func (fx *b11Fixture) b11OrderVersion(t *testing.T, orderID string) int64 {
	t.Helper()
	var v int64
	if err := fx.db.Raw(`select version from change_orders where id = ?`, orderID).Scan(&v).Error; err != nil {
		t.Fatal(err)
	}
	return v
}

// b11ScheduleRowState reads state of the schedule row for the order.
func (fx *b11Fixture) b11ScheduleRowState(t *testing.T, orderID string) string {
	t.Helper()
	var v string
	if err := fx.db.Raw(`select state from execution_schedules where order_id = ?`, orderID).Scan(&v).Error; err != nil {
		t.Fatal(err)
	}
	return v
}

// TestB11CancelScheduleRoundTrip schedule cancellation main flow: second frozen executor (non-scheduler)
// cancels -> order returns to execution_pending + schedule row cancelled + timeline/event/audit recorded +
// active_schedule absent on read surface -> reschedule succeeds -> cancel again -> immediate execution succeeds.
func TestB11CancelScheduleRoundTrip(t *testing.T) {
	fx := newB11Fixture(t)
	execapp.SetScheduleClaimGraceForTest(5 * time.Minute)
	defer execapp.SetScheduleClaimGraceForTest(execapp.ScheduleClaimGrace)
	orderID, version, exec2 := fx.b11DualExecutorPendingOrder(t)

	// exec1 schedules execution.
	at := time.Now().Add(10 * time.Minute).UTC().Truncate(time.Second)
	if code, body, raw := fx.b11CreateScheduleHTTP(t, orderID, version, at); code != 200 || errCode(body) != 0 {
		t.Fatalf("schedule: %d %s", code, raw)
	}
	if got := fx.b10OrderState(t, orderID); got != "scheduled" {
		t.Fatalf("order state = %s want scheduled", got)
	}
	// Read surface: scheduled state carries active_schedule (scheduled time echoed).
	code, body, raw := fx.owner.do("GET", "/api/v4/change-orders/"+orderID, nil, true)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("get order: %d %s", code, raw)
	}
	active, _ := body["data"].(map[string]any)["active_schedule"].(map[string]any)
	if active == nil || active["state"] != "scheduled" {
		t.Fatalf("active_schedule missing while scheduled: %s", raw)
	}

	// exec2 (second frozen executor, not the scheduler) cancels -- owner decision: any frozen executor.
	fresh := fx.b11OrderVersion(t, orderID)
	cancelKey := "b11-cancel-" + orderID[:8]
	if code, body, raw = fx.b11CancelScheduleHTTP(t, exec2, orderID, fresh, cancelKey); code != 200 || errCode(body) != 0 {
		t.Fatalf("cancel by second frozen executor: %d %s", code, raw)
	}
	sv := body["data"].(map[string]any)
	if sv["state"] != "cancelled" {
		t.Fatalf("cancel view state = %v", sv["state"])
	}
	if got := fx.b10OrderState(t, orderID); got != "stage_execution_pending" {
		t.Fatalf("order state after cancel = %s want stage_execution_pending", got)
	}
	if got := fx.b11ScheduleRowState(t, orderID); got != "cancelled" {
		t.Fatalf("schedule row = %s want cancelled", got)
	}
	// Event + audit + timeline trail (submitter visibility = timeline).
	var events int
	fx.db.Raw(`select count(*) from domain_events
		where event_type = 'execution.schedule_cancelled'
		  and event_data->>'reason_code' = 'executor_cancelled_schedule'`).Scan(&events)
	if events != 1 {
		t.Fatalf("execution.schedule_cancelled events = %d want 1", events)
	}
	var audits int
	fx.db.Raw(`select count(*) from audit_events where event_type = 'execution.schedule_cancelled'`).Scan(&audits)
	if audits != 1 {
		t.Fatalf("schedule_cancelled audits = %d want 1", audits)
	}
	code, body, raw = fx.owner.do("GET", "/api/v4/change-orders/"+orderID+"/timeline?limit=50", nil, true)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("timeline: %d %s", code, raw)
	}
	found := false
	for _, item := range body["data"].(map[string]any)["items"].([]any) {
		entry := item.(map[string]any)
		if entry["event_type"] == "execution.schedule_cancelled" {
			if summary, _ := entry["summary"].(string); strings.TrimSpace(summary) == "" || summary == "execution.schedule_cancelled" {
				t.Fatalf("cancel timeline summary = %q", summary)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("timeline missing execution.schedule_cancelled entry")
	}
	// active_schedule is absent after cancellation.
	code, body, _ = fx.owner.do("GET", "/api/v4/change-orders/"+orderID, nil, true)
	if body["data"].(map[string]any)["active_schedule"] != nil {
		t.Fatalf("active_schedule should be absent after cancel")
	}

	// Replay with same idempotency key: returns identical cancellation result (no duplicate state transition).
	fresh = fx.b11OrderVersion(t, orderID)
	if code, body, raw = fx.b11CancelScheduleHTTP(t, exec2, orderID, fresh, cancelKey); code != 200 || errCode(body) != 0 {
		t.Fatalf("idempotent replay: %d %s", code, raw)
	}
	if sv := body["data"].(map[string]any); sv["state"] != "cancelled" {
		t.Fatalf("replay state = %v", sv["state"])
	}

	// Reschedule (re-booking succeeds naturally).
	fresh = fx.b11OrderVersion(t, orderID)
	if code, body, raw = fx.b11CreateScheduleHTTP(t, orderID, fresh, at.Add(time.Hour)); code != 200 || errCode(body) != 0 {
		t.Fatalf("re-schedule after cancel: %d %s", code, raw)
	}

	// Cancel again and execute immediately (full semantics of returning to execution_pending).
	fresh = fx.b11OrderVersion(t, orderID)
	if code, body, raw = fx.b11CancelScheduleHTTP(t, fx.executor, orderID, fresh,
		"b11-cancel2-"+orderID[:8]); code != 200 || errCode(body) != 0 {
		t.Fatalf("second cancel: %d %s", code, raw)
	}
	fresh = fx.b11OrderVersion(t, orderID)
	if code, body, raw = fx.executor.doWithHeaders("POST",
		"/api/v4/change-orders/"+orderID+"/execution-attempts", nil, map[string]string{
			"If-Match":        fmt.Sprintf(`"%d"`, fresh),
			"Idempotency-Key": "b11-cancel-exec-" + orderID[:8],
			"X-CSRF-Token":    fx.executor.cookies["yearning_csrf"],
		}); code != 200 || errCode(body) != 0 {
		t.Fatalf("execute immediately after cancel: %d %s", code, raw)
	}
	attemptID := body["data"].(map[string]any)["id"].(string)
	if got := fx.b10OrderState(t, orderID); got != "running" {
		t.Fatalf("order state after immediate execute = %s want running", got)
	}
	if err := fx.b10Drive(t, attemptID); err != nil {
		t.Fatalf("drive: %v", err)
	}
	if got := fx.b10OrderState(t, orderID); got != "completed" {
		t.Fatalf("final state = %s want completed", got)
	}
}

// TestB11CancelScheduleGuards cancellation guards: non-frozen executor 403; stale version 1004;
// non-scheduled state 1010; non-existent order 1002; duplicate cancel (new idempotency key) 1010.
func TestB11CancelScheduleGuards(t *testing.T) {
	fx := newB11Fixture(t)
	orderID, version := fx.b11PendingOrder(t)

	// Unscheduled (stage_execution_pending) -> 1010.
	if code, body, _ := fx.b11CancelScheduleHTTP(t, fx.executor, orderID, version,
		"b11-g0-"+orderID[:8]); code != 200 || errCode(body) != 1010 {
		t.Fatalf("cancel while pending: %d err=%d", code, errCode(body))
	}

	at := time.Now().Add(10 * time.Minute).UTC().Truncate(time.Second)
	if code, body, raw := fx.b11CreateScheduleHTTP(t, orderID, version, at); code != 200 || errCode(body) != 0 {
		t.Fatalf("schedule: %d %s", code, raw)
	}
	fresh := fx.b11OrderVersion(t, orderID)

	// Submitter (not a frozen executor) -> 403.
	if code, _, _ := fx.b11CancelScheduleHTTP(t, fx.owner, orderID, fresh,
		"b11-g1-"+orderID[:8]); code != 403 {
		t.Fatalf("submitter cancel: %d want 403", code)
	}
	// Stale If-Match -> 1004.
	if code, body, _ := fx.b11CancelScheduleHTTP(t, fx.executor, orderID, version,
		"b11-g2-"+orderID[:8]); code != 200 || errCode(body) != 1004 {
		t.Fatalf("stale version cancel: %d err=%d", code, errCode(body))
	}
	// Non-existent order -> 1002 (non-enumeration behavior aligned with creation endpoint).
	if code, body, _ := fx.b11CancelScheduleHTTP(t, fx.executor,
		"00000000-0000-0000-0000-000000000000", 1, "b11-g3"); code != 200 || errCode(body) != 1002 {
		t.Fatalf("unknown order cancel: %d err=%d", code, errCode(body))
	}

	// After normal cancellation, cancelling again with a new idempotency key -> 1010 (order no longer scheduled).
	if code, body, raw := fx.b11CancelScheduleHTTP(t, fx.executor, orderID, fresh,
		"b11-g4-"+orderID[:8]); code != 200 || errCode(body) != 0 {
		t.Fatalf("cancel: %d %s", code, raw)
	}
	after := fx.b11OrderVersion(t, orderID)
	if code, body, _ := fx.b11CancelScheduleHTTP(t, fx.executor, orderID, after,
		"b11-g5-"+orderID[:8]); code != 200 || errCode(body) != 1010 {
		t.Fatalf("re-cancel: %d err=%d", code, errCode(body))
	}
}

// TestB11CancelScheduleVsDueClaimRace mutual exclusion between cancellation and due claim (schedule row CAS,
// first committer wins): cancel commits first -> tick never claims; claim commits first -> cancel returns 1010.
// Both orders share the same datasource/flow (only one datasource can be registered per target identity).
func TestB11CancelScheduleVsDueClaimRace(t *testing.T) {
	fx := newB11Fixture(t)
	execapp.SetScheduleClaimGraceForTest(5 * time.Minute)
	defer execapp.SetScheduleClaimGraceForTest(execapp.ScheduleClaimGrace)
	execTarget(t, mustOpenTargetPG(t, fx.b10Fixture), `update accounts set amount = 100 where id = 1`)
	dsID := fx.b10PGDatasource(t, "b11-race", 54330)
	flowID := fx.b10ExecFlow(t, "b11-race-flow", "exec_target", dsID)
	sqlText := "update accounts set amount = amount + 1 where id = 1;"

	// Direction 1: cancel before due tick runs.
	orderID, version := fx.b10OrderToExecutionPending(t, flowID, sqlText)
	if code, body, raw := fx.b11CreateScheduleHTTP(t, orderID, version,
		time.Now().Add(10*time.Minute)); code != 200 || errCode(body) != 0 {
		t.Fatalf("schedule A: %d %s", code, raw)
	}
	fx.db.Exec(`update execution_schedules set scheduled_for = now() - interval '2 seconds'
		where order_id = ?`, orderID)
	fresh := fx.b11OrderVersion(t, orderID)
	if code, body, raw := fx.b11CancelScheduleHTTP(t, fx.executor, orderID, fresh,
		"b11-r1-"+orderID[:8]); code != 200 || errCode(body) != 0 {
		t.Fatalf("cancel before tick: %d %s", code, raw)
	}
	res, err := fx.exec.ProcessDueSchedules(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if res.Claimed != 0 {
		t.Fatalf("cancelled schedule claimed: %+v", res)
	}
	if got := fx.b10OrderState(t, orderID); got != "stage_execution_pending" {
		t.Fatalf("order state after cancel+tick = %s", got)
	}
	var attempts int
	fx.db.Raw(`select count(*) from execution_attempts where order_id = ?`, orderID).Scan(&attempts)
	if attempts != 0 {
		t.Fatalf("cancelled schedule executed: %d attempts", attempts)
	}

	// Direction 2: claim commits first -> cancel is rejected (1010; version has also advanced).
	orderID2, version2 := fx.b10OrderToExecutionPending(t, flowID, sqlText)
	if code, body, raw := fx.b11CreateScheduleHTTP(t, orderID2, version2,
		time.Now().Add(10*time.Minute)); code != 200 || errCode(body) != 0 {
		t.Fatalf("schedule B: %d %s", code, raw)
	}
	fx.db.Exec(`update execution_schedules set scheduled_for = now() - interval '2 seconds'
		where order_id = ?`, orderID2)
	res2, err := fx.exec.ProcessDueSchedules(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if res2.Claimed != 1 {
		t.Fatalf("claim B: %+v", res2)
	}
	if got := fx.b10OrderState(t, orderID2); got != "running" {
		t.Fatalf("order B = %s want running", got)
	}
	fresh2 := fx.b11OrderVersion(t, orderID2)
	if code, body, _ := fx.b11CancelScheduleHTTP(t, fx.executor, orderID2, fresh2,
		"b11-r2-"+orderID2[:8]); code != 200 || errCode(body) != 1010 {
		t.Fatalf("cancel after claim: %d err=%d", code, errCode(body))
	}
}

// TestB11ScheduleSurvivesProcessRestart crash-safety proof (owner requirement: "if server crashes
// suddenly and restarts, the schedule mechanism remains valid as long as scheduled time has not passed"):
// all schedule and cancellation state lives in the DB (single-transaction commit), and neither Scheduler nor
// cancel command holds in-memory state -- this test uses a fresh service instance ("restarted process") to
// consume schedules written by the fixture: new instance cancels old schedule, then claims and drives a new schedule at due time.
func TestB11ScheduleSurvivesProcessRestart(t *testing.T) {
	fx := newB11Fixture(t)
	execapp.SetScheduleClaimGraceForTest(5 * time.Minute)
	defer execapp.SetScheduleClaimGraceForTest(execapp.ScheduleClaimGrace)
	orderID, version := fx.b11PendingOrder(t)
	at := time.Now().Add(10 * time.Minute).UTC().Truncate(time.Second)
	if code, body, raw := fx.b11CreateScheduleHTTP(t, orderID, version, at); code != 200 || errCode(body) != 0 {
		t.Fatalf("schedule: %d %s", code, raw)
	}

	// "Restart": new service instance (sharing zero in-memory state).
	restarted, err := execapp.NewServices(fx.db, fx.svc.Crypter, fx.svc.Recorder)
	if err != nil {
		t.Fatal(err)
	}
	// After restart, before due time: schedule remains valid -- active_schedule still present on read surface.
	code, body, raw := fx.owner.do("GET", "/api/v4/change-orders/"+orderID, nil, true)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("get order: %d %s", code, raw)
	}
	if body["data"].(map[string]any)["active_schedule"] == nil {
		t.Fatalf("schedule lost after restart: %s", raw)
	}
	// New instance cancels prior schedule (driven purely by table state).
	cancelView, _, err := restarted.CancelSchedule(t.Context(), execapp.CancelScheduleInput{
		OrderID: orderID, Actor: execapp.Actor{UserID: fx.executorID, Username: "exec1"},
		ExpectVersion: fx.b11OrderVersion(t, orderID), IdempotencyKey: "b11-restart-cancel",
	})
	if err != nil {
		t.Fatalf("restart cancel: %v", err)
	}
	if cancelView.State != "cancelled" {
		t.Fatalf("restart cancel state = %s", cancelView.State)
	}
	if got := fx.b10OrderState(t, orderID); got != "stage_execution_pending" {
		t.Fatalf("order state after restart cancel = %s", got)
	}
	// Reschedule and reach due time: still consumed by new instance (restart preserves schedule semantics).
	if code, body, raw = fx.b11CreateScheduleHTTP(t, orderID, fx.b11OrderVersion(t, orderID),
		at.Add(30*time.Minute)); code != 200 || errCode(body) != 0 {
		t.Fatalf("re-schedule: %d %s", code, raw)
	}
	fx.db.Exec(`update execution_schedules set scheduled_for = now() - interval '2 seconds'
		where order_id = ?`, orderID)
	res, err := restarted.ProcessDueSchedules(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if res.Claimed != 1 {
		t.Fatalf("restart claim: %+v", res)
	}
	var attemptID string
	fx.db.Raw(`select id::text from execution_attempts where order_id = ?`, orderID).Row().Scan(&attemptID)
	if attemptID == "" {
		t.Fatal("no attempt after restart claim")
	}
	if err := fx.b10Drive(t, attemptID); err != nil {
		t.Fatalf("drive: %v", err)
	}
	if got := fx.b10OrderState(t, orderID); got != "completed" {
		t.Fatalf("final state = %s want completed", got)
	}
}

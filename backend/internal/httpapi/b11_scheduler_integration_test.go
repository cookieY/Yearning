//go:build integration

// b11_scheduler_integration_test.go E007 acceptance: schedule creation eligibility and window,
// execute-once at due time (no second click, traceable authorizer), missed schedule never catches up,
// multi-instance single claim, datasource unavailable blocked, and order withdrawal cancels schedule.
package httpapi

import (
	"context"
	"fmt"
	"testing"
	"time"

	execapp "github.com/cookieY/YearningX/backend/internal/execution/application"
)

// b11ScheduleFixture sets up execution pipeline to execution_pending (real PG target).
func (fx *b11Fixture) b11PendingOrder(t *testing.T) (string, int64) {
	t.Helper()
	execTarget(t, mustOpenTargetPG(t, fx.b10Fixture), `update accounts set amount = 100 where id = 1`)
	dsID := fx.b10PGDatasource(t, "b11-sched", 54330)
	flowID := fx.b10ExecFlow(t, "b11-sched-flow", "exec_target", dsID)
	return fx.b10OrderToExecutionPending(t, flowID,
		"update accounts set amount = amount + 1 where id = 1;")
}

// b11CreateScheduleHTTP creates an execution schedule as executor over HTTP.
func (fx *b11Fixture) b11CreateScheduleHTTP(t *testing.T, orderID string, version int64, at time.Time) (int, map[string]any, string) {
	t.Helper()
	return fx.executor.doWithHeaders("POST", "/api/v4/change-orders/"+orderID+"/execution-schedules",
		map[string]any{"scheduled_for": at.UTC().Format(time.RFC3339Nano)}, map[string]string{
			"Idempotency-Key": "b11-sched-" + orderID[:8] + "-" + fmt.Sprint(at.Unix()),
			"If-Match":        fmt.Sprintf(`"%d"`, version),
			"X-CSRF-Token":    fx.executor.cookies["yearning_csrf"],
		})
}

// TestB11ScheduleCreationGates creation eligibility and window: non-frozen executor 403;
// lead time <5m or >30d -> 3007; success -> order scheduled + single-authorization semantics.
func TestB11ScheduleCreationGates(t *testing.T) {
	fx := newB11Fixture(t)
	orderID, version := fx.b11PendingOrder(t)
	// Non-executor gets 403 (submitter).
	code, body, _ := fx.owner.doWithHeaders("POST", "/api/v4/change-orders/"+orderID+"/execution-schedules",
		map[string]any{"scheduled_for": time.Now().Add(1 * time.Hour).UTC().Format(time.RFC3339Nano)},
		map[string]string{
			"Idempotency-Key": "b11-sched-owner", "If-Match": fmt.Sprintf(`"%d"`, version),
			"X-CSRF-Token": fx.owner.cookies["yearning_csrf"],
		})
	if code != 403 {
		t.Fatalf("submitter schedule: %d want 403", code)
	}
	// Past timestamp.
	if code, body, _ = fx.b11CreateScheduleHTTP(t, orderID, version, time.Now().Add(-time.Minute)); code != 200 || errCode(body) != 3007 {
		t.Fatalf("past schedule: %d err=%d", code, errCode(body))
	}
	// Lead time under 5 minutes.
	if code, body, _ = fx.b11CreateScheduleHTTP(t, orderID, version, time.Now().Add(2*time.Minute)); code != 200 || errCode(body) != 3007 {
		t.Fatalf("near schedule: %d err=%d", code, errCode(body))
	}
	// Beyond 30 days.
	if code, body, _ = fx.b11CreateScheduleHTTP(t, orderID, version, time.Now().Add(31*24*time.Hour)); code != 200 || errCode(body) != 3007 {
		t.Fatalf("far schedule: %d err=%d", code, errCode(body))
	}
	// Valid schedule.
	at := time.Now().Add(10 * time.Minute).UTC().Truncate(time.Second)
	code, body, raw := fx.b11CreateScheduleHTTP(t, orderID, version, at)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("valid schedule: %d %s", code, raw)
	}
	sv := body["data"].(map[string]any)
	if sv["state"] != "scheduled" {
		t.Fatalf("schedule state = %v", sv["state"])
	}
	if got := fx.b10OrderState(t, orderID); got != "scheduled" {
		t.Fatalf("order state = %s want scheduled", got)
	}
	// Duplicate schedule: order has left execution_pending -> 1010 (state machine rejects before
	// unique index; same semantics = no second active schedule allowed).
	if code, body, _ = fx.b11CreateScheduleHTTP(t, orderID, version+1, at.Add(time.Hour)); code != 200 || errCode(body) != 1010 {
		t.Fatalf("duplicate schedule: %d err=%d", code, errCode(body))
	}
	// Event persisted (execution.scheduled in routing table).
	var events int
	fx.db.Raw(`select count(*) from domain_events where event_type = 'execution.scheduled'`).Scan(&events)
	if events == 0 {
		t.Fatal("execution.scheduled event missing")
	}
}

// TestB11ScheduleDueClaimsOnce execute-once at due time: claim -> order running ->
// Attempt created (authorizer = frozen executor who created schedule) -> drive to completion; two instances
// claiming concurrently produce only one Attempt.
func TestB11ScheduleDueClaimsOnce(t *testing.T) {
	fx := newB11Fixture(t)
	execapp.SetScheduleClaimGraceForTest(5 * time.Minute)
	defer execapp.SetScheduleClaimGraceForTest(execapp.ScheduleClaimGrace)
	orderID, version := fx.b11PendingOrder(t)
	at := time.Now().Add(10 * time.Minute)
	if code, body, raw := fx.b11CreateScheduleHTTP(t, orderID, version, at); code != 200 || errCode(body) != 0 {
		t.Fatalf("schedule: %d %s", code, raw)
	}
	// Simulate due time reached: rewind schedule row timestamp to now (equivalent to becoming due).
	fx.db.Exec(`update execution_schedules set scheduled_for = now() - interval '2 seconds'`)
	// Two instances adjudicate concurrently: claimed only once.
	other, err := execapp.NewServices(fx.db, fx.svc.Crypter, fx.svc.Recorder)
	if err != nil {
		t.Fatal(err)
	}
	res1, err := fx.exec.ProcessDueSchedules(context.Background())
	if err != nil {
		t.Fatalf("tick1: %v", err)
	}
	res2, err := other.ProcessDueSchedules(context.Background())
	if err != nil {
		t.Fatalf("tick2: %v", err)
	}
	if res1.Claimed != 1 || res2.Claimed != 0 {
		t.Fatalf("claims: %+v / %+v want 1/0", res1, res2)
	}
	if got := fx.b10OrderState(t, orderID); got != "running" {
		t.Fatalf("order state = %s want running", got)
	}
	// Attempt authorizer = schedule creator (traceable).
	var attemptID, initiatedBy string
	fx.db.Raw(`select id::text, initiated_by_user_id_snapshot from execution_attempts
		where order_id = ? order by created_at desc limit 1`, orderID).Row().Scan(&attemptID, &initiatedBy)
	if attemptID == "" {
		t.Fatal("no attempt created by due claim")
	}
	if initiatedBy != fx.executorID {
		t.Fatalf("attempt actor = %s want executor", initiatedBy)
	}
	// Drive to terminal state (without a second click).
	if err := fx.b10Drive(t, attemptID); err != nil {
		t.Fatalf("drive: %v", err)
	}
	if got := fx.b10OrderState(t, orderID); got != "completed" {
		t.Fatalf("final order state = %s want completed", got)
	}
	var schedState string
	fx.db.Raw(`select state from execution_schedules where order_id = ?`, orderID).Scan(&schedState)
	if schedState != "running" {
		t.Fatalf("schedule state = %s want running", schedState)
	}
	// Terminal schedule is not adjudicated again (idempotent).
	res3, err := fx.exec.ProcessDueSchedules(context.Background())
	if err != nil {
		t.Fatalf("tick3: %v", err)
	}
	if res3.Claimed != 0 {
		t.Fatalf("re-claim: %+v", res3)
	}
}

// TestB11ScheduleMissedNeverCatchesUp missed schedule never catches up: past grace window ->
// missed + order missed_schedule terminal state, zero Attempts; restart recovery follows this path.
func TestB11ScheduleMissedNeverCatchesUp(t *testing.T) {
	fx := newB11Fixture(t)
	execapp.SetScheduleClaimGraceForTest(10 * time.Second)
	defer execapp.SetScheduleClaimGraceForTest(execapp.ScheduleClaimGrace)
	orderID, version := fx.b11PendingOrder(t)
	at := time.Now().Add(10 * time.Minute)
	if code, body, raw := fx.b11CreateScheduleHTTP(t, orderID, version, at); code != 200 || errCode(body) != 0 {
		t.Fatalf("schedule: %d %s", code, raw)
	}
	// Simulate downtime past scheduled time + grace window.
	fx.db.Exec(`update execution_schedules set scheduled_for = now() - interval '120 seconds'`)
	fx.schedulerTick(t)
	if got := fx.b10OrderState(t, orderID); got != "missed_schedule" {
		t.Fatalf("order state = %s want missed_schedule", got)
	}
	var schedState string
	fx.db.Raw(`select state from execution_schedules where order_id = ?`, orderID).Scan(&schedState)
	if schedState != "missed" {
		t.Fatalf("schedule state = %s want missed", schedState)
	}
	var attempts int
	fx.db.Raw(`select count(*) from execution_attempts where order_id = ?`, orderID).Scan(&attempts)
	if attempts != 0 {
		t.Fatalf("missed schedule executed: %d attempts", attempts)
	}
	// Never catches up after reaching terminal state.
	res, err := fx.exec.ProcessDueSchedules(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Missed != 0 && res.Claimed != 0 {
		t.Fatalf("terminal reprocessed: %+v", res)
	}
}

// TestB11ScheduleBlockedByDatasource datasource unavailable at due time -> order
// blocked_datasource_unavailable + schedule blocked; no automatic retry.
func TestB11ScheduleBlockedByDatasource(t *testing.T) {
	fx := newB11Fixture(t)
	execapp.SetScheduleClaimGraceForTest(5 * time.Minute)
	defer execapp.SetScheduleClaimGraceForTest(execapp.ScheduleClaimGrace)
	orderID, version := fx.b11PendingOrder(t)
	at := time.Now().Add(10 * time.Minute)
	if code, body, raw := fx.b11CreateScheduleHTTP(t, orderID, version, at); code != 200 || errCode(body) != 0 {
		t.Fatalf("schedule: %d %s", code, raw)
	}
	fx.db.Exec(`update execution_schedules set scheduled_for = now() - interval '2 seconds'`)
	// Disable datasource.
	fx.db.Exec(`update datasources set enabled = false where id = (
		select datasource_id from order_stages where order_id = ?)`, orderID)
	res, err := fx.exec.ProcessDueSchedules(context.Background())
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	if res.Blocked != 1 {
		t.Fatalf("blocked: %+v", res)
	}
	if got := fx.b10OrderState(t, orderID); got != "blocked_datasource_unavailable" {
		t.Fatalf("order state = %s want blocked_datasource_unavailable", got)
	}
	var schedState string
	fx.db.Raw(`select state from execution_schedules where order_id = ?`, orderID).Scan(&schedState)
	if schedState != "blocked" {
		t.Fatalf("schedule state = %s want blocked", schedState)
	}
	var attempts int
	fx.db.Raw(`select count(*) from execution_attempts where order_id = ?`, orderID).Scan(&attempts)
	if attempts != 0 {
		t.Fatalf("blocked schedule executed: %d", attempts)
	}
}

// TestB11ScheduleWithdrawCancels withdrawing order cancels pending schedule
// (pending_schedule_cancelled postcondition; implemented in B9, verified end-to-end here).
func TestB11ScheduleWithdrawCancels(t *testing.T) {
	fx := newB11Fixture(t)
	orderID, version := fx.b11PendingOrder(t)
	at := time.Now().Add(10 * time.Minute)
	if code, body, raw := fx.b11CreateScheduleHTTP(t, orderID, version, at); code != 200 || errCode(body) != 0 {
		t.Fatalf("schedule: %d %s", code, raw)
	}
	// Submitter withdraws order.
	if code, body, raw := fx.owner.doWithHeaders("POST", "/api/v4/change-orders/"+orderID+"/withdrawal",
		map[string]any{"reason": "changed mind"}, map[string]string{
			"Idempotency-Key": "b11-wd-" + orderID[:8],
			"If-Match":        fmt.Sprintf(`"%d"`, version+1),
			"X-CSRF-Token":    fx.owner.cookies["yearning_csrf"],
		}); code != 200 || errCode(body) != 0 {
		t.Fatalf("withdraw: %d %s", code, raw)
	}
	var schedState string
	fx.db.Raw(`select state from execution_schedules where order_id = ?`, orderID).Scan(&schedState)
	if schedState != "cancelled" {
		t.Fatalf("schedule state = %s want cancelled", schedState)
	}
	if got := fx.b10OrderState(t, orderID); got != "withdrawn" {
		t.Fatalf("order state = %s want withdrawn", got)
	}
}

// TestB11ScheduleNotYetDueDoesNotClaim M3/H1 regression: schedule not yet due (including future
// timestamps within grace window duration) is not claimed by tick -- execution time is never moved earlier.
func TestB11ScheduleNotYetDueDoesNotClaim(t *testing.T) {
	fx := newB11Fixture(t)
	execapp.SetScheduleClaimGraceForTest(5 * time.Minute)
	defer execapp.SetScheduleClaimGraceForTest(execapp.ScheduleClaimGrace)
	orderID, version := fx.b11PendingOrder(t)
	// Schedule 30 seconds in the future (falls within grace window duration -- H1 defect shape).
	if code, body, raw := fx.b11CreateScheduleHTTP(t, orderID, version, time.Now().Add(30*time.Second).Truncate(time.Second).Add(6*time.Minute)); code != 200 || errCode(body) != 0 {
		t.Fatalf("schedule: %d %s", code, raw)
	}
	// Rewind to "30 seconds in the future" (still in the future).
	fx.db.Exec(`update execution_schedules set scheduled_for = now() + interval '30 seconds'`)
	fx.schedulerTick(t)
	if got := fx.b10OrderState(t, orderID); got != "scheduled" {
		t.Fatalf("premature claim: order state = %s want scheduled", got)
	}
	var attempts int
	fx.db.Raw(`select count(*) from execution_attempts where order_id = ?`, orderID).Scan(&attempts)
	if attempts != 0 {
		t.Fatalf("premature execution: %d attempts", attempts)
	}
	var schedState string
	fx.db.Raw(`select state from execution_schedules where order_id = ?`, orderID).Scan(&schedState)
	if schedState != "scheduled" {
		t.Fatalf("schedule state = %s want scheduled", schedState)
	}
}

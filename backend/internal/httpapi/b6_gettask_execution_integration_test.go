//go:build integration

// b6_gettask_execution_integration_test.go SEC-0059 regression: stage_execution branch of
// getTask (RCP-20261021-GETTASK-EXECUTOR-READ) --
// executor can read task metadata for their own Attempt; submitter / reviewer / unrelated user /
// builtin admin all receive 403 (admin no longer reads non-management tasks); executor reading
// another user Attempt task receives 403 (horizontal privilege escalation blocked).
package httpapi

import (
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestB6GetTaskStageExecutionRelation(t *testing.T) {
	fx := newB10Fixture(t)
	target := newPostgresSearchPathTarget(t, fx)
	table := "task_reader_" + strings.ToLower(target.executionRole[len("yr_search_exec_"):])
	execTarget(t, target.admin,
		`create table `+quoteSearchPathIdent(target.secondSchema)+`.`+
			quoteSearchPathIdent(table)+` (id integer primary key, amount integer)`)
	execTarget(t, target.admin,
		`grant select, update on `+quoteSearchPathIdent(target.secondSchema)+`.`+
			quoteSearchPathIdent(table)+` to `+quoteSearchPathIdent(target.executionRole))
	datasourceID := target.createDatasource(t, fx, "b6-task-reader")
	flowID := target.createEmptyMappingFlow(t, fx, "b6-task-reader-flow", datasourceID)

	statement := `update ` + table + ` set amount = amount + 1 where id = 1;`
	draftID, _, runID := fx.b9ReadyDraft(t, flowID, statement, (&searchPathRequestCapture{}).responder(fx))
	code, body, raw := fx.b9Submit(t, flowID, draftID, runID, statement)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("submit: %d %s", code, raw)
	}
	orderID := body["data"].(map[string]any)["id"].(string)
	orderVersion := int64(body["data"].(map[string]any)["version"].(float64))
	code, body, raw = fx.reviewer1.doWithHeaders("POST",
		"/api/v4/change-orders/"+orderID+"/approval-decisions",
		map[string]any{"decision": "approve", "comment": "go"},
		map[string]string{
			"If-Match":        fmt.Sprintf(`"%d"`, orderVersion),
			"Idempotency-Key": "b6-task-approve-" + uuid.NewString()[:8],
			"X-CSRF-Token":    fx.reviewer1.cookies["yearning_csrf"],
		})
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("approve: %d %s", code, raw)
	}
	attemptID := fx.b10Begin(t, orderID, fx.b10Version(t, orderID))

	var taskID string
	if err := fx.db.Raw(`select id::text from async_tasks
		where kind = 'stage_execution' and aggregate_id = ?`, attemptID).
		Scan(&taskID).Error; err != nil || taskID == "" {
		t.Fatalf("stage_execution task row: %v %q", err, taskID)
	}

	// Executor reading own Attempt -> 200 (allowlisted metadata view).
	code, body, raw = fx.executor.do("GET", "/api/v4/tasks/"+taskID, nil, false)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("executor task read: %d %s", code, raw)
	}
	view := body["data"].(map[string]any)
	if view["kind"] != "stage_execution" || view["state"] == "" {
		t.Fatalf("task view: %v", view)
	}
	if _, ok := view["progress"].(map[string]any); !ok {
		t.Fatalf("progress block missing: %v", view)
	}

	// Submitter / reviewer / unrelated user -> 403 (executor relationship does not spill over; their progress
	// visibility uses the existing execution-attempts read surface -- submitter can still read Attempt detail).
	for _, tc := range []struct {
		name string
		cl   testClient
	}{
		{"submitter", fx.owner},
		{"reviewer", fx.reviewer1},
		{"unrelated", fx.intruder},
	} {
		if c, _, _ := tc.cl.do("GET", "/api/v4/tasks/"+taskID, nil, false); c != http403 {
			t.Fatalf("%s task read want 403 got %d", tc.name, c)
		}
	}
	if c, b, r := fx.owner.do("GET", "/api/v4/execution-attempts/"+attemptID, nil, true); c != 200 || errCode(b) != 0 {
		t.Fatalf("submitter attempt read (existing channel) : %d %s", c, r)
	}

	// Builtin admin -> 403 (tightened: admin is rejected just like regular users on non-management tasks).
	if c, _, _ := fx.admin.do("GET", "/api/v4/tasks/"+taskID, nil, false); c != http403 {
		t.Fatalf("admin stage_execution task read want 403 got %d", c)
	}

	// Mismatched aggregate_type -> 403 (aggregate reference must be execution_attempt).
	if err := fx.db.Exec(`update async_tasks set aggregate_type = 'other' where id = ?`, taskID).Error; err != nil {
		t.Fatal(err)
	}
	if c, _, _ := fx.executor.do("GET", "/api/v4/tasks/"+taskID, nil, false); c != http403 {
		t.Fatalf("executor task read with wrong aggregate_type want 403 got %d", c)
	}
	if err := fx.db.Exec(`update async_tasks set aggregate_type = 'execution_attempt' where id = ?`, taskID).Error; err != nil {
		t.Fatal(err)
	}
}

// TestB6GetTaskStageExecutionHorizontal executor reading another executor Attempt task ->
// 403 (horizontal_access_cases: cross-order task metadata is not visible).
func TestB6GetTaskStageExecutionHorizontal(t *testing.T) {
	fx := newB10Fixture(t)
	target := newPostgresSearchPathTarget(t, fx)
	table := "task_horiz_" + strings.ToLower(target.executionRole[len("yr_search_exec_"):])
	execTarget(t, target.admin,
		`create table `+quoteSearchPathIdent(target.secondSchema)+`.`+
			quoteSearchPathIdent(table)+` (id integer primary key, amount integer)`)
	execTarget(t, target.admin,
		`grant select, update on `+quoteSearchPathIdent(target.secondSchema)+`.`+
			quoteSearchPathIdent(table)+` to `+quoteSearchPathIdent(target.executionRole))
	datasourceID := target.createDatasource(t, fx, "b6-task-reader-h")

	submitApprove := func(flowID, idemTag, statement string) (string, int64) {
		t.Helper()
		draftID, _, runID := fx.b9ReadyDraft(t, flowID, statement, (&searchPathRequestCapture{}).responder(fx))
		code, body, raw := fx.b9Submit(t, flowID, draftID, runID, statement)
		if code != 200 || errCode(body) != 0 {
			t.Fatalf("submit %s: %d %s", idemTag, code, raw)
		}
		orderID := body["data"].(map[string]any)["id"].(string)
		orderVersion := int64(body["data"].(map[string]any)["version"].(float64))
		code, body, raw = fx.reviewer1.doWithHeaders("POST",
			"/api/v4/change-orders/"+orderID+"/approval-decisions",
			map[string]any{"decision": "approve", "comment": "go"},
			map[string]string{
				"If-Match":        fmt.Sprintf(`"%d"`, orderVersion),
				"Idempotency-Key": "b6-task-h-" + idemTag + "-" + uuid.NewString()[:8],
				"X-CSRF-Token":    fx.reviewer1.cookies["yearning_csrf"],
			})
		if code != 200 || errCode(body) != 0 {
			t.Fatalf("approve %s: %d %s", idemTag, code, raw)
		}
		return orderID, fx.b10Version(t, orderID)
	}

	// Order A: executor exec1.
	flowA := target.createEmptyMappingFlow(t, fx, "b6-task-h-a", datasourceID)
	orderA, versionA := submitApprove(flowA, "a",
		`update `+table+` set amount = amount + 1 where id = 1;`)
	attemptA := fx.b10Begin(t, orderA, versionA)

	// Order B: second executor exec2 (same target DB, separate flow).
	exec2ID := fx.adminCreateUserB6(t, "exec2", "exec2-pass-123")
	exec2 := testClient{t: t, engine: fx.engine, cookies: map[string]string{}}
	if _, b, _ := exec2.login("exec2", "exec2-pass-123"); errCode(b) != 0 {
		t.Fatal("exec2 login failed")
	}
	stageB := fx.b10Stage(1, datasourceID, target.secondSchema)
	stageB["execution_actors"] = []map[string]any{{"user_id": exec2ID}}
	flowB := fx.b9CreateFlow(t, "b6-task-h-b", []map[string]any{stageB})
	fx.b9CreateGrant(t, fx.ownerID, flowB)
	orderB, versionB := submitApprove(flowB, "b",
		`update `+table+` set amount = amount + 2 where id = 1;`)
	_, body, raw := fx.b10HTTPBegin(t, exec2, orderB, versionB)
	if errCode(body) != 0 {
		t.Fatalf("exec2 begin: %s", raw)
	}
	attemptB := body["data"].(map[string]any)["id"].(string)

	taskFor := func(attemptID string) string {
		t.Helper()
		var id string
		if err := fx.db.Raw(`select id::text from async_tasks
			where kind = 'stage_execution' and aggregate_id = ?`, attemptID).
			Scan(&id).Error; err != nil || id == "" {
			t.Fatalf("task row for %s: %v", attemptID, err)
		}
		return id
	}

	// Each executor reads their own task -> 200.
	for _, tc := range []struct {
		cl      testClient
		attempt string
	}{
		{fx.executor, attemptA}, {exec2, attemptB},
	} {
		if c, b, r := tc.cl.do("GET", "/api/v4/tasks/"+taskFor(tc.attempt), nil, false); c != 200 || errCode(b) != 0 {
			t.Fatalf("executor own task read: %d %s", c, r)
		}
	}
	// Cross-reading -> 403.
	if c, _, _ := fx.executor.do("GET", "/api/v4/tasks/"+taskFor(attemptB), nil, false); c != http403 {
		t.Fatalf("exec1 reading exec2 task want 403 got %d", c)
	}
	if c, _, _ := exec2.do("GET", "/api/v4/tasks/"+taskFor(attemptA), nil, false); c != http403 {
		t.Fatalf("exec2 reading exec1 task want 403 got %d", c)
	}
}

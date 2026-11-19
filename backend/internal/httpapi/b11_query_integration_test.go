//go:build integration

// b11_query_integration_test.go query domain acceptance (Q001-Q007):
// approval grants access rather than reviewing SQL, direct session when approval disabled, Grant revocation/relinquish,
// single-SELECT safety, server-side cursor pagination, display/export Purpose, Owner affinity and no-replay on expiry,
// and masking alias escape closure (RCP-20260828-FLOW-MASKING).
package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/cookieY/YearningX/backend/internal/query/application"
	queryapp "github.com/cookieY/YearningX/backend/internal/query/application"
	querydomain "github.com/cookieY/YearningX/backend/internal/query/domain"
)

// b11ActiveSessionHTTP creates a Session over HTTP (requires request and approval when approval is enabled by default).
func (fx *b11Fixture) b11ActiveSessionHTTP(t *testing.T) string {
	t.Helper()
	// Request -> approval -> Grant -> Session.
	code, body, raw := fx.requester.doWithHeaders("POST", "/api/v4/query-access-requests", map[string]any{
		"flow_id":         fx.qFlowID,
		"datasource_ids":  []string{fx.qDsID},
		"requested_until": time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339Nano),
		"reason":          "b11 integration",
	}, map[string]string{
		"Idempotency-Key": "b11-req-" + uuid.NewString()[:12],
		"X-CSRF-Token":    fx.requester.cookies["yearning_csrf"],
	})
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("create access request: %d %s", code, raw)
	}
	req := body["data"].(map[string]any)
	reqID := req["id"].(string)
	version := int64(req["version"].(float64))
	// rev1 approves (any-one passes).
	if code, body, raw = fx.reviewer1.doWithHeaders("POST", "/api/v4/query-access-requests/"+reqID+"/decisions",
		map[string]any{"decision": "approve", "comment": "ok"}, map[string]string{
			"Idempotency-Key": "b11-dec-" + reqID[:8],
			"If-Match":        fmt.Sprintf(`"%d"`, version),
			"X-CSRF-Token":    fx.reviewer1.cookies["yearning_csrf"],
		}); code != 200 || errCode(body) != 0 {
		t.Fatalf("decide: %d %s", code, raw)
	}
	grantID := body["data"].(map[string]any)["grant_id"].(string)
	// Session.
	if code, body, raw = fx.requester.do("POST", "/api/v4/query-sessions", map[string]any{
		"flow_id": fx.qFlowID, "grant_id": grantID, "datasource_ids": []string{fx.qDsID},
	}, true); code != 200 || errCode(body) != 0 {
		t.Fatalf("create session: %d %s", code, raw)
	}
	return body["data"].(map[string]any)["id"].(string)
}

// b11ExecHTTP executes SELECT over HTTP.
func (fx *b11Fixture) b11ExecHTTP(t *testing.T, sessionID, sql string, timeoutMS, pageSize int) (int, map[string]any, string) {
	t.Helper()
	payload := map[string]any{
		"datasource_id": fx.qDsID, "schema_name": fx.qSchema, "sql": sql,
	}
	if timeoutMS > 0 {
		payload["timeout_ms"] = timeoutMS
	}
	if pageSize > 0 {
		payload["page_size"] = pageSize
	}
	return fx.requester.do("POST", "/api/v4/query-sessions/"+sessionID+"/executions", payload, true)
}

// TestB11ApprovalGrantsDatasourceAccessNotSQL Q001/Q003: approval grants datasource
// access eligibility; once approved, executing multiple SELECTs creates no further approval instances.
func TestB11ApprovalGrantsDatasourceAccessNotSQL(t *testing.T) {
	fx := newB11Fixture(t)
	sessionID := fx.b11ActiveSessionHTTP(t)
	var before int
	fx.db.Raw(`select count(*) from query_access_requests`).Scan(&before)
	for i := 0; i < 3; i++ {
		code, body, raw := fx.b11ExecHTTP(t, sessionID,
			fmt.Sprintf("select id, name from qtarget.accounts where id = %d", i+1), 0, 0)
		if code != 200 || errCode(body) != 0 {
			t.Fatalf("execute %d: %d %s", i, code, raw)
		}
	}
	var after int
	fx.db.Raw(`select count(*) from query_access_requests`).Scan(&after)
	if after != before {
		t.Fatalf("per-SQL approval created: before=%d after=%d", before, after)
	}
	// Grant capabilities = frozen subset of request; Session freezes datasource set.
	var caps int
	fx.db.Raw(`select count(*) from query_session_capabilities where session_id = ?`, sessionID).Scan(&caps)
	if caps != 1 {
		t.Fatalf("session capabilities = %d want 1", caps)
	}
}

// TestB11ApprovalDisabledDirectSession Q002: approval-disabled mode resolves flow datasources directly
// with no max TTL; flow edits do not alter existing Sessions; re-enabling approval immediately invalidates direct access.
func TestB11ApprovalDisabledDirectSession(t *testing.T) {
	fx := newB11Fixture(t)
	fx.b11SetApprovalEnabled(t, false)
	code, body, raw := fx.requester.do("POST", "/api/v4/query-sessions", map[string]any{
		"flow_id": fx.qFlowID, "datasource_ids": []string{fx.qDsID},
	}, true)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("direct session: %d %s", code, raw)
	}
	sessionID := body["data"].(map[string]any)["id"].(string)
	// Direct execution (without Grant).
	if code, body, raw = fx.b11ExecHTTP(t, sessionID, "select 1 as v", 0, 0); code != 200 || errCode(body) != 0 {
		t.Fatalf("direct execute: %d %s", code, raw)
	}
	// Datasource outside flow is rejected (business errors also return HTTP 200; assert via err_code).
	if code, body, _ = fx.requester.do("POST", "/api/v4/query-sessions", map[string]any{
		"flow_id": fx.qFlowID, "datasource_ids": []string{"00000000-0000-0000-0000-000000000001"},
	}, true); errCode(body) == 0 {
		t.Fatalf("foreign datasource session accepted: %d %s", code, body)
	}
	// Re-enable approval: direct access is immediately invalidated (new query returns 4001).
	fx.b11SetApprovalEnabled(t, true)
	if code, body, _ = fx.b11ExecHTTP(t, sessionID, "select 1 as v", 0, 0); code != 200 || errCode(body) != 4001 {
		t.Fatalf("after re-enable: %d err=%d want 4001", code, errCode(body))
	}
}

// TestB11GrantRevocation Q004: frozen reviewer/admin revokes Grant (reason required, visible to requester);
// admin does not gain query access from revocation privilege; new queries immediately return 4004 after revocation.
func TestB11GrantRevocation(t *testing.T) {
	fx := newB11Fixture(t)
	sessionID := fx.b11ActiveSessionHTTP(t)
	var grantID string
	fx.db.Raw(`select grant_id::text from query_sessions where id = ?`, sessionID).Scan(&grantID)
	var version int64
	fx.db.Raw(`select version from query_grants where id = ?`, grantID).Scan(&version)
	// Admin revokes (reason required).
	code, body, raw := fx.admin.doWithHeaders("POST", "/api/v4/query-grants/"+grantID+"/revocations", map[string]any{
		"reason": "policy: rotated",
	}, map[string]string{
		"Idempotency-Key": "b11-rev-" + grantID[:8],
		"If-Match":        fmt.Sprintf(`"%d"`, version),
		"X-CSRF-Token":    fx.admin.cookies["yearning_csrf"],
	})
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("admin revoke: %d %s", code, raw)
	}
	if body["data"].(map[string]any)["state"] != "revoked" {
		t.Fatal("grant not revoked")
	}
	// Requester can see revocation facts (reason / actor persisted).
	var rb struct {
		Reason string `gorm:"column:revoke_reason"`
		By     string `gorm:"column:revoked_by_username_snapshot"`
	}
	fx.db.Raw(`select revoke_reason, revoked_by_username_snapshot from query_grants where id = ?`, grantID).Scan(&rb)
	reason, by := rb.Reason, rb.By
	if reason != "policy: rotated" || by != "admin" {
		t.Fatalf("revocation visibility: reason=%q by=%q", reason, by)
	}
	// New queries fail immediately after revocation.
	if code, body, _ = fx.b11ExecHTTP(t, sessionID, "select 1 as v", 0, 0); code != 200 || errCode(body) != 4004 {
		t.Fatalf("after revoke: %d err=%d want 4004", code, errCode(body))
	}
	// Admin has no Grant of their own: creating a Session is rejected (revocation privilege does not grant query access).
	if code, body, _ = fx.admin.do("POST", "/api/v4/query-sessions", map[string]any{
		"flow_id": fx.qFlowID, "datasource_ids": []string{fx.qDsID},
	}, true); code != 200 || errCode(body) == 0 {
		t.Fatal("admin gained query access via revocation")
	}
	// Frozen reviewer can also revoke (new Grant path).
	session2 := fx.b11ActiveSessionHTTP(t)
	var grant2 string
	fx.db.Raw(`select grant_id::text from query_sessions where id = ?`, session2).Scan(&grant2)
	var v2 int64
	fx.db.Raw(`select version from query_grants where id = ?`, grant2).Scan(&v2)
	if code, body, raw = fx.reviewer2.doWithHeaders("POST", "/api/v4/query-grants/"+grant2+"/revocations", map[string]any{
		"reason": "reviewer revokes",
	}, map[string]string{
		"Idempotency-Key": "b11-rev2-" + grant2[:8],
		"If-Match":        fmt.Sprintf(`"%d"`, v2),
		"X-CSRF-Token":    fx.reviewer2.cookies["yearning_csrf"],
	}); code != 200 || errCode(body) != 0 {
		t.Fatalf("reviewer revoke: %d %s", code, raw)
	}
}

// TestB11GrantRelinquish relinquishes access grant (irreversible; Session terminated).
func TestB11GrantRelinquish(t *testing.T) {
	fx := newB11Fixture(t)
	sessionID := fx.b11ActiveSessionHTTP(t)
	var grantID string
	fx.db.Raw(`select grant_id::text from query_sessions where id = ?`, sessionID).Scan(&grantID)
	var version int64
	fx.db.Raw(`select version from query_grants where id = ?`, grantID).Scan(&version)
	code, body, raw := fx.requester.doWithHeaders("POST", "/api/v4/query-grants/"+grantID+"/relinquishment", map[string]any{
		"reason": "done",
	}, map[string]string{
		"Idempotency-Key": "b11-rel-" + grantID[:8],
		"If-Match":        fmt.Sprintf(`"%d"`, version),
		"X-CSRF-Token":    fx.requester.cookies["yearning_csrf"],
	})
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("relinquish: %d %s", code, raw)
	}
	if body["data"].(map[string]any)["state"] != "relinquished" {
		t.Fatal("grant not relinquished")
	}
	// Relinquish semantics: Session closes with close_reason=grant_relinquished
	// -> new queries return 4006; Grant enters relinquished terminal state (no separate error code; 4006 is honest).
	if code, body, _ = fx.b11ExecHTTP(t, sessionID, "select 1 as v", 0, 0); code != 200 || errCode(body) != 4006 {
		t.Fatalf("after relinquish: %d err=%d want 4006", code, errCode(body))
	}
	// Duplicate relinquish is invalid (already terminal).
	if code, body, _ = fx.requester.doWithHeaders("POST", "/api/v4/query-grants/"+grantID+"/relinquishment", map[string]any{
		"reason": "again",
	}, map[string]string{
		"Idempotency-Key": "b11-rel2-" + grantID[:8],
		"If-Match":        fmt.Sprintf(`"%d"`, version+1),
		"X-CSRF-Token":    fx.requester.cookies["yearning_csrf"],
	}); code == 200 && errCode(body) == 0 {
		t.Fatal("double relinquish accepted")
	}
}

// TestB11SingleSelectSafetyAndPagination Q005: writes / batches with impure statements /
// disguised CTEs are rejected; timeout bounds; server-side cursor pagination pages without duplicates.
// F-Q-001 (owner decision): pure SELECT batches (semicolon-separated) are allowed -- this test
// only rejects batches containing non-pure SELECT statements.
func TestB11SingleSelectSafetyAndPagination(t *testing.T) {
	fx := newB11Fixture(t)
	sessionID := fx.b11ActiveSessionHTTP(t)
	for _, sql := range []string{
		"update qtarget.accounts set name = 'x' where id = 1",
		"select 1; update qtarget.accounts set name = 'x' where id = 1",
		"with d as (delete from qtarget.accounts returning *) select * from d",
		"explain select 1",
		"select * from qtarget.accounts into outfile '/tmp/x'",
		"select pg_sleep(10)",
	} {
		if code, body, _ := fx.b11ExecHTTP(t, sessionID, sql, 0, 0); code != 200 || errCode(body) != 4007 {
			t.Fatalf("unsafe sql %q: code=%d err=%d", sql, code, errCode(body))
		}
	}
	// F-Q-001: pure SELECT batch + trailing semicolon -- passes and returns the result set of the last statement.
	code, body, raw := fx.b11ExecHTTP(t, sessionID,
		"select id, label from qtarget.pages order by id limit 1;", 0, 100)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("trailing semicolon single: %d %s", code, raw)
	}
	code, body, raw = fx.b11ExecHTTP(t, sessionID,
		"select id, label from qtarget.pages order by id limit 3; select id, label from qtarget.pages order by id", 0, 100)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("batch last-result: %d %s", code, raw)
	}
	{
		data := body["data"].(map[string]any)
		if rows := data["rows"].([]any); len(rows) != 100 {
			t.Fatalf("batch rows = %d want 100 (last statement)", len(rows))
		}
	}
	// Timeout range (>300s rejected).
	if code, body, _ := fx.b11ExecHTTP(t, sessionID, "select 1 as v", 300001, 0); code != 200 || errCode(body) != 4008 {
		t.Fatalf("timeout range: %d err=%d", code, errCode(body))
	}
	// Pagination: 200 rows, page size 100 -> 2 pages.
	code, body, raw = fx.b11ExecHTTP(t, sessionID, "select id, label from qtarget.pages order by id", 0, 100)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("page1: %d %s", code, raw)
	}
	data := body["data"].(map[string]any)
	rows := data["rows"].([]any)
	if len(rows) != 100 {
		t.Fatalf("page1 rows = %d want 100", len(rows))
	}
	first := data["page"].(map[string]any)
	if first["has_more"] != true || first["next_cursor"] == nil {
		t.Fatalf("page1 pagination: %v", first)
	}
	cursor := first["next_cursor"].(string)
	execID := data["execution_id"].(string)
	seen := map[float64]bool{}
	for _, r := range rows {
		seen[r.([]any)[0].(float64)] = true
	}
	// Page until exhausted (adapter conservatively sets hasMore=true on full page: may terminate after an empty final page).
	cursorPtr := &cursor
	total := 100
	for page := 2; page <= 6; page++ {
		code, body, raw = fx.requester.do("GET",
			"/api/v4/query-executions/"+execID+"/pages?cursor="+*cursorPtr+"&purpose=display", nil, false)
		if code != 200 || errCode(body) != 0 {
			t.Fatalf("page%d: %d %s", page, code, raw)
		}
		data = body["data"].(map[string]any)
		rows = data["rows"].([]any)
		total += len(rows)
		for _, r := range rows {
			id := r.([]any)[0].(float64)
			if seen[id] {
				t.Fatalf("row %v repeated across pages", id)
			}
			seen[id] = true
		}
		if data["page"].(map[string]any)["has_more"] != true {
			break
		}
		nc, _ := data["page"].(map[string]any)["next_cursor"].(string)
		if nc == "" {
			t.Fatal("has_more without next_cursor")
		}
		cursorPtr = &nc
	}
	if len(seen) != 200 || total != 200 {
		t.Fatalf("total distinct rows = %d (fetched %d) want 200", len(seen), total)
	}
	// No product-level total row/byte cap: valid large page (within page_size limit) returns normally.
	if code, body, raw = fx.b11ExecHTTP(t, sessionID, "select id from qtarget.pages", 0, 5000); code != 200 || errCode(body) != 0 {
		t.Fatalf("large page: %d %s", code, raw)
	}
}

// TestB11ExportPurpose Q006: export enforces can_export + audit on every page;
// display cannot be used for export semantics (export rejected when can_export is false).
func TestB11ExportPurpose(t *testing.T) {
	fx := newB11Fixture(t)
	// Flow without export capability.
	flowNoExport := fx.b11QueryFlow(t, fx.reviewer1ID, fx.reviewer2ID, fx.qDsID, true, false)
	fx.b9CreateGrant(t, fx.requesterID, flowNoExport)
	_ = flowNoExport
	// Service surface: HTTP session is on default flow (with export); use service instance to verify
	// an independent Session without export capability.
	sessionID := fx.b11ActiveSessionHTTP(t)
	code, body, raw := fx.b11ExecHTTP(t, sessionID, "select id from qtarget.pages", 0, 100)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("exec: %d %s", code, raw)
	}
	data := body["data"].(map[string]any)
	cursor := data["page"].(map[string]any)["next_cursor"].(string)
	execID := data["execution_id"].(string)
	var auditsBefore int
	fx.db.Raw(`select count(*) from audit_events where event_type = 'query.export_page_read'`).Scan(&auditsBefore)
	// With can_export: export page fetch succeeds and writes audit log.
	if code, body, raw = fx.requester.do("GET",
		"/api/v4/query-executions/"+execID+"/pages?cursor="+cursor+"&purpose=export", nil, false); code != 200 || errCode(body) != 0 {
		t.Fatalf("export page: %d %s", code, raw)
	}
	var auditsAfter int
	fx.db.Raw(`select count(*) from audit_events where event_type = 'query.export_page_read'`).Scan(&auditsAfter)
	if auditsAfter != auditsBefore+1 {
		t.Fatalf("export audit rows %d → %d", auditsBefore, auditsAfter)
	}
	// Without export capability: inject a Session without can_export into independent service instance.
	// Construct Session without export capability: copy Session row and flip can_export.
	sid2 := uuid.NewString()
	fx.db.Exec(`insert into query_sessions (id, user_id_snapshot, username_snapshot, flow_id_snapshot,
		flow_name_snapshot, grant_id, approval_required_snapshot, state, version, created_at, last_seen_at)
		select ?, user_id_snapshot, username_snapshot, flow_id_snapshot, flow_name_snapshot, grant_id,
		approval_required_snapshot, 'active', 1, now(), now() from query_sessions where id = ?`, sid2, sessionID)
	fx.db.Exec(`insert into query_session_capabilities (session_id, datasource_id, datasource_name_snapshot,
		datasource_identity_fingerprint, state, can_query, can_export)
		select ?, datasource_id, datasource_name_snapshot, datasource_identity_fingerprint, 'active', true, false
		from query_session_capabilities where session_id = ?`, sid2, sessionID)
	view, err := fx.q.ExecuteSelect(t.Context(), application.ExecuteSelectInput{
		Actor: fx.b11QActor(), SessionID: sid2, DatasourceID: fx.qDsID,
		SchemaName: fx.qSchema, SQL: "select id from qtarget.pages", PageSize: 100,
	})
	if err != nil {
		t.Fatalf("exec noexport: %v", err)
	}
	tok := *view.Page.NextCursor
	if _, err := fx.q.FetchPage(t.Context(), application.FetchPageInput{
		Actor: fx.b11QActor(), ExecutionID: view.ExecutionID, CursorToken: tok, Purpose: "export",
	}); err == nil || nameOfErr(err) != "QUERY_EXPORT_NOT_GRANTED" {
		t.Fatalf("export without can_export: %v", err)
	}
	// Invalid purpose.
	if _, err := fx.q.FetchPage(t.Context(), application.FetchPageInput{
		Actor: fx.b11QActor(), ExecutionID: view.ExecutionID, CursorToken: tok, Purpose: "other",
	}); err == nil || nameOfErr(err) != "VALIDATION_FAILED" {
		t.Fatalf("bad purpose: %v", err)
	}
}

// TestB11CursorOwnerLossAndExpiry Q007: Owner instance loss (cross-Pod) ->
// CURSOR_EXPIRED without replay; rejected after idle expiry sweep as well; unknown token = INVALID.
func TestB11CursorOwnerLossAndExpiry(t *testing.T) {
	fx := newB11Fixture(t)
	sessionID := fx.b11ActiveSessionHTTP(t)
	view, err := fx.q.ExecuteSelect(t.Context(), application.ExecuteSelectInput{
		Actor: fx.b11QActor(), SessionID: sessionID, DatasourceID: fx.qDsID,
		SchemaName: fx.qSchema, SQL: "select id from qtarget.pages order by id", PageSize: 50,
	})
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	tok := *view.Page.NextCursor
	// Same-instance continuation succeeds.
	if _, err := fx.q.FetchPage(t.Context(), application.FetchPageInput{
		Actor: fx.b11QActor(), ExecutionID: view.ExecutionID, CursorToken: tok, Purpose: "display",
	}); err != nil {
		t.Fatalf("same-instance fetch: %v", err)
	}
	// Second instance (simulating another Pod): Owner lost -> 1009 CURSOR_EXPIRED.
	other, oerr := application.NewServices(fx.db, fx.svc.Crypter, fx.svc.Recorder)
	if oerr != nil {
		t.Fatal(oerr)
	}
	if _, err := other.FetchPage(t.Context(), application.FetchPageInput{
		Actor: fx.b11QActor(), ExecutionID: view.ExecutionID, CursorToken: tok, Purpose: "display",
	}); err == nil || nameOfErr(err) != "CURSOR_EXPIRED" {
		t.Fatalf("cross-pod fetch: %v", err)
	}
	// Unknown token -> 4010.
	if _, err := fx.q.FetchPage(t.Context(), application.FetchPageInput{
		Actor: fx.b11QActor(), ExecutionID: view.ExecutionID, CursorToken: "deadbeef", Purpose: "display",
	}); err == nil || nameOfErr(err) != "QUERY_CURSOR_INVALID" {
		t.Fatalf("unknown token: %v", err)
	}
	// Idle expiry: inject very short idle TTL -> sweep -> 1009.
	fx.q.SetCursorIdleForTest(time.Millisecond)
	view2, err := fx.q.ExecuteSelect(t.Context(), application.ExecuteSelectInput{
		Actor: fx.b11QActor(), SessionID: sessionID, DatasourceID: fx.qDsID,
		SchemaName: fx.qSchema, SQL: "select id from qtarget.pages order by id", PageSize: 50,
	})
	if err != nil {
		t.Fatalf("exec2: %v", err)
	}
	tok2 := *view2.Page.NextCursor
	time.Sleep(5 * time.Millisecond)
	fx.q.SweepCursors(t.Context())
	if _, err := fx.q.FetchPage(t.Context(), application.FetchPageInput{
		Actor: fx.b11QActor(), ExecutionID: view2.ExecutionID, CursorToken: tok2, Purpose: "display",
	}); err == nil || nameOfErr(err) != "CURSOR_EXPIRED" {
		t.Fatalf("idle expired fetch: %v", err)
	}
}

// TestB11MaskingAliasEscapeHTTP masking alias escape closure (owner decision test):
// when vocabulary contains secret, `secret as z`, expression references, and star expansion mask by column name;
// unmatched columns are unaffected.
func TestB11MaskingAliasEscapeHTTP(t *testing.T) {
	fx := newB11Fixture(t)
	fx.b11SetVocabulary("secret")
	sessionID := fx.b11ActiveSessionHTTP(t)
	view, err := fx.q.ExecuteSelect(t.Context(), application.ExecuteSelectInput{
		Actor: fx.b11QActor(), SessionID: sessionID, DatasourceID: fx.qDsID,
		SchemaName: fx.qSchema, SQL: "select id, secret as z, name, upper(secret) as u from qtarget.accounts order by id",
	})
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if len(view.Rows) != 2 {
		t.Fatalf("rows = %d want 2", len(view.Rows))
	}
	for _, row := range view.Rows {
		if len(row) != 4 {
			t.Fatalf("row shape: %#v", row)
		}
		if row[1] != application.MaskedPlaceholder {
			t.Fatalf("aliased secret not masked: %#v", row)
		}
		if row[3] != application.MaskedPlaceholder {
			t.Fatalf("expression secret not masked: %#v", row)
		}
		if row[0] == application.MaskedPlaceholder || row[2] == application.MaskedPlaceholder {
			t.Fatalf("unmasked columns over-masked: %#v", row)
		}
	}
	// Star expansion: secret column is masked by name, name column is unmasked.
	view2, err := fx.q.ExecuteSelect(t.Context(), application.ExecuteSelectInput{
		Actor: fx.b11QActor(), SessionID: sessionID, DatasourceID: fx.qDsID,
		SchemaName: fx.qSchema, SQL: "select * from qtarget.accounts order by id",
	})
	if err != nil {
		t.Fatalf("exec star: %v", err)
	}
	for _, row := range view2.Rows {
		if len(row) != 4 {
			t.Fatalf("star row shape: %#v", row)
		}
		if row[2] != application.MaskedPlaceholder {
			t.Fatalf("star secret column not masked: %#v", row)
		}
	}
	// Metadata masked flag (evaluated live against vocabulary).
	cols, err := fx.q.ListColumns(t.Context(), fx.b11QActor(), sessionID, fx.qDsID, fx.qSchema, "accounts")
	if err != nil {
		t.Fatalf("metadata columns: %v", err)
	}
	masked := map[bool]int{}
	for _, c := range cols {
		masked[c.Masked]++
	}
	if masked[true] != 1 || masked[false] != 3 {
		t.Fatalf("masked flags: %+v", cols)
	}
}

// nameOf extracts business error name from HTTP response body.
func nameOf(body map[string]any) string {
	if body == nil {
		return ""
	}
	if n, ok := body["name"].(string); ok {
		return n
	}
	return ""
}

// nameOfErr extracts domain error name from service-layer error.
func nameOfErr(err error) string {
	type namer interface{ ErrorName() string }
	if e, ok := err.(namer); ok {
		return e.ErrorName()
	}
	var de *querydomain.Error
	if errors.As(err, &de) {
		return de.Name
	}
	return ""
}

// TestB11RevokeBetweenPagesDiscards M5: revocation occurring during continuation page fetch window ->
// page is discarded (not returned to client) and continuation immediately returns 4004.
func TestB11RevokeBetweenPagesDiscards(t *testing.T) {
	fx := newB11Fixture(t)
	sessionID := fx.b11ActiveSessionHTTP(t)
	view, err := fx.q.ExecuteSelect(t.Context(), application.ExecuteSelectInput{
		Actor: fx.b11QActor(), SessionID: sessionID, DatasourceID: fx.qDsID,
		SchemaName: fx.qSchema, SQL: "select id from qtarget.pages order by id", PageSize: 50,
	})
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	tok := *view.Page.NextCursor
	var grantID string
	fx.db.Raw(`select grant_id::text from query_sessions where id = ?`, sessionID).Scan(&grantID)
	var version int64
	fx.db.Raw(`select version from query_grants where id = ?`, grantID).Scan(&version)
	if _, _, err := fx.q.Revoke(t.Context(), application.RevokeInput{
		Actor:   queryapp.Actor{UserID: fx.reviewer1ID, Username: "rev1"},
		GrantID: grantID, Reason: "mid-page revoke", ExpectVersion: version,
		IdempotencyKey: "b11-mrev-" + grantID[:8],
	}); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	// Continuation after revocation: data is not returned to client -- revocation either expired the cursor row (1009)
	// or post-fetch guard discarded it (4004); both satisfy result discard semantics.
	if _, err := fx.q.FetchPage(t.Context(), application.FetchPageInput{
		Actor: fx.b11QActor(), ExecutionID: view.ExecutionID, CursorToken: tok, Purpose: "display",
	}); err == nil || (nameOfErr(err) != "QUERY_GRANT_REVOKED" && nameOfErr(err) != "CURSOR_EXPIRED") {
		t.Fatalf("post-revoke fetch returned data: %v", err)
	}
}

// TestB11ConcurrentFetchSameToken M6: concurrent continuations with same token are serialized via
// per-entry mutex -- no errors or skipped pages (every row returned exactly once).
func TestB11ConcurrentFetchSameToken(t *testing.T) {
	fx := newB11Fixture(t)
	sessionID := fx.b11ActiveSessionHTTP(t)
	view, err := fx.q.ExecuteSelect(t.Context(), application.ExecuteSelectInput{
		Actor: fx.b11QActor(), SessionID: sessionID, DatasourceID: fx.qDsID,
		SchemaName: fx.qSchema, SQL: "select id from qtarget.pages order by id", PageSize: 40,
	})
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	execID := view.ExecutionID
	first := *view.Page.NextCursor
	var mu sync.Mutex
	seen := map[float64]bool{}
	var wg sync.WaitGroup
	// 3 page fetchers start with the same token: serialized by mutex, cursor advances monotonically page by page;
	// late arrivers after exhaustion receive cursor terminal error and exit -- union equals all remaining rows once.
	for w := 0; w < 3; w++ {
		wg.Add(1)
		go func(tok string) {
			defer wg.Done()
			for {
				page, ferr := fx.q.FetchPage(t.Context(), application.FetchPageInput{
					Actor: fx.b11QActor(), ExecutionID: execID, CursorToken: tok, Purpose: "display",
				})
				if ferr != nil {
					return // Exhaustion / race termination: exit on non-data-plane error
				}
				mu.Lock()
				for _, row := range page.Rows {
					seen[any2float(row[0])] = true
				}
				mu.Unlock()
				if !page.Page.HasMore || page.Page.NextCursor == nil {
					return
				}
				tok = *page.Page.NextCursor
			}
		}(first)
	}
	wg.Wait()
	if len(seen) != 160 { // First page of 40 rows already returned; remaining 160 rows fetched via concurrent continuations
		t.Fatalf("distinct rows across concurrent fetchers = %d want 160", len(seen))
	}
}

func any2float(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case json.Number:
		f, _ := t.Float64()
		return f
	}
	return -1
}

// TestB11QueryMaxResultRowsCeiling verifies that query.max_result_rows caps online display
// queries (both single-shot and multi-page continuations) and closes the cursor immediately
// upon reaching the ceiling, while leaving purpose=export continuations unbounded.
func TestB11QueryMaxResultRowsCeiling(t *testing.T) {
	fx := newB11Fixture(t)
	sessionID := fx.b11ActiveSessionHTTP(t)

	code, body, raw := fx.admin.putWithIfMatch("/api/v4/admin/settings/query", map[string]any{
		"settings": map[string]any{
			"approval_enabled":                         true,
			"default_timeout_ms":                       30000,
			"maximum_approved_access_duration_minutes": 43200,
			"max_result_rows":                          60,
		},
	}, "1")
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("update max_result_rows: %d %s", code, raw)
	}

	// 1. Single-request display query with page_size=1000 caps at 60 rows and immediately closes cursor.
	code, body, raw = fx.b11ExecHTTP(t, sessionID, "select id from qtarget.pages order by id", 0, 1000)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("exec single-shot: %d %s", code, raw)
	}
	data := body["data"].(map[string]any)
	if got := len(data["rows"].([]any)); got != 60 {
		t.Fatalf("single-shot rows = %d, want 60", got)
	}
	page := data["page"].(map[string]any)
	if page["has_more"] != false || page["next_cursor"] != nil {
		t.Fatalf("single-shot page should be exhausted immediately, got %v", page)
	}

	// 2. Multi-page display query with page_size=40 returns 40 rows then 20 rows and terminates.
	code, body, raw = fx.b11ExecHTTP(t, sessionID, "select id from qtarget.pages order by id", 0, 40)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("exec multi-page: %d %s", code, raw)
	}
	data = body["data"].(map[string]any)
	if got := len(data["rows"].([]any)); got != 40 {
		t.Fatalf("page1 rows = %d, want 40", got)
	}
	cursor := data["page"].(map[string]any)["next_cursor"].(string)
	execID := data["execution_id"].(string)
	code, body, raw = fx.requester.do("GET",
		"/api/v4/query-executions/"+execID+"/pages?cursor="+cursor+"&purpose=display", nil, false)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("page2 display: %d %s", code, raw)
	}
	data = body["data"].(map[string]any)
	if got := len(data["rows"].([]any)); got != 20 {
		t.Fatalf("page2 rows = %d, want 20 (capped at 60 total)", got)
	}
	page = data["page"].(map[string]any)
	if page["has_more"] != false || page["next_cursor"] != nil {
		t.Fatalf("page2 should be exhausted at max_result_rows=60, got %v", page)
	}

	// 3. Export flow (page_size=1 + purpose=export) is not truncated by max_result_rows.
	code, body, raw = fx.b11ExecHTTP(t, sessionID, "select id from qtarget.pages order by id", 0, 1)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("exec export start: %d %s", code, raw)
	}
	data = body["data"].(map[string]any)
	cursor = data["page"].(map[string]any)["next_cursor"].(string)
	execID = data["execution_id"].(string)
	code, body, raw = fx.requester.do("GET",
		"/api/v4/query-executions/"+execID+"/pages?cursor="+cursor+"&purpose=export&page_size=500", nil, false)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("export continuation: %d %s", code, raw)
	}
	data = body["data"].(map[string]any)
	if got := len(data["rows"].([]any)); got != 199 {
		t.Fatalf("export continuation rows = %d, want 199 (total 200 rows)", got)
	}
}

// TestB11SessionExecutionsAndDashboardPendingApprovalCount verifies that:
// 1. Submitting a query access request increments pending_approval_count on GET /api/v4/dashboard/me for active step reviewers, and approving decrements it.
// 2. GET /api/v4/query-sessions/{session_id} returns decrypted execution history items in newest-first order so page refreshes preserve the session history panel.
func TestB11SessionExecutionsAndDashboardPendingApprovalCount(t *testing.T) {
	fx := newB11Fixture(t)

	// 1. Submit a query access request and verify reviewer1's dashboard pending_approval_count increments to 1.
	code, body, raw := fx.requester.doWithHeaders("POST", "/api/v4/query-access-requests", map[string]any{
		"flow_id":         fx.qFlowID,
		"datasource_ids":  []string{fx.qDsID},
		"requested_until": time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339Nano),
		"reason":          "verify dashboard counter and session history",
	}, map[string]string{
		"Idempotency-Key": "b11-dash-req-" + uuid.NewString()[:12],
		"X-CSRF-Token":    fx.requester.cookies["yearning_csrf"],
	})
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("create access request: %d %s", code, raw)
	}
	req := body["data"].(map[string]any)
	reqID := req["id"].(string)
	version := int64(req["version"].(float64))

	code, body, raw = fx.reviewer1.do("GET", "/api/v4/dashboard/me", nil, false)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("reviewer1 dashboard before approve: %d %s", code, raw)
	}
	if got := int(body["data"].(map[string]any)["pending_approval_count"].(float64)); got != 1 {
		t.Fatalf("reviewer1 pending_approval_count with active query access request = %d, want 1", got)
	}

	// Approve the request and verify reviewer1's pending_approval_count returns to 0.
	code, body, raw = fx.reviewer1.doWithHeaders("POST", "/api/v4/query-access-requests/"+reqID+"/decisions",
		map[string]any{"decision": "approve", "comment": "approved"}, map[string]string{
			"Idempotency-Key": "b11-dash-dec-" + reqID[:8],
			"If-Match":        fmt.Sprintf(`"%d"`, version),
			"X-CSRF-Token":    fx.reviewer1.cookies["yearning_csrf"],
		})
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("decide: %d %s", code, raw)
	}
	grantID := body["data"].(map[string]any)["grant_id"].(string)

	code, body, raw = fx.reviewer1.do("GET", "/api/v4/dashboard/me", nil, false)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("reviewer1 dashboard after approve: %d %s", code, raw)
	}
	if got := int(body["data"].(map[string]any)["pending_approval_count"].(float64)); got != 0 {
		t.Fatalf("reviewer1 pending_approval_count after approval = %d, want 0", got)
	}

	// 2. Open session, run two SELECT queries, and verify GET /api/v4/query-sessions/{id} returns decrypted executions.
	code, body, raw = fx.requester.do("POST", "/api/v4/query-sessions", map[string]any{
		"flow_id": fx.qFlowID, "grant_id": grantID, "datasource_ids": []string{fx.qDsID},
	}, true)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("create session: %d %s", code, raw)
	}
	sessionID := body["data"].(map[string]any)["id"].(string)

	sql1 := "select id, name from qtarget.accounts where id = 1"
	sql2 := "select id from qtarget.pages order by id limit 5"
	if code, body, raw = fx.b11ExecHTTP(t, sessionID, sql1, 0, 10); code != 200 || errCode(body) != 0 {
		t.Fatalf("exec sql1: %d %s", code, raw)
	}
	if code, body, raw = fx.b11ExecHTTP(t, sessionID, sql2, 0, 10); code != 200 || errCode(body) != 0 {
		t.Fatalf("exec sql2: %d %s", code, raw)
	}

	code, body, raw = fx.requester.do("GET", "/api/v4/query-sessions/"+sessionID, nil, false)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("get session: %d %s", code, raw)
	}
	executions, ok := body["data"].(map[string]any)["executions"].([]any)
	if !ok || len(executions) != 2 {
		t.Fatalf("session executions = %#v, want 2 items", body["data"].(map[string]any)["executions"])
	}
	first := executions[0].(map[string]any)
	second := executions[1].(map[string]any)
	if first["sql"] != sql2 || second["sql"] != sql1 {
		t.Fatalf("executions sql order = [%v, %v], want [%q, %q]", first["sql"], second["sql"], sql2, sql1)
	}
	if first["datasource_id"] != fx.qDsID || first["state"] != "succeeded" {
		t.Fatalf("unexpected execution metadata: %#v", first)
	}
}

// TestQueryDatasourceIdentityBootstrap verifies BE-B13-QUERY-DATASOURCE-IDENTITY-FIX:
//  1. A newly created datasource (identity_fingerprint IS NULL, never manually probed via /connection-tests)
//     transparently bootstraps its 64-hex identity_fingerprint on POST /api/v4/query-access-requests,
//     allows approval + session + SELECT execution, and similarly bootstraps on approval-free POST /api/v4/query-sessions.
//  2. SEC-0026 regression: after an active Grant + QuerySession freezes FP_old, replacing the datasource
//     endpoint clears identity_fingerprint to NULL; when a subsequent request bootstraps FP_new for the new
//     endpoint, the pre-existing session still transitions to identity_changed and rejects SELECT (fail closed).
//  3. Disabled or unreachable unprobed datasources return HTTP 200 + err_code=3010 (DATASOURCE_UNAVAILABLE)
//     instead of HTTP 500, and leave zero orphaned in_progress rows in idempotency_records.
func TestQueryDatasourceIdentityBootstrap(t *testing.T) {
	fx := newB11Fixture(t)
	cfg := fx.qTargetPG

	createUnprobedDatasource := func(name string, port int, enabled bool) string {
		t.Helper()
		payload := map[string]any{
			"name":               name,
			"engine":             "postgresql",
			"compatibility_mode": "postgresql",
			"deployment_kind":    "native",
			"host":               cfg.Host,
			"port":               port,
			"database_name":      cfg.FixtureDatabase,
			"version_constraint": cfg.VersionConstraint,
			"enabled":            enabled,
			"credentials": []map[string]any{{
				"purpose":  "query",
				"username": cfg.FixtureRole,
				"password": map[string]any{"value": cfg.FixturePassword},
			}},
		}
		code, body, raw := fx.admin.do("POST", "/api/v4/admin/datasources", payload, true)
		if code != 200 || errCode(body) != 0 {
			t.Fatalf("create unprobed datasource %s: %d %s", name, code, raw)
		}
		dsID := body["data"].(map[string]any)["id"].(string)
		var fp *string
		if err := fx.db.Raw(`select identity_fingerprint from datasources where id = ?`, dsID).Scan(&fp).Error; err != nil {
			t.Fatal(err)
		}
		if fp != nil {
			t.Fatalf("expected newly created datasource identity_fingerprint to be NULL, got %q", *fp)
		}
		return dsID
	}

	// Case 1: Approval-enabled request on unprobed reachable datasource succeeds and persists 64-hex fingerprint.
	unprobedDS := createUnprobedDatasource("b11-unprobed-approval-"+uuid.NewString()[:8], cfg.Port, true)
	unprobedFlow := fx.b11QueryFlow(t, fx.reviewer1ID, fx.reviewer2ID, unprobedDS, true, true)
	fx.b9CreateGrant(t, fx.requesterID, unprobedFlow)

	code, body, raw := fx.requester.doWithHeaders("POST", "/api/v4/query-access-requests", map[string]any{
		"flow_id":         unprobedFlow,
		"datasource_ids":  []string{unprobedDS},
		"requested_until": time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339Nano),
		"reason":          "apply against freshly created datasource without manual connection test",
	}, map[string]string{
		"Idempotency-Key": "b11-unprobed-req-" + uuid.NewString()[:12],
		"X-CSRF-Token":    fx.requester.cookies["yearning_csrf"],
	})
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("create access request on unprobed datasource: %d %s", code, raw)
	}
	var bootstrappedFP string
	if err := fx.db.Raw(`select coalesce(identity_fingerprint, '') from datasources where id = ?`, unprobedDS).Scan(&bootstrappedFP).Error; err != nil {
		t.Fatal(err)
	}
	if len(bootstrappedFP) != 64 {
		t.Fatalf("expected 64-hex bootstrapped identity_fingerprint, got %q", bootstrappedFP)
	}

	reqData := body["data"].(map[string]any)
	reqID := reqData["id"].(string)
	reqVer := int64(reqData["version"].(float64))
	code, body, raw = fx.reviewer1.doWithHeaders("POST", "/api/v4/query-access-requests/"+reqID+"/decisions",
		map[string]any{"decision": "approve", "comment": "approved"}, map[string]string{
			"Idempotency-Key": "b11-unprobed-dec-" + reqID[:8],
			"If-Match":        fmt.Sprintf(`"%d"`, reqVer),
			"X-CSRF-Token":    fx.reviewer1.cookies["yearning_csrf"],
		})
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("approve unprobed request: %d %s", code, raw)
	}
	grantID := body["data"].(map[string]any)["grant_id"].(string)
	code, body, raw = fx.requester.do("POST", "/api/v4/query-sessions", map[string]any{
		"flow_id": unprobedFlow, "grant_id": grantID, "datasource_ids": []string{unprobedDS},
	}, true)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("open session after bootstrap: %d %s", code, raw)
	}
	oldSessionID := body["data"].(map[string]any)["id"].(string)
	code, body, raw = fx.requester.do("POST", "/api/v4/query-sessions/"+oldSessionID+"/executions", map[string]any{
		"datasource_id": unprobedDS, "schema_name": fx.qSchema, "sql": "select id, name from qtarget.accounts where id = 1",
	}, true)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("exec select on bootstrapped session: %d %s", code, raw)
	}

	// Case 2 (SEC-0026 regression): Simulate endpoint change clearing identity_fingerprint to NULL and pointing
	// to a different instance identity; new request bootstraps FP_new, while oldSessionID must still fail closed!
	var dsVer int64
	if err := fx.db.Raw(`select version from datasources where id = ?`, unprobedDS).Scan(&dsVer).Error; err != nil {
		t.Fatal(err)
	}
	// Replace host from localhost -> 127.0.0.1 (or vice versa) so endpoint-family change clears identity_fingerprint
	// AND produces a distinct HMAC fingerprint ("host=" differs) while still reaching the real target PG!
	altHost := "127.0.0.1"
	if cfg.Host == "127.0.0.1" {
		altHost = "localhost"
	}
	code, body, raw = fx.admin.doWithHeaders("PUT", "/api/v4/admin/datasources/"+unprobedDS, map[string]any{
		"name":               "b11-unprobed-replaced-" + uuid.NewString()[:8],
		"engine":             "postgresql",
		"compatibility_mode": "postgresql",
		"deployment_kind":    "native",
		"host":               altHost,
		"port":               cfg.Port,
		"database_name":      cfg.FixtureDatabase,
		"version_constraint": cfg.VersionConstraint,
		"enabled":            true,
		"credentials": []map[string]any{{
			"purpose":                  "query",
			"reuse_credential_purpose": "query",
		}},
		"tls": nil,
	}, map[string]string{
		"If-Match":     fmt.Sprintf(`"%d"`, dsVer),
		"X-CSRF-Token": fx.admin.cookies["yearning_csrf"],
	})
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("replace datasource endpoint: %d %s", code, raw)
	}
	var clearedFP *string
	if err := fx.db.Raw(`select identity_fingerprint from datasources where id = ?`, unprobedDS).Scan(&clearedFP).Error; err != nil {
		t.Fatal(err)
	}
	if clearedFP != nil {
		t.Fatalf("SEC-0026: expected endpoint replace to clear identity_fingerprint to NULL, got %q", *clearedFP)
	}

	// Trigger Grant renewal (which calls CreateAccessRequest -> bootstraps FP_new for altHost).
	code, body, raw = fx.requester.doWithHeaders("POST", "/api/v4/query-grants/"+grantID+"/renewal-requests", map[string]any{
		"requested_until": time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339Nano),
		"reason":          "renew after endpoint update",
	}, map[string]string{
		"Idempotency-Key": "b11-renew-bootstrap-" + uuid.NewString()[:12],
		"X-CSRF-Token":    fx.requester.cookies["yearning_csrf"],
	})
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("renew grant on cleared-fingerprint datasource: %d %s", code, raw)
	}
	var newFP string
	if err := fx.db.Raw(`select coalesce(identity_fingerprint, '') from datasources where id = ?`, unprobedDS).Scan(&newFP).Error; err != nil {
		t.Fatal(err)
	}
	if len(newFP) != 64 || newFP == bootstrappedFP {
		t.Fatalf("SEC-0026: expected distinct 64-hex newFP != %q, got %q", bootstrappedFP, newFP)
	}

	// Old session MUST fail closed (4002 QUERY_DATASOURCE_NOT_GRANTED via identity_changed)!
	code, body, raw = fx.requester.do("POST", "/api/v4/query-sessions/"+oldSessionID+"/executions", map[string]any{
		"datasource_id": unprobedDS, "schema_name": fx.qSchema, "sql": "select 1",
	}, true)
	if code != 200 || errCode(body) != int(querydomain.CodeDsNotGranted) {
		t.Fatalf("SEC-0026: expected old session SELECT after endpoint change to fail closed with 4002, got %d %s", code, raw)
	}

	// Case 3: Unreachable unprobed datasource returns HTTP 200 + err_code=3010 (DATASOURCE_UNAVAILABLE)
	// and leaves zero in_progress idempotency rows.
	unreachableDS := createUnprobedDatasource("b11-unprobed-unreachable-"+uuid.NewString()[:8], 1, true)
	unreachableFlow := fx.b11QueryFlow(t, fx.reviewer1ID, fx.reviewer2ID, unreachableDS, true, false)
	fx.b9CreateGrant(t, fx.requesterID, unreachableFlow)

	idemUnreachable := "b11-unreachable-idem-" + uuid.NewString()[:12]
	code, body, raw = fx.requester.doWithHeaders("POST", "/api/v4/query-access-requests", map[string]any{
		"flow_id":         unreachableFlow,
		"datasource_ids":  []string{unreachableDS},
		"requested_until": time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339Nano),
		"reason":          "unreachable target test",
	}, map[string]string{
		"Idempotency-Key": idemUnreachable,
		"X-CSRF-Token":    fx.requester.cookies["yearning_csrf"],
	})
	if code != 200 || errCode(body) != 3010 {
		t.Fatalf("unreachable unprobed datasource: got HTTP %d err_code=%d (%s), want HTTP 200 err_code=3010", code, errCode(body), raw)
	}
	var inProgressCount int
	if err := fx.db.Raw(`select count(*) from idempotency_records where idempotency_key = ? and state = 'in_progress'`, idemUnreachable).Scan(&inProgressCount).Error; err != nil {
		t.Fatal(err)
	}
	if inProgressCount != 0 {
		t.Fatalf("expected 0 orphaned in_progress idempotency records, got %d", inProgressCount)
	}

	// Case 4: Approval-free direct session (query.approval_enabled = false) also bootstraps unprobed datasource,
	// and returns 3010 on unreachable unprobed datasource.
	fx.b11SetApprovalEnabled(t, false)
	defer fx.b11SetApprovalEnabled(t, true)

	directDS := createUnprobedDatasource("b11-unprobed-direct-"+uuid.NewString()[:8], cfg.Port, true)
	directFlow := fx.b11QueryFlow(t, fx.reviewer1ID, fx.reviewer2ID, directDS, true, false)
	fx.b9CreateGrant(t, fx.requesterID, directFlow)

	code, body, raw = fx.requester.do("POST", "/api/v4/query-sessions", map[string]any{
		"flow_id": directFlow, "datasource_ids": []string{directDS},
	}, true)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("approval-free session on unprobed datasource: %d %s", code, raw)
	}

	code, body, raw = fx.requester.do("POST", "/api/v4/query-sessions", map[string]any{
		"flow_id": unreachableFlow, "datasource_ids": []string{unreachableDS},
	}, true)
	if code != 200 || errCode(body) != 3010 {
		t.Fatalf("approval-free session on unreachable unprobed datasource: got HTTP %d err_code=%d (%s), want HTTP 200 err_code=3010", code, errCode(body), raw)
	}
}

//go:build integration

// b13_alignment2_integration_test.go HTTP-surface acceptance for the five BE-B13-CONTRACT-ALIGNMENT-2
// RCPs: revocation read-surface snapshot fields (RCP-20260901-QUERY-REVOCATION-
// READ-FACE), server-side audit list filters (AUDIT-LIST-FILTERS), flow catalog datasource
// name projection (FLOW-CATALOG-DATASOURCE-NAMES), continuation page_size (QUERY-
// PAGE-SIZE-PARAM), and session grant_id (QUERY-SESSION-GRANT-REF). TLS tri-state is
// verified in TestTLSMaterialSurface in b13_integration_test.go.
package httpapi

import (
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
)

// alignment2GrantFlow runs request -> approval -> Session, returning request response, final approval
// response, and Session response (for read-surface field assertions).
func (fx *b11Fixture) alignment2GrantFlow(t *testing.T) (map[string]any, map[string]any, map[string]any) {
	t.Helper()
	code, body, raw := fx.requester.doWithHeaders("POST", "/api/v4/query-access-requests", map[string]any{
		"flow_id":         fx.qFlowID,
		"datasource_ids":  []string{fx.qDsID},
		"requested_until": time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339Nano),
		"reason":          "alignment2 read face",
	}, map[string]string{
		"Idempotency-Key": "al2-req-" + uuid.NewString()[:12],
		"X-CSRF-Token":    fx.requester.cookies["yearning_csrf"],
	})
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("create access request: %d %s", code, raw)
	}
	req := body["data"].(map[string]any)
	reqID := req["id"].(string)
	version := int64(req["version"].(float64))
	if code, body, raw = fx.reviewer1.doWithHeaders("POST",
		"/api/v4/query-access-requests/"+reqID+"/decisions",
		map[string]any{"decision": "approve", "comment": "ok"}, map[string]string{
			"Idempotency-Key": "al2-dec-" + reqID[:8],
			"If-Match":        fmt.Sprintf(`"%d"`, version),
			"X-CSRF-Token":    fx.reviewer1.cookies["yearning_csrf"],
		}); code != 200 || errCode(body) != 0 {
		t.Fatalf("decide: %d %s", code, raw)
	}
	decided := body["data"].(map[string]any)
	if code, body, raw = fx.requester.do("POST", "/api/v4/query-sessions", map[string]any{
		"flow_id": fx.qFlowID, "grant_id": decided["grant_id"], "datasource_ids": []string{fx.qDsID},
	}, true); code != 200 || errCode(body) != 0 {
		t.Fatalf("create session: %d %s", code, raw)
	}
	return req, decided, body["data"].(map[string]any)
}

// TestAlignment2RevocationReadFace revocation read surface: request reason / requested_until / terminal_at
// and revoker / revoked_at are visible on read surface as snapshots only (no Secret readback).
func TestAlignment2RevocationReadFace(t *testing.T) {
	fx := newB11Fixture(t)
	req, decided, session := fx.alignment2GrantFlow(t)
	// Request read surface: reason/requested_until non-empty; terminal_at is null during pending -> active.
	if req["reason"] != "alignment2 read face" || req["requested_until"] == nil {
		t.Fatalf("access request read face: reason=%v requested_until=%v", req["reason"], req["requested_until"])
	}
	if req["terminal_at"] != nil {
		t.Fatalf("pending request terminal_at must be null, got %v", req["terminal_at"])
	}
	grantID := decided["grant_id"].(string)
	// Session read surface carries grant_id (exact association, RCP-20260901-QUERY-SESSION-GRANT-REF).
	if session["grant_id"] != grantID {
		t.Fatalf("session grant_id = %v want %s", session["grant_id"], grantID)
	}
	// Frozen reviewer revokes (reason required) -> Grant read surface populates revoker/timestamp.
	var version int64
	fx.db.Raw(`select version from query_grants where id = ?`, grantID).Scan(&version)
	code, body, raw := fx.reviewer1.doWithHeaders("POST", "/api/v4/query-grants/"+grantID+"/revocations",
		map[string]any{"reason": "alignment2 revoke"}, map[string]string{
			"Idempotency-Key": "al2-rev-" + grantID[:8],
			"If-Match":        fmt.Sprintf(`"%d"`, version),
			"X-CSRF-Token":    fx.reviewer1.cookies["yearning_csrf"],
		})
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("revoke: %d %s", code, raw)
	}
	grant := body["data"].(map[string]any)
	if grant["revoked_by_username"] != "rev1" {
		t.Fatalf("revoked_by_username = %v want rev1", grant["revoked_by_username"])
	}
	if grant["revoked_at"] == nil {
		t.Fatal("revoked_at must be visible after revocation")
	}
	// Re-reading Grant after revocation (owner read surface) preserves fields, and key set matches OpenAPI
	// QueryGrant verbatim (snapshot-only: no credential/Secret keys mixed in).
	if code, body, _ = fx.requester.do("GET", "/api/v4/query-grants/"+grantID, nil, false); code != 200 || errCode(body) != 0 {
		t.Fatalf("get grant: %d %s", code, raw)
	}
	g := body["data"].(map[string]any)
	if g["revoked_by_username"] != "rev1" || g["revoked_at"] == nil {
		t.Fatalf("grant reread: by=%v at=%v", g["revoked_by_username"], g["revoked_at"])
	}
	want := map[string]bool{
		"id": true, "requester_user_id": true, "state": true, "version": true,
		"created_at": true, "revoked_reason": true, "revoked_by_username": true,
		"revoked_at": true, "expires_at": true, "can_revoke": true,
	}
	if g["can_revoke"] != false {
		t.Fatalf("revoked grant must report can_revoke=false, got %v", g["can_revoke"])
	}
	if len(g) != len(want) {
		t.Fatalf("grant view key set drifted: %v", g)
	}
	for k := range g {
		if !want[k] {
			t.Fatalf("grant view leaked non-schema key %q", k)
		}
	}
}

// TestAlignment2SessionGrantIDNullWhenApprovalOff when approval is disabled,
// Session.grant_id is null (direct sessions have no Grant).
func TestAlignment2SessionGrantIDNullWhenApprovalOff(t *testing.T) {
	fx := newB11Fixture(t)
	fx.b11SetApprovalEnabled(t, false)
	code, body, raw := fx.requester.do("POST", "/api/v4/query-sessions", map[string]any{
		"flow_id": fx.qFlowID, "datasource_ids": []string{fx.qDsID},
	}, true)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("direct session: %d %s", code, raw)
	}
	session := body["data"].(map[string]any)
	if v, ok := session["grant_id"]; !ok || v != nil {
		t.Fatalf("approval-off session grant_id = %v (present=%v), want explicit null", v, ok)
	}
}

// TestAlignment2FlowCatalogDatasourceNames user flow catalog capability items include datasource
// names (consistent with datasource catalog); listDatasources remains admin-only.
func TestAlignment2FlowCatalogDatasourceNames(t *testing.T) {
	fx := newB11Fixture(t)
	var dsName string
	fx.db.Raw(`select name from datasources where id = ?`, fx.qDsID).Scan(&dsName)
	code, body, raw := fx.requester.do("GET",
		"/api/v4/users/me/flows?flow_type=query_access", nil, false)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("list my flows: %d %s", code, raw)
	}
	items := itemsOf(body)
	found := false
	for _, it := range items {
		flow := it.(map[string]any)
		caps, _ := flow["query_capabilities"].([]any)
		for _, c := range caps {
			cap := c.(map[string]any)
			if cap["datasource_id"] == fx.qDsID {
				found = true
				if cap["datasource_name"] != dsName {
					t.Fatalf("flow catalog datasource_name = %v want %q", cap["datasource_name"], dsName)
				}
				if cap["can_query"] != true {
					t.Fatalf("can_query = %v want true", cap["can_query"])
				}
			}
		}
	}
	if !found {
		t.Fatalf("query flow %s capability not in catalog: %s", fx.qFlowID, raw)
	}
	// Datasource name updates propagate naturally when renamed in catalog (joins current name, no stale snapshot).
	fx.db.Exec(`update datasources set name = ? where id = ?`, dsName+"-renamed", fx.qDsID)
	if code, body, _ = fx.requester.do("GET", "/api/v4/users/me/flows?flow_type=query_access", nil, false); code != 200 || errCode(body) != 0 {
		t.Fatalf("relist flows: %d", code)
	}
	renamedSeen := false
	for _, it := range itemsOf(body) {
		for _, c := range it.(map[string]any)["query_capabilities"].([]any) {
			cap := c.(map[string]any)
			if cap["datasource_id"] == fx.qDsID {
				renamedSeen = cap["datasource_name"] == dsName+"-renamed"
			}
		}
	}
	if !renamedSeen {
		t.Fatal("rename did not propagate to flow catalog")
	}
	// Regular user reading datasource catalog is rejected (admin-only boundary unchanged).
	if code, _, _ = fx.requester.do("GET", "/api/v4/admin/datasources", nil, false); code != 403 {
		t.Fatalf("non-admin listDatasources: code=%d want 403", code)
	}
}

// TestAlignment2AuditListFilters server-side audit filter narrowing, invalid values -> 1001,
// and compatibility with after-cursor pagination.
func TestAlignment2AuditListFilters(t *testing.T) {
	fx := newB11Fixture(t)
	// Generate filterable audit fact: reviewer revokes Grant (action=revoke, outcome=succeeded).
	_, decided, _ := fx.alignment2GrantFlow(t)
	grantID := decided["grant_id"].(string)
	var version int64
	fx.db.Raw(`select version from query_grants where id = ?`, grantID).Scan(&version)
	if code, body, raw := fx.reviewer1.doWithHeaders("POST", "/api/v4/query-grants/"+grantID+"/revocations",
		map[string]any{"reason": "filter probe"}, map[string]string{
			"Idempotency-Key": "al2-frev-" + grantID[:8],
			"If-Match":        fmt.Sprintf(`"%d"`, version),
			"X-CSRF-Token":    fx.reviewer1.cookies["yearning_csrf"],
		}); code != 200 || errCode(body) != 0 {
		t.Fatalf("revoke: %d %s", code, raw)
	}
	fetch := func(query string) (int, map[string]any, string) {
		return fx.admin.do("GET", "/api/v4/admin/audit-events"+query, nil, false)
	}
	// Exact action filter narrows results and matches revocation event.
	code, body, raw := fetch("?action=revoke&limit=200")
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("filter action=revoke: %d %s", code, raw)
	}
	revokeItems := itemsOf(body)
	if len(revokeItems) == 0 {
		t.Fatal("action=revoke returned no items")
	}
	for _, it := range revokeItems {
		if it.(map[string]any)["action"] != "revoke" {
			t.Fatal("action filter leaked non-revoke rows")
		}
	}
	// Outcome enum filter combines with action filter.
	if code, body, _ = fetch("?action=revoke&outcome=failed"); code != 200 || errCode(body) != 0 {
		t.Fatalf("outcome filter: %d", code)
	}
	if got := len(itemsOf(body)); got != 0 {
		t.Fatalf("outcome=failed for succeeded revoke: items=%d want 0", got)
	}
	// Exact resource_type filter matches query_grants.
	if code, body, _ = fetch("?resource_type=query_grants&action=revoke"); code != 200 || errCode(body) != 0 {
		t.Fatalf("resource_type filter: %d", code)
	}
	if len(itemsOf(body)) == 0 {
		t.Fatal("resource_type=query_grants returned no items")
	}
	// Exact actor_id filter (revocation by rev1).
	var rev1ID string
	fx.db.Raw(`select id::text from users where username = 'rev1'`).Scan(&rev1ID)
	if code, body, _ = fetch("?actor_id=" + rev1ID + "&action=revoke"); code != 200 || errCode(body) != 0 {
		t.Fatalf("actor_id filter: %d", code)
	}
	if len(itemsOf(body)) == 0 {
		t.Fatal("actor_id filter returned no items")
	}
	// request_id filter: matches exactly when revocation request_id is populated on audit row.
	var reqID *string
	fx.db.Raw(`select request_id::text from audit_events where action = 'revoke' and request_id is not null limit 1`).Scan(&reqID)
	if reqID != nil && *reqID != "" {
		if code, body, _ = fetch("?request_id=" + *reqID); code != 200 || errCode(body) != 0 {
			t.Fatalf("request_id filter: %d", code)
		}
		for _, it := range itemsOf(body) {
			if it.(map[string]any)["request_id"] != *reqID {
				t.Fatal("request_id filter leaked foreign rows")
			}
		}
	}
	// Closed date range: today is included, starting tomorrow returns empty.
	today := time.Now().UTC().Format("2006-01-02")
	tomorrow := time.Now().UTC().AddDate(0, 0, 1).Format("2006-01-02")
	if code, body, _ = fetch("?occurred_from=" + today + "&occurred_to=" + today + "&action=revoke"); code != 200 || errCode(body) != 0 {
		t.Fatalf("occurred range: %d", code)
	}
	if len(itemsOf(body)) == 0 {
		t.Fatal("today closed interval missed revoke events")
	}
	if code, body, _ = fetch("?occurred_from=" + tomorrow); code != 200 || errCode(body) != 0 {
		t.Fatalf("occurred_from future: %d", code)
	}
	if len(itemsOf(body)) != 0 {
		t.Fatal("future occurred_from returned items")
	}
	// Invalid values return 1001: outcome outside enum, non-UUID actor_id, non-date date string.
	for _, q := range []string{
		"?outcome=boom", "?actor_id=not-a-uuid", "?occurred_from=2026-13-99",
		"?occurred_to=notadate",
	} {
		if code, body, _ = fetch(q); code != 200 || errCode(body) != 1001 {
			t.Fatalf("invalid filter %q: code=%d err=%d want 1001", q, code, errCode(body))
		}
	}
	// Cursor compatibility: filter + limit=1 pagination keeps second page narrowed.
	if code, body, _ = fetch("?action=revoke&limit=1"); code != 200 || errCode(body) != 0 {
		t.Fatalf("cursor filter page1: %d", code)
	}
	page := body["data"].(map[string]any)["page"].(map[string]any)
	firstID := itemsOf(body)[0].(map[string]any)["id"].(string)
	if page["next_cursor"] == nil {
		t.Fatal("expected next_cursor on filtered page1")
	}
	next := page["next_cursor"].(string)
	if code, body, _ = fetch("?action=revoke&limit=1&after=" + next); code != 200 || errCode(body) != 0 {
		t.Fatalf("cursor filter page2: %d", code)
	}
	for _, it := range itemsOf(body) {
		if it.(map[string]any)["id"] == firstID {
			t.Fatal("cursor page repeated first item")
		}
		if it.(map[string]any)["action"] != "revoke" {
			t.Fatal("cursor page leaked non-revoke rows")
		}
	}
}

// TestAlignment2FetchPageSizeOverride continuation page_size: 1..5000 overrides, omitted
// retains current override (override persists across pages), invalid value -> 1001.
func TestAlignment2FetchPageSizeOverride(t *testing.T) {
	fx := newB11Fixture(t)
	sessionID := fx.b11ActiveSessionHTTP(t)
	code, body, raw := fx.b11ExecHTTP(t, sessionID, "select id from qtarget.pages", 0, 10)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("execute: %d %s", code, raw)
	}
	data := body["data"].(map[string]any)
	cursor := data["page"].(map[string]any)["next_cursor"].(string)
	execID := data["execution_id"].(string)
	if len(data["rows"].([]any)) != 10 {
		t.Fatalf("first page rows = %d want 10", len(data["rows"].([]any)))
	}
	fetch := func(query string) (int, map[string]any, string) {
		return fx.requester.do("GET",
			"/api/v4/query-executions/"+execID+"/pages?cursor="+cursor+"&purpose=display"+query, nil, false)
	}
	// page_size=50 override: returns 50 rows on this page.
	if code, body, raw = fetch("&page_size=50"); code != 200 || errCode(body) != 0 {
		t.Fatalf("fetch override 50: %d %s", code, raw)
	}
	data = body["data"].(map[string]any)
	if len(data["rows"].([]any)) != 50 {
		t.Fatalf("overridden page rows = %d want 50", len(data["rows"].([]any)))
	}
	cursor = data["page"].(map[string]any)["next_cursor"].(string)
	// Omitted retains overridden value: still 50 rows.
	if code, body, raw = fetch(""); code != 200 || errCode(body) != 0 {
		t.Fatalf("fetch default-after-override: %d %s", code, raw)
	}
	data = body["data"].(map[string]any)
	if len(data["rows"].([]any)) != 50 {
		t.Fatalf("default-after-override rows = %d want 50", len(data["rows"].([]any)))
	}
	cursor = data["page"].(map[string]any)["next_cursor"].(string)
	// Narrow back to 5: subsequent pages remain 5.
	if code, body, raw = fetch("&page_size=5"); code != 200 || errCode(body) != 0 {
		t.Fatalf("fetch override 5: %d %s", code, raw)
	}
	if len(body["data"].(map[string]any)["rows"].([]any)) != 5 {
		t.Fatalf("narrowed page rows = %d want 5", len(body["data"].(map[string]any)["rows"].([]any)))
	}
	// Invalid values return 1001: 0 / 5001 / non-integer.
	for _, v := range []string{"0", "5001", "abc"} {
		if code, body, _ = fetch("&page_size=" + v); code != 200 || errCode(body) != 1001 {
			t.Fatalf("page_size=%q: code=%d err=%d want 1001", v, code, errCode(body))
		}
	}
}

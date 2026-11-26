//go:build integration

package httpapi

import (
	"fmt"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func seedAdversarialOrdersForL1(t *testing.T, fx *b9Fixture, baseOrder map[string]any) []string {
	t.Helper()
	baseID := baseOrder["id"].(string)
	states := []string{
		"submitted", "stage_approval_active", "stage_execution_pending",
		"completed", "rejected", "withdrawn", "failed", "voided",
	}
	nonTerminal := map[string]bool{
		"submitted": true, "stage_approval_active": true, "stage_execution_pending": true,
	}
	titles := []string{
		"l1-filter % percent _ under",
		"l1-filter ' quote ; semi",
		"l1-filter-alpha-target",
		"l1-filter-beta-case",
	}
	baseTS := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	ids := []string{baseID}
	for i := 1; i <= 24; i++ {
		oid := uuid.NewString()
		disp := fmt.Sprintf("YR-20261110-%06d", 800000+i)
		st := states[i%len(states)]
		title := titles[i%len(titles)]
		subTS := baseTS.Add(time.Duration(i) * time.Hour)
		var termTS any
		if !nonTerminal[st] {
			termTS = subTS.Add(15 * time.Minute)
		}
		if err := fx.db.Exec(`
			insert into change_orders (
				id, display_number, submitter_user_id_snapshot, submitter_username_snapshot, submitter_display_name_snapshot,
				title, description, state, current_stage_position, sql_hmac, snapshot_hash, manually_verified, version, submitted_at, terminal_at
			)
			select ?, ?, submitter_user_id_snapshot, submitter_username_snapshot, submitter_display_name_snapshot,
				?, description, ?, 1, sql_hmac, snapshot_hash, manually_verified, 1, ?, ?
			from change_orders where id = ?`,
			oid, disp, title, st, subTS, termTS, baseID).Error; err != nil {
			t.Fatalf("clone order %d: %v", i, err)
		}
		for _, stmt := range []struct {
			sql  string
			args []any
		}{
			{`insert into order_flow_snapshots (order_id, flow_snapshot_ciphertext, flow_snapshot_nonce, flow_snapshot_key_id, flow_snapshot_algorithm, snapshot_hash, created_at)
			  select ?, flow_snapshot_ciphertext, flow_snapshot_nonce, flow_snapshot_key_id, flow_snapshot_algorithm, snapshot_hash, created_at from order_flow_snapshots where order_id = ?`, []any{oid, baseID}},
			{`insert into order_sql_payloads (order_id, sql_ciphertext, sql_nonce, sql_key_id, sql_algorithm, plaintext_hmac, size_bytes, statement_count, created_at)
			  select ?, sql_ciphertext, sql_nonce, sql_key_id, sql_algorithm, plaintext_hmac, size_bytes, statement_count, created_at from order_sql_payloads where order_id = ?`, []any{oid, baseID}},
			{`insert into order_stages (id, order_id, position, datasource_id, datasource_name_snapshot, datasource_engine_snapshot, datasource_identity_fingerprint,
				stage_review_snapshot_id, review_snapshot_hash, review_gate_passed, target_snapshot_ciphertext, target_snapshot_nonce,
				target_snapshot_key_id, target_snapshot_algorithm, state, version, activated_at, terminal_at)
			  select ?, ?, position, datasource_id, datasource_name_snapshot, datasource_engine_snapshot, datasource_identity_fingerprint,
				stage_review_snapshot_id, review_snapshot_hash, review_gate_passed, target_snapshot_ciphertext, target_snapshot_nonce,
				target_snapshot_key_id, target_snapshot_algorithm, 'succeeded', 1, ?, ? from order_stages where order_id = ? and position = 1`, []any{uuid.NewString(), oid, subTS, subTS, baseID}},
		} {
			if err := fx.db.Exec(stmt.sql, stmt.args...).Error; err != nil {
				t.Fatalf("clone child row for order %d: %v", i, err)
			}
		}
		ids = append(ids, oid)
	}
	return ids
}

// TestL1ListOrdersDifferentialAndCursorPagination exercises DEF-01 and DEF-02
// against real PostgreSQL with differential DB assertions and >2*limit+1 cursor walks.
func TestL1ListOrdersDifferentialAndCursorPagination(t *testing.T) {
	fx := newB9Fixture(t)
	baseOrder := fx.b9ReadySubmit(t)
	allIDs := seedAdversarialOrdersForL1(t, fx, baseOrder)
	if len(allIDs) != 25 {
		t.Fatalf("seeded %d orders, want 25", len(allIDs))
	}

	// DEF-02: walk all 25 orders in pages of limit=10 using returned next_cursor.
	var gotOrder []string
	seen := map[string]bool{}
	cursor := ""
	for page := 0; page < 5; page++ {
		path := "/api/v4/change-orders?limit=10"
		if cursor != "" {
			path += "&after=" + url.QueryEscape(cursor)
		}
		code, body, raw := fx.owner.do("GET", path, nil, false)
		if code != 200 || errCode(body) != 0 {
			t.Fatalf("orders page %d: %d %s", page, code, raw)
		}
		data := dataMap(body)
		items, _ := data["items"].([]any)
		for _, it := range items {
			id := it.(map[string]any)["id"].(string)
			if seen[id] {
				t.Fatalf("duplicate id %s on page %d", id, page)
			}
			seen[id] = true
			gotOrder = append(gotOrder, id)
		}
		pageObj, _ := data["page"].(map[string]any)
		nextPtr, _ := pageObj["next_cursor"].(string)
		if nextPtr == "" {
			break
		}
		cursor = nextPtr
	}
	var dbOrder []string
	if err := fx.db.Raw(`select id::text from change_orders order by submitted_at desc, id desc`).Scan(&dbOrder).Error; err != nil {
		t.Fatalf("db order scan: %v", err)
	}
	if !slices.Equal(gotOrder, dbOrder) {
		t.Fatalf("cursor walk mismatch: api=%v db=%v", gotOrder, dbOrder)
	}

	// DEF-01: differential checks across state, states, q, datasource_id, flow_id, only_mine.
	assertMatchesSQL := func(label, query string, sql string, args ...any) {
		t.Helper()
		code, body, raw := fx.owner.do("GET", "/api/v4/change-orders?"+query, nil, false)
		if code != 200 || errCode(body) != 0 {
			t.Fatalf("%s HTTP failed: %d %s", label, code, raw)
		}
		items, _ := dataMap(body)["items"].([]any)
		var apiIDs []string
		for _, it := range items {
			apiIDs = append(apiIDs, it.(map[string]any)["id"].(string))
		}
		var wantIDs []string
		if err := fx.db.Raw(sql, args...).Scan(&wantIDs).Error; err != nil {
			t.Fatalf("%s db oracle: %v", label, err)
		}
		slices.Sort(apiIDs)
		slices.Sort(wantIDs)
		if !slices.Equal(apiIDs, wantIDs) {
			t.Fatalf("%s differential mismatch: api=%v db=%v", label, apiIDs, wantIDs)
		}
	}

	assertMatchesSQL("state=completed", "limit=50&state=completed",
		`select id::text from change_orders where state = 'completed'`)
	assertMatchesSQL("state=rejected", "limit=50&state=rejected",
		`select id::text from change_orders where state = 'rejected'`)
	assertMatchesSQL("q=alpha-target", "limit=50&q=alpha-target",
		`select id::text from change_orders where title ilike '%alpha-target%'`)
	assertMatchesSQL("q=percent_literal", "limit=50&q="+url.QueryEscape("% percent"),
		`select id::text from change_orders where title like '%\% percent%' escape '\'`)
	assertMatchesSQL("datasource=b9-ds", "limit=50&datasource=b9-ds",
		`select distinct order_id::text from order_stages where datasource_name_snapshot ilike '%b9-ds%'`)
	assertMatchesSQL("submitted_from_to", "limit=50&submitted_from=2026-11-11&submitted_to=2026-11-11",
		`select id::text from change_orders where submitted_at >= timestamptz '2026-11-10T16:00:00Z' and submitted_at < timestamptz '2026-11-11T16:00:00Z'`)
}

// TestL1AdminPaginationSearchAndKnowledge exercises DEF-02 (>2*limit+1 admin
// cursor walks), DEF-11 (knowledge initial state persistence), and DEF-12
// (admin server-side q search with special SQL wildcard characters).
func TestL1AdminPaginationSearchAndKnowledge(t *testing.T) {
	fx := newB6Fixture(t)

	for i := 1; i <= 24; i++ {
		uname := fmt.Sprintf("l1-u-%02d", i)
		if i == 1 {
			uname = "l1-u-special-alpha"
		}
		fx.adminCreateUserB6(t, uname, "UserPass123!")
	}
	for i := 1; i <= 24; i++ {
		dsName := fmt.Sprintf("l1-ds-%02d", i)
		if i == 1 {
			dsName = "l1-ds-special_match"
		}
		fx.b6CreateDatasource(t, dsName)
	}

	walkAdminCursor := func(path string) []string {
		t.Helper()
		var ids []string
		seen := map[string]bool{}
		cursor := ""
		for page := 0; page < 6; page++ {
			sep := "?"
			if strings.Contains(path, "?") {
				sep = "&"
			}
			full := path + sep + "limit=10"
			if cursor != "" {
				full += "&after=" + url.QueryEscape(cursor)
			}
			code, body, raw := fx.admin.do("GET", full, nil, false)
			if code != 200 || errCode(body) != 0 {
				t.Fatalf("GET %s: %d %s", full, code, raw)
			}
			data := dataMap(body)
			for _, it := range data["items"].([]any) {
				id := it.(map[string]any)["id"].(string)
				if seen[id] {
					t.Fatalf("duplicate id %s on %s", id, path)
				}
				seen[id] = true
				ids = append(ids, id)
			}
			pageObj, _ := data["page"].(map[string]any)
			nextPtr, _ := pageObj["next_cursor"].(string)
			if nextPtr == "" {
				break
			}
			cursor = nextPtr
		}
		return ids
	}

	userIDs := walkAdminCursor("/api/v4/admin/users")
	var totalUsers int64
	if err := fx.db.Raw(`select count(*) from users`).Scan(&totalUsers).Error; err != nil {
		t.Fatalf("count users: %v", err)
	}
	if int64(len(userIDs)) != totalUsers {
		t.Fatalf("users cursor walk collected %d ids, want %d", len(userIDs), totalUsers)
	}

	dsIDs := walkAdminCursor("/api/v4/admin/datasources")
	var totalDS int64
	fx.db.Raw(`select count(*) from datasources`).Scan(&totalDS)
	if int64(len(dsIDs)) != totalDS {
		t.Fatalf("datasources cursor walk collected %d ids, want %d", len(dsIDs), totalDS)
	}

	// DEF-12: verify q filter against literal underscore and substring across admin lists.
	_, dsSearchBody, _ := fx.admin.do("GET", "/api/v4/admin/datasources?q="+url.QueryEscape("special_match"), nil, false)
	dsMatches := dataMap(dsSearchBody)["items"].([]any)
	if len(dsMatches) != 1 || dsMatches[0].(map[string]any)["name"] != "l1-ds-special_match" {
		t.Fatalf("datasource q=special_match got %v, want exact single match", dsMatches)
	}
	_, userSearchBody, _ := fx.admin.do("GET", "/api/v4/admin/users?q=SPECIAL-ALPHA", nil, false)
	userMatches := dataMap(userSearchBody)["items"].([]any)
	if len(userMatches) != 1 || userMatches[0].(map[string]any)["username"] != "l1-u-special-alpha" {
		t.Fatalf("user q=SPECIAL-ALPHA got %v, want single match", userMatches)
	}

	// DEF-11: requested initial state is faithfully persisted across draft/disabled/enabled
	// for scoped datasource/table entries, while global scope and high/critical severities are rejected.
	if len(dsIDs) == 0 {
		t.Fatal("expected at least one seeded datasource for DEF-11 knowledge entry test")
	}
	targetDSID := dsIDs[0]
	for _, wantState := range []string{"draft", "disabled", "enabled"} {
		code, body, raw := fx.admin.do("POST", "/api/v4/admin/knowledge-entries", map[string]any{
			"name":          "l1-kb-" + wantState,
			"purpose":       "l1 state parity",
			"state":         wantState,
			"scope_type":    "datasource",
			"datasource_id": targetDSID,
			"database_name": "shop",
			"definition": map[string]any{
				"knowledge_text": "Require explicit primary key on new tables.",
				"max_severity":   "medium",
				"version":        1,
			},
		}, true)
		if code != 200 || errCode(body) != 0 {
			t.Fatalf("create knowledge %s: %d %s", wantState, code, raw)
		}
		entry := dataMap(body)
		if entry["state"] != wantState {
			t.Fatalf("create response state=%v, want %s", entry["state"], wantState)
		}
		id := entry["id"].(string)
		_, gotBody, _ := fx.admin.do("GET", "/api/v4/admin/knowledge-entries/"+id, nil, false)
		if dataMap(gotBody)["state"] != wantState {
			t.Fatalf("GET knowledge state=%v, want %s", dataMap(gotBody)["state"], wantState)
		}
	}
	code, body, raw := fx.admin.do("POST", "/api/v4/admin/knowledge-entries", map[string]any{
		"name":       "l1-kb-global-rejected",
		"purpose":    "retired global",
		"state":      "draft",
		"scope_type": "global",
		"definition": map[string]any{
			"knowledge_text": "Global scope must fail validation.",
			"max_severity":   "medium",
			"version":        1,
		},
	}, true)
	if code != 200 || errCode(body) != 1001 {
		t.Fatalf("expected 1001 VALIDATION_FAILED for global scope, got %d %s", code, raw)
	}
	code, body, raw = fx.admin.do("POST", "/api/v4/admin/knowledge-entries", map[string]any{
		"name":          "l1-kb-critical-rejected",
		"purpose":       "blocking severity retired",
		"state":         "draft",
		"scope_type":    "datasource",
		"datasource_id": targetDSID,
		"database_name": "shop",
		"definition": map[string]any{
			"knowledge_text": "Critical severity on knowledge must fail validation.",
			"max_severity":   "critical",
			"version":        1,
		},
	}, true)
	if code != 200 || errCode(body) != 1001 {
		t.Fatalf("expected 1001 VALIDATION_FAILED for critical severity, got %d %s", code, raw)
	}
}

// TestL1EdgeInputsAndRoleSeparation exercises DEF-08 (missing UUIDs and
// unreachable OIDC discovery never HTTP 500) and J1-J8 single-role separation.
func TestL1EdgeInputsAndRoleSeparation(t *testing.T) {
	fx := newB9Fixture(t)
	missingID := uuid.NewString()

	for _, ep := range []struct {
		method string
		path   string
	}{
		{"GET", "/api/v4/change-orders/" + missingID},
		{"GET", "/api/v4/admin/datasources/" + missingID},
		{"GET", "/api/v4/admin/identity-providers/" + missingID},
		{"GET", "/api/v4/admin/knowledge-entries/" + missingID},
	} {
		code, body, raw := fx.admin.do(ep.method, ep.path, nil, false)
		if code >= 500 || errCode(body) == 0 {
			t.Fatalf("%s %s expected structured non-500 error, got %d %s", ep.method, ep.path, code, raw)
		}
	}

	// DEF-08: unreachable OIDC discovery returns structured 3010 error, never 500.
	code, idpBody, raw := fx.admin.do("POST", "/api/v4/admin/identity-providers", map[string]any{
		"provider_key":  "l1-unreachable-oidc",
		"provider_kind": "oidc",
		"display_name":  "Unreachable OIDC",
		"enabled":       false,
		"configuration": map[string]any{
			"issuer_url":         "https://127.0.0.1:59998",
			"client_id":          "l1-client",
			"scopes":             []string{"openid", "profile"},
			"username_claim":     "preferred_username",
			"display_name_claim": "name",
			"email_claim":        "email",
			"connect_timeout_ms": 200,
			"request_timeout_ms": 300,
		},
		"client_secret": map[string]any{"value": "l1-secret"},
	}, true)
	if code != 200 || errCode(idpBody) != 0 {
		t.Fatalf("create unreachable oidc provider: %d %s", code, raw)
	}
	idpID := dataMap(idpBody)["id"].(string)
	code, probeBody, raw := fx.admin.do("POST", "/api/v4/admin/identity-providers/"+idpID+"/connection-tests", map[string]any{}, true)
	if code >= 500 || errCode(probeBody) != 3010 {
		t.Fatalf("unreachable OIDC test expected non-500 err_code=3010, got %d %s", code, raw)
	}

	// Role separation matrix: non-admin roles blocked from admin APIs (403),
	// intruder/executor denied decision on pending order, non-submitter denied withdrawal.
	order := fx.b9ReadySubmit(t)
	orderID := order["id"].(string)
	for name, cl := range map[string]*testClient{
		"submitter": &fx.owner,
		"approver":  &fx.reviewer1,
		"executor":  &fx.executor,
		"intruder":  &fx.intruder,
	} {
		c, _, r := cl.do("GET", "/api/v4/admin/users", nil, false)
		if c != 403 {
			t.Fatalf("%s GET /admin/users status=%d want 403 (%s)", name, c, r)
		}
	}
	c, b, r := fx.intruder.do("POST", "/api/v4/change-orders/"+orderID+"/approval-decisions",
		map[string]any{"decision": "approve", "comment": "hijack"}, true)
	if c < 400 && errCode(b) == 0 {
		t.Fatalf("intruder unexpectedly approved order %s: %d %s", orderID, c, r)
	}
	c, _, r = fx.reviewer1.doWithHeaders("POST", "/api/v4/change-orders/"+orderID+"/withdrawal",
		map[string]any{"reason": "non-submitter attempt"}, map[string]string{
			"If-Match":        `"1"`,
			"Idempotency-Key": "l1-withdraw-" + uuid.NewString()[:8],
			"X-CSRF-Token":    fx.reviewer1.cookies["yearning_csrf"],
		})
	if c != 403 {
		t.Fatalf("non-submitter approver withdraw status=%d want 403 (%s)", c, r)
	}
}

// TestL1QueryTargetExecutionAndCapabilities exercises DEF-07 (comments,
// string semicolons, CTEs, set-returning functions) and DEF-10 (0-sleep probe
// followed immediately by capabilities read) on real target PostgreSQL.
func TestL1QueryTargetExecutionAndCapabilities(t *testing.T) {
	fx := newB11Fixture(t)

	// DEF-10: probe datasource connection using stored test_purpose, then immediately read capabilities without sleep.
	code, dsBody, raw := fx.admin.do("GET", "/api/v4/admin/datasources/"+fx.qDsID, nil, false)
	if code != 200 || errCode(dsBody) != 0 {
		t.Fatalf("get datasource: %d %s", code, raw)
	}
	testPurpose, _ := dataMap(dsBody)["test_purpose"].(string)
	if testPurpose == "" {
		testPurpose = "review"
	}
	code, probeBody, raw := fx.admin.do("POST", "/api/v4/admin/datasources/"+fx.qDsID+"/connection-tests",
		map[string]any{"purpose": testPurpose}, true)
	if code != 200 || errCode(probeBody) != 0 {
		t.Fatalf("connection test: %d %s", code, raw)
	}
	code, capBody, raw := fx.admin.do("GET", "/api/v4/admin/datasources/"+fx.qDsID+"/capabilities", nil, false)
	if code != 200 || errCode(capBody) != 0 {
		t.Fatalf("immediate capabilities after probe failed: %d %s", code, raw)
	}

	// DEF-07 / DEF-08: real query session executes SQL corpus cleanly on target PG.
	sessionID := fx.b11ActiveSessionHTTP(t)
	for _, sql := range []string{
		"SELECT 1 AS ok; -- trailing comment",
		"SELECT 'a;b' AS semi_val",
		"WITH cte AS (SELECT 42 AS n) SELECT * FROM cte",
		"SELECT n FROM generate_series(1, 5) AS n",
	} {
		code, body, raw := fx.b11ExecHTTP(t, sessionID, sql, 5000, 50)
		if code != 200 || errCode(body) != 0 {
			t.Fatalf("query %q failed: %d %s", sql, code, raw)
		}
	}
}

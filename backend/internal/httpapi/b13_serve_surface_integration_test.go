//go:build integration

// b13_serve_surface_integration_test.go BE-B13-SERVE-SURFACE-ALIGNMENT
// acceptance: S17.6-5 four-domain mounting (dashboard/announcements/audit-events/
// notification) + S18.73 rule-sets route wiring + guard matrix
// (authenticated-session / builtin-admin-management / CSRF double-submit).
package httpapi

import (
	"net/http/httptest"

	"github.com/google/uuid"
	"strings"
	"testing"
)

func TestServeSurfaceFourDomainsReachable(t *testing.T) {
	fx := newB3Fixture(t)
	fx.seedAdmin(t)
	if code, body, _ := fx.admin.login("admin", adminPassword); code != 200 || errCode(body) != 0 {
		t.Fatalf("admin login: code=%d body=%v", code, body)
	}

	// Regular user created via real management API (satisfying password policy).
	code, body, _ := fx.admin.do("POST", "/api/v4/admin/users", map[string]any{
		"username": "alice", "display_name": "Alice", "password": "alice-pass-123456",
	}, true)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("create alice: code=%d body=%v", code, body)
	}
	alice := testClient{t: t, engine: fx.engine, cookies: map[string]string{}}
	if code, body, _ = alice.login("alice", "alice-pass-123456"); code != 200 || errCode(body) != 0 {
		t.Fatalf("alice login: code=%d body=%v", code, body)
	}

	// Anonymous: authenticated surfaces return 401 (problem envelope).
	anon := testClient{t: t, engine: fx.engine, cookies: map[string]string{}}
	for _, path := range []string{"/api/v4/dashboard/me", "/api/v4/announcements/current"} {
		if code, _, _ = anon.do("GET", path, nil, false); code != 401 {
			t.Fatalf("anonymous %s: want 401 got %d", path, code)
		}
	}

	// Authenticated regular user: personal dashboard fields complete, current announcement, platform ops stats (S18.71).
	code, body, _ = alice.do("GET", "/api/v4/dashboard/me", nil, false)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("alice dashboard/me: code=%d body=%v", code, body)
	}
	data, _ := body["data"].(map[string]any)
	for _, f := range []string{"draft_count", "submitted_order_count", "pending_approval_count",
		"pending_execution_count", "blocked_review_count", "active_query_grant_count",
		"active_query_session_count"} {
		if _, ok := data[f]; !ok {
			t.Fatalf("dashboard/me missing %s: %v", f, data)
		}
	}
	if code, _, _ = alice.do("GET", "/api/v4/announcements/current", nil, false); code != 200 {
		t.Fatalf("alice announcements/current: want 200 got %d", code)
	}
	if code, _, _ = alice.do("GET", "/api/v4/admin/dashboard/operations", nil, false); code != 200 {
		t.Fatalf("alice operations dashboard (§18.71): want 200 got %d", code)
	}
	// S18.83 system-health is open to all authenticated sessions; reachable by regular users.
	if code, _, _ = alice.do("GET", "/api/v4/admin/dashboard/system-health", nil, false); code != 200 {
		t.Fatalf("alice system-health (§18.83): want 200 got %d", code)
	}
	// Management surfaces reject regular users (including all admin read surfaces across the four domains; review-quality removed per owner directive).
	for _, path := range []string{
		"/api/v4/admin/audit-events", "/api/v4/admin/notification-channels",
		"/api/v4/admin/notification-deliveries", "/api/v4/admin/announcement-revisions",
	} {
		if code, _, _ = alice.do("GET", path, nil, false); code != 403 {
			t.Fatalf("alice %s: want 403 got %d", path, code)
		}
	}

	// All admin read surfaces are reachable and return valid business envelopes.
	for _, path := range []string{
		"/api/v4/admin/audit-events", "/api/v4/admin/notification-channels",
		"/api/v4/admin/notification-deliveries", "/api/v4/admin/announcement-revisions",
		"/api/v4/admin/dashboard/operations", "/api/v4/admin/dashboard/system-health",
	} {
		code, body, _ = fx.admin.do("GET", path, nil, false)
		if code != 200 || errCode(body) != 0 {
			t.Fatalf("admin %s: code=%d body=%v", path, code, body)
		}
	}
	code, body, _ = fx.admin.do("GET", "/api/v4/admin/audit-events", nil, false)
	if data, _ = body["data"].(map[string]any); data == nil {
		t.Fatalf("audit-events data envelope missing: %v", body)
	}

	// Mutation surface CSRF double-submit: POST without X-CSRF-Token is rejected at guard layer.
	if code, _, _ = fx.admin.do("POST", "/api/v4/admin/notification-channels", validChannelWrite("csrf-probe"), false); code != 403 {
		t.Fatalf("notification channel create without CSRF: want 403 got %d", code)
	}
	// With CSRF token, request reaches domain handler and succeeds (HTTP 200 and err_code=0 -- business errors
	// also return HTTP 200, so err_code must be asserted).
	code, body, _ = fx.admin.do("POST", "/api/v4/admin/notification-channels", validChannelWrite("csrf-probe"), true)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("notification channel create with CSRF: code=%d body=%v", code, body)
	}
}

func TestServeSurfaceRuleSetsRoutes(t *testing.T) {
	fx := newB3Fixture(t)
	fx.seedAdmin(t)
	if code, body, _ := fx.admin.login("admin", adminPassword); code != 200 || errCode(body) != 0 {
		t.Fatalf("admin login: code=%d body=%v", code, body)
	}

	create := func(name string) map[string]any {
		t.Helper()
		code, body, _ := fx.admin.do("POST", "/api/v4/admin/rule-sets", map[string]any{
			"name": name, "enabled": true, "rules": map[string]any{}, "prompt_tool_ids": []string{},
		}, true)
		if code != 200 || errCode(body) != 0 {
			t.Fatalf("create %s: code=%d body=%v", name, code, body)
		}
		data, _ := body["data"].(map[string]any)
		if data == nil {
			t.Fatalf("create %s: no data: %v", name, body)
		}
		return data
	}
	a := create("change-review-default")
	create("offline-readonly")

	// RuleSet view shape (all OpenAPI required fields).
	for _, f := range []string{"id", "name", "enabled", "rules", "prompt_tool_ids",
		"config_hash", "version", "created_at", "updated_at"} {
		if _, ok := a[f]; !ok {
			t.Fatalf("rule set missing %s: %v", f, a)
		}
	}

	// List + server-side q (case-insensitive substring).
	code, body, _ := fx.admin.do("GET", "/api/v4/admin/rule-sets?limit=20", nil, false)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("list rule sets: code=%d body=%v", code, body)
	}
	listData, _ := body["data"].(map[string]any)
	items, _ := listData["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("list rule sets: want 2 items got %d (%v)", len(items), listData)
	}
	code, body, _ = fx.admin.do("GET", "/api/v4/admin/rule-sets?q=CHANGE-REVIEW", nil, false)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("search rule sets: code=%d body=%v", code, body)
	}
	listData, _ = body["data"].(map[string]any)
	items, _ = listData["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("search rule sets: want 1 item got %d (%v)", len(items), listData)
	}

	id, _ := a["id"].(string)
	if code, body, _ = fx.admin.do("GET", "/api/v4/admin/rule-sets/"+id, nil, false); code != 200 || errCode(body) != 0 {
		t.Fatalf("get rule set: code=%d body=%v", code, body)
	}

	// Replace: If-Match advances version; stale version -> 1003 VERSION_CONFLICT.
	replace := map[string]any{
		"name": "change-review-v2", "enabled": false, "rules": map[string]any{}, "prompt_tool_ids": []string{},
	}
	code, body, _ = fx.admin.putWithIfMatch("/api/v4/admin/rule-sets/"+id, replace, "1")
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("replace rule set: code=%d body=%v", code, body)
	}
	if data, _ := body["data"].(map[string]any); data == nil || data["version"].(float64) != 2 {
		t.Fatalf("replace rule set: want version 2 got %v", body)
	}
	code, body, _ = fx.admin.putWithIfMatch("/api/v4/admin/rule-sets/"+id, replace, "1")
	if code != 200 || errCode(body) != 1003 {
		t.Fatalf("stale replace: want 1003 got code=%d body=%v", code, body)
	}

	// Delete: missing If-Match -> 1001; stale version -> 1004 (C1004-only profile);
	// current version -> 200, subsequent read -> 1002.
	code, body, _ = fx.admin.do("DELETE", "/api/v4/admin/rule-sets/"+id, nil, true)
	if code != 200 || errCode(body) != 1001 {
		t.Fatalf("delete rule set without If-Match: want 1001 got code=%d body=%v", code, body)
	}
	code, body, _ = fx.admin.deleteWithIfMatch("/api/v4/admin/rule-sets/"+id, "1")
	if code != 200 || errCode(body) != 1004 {
		t.Fatalf("delete rule set stale If-Match: want 1004 got code=%d body=%v", code, body)
	}
	code, body, _ = fx.admin.deleteWithIfMatch("/api/v4/admin/rule-sets/"+id, "2")
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("delete rule set: code=%d body=%v", code, body)
	}
	if code, body, _ = fx.admin.do("GET", "/api/v4/admin/rule-sets/"+id, nil, false); code != 200 || errCode(body) != 1002 {
		t.Fatalf("get deleted rule set: want 1002 got code=%d body=%v", code, body)
	}
}

// deleteWithIfMatch sends DELETE with If-Match and CSRF headers.
func (cl *testClient) deleteWithIfMatch(path, ifMatch string) (int, map[string]any, string) {
	cl.t.Helper()
	req := httptest.NewRequest("DELETE", path, nil)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("If-Match", `"`+ifMatch+`"`)
	if token := cl.cookies["yearning_csrf"]; token != "" {
		req.Header.Set("X-CSRF-Token", token)
	}
	return cl.dispatch(req)
}

// TestServeSurfaceOpenAPIFieldParity acceptance gate 1: real HTTP responses across all four domains
// match frozen OpenAPI schemas field-by-field (reuses b13 validator; extra fields under additionalProperties:
// false or missing required fields fail -- the exact drift category aligned in S17.6-5).
func TestServeSurfaceOpenAPIFieldParity(t *testing.T) {
	schemas := loadSchemas(t)
	fx := newB3Fixture(t)
	fx.seedAdmin(t)
	if code, body, _ := fx.admin.login("admin", adminPassword); code != 200 || errCode(body) != 0 {
		t.Fatalf("admin login: code=%d body=%v", code, body)
	}

	checkEnvelope := func(op, schemaName string, body map[string]any) {
		t.Helper()
		if errCode(body) != 0 {
			t.Fatalf("%s: err_code %d (%v)", op, errCode(body), body)
		}
		errs := validateSchema(schemas, map[string]any{
			"$ref": "#/components/schemas/" + schemaName}, body, op)
		if len(errs) > 0 {
			t.Fatalf("%s: schema violations: %s", op, strings.Join(errs, "; "))
		}
	}
	checkData := func(op, schemaName string, body map[string]any) {
		t.Helper()
		if errCode(body) != 0 {
			t.Fatalf("%s: err_code %d (%v)", op, errCode(body), body)
		}
		errs := validateSchema(schemas, map[string]any{
			"$ref": "#/components/schemas/" + schemaName}, dataMap(body), op+".data")
		if len(errs) > 0 {
			t.Fatalf("%s: schema violations: %s", op, strings.Join(errs, "; "))
		}
	}

	// Dashboard envelope field-by-field (review-quality removed per owner 2026-10-23 directive).
	_, body, _ := fx.admin.do("GET", "/api/v4/dashboard/me", nil, false)
	checkEnvelope("getMyDashboard", "MyDashboardSuccess", body)
	_, body, _ = fx.admin.do("GET", "/api/v4/admin/dashboard/operations", nil, false)
	checkEnvelope("getOperationsDashboard", "OperationsDashboardSuccess", body)
	_, body, _ = fx.admin.do("GET", "/api/v4/admin/dashboard/system-health", nil, false)
	checkEnvelope("getSystemHealthDashboard", "SystemHealthDashboardSuccess", body)

	// No published announcement: CurrentAnnouncementSuccess (null data branch).
	_, body, _ = fx.admin.do("GET", "/api/v4/announcements/current", nil, false)
	checkEnvelope("getCurrentAnnouncement(empty)", "CurrentAnnouncementSuccess", body)

	// Announcement closed loop: revision -> publish -> current pointer (all additionalProperties:false).
	_, body, _ = fx.admin.do("POST", "/api/v4/admin/announcement-revisions", map[string]any{
		"title": "serve-surface parity", "markdown_source": "# hello\nfirst revision body",
	}, true)
	checkEnvelope("createAnnouncementRevision", "AnnouncementRevisionSuccess", body)
	revID := dataMap(body)["id"].(string)
	// Initial publish: pointer does not yet exist; client sends If-Match "1" per contract
	// (FE = publicationVersion ?? 1; insert branch is unconstrained by expected value).
	_, body, _ = fx.admin.putWithIfMatch("/api/v4/admin/announcement-publication",
		map[string]any{"announcement_revision_id": revID}, "1")
	checkEnvelope("publishAnnouncementRevision", "AnnouncementPublicationSuccess", body)
	publishedVersion := dataMap(body)["version"].(float64)
	_, body, _ = fx.admin.do("GET", "/api/v4/announcements/current", nil, false)
	checkEnvelope("getCurrentAnnouncement(published)", "CurrentAnnouncementSuccess", body)
	// R2-F12: publish response version matches Current() readback (initial publish = 1).
	if dataMap(body)["version"].(float64) != publishedVersion {
		t.Fatalf("publication version drift: publish=%v current=%v",
			publishedVersion, dataMap(body)["version"])
	}
	_, body, _ = fx.admin.do("GET", "/api/v4/admin/announcement-revisions?limit=10", nil, false)
	checkData("listAnnouncementRevisions", "AnnouncementRevisionPage", body)

	// Publication pointer update path (R1-F1): missing If-Match -> 1001; stale version -> 1003
	// (announcement_publish profile declares VERSION_CONFLICT); current version -> 200.
	// Error envelope includes required request_id and retryable (R1-F3).
	_, body, _ = fx.admin.do("PUT", "/api/v4/admin/announcement-publication",
		map[string]any{"announcement_revision_id": revID}, true)
	if errCode(body) != 1001 || body["request_id"] == nil || body["retryable"] != false {
		t.Fatalf("publish no If-Match: want 1001 envelope got %v", body)
	}
	_, body, _ = fx.admin.putWithIfMatch("/api/v4/admin/announcement-publication",
		map[string]any{"announcement_revision_id": revID}, "9")
	if errCode(body) != 1003 {
		t.Fatalf("publish stale If-Match: want 1003 got %v", body)
	}
	_, body, _ = fx.admin.putWithIfMatch("/api/v4/admin/announcement-publication",
		map[string]any{"announcement_revision_id": revID}, "1")
	if errCode(body) != 0 {
		t.Fatalf("publish update path: %v", body)
	}
	// Update path advances version: 1 -> 2, matching Current() (R2-F12).
	if v := dataMap(body)["version"].(float64); v != 2 {
		t.Fatalf("publish update version: want 2 got %v", v)
	}
	_, curBody, _ := fx.admin.do("GET", "/api/v4/announcements/current", nil, false)
	if dataMap(curBody)["version"].(float64) != 2 {
		t.Fatalf("current after update: want 2 got %v", dataMap(curBody)["version"])
	}

	// Notification channel: create -> list (first item = NotificationChannel).
	_, body, _ = fx.admin.do("POST", "/api/v4/admin/notification-channels", validChannelWrite("parity-probe"), true)
	checkData("createNotificationChannel", "NotificationChannel", body)
	channelID := dataMap(body)["id"].(string)
	_, body, _ = fx.admin.do("GET", "/api/v4/admin/notification-channels", nil, false)
	listData := dataMap(body)
	items, _ := listData["items"].([]any)
	if len(items) == 0 {
		t.Fatalf("listNotificationChannels: no items %v", listData)
	}
	errs := validateSchema(schemas, map[string]any{
		"$ref": "#/components/schemas/NotificationChannel"}, items[0], "listNotificationChannels.data.items[0]")
	if len(errs) > 0 {
		t.Fatalf("NotificationChannel item: %s", strings.Join(errs, "; "))
	}

	// Delivery surface (R1-F2): test delivery produces delivery row -> first item matches
	// NotificationDelivery schema field-by-field (last_error_code key);
	// state filter narrows results, unknown state returns empty set.
	_, body, _ = fx.admin.do("POST", "/api/v4/admin/notification-channels/"+channelID+"/test-deliveries", nil, true)
	if errCode(body) != 0 {
		t.Fatalf("createNotificationTestDelivery: %v", body)
	}
	// Test delivery is an async task (worker consumes outbox to create delivery row); this fixture has no
	// worker, so seed a delivery row directly to verify read-surface shape and filtering.
	seedEvent := uuid.NewString()
	deliveryID := uuid.NewString()
	if err := fx.db.Exec(`insert into notification_deliveries
		(id, domain_event_id, notification_channel_id, recipient_user_id, recipient_key,
		 recipient_address_ciphertext, recipient_address_nonce, recipient_address_key_id,
		 recipient_address_algorithm, state, delivery_attempt_count, created_at, updated_at)
		values (?, ?, ?, null, 'group:' || ?::text, decode('00', 'hex'), decode('00', 'hex'), 'pay-1',
		 'aes-256-gcm', 'queued', 0, now(), now())`,
		deliveryID, seedEvent, channelID, channelID).Error; err != nil {
		t.Fatalf("seed delivery: %v", err)
	}
	_, body, _ = fx.admin.do("GET", "/api/v4/admin/notification-deliveries", nil, false)
	deliveryData := dataMap(body)
	deliveryItems, _ := deliveryData["items"].([]any)
	if len(deliveryItems) == 0 {
		t.Fatalf("listNotificationDeliveries: no items %v", deliveryData)
	}
	errs = validateSchema(schemas, map[string]any{
		"$ref": "#/components/schemas/NotificationDelivery"}, deliveryItems[0], "listNotificationDeliveries.data.items[0]")
	if len(errs) > 0 {
		t.Fatalf("NotificationDelivery item: %s", strings.Join(errs, "; "))
	}
	st := deliveryItems[0].(map[string]any)["state"].(string)
	_, body, _ = fx.admin.do("GET", "/api/v4/admin/notification-deliveries?state="+st, nil, false)
	for _, it := range dataMap(body)["items"].([]any) {
		if it.(map[string]any)["state"] != st {
			t.Fatalf("state filter leaked foreign state: %v", it)
		}
	}
	_, body, _ = fx.admin.do("GET", "/api/v4/admin/notification-deliveries?state=no-such-state", nil, false)
	if items := dataMap(body)["items"].([]any); len(items) != 0 {
		t.Fatalf("state filter unknown state: want 0 items got %d", len(items))
	}

	// RuleSet referencing non-existent Prompt Tool -> 1001 (R1-F4, admin_create profile
	// exhaustive set excludes 1002).
	_, body, _ = fx.admin.do("POST", "/api/v4/admin/rule-sets", map[string]any{
		"name": "ghost-tool-set", "enabled": true, "rules": map[string]any{},
		"prompt_tool_ids": []string{"99999999-9999-4999-8999-999999999999"},
	}, true)
	if errCode(body) != 1001 {
		t.Fatalf("rule set unknown tool: want 1001 got %v", body)
	}
	// R2-F13: replace non-existent target -> 1002 (admin_replace declares RESOURCE_NOT_FOUND,
	// consistent with get returning 1002).
	_, body, _ = fx.admin.putWithIfMatch("/api/v4/admin/rule-sets/99999999-9999-4999-8999-199999999999",
		map[string]any{"name": "ghost", "enabled": true, "rules": map[string]any{}, "prompt_tool_ids": []string{}}, "1")
	if errCode(body) != 1002 {
		t.Fatalf("replace nonexistent rule set: want 1002 got %v", body)
	}

	// Audit events (management activities such as login have already produced events): first item = AuditEvent.
	_, body, _ = fx.admin.do("GET", "/api/v4/admin/audit-events?limit=10", nil, false)
	listData = dataMap(body)
	items, _ = listData["items"].([]any)
	if len(items) == 0 {
		t.Fatalf("listAuditEvents: no items %v", listData)
	}
	errs = validateSchema(schemas, map[string]any{
		"$ref": "#/components/schemas/AuditEvent"}, items[0], "listAuditEvents.data.items[0]")
	if len(errs) > 0 {
		t.Fatalf("AuditEvent item: %s", strings.Join(errs, "; "))
	}
}

// validChannelWrite returns a valid email channel write payload (service validation requires
// host + port + tls_mode).
func validChannelWrite(name string) map[string]any {
	return map[string]any{
		"kind": "email", "name": name, "enabled": true,
		"configuration": map[string]any{
			"host": "127.0.0.1", "port": 587, "tls_mode": "starttls",
		},
	}
}

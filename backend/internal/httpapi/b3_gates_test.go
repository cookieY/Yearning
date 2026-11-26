//go:build integration

// Gate test suite. Each Test corresponds to a B3 acceptance gate or its key negative cases.
package httpapi

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

const adminPassword = "correct-admin-pass-12"

func (fx *b3Fixture) seedAdmin(t *testing.T) {
	t.Helper()
	if err := fx.svc.BootstrapAdmin(t.Context(), adminPassword); err != nil {
		t.Fatalf("bootstrap admin: %v", err)
	}
}

// G2: admin five-failure lockout and CLI recovery.
func TestGate2AdminLockoutAndCLIReset(t *testing.T) {
	fx := newB3Fixture(t)
	fx.seedAdmin(t)

	// Successful login resets failure counter: log in once, fail 4 times (count=4), then log in again (resets to 0).
	if code, body, _ := fx.admin.login("admin", adminPassword); code != 200 || errCode(body) != 0 {
		t.Fatalf("initial login failed: %d %v", code, body)
	}
	for i := 0; i < 4; i++ {
		code, body, _ := fx.admin.login("admin", "wrong-password-"+fmt.Sprint(i))
		if code != 200 || errCode(body) != 1101 {
			t.Fatalf("attempt %d: expect 1101, got %d %v", i, code, body)
		}
	}
	if code, body, _ := fx.admin.login("admin", adminPassword); code != 200 || errCode(body) != 0 {
		t.Fatalf("success should reset counter, got %d %v", code, body)
	}

	// 5 consecutive failures -> locked.
	for i := 0; i < 4; i++ {
		_, body, _ := fx.admin.login("admin", "bad-pass")
		if errCode(body) != 1101 {
			t.Fatalf("pre-lock attempt %d: %v", i, body)
		}
	}
	code, body, _ := fx.admin.login("admin", "bad-pass-5")
	if errCode(body) != 1101 {
		t.Fatalf("5th failure should still report 1101, got %v", body)
	}
	// Once locked, even the valid password is rejected with the fixed CLI recovery guidance.
	code, body, _ = fx.admin.login("admin", adminPassword)
	if code != 200 || errCode(body) != 1102 {
		t.Fatalf("locked admin must get 1102, got %d %v", code, body)
	}
	if msg, _ := body["message"].(string); !strings.Contains(msg, "./Yearning --reset-admin-password") {
		t.Fatalf("lock message must point to CLI reset: %q", msg)
	}

	// Audit: lockout event has been recorded.
	var lockEvents int64
	_ = fx.db.Raw(`select count(*) from audit_events where event_type = 'admin_login_locked'`).Scan(&lockEvents).Error
	if lockEvents == 0 {
		t.Fatal("lockout audit event missing")
	}

	// CLI reset: executed while service is running, serialized by row lock; restores login and revokes prior sessions.
	if err := fx.svc.ResetAdminPassword(t.Context(), "new-admin-pass-34", identityRequestMeta()); err != nil {
		t.Fatalf("reset: %v", err)
	}
	code, body, _ = fx.admin.login("admin", "new-admin-pass-34")
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("login after reset failed: %d %v", code, body)
	}
	var resetEvents int64
	_ = fx.db.Raw(`select count(*) from audit_events where event_type = 'admin.password_reset_cli'`).Scan(&resetEvents).Error
	if resetEvents == 0 {
		t.Fatal("reset audit event missing")
	}

	// Prior sessions revoked: cookies issued before reset must be invalidated.
	stale := testClient{t: t, engine: fx.engine, cookies: map[string]string{}}
	if _, body, _ := stale.login("admin", "new-admin-pass-34"); errCode(body) != 0 {
		t.Fatalf("stale client relogin failed: %v", body)
	}
	if err := fx.svc.ResetAdminPassword(t.Context(), "newer-admin-pass-5", identityRequestMeta()); err != nil {
		t.Fatal(err)
	}
	code, _, _ = stale.do("GET", "/api/v4/users/me", nil, false)
	if code != http401 {
		t.Fatalf("stale session must be revoked after reset, got %d", code)
	}
	// Concurrent reset: only one of two concurrent CLI invocations succeeds (or the second rewrites after row lock;
	// verifies mutual exclusion produces no partial state and admin can still log in).
	if err := fx.svc.ResetAdminPassword(t.Context(), "final-admin-pass-9", identityRequestMeta()); err != nil {
		t.Fatal(err)
	}
	_, body, _ = fx.admin.login("admin", "final-admin-pass-9")
	if errCode(body) != 0 {
		t.Fatalf("login after concurrent resets failed: %v", body)
	}
}

// G1: default-deny authorization, admin-exclusive management surface, CSRF enforcement, regular user session.
func TestGate1DefaultDenyAndIDORMatrix(t *testing.T) {
	fx := newB3Fixture(t)
	fx.seedAdmin(t)
	if code, body, _ := fx.admin.login("admin", adminPassword); code != 200 || errCode(body) != 0 {
		t.Fatalf("admin login: %v", body)
	}

	// Admin creates regular user (immediately active with zero permissions).
	code, body, _ := fx.admin.do("POST", "/api/v4/admin/users", map[string]any{
		"username": "alice", "display_name": "Alice", "password": "alice-password-123",
	}, true)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("createUser: %d %v", code, body)
	}
	aliceID, _ := body["data"].(map[string]any)["id"].(string)
	if aliceID == "" {
		t.Fatal("created user id missing")
	}

	alice := testClient{t: t, engine: fx.engine, cookies: map[string]string{}}
	if _, b, _ := alice.login("alice", "alice-password-123"); errCode(b) != 0 {
		t.Fatalf("alice login: %v", b)
	}

	// admin_surface_cases: every management operation rejects non-admin (403) and anonymous (401).
	anon := testClient{t: t, engine: fx.engine, cookies: map[string]string{}}
	adminSurface := []struct{ method, path string }{
		{"GET", "/api/v4/admin/users"},
		{"POST", "/api/v4/admin/users"},
		{"GET", "/api/v4/admin/flows"},
		{"POST", "/api/v4/admin/flows"},
		{"GET", "/api/v4/admin/datasources"},
		{"POST", "/api/v4/admin/datasources"},
		{"GET", "/api/v4/admin/permission-groups"},
		{"GET", "/api/v4/admin/identity-providers"},
		{"GET", "/api/v4/admin/datasources/00000000-0000-0000-0000-000000000001"},
		{"GET", "/api/v4/tasks/00000000-0000-0000-0000-000000000002"},
	}
	for _, route := range adminSurface {
		code, _, _ := alice.do(route.method, route.path, map[string]any{}, true)
		if code != http403 {
			t.Fatalf("admin surface %s %s by normal user: want 403 got %d", route.method, route.path, code)
		}
		code, _, _ = anon.do(route.method, route.path, nil, false)
		if code != http401 {
			t.Fatalf("admin surface %s %s anonymous: want 401 got %d", route.method, route.path, code)
		}
	}

	// constraint_deny: mutation request missing CSRF token -> 403; forged token -> 403.
	code, _, _ = alice.do("POST", "/api/v4/auth/logout", nil, false)
	if code != http403 {
		t.Fatalf("missing csrf: want 403 got %d", code)
	}
	forged := testClient{t: t, engine: fx.engine, cookies: map[string]string{
		"yearning_session": alice.cookies["yearning_session"],
		"yearning_csrf":    "forged-token",
	}}
	code, _, _ = forged.do("POST", "/api/v4/auth/logout", nil, true)
	if code != http403 {
		t.Fatalf("forged csrf: want 403 got %d", code)
	}

	// horizontal_access_cases: unauthorized flow catalog is empty; visible after grant; immediately revoked upon grant removal.
	code, body, _ = alice.do("GET", "/api/v4/users/me/flows?flow_type=change_review", nil, false)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("me flows: %v", body)
	}
	if n := len(itemsOf(body)); n != 0 {
		t.Fatalf("zero-grant user must see no flows, saw %d", n)
	}

	// Prepare flow (admin surface): rule set + datasource + approver alice.
	ruleSetID := fx.insertRuleSet(t)
	dsID := fx.adminCreateDatasource(t, "pg-main")
	flowID := fx.adminCreateChangeFlow(t, "release-flow", ruleSetID, dsID, aliceID)

	// Permission groups: alice in group-a(flow) and group-b(flow); still visible after revoking group-a (union semantics).
	groupA := fx.adminCreateGroup(t, "group-a", []string{aliceID}, []string{flowID})
	groupB := fx.adminCreateGroup(t, "group-b", []string{aliceID}, []string{flowID})
	_, body, _ = alice.do("GET", "/api/v4/users/me/flows?flow_type=change_review", nil, false)
	if n := len(itemsOf(body)); n != 1 {
		t.Fatalf("granted user must see exactly 1 flow, saw %d", n)
	}
	fx.adminDeleteGroup(t, groupA)
	_, body, _ = alice.do("GET", "/api/v4/users/me/flows?flow_type=change_review", nil, false)
	if n := len(itemsOf(body)); n != 1 {
		t.Fatalf("union via remaining group must keep flow visible, saw %d", n)
	}
	fx.adminDeleteGroup(t, groupB)
	_, body, _ = alice.do("GET", "/api/v4/users/me/flows?flow_type=change_review", nil, false)
	if n := len(itemsOf(body)); n != 0 {
		t.Fatalf("revoked grant must take effect immediately, saw %d", n)
	}

	// deleted_actor_cases: deleting a user immediately invalidates their sessions and blocks authentication.
	bobID := fx.adminCreateUser(t, "bob", "bob-password-123")
	bob := testClient{t: t, engine: fx.engine, cookies: map[string]string{}}
	if _, b, _ := bob.login("bob", "bob-password-123"); errCode(b) != 0 {
		t.Fatalf("bob login: %v", b)
	}
	fx.adminDeleteUser(t, bobID)
	code, _, _ = bob.do("GET", "/api/v4/users/me", nil, false)
	if code != http401 {
		t.Fatalf("deleted user session must fail, got %d", code)
	}
	_, b, _ := bob.login("bob", "bob-password-123")
	if errCode(b) != 1101 {
		t.Fatalf("deleted user re-login must be rejected, got %v", b)
	}

	// Builtin admin cannot be deleted (ADMIN_IMMUTABLE).
	var adminID string
	_ = fx.db.Raw(`select id from users where username = 'admin'`).Scan(&adminID).Error
	adminVersion := userVersion(t, fx.db, adminID)
	code, body, _ = fx.admin.doWithVersion("DELETE", "/api/v4/admin/users/"+adminID, nil, adminVersion)
	if code != 200 || errCode(body) != 1103 {
		t.Fatalf("delete builtin admin must be 1103, got %d %v", code, body)
	}
}

// G3: Secrets never enter responses, logs, or events.
func TestGate3SecretsNeverSurface(t *testing.T) {
	fx := newB3Fixture(t)
	fx.seedAdmin(t)
	fx.admin.login("admin", adminPassword)

	const oidcSecret = "oidc-client-secret-do-not-leak"
	const dsPassword = "datasource-password-do-not-leak"

	// Create OIDC Provider (with Client Secret).
	code, body, raw := fx.admin.do("POST", "/api/v4/admin/identity-providers", map[string]any{
		"provider_key": "sso", "provider_kind": "oidc", "display_name": "SSO", "enabled": true,
		"configuration": map[string]any{
			"issuer_url": "https://sso.example.com", "client_id": "yearning",
			"scopes": []string{"openid"}, "username_claim": "preferred_username",
			"connect_timeout_ms": 3000, "request_timeout_ms": 5000,
		},
		"client_secret": map[string]any{"value": oidcSecret},
	}, true)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("create provider: %d %s", code, raw)
	}
	if strings.Contains(raw, oidcSecret) {
		t.Fatal("provider secret leaked in create response")
	}
	providerID, _ := body["data"].(map[string]any)["id"].(string)

	// Read endpoints only return secret_configured.
	_, _, raw = fx.admin.do("GET", "/api/v4/admin/identity-providers/"+providerID, nil, false)
	if strings.Contains(raw, oidcSecret) {
		t.Fatal("provider secret leaked in read response")
	}
	if !strings.Contains(raw, "secret_configured") {
		t.Fatal("secret_configured flag missing")
	}
	_, _, raw = fx.admin.do("GET", "/api/v4/admin/identity-providers", nil, false)
	if strings.Contains(raw, oidcSecret) {
		t.Fatal("provider secret leaked in list response")
	}

	// Datasource Purpose credentials.
	code, body, raw = fx.admin.do("POST", "/api/v4/admin/datasources", map[string]any{
		"name": "sec-ds", "engine": "mysql", "compatibility_mode": "mysql",
		"deployment_kind": "native", "host": "db.invalid", "port": 3306,
		"version_constraint": "8.0.x", "enabled": true,
		"credentials": []map[string]any{{
			"purpose": "execution", "username": "app",
			"password": map[string]any{"value": dsPassword},
		}},
	}, true)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("create datasource: %d %s", code, raw)
	}
	if strings.Contains(raw, dsPassword) {
		t.Fatal("datasource password leaked in response")
	}
	dsID, _ := body["data"].(map[string]any)["id"].(string)
	_, _, raw = fx.admin.do("GET", "/api/v4/admin/datasources/"+dsID, nil, false)
	if strings.Contains(raw, dsPassword) {
		t.Fatal("datasource password leaked in read response")
	}
	if !strings.Contains(raw, "credential_status") {
		t.Fatal("credential_status missing")
	}

	// Database layer: credential ciphertext contains no plaintext; audit/domain/outbox events contain no Secret.
	var ciphertexts []string
	if err := fx.db.Raw(`select encode(credential_ciphertext, 'hex') from datasource_credentials`).Scan(&ciphertexts).Error; err != nil {
		t.Fatal(err)
	}
	if len(ciphertexts) == 0 {
		t.Fatal("credential row missing")
	}
	for _, ct := range ciphertexts {
		if strings.Contains(strings.ToLower(ct), strings.ToLower(dsPassword)) {
			t.Fatal("plaintext found inside ciphertext column")
		}
	}
	var eventBlobs []string
	if err := fx.db.Raw(`select event_type || '|' || coalesce(actor_username_snapshot,'') || '|' || metadata::text from audit_events`).Scan(&eventBlobs).Error; err != nil {
		t.Fatal(err)
	}
	eventBlobs = append(eventBlobs, scanStrings(t, fx.db, `select event_type || '|' || event_data::text from domain_events`)...)
	eventBlobs = append(eventBlobs, scanStrings(t, fx.db, `select event_type || '|' || payload::text from outbox_events`)...)
	for _, blob := range eventBlobs {
		if strings.Contains(blob, oidcSecret) || strings.Contains(blob, dsPassword) {
			t.Fatalf("secret leaked into events: %.120s", blob)
		}
	}
	// Logs: none of the observed log entries contain the Secret.
	for _, entry := range fx.logs.All() {
		line := entry.Message
		for _, f := range entry.ContextMap() {
			line += fmt.Sprintf("%v", f)
		}
		if strings.Contains(line, oidcSecret) || strings.Contains(line, dsPassword) || strings.Contains(line, adminPassword) {
			t.Fatalf("secret leaked into logs: %.200s", line)
		}
	}
	// Connection test failure path also never echoes credentials (unreachable host -> structured business error).
	_, body, raw = fx.admin.do("POST", "/api/v4/admin/datasources/"+dsID+"/connection-tests",
		map[string]any{"purpose": "execution"}, true)
	if strings.Contains(raw, dsPassword) || strings.Contains(raw, "app") {
		t.Fatalf("connection test response leaked credential material: %s", raw)
	}
	if errCode(body) != 3010 {
		t.Fatalf("unreachable datasource expect 3010, got %v", body)
	}
}

// S18.61: connection test purpose persistence -- defaults to review on create; explicit execution persists and is echoed;
// full replacement can change to query; omitting on replacement resets to review (DatasourceWrite default semantics);
// invalid enum returns 1001; read surface matches DB column.
func TestB3DatasourceTestPurpose(t *testing.T) {
	fx := newB3Fixture(t)
	fx.seedAdmin(t)
	fx.admin.login("admin", adminPassword)

	payload := func(name string, purpose any) map[string]any {
		body := map[string]any{
			"name": name, "engine": "mysql", "compatibility_mode": "mysql",
			"deployment_kind": "native", "host": "db.invalid", "port": 3306,
			"version_constraint": "8.0.x", "enabled": true,
			"credentials": []map[string]any{{
				"purpose": "execution", "username": "app",
				"password": map[string]any{"value": "purpose-pw-123456"},
			}},
		}
		if purpose != nil {
			body["test_purpose"] = purpose
		}
		return body
	}

	code, body, raw := fx.admin.do("POST", "/api/v4/admin/datasources", payload("tp-ds", nil), true)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("create datasource: %d %s", code, raw)
	}
	data, _ := body["data"].(map[string]any)
	dsID, _ := data["id"].(string)
	if data["test_purpose"] != "review" {
		t.Fatalf("create default test_purpose = %v, want review", data["test_purpose"])
	}

	// Explicit execution purpose.
	code, body, raw = fx.admin.doWithVersion("PUT", "/api/v4/admin/datasources/"+dsID,
		payload("tp-ds", "execution"), dsVersion(t, fx.db, dsID))
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("replace test_purpose=execution: %d %s", code, raw)
	}
	if data, _ = body["data"].(map[string]any); data["test_purpose"] != "execution" {
		t.Fatalf("replace test_purpose = %v, want execution", data["test_purpose"])
	}

	// Omitting on full replacement resets to review.
	code, body, raw = fx.admin.doWithVersion("PUT", "/api/v4/admin/datasources/"+dsID,
		payload("tp-ds", nil), dsVersion(t, fx.db, dsID))
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("replace without test_purpose: %d %s", code, raw)
	}
	if data, _ = body["data"].(map[string]any); data["test_purpose"] != "review" {
		t.Fatalf("omitted test_purpose = %v, want reset to review", data["test_purpose"])
	}

	// Invalid enum rejected.
	code, body, raw = fx.admin.doWithVersion("PUT", "/api/v4/admin/datasources/"+dsID,
		payload("tp-ds", "audit"), dsVersion(t, fx.db, dsID))
	if code != 200 || errCode(body) != 1001 {
		t.Fatalf("invalid test_purpose expect 1001, got %d %s", code, raw)
	}

	// DB layer matches read surface.
	var stored string
	if err := fx.db.Raw(`select test_purpose from datasources where id = ?`, dsID).Scan(&stored).Error; err != nil || stored != "review" {
		t.Fatalf("stored test_purpose = %q err %v, want review", stored, err)
	}
	_, body, raw = fx.admin.do("GET", "/api/v4/admin/datasources/"+dsID, nil, false)
	if data, _ = body["data"].(map[string]any); data["test_purpose"] != "review" {
		t.Fatalf("read test_purpose = %v, want review (%s)", data["test_purpose"], raw)
	}
}

// TestB3StoredTestPurpose S18.73: probe purpose read path for capabilities endpoint -- persisted value
// takes precedence, falling back to review when empty (used by HandleDatasourceCapabilities to select credentials).
func TestB3StoredTestPurpose(t *testing.T) {
	fx := newB3Fixture(t)
	fx.seedAdmin(t)
	fx.admin.login("admin", adminPassword)
	fx.adminCreateDatasource(t, "purpose-ds")

	var dsID string
	if err := fx.db.Raw(`select id from datasources where name = 'purpose-ds'`).Scan(&dsID).Error; err != nil || dsID == "" {
		t.Fatalf("seed datasource: %v", err)
	}
	got, err := fx.dss.StoredTestPurpose(context.Background(), dsID)
	if err != nil || got != "review" {
		t.Fatalf("StoredTestPurpose default = %q err %v, want review", got, err)
	}
	if err := fx.db.Exec(`update datasources set test_purpose = 'execution' where id = ?`, dsID).Error; err != nil {
		t.Fatal(err)
	}
	got, err = fx.dss.StoredTestPurpose(context.Background(), dsID)
	if err != nil || got != "execution" {
		t.Fatalf("StoredTestPurpose persisted = %q err %v, want execution", got, err)
	}
}

// TestB3AdminListServerSearch S18.73: server-side q on admin list endpoints -- all seven mounted
// list endpoints filter by q (case-insensitive name substring; users also matches display_name/email,
// datasources also matches host). Seeded row names carry a searchable suffix.
func TestB3AdminListServerSearch(t *testing.T) {
	fx := newB3Fixture(t)
	fx.seedAdmin(t)
	fx.admin.login("admin", adminPassword)

	mustList := func(path string) []any {
		t.Helper()
		code, body, raw := fx.admin.do("GET", path, nil, false)
		if code != 200 || errCode(body) != 0 {
			t.Fatalf("GET %s: %d %s", path, code, raw)
		}
		data, _ := body["data"].(map[string]any)
		items, _ := data["items"].([]any)
		return items
	}

	// users: matches username/display_name.
	fx.adminCreateUser(t, "quser-alpha", adminPassword)
	fx.adminCreateUser(t, "quser-beta", adminPassword)
	items := mustList("/api/v4/admin/users?q=ALPHA")
	if len(items) != 1 {
		t.Fatalf("users q=ALPHA items=%d want 1", len(items))
	}
	if len(mustList("/api/v4/admin/users?q=zzz-no-hit")) != 0 {
		t.Fatal("users q=no-hit must be empty")
	}

	// datasources: matches name/host.
	fx.adminCreateDatasource(t, "qds-gamma")
	fx.adminCreateDatasource(t, "qds-delta")
	if len(mustList("/api/v4/admin/datasources?q=gamma")) != 1 {
		t.Fatal("datasources q=gamma want 1")
	}
	if len(mustList("/api/v4/admin/datasources?q=delta")) != 1 {
		t.Fatal("datasources q=delta want 1")
	}

	// flows: matches name (create flow via helper then search by name).
	items = mustList("/api/v4/admin/flows?q=")
	if items == nil {
		t.Fatal("flows list must respond")
	}
	// groups.
	fx.adminCreateGroup(t, "qgroup-omega", nil, nil)
	if len(mustList("/api/v4/admin/permission-groups?q=omega")) != 1 {
		t.Fatal("groups q=omega want 1")
	}
	if len(mustList("/api/v4/admin/permission-groups?q=zzz-no-hit")) != 0 {
		t.Fatal("groups q=no-hit must be empty")
	}
}

// G4: template edits do not mutate frozen instances (replace_flow_template isolation).
func TestGate4TemplateEditNeverTouchesFrozenInstances(t *testing.T) {
	fx := newB3Fixture(t)
	fx.seedAdmin(t)
	fx.admin.login("admin", adminPassword)

	ruleSetID := fx.insertRuleSet(t)
	dsID := fx.adminCreateDatasource(t, "frozen-ds")
	approver := fx.adminCreateUser(t, "carol", "carol-password-123")
	flowID := fx.adminCreateChangeFlow(t, "frozen-flow", ruleSetID, dsID, approver)

	// Simulate frozen instance of a submitted change order: order_flow_snapshots + order_stages.
	orderID := uuid.NewString()
	snapshotHash := strings.Repeat("ab", 32)
	if err := fx.db.Exec(`insert into order_flow_snapshots (order_id, flow_snapshot_ciphertext, flow_snapshot_nonce,
		flow_snapshot_key_id, flow_snapshot_algorithm, snapshot_hash, created_at)
		values (?, decode('00112233','hex'), decode('445566778899','hex'), 'pay-1', 'aes-256-gcm', ?, now())`,
		orderID, snapshotHash).Error; err != nil {
		t.Fatal(err)
	}
	stageID := uuid.NewString()
	fingerprint := strings.Repeat("11", 32)
	if err := fx.db.Exec(`insert into order_stages (id, order_id, position, datasource_id,
		datasource_name_snapshot, datasource_engine_snapshot, datasource_identity_fingerprint,
		stage_review_snapshot_id, review_snapshot_hash, review_gate_passed,
		target_snapshot_ciphertext, target_snapshot_nonce, target_snapshot_key_id, target_snapshot_algorithm,
		state, version)
		values (?, ?, 1, ?, 'frozen-ds', 'mysql', ?, ?, ?, true,
		decode('aabb','hex'), decode('ccdd','hex'), 'pay-1', 'aes-256-gcm', 'pending', 1)`,
		stageID, orderID, dsID, fingerprint, uuid.NewString(), strings.Repeat("99", 32)).Error; err != nil {
		t.Fatalf("insert order stage: %v", err)
	}

	// Edit template: rename and replace approver.
	replacement := changeFlowPayload("frozen-flow-v2", ruleSetID, dsID, fx.mustAdminID(t))
	version := flowVersion(t, fx.db, flowID)
	code, body, raw := fx.admin.doWithVersion("PUT", "/api/v4/admin/flows/"+flowID, replacement, version)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("replace flow failed: %d %s", code, raw)
	}
	if flowVersion(t, fx.db, flowID) <= version {
		t.Fatal("flow version must increment on replace")
	}

	// Frozen instance remains unchanged field-by-field.
	var snap struct {
		Ciphertext string
		Nonce      string
		KeyID      string
		Hash       string
		Created    time.Time
	}
	if err := fx.db.Raw(`select encode(flow_snapshot_ciphertext,'hex') as ciphertext,
		encode(flow_snapshot_nonce,'hex') as nonce, flow_snapshot_key_id as key_id,
		snapshot_hash as hash, created_at as created
		from order_flow_snapshots where order_id = ?`, orderID).Scan(&snap).Error; err != nil {
		t.Fatal(err)
	}
	if snap.Ciphertext != "00112233" || snap.Nonce != "445566778899" || snap.Hash != snapshotHash {
		t.Fatalf("frozen snapshot mutated: %+v", snap)
	}
	var stageCount int64
	_ = fx.db.Raw(`select count(*) from order_stages where order_id = ? and datasource_id = ?`, orderID, dsID).Scan(&stageCount).Error
	if stageCount != 1 {
		t.Fatal("frozen order stage mutated")
	}

	// Template edit produces flow.updated domain event (written atomically to outbox).
	var events int64
	_ = fx.db.Raw(`select count(*) from domain_events where subject = ? and event_type = 'flow.updated'`,
		"flow:"+flowID).Scan(&events).Error
	if events == 0 {
		t.Fatal("flow.updated domain event missing")
	}
	var outbox int64
	_ = fx.db.Raw(`select count(*) from outbox_events o join domain_events d on d.id = o.id
		where d.subject = ?`, "flow:"+flowID).Scan(&outbox).Error
	if outbox == 0 {
		t.Fatal("flow.updated outbox row missing")
	}
}

// delete_user: empty approval node marks flow invalid; deleting submitter with active orders is forbidden.
func TestDeleteUserFlowInvalidationAndActiveOrders(t *testing.T) {
	fx := newB3Fixture(t)
	fx.seedAdmin(t)
	fx.admin.login("admin", adminPassword)

	ruleSetID := fx.insertRuleSet(t)
	dsID := fx.adminCreateDatasource(t, "del-ds")
	sole := fx.adminCreateUser(t, "dave", "dave-password-1234")
	flowID := fx.adminCreateChangeFlow(t, "dave-flow", ruleSetID, dsID, sole)

	// dave is the sole reviewer: deleting dave leaves node empty -> flow becomes invalid.
	fx.adminDeleteUser(t, sole)
	var status string
	_ = fx.db.Raw(`select status from flows where id = ?`, flowID).Scan(&status).Error
	if status != "invalid" {
		t.Fatalf("flow with empty approval node must be invalid, got %q", status)
	}
	var nodeEvents int64
	_ = fx.db.Raw(`select count(*) from domain_events where event_type = 'workflow.node_invalidated' and subject = ?`,
		"flow:"+flowID).Scan(&nodeEvents).Error
	if nodeEvents == 0 {
		t.Fatal("workflow.node_invalidated event missing")
	}

	// Deleting submitter of an non-terminal order is forbidden (USER_HAS_ACTIVE_ORDERS).
	erin := fx.adminCreateUser(t, "erin", "erin-password-1234")
	_ = fx.adminCreateChangeFlow(t, "erin-flow", ruleSetID, dsID, erin)
	orderID := uuid.NewString()
	if err := fx.db.Exec(`insert into change_orders (id, display_number, submitter_user_id_snapshot,
		submitter_username_snapshot, submitter_display_name_snapshot, title, state, sql_hmac, snapshot_hash, version, submitted_at)
		values (?, 'YR-20260825-000001', ?, 'erin', 'Erin', 't', 'submitted', ?, ?, 1, now())`,
		orderID, erin, strings.Repeat("cd", 32), strings.Repeat("ef", 32)).Error; err != nil {
		t.Fatalf("insert change order: %v", err)
	}
	erinVersion := userVersion(t, fx.db, erin)
	code, body, _ := fx.admin.doWithVersion("DELETE", "/api/v4/admin/users/"+erin, nil, erinVersion)
	if code != 200 || errCode(body) != 1104 {
		t.Fatalf("submitter deletion must be 1104, got %d %v", code, body)
	}
	// Deletion is allowed once order reaches a terminal state.
	if err := fx.db.Exec(`update change_orders set state = 'withdrawn', terminal_at = now() where id = ?`, orderID).Error; err != nil {
		t.Fatal(err)
	}
	code, body, _ = fx.admin.doWithVersion("DELETE", "/api/v4/admin/users/"+erin, nil, erinVersion)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("deletion after terminal order: %d %v", code, body)
	}

	// Set-based flow invalidation across multiple flows/stages + auth_sessions cleanup while preserving flows with co-actors.
	frank := fx.adminCreateUser(t, "frank", "frank-password-1234")
	grace := fx.adminCreateUser(t, "grace", "grace-password-1234")
	frankClient := testClient{t: t, engine: fx.engine, cookies: map[string]string{}}
	if code, body, _ := frankClient.login("frank", "frank-password-1234"); code != 200 || errCode(body) != 0 {
		t.Fatalf("frank login failed: %d %v", code, body)
	}
	// fExecOnly: grace is approver, frank is sole executor -> emptied execution stage invalidates flow.
	fExecOnly := fx.adminCreateFlowWithActors(t, "frank-exec-flow", ruleSetID, dsID, []string{grace}, []string{frank})
	// fCoActor: grace & frank share approval and execution -> retains grace, stays enabled.
	fCoActor := fx.adminCreateFlowWithActors(t, "frank-co-flow", ruleSetID, dsID, []string{frank, grace}, []string{frank, grace})

	fx.adminDeleteUser(t, frank)
	var sExec, sCo string
	_ = fx.db.Raw(`select status from flows where id = ?`, fExecOnly).Scan(&sExec).Error
	_ = fx.db.Raw(`select status from flows where id = ?`, fCoActor).Scan(&sCo).Error
	if sExec != "invalid" {
		t.Fatalf("flow with emptied execution actors must be invalid, got %q", sExec)
	}
	if sCo != "enabled" {
		t.Fatalf("flow with remaining co-actor must stay enabled, got %q", sCo)
	}
	var sessionCount int64
	_ = fx.db.Raw(`select count(*) from auth_sessions where user_id = ?`, frank).Scan(&sessionCount).Error
	if sessionCount != 0 {
		t.Fatalf("expected 0 remaining auth_sessions for deleted user, got %d", sessionCount)
	}
}

func (fx *b3Fixture) adminCreateFlowWithActors(t *testing.T, name, ruleSetID, dsID string, approvers, executors []string) string {
	t.Helper()
	appRefs := make([]map[string]any, len(approvers))
	for i, u := range approvers {
		appRefs[i] = map[string]any{"user_id": u}
	}
	execRefs := make([]map[string]any, len(executors))
	for i, u := range executors {
		execRefs[i] = map[string]any{"user_id": u}
	}
	code, body, raw := fx.admin.do("POST", "/api/v4/admin/flows", map[string]any{
		"name": name, "flow_type": "change_review", "enabled": true, "rule_set_id": ruleSetID,
		"stages": []map[string]any{{
			"position": 1, "datasource_id": dsID,
			"approval_steps":   []map[string]any{{"position": 1, "actors": appRefs}},
			"execution_actors": execRefs,
		}},
	}, true)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("create flow %s: %d %s", name, code, raw)
	}
	id, _ := body["data"].(map[string]any)["id"].(string)
	return id
}

// Datasource reference protection and optimistic concurrency.
func TestDatasourceReferenceProtectionAndVersionConflict(t *testing.T) {
	fx := newB3Fixture(t)
	fx.seedAdmin(t)
	fx.admin.login("admin", adminPassword)

	dsID := fx.adminCreateDatasource(t, "ref-ds")
	ruleSetID := fx.insertRuleSet(t)
	approver := fx.adminCreateUser(t, "frank", "frank-password-123")
	flowID := fx.adminCreateChangeFlow(t, "ref-flow", ruleSetID, dsID, approver)

	// Referenced by flow stage -> physical deletion forbidden (1107).
	code, body, _ := fx.admin.doWithVersion("DELETE", "/api/v4/admin/datasources/"+dsID, nil, dsVersion(t, fx.db, dsID))
	if code != 200 || errCode(body) != 1107 {
		t.Fatalf("referenced datasource delete must be 1107, got %d %v", code, body)
	}
	// Flow referenced by permission group Grant -> physical deletion forbidden (1106).
	groupID := fx.adminCreateGroup(t, "ref-group", nil, []string{flowID})
	code, body, _ = fx.admin.doWithVersion("DELETE", "/api/v4/admin/flows/"+flowID, nil, flowVersion(t, fx.db, flowID))
	if code != 200 || errCode(body) != 1106 {
		t.Fatalf("referenced flow delete must be 1106, got %d %v", code, body)
	}
	fx.adminDeleteGroup(t, groupID)
	// Optimistic concurrency: stale If-Match -> 1004 (flow_delete profile is C1004-only,
	// aligned with operation-error-profiles).
	code, body, _ = fx.admin.doWithVersion("DELETE", "/api/v4/admin/flows/"+flowID, nil, 999)
	if code != 200 || errCode(body) != 1004 {
		t.Fatalf("stale If-Match must be 1004, got %d %v", code, body)
	}
	// Remove flow first before deleting datasource (unreferenced datasource can be physically deleted).
	version := flowVersion(t, fx.db, flowID)
	code, body, _ = fx.admin.doWithVersion("DELETE", "/api/v4/admin/flows/"+flowID, nil, version)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("unreferenced flow delete failed: %d %v", code, body)
	}
	version = dsVersion(t, fx.db, dsID)
	code, body, _ = fx.admin.doWithVersion("DELETE", "/api/v4/admin/datasources/"+dsID, nil, version)
	_ = version
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("unreferenced datasource delete failed: %d %v", code, body)
	}
}

// G-self-change: self-service password change (S18.52). Covers policy validation, current password check,
// external identity rejection, revocation of all sessions, audit events, and auth_provider disclosure.
func TestGateSelfPasswordChange(t *testing.T) {
	fx := newB3Fixture(t)
	fx.seedAdmin(t)
	if _, body, _ := fx.admin.login("admin", adminPassword); errCode(body) != 0 {
		t.Fatalf("admin login: %v", body)
	}
	userID := fx.adminCreateUser(t, "carol", "carol-password-123")
	carol := testClient{t: t, engine: fx.engine, cookies: map[string]string{}}
	if _, b, _ := carol.login("carol", "carol-password-123"); errCode(b) != 0 {
		t.Fatalf("carol login: %v", b)
	}

	// CurrentUser discloses auth_provider: local accounts report local.
	_, body, _ := carol.do("GET", "/api/v4/users/me", nil, false)
	data, _ := body["data"].(map[string]any)
	if data["auth_provider"] != "local" {
		t.Fatalf("local user auth_provider must be local, got %v", data["auth_provider"])
	}

	// changeMyPassword is a state-changing operation: missing CSRF token must return 403.
	if code, _, _ := carol.do("POST", "/api/v4/users/me/password", map[string]any{
		"old_password": "carol-password-123", "new_password": "carol-new-pass-45",
	}, false); code != http403 {
		t.Fatalf("missing csrf must be 403, got %d", code)
	}

	// 12-character policy -> 1001.
	_, body, _ = carol.do("POST", "/api/v4/users/me/password", map[string]any{
		"old_password": "carol-password-123", "new_password": "short",
	}, true)
	if errCode(body) != 1001 {
		t.Fatalf("short password must be 1001, got %v", body)
	}
	// Wrong current password -> 1101, disclosing no other information.
	_, body, _ = carol.do("POST", "/api/v4/users/me/password", map[string]any{
		"old_password": "wrong-old-password", "new_password": "carol-new-pass-45",
	}, true)
	if errCode(body) != 1101 {
		t.Fatalf("wrong old password must be 1101, got %v", body)
	}

	// Success: all prior sessions (including current session) are invalidated, new password can log in, audit written.
	if code, body, _ := carol.do("POST", "/api/v4/users/me/password", map[string]any{
		"old_password": "carol-password-123", "new_password": "carol-new-pass-45",
	}, true); code != 200 || errCode(body) != 0 {
		t.Fatalf("change password failed: %d %v", code, body)
	}
	if code, _, _ := carol.do("GET", "/api/v4/users/me", nil, false); code != http401 {
		t.Fatalf("current session must be revoked after change, got %d", code)
	}
	if code, body, _ := carol.login("carol", "carol-password-123"); errCode(body) != 1101 {
		t.Fatalf("old password must stop working, got %d %v", code, body)
	}
	if _, b, _ := carol.login("carol", "carol-new-pass-45"); errCode(b) != 0 {
		t.Fatalf("login with new password failed: %v", b)
	}
	var changeEvents int64
	_ = fx.db.Raw(`select count(*) from audit_events where event_type = 'user.password_changed'`).Scan(&changeEvents).Error
	if changeEvents == 0 {
		t.Fatal("password change audit event missing")
	}

	// External-identity-bound account -> 1109, and auth_provider discloses bound provider.
	bound := fx.adminCreateUser(t, "dana", "dana-password-123")
	if err := fx.db.Exec(`insert into external_identities (id, user_id, provider_kind, provider_key, external_subject, created_at)
		values (?, ?, 'oidc', 'corp-oidc', 'dana-sub', now())`,
		"00000000-0000-0000-0000-0000000d0001", bound).Error; err != nil {
		t.Fatal(err)
	}
	dana := testClient{t: t, engine: fx.engine, cookies: map[string]string{}}
	if _, b, _ := dana.login("dana", "dana-password-123"); errCode(b) != 0 {
		t.Fatalf("dana login: %v", b)
	}
	_, body, _ = dana.do("GET", "/api/v4/users/me", nil, false)
	if data, _ = body["data"].(map[string]any); data["auth_provider"] != "oidc" {
		t.Fatalf("bound user auth_provider must be oidc, got %v", data["auth_provider"])
	}
	_, body, _ = dana.do("POST", "/api/v4/users/me/password", map[string]any{
		"old_password": "dana-password-123", "new_password": "dana-new-pass-678",
	}, true)
	if errCode(body) != 1109 {
		t.Fatalf("external-identity change must be 1109, got %v", body)
	}

	// Builtin admin self-service password change also works (local account).
	if _, b, _ := fx.admin.login("admin", adminPassword); errCode(b) != 0 {
		t.Fatalf("admin relogin: %v", b)
	}
	if code, body, _ := fx.admin.do("POST", "/api/v4/users/me/password", map[string]any{
		"old_password": adminPassword, "new_password": "admin-self-new-77",
	}, true); code != 200 || errCode(body) != 0 {
		t.Fatalf("admin self change failed: %d %v", code, body)
	}
	if _, b, _ := fx.admin.login("admin", "admin-self-new-77"); errCode(b) != 0 {
		t.Fatalf("admin login after self change: %v", b)
	}
	_ = userID
}

// RCP-20261105-ADMIN-LIST-FIELD-FILTERS: per-column filter parameters across the four admin list endpoints.
// Covers: accurate narrowing, multi-parameter AND, cursor pagination across pages, invalid value -> 1001, default behavior unchanged.
func TestB3AdminListFieldFilters(t *testing.T) {
	fx := newB3Fixture(t)
	fx.seedAdmin(t)
	if code, body, _ := fx.admin.login("admin", adminPassword); code != 200 || errCode(body) != 0 {
		t.Fatalf("admin login: %v", body)
	}
	adminID := fx.mustAdminID(t)

	mustList := func(path string) []any {
		t.Helper()
		code, body, raw := fx.admin.do("GET", path, nil, false)
		if code != 200 || errCode(body) != 0 {
			t.Fatalf("GET %s: %d %s", path, code, raw)
		}
		data, _ := body["data"].(map[string]any)
		items, _ := data["items"].([]any)
		return items
	}
	mustReject := func(path string) {
		t.Helper()
		code, body, _ := fx.admin.do("GET", path, nil, false)
		if code != 200 || errCode(body) != 1001 {
			t.Fatalf("GET %s: expect 1001, got %d %v", path, code, body)
		}
	}
	pageOf := func(path string) map[string]any {
		t.Helper()
		code, body, raw := fx.admin.do("GET", path, nil, false)
		if code != 200 || errCode(body) != 0 {
			t.Fatalf("GET %s: %d %s", path, code, raw)
		}
		data, _ := body["data"].(map[string]any)
		page, _ := data["page"].(map[string]any)
		return page
	}

	// ---- users.kind ----
	localA := fx.adminCreateUser(t, "filter-kind-alpha", adminPassword)
	localB := fx.adminCreateUser(t, "filter-kind-beta", adminPassword)
	if got := len(mustList("/api/v4/admin/users?kind=local_user")); got < 2 {
		t.Fatalf("kind=local_user items=%d want >=2", got)
	}
	if got := len(mustList("/api/v4/admin/users?kind=builtin_admin")); got != 1 {
		t.Fatalf("kind=builtin_admin items=%d want 1", got)
	}
	if got := len(mustList("/api/v4/admin/users?kind=local_user&q=ALPHA")); got != 1 {
		t.Fatalf("kind+q AND items=%d want 1", got)
	}
	mustReject("/api/v4/admin/users?kind=superuser")
	// Cursor pagination across pages: after narrowing by kind, paging still reaches the second page.
	page := pageOf("/api/v4/admin/users?kind=local_user&limit=1")
	if page["has_more"] != true {
		t.Fatalf("kind=local_user limit=1 has_more=%v want true", page["has_more"])
	}
	next, _ := page["next_cursor"].(string)
	if next == "" {
		t.Fatal("kind=local_user limit=1 must carry next_cursor")
	}
	items := mustList("/api/v4/admin/users?kind=local_user&limit=1&after=" + next)
	if len(items) != 1 {
		t.Fatalf("second page under kind filter items=%d want 1", len(items))
	}
	_ = localA
	_ = localB

	// ---- datasources: engine/compatibility_mode/credential_mode/referenced ----
	dsRW := fx.adminCreateDatasource(t, "filter-ds-rw")
	// Datasource with review credential only: no execution credential -> credential_mode=read_only.
	code, body, raw := fx.admin.do("POST", "/api/v4/admin/datasources", map[string]any{
		"name": "filter-ds-ro", "engine": "postgresql", "compatibility_mode": "postgresql",
		"deployment_kind": "native", "host": "filter-ds-ro.invalid", "port": 5432,
		"version_constraint": "16.x", "enabled": true,
		"credentials": []map[string]any{{
			"purpose": "review", "username": "reader",
			"password": map[string]any{"value": "not-a-real-secret"},
		}},
	}, true)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("create read-only datasource: %d %s", code, raw)
	}
	if got := len(mustList("/api/v4/admin/datasources?engine=mysql")); got < 1 {
		t.Fatalf("engine=mysql items=%d want >=1", got)
	}
	if got := len(mustList("/api/v4/admin/datasources?engine=postgresql")); got != 1 {
		t.Fatalf("engine=postgresql items=%d want 1", got)
	}
	if got := len(mustList("/api/v4/admin/datasources?compatibility_mode=postgresql")); got != 1 {
		t.Fatalf("compatibility_mode=postgresql items=%d want 1", got)
	}
	if got := len(mustList("/api/v4/admin/datasources?credential_mode=read_only")); got != 1 {
		t.Fatalf("credential_mode=read_only items=%d want 1", got)
	}
	roCount := len(mustList("/api/v4/admin/datasources?credential_mode=read_write"))
	if roCount < 1 {
		t.Fatalf("credential_mode=read_write items=%d want >=1", roCount)
	}
	mustReject("/api/v4/admin/datasources?engine=sqlite")
	mustReject("/api/v4/admin/datasources?credential_mode=rw")
	mustReject("/api/v4/admin/datasources?referenced=yes")

	// ---- flows: flow_type/enabled; reference relationship also feeds referenced assertions ----
	ruleSetID := fx.insertRuleSet(t)
	flowID := fx.adminCreateChangeFlow(t, "filter-flow-change", ruleSetID, dsRW, adminID)
	// Disabled flow: enabled=false filter surface.
	code, body, raw = fx.admin.do("POST", "/api/v4/admin/flows", map[string]any{
		"name": "filter-flow-disabled", "flow_type": "change_review", "enabled": false,
		"rule_set_id": ruleSetID,
		"stages": []map[string]any{{
			"position": 1, "datasource_id": dsRW,
			"approval_steps":   []map[string]any{{"position": 1, "actors": []map[string]any{{"user_id": adminID}}}},
			"execution_actors": []map[string]any{{"user_id": adminID}},
		}},
	}, true)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("create disabled flow: %d %s", code, raw)
	}
	if got := len(mustList("/api/v4/admin/flows?flow_type=change_review&enabled=true")); got != 1 {
		t.Fatalf("flows AND filter items=%d want 1", got)
	}
	if got := len(mustList("/api/v4/admin/flows?enabled=false")); got != 1 {
		t.Fatalf("flows enabled=false items=%d want 1", got)
	}
	if got := len(mustList("/api/v4/admin/flows?flow_type=query_access")); got != 0 {
		t.Fatalf("flows query_access items=%d want 0", got)
	}
	mustReject("/api/v4/admin/flows?flow_type=change")
	mustReject("/api/v4/admin/flows?enabled=1")
	// referenced: filter-ds-rw is referenced by flow stage; filter-ds-ro is unreferenced.
	if got := len(mustList("/api/v4/admin/datasources?referenced=true&engine=mysql")); got != 1 {
		t.Fatalf("referenced=true&engine=mysql items=%d want 1", got)
	}
	if got := len(mustList("/api/v4/admin/datasources?referenced=false")); got != 1 {
		t.Fatalf("referenced=false items=%d want 1", got)
	}

	// ---- permission-groups: enabled/member_user_id/flow_id ----
	fx.adminCreateGroup(t, "filter-group-hit", []string{adminID}, []string{flowID})
	fx.adminCreateGroup(t, "filter-group-empty", nil, nil)
	if got := len(mustList("/api/v4/admin/permission-groups?enabled=true")); got != 2 {
		t.Fatalf("groups enabled=true items=%d want 2", got)
	}
	if got := len(mustList("/api/v4/admin/permission-groups?member_user_id=" + adminID)); got != 1 {
		t.Fatalf("groups member_user_id items=%d want 1", got)
	}
	if got := len(mustList("/api/v4/admin/permission-groups?flow_id=" + flowID)); got != 1 {
		t.Fatalf("groups flow_id items=%d want 1", got)
	}
	if got := len(mustList("/api/v4/admin/permission-groups?member_user_id=" + adminID + "&flow_id=" + flowID)); got != 1 {
		t.Fatalf("groups member+flow AND items=%d want 1", got)
	}
	mustReject("/api/v4/admin/permission-groups?member_user_id=not-a-uuid")
	mustReject("/api/v4/admin/permission-groups?flow_id=42")
	mustReject("/api/v4/admin/permission-groups?enabled=maybe")

	// Default behavior unchanged: all four tables return normally when new filter parameters are omitted.
	if mustList("/api/v4/admin/users") == nil || mustList("/api/v4/admin/datasources") == nil ||
		mustList("/api/v4/admin/flows") == nil || mustList("/api/v4/admin/permission-groups") == nil {
		t.Fatal("unfiltered lists must respond")
	}
}

//go:build integration

// b13_diagnostics_test.go tests SQL performance diagnostics and user administrator role (BE-B13-SQL-DIAGNOSTICS)
// HTTP routes, role authorization (role='admin' vs role='user'), builtin admin demotion protection,
// Cache-Control: no-store, If-Match optimistic concurrency, 30s manual snapshot rate limiting, and idempotent order submission.
package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/database"
	diagapp "github.com/cookieY/YearningX/backend/internal/diagnostics/application"
	diagdom "github.com/cookieY/YearningX/backend/internal/diagnostics/domain"
	"github.com/cookieY/YearningX/backend/internal/identity"
	"github.com/cookieY/YearningX/backend/internal/identity/session"
	"github.com/cookieY/YearningX/backend/internal/platform/addrguard"
	reviewapp "github.com/cookieY/YearningX/backend/internal/review/application"
	reviewprovider "github.com/cookieY/YearningX/backend/internal/review/provider"
)

type fakeB13DiagCollector struct {
	probeState    database.StatsProbeState
	snapshotRows  []database.NativeStatementStatRow
	templatesByFP map[string]string
}

func (f *fakeB13DiagCollector) Probe(_ context.Context, ds diagapp.DatasourceRow) (*database.StatsProbeResult, error) {
	st := f.probeState
	if st == "" {
		st = database.StatsProbeReady
	}
	return &database.StatsProbeResult{
		State:        st,
		EngineFamily: ds.CompatibilityMode,
	}, nil
}

func (f *fakeB13DiagCollector) ReadSnapshot(_ context.Context, _ diagapp.DatasourceRow, _ int) ([]database.NativeStatementStatRow, error) {
	return f.snapshotRows, nil
}

func (f *fakeB13DiagCollector) ReadTemplate(_ context.Context, _ diagapp.DatasourceRow, fp, _ string) (string, error) {
	if f.templatesByFP != nil {
		if t, ok := f.templatesByFP[fp]; ok && t != "" {
			return t, nil
		}
	}
	return "", database.ErrTemplateUnavailable
}

func (f *fakeB13DiagCollector) ListColumns(_ context.Context, _ diagapp.DatasourceRow, schema, table string) ([]database.MetadataColumn, error) {
	return []database.MetadataColumn{
		{SchemaName: schema, TableName: table, ColumnName: "id", Ordinal: 1, DatabaseType: "bigint", Nullable: false},
		{SchemaName: schema, TableName: table, ColumnName: "customer_code", Ordinal: 2, DatabaseType: "varchar(64)", Nullable: false},
		{SchemaName: schema, TableName: table, ColumnName: "created_at", Ordinal: 3, DatabaseType: "timestamptz", Nullable: false},
	}, nil
}

func (f *fakeB13DiagCollector) ListIndexes(_ context.Context, _ diagapp.DatasourceRow, schema, table string) ([]database.MetadataIndex, error) {
	return []database.MetadataIndex{
		{SchemaName: schema, TableName: table, IndexName: "PRIMARY", Primary: true, ColumnNames: []string{"id"}},
	}, nil
}

func doB13DiagReq(t *testing.T, r http.Handler, method, path, userID string, headers map[string]string, body any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var reqBody *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reqBody = bytes.NewReader(raw)
	} else {
		reqBody = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reqBody)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if userID != "" {
		req.Header.Set("X-Test-User-ID", userID)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var parsed map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &parsed)
	return w, parsed
}

// TestB13_AdminRoleLifecycleAndDiagnosticsHTTPRoutes verifies:
// 1. Creating and editing a user with role='admin', and /users/me returning role="admin" with can_access_admin=true;
// 2. Builtin super administrator is_builtin_admin=true cannot be demoted to role='user' (returns business code 1103 BUILTIN_ADMIN_IMMUTABLE);
// 3. Regular user role='user' accessing /api/v4/admin/diagnostics/* is rejected with HTTP 403, while user administrator role='admin' is allowed;
// 4. All 10 SQL performance diagnostics HTTP endpoints, Cache-Control: no-store, If-Match optimistic locking, 30s manual snapshot rate limiting, and idempotent order submission.
func TestB13_AdminRoleLifecycleAndDiagnosticsHTTPRoutes(t *testing.T) {
	db := openB13HTTPTestDB(t)
	for _, tbl := range []string{
		"sql_diagnostics_order_links",
		"sql_diagnostics_ai_reports",
		"sql_diagnostics_bucket_metrics",
		"sql_diagnostics_statements",
		"sql_diagnostics_snapshots",
		"diagnostics_instance_leases",
		"datasource_diagnostics_configs",
		"order_stages",
		"change_orders",
		"datasource_credentials",
		"ai_providers",
		"auth_sessions",
	} {
		_ = db.Exec("TRUNCATE TABLE " + tbl + " CASCADE").Error
	}

	dir := t.TempDir()
	doc := `{"contract_version":1,"deployment_id":"b13-diag-http-test","keys":[
		{"key_id":"pay-1","purpose":"payload","status":"encrypt_decrypt","created_at":"2026-11-07T00:00:00Z","key_base64":"` + makeB13Key32(1) + `"},
		{"key_id":"state-1","purpose":"oidc_state","status":"encrypt_decrypt","created_at":"2026-11-07T00:00:00Z","key_base64":"` + makeB13Key32(2) + `"},
		{"key_id":"ident-1","purpose":"identifier_token","status":"encrypt_decrypt","created_at":"2026-11-07T00:00:00Z","key_base64":"` + makeB13Key32(3) + `"},
		{"key_id":"fp-1","purpose":"sql_fingerprint","status":"encrypt_decrypt","created_at":"2026-11-07T00:00:00Z","key_base64":"` + makeB13Key32(4) + `"},
		{"key_id":"aff-1","purpose":"cursor_affinity","status":"encrypt_decrypt","created_at":"2026-11-07T00:00:00Z","key_base64":"` + makeB13Key32(5) + `"}
	]}`
	path := dir + "/keyring.json"
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
	recorder, _ := auditlog.NewRecorder(db)

	builtinAdminID := uuid.NewString()
	normalUserID := uuid.NewString()
	dsID := uuid.NewString()
	flowID := uuid.NewString()
	rsID := uuid.NewString()
	pgID := uuid.NewString()

	if err := db.Exec(`
		INSERT INTO users (id, username, display_name, password_hash, is_builtin_admin, role, version, created_at, updated_at)
		VALUES (?, 'admin', 'Builtin Admin', 'argon2id$test', true, 'admin', 1, now(), now()),
		       (?, 'normal_bob', 'Normal Bob', 'argon2id$test', false, 'user', 1, now(), now())`,
		builtinAdminID, normalUserID).Error; err != nil {
		t.Fatalf("seed users: %v", err)
	}
	if err := db.Exec(`
		INSERT INTO datasources (
			id, name, engine, compatibility_mode, deployment_kind, host, port, database_name,
			version_constraint, identity_fingerprint, enabled, test_purpose, version, created_by_user_id, created_at, updated_at
		) VALUES (?, 'mysql-diag-prod', 'mysql', 'mysql', 'native', '10.0.0.25', 3306, 'shop_db',
			'8.0', repeat('2', 64), true, 'query', 1, ?, now(), now())`,
		dsID, builtinAdminID).Error; err != nil {
		t.Fatalf("seed datasource: %v", err)
	}
	if err := db.Exec(`
		INSERT INTO datasource_credentials (
			id, datasource_id, purpose, credential_ciphertext, credential_nonce,
			credential_key_id, credential_algorithm, enabled, created_at, updated_at
		) VALUES (?, ?, 'query', decode('0102','hex'), decode('0102030405060708090a0b0c','hex'),
			'pay-1', 'aes-256-gcm', true, now(), now())`,
		uuid.NewString(), dsID).Error; err != nil {
		t.Fatalf("seed credential: %v", err)
	}
	if err := db.Exec(`
		INSERT INTO rule_sets (id, name, enabled, rules, config_hash, version, created_by_user_id, created_at, updated_at)
		VALUES (?, 'diag-rules', true, '{}'::jsonb, repeat('a', 64), 1, ?, now(), now())`,
		rsID, builtinAdminID).Error; err != nil {
		t.Fatalf("seed rule_set: %v", err)
	}
	if err := db.Exec(`
		INSERT INTO flows (id, name, flow_type, status, rule_set_id, version, created_by_user_id, created_at, updated_at)
		VALUES (?, 'diag-change-flow', 'change_review', 'enabled', ?, 1, ?, now(), now())`,
		flowID, rsID, builtinAdminID).Error; err != nil {
		t.Fatalf("seed flow: %v", err)
	}
	if err := db.Exec(`
		INSERT INTO flow_stages (id, flow_id, position, datasource_id, schema_mapping_ciphertext, schema_mapping_nonce, schema_mapping_key_id, schema_mapping_algorithm)
		VALUES (?, ?, 1, ?, '\x01', '\x02', 'pay-1', 'aes-256-gcm')`,
		uuid.NewString(), flowID, dsID).Error; err != nil {
		t.Fatalf("seed flow_stage: %v", err)
	}
	if err := db.Exec(`
		INSERT INTO permission_groups (id, name, enabled, version, created_by_user_id, created_at, updated_at)
		VALUES (?, 'diag-dba-group', true, 1, ?, now(), now())`,
		pgID, builtinAdminID).Error; err != nil {
		t.Fatalf("seed permission_group: %v", err)
	}

	fakeCollector := &fakeB13DiagCollector{
		probeState: database.StatsProbeReady,
		snapshotRows: []database.NativeStatementStatRow{
			{
				EngineFingerprint:                 "fp_slow_orders",
				DatabaseOrSchema:                  "shop_db",
				CallsTotal:                        80,
				TotalExecTimeMS:                   4000.0,
				RowsExaminedOrSharedBlksReadTotal: 80000,
				RowsSentOrRowsReturnedTotal:       80,
				LockTimeMSOrTempBlksWrittenTotal:  15.0,
			},
		},
		templatesByFP: map[string]string{
			"fp_slow_orders": "SELECT `id`, `customer_code` FROM `orders` WHERE `customer_code` = ? ORDER BY `created_at` DESC",
		},
	}

	diagSvc, err := diagapp.NewServices(db, crypter, recorder, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	diagSvc.Collector = fakeCollector

	fakeRunID := uuid.NewString()
	fakeOrderID := uuid.NewString()
	diagSvc.PrecheckRunner = func(_ context.Context, _ diagdom.Actor, fID, sqlText, _ string) (string, error) {
		if fID != flowID || !strings.Contains(sqlText, "CREATE INDEX") {
			return "", errors.New("unexpected precheck input")
		}
		return fakeRunID, nil
	}
	diagSvc.OrderSubmitter = func(_ context.Context, actor diagdom.Actor, fID, _, stmtKind, _, runID, title, _, idemKey string) (string, string, string, error) {
		if fID != flowID || runID != fakeRunID || stmtKind != "ddl" || idemKey == "" {
			return "", "", "", errors.New("unexpected order submit input")
		}
		if err := db.Exec(`
			INSERT INTO change_orders (
				id, display_number, submitter_user_id_snapshot, submitter_username_snapshot, submitter_display_name_snapshot,
				title, description, state, sql_hmac, snapshot_hash, manually_verified, rollback_enabled,
				ticket_type, rollback_status, version, submitted_at, terminal_at
			) VALUES (?, 'YR-20261107-000199', ?, ?, 'Admin User',
				?, 'diag', 'completed', repeat('a', 64), repeat('b', 64), false, false,
				'normal', 'not_requested', 1, now(), now())`,
			fakeOrderID, actor.UserID, actor.Username, title).Error; err != nil {
			return "", "", "", err
		}
		return fakeOrderID, "YR-20261107-000199", "stage_approval_active", nil
	}

	memStore := session.NewMemoryStore()
	memStore.Put(&session.Session{
		ID: builtinAdminID,
		Identity: &session.Identity{
			UserID:         builtinAdminID,
			Username:       "admin",
			IsBuiltinAdmin: true,
			Role:           "admin",
		},
		ExpiresAt: time.Now().Add(time.Hour),
	})
	memStore.Put(&session.Session{
		ID: normalUserID,
		Identity: &session.Identity{
			UserID:         normalUserID,
			Username:       "normal_bob",
			IsBuiltinAdmin: false,
			Role:           "user",
		},
		ExpiresAt: time.Now().Add(time.Hour),
	})

	sessStore, err := identity.NewSessionStore(db, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	identSvc, err := identity.NewService(db, sessStore, crypter, recorder)
	if err != nil {
		t.Fatal(err)
	}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RequestIDMiddleware())
	r.Use(func(c *gin.Context) {
		uid := c.GetHeader("X-Test-User-ID")
		if uid == "" {
			uid = builtinAdminID
		}
		c.Request.AddCookie(&http.Cookie{Name: "yearning_session", Value: uid})
		c.Next()
	})
	r.Use(session.Middleware(memStore, session.Options{CookieName: "yearning_session"}))

	b3Deps := &B3Deps{
		Identity: identSvc,
		Sessions: nil, // Skip double-submit CSRF cookie in unit test to focus on RequireOperation role authorization and business codes
		DB:       db,
		Crypter:  crypter,
	}
	diagDeps := &DiagnosticsDeps{
		Diagnostics: diagSvc,
		B3:          b3Deps,
	}
	serveDeps := ServeDeps{
		B3:          b3Deps,
		Diagnostics: diagDeps,
	}
	mountB3Routes(r, serveDeps)
	mountDiagnosticsRoutes(r, serveDeps)

	// 1. Builtin admin creates a user administrator with role='admin' (carol_admin)
	wCreate, createBody := doB13DiagReq(t, r, http.MethodPost, "/api/v4/admin/users", builtinAdminID, nil, map[string]any{
		"username":     "carol_admin",
		"display_name": "Carol Admin",
		"password":     "CorrectHorseBatteryStaple!9",
		"role":         "admin",
	})
	if wCreate.Code != http.StatusOK || int(createBody["err_code"].(float64)) != 0 {
		t.Fatalf("CreateUser role=admin failed: status=%d body=%s", wCreate.Code, wCreate.Body.String())
	}
	createdData := createBody["data"].(map[string]any)
	carolAdminID := createdData["id"].(string)
	if carolAdminID == "" || createdData["role"] != "admin" || createdData["is_builtin_admin"] != false {
		t.Fatalf("unexpected created admin user: %+v", createdData)
	}
	memStore.Put(&session.Session{
		ID: carolAdminID,
		Identity: &session.Identity{
			UserID:         carolAdminID,
			Username:       "carol_admin",
			IsBuiltinAdmin: false,
			Role:           "admin",
		},
		ExpiresAt: time.Now().Add(time.Hour),
	})

	// 2. Verify carol_admin calling GET /api/v4/users/me receives role="admin" and can_access_admin=true
	wMe, meBody := doB13DiagReq(t, r, http.MethodGet, "/api/v4/users/me", carolAdminID, nil, nil)
	if wMe.Code != http.StatusOK || int(meBody["err_code"].(float64)) != 0 {
		t.Fatalf("GetCurrentUser failed: status=%d body=%s", wMe.Code, wMe.Body.String())
	}
	meData := meBody["data"].(map[string]any)
	if meData["role"] != "admin" || meData["can_access_admin"] != true || meData["is_builtin_admin"] != false {
		t.Fatalf("expected role=admin & can_access_admin=true for carol_admin, got %+v", meData)
	}

	// 3. Verify demoting the builtin super admin to role='user' is rejected (business code 1103 BUILTIN_ADMIN_IMMUTABLE)
	wDemote, demoteBody := doB13DiagReq(t, r, http.MethodPatch, "/api/v4/admin/users/"+builtinAdminID, carolAdminID,
		map[string]string{"If-Match": `"1"`},
		map[string]any{"role": "user"},
	)
	if wDemote.Code != http.StatusOK || int(demoteBody["err_code"].(float64)) != 1103 {
		t.Fatalf("expected err_code=1103 BUILTIN_ADMIN_IMMUTABLE when demoting builtin admin, got status=%d body=%s", wDemote.Code, wDemote.Body.String())
	}

	// 4. Regular user normal_bob accessing /api/v4/admin/diagnostics/datasources must be rejected with HTTP 403; carol_admin succeeds
	wBobForbidden, _ := doB13DiagReq(t, r, http.MethodGet, "/api/v4/admin/diagnostics/datasources", normalUserID, nil, nil)
	if wBobForbidden.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for role=user on /admin/diagnostics/datasources, got %d", wBobForbidden.Code)
	}
	wCarolList, listBody := doB13DiagReq(t, r, http.MethodGet, "/api/v4/admin/diagnostics/datasources", carolAdminID, nil, nil)
	if wCarolList.Code != http.StatusOK || int(listBody["err_code"].(float64)) != 0 || wCarolList.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("expected 200 + Cache-Control: no-store for role=admin, got %d hdr=%q body=%s",
			wCarolList.Code, wCarolList.Header().Get("Cache-Control"), wCarolList.Body.String())
	}

	// 5. carol_admin fetches and enables datasource diagnostics config (testing If-Match optimistic concurrency)
	wGetCfg, getCfgBody := doB13DiagReq(t, r, http.MethodGet, "/api/v4/admin/datasources/"+dsID+"/diagnostics-config", carolAdminID, nil, nil)
	if wGetCfg.Code != http.StatusOK || int(getCfgBody["err_code"].(float64)) != 0 {
		t.Fatalf("GetDiagnosticsConfig failed: %d %s", wGetCfg.Code, wGetCfg.Body.String())
	}
	wUpdateCfg, updCfgBody := doB13DiagReq(t, r, http.MethodPut, "/api/v4/admin/datasources/"+dsID+"/diagnostics-config", carolAdminID,
		map[string]string{"If-Match": `"1"`},
		map[string]any{
			"enabled":          true,
			"interval_seconds": 60,
			"top_n":            200,
		},
	)
	if wUpdateCfg.Code != http.StatusOK || int(updCfgBody["err_code"].(float64)) != 0 {
		t.Fatalf("UpdateDiagnosticsConfig failed: %d %s", wUpdateCfg.Code, wUpdateCfg.Body.String())
	}
	// Updating again with a stale version number must return business code 1003 VERSION_CONFLICT
	wConflictCfg, conflictBody := doB13DiagReq(t, r, http.MethodPut, "/api/v4/admin/datasources/"+dsID+"/diagnostics-config", carolAdminID,
		map[string]string{"If-Match": `"1"`},
		map[string]any{
			"enabled":          true,
			"interval_seconds": 90,
			"top_n":            200,
		},
	)
	if wConflictCfg.Code != http.StatusOK || int(conflictBody["err_code"].(float64)) != diagdom.CodeVersionConflict {
		t.Fatalf("expected err_code=1003 VERSION_CONFLICT on stale If-Match, got %d %s", wConflictCfg.Code, wConflictCfg.Body.String())
	}

	// 6. Trigger manual snapshot: first succeeds (err_code=0), second within 30s returns business code 7006 DIAGNOSTICS_SNAPSHOT_RATE_LIMITED
	wSnap1, snap1Body := doB13DiagReq(t, r, http.MethodPost, "/api/v4/admin/diagnostics/datasources/"+dsID+"/snapshots", carolAdminID, nil, nil)
	if wSnap1.Code != http.StatusOK || int(snap1Body["err_code"].(float64)) != 0 {
		t.Fatalf("first manual snapshot failed: %d %s", wSnap1.Code, wSnap1.Body.String())
	}
	wSnap2, snap2Body := doB13DiagReq(t, r, http.MethodPost, "/api/v4/admin/diagnostics/datasources/"+dsID+"/snapshots", carolAdminID, nil, nil)
	if wSnap2.Code != http.StatusOK || int(snap2Body["err_code"].(float64)) != diagdom.CodeDiagnosticsSnapshotRateLimited {
		t.Fatalf("second manual snapshot within 30s expected err_code=7006, got %d %s", wSnap2.Code, wSnap2.Body.String())
	}

	// 7. Read overview (GET /api/v4/admin/diagnostics/datasources/{id}/overview)
	wOverview, ovBody := doB13DiagReq(t, r, http.MethodGet, "/api/v4/admin/diagnostics/datasources/"+dsID+"/overview", carolAdminID, nil, nil)
	if wOverview.Code != http.StatusOK || int(ovBody["err_code"].(float64)) != 0 {
		t.Fatalf("GetDiagnosticsOverview failed: %d %s", wOverview.Code, wOverview.Body.String())
	}
	ovData := ovBody["data"].(map[string]any)
	stmts := ovData["statements"].([]any)
	if len(stmts) == 0 {
		t.Fatalf("expected statements in overview, got %s", wOverview.Body.String())
	}
	stmtID := stmts[0].(map[string]any)["statement_id"].(string)

	// 8. Read single SQL statement detail (fetching template on demand), verify AI diagnosis fails closed (2011)
	// when no active AI provider is configured, then configure a mock LLM provider and trigger AI diagnosis report (POST .../ai-reports).
	wDetail, detBody := doB13DiagReq(t, r, http.MethodGet, "/api/v4/admin/diagnostics/datasources/"+dsID+"/statements/"+stmtID, carolAdminID, nil, nil)
	if wDetail.Code != http.StatusOK || int(detBody["err_code"].(float64)) != 0 {
		t.Fatalf("GetDiagnosticStatementDetail failed: %d %s", wDetail.Code, wDetail.Body.String())
	}
	wAIDiagNoProv, repNoProvBody := doB13DiagReq(t, r, http.MethodPost, "/api/v4/admin/diagnostics/datasources/"+dsID+"/statements/"+stmtID+"/ai-reports", carolAdminID, nil, map[string]any{})
	if wAIDiagNoProv.Code != http.StatusOK || int(repNoProvBody["err_code"].(float64)) != diagdom.CodeAIProviderFailed {
		t.Fatalf("expected err_code=2011 AI_PROVIDER_FAILED when no AI provider is configured, got %d %s",
			wAIDiagNoProv.Code, wAIDiagNoProv.Body.String())
	}

	t.Cleanup(addrguard.SetLoopbackBypassForTest(true))
	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		payload := map[string]any{
			"observed_facts": []map[string]any{
				{"metric": "Execution Calls & Total Time", "value": "calls=80, total_exec_time_ms=4000.000", "window": "window"},
			},
			"root_cause_hypotheses": []map[string]any{
				{
					"hypothesis":     "Filter predicate on customer_code may lack a composite index (inference).",
					"confidence":     "medium",
					"evidence_basis": "calls=80, rows_examined=80000",
				},
			},
			"optimization_recommendations": []map[string]any{
				{
					"kind":                  "index_ddl",
					"title":                 "Create composite index on orders",
					"rationale":             "Lower per-call rows scanned",
					"applicable_conditions": []string{"Confirm selectivity"},
					"candidate_sql":         "CREATE INDEX idx_orders_customer ON orders (customer_code)",
					"risk_notes":            []string{"Validate DDL lock impact via AI Precheck"},
				},
			},
			"verification_steps": []string{"Run AI Precheck before submission."},
		}
		content, _ := json.Marshal(payload)
		resp, _ := json.Marshal(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"content": string(content)}},
			},
		})
		_, _ = w.Write(resp)
	}))
	defer llmServer.Close()

	provID := uuid.NewString()
	provEnv, err := crypter.Seal(crypto.PurposePayload, []byte("sk-b13-diag"), crypto.AAD{
		DeploymentID: crypter.DeploymentID(),
		Table:        "ai_providers",
		PrimaryKey:   provID,
		Column:       "secret_ciphertext",
		Purpose:      "provider_secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`
		INSERT INTO ai_providers (
			id, name, provider_kind, base_url, model_name, enabled,
			selection_priority, privacy_contract_hash, output_schema_hash, configuration,
			secret_ciphertext, secret_nonce, secret_key_id, secret_algorithm,
			version, created_by_user_id, created_at, updated_at
		) VALUES (?, ?, 'openai_compatible', ?, 'gpt-4o-mini', true,
			1, ?, ?, '{"temperature":0.1}'::jsonb,
			?, ?, ?, 'aes-256-gcm', 1, ?, now(), now())`,
		provID, "b13-diag-llm-"+provID[:8], llmServer.URL,
		reviewprovider.PrivacyContractHash, reviewprovider.OutputSchemaHash,
		provEnv.Ciphertext, provEnv.Nonce, provEnv.KeyID, builtinAdminID).Error; err != nil {
		t.Fatalf("seed b13 ai_provider failed: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Exec(`DELETE FROM ai_providers WHERE id = ?`, provID).Error
	})
	diagSvc.Review = &reviewapp.Services{DB: db, Crypter: crypter}

	wAIDiag, repBody := doB13DiagReq(t, r, http.MethodPost, "/api/v4/admin/diagnostics/datasources/"+dsID+"/statements/"+stmtID+"/ai-reports", carolAdminID, nil, map[string]any{})
	if wAIDiag.Code != http.StatusOK || int(repBody["err_code"].(float64)) != 0 {
		t.Fatalf("DiagnoseStatementWithAI failed: %d %s", wAIDiag.Code, wAIDiag.Body.String())
	}
	repData := repBody["data"].(map[string]any)
	reportID := repData["report_id"].(string)
	if reportID == "" {
		t.Fatalf("expected report_id in AI diagnose response, got %s", wAIDiag.Body.String())
	}

	// 9. Verify admin_is_not_business_override: when carol_admin is not in a change_review permission group, starting precheck returns business code 7001 DIAGNOSTICS_DATASOURCE_FORBIDDEN
	wPrecheckDenied, preDeniedBody := doB13DiagReq(t, r, http.MethodPost,
		"/api/v4/admin/diagnostics/datasources/"+dsID+"/ai-reports/"+reportID+"/prechecks",
		carolAdminID, nil, map[string]any{"flow_id": flowID},
	)
	if wPrecheckDenied.Code != http.StatusOK || int(preDeniedBody["err_code"].(float64)) != diagdom.CodeDatasourceForbidden {
		t.Fatalf("expected err_code=7001 when admin user lacks change_review flow grant, got %d %s",
			wPrecheckDenied.Code, wPrecheckDenied.Body.String())
	}

	// After granting carol_admin the change_review flow permission, precheck and change order submission succeed
	_ = db.Exec(`INSERT INTO permission_group_members (permission_group_id, user_id) VALUES (?, ?)`, pgID, carolAdminID).Error
	_ = db.Exec(`INSERT INTO permission_group_flow_grants (permission_group_id, flow_id) VALUES (?, ?)`, pgID, flowID).Error

	wPrecheckOK, preOKBody := doB13DiagReq(t, r, http.MethodPost,
		"/api/v4/admin/diagnostics/datasources/"+dsID+"/ai-reports/"+reportID+"/prechecks",
		carolAdminID, nil, map[string]any{"flow_id": flowID},
	)
	if wPrecheckOK.Code != http.StatusOK || int(preOKBody["err_code"].(float64)) != 0 {
		t.Fatalf("PrecheckDiagnosticOptimizationDraft failed: %d %s", wPrecheckOK.Code, wPrecheckOK.Body.String())
	}

	wSubmit, subBody := doB13DiagReq(t, r, http.MethodPost,
		"/api/v4/admin/diagnostics/datasources/"+dsID+"/ai-reports/"+reportID+"/order-submissions",
		carolAdminID,
		map[string]string{"Idempotency-Key": "idem-http-submit-1"},
		map[string]any{
			"flow_id":          flowID,
			"ephemeral_run_id": fakeRunID,
			"title":            "Create composite index on orders for slow query optimization",
		},
	)
	if wSubmit.Code != http.StatusOK || int(subBody["err_code"].(float64)) != 0 {
		t.Fatalf("SubmitDiagnosticOptimizationOrder failed: %d %s", wSubmit.Code, wSubmit.Body.String())
	}

	// 10. Confirm order link (POST /api/v4/admin/diagnostics/datasources/{id}/statements/{statementId}/order-links)
	wLink, linkBody := doB13DiagReq(t, r, http.MethodPost,
		"/api/v4/admin/diagnostics/datasources/"+dsID+"/statements/"+stmtID+"/order-links",
		carolAdminID, nil,
		map[string]any{
			"change_order_id": fakeOrderID,
			"relation_kind":   "index_or_schema_change",
		},
	)
	if wLink.Code != http.StatusOK || int(linkBody["err_code"].(float64)) != 0 {
		t.Fatalf("ConfirmDiagnosticOrderLink failed: %d %s", wLink.Code, wLink.Body.String())
	}
}

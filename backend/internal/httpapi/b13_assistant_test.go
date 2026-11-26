//go:build integration

// b13_assistant_test.go tests Database AI Assistant (BE-B13-AI-ASSISTANT) HTTP routes, SSE streaming,
// permission isolation, Cache-Control: no-store, If-Match optimistic concurrency, and idempotent order submission.
package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	assistantapp "github.com/cookieY/YearningX/backend/internal/assistant/application"
	assistantdomain "github.com/cookieY/YearningX/backend/internal/assistant/domain"
	assistanttool "github.com/cookieY/YearningX/backend/internal/assistant/tool"
	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	orderapp "github.com/cookieY/YearningX/backend/internal/changeorder/application"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/identity/session"
	"github.com/cookieY/YearningX/backend/internal/platform/testenv"
	"github.com/cookieY/YearningX/backend/internal/platform/testkit"
	reviewapp "github.com/cookieY/YearningX/backend/internal/review/application"
)

func openB13HTTPTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := testkit.OpenPG(t, testenv.WithDefaultPort(54332), testenv.WithDefaultDBName("yearning_b13_test"))
	for _, tbl := range []string{
		"ai_assistant_sql_drafts",
		"ai_assistant_context_summaries",
		"ai_assistant_messages",
		"ai_assistant_sessions",
		"ai_daily_budget_usage",
		"ai_providers",
		"query_grants",
		"query_sessions",
		"query_access_requests",
		"permission_group_flow_grants",
		"permission_group_members",
		"permission_groups",
		"flow_stages",
		"flows",
		"rule_sets",
		"datasources",
		"users",
	} {
		_ = db.Exec("TRUNCATE TABLE " + tbl + " CASCADE").Error
	}
	return db
}

type fakeB13HTTPCatalog struct{}

func (fakeB13HTTPCatalog) ListSchemas(_ context.Context, _ assistanttool.DatasourceTarget) ([]string, error) {
	return []string{"shop_db"}, nil
}

func (fakeB13HTTPCatalog) ListTables(_ context.Context, _ assistanttool.DatasourceTarget, _ string) ([]string, error) {
	return []string{"warehouses"}, nil
}

func (fakeB13HTTPCatalog) ListColumns(_ context.Context, _ assistanttool.DatasourceTarget, _, _ string) ([]database.MetadataColumn, error) {
	return []database.MetadataColumn{
		{ColumnName: "id", Ordinal: 1, DatabaseType: "bigint", Nullable: false, ColumnKey: "PRI", ColumnExtra: "auto_increment"},
		{ColumnName: "code", Ordinal: 2, DatabaseType: "varchar(64)", Nullable: false},
		{ColumnName: "title", Ordinal: 3, DatabaseType: "varchar(128)", Nullable: false},
	}, nil
}

func (fakeB13HTTPCatalog) ListIndexes(_ context.Context, _ assistanttool.DatasourceTarget, _, _ string) ([]database.MetadataIndex, error) {
	return []database.MetadataIndex{
		{IndexName: "PRIMARY", Primary: true, NonUnique: false, ColumnNames: []string{"id"}},
	}, nil
}

type fakeB13HTTPTestSynthesizer struct{}

func (fakeB13HTTPTestSynthesizer) Synthesize(ctx context.Context, in assistantapp.SynthesizeInput) (*assistantapp.SynthesizeOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.Contains(strings.ToLower(in.AnonymizedPrompt), "poem") {
		return &assistantapp.SynthesizeOutput{
			OutOfScope:         true,
			ResolvedCapability: string(assistantdomain.CapabilityOutOfScope),
			AssistantReply:     "Non-database requests are out of scope.",
		}, nil
	}
	if len(in.AnonymizedMetadata) == 0 {
		return &assistantapp.SynthesizeOutput{
			AssistantReply: "Please select a target datasource.",
			NeedsClarify:   true,
		}, nil
	}
	tbl := in.AnonymizedMetadata[0]
	qualTable := tbl.TableToken
	if tbl.SchemaToken != "" {
		qualTable = tbl.SchemaToken + "." + tbl.TableToken
	}
	var cols, vals []string
	idx := 0
	for _, c := range tbl.Columns {
		if c.DefaultBehavior == assistanttool.DefaultAutoIncrement || idx >= len(in.BoundPlaceholders) {
			continue
		}
		cols = append(cols, c.ColumnToken)
		vals = append(vals, in.BoundPlaceholders[idx])
		idx++
	}
	anonSQL := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", qualTable, strings.Join(cols, ", "), strings.Join(vals, ", "))
	reply := "Generated INSERT draft for " + qualTable + "."
	if in.OnDelta != nil {
		in.OnDelta("delta", reply)
	}
	return &assistantapp.SynthesizeOutput{
		AssistantReply: reply,
		AnonymizedSQL:  anonSQL,
		Assumptions:    []string{"Auto-increment id omitted."},
		NextSteps:      []string{"Run AI precheck before submitting."},
		InputTokens:    32,
		OutputTokens:   48,
	}, nil
}

func (fakeB13HTTPTestSynthesizer) Summarize(_ context.Context, _ []assistantapp.AnonymizedTurn) (string, int64, int64, error) {
	return "summary", 8, 4, nil
}

func makeB13Key32(seed byte) string {
	b := make([]byte, 32)
	for i := range b {
		b[i] = seed + byte(i)
	}
	return base64.StdEncoding.EncodeToString(b)
}

func newB13HTTPRouter(t *testing.T, db *gorm.DB) (*gin.Engine, string, string, string, string) {
	t.Helper()
	dir := t.TempDir()
	doc := fmt.Sprintf(`{"contract_version":1,"deployment_id":"b13-http-test","keys":[
		{"key_id":"pay-1","purpose":"payload","status":"encrypt_decrypt","created_at":"2026-11-07T00:00:00Z","key_base64":"%s"},
		{"key_id":"state-1","purpose":"oidc_state","status":"encrypt_decrypt","created_at":"2026-11-07T00:00:00Z","key_base64":"%s"},
		{"key_id":"ident-1","purpose":"identifier_token","status":"encrypt_decrypt","created_at":"2026-11-07T00:00:00Z","key_base64":"%s"},
		{"key_id":"fp-1","purpose":"sql_fingerprint","status":"encrypt_decrypt","created_at":"2026-11-07T00:00:00Z","key_base64":"%s"},
		{"key_id":"aff-1","purpose":"cursor_affinity","status":"encrypt_decrypt","created_at":"2026-11-07T00:00:00Z","key_base64":"%s"}
	]}`, makeB13Key32(1), makeB13Key32(2), makeB13Key32(3), makeB13Key32(4), makeB13Key32(5))
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
		t.Fatalf("NewCrypter: %v", err)
	}

	ownerID := uuid.NewString()
	otherUserID := uuid.NewString()
	dsID := uuid.NewString()
	flowID := uuid.NewString()
	rsID := uuid.NewString()
	pgID := uuid.NewString()

	for _, uid := range []struct{ id, name string }{{ownerID, "alice"}, {otherUserID, "bob"}} {
		if err := db.Exec(`
			INSERT INTO users (id, username, display_name, password_hash, is_builtin_admin, version, created_at, updated_at)
			VALUES (?, ?, ?, 'argon2id$test', false, 1, now(), now())
		`, uid.id, uid.name, uid.name).Error; err != nil {
			t.Fatalf("seed user: %v", err)
		}
	}
	if err := db.Exec(`
		INSERT INTO datasources (id, name, engine, compatibility_mode, deployment_kind, host, port, database_name, version_constraint, enabled, version, created_by_user_id, created_at, updated_at)
		VALUES (?, 'mysql-prod', 'mysql', 'mysql', 'native', '127.0.0.1', 3306, 'shop_db', '8.0', true, 1, ?, now(), now())
	`, dsID, ownerID).Error; err != nil {
		t.Fatalf("seed datasource: %v", err)
	}
	if err := db.Exec(`
		INSERT INTO rule_sets (id, name, enabled, rules, config_hash, version, created_by_user_id, created_at, updated_at)
		VALUES (?, 'default-mysql', true, '{}'::jsonb, repeat('a', 64), 1, ?, now(), now())
	`, rsID, ownerID).Error; err != nil {
		t.Fatalf("seed rule_set: %v", err)
	}
	if err := db.Exec(`
		INSERT INTO flows (id, name, flow_type, status, rule_set_id, version, created_by_user_id, created_at, updated_at)
		VALUES (?, 'prod-change-flow', 'change_review', 'enabled', ?, 1, ?, now(), now())
	`, flowID, rsID, ownerID).Error; err != nil {
		t.Fatalf("seed flow: %v", err)
	}
	if err := db.Exec(`
		INSERT INTO flow_stages (id, flow_id, position, datasource_id, schema_mapping_ciphertext, schema_mapping_nonce, schema_mapping_key_id, schema_mapping_algorithm)
		VALUES (?, ?, 1, ?, '\x01', '\x02', 'pay-1', 'aes-256-gcm')
	`, uuid.NewString(), flowID, dsID).Error; err != nil {
		t.Fatalf("seed flow_stage: %v", err)
	}
	if err := db.Exec(`
		INSERT INTO permission_groups (id, name, enabled, version, created_by_user_id, created_at, updated_at)
		VALUES (?, 'pg-devs', true, 1, ?, now(), now())
	`, pgID, ownerID).Error; err != nil {
		t.Fatalf("seed pg: %v", err)
	}
	if err := db.Exec(`
		INSERT INTO permission_group_members (permission_group_id, user_id)
		VALUES (?, ?)
	`, pgID, ownerID).Error; err != nil {
		t.Fatalf("seed pg member: %v", err)
	}
	if err := db.Exec(`
		INSERT INTO permission_group_flow_grants (permission_group_id, flow_id)
		VALUES (?, ?)
	`, pgID, flowID).Error; err != nil {
		t.Fatalf("seed flow grant: %v", err)
	}

	rec, err := auditlog.NewRecorder(db)
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	reviewSvc, err := reviewapp.NewServices(db, crypter, rec, nil)
	if err != nil {
		t.Fatalf("review NewServices: %v", err)
	}
	svc, err := assistantapp.NewServices(db, crypter, reviewSvc, nil, nil)
	if err != nil {
		t.Fatalf("NewServices: %v", err)
	}
	svc.Synthesizer = fakeB13HTTPTestSynthesizer{}
	svc.Authorizer.Catalog = fakeB13HTTPCatalog{}
	svc.PrecheckRunner = func(_ context.Context, _ assistantdomain.Actor, _ string, _ []byte, _, idemKey string) (string, string, error) {
		return uuid.NewSHA1(uuid.NameSpaceOID, []byte("http-precheck:"+idemKey)).String(), "ready", nil
	}
	svc.OrderSubmitter = func(_ context.Context, _ assistantdomain.Actor, in orderapp.SubmitOrderV2Input) (*orderapp.OrderView, error) {
		return &orderapp.OrderView{
			ID:            uuid.NewSHA1(uuid.NameSpaceOID, []byte("http-order:"+in.IdempotencyKey)).String(),
			DisplayNumber: "YN-20261107-000088",
		}, nil
	}

	deps := &AssistantDeps{Assistant: svc}
	store := session.NewMemoryStore()
	store.Put(&session.Session{
		ID:        ownerID,
		Identity:  &session.Identity{UserID: ownerID, Username: "alice"},
		ExpiresAt: time.Now().Add(time.Hour),
	})
	store.Put(&session.Session{
		ID:        otherUserID,
		Identity:  &session.Identity{UserID: otherUserID, Username: "bob"},
		ExpiresAt: time.Now().Add(time.Hour),
	})
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RequestIDMiddleware())
	// Map X-Test-User-ID Header to yearning_session Cookie to inject identity through real session.Middleware.
	r.Use(func(c *gin.Context) {
		uid := c.GetHeader("X-Test-User-ID")
		if uid == "" {
			uid = ownerID
		}
		c.Request.AddCookie(&http.Cookie{Name: "yearning_session", Value: uid})
		c.Next()
	})
	r.Use(session.Middleware(store, session.Options{CookieName: "yearning_session"}))
	api := r.Group("/api/v4/ai-assistant/sessions")
	api.GET("", deps.HandleListAssistantSessions)
	api.POST("", deps.HandleCreateAssistantSession)
	api.GET("/:session_id", deps.HandleGetAssistantSession)
	api.DELETE("/:session_id", deps.HandleDeleteAssistantSession)
	api.POST("/:session_id/messages", deps.HandleSendAssistantMessage)
	api.POST("/:session_id/messages/:message_id/cancellation", deps.HandleCancelAssistantMessageStream)
	api.POST("/:session_id/drafts/:draft_id/prechecks", deps.HandlePrecheckAssistantSqlDraft)
	api.POST("/:session_id/drafts/:draft_id/order-submissions", deps.HandleSubmitAssistantSqlDraftOrder)

	return r, ownerID, otherUserID, dsID, flowID
}

func doB13JSON(t *testing.T, r *gin.Engine, method, path string, payload any, headers map[string]string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var bodyReader *bytes.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}
		bodyReader = bytes.NewReader(raw)
	} else {
		bodyReader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, bodyReader)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var parsed map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("%s %s status=%d invalid json (%v): %s", method, path, w.Code, err, w.Body.String())
	}
	return w, parsed
}

func TestB13AssistantHTTPRoutesSSEAndSecurityGates(t *testing.T) {
	db := openB13HTTPTestDB(t)
	r, ownerID, otherUserID, dsID, flowID := newB13HTTPRouter(t, db)

	// 1. POST /api/v4/ai-assistant/sessions creates a session.
	w, createResp := doB13JSON(t, r, http.MethodPost, "/api/v4/ai-assistant/sessions", map[string]any{
		"title":                "HTTP Test Session",
		"active_datasource_id": dsID,
		"active_flow_id":       flowID,
		"active_schema_name":   "shop_db",
	}, map[string]string{"X-Test-User-ID": ownerID})
	if w.Code != http.StatusOK || int(createResp["err_code"].(float64)) != 0 {
		t.Fatalf("create session failed: %d %v", w.Code, createResp)
	}
	sessData := createResp["data"].(map[string]any)
	sessionID := sessData["id"].(string)

	// 2. GET /api/v4/ai-assistant/sessions lists paginated sessions.
	w, listResp := doB13JSON(t, r, http.MethodGet, "/api/v4/ai-assistant/sessions?limit=10", nil, map[string]string{"X-Test-User-ID": ownerID})
	if w.Code != http.StatusOK || int(listResp["err_code"].(float64)) != 0 {
		t.Fatalf("list sessions failed: %d %v", w.Code, listResp)
	}
	items := listResp["data"].(map[string]any)["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("expected 1 session in list, got %d", len(items))
	}

	// 3. POST /api/v4/ai-assistant/sessions/{id}/messages (Accept: text/event-stream SSE mode).
	ssePayload, _ := json.Marshal(map[string]any{
		"content":         "Insert a warehouse row into warehouses table with code=WH-SSE-01, title=NorthHub01, and generate SQL",
		"capability_hint": "sql_generate",
		"datasource_id":   dsID,
		"flow_id":         flowID,
		"schema_name":     "shop_db",
		"target_table":    "warehouses",
	})
	sseReq := httptest.NewRequest(http.MethodPost, "/api/v4/ai-assistant/sessions/"+sessionID+"/messages", bytes.NewReader(ssePayload))
	sseReq.Header.Set("Content-Type", "application/json")
	sseReq.Header.Set("Accept", "text/event-stream")
	sseReq.Header.Set("X-Test-User-ID", ownerID)
	sseW := httptest.NewRecorder()
	r.ServeHTTP(sseW, sseReq)
	if sseW.Code != http.StatusOK {
		t.Fatalf("SSE status = %d, body = %s", sseW.Code, sseW.Body.String())
	}
	if got := sseW.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("SSE Cache-Control = %q, want no-store", got)
	}
	if ct := sseW.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("SSE Content-Type = %q", ct)
	}
	sseBody := sseW.Body.String()
	if !strings.Contains(sseBody, "event: progress") || !strings.Contains(sseBody, "event: done") || !strings.Contains(sseBody, "WH-SSE-01") {
		t.Fatalf("unexpected SSE body: %s", sseBody)
	}

	// 4. GET /api/v4/ai-assistant/sessions/{id} verifies Cache-Control: no-store and draft response.
	w, detailResp := doB13JSON(t, r, http.MethodGet, "/api/v4/ai-assistant/sessions/"+sessionID, nil, map[string]string{"X-Test-User-ID": ownerID})
	if w.Code != http.StatusOK || int(detailResp["err_code"].(float64)) != 0 {
		t.Fatalf("get session detail failed: %d %v", w.Code, detailResp)
	}
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("detail Cache-Control = %q, want no-store", got)
	}
	detailData := detailResp["data"].(map[string]any)
	drafts := detailData["drafts"].([]any)
	if len(drafts) != 1 {
		t.Fatalf("expected 1 draft, got %d", len(drafts))
	}
	draft := drafts[0].(map[string]any)
	draftID := draft["id"].(string)
	sqlHMAC := draft["sql_hmac"].(string)
	revision := int64(draft["revision"].(float64))

	// 5. Cross-user IDOR access must uniformly return 1002 RESOURCE_NOT_FOUND.
	_, idorResp := doB13JSON(t, r, http.MethodGet, "/api/v4/ai-assistant/sessions/"+sessionID, nil, map[string]string{"X-Test-User-ID": otherUserID})
	if int(idorResp["err_code"].(float64)) != 1002 {
		t.Fatalf("expected IDOR err_code 1002, got %v", idorResp)
	}

	// 6. Out-of-scope request returns 6001 ASSISTANT_OUT_OF_SCOPE.
	_, oosResp := doB13JSON(t, r, http.MethodPost, "/api/v4/ai-assistant/sessions/"+sessionID+"/messages", map[string]any{
		"content": "Please write a poem about spring weather",
	}, map[string]string{"X-Test-User-ID": ownerID})
	if int(oosResp["err_code"].(float64)) != 6001 {
		t.Fatalf("expected 6001 for out-of-scope message, got %v", oosResp)
	}

	// 7. Unauthorized datasource request returns 6002 ASSISTANT_DATASOURCE_SCOPE_DENIED.
	_, unauthDsResp := doB13JSON(t, r, http.MethodPost, "/api/v4/ai-assistant/sessions/"+sessionID+"/messages", map[string]any{
		"content":         "Query the warehouses table",
		"capability_hint": "sql_generate",
		"datasource_id":   uuid.NewString(),
	}, map[string]string{"X-Test-User-ID": ownerID})
	if int(unauthDsResp["err_code"].(float64)) != 6002 {
		t.Fatalf("expected 6002 for unauthorized datasource, got %v", unauthDsResp)
	}

	// 8. Trigger precheck: tampered HMAC returns 6005 ASSISTANT_DRAFT_REVISION_STALE.
	_, badPrecheck := doB13JSON(t, r, http.MethodPost, "/api/v4/ai-assistant/sessions/"+sessionID+"/drafts/"+draftID+"/prechecks", map[string]any{
		"flow_id":  flowID,
		"revision": revision,
		"sql_hmac": strings.Repeat("f", 64),
	}, map[string]string{"X-Test-User-ID": ownerID})
	if int(badPrecheck["err_code"].(float64)) != 6005 {
		t.Fatalf("expected 6005 for tampered sql_hmac, got %v", badPrecheck)
	}

	// 9. Trigger real precheck and idempotently submit change order.
	_, precheckResp := doB13JSON(t, r, http.MethodPost, "/api/v4/ai-assistant/sessions/"+sessionID+"/drafts/"+draftID+"/prechecks", map[string]any{
		"flow_id":  flowID,
		"revision": revision,
		"sql_hmac": sqlHMAC,
	}, map[string]string{"X-Test-User-ID": ownerID, "Idempotency-Key": "b13-http-precheck-1"})
	if int(precheckResp["err_code"].(float64)) != 0 {
		t.Fatalf("precheck failed: %v", precheckResp)
	}
	runID := precheckResp["data"].(map[string]any)["ephemeral_run_id"].(string)

	_, submitResp := doB13JSON(t, r, http.MethodPost, "/api/v4/ai-assistant/sessions/"+sessionID+"/drafts/"+draftID+"/order-submissions", map[string]any{
		"flow_id":  flowID,
		"run_id":   runID,
		"revision": revision,
		"sql_hmac": sqlHMAC,
		"title":    "Insert NorthHub01 warehouse record",
	}, map[string]string{"X-Test-User-ID": ownerID, "Idempotency-Key": "b13-http-order-1"})
	if int(submitResp["err_code"].(float64)) != 0 {
		t.Fatalf("submit order failed: %v", submitResp)
	}
	orderData := submitResp["data"].(map[string]any)
	if orderData["display_number"] != "YN-20261107-000088" {
		t.Fatalf("unexpected display_number: %v", orderData["display_number"])
	}

	// 10. DELETE /api/v4/ai-assistant/sessions/{id} verifies If-Match optimistic concurrency and deletion.
	_, latestDetail := doB13JSON(t, r, http.MethodGet, "/api/v4/ai-assistant/sessions/"+sessionID, nil, map[string]string{"X-Test-User-ID": ownerID})
	latestVer := int64(latestDetail["data"].(map[string]any)["session"].(map[string]any)["version"].(float64))
	_, delResp := doB13JSON(t, r, http.MethodDelete, "/api/v4/ai-assistant/sessions/"+sessionID, nil, map[string]string{
		"X-Test-User-ID": ownerID,
		"If-Match":       fmt.Sprintf(`"%d"`, latestVer),
	})
	if int(delResp["err_code"].(float64)) != 0 {
		t.Fatalf("delete session failed: %v", delResp)
	}
}

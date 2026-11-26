//go:build integration

package application

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/assistant/domain"
	"github.com/cookieY/YearningX/backend/internal/assistant/persistence"
	assistanttool "github.com/cookieY/YearningX/backend/internal/assistant/tool"
	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	orderapp "github.com/cookieY/YearningX/backend/internal/changeorder/application"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/platform/testenv"
	"github.com/cookieY/YearningX/backend/internal/platform/testkit"
	reviewapp "github.com/cookieY/YearningX/backend/internal/review/application"
)

func openAssistantTestDB(t *testing.T) *gorm.DB {
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

func newTestCrypter(t *testing.T) *crypto.Crypter {
	t.Helper()
	return testkit.NewCrypter(t)
}

type fakeCatalog struct{}

func (f *fakeCatalog) ListSchemas(ctx context.Context, ds assistanttool.DatasourceTarget) ([]string, error) {
	return []string{"core_db", "analytics_db"}, nil
}

func (f *fakeCatalog) ListTables(ctx context.Context, ds assistanttool.DatasourceTarget, schema string) ([]string, error) {
	return []string{"warehouses", "orders"}, nil
}

func (f *fakeCatalog) ListColumns(ctx context.Context, ds assistanttool.DatasourceTarget, schema, table string) ([]database.MetadataColumn, error) {
	switch table {
	case "warehouses":
		return []database.MetadataColumn{
			{ColumnName: "id", Ordinal: 1, DatabaseType: "bigint", Nullable: false, ColumnKey: "PRI", ColumnExtra: "auto_increment", ColumnRemark: "SECRET_INTERNAL_ID"},
			{ColumnName: "code", Ordinal: 2, DatabaseType: "varchar(64)", Nullable: false, ColumnRemark: "Warehouse code"},
			{ColumnName: "name", Ordinal: 3, DatabaseType: "varchar(128)", Nullable: false, ColumnRemark: "Warehouse name"},
			{ColumnName: "city", Ordinal: 4, DatabaseType: "varchar(64)", Nullable: true, ColumnDeflt: "'Shanghai'"},
			{ColumnName: "created_at", Ordinal: 5, DatabaseType: "datetime", Nullable: false, ColumnDeflt: "CURRENT_TIMESTAMP"},
		}, nil
	case "orders":
		return []database.MetadataColumn{
			{ColumnName: "id", Ordinal: 1, DatabaseType: "bigint", Nullable: false, ColumnKey: "PRI", ColumnExtra: "auto_increment"},
			{ColumnName: "order_no", Ordinal: 2, DatabaseType: "varchar(64)", Nullable: false},
			{ColumnName: "customer_email", Ordinal: 3, DatabaseType: "varchar(128)", Nullable: false},
			{ColumnName: "status", Ordinal: 4, DatabaseType: "varchar(32)", Nullable: false, ColumnDeflt: "'pending'"},
			{ColumnName: "total_cents", Ordinal: 5, DatabaseType: "bigint", Nullable: false},
		}, nil
	default:
		return nil, nil
	}
}

func (f *fakeCatalog) ListIndexes(ctx context.Context, ds assistanttool.DatasourceTarget, schema, table string) ([]database.MetadataIndex, error) {
	return []database.MetadataIndex{
		{IndexName: "PRIMARY", Primary: true, NonUnique: false, ColumnNames: []string{"id"}},
	}, nil
}

type assistantTestFixture struct {
	db            *gorm.DB
	crypter       *crypto.Crypter
	svc           *Services
	userA         domain.Actor
	userB         domain.Actor
	dsChangeID    string
	dsQueryOnlyID string
	dsUnauthID    string
	changeFlowID  string
}

func newAssistantFixture(t *testing.T) *assistantTestFixture {
	t.Helper()
	db := openAssistantTestDB(t)
	crypter := newTestCrypter(t)

	userAID := uuid.NewString()
	userBID := uuid.NewString()
	for _, u := range []struct {
		id   string
		name string
	}{
		{userAID, "alice"},
		{userBID, "bob"},
	} {
		if err := db.Exec(`INSERT INTO users (id, username, display_name, password_hash, is_builtin_admin, version, created_at, updated_at)
			VALUES (?, ?, ?, 'argon2id$test', false, 1, now(), now())`, u.id, u.name, u.name).Error; err != nil {
			t.Fatalf("insert user %s: %v", u.name, err)
		}
	}

	dsChangeID := uuid.NewString()
	dsQueryOnlyID := uuid.NewString()
	dsUnauthID := uuid.NewString()
	for _, ds := range []struct {
		id     string
		name   string
		engine string
	}{
		{dsChangeID, "prod-mysql-core", "mysql"},
		{dsQueryOnlyID, "prod-pg-analytics", "postgresql"},
		{dsUnauthID, "restricted-finance-db", "mysql"},
	} {
		if err := db.Exec(`INSERT INTO datasources (id, name, engine, compatibility_mode, deployment_kind, host, port, database_name, version_constraint, enabled, version, created_by_user_id, created_at, updated_at)
			VALUES (?, ?, ?, ?, 'native', '127.0.0.1', 3306, 'core_db', '8.0', true, 1, ?, now(), now())`,
			ds.id, ds.name, ds.engine, ds.engine, userAID).Error; err != nil {
			t.Fatalf("insert datasource %s: %v", ds.name, err)
		}
	}

	// Grant userA change_review flow permission on dsChangeID.
	ruleSetID := uuid.NewString()
	if err := db.Exec(`INSERT INTO rule_sets (id, name, enabled, rules, config_hash, version, created_by_user_id, created_at, updated_at)
		VALUES (?, 'default-rules', true, '{}'::jsonb, repeat('a', 64), 1, ?, now(), now())`, ruleSetID, userAID).Error; err != nil {
		t.Fatalf("insert rule_set: %v", err)
	}
	flowID := uuid.NewString()
	if err := db.Exec(`INSERT INTO flows (id, name, flow_type, status, rule_set_id, version, created_by_user_id, created_at, updated_at)
		VALUES (?, 'change-flow', 'change_review', 'enabled', ?, 1, ?, now(), now())`, flowID, ruleSetID, userAID).Error; err != nil {
		t.Fatalf("insert flow: %v", err)
	}
	stageID := uuid.NewString()
	if err := db.Exec(`INSERT INTO flow_stages (id, flow_id, position, datasource_id, schema_mapping_ciphertext, schema_mapping_nonce, schema_mapping_key_id, schema_mapping_algorithm)
		VALUES (?, ?, 1, ?, '\x01', '\x02', 'pay-1', 'aes-256-gcm')`, stageID, flowID, dsChangeID).Error; err != nil {
		t.Fatalf("insert flow_stage: %v", err)
	}
	groupID := uuid.NewString()
	if err := db.Exec(`INSERT INTO permission_groups (id, name, enabled, version, created_by_user_id, created_at, updated_at)
		VALUES (?, 'devs', true, 1, ?, now(), now())`, groupID, userAID).Error; err != nil {
		t.Fatalf("insert permission_group: %v", err)
	}
	if err := db.Exec(`INSERT INTO permission_group_members (permission_group_id, user_id)
		VALUES (?, ?)`, groupID, userAID).Error; err != nil {
		t.Fatalf("insert permission_group_member: %v", err)
	}
	if err := db.Exec(`INSERT INTO permission_group_flow_grants (permission_group_id, flow_id)
		VALUES (?, ?)`, groupID, flowID).Error; err != nil {
		t.Fatalf("insert permission_group_flow_grant: %v", err)
	}

	// Grant userA active query_sessions + query_session_capabilities on dsQueryOnlyID (read-only query access, no change_review flow).
	queryFlowID := uuid.NewString()
	if err := db.Exec(`INSERT INTO flows (id, name, flow_type, status, version, created_by_user_id, created_at, updated_at)
		VALUES (?, 'query-flow', 'query_access', 'enabled', 1, ?, now(), now())`, queryFlowID, userAID).Error; err != nil {
		t.Fatalf("insert query flow: %v", err)
	}
	qSessID := uuid.NewString()
	if err := db.Exec(`INSERT INTO query_sessions (id, user_id_snapshot, username_snapshot, flow_id_snapshot, flow_name_snapshot, approval_required_snapshot, state, version, created_at, last_seen_at)
		VALUES (?, ?, 'alice', ?, 'query-flow', false, 'active', 1, now(), now())`,
		qSessID, userAID, queryFlowID).Error; err != nil {
		t.Fatalf("insert query_session: %v", err)
	}
	if err := db.Exec(`INSERT INTO query_session_capabilities (session_id, datasource_id, datasource_name_snapshot, datasource_identity_fingerprint, state, can_query, can_export)
		VALUES (?, ?, 'prod-pg-analytics', repeat('b', 64), 'active', true, false)`,
		qSessID, dsQueryOnlyID).Error; err != nil {
		t.Fatalf("insert query_session_capability: %v", err)
	}

	rec, err := auditlog.NewRecorder(db)
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	reviewSvc, err := reviewapp.NewServices(db, crypter, rec, nil)
	if err != nil {
		t.Fatalf("review NewServices: %v", err)
	}
	svc, err := NewServices(db, crypter, reviewSvc, nil, nil)
	if err != nil {
		t.Fatalf("NewServices: %v", err)
	}
	svc.Synthesizer = NewProviderSynthesizer(db, crypter, &DeterministicSynthesizer{})
	svc.Authorizer.Catalog = &fakeCatalog{}
	return &assistantTestFixture{
		db:            db,
		crypter:       crypter,
		svc:           svc,
		userA:         domain.Actor{UserID: userAID, Username: "alice"},
		userB:         domain.Actor{UserID: userBID, Username: "bob"},
		dsChangeID:    dsChangeID,
		dsQueryOnlyID: dsQueryOnlyID,
		dsUnauthID:    dsUnauthID,
		changeFlowID:  flowID,
	}
}

func TestSessionLifecycleEncryptionErasureAndQuota(t *testing.T) {
	fx := newAssistantFixture(t)
	ctx := context.Background()
	schemaName := "core_db"
	tableName := "warehouses"

	// 1. Create session and verify defaults.
	sess, err := fx.svc.CreateSession(ctx, fx.userA, CreateSessionInput{
		Title:              "Warehouse table insert consultation",
		ActiveDatasourceID: &fx.dsChangeID,
		ActiveFlowID:       &fx.changeFlowID,
		ActiveSchemaName:   &schemaName,
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if sess.Title != "Warehouse table insert consultation" || sess.State != string(domain.SessionStateActive) {
		t.Fatalf("unexpected session view: %+v", sess)
	}

	// 2. Run a SQL generation turn so messages and drafts are persisted encrypted.
	res, err := fx.svc.SendMessage(ctx, fx.userA, SendMessageInput{
		SessionID:      sess.ID,
		Content:        "Please insert a warehouse row into the warehouses table in core_db with code: WH-HZ-01, name: HangzhouHub01, city: Hangzhou, and generate SQL",
		CapabilityHint: string(domain.CapabilitySQLGenerate),
		DatasourceID:   &fx.dsChangeID,
		FlowID:         &fx.changeFlowID,
		SchemaName:     &schemaName,
		TargetTable:    &tableName,
	})
	if err != nil {
		t.Fatalf("SendMessage sql_generate: %v", err)
	}
	if res.SQLDraft == nil {
		t.Fatalf("expected SQL draft to be generated")
	}
	if !strings.Contains(strings.ToUpper(res.SQLDraft.SQLText), "INSERT INTO") || !strings.Contains(res.SQLDraft.SQLText, "'WH-HZ-01'") {
		t.Fatalf("unexpected restored SQL: %s", res.SQLDraft.SQLText)
	}

	// 3. Verify encryption at rest in PostgreSQL: ciphertext must not contain plaintext literals.
	var msgModel persistence.AIAssistantMessageModel
	if err := fx.db.Where("id = ?", res.UserMessage.ID).First(&msgModel).Error; err != nil {
		t.Fatalf("load message model: %v", err)
	}
	if len(msgModel.ContentCiphertext) == 0 || strings.Contains(string(msgModel.ContentCiphertext), "HangzhouHub01") {
		t.Fatalf("message content was not encrypted at rest")
	}
	var draftModel persistence.AIAssistantSQLDraftModel
	if err := fx.db.Where("id = ?", res.SQLDraft.ID).First(&draftModel).Error; err != nil {
		t.Fatalf("load draft model: %v", err)
	}
	if len(draftModel.SQLCiphertext) == 0 || strings.Contains(string(draftModel.SQLCiphertext), "WH-HZ-01") {
		t.Fatalf("draft SQL was not encrypted at rest")
	}

	// 4. Cross-user IDOR isolation: userB cannot read or delete userA's session.
	if _, err := fx.svc.GetSessionDetail(ctx, fx.userB, sess.ID); !hasErrCode(err, domain.CodeResourceNotFound) {
		t.Fatalf("expected 1002 for cross-user read, got %v", err)
	}
	if err := fx.svc.DeleteSession(ctx, fx.userB, sess.ID, res.Session.Version); !hasErrCode(err, domain.CodeResourceNotFound) {
		t.Fatalf("expected 1002 for cross-user delete, got %v", err)
	}

	// 5. DeleteSession soft-deletes session metadata and immediately erases message, draft, and summary ciphertext rows.
	if err := fx.svc.DeleteSession(ctx, fx.userA, sess.ID, res.Session.Version); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	var remainingMsgs int64
	fx.db.Model(&persistence.AIAssistantMessageModel{}).Where("session_id = ?", sess.ID).Count(&remainingMsgs)
	if remainingMsgs != 0 {
		t.Fatalf("expected all message ciphertext rows to be physically deleted on DeleteSession, got %d", remainingMsgs)
	}
	var remainingDrafts int64
	fx.db.Model(&persistence.AIAssistantSQLDraftModel{}).Where("session_id = ?", sess.ID).Count(&remainingDrafts)
	if remainingDrafts != 0 {
		t.Fatalf("expected all draft ciphertext rows to be physically deleted on DeleteSession, got %d", remainingDrafts)
	}
	var deletedSess persistence.AIAssistantSessionModel
	if err := fx.db.Where("id = ?", sess.ID).First(&deletedSess).Error; err != nil {
		t.Fatalf("load deleted session metadata: %v", err)
	}
	if deletedSess.State != string(domain.SessionStateDeleted) || deletedSess.DeletedAt == nil {
		t.Fatalf("expected session state=deleted and deleted_at set, got %+v", deletedSess)
	}

	// 6. Verify 50 active session quota enforcement (1012).
	for i := 0; i < domain.MaxActiveSessionsPerUser; i++ {
		if _, err := fx.svc.CreateSession(ctx, fx.userA, CreateSessionInput{Title: fmt.Sprintf("sess-%d", i)}); err != nil {
			t.Fatalf("CreateSession %d: %v", i, err)
		}
	}
	if _, err := fx.svc.CreateSession(ctx, fx.userA, CreateSessionInput{Title: "sess-overflow"}); !hasErrCode(err, domain.CodeSystemCapacity) {
		t.Fatalf("expected 1012 SYSTEM_CAPACITY_EXHAUSTED, got %v", err)
	}
}

func TestPermissionBoundariesOutOfScopeAndCredentialLeak(t *testing.T) {
	fx := newAssistantFixture(t)
	ctx := context.Background()
	schemaName := "core_db"
	tableName := "orders"

	sess, err := fx.svc.CreateSession(ctx, fx.userA, CreateSessionInput{Title: "Security gate test"})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// 1. Out-of-scope non-database prompt -> 6001.
	_, err = fx.svc.SendMessage(ctx, fx.userA, SendMessageInput{
		SessionID: sess.ID,
		Content:   "What is the weather today? Please write a poem for me",
	})
	if !hasErrCode(err, domain.CodeAssistantOutOfScope) {
		t.Fatalf("expected 6001 ASSISTANT_OUT_OF_SCOPE for non-DB prompt, got %v", err)
	}

	// 2. Forbidden SQL statement (TRUNCATE TABLE) -> 6001.
	truncateSQL := "TRUNCATE TABLE warehouses"
	_, err = fx.svc.SendMessage(ctx, fx.userA, SendMessageInput{
		SessionID:      sess.ID,
		Content:        "Please optimize this SQL statement",
		CapabilityHint: string(domain.CapabilitySQLOptimize),
		DatasourceID:   &fx.dsChangeID,
		SchemaName:     &schemaName,
		InputSQL:       &truncateSQL,
	})
	if !hasErrCode(err, domain.CodeAssistantOutOfScope) {
		t.Fatalf("expected 6001 ASSISTANT_OUT_OF_SCOPE for TRUNCATE TABLE, got %v", err)
	}

	// 3. Unauthorized datasource -> 6002 without leaking table existence.
	_, err = fx.svc.SendMessage(ctx, fx.userA, SendMessageInput{
		SessionID:      sess.ID,
		Content:        "Please query the warehouses table",
		CapabilityHint: string(domain.CapabilitySQLGenerate),
		DatasourceID:   &fx.dsUnauthID,
		SchemaName:     &schemaName,
		TargetTable:    &tableName,
	})
	if !hasErrCode(err, domain.CodeAssistantDatasourceForbidden) {
		t.Fatalf("expected 6002 ASSISTANT_DATASOURCE_FORBIDDEN for unauthorized datasource, got %v", err)
	}

	// 4. Query-only datasource allows SELECT generation, but rejects DML (INSERT) with 6002!
	selectRes, err := fx.svc.SendMessage(ctx, fx.userA, SendMessageInput{
		SessionID:      sess.ID,
		Content:        "Please generate a SELECT statement to query the orders table",
		CapabilityHint: string(domain.CapabilitySQLGenerate),
		DatasourceID:   &fx.dsQueryOnlyID,
		SchemaName:     &schemaName,
		TargetTable:    &tableName,
	})
	if err != nil {
		t.Fatalf("expected SELECT generation on query-only datasource to succeed, got %v", err)
	}
	if selectRes.SQLDraft == nil || selectRes.SQLDraft.StatementKind != string(domain.StmtSelect) {
		t.Fatalf("expected SELECT draft on query-only datasource, got %+v", selectRes.SQLDraft)
	}
	if selectRes.SQLDraft.RiskCategory != string(domain.RiskReadOnly) {
		t.Fatalf("expected risk_category=read_only for SELECT draft, got %s", selectRes.SQLDraft.RiskCategory)
	}

	_, err = fx.svc.SendMessage(ctx, fx.userA, SendMessageInput{
		SessionID:      sess.ID,
		Content:        "Please insert a new record into the orders table INSERT",
		CapabilityHint: string(domain.CapabilitySQLGenerate),
		DatasourceID:   &fx.dsQueryOnlyID,
		SchemaName:     &schemaName,
		TargetTable:    &tableName,
	})
	if !hasErrCode(err, domain.CodeAssistantDatasourceForbidden) {
		t.Fatalf("expected 6002 ASSISTANT_DATASOURCE_FORBIDDEN for DML on query-only datasource, got %v", err)
	}

	// 5. Credential leak in prompt -> 6003 before model invocation.
	_, err = fx.svc.SendMessage(ctx, fx.userA, SendMessageInput{
		SessionID:      sess.ID,
		Content:        "Database connection password=MySuperSecret12345, please query the orders table",
		CapabilityHint: string(domain.CapabilitySQLGenerate),
		DatasourceID:   &fx.dsChangeID,
		SchemaName:     &schemaName,
		TargetTable:    &tableName,
	})
	if !hasErrCode(err, domain.CodeAssistantAnonymizationFailed) {
		t.Fatalf("expected 6003 ASSISTANT_ANONYMIZATION_FAILED for credential in prompt, got %v", err)
	}
}

func TestOptimizeSQLCompactionConcurrencyAndOrderBridge(t *testing.T) {
	fx := newAssistantFixture(t)
	ctx := context.Background()
	schemaName := "core_db"
	tableName := "warehouses"

	sess, err := fx.svc.CreateSession(ctx, fx.userA, CreateSessionInput{
		Title:              "SQL optimization and order bridge test",
		ActiveDatasourceID: &fx.dsChangeID,
		ActiveFlowID:       &fx.changeFlowID,
		ActiveSchemaName:   &schemaName,
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// 1. Optimize SELECT * SQL and verify semantic boundary warnings & explicit projection.
	rawSQL := "SELECT * FROM orders WHERE customer_email = 'vip@example.com'"
	optRes, err := fx.svc.SendMessage(ctx, fx.userA, SendMessageInput{
		SessionID:      sess.ID,
		Content:        "Please analyze and optimize this SQL statement",
		CapabilityHint: string(domain.CapabilitySQLOptimize),
		DatasourceID:   &fx.dsChangeID,
		SchemaName:     &schemaName,
		InputSQL:       &rawSQL,
	})
	if err != nil {
		t.Fatalf("SendMessage sql_optimize: %v", err)
	}
	if optRes.SQLDraft == nil {
		t.Fatalf("expected optimized SQL draft")
	}
	if strings.Contains(optRes.SQLDraft.SQLText, "SELECT *") {
		t.Fatalf("expected SELECT * to be rewritten to explicit columns, got: %s", optRes.SQLDraft.SQLText)
	}
	if !strings.Contains(optRes.SQLDraft.SQLText, "'vip@example.com'") {
		t.Fatalf("expected literal 'vip@example.com' to be safely restored, got: %s", optRes.SQLDraft.SQLText)
	}
	if len(optRes.SQLDraft.SemanticWarnings) == 0 || len(optRes.SQLDraft.NextSteps) == 0 {
		t.Fatalf("expected semantic warnings and next steps on optimized draft: %+v", optRes.SQLDraft)
	}

	// 2. Single-turn budget overflow -> 6006.
	hugePrompt := "Please query the orders table " + strings.Repeat("SELECT order_no, customer_email, status, total_cents FROM orders WHERE status = 'pending' ", 150)
	hugePlan := strings.Repeat("Seq Scan on orders (cost=0.00..99999.00 rows=1000000 width=128) ", 350)
	_, err = fx.svc.SendMessage(ctx, fx.userA, SendMessageInput{
		SessionID:         sess.ID,
		Content:           hugePrompt,
		CapabilityHint:    string(domain.CapabilitySQLGenerate),
		DatasourceID:      &fx.dsChangeID,
		SchemaName:        &schemaName,
		ExecutionPlanText: &hugePlan,
	})
	if !hasErrCode(err, domain.CodeAssistantContextBudgetExceeded) {
		t.Fatalf("expected 6006 ASSISTANT_CONTEXT_BUDGET_EXCEEDED, got %v", err)
	}

	// 3. Multi-turn auto-compaction: run enough turns to exceed TriggerMessages (12), then run a SQL generation turn.
	var compactedSeen bool
	for i := 0; i < 6; i++ {
		turnRes, err := fx.svc.SendMessage(ctx, fx.userA, SendMessageInput{
			SessionID:      sess.ID,
			Content:        fmt.Sprintf("Turn %d question: How do I submit a change order and inspect AI review precheck?", i+1),
			CapabilityHint: string(domain.CapabilityPlatformHelp),
		})
		if err != nil {
			t.Fatalf("turn %d failed: %v", i+1, err)
		}
		if turnRes.ContextCompacted {
			compactedSeen = true
		}
	}
	compactTurnRes, err := fx.svc.SendMessage(ctx, fx.userA, SendMessageInput{
		SessionID:      sess.ID,
		Content:        "Please generate a SELECT statement for the orders table",
		CapabilityHint: string(domain.CapabilitySQLGenerate),
		DatasourceID:   &fx.dsChangeID,
		SchemaName:     &schemaName,
	})
	if err != nil {
		t.Fatalf("compaction trigger turn failed: %v", err)
	}
	if compactTurnRes.ContextCompacted {
		compactedSeen = true
	}
	if !compactedSeen {
		t.Fatalf("expected auto-compaction to trigger after exceeding message threshold")
	}
	detail, err := fx.svc.GetSessionDetail(ctx, fx.userA, sess.ID)
	if err != nil {
		t.Fatalf("GetSessionDetail: %v", err)
	}
	if detail.Session.CompactedThroughSeq == 0 {
		t.Fatalf("expected CompactedThroughSeq > 0 after auto-compaction")
	}
	if len(detail.Messages) < 16 {
		t.Fatalf("expected full raw message history to be preserved (>=16), got %d", len(detail.Messages))
	}

	// 4. Stream concurrency conflict (6007) and CancelMessageStream.
	_, cancel := context.WithCancel(ctx)
	fx.svc.streamMu.Lock()
	fx.svc.activeStreams[fx.userA.UserID] = &activeStreamEntry{
		sessionID: sess.ID,
		messageID: detail.Messages[len(detail.Messages)-1].ID,
		cancel:    cancel,
	}
	fx.svc.streamMu.Unlock()

	_, err = fx.svc.SendMessage(ctx, fx.userA, SendMessageInput{
		SessionID:      sess.ID,
		Content:        "Concurrent stream conflict test",
		CapabilityHint: string(domain.CapabilityPlatformHelp),
	})
	if !hasErrCode(err, domain.CodeAssistantStreamInProgress) {
		t.Fatalf("expected 6007 ASSISTANT_STREAM_IN_PROGRESS, got %v", err)
	}
	cancelView, err := fx.svc.CancelMessageStream(ctx, fx.userA, sess.ID, detail.Messages[len(detail.Messages)-1].ID)
	if err != nil || cancelView == nil {
		t.Fatalf("expected CancelMessageStream to succeed, got %+v, err=%v", cancelView, err)
	}

	// 5. Generate DML draft, test PrecheckDraft & SubmitDraftOrder with 6005 stale revision/HMAC guard.
	genRes, err := fx.svc.SendMessage(ctx, fx.userA, SendMessageInput{
		SessionID:      sess.ID,
		Content:        "Insert a warehouse row into the warehouses table in core_db with code: WH-BJ-09, name: BeijingHub09, city: Beijing",
		CapabilityHint: string(domain.CapabilitySQLGenerate),
		DatasourceID:   &fx.dsChangeID,
		FlowID:         &fx.changeFlowID,
		SchemaName:     &schemaName,
		TargetTable:    &tableName,
	})
	if err != nil || genRes.SQLDraft == nil {
		t.Fatalf("SendMessage DML draft: %v", err)
	}
	dmlDraft := genRes.SQLDraft

	// Submitting before precheck must fail with 6005 ASSISTANT_DRAFT_REVISION_STALE.
	_, err = fx.svc.SubmitDraftOrder(ctx, fx.userA, SubmitDraftOrderInput{
		SessionID:      sess.ID,
		DraftID:        dmlDraft.ID,
		FlowID:         fx.changeFlowID,
		RunID:          uuid.NewString(),
		Revision:       dmlDraft.Revision,
		SQLHMAC:        dmlDraft.SQLHMAC,
		Title:          "Add Beijing Hub 09 warehouse",
		IdempotencyKey: "idem-submit-before-precheck",
	})
	if !hasErrCode(err, domain.CodeAssistantDraftRevisionStale) {
		t.Fatalf("expected 6005 when submitting before precheck, got %v", err)
	}

	// Precheck with mismatched HMAC must fail with 6005.
	_, err = fx.svc.PrecheckDraft(ctx, fx.userA, PrecheckDraftInput{
		SessionID:      sess.ID,
		DraftID:        dmlDraft.ID,
		FlowID:         fx.changeFlowID,
		Revision:       dmlDraft.Revision,
		SQLHMAC:        strings.Repeat("0", 64),
		OutputLocale:   "zh-CN",
		IdempotencyKey: "idem-precheck-bad-hmac",
	})
	if !hasErrCode(err, domain.CodeAssistantDraftRevisionStale) {
		t.Fatalf("expected 6005 for mismatched SQL HMAC, got %v", err)
	}

	// Configure PrecheckRunner and OrderSubmitter hooks to verify formal bridge transitions.
	expectedRunID := uuid.NewString()
	expectedOrderID := uuid.NewString()
	fx.svc.PrecheckRunner = func(ctx context.Context, actor domain.Actor, flowID string, sql []byte, locale, idemKey string) (string, string, error) {
		return expectedRunID, "ready", nil
	}
	fx.svc.OrderSubmitter = func(ctx context.Context, actor domain.Actor, in orderapp.SubmitOrderV2Input) (*orderapp.OrderView, error) {
		return &orderapp.OrderView{
			ID:            expectedOrderID,
			DisplayNumber: "ORD-20261107-0001",
		}, nil
	}

	precheckRes, err := fx.svc.PrecheckDraft(ctx, fx.userA, PrecheckDraftInput{
		SessionID:      sess.ID,
		DraftID:        dmlDraft.ID,
		FlowID:         fx.changeFlowID,
		Revision:       dmlDraft.Revision,
		SQLHMAC:        dmlDraft.SQLHMAC,
		OutputLocale:   "zh-CN",
		IdempotencyKey: "idem-precheck-ok",
	})
	if err != nil {
		t.Fatalf("PrecheckDraft: %v", err)
	}
	if precheckRes.EphemeralRunID != expectedRunID || precheckRes.Draft.SubmissionState != string(domain.SubmissionPrecheckReady) {
		t.Fatalf("unexpected PrecheckDraft result: %+v", precheckRes)
	}

	orderView, err := fx.svc.SubmitDraftOrder(ctx, fx.userA, SubmitDraftOrderInput{
		SessionID:      sess.ID,
		DraftID:        dmlDraft.ID,
		FlowID:         fx.changeFlowID,
		RunID:          expectedRunID,
		Revision:       dmlDraft.Revision,
		SQLHMAC:        dmlDraft.SQLHMAC,
		Title:          "Add Beijing Hub 09 warehouse",
		IdempotencyKey: "idem-submit-ok",
	})
	if err != nil {
		t.Fatalf("SubmitDraftOrder: %v", err)
	}
	if orderView.ID != expectedOrderID || orderView.DisplayNumber != "ORD-20261107-0001" {
		t.Fatalf("unexpected OrderView: %+v", orderView)
	}
}

func TestMultiTurnPlatformHelpWithQuotedHistoryDoesNotTriggerFalsePositive6003(t *testing.T) {
	fx := newAssistantFixture(t)
	ctx := context.Background()
	adminActor := domain.Actor{UserID: fx.userA.UserID, Username: "alice", IsBuiltinAdmin: true}

	sess, err := fx.svc.CreateSession(ctx, adminActor, CreateSessionInput{
		Title: "Diagnostics permission consultation",
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	prompts := []string{
		"How do I use the SQL diagnostics page (/admin/diagnostics)?",
		`Can a "data_analyst" role or data_analyst user view /admin/diagnostics?`,
		"Which users are allowed to view SQL diagnostics?",
	}
	for idx, p := range prompts {
		res, err := fx.svc.SendMessage(ctx, adminActor, SendMessageInput{
			SessionID:      sess.ID,
			Content:        p,
			CapabilityHint: string(domain.CapabilityPlatformHelp),
		})
		if err != nil {
			t.Fatalf("turn %d (%q) failed: %v", idx+1, p, err)
		}
		if strings.TrimSpace(res.AssistantMessage.Content) == "" {
			t.Fatalf("turn %d returned empty assistant reply", idx+1)
		}
	}
}

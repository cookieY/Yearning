//go:build integration

// B3 integration tests: connect to real PostgreSQL and cover the four acceptance gates:
//
//	G1 Default-deny authorization and IDOR matrix (actor/constraint/admin_surface/horizontal)
//	G2 Admin five-failure lockout and CLI recovery (including session revocation and audit)
//	G3 Secrets never enter responses, logs, or events
//	G4 Template edits do not mutate frozen instances
//
// Environment variables follow B1 conventions (YEARNING_TEST_DATABASE_*); skipped when unconfigured.
package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/datasource"
	"github.com/cookieY/YearningX/backend/internal/identity"
	"github.com/cookieY/YearningX/backend/internal/identity/session"
	"github.com/cookieY/YearningX/backend/internal/platform/testkit"
	"github.com/cookieY/YearningX/backend/internal/workflow"
)

func integrationDSN(t *testing.T) string {
	t.Helper()
	return testkit.PGDSN(t)
}

// testClient holds cookies and CSRF tokens to simulate a browser session.
type testClient struct {
	t       *testing.T
	engine  *gin.Engine
	cookies map[string]string
}

func (cl *testClient) do(method, path string, body any, withCSRF bool) (int, map[string]any, string) {
	cl.t.Helper()
	var raw []byte
	if body != nil {
		var err error
		raw, err = json.Marshal(body)
		if err != nil {
			cl.t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	if withCSRF {
		if token := cl.cookies["yearning_csrf"]; token != "" {
			req.Header.Set("X-CSRF-Token", token)
		}
	}
	if len(cl.cookies) > 0 {
		parts := make([]string, 0, len(cl.cookies))
		for k, v := range cl.cookies {
			parts = append(parts, k+"="+v)
		}
		req.Header.Set("Cookie", strings.Join(parts, "; "))
	}
	rec := httptest.NewRecorder()
	cl.engine.ServeHTTP(rec, req)
	for _, c := range rec.Result().Cookies() {
		if c.Value == "" || c.MaxAge < 0 {
			delete(cl.cookies, c.Name)
		} else {
			cl.cookies[c.Name] = c.Value
		}
	}
	bodyText := rec.Body.String()
	var parsed map[string]any
	_ = json.Unmarshal([]byte(bodyText), &parsed)
	return rec.Code, parsed, bodyText
}

func (cl *testClient) login(username, password string) (int, map[string]any, string) {
	return cl.do("POST", "/api/v4/auth/login", map[string]any{
		"username": username, "password": password,
	}, false)
}

type b3Fixture struct {
	engine   *gin.Engine
	db       *gorm.DB
	svc      *identity.Service
	ws       *workflow.Service
	dss      *datasource.Service
	sessions *identity.SessionStore
	crypter  *crypto.Crypter
	logs     *observer.ObservedLogs
	admin    testClient
}

func newB3Fixture(t *testing.T) *b3Fixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db := testkit.OpenPG(t)
	truncateAll(t, db)
	crypter := testkit.NewCrypter(t)

	logger, logs := observedLogger()
	recorder, _ := auditlog.NewRecorder(db)
	sessions, err := identity.NewSessionStore(db, identity.DefaultSessionTTL)
	if err != nil {
		t.Fatal(err)
	}
	identitySvc, err := identity.NewService(db, sessions, crypter, recorder)
	if err != nil {
		t.Fatal(err)
	}
	workflowSvc, err := workflow.NewService(db, crypter, recorder)
	if err != nil {
		t.Fatal(err)
	}
	factory, err := datasource.NewFactory(crypter)
	if err != nil {
		t.Fatal(err)
	}
	datasourceSvc, err := datasource.NewService(db, factory, crypter, recorder)
	if err != nil {
		t.Fatal(err)
	}
	engine := NewServeRouter(ServeDeps{
		Logger: logger,
		Readiness: NewReadiness(logger,
			ReadinessCheck{Name: "db", Check: func(ctx context.Context) error {
				return db.WithContext(ctx).Raw("select 1").Scan(&[]int{}).Error
			}},
		),
		SessionStore:  sessions,
		SessionOption: session.Options{CookieName: "yearning_session", Secure: false, AnonymousAllowed: true},
		B3: &B3Deps{
			Identity: identitySvc, Sessions: sessions, Workflow: workflowSvc,
			Datasource: datasourceSvc, DB: db, Crypter: crypter, SecureCookie: false,
		},
	})
	return &b3Fixture{
		engine: engine, db: db, svc: identitySvc, ws: workflowSvc, dss: datasourceSvc,
		sessions: sessions, crypter: crypter, logs: logs,
		admin: testClient{t: t, engine: engine, cookies: map[string]string{}},
	}
}

func observedLogger() (*zap.Logger, *observer.ObservedLogs) {
	core, logs := observer.New(zapcore.InfoLevel)
	return zap.New(core), logs
}

func truncateAll(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, table := range []string{
		"ai_assistant_sql_drafts", "ai_assistant_context_summaries",
		"ai_assistant_messages", "ai_assistant_sessions",
		"sql_diagnostics_finding_evidences", "sql_diagnostics_findings",
		"sql_diagnostics_evidence_facts", "sql_diagnostics_candidates",
		"sql_diagnostics_runs", "datasource_diagnostics_configs",
		"diagnostics_instance_leases", "notification_rules",
		"in_app_notifications", "order_rollback_evidences",
		"ai_call_usage", "ai_provider_rate_windows", "ai_provider_rate_policies",
	} {
		_ = db.Exec("truncate table " + table + " cascade").Error
	}
	tables := []string{
		"audit_events", "domain_events", "outbox_events", "async_tasks",
		"query_grants", "query_sessions", "query_access_requests",
		"order_flow_snapshots", "order_stages", "change_orders", "change_drafts",
		"order_sql_payloads", "order_approval_steps", "order_approval_actors",
		"approval_decisions", "order_execution_actors", "order_comments",
		"execution_attempts", "execution_statements", "execution_schedules",
		"execution_verifications",
		"flow_approval_step_actors", "flow_execution_actors", "flow_approval_steps",
		"flow_stages", "flow_masking_rules", "flow_query_capabilities", "flows",
		"permission_group_flow_grants", "permission_group_members", "permission_groups",
		"datasource_credentials", "datasource_tls_materials", "datasources",
		"external_identities", "identity_providers", "auth_sessions", "admin_login_security",
		"users", "system_settings", "rule_sets",
		"rule_set_prompt_tools", "prompt_tools", "knowledge_entries",
		"review_tool_calls", "ai_usage_ledger", "ai_daily_budget_usage", "ai_providers",
		// Four domains mounted into test surface (S17.6-5): truncate announcement and notification tables as well.
		"announcement_revisions", "announcement_publication",
		"notification_channels", "notification_deliveries",
	}
	for _, table := range tables {
		if err := db.Exec("truncate table " + table + " cascade").Error; err != nil {
			t.Fatalf("truncate %s: %v", table, err)
		}
	}
}

func registerIntegrationDBCleanup(t *testing.T, db *gorm.DB) {
	t.Helper()
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("integration sql db: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
}

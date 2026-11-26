package httpapi

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/identity/session"
	queryapp "github.com/cookieY/YearningX/backend/internal/query/application"
)

func TestWriteQueryProblemSanitizesInternalErrors(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// Case 1: PostgreSQL metadata constraint error records only SQLSTATE + constraint name, never raw detail/DSN.
	w1 := httptest.NewRecorder()
	c1, _ := gin.CreateTestContext(w1)
	c1.Request = httptest.NewRequest(http.MethodPost, "/api/v4/query-access-requests", nil)
	pgErr := &pgconn.PgError{
		Code:           "23514",
		ConstraintName: "query_access_request_datasources_datasource_identity_finger_check",
		Message:        "secret-password-must-not-leak",
		Detail:         "Failing row contains (secret-dsn)",
	}
	writeQueryProblem(c1, pgErr)
	if w1.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w1.Code)
	}
	if len(c1.Errors) != 1 {
		t.Fatalf("c.Errors len = %d, want 1", len(c1.Errors))
	}
	logged1 := c1.Errors.String()
	if !strings.Contains(logged1, "sqlstate=23514") || !strings.Contains(logged1, "query_access_request_datasources_datasource_identity_finger_check") {
		t.Fatalf("expected sqlstate and constraint in sanitized error, got %q", logged1)
	}
	if strings.Contains(logged1, "secret-password") || strings.Contains(logged1, "secret-dsn") || strings.Contains(w1.Body.String(), "23514") {
		t.Fatalf("leaked sensitive detail in log=%q or body=%q", logged1, w1.Body.String())
	}

	// Case 2: PostgreSQL error without constraint name + generic non-PG error.
	if got := sanitizeB11InternalErr(&pgconn.PgError{Code: "40001"}).Error(); got != "metadata_db_error: sqlstate=40001" {
		t.Fatalf("unexpected sanitized pg error: %q", got)
	}
	w2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(w2)
	c2.Request = httptest.NewRequest(http.MethodPost, "/api/v4/query-sessions", nil)
	writeQueryProblem(c2, errors.New("postgres://admin:topsecret@10.0.0.1:5432/app"))
	if strings.Contains(c2.Errors.String(), "topsecret") || !strings.Contains(c2.Errors.String(), "b11_internal_error") {
		t.Fatalf("generic error not sanitized: %q", c2.Errors.String())
	}
}

var registerB11StubDriverOnce sync.Once

type b11StubDriver struct{}
type b11StubConn struct{}
type b11StubTx struct{}
type b11SingleIntRow struct{ done bool }

func (b11StubDriver) Open(string) (driver.Conn, error) { return b11StubConn{}, nil }
func (b11StubConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare unsupported")
}
func (b11StubConn) Close() error              { return nil }
func (b11StubConn) Begin() (driver.Tx, error) { return b11StubTx{}, nil }
func (b11StubConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return b11StubTx{}, nil
}
func (b11StubTx) Commit() error   { return nil }
func (b11StubTx) Rollback() error { return nil }

func b11MetadataPgErr() error {
	return &pgconn.PgError{
		Code:           "23514",
		ConstraintName: "b11_unit_constraint",
		Message:        "secret-password-in-driver-err",
	}
}

func (b11StubConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return nil, b11MetadataPgErr()
}

func (b11StubConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(query, "from flow_stages fs") {
		return &b11SingleIntRow{}, nil
	}
	return nil, b11MetadataPgErr()
}

func (r *b11SingleIntRow) Columns() []string { return []string{"count"} }
func (r *b11SingleIntRow) Close() error      { return nil }
func (r *b11SingleIntRow) Next(dest []driver.Value) error {
	if r.done {
		return io.EOF
	}
	r.done = true
	dest[0] = int64(1)
	return nil
}

type b11MemSessionStore struct {
	id *session.Identity
}

func (s b11MemSessionStore) Get(_ context.Context, token string) (*session.Session, error) {
	if token == "tok" {
		return &session.Session{ID: "tok", Identity: s.id}, nil
	}
	return nil, nil
}

func newHermeticB11DepsWithFailingDB(t *testing.T) (*B11Deps, string) {
	t.Helper()
	registerB11StubDriverOnce.Do(func() {
		sql.Register("b11_stub_pgerr", b11StubDriver{})
	})
	sqlDB, err := sql.Open("b11_stub_pgerr", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	gdb, err := gorm.Open(postgres.New(postgres.Config{
		Conn:                 sqlDB,
		PreferSimpleProtocol: true,
	}), &gorm.Config{
		DisableAutomaticPing: true,
		Logger:               gormlogger.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}

	makeKey := func(b byte) string {
		buf := make([]byte, 32)
		for i := range buf {
			buf[i] = b
		}
		return base64.StdEncoding.EncodeToString(buf)
	}
	keyPath := t.TempDir() + "/keyring.json"
	doc := fmt.Sprintf(`{"contract_version":1,"deployment_id":"b11-unit","keys":[
		{"key_id":"pay-1","purpose":"payload","status":"encrypt_decrypt","created_at":"2026-08-25T00:00:00Z","key_base64":"%s"},
		{"key_id":"id-1","purpose":"identifier_token","status":"encrypt_decrypt","created_at":"2026-08-25T00:00:00Z","key_base64":"%s"},
		{"key_id":"fp-1","purpose":"sql_fingerprint","status":"encrypt_decrypt","created_at":"2026-08-25T00:00:00Z","key_base64":"%s"},
		{"key_id":"cur-1","purpose":"cursor_affinity","status":"encrypt_decrypt","created_at":"2026-08-25T00:00:00Z","key_base64":"%s"}]}`,
		makeKey(1), makeKey(2), makeKey(3), makeKey(4))
	if err := os.WriteFile(keyPath, []byte(doc), 0o400); err != nil {
		t.Fatal(err)
	}
	ring, err := crypto.LoadKeyring(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	crypter, err := crypto.NewCrypter(ring)
	if err != nil {
		t.Fatal(err)
	}
	recorder, err := auditlog.NewRecorder(gdb)
	if err != nil {
		t.Fatal(err)
	}
	qSvc, err := queryapp.NewServices(gdb, crypter, recorder)
	if err != nil {
		t.Fatal(err)
	}
	return &B11Deps{Query: qSvc, DB: gdb}, uuid.NewString()
}

func TestB11HandlersSanitizeInternalDBErrors(t *testing.T) {
	gin.SetMode(gin.TestMode)
	deps, adminID := newHermeticB11DepsWithFailingDB(t)
	id1 := uuid.NewString()
	id2 := uuid.NewString()

	r := gin.New()
	var captured []string
	r.Use(func(c *gin.Context) {
		c.Next()
		captured = append(captured, c.Errors.String())
	})
	r.Use(session.Middleware(b11MemSessionStore{
		id: &session.Identity{UserID: adminID, Username: "admin", IsBuiltinAdmin: true},
	}, session.Options{CookieName: "yearning_session"}))

	r.GET("/flows/:flow_id/masking-rules", deps.HandleListFlowMaskingRules)
	r.PUT("/flows/:flow_id/masking-rules/:datasource_id", deps.HandleReplaceFlowMaskingRule)
	r.GET("/query-orders", deps.HandleListQueryOrders)
	r.GET("/query-access-requests", deps.HandleListQueryAccessRequests)
	r.POST("/query-access-requests", deps.HandleCreateQueryAccessRequest)
	r.GET("/query-access-requests/:request_id", deps.HandleGetQueryAccessRequest)
	r.POST("/query-access-requests/:request_id/decisions", deps.HandleDecideQueryAccess)
	r.POST("/query-access-requests/:request_id/withdrawal", deps.HandleWithdrawQueryAccessRequest)
	r.GET("/query-grants", deps.HandleListQueryGrants)
	r.GET("/query-grants/:grant_id", deps.HandleGetQueryGrant)
	r.POST("/query-grants/:grant_id/revocation", deps.HandleRevokeQueryGrant)
	r.POST("/query-grants/:grant_id/relinquishment", deps.HandleRelinquishQueryGrant)
	r.POST("/query-grants/:grant_id/renewals", deps.HandleRenewQueryGrant)
	r.GET("/query-sessions", deps.HandleListQuerySessions)
	r.POST("/query-sessions", deps.HandleCreateQuerySession)
	r.GET("/query-sessions/:session_id", deps.HandleGetQuerySession)
	r.POST("/query-sessions/:session_id/closure", deps.HandleCloseQuerySession)
	r.GET("/query-sessions/:session_id/schemas", deps.HandleListQuerySessionSchemas)
	r.GET("/query-sessions/:session_id/tables", deps.HandleListQuerySessionTables)
	r.GET("/query-sessions/:session_id/columns", deps.HandleListQuerySessionColumns)
	r.GET("/my/flows/:flow_id/datasources/:datasource_id/schemas", deps.HandleListMyFlowDatasourceSchemas)
	r.GET("/my/flows/:flow_id/datasources/:datasource_id/tables", deps.HandleListMyFlowDatasourceTables)
	r.GET("/my/flows/:flow_id/datasources/:datasource_id/columns", deps.HandleListMyFlowDatasourceColumns)
	r.POST("/query-sessions/:session_id/executions", deps.HandleExecuteSelect)

	cases := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/flows/" + id1 + "/masking-rules", ""},
		{http.MethodPut, "/flows/" + id1 + "/masking-rules/" + id2, `{"sensitive_columns":["phone"]}`},
		{http.MethodGet, "/query-orders", ""},
		{http.MethodGet, "/query-access-requests", ""},
		{http.MethodPost, "/query-access-requests", fmt.Sprintf(`{"flow_id":%q,"datasource_ids":[%q],"requested_until":"2026-12-01T00:00:00Z","reason":"r"}`, id1, id2)},
		{http.MethodGet, "/query-access-requests/" + id1, ""},
		{http.MethodPost, "/query-access-requests/" + id1 + "/decisions", `{"decision":"approved","comment":"ok"}`},
		{http.MethodPost, "/query-access-requests/" + id1 + "/withdrawal", `{"reason":"withdraw"}`},
		{http.MethodGet, "/query-grants", ""},
		{http.MethodGet, "/query-grants/" + id1, ""},
		{http.MethodPost, "/query-grants/" + id1 + "/revocation", `{"reason":"revoke"}`},
		{http.MethodPost, "/query-grants/" + id1 + "/relinquishment", `{"reason":"giveup"}`},
		{http.MethodPost, "/query-grants/" + id1 + "/renewals", `{"requested_until":"2026-12-01T00:00:00Z","reason":"renew"}`},
		{http.MethodGet, "/query-sessions", ""},
		{http.MethodPost, "/query-sessions", fmt.Sprintf(`{"flow_id":%q,"grant_id":%q,"datasource_ids":[%q]}`, id1, id1, id2)},
		{http.MethodGet, "/query-sessions/" + id1, ""},
		{http.MethodPost, "/query-sessions/" + id1 + "/closure", `{"reason":"done"}`},
		{http.MethodGet, "/query-sessions/" + id1 + "/schemas?datasource_id=" + id2, ""},
		{http.MethodGet, "/query-sessions/" + id1 + "/tables?datasource_id=" + id2 + "&schema_name=public", ""},
		{http.MethodGet, "/query-sessions/" + id1 + "/columns?datasource_id=" + id2 + "&schema_name=public&table_name=t", ""},
		{http.MethodGet, "/my/flows/" + id1 + "/datasources/" + id2 + "/schemas", ""},
		{http.MethodGet, "/my/flows/" + id1 + "/datasources/" + id2 + "/tables?schema_name=public", ""},
		{http.MethodGet, "/my/flows/" + id1 + "/datasources/" + id2 + "/columns?schema_name=public&table_name=t", ""},
		{http.MethodPost, "/query-sessions/" + id1 + "/executions", fmt.Sprintf(`{"datasource_id":%q,"schema_name":"public","sql":"select 1"}`, id2)},
	}

	for i, tc := range cases {
		captured = captured[:0]
		var body io.Reader
		if tc.body != "" {
			body = bytes.NewBufferString(tc.body)
		}
		req := httptest.NewRequest(tc.method, tc.path, body)
		req.AddCookie(&http.Cookie{Name: "yearning_session", Value: "tok"})
		if tc.body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("Idempotency-Key", fmt.Sprintf("idem-b11-%d", i))
		req.Header.Set("If-Match", `"1"`)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusInternalServerError {
			t.Fatalf("%s %s: status = %d, want 500 (body=%s)", tc.method, tc.path, w.Code, w.Body.String())
		}
		joined := strings.Join(captured, "\n")
		if !strings.Contains(joined, "sqlstate=23514") || !strings.Contains(joined, "b11_unit_constraint") {
			t.Fatalf("%s %s: expected sanitized sqlstate+constraint in c.Errors, got %q", tc.method, tc.path, joined)
		}
		if strings.Contains(joined, "secret-password") || strings.Contains(w.Body.String(), "secret-password") {
			t.Fatalf("%s %s: leaked sensitive driver message", tc.method, tc.path)
		}
	}
}

func TestHandleListQueryAccessRequestsInvalidScope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	deps := &B11Deps{}
	r := gin.New()
	r.GET("/query-access-requests", deps.HandleListQueryAccessRequests)

	req := httptest.NewRequest(http.MethodGet, "/query-access-requests?scope=bogus", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"err_code":1001`) {
		t.Fatalf("want err_code=1001 for invalid scope, got %s", w.Body.String())
	}
}

package httpapi

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	orderapp "github.com/cookieY/YearningX/backend/internal/changeorder/application"
	"github.com/cookieY/YearningX/backend/internal/identity"
	"github.com/cookieY/YearningX/backend/internal/identity/session"
	queryapp "github.com/cookieY/YearningX/backend/internal/query/application"
)

type devViewAsStubUser struct {
	id             string
	username       string
	displayName    string
	isBuiltinAdmin bool
	role           string
}

type devViewAsConnector struct {
	users map[string]devViewAsStubUser
}

func (c devViewAsConnector) Connect(context.Context) (driver.Conn, error) {
	return &devViewAsConn{users: c.users}, nil
}

func (devViewAsConnector) Driver() driver.Driver { return nil }

type devViewAsConn struct {
	users map[string]devViewAsStubUser
}

func (*devViewAsConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unused") }
func (*devViewAsConn) Close() error                        { return nil }
func (*devViewAsConn) Begin() (driver.Tx, error)           { return nil, errors.New("unused") }

func (c *devViewAsConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(query, `"users"`) {
		cols := []string{"id", "username", "display_name", "email", "is_builtin_admin", "role", "version", "created_at", "updated_at"}
		now := time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)
		rowOf := func(u devViewAsStubUser) []driver.Value {
			return []driver.Value{u.id, u.username, u.displayName, nil, u.isBuiltinAdmin, u.role, int64(1), now, now}
		}
		if len(args) > 0 && strings.Contains(query, "id =") {
			uid, _ := args[0].Value.(string)
			u, ok := c.users[uid]
			if !ok {
				return &devViewAsRows{cols: cols}, nil
			}
			return &devViewAsRows{cols: cols, rows: [][]driver.Value{rowOf(u)}}, nil
		}
		var rows [][]driver.Value
		for _, u := range c.users {
			rows = append(rows, rowOf(u))
		}
		return &devViewAsRows{cols: cols, rows: rows}, nil
	}
	return &devViewAsRows{cols: []string{}}, nil
}

type devViewAsRows struct {
	cols []string
	rows [][]driver.Value
	idx  int
}

func (r *devViewAsRows) Columns() []string { return r.cols }
func (r *devViewAsRows) Close() error      { return nil }
func (r *devViewAsRows) Next(dest []driver.Value) error {
	if r.idx >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.idx])
	r.idx++
	return nil
}

func newDevViewAsTestRouter(t *testing.T, enabled bool) (*gin.Engine, string, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	const (
		adminID = "0198da00-0000-7000-8000-000000000011"
		userID  = "0198da00-0000-7000-8000-000000000012"
	)
	users := map[string]devViewAsStubUser{
		adminID: {id: adminID, username: "admin", displayName: "Built-in Admin", isBuiltinAdmin: true, role: "admin"},
		userID:  {id: userID, username: "alice", displayName: "Alice", isBuiltinAdmin: false, role: "user"},
	}
	sqlDB := sql.OpenDB(devViewAsConnector{users: users})
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(gormpostgres.New(gormpostgres.Config{
		Conn:                 sqlDB,
		PreferSimpleProtocol: true,
	}), &gorm.Config{
		DisableAutomaticPing: true,
		Logger:               gormlogger.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := identity.NewSessionStore(db, identity.DefaultSessionTTL)
	if err != nil {
		t.Fatal(err)
	}
	recorder, err := auditlog.NewRecorder(db)
	if err != nil {
		t.Fatal(err)
	}
	identSvc, err := identity.NewService(db, sessions, nil, recorder)
	if err != nil {
		t.Fatal(err)
	}

	mem := session.NewMemoryStore()
	mem.Put(&session.Session{
		ID: "admin-sess",
		Identity: &session.Identity{
			UserID:         adminID,
			Username:       "admin",
			IsBuiltinAdmin: true,
			Role:           "admin",
		},
		ExpiresAt: time.Now().Add(time.Hour),
	})
	mem.Put(&session.Session{
		ID: "user-sess",
		Identity: &session.Identity{
			UserID:         userID,
			Username:       "alice",
			IsBuiltinAdmin: false,
			Role:           "user",
		},
		ExpiresAt: time.Now().Add(time.Hour),
	})

	logger := zap.NewNop()
	router := NewServeRouter(ServeDeps{
		StrictWiring:     true,
		DevViewAsEnabled: enabled,
		Logger:           logger,
		Readiness:        NewReadiness(logger),
		SessionStore:     mem,
		SessionOption: session.Options{
			CookieName:       sessionCookieName,
			AnonymousAllowed: true,
		},
		B3: &B3Deps{
			Identity: identSvc,
			DB:       db,
		},
	})
	return router, "admin-sess", "user-sess"
}

func doDevViewAsReq(t *testing.T, router *gin.Engine, path, sessionToken, headerValue string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if sessionToken != "" {
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessionToken})
	}
	if headerValue != "" {
		req.Header.Set(DevViewAsHeader, headerValue)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	return w.Code, body
}

func assertCurrentUserAdminClaims(t *testing.T, body map[string]any, wantBuiltin, wantCanAccessAdmin bool, wantRole string) {
	t.Helper()
	data, ok := body["data"].(map[string]any)
	if !ok {
		t.Fatalf("missing data object in /users/me response: %+v", body)
	}
	if got, _ := data["is_builtin_admin"].(bool); got != wantBuiltin {
		t.Fatalf("is_builtin_admin = %v, want %v (data=%+v)", got, wantBuiltin, data)
	}
	if got, _ := data["can_access_admin"].(bool); got != wantCanAccessAdmin {
		t.Fatalf("can_access_admin = %v, want %v (data=%+v)", got, wantCanAccessAdmin, data)
	}
	if got, _ := data["role"].(string); got != wantRole {
		t.Fatalf("role = %q, want %q (data=%+v)", got, wantRole, data)
	}
}

func TestDevViewAsMiddleware_ProductionIgnoresHeader(t *testing.T) {
	router, adminSess, userSess := newDevViewAsTestRouter(t, false)

	// Admin sending downgrade header in production mode stays full admin.
	code, meBody := doDevViewAsReq(t, router, "/api/v4/users/me", adminSess, "user")
	if code != http.StatusOK {
		t.Fatalf("GET /users/me = %d, want 200", code)
	}
	assertCurrentUserAdminClaims(t, meBody, true, true, "admin")

	code, _ = doDevViewAsReq(t, router, "/api/v4/admin/users", adminSess, "user")
	if code != http.StatusOK {
		t.Fatalf("GET /admin/users in production with View-As header = %d, want 200", code)
	}

	// Regular user attempting escalation header in production remains denied.
	code, _ = doDevViewAsReq(t, router, "/api/v4/admin/users", userSess, "admin")
	if code != http.StatusForbidden {
		t.Fatalf("GET /admin/users for regular user with admin header = %d, want 403", code)
	}
	code, meBody = doDevViewAsReq(t, router, "/api/v4/users/me", userSess, "admin")
	if code != http.StatusOK {
		t.Fatalf("GET /users/me for regular user = %d, want 200", code)
	}
	assertCurrentUserAdminClaims(t, meBody, false, false, "user")
}

func TestDevViewAsMiddleware_DevelopmentDowngradeAndNoEscalation(t *testing.T) {
	router, adminSess, userSess := newDevViewAsTestRouter(t, true)

	// Valid non-admin presets downgrade admin for both /users/me and RequireOperation admin endpoints.
	for _, header := range []string{"user", "approver", "executor", "  Approver "} {
		code, meBody := doDevViewAsReq(t, router, "/api/v4/users/me", adminSess, header)
		if code != http.StatusOK {
			t.Fatalf("GET /users/me (%q) = %d, want 200", header, code)
		}
		assertCurrentUserAdminClaims(t, meBody, false, false, "user")

		code, _ = doDevViewAsReq(t, router, "/api/v4/admin/users", adminSess, header)
		if code != http.StatusForbidden {
			t.Fatalf("GET /admin/users (%q) = %d, want 403", header, code)
		}
	}

	// Stored session identity must remain unmutated after downgraded requests.
	code, meBody := doDevViewAsReq(t, router, "/api/v4/users/me", adminSess, "")
	if code != http.StatusOK {
		t.Fatalf("GET /users/me without header = %d, want 200", code)
	}
	assertCurrentUserAdminClaims(t, meBody, true, true, "admin")
	code, _ = doDevViewAsReq(t, router, "/api/v4/admin/users", adminSess, "")
	if code != http.StatusOK {
		t.Fatalf("GET /admin/users without header = %d, want 200", code)
	}

	// Invalid / escalation values cannot escalate a regular user.
	for _, header := range []string{"admin", "superadmin", "unknown"} {
		code, _ = doDevViewAsReq(t, router, "/api/v4/admin/users", userSess, header)
		if code != http.StatusForbidden {
			t.Fatalf("regular user with header %q got %d on /admin/users, want 403", header, code)
		}
		code, meBody = doDevViewAsReq(t, router, "/api/v4/users/me", userSess, header)
		if code != http.StatusOK {
			t.Fatalf("regular user /users/me (%q) = %d, want 200", header, code)
		}
		assertCurrentUserAdminClaims(t, meBody, false, false, "user")
	}

	// Anonymous requests remain unauthenticated (401) regardless of header.
	for _, path := range []string{"/api/v4/users/me", "/api/v4/admin/users"} {
		code, _ = doDevViewAsReq(t, router, path, "", "user")
		if code != http.StatusUnauthorized {
			t.Fatalf("anonymous %s with View-As header = %d, want 401", path, code)
		}
		code, _ = doDevViewAsReq(t, router, path, "", "admin")
		if code != http.StatusUnauthorized {
			t.Fatalf("anonymous %s with admin View-As header = %d, want 401", path, code)
		}
	}
}

func contextWithViewAsPreset(t *testing.T, enabled bool, id *session.Identity, preset string) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if preset != "" {
		req.Header.Set(DevViewAsHeader, preset)
	}
	req = req.WithContext(session.WithIdentity(req.Context(), id))
	c.Request = req
	DevViewAsMiddleware(enabled)(c)
	return c
}

func hasOrderActorID(view *orderapp.OrderView, want string) (hasApproval bool, hasExecution bool) {
	for _, st := range view.Stages {
		for _, step := range st.ApprovalSteps {
			for _, a := range step.Actors {
				if a.ID == want {
					hasApproval = true
				}
			}
		}
		for _, a := range st.ExecutionActors {
			if a.ID == want {
				hasExecution = true
			}
		}
	}
	return hasApproval, hasExecution
}

func TestDevViewAsRoleProjectionsAndFiltering(t *testing.T) {
	const (
		adminID = "0198da00-0000-7000-8000-000000000011"
		otherID = "0198da00-0000-7000-8000-000000000012"
	)
	baseID := func() *session.Identity {
		return &session.Identity{UserID: adminID, Username: "owner", IsBuiltinAdmin: true, Role: "admin"}
	}
	sampleActors := func() []orderapp.ActorView {
		return []orderapp.ActorView{{ID: adminID, Username: "owner"}, {ID: otherID, Username: "other"}}
	}
	makeOrders := func() []*orderapp.OrderView {
		return []*orderapp.OrderView{
			{
				ID: "ord-self", SubmitterUserID: adminID,
				Stages: []orderapp.StageView{{
					ID:              "stg-1",
					ApprovalSteps:   []orderapp.StepView{{ID: "step-1", Actors: sampleActors()}},
					ExecutionActors: sampleActors(),
				}},
			},
			{
				ID: "ord-acting", SubmitterUserID: otherID,
				Stages: []orderapp.StageView{{
					ID:              "stg-2",
					ApprovalSteps:   []orderapp.StepView{{ID: "step-2", Actors: sampleActors()}},
					ExecutionActors: sampleActors(),
				}},
			},
		}
	}

	cases := []struct {
		name          string
		enabled       bool
		preset        string
		wantApprover  bool
		wantExecutor  bool
		wantActingRow bool
	}{
		{name: "production_ignores_user_header", enabled: false, preset: "user", wantApprover: true, wantExecutor: true, wantActingRow: true},
		{name: "dev_none", enabled: true, preset: "", wantApprover: true, wantExecutor: true, wantActingRow: true},
		{name: "dev_user", enabled: true, preset: "user", wantApprover: false, wantExecutor: false, wantActingRow: false},
		{name: "dev_approver", enabled: true, preset: "approver", wantApprover: true, wantExecutor: false, wantActingRow: true},
		{name: "dev_executor", enabled: true, preset: "executor", wantApprover: false, wantExecutor: true, wantActingRow: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := contextWithViewAsPreset(t, tc.enabled, baseID(), tc.preset)
			orig := makeOrders()
			projected := projectOrderForDevViewAs(c, orig[0])
			gotApp, gotExec := hasOrderActorID(projected, adminID)
			if gotApp != tc.wantApprover || gotExec != tc.wantExecutor {
				t.Fatalf("projectOrder actors=(%v,%v), want (%v,%v)", gotApp, gotExec, tc.wantApprover, tc.wantExecutor)
			}
			if projected.SubmitterUserID != adminID {
				t.Fatalf("SubmitterUserID altered to %q", projected.SubmitterUserID)
			}
			// Original order slices must remain unmutated.
			origApp, origExec := hasOrderActorID(orig[0], adminID)
			if !origApp || !origExec {
				t.Fatalf("source slices mutated by projectOrderForDevViewAs: (%v,%v)", origApp, origExec)
			}

			filtered := filterOrdersForDevViewAs(c, orig)
			wantCount := 1
			if tc.wantActingRow {
				wantCount = 2
			}
			if len(filtered) != wantCount {
				t.Fatalf("filterOrders len = %d, want %d", len(filtered), wantCount)
			}

			reqs := filterQueryAccessRequestsForDevViewAs(c, []queryapp.AccessRequestView{
				{ID: "r-self", RequesterUserID: adminID},
				{ID: "r-other", RequesterUserID: otherID},
			})
			grants := filterQueryGrantsForDevViewAs(c, []queryapp.GrantView{
				{ID: "g-self", RequesterUserID: adminID},
				{ID: "g-other", RequesterUserID: otherID},
			})
			qOrders := filterQueryOrdersForDevViewAs(c, []queryapp.QueryOrderEntry{
				{Kind: "access_request", ID: "r-self", Request: &queryapp.AccessRequestView{ID: "r-self", RequesterUserID: adminID}},
				{Kind: "access_request", ID: "r-other", Request: &queryapp.AccessRequestView{ID: "r-other", RequesterUserID: otherID}},
				{Kind: "session", ID: "s-self", Session: &queryapp.SessionView{ID: "s-self", UserID: adminID}},
			})
			wantQueryLen := 1
			wantQOrderLen := 2
			if tc.wantApprover {
				wantQueryLen = 2
				wantQOrderLen = 3
			}
			if len(reqs) != wantQueryLen || len(grants) != wantQueryLen || len(qOrders) != wantQOrderLen {
				t.Fatalf("query filtering len reqs=%d grants=%d orders=%d (want %d,%d,%d)",
					len(reqs), len(grants), len(qOrders), wantQueryLen, wantQueryLen, wantQOrderLen)
			}
		})
	}
}

func TestDevViewAsHandlerGuards_DifferentiateRolesInDevAndAllowInProd(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const (
		adminID = "0198da00-0000-7000-8000-000000000011"
		uuidArg = "0198da00-0000-7000-8000-000000000099"
	)

	newGuardRouter := func(enabled bool) *gin.Engine {
		r := gin.New()
		r.Use(func(c *gin.Context) {
			id := &session.Identity{UserID: adminID, Username: "owner", IsBuiltinAdmin: true, Role: "admin"}
			c.Request = c.Request.WithContext(session.WithIdentity(c.Request.Context(), id))
			c.Next()
		})
		r.Use(DevViewAsMiddleware(enabled))
		b9 := &B9Deps{}
		b10 := &B10Deps{}
		b11 := &B11Deps{}
		r.POST("/api/v4/change-orders/:order_id/approval-decisions", b9.HandleDecideOrder)
		r.POST("/api/v4/change-orders/:order_id/execution-attempts", b10.HandleCreateExecutionAttempt)
		r.POST("/api/v4/execution-attempts/:attempt_id/cancellation", b10.HandleCancelExecutionAttempt)
		r.POST("/api/v4/execution-attempts/:attempt_id/verifications", b10.HandleCreateExecutionVerification)
		r.POST("/api/v4/change-orders/:order_id/execution-schedules", b11.HandleCreateExecutionSchedule)
		r.POST("/api/v4/change-orders/:order_id/execution-schedules/cancellation", b11.HandleCancelExecutionSchedule)
		r.POST("/api/v4/query-access-requests/:request_id/decisions", b11.HandleDecideQueryAccess)
		r.POST("/api/v4/query-grants/:grant_id/revocations", b11.HandleRevokeQueryGrant)
		return r
	}

	callEndpoint := func(r *gin.Engine, path, payload, preset string) (int, map[string]any) {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("If-Match", `"1"`)
		req.Header.Set("Idempotency-Key", "viewas-guard-key")
		if preset != "" {
			req.Header.Set(DevViewAsHeader, preset)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		var parsed map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &parsed)
		return w.Code, parsed
	}

	devRouter := newGuardRouter(true)
	for _, preset := range []string{"user", "executor"} {
		code, body := callEndpoint(devRouter, "/api/v4/change-orders/"+uuidArg+"/approval-decisions", `{"decision":"approve"}`, preset)
		if code != http.StatusOK || body["err_code"] != float64(3001) {
			t.Fatalf("preset=%s DecideOrder got (%d, %v), want (200, err_code=3001)", preset, code, body)
		}
		for _, qPath := range []string{
			"/api/v4/query-access-requests/" + uuidArg + "/decisions",
			"/api/v4/query-grants/" + uuidArg + "/revocations",
		} {
			qCode, qBody := callEndpoint(devRouter, qPath, `{"decision":"approve","reason":"revoke"}`, preset)
			if qCode != http.StatusOK || qBody["err_code"] != float64(1002) {
				t.Fatalf("preset=%s %s got (%d, %v), want (200, err_code=1002)", preset, qPath, qCode, qBody)
			}
		}
	}

	for _, preset := range []string{"user", "approver"} {
		for _, execCall := range []struct {
			path    string
			payload string
		}{
			{"/api/v4/change-orders/" + uuidArg + "/execution-attempts", `{}`},
			{"/api/v4/execution-attempts/" + uuidArg + "/cancellation", `{"reason":"cancel"}`},
			{"/api/v4/execution-attempts/" + uuidArg + "/verifications", `{"result":"confirmed_succeeded","reason":"ok","evidence":[{"kind":"text","content":"done"}]}`},
			{"/api/v4/change-orders/" + uuidArg + "/execution-schedules", `{"scheduled_for":"2026-12-01T00:00:00Z"}`},
			{"/api/v4/change-orders/" + uuidArg + "/execution-schedules/cancellation", `{}`},
		} {
			eCode, _ := callEndpoint(devRouter, execCall.path, execCall.payload, preset)
			if eCode != http.StatusForbidden {
				t.Fatalf("preset=%s %s got %d, want 403", preset, execCall.path, eCode)
			}
		}
	}

	// Production router ignores downgrade headers so guards never trip on X-Yearning-Dev-View-As.
	prodRouter := newGuardRouter(false)
	func() {
		defer func() {
			// Nil service pointer panic proves handler proceeded past the DevViewAs guard in production.
			if recover() == nil {
				t.Fatalf("expected production DecideOrder with nil service to bypass DevViewAs guard")
			}
		}()
		_, _ = callEndpoint(prodRouter, "/api/v4/change-orders/"+uuidArg+"/approval-decisions", `{"decision":"approve"}`, "user")
	}()
}

func TestDevViewAsQueryAccessEligibilityStripped(t *testing.T) {
	gin.SetMode(gin.TestMode)
	selfID := "11111111-1111-4111-8111-111111111111"
	otherID := "22222222-2222-4222-8222-222222222222"

	r := gin.New()
	r.Use(func(c *gin.Context) {
		id := &session.Identity{UserID: selfID, Username: "admin", Role: "admin", IsBuiltinAdmin: true}
		c.Request = c.Request.WithContext(session.WithIdentity(c.Request.Context(), id))
		c.Next()
	})
	r.Use(DevViewAsMiddleware(true))
	b11 := &B11Deps{}
	r.GET("/api/v4/query-access-requests", b11.HandleListQueryAccessRequests)
	r.GET("/test/filter-req", func(c *gin.Context) {
		items := []queryapp.AccessRequestView{
			{ID: "req-self", RequesterUserID: selfID, State: "access_pending", CanDecide: true},
			{ID: "req-other", RequesterUserID: otherID, State: "access_pending", CanDecide: true},
		}
		writePage(c, filterQueryAccessRequestsForDevViewAs(c, items), nil)
	})
	r.GET("/test/filter-grant", func(c *gin.Context) {
		items := []queryapp.GrantView{
			{ID: "grant-self", RequesterUserID: selfID, State: "active", CanRevoke: true},
			{ID: "grant-other", RequesterUserID: otherID, State: "active", CanRevoke: true},
		}
		writePage(c, filterQueryGrantsForDevViewAs(c, items), nil)
	})
	r.GET("/test/write-req-self", func(c *gin.Context) {
		writeQueryAccessRequestForDevViewAs(c, &queryapp.AccessRequestView{
			ID: "req-self", RequesterUserID: selfID, State: "access_pending", CanDecide: true,
		})
	})
	r.GET("/test/write-req-other", func(c *gin.Context) {
		writeQueryAccessRequestForDevViewAs(c, &queryapp.AccessRequestView{
			ID: "req-other", RequesterUserID: otherID, State: "access_pending", CanDecide: true,
		})
	})
	r.GET("/test/write-grant-self", func(c *gin.Context) {
		writeQueryGrantForDevViewAs(c, &queryapp.GrantView{
			ID: "grant-self", RequesterUserID: selfID, State: "active", CanRevoke: true,
		})
	})

	r.GET("/test/effective-scope", func(c *gin.Context) {
		eff, empty := effectiveQueryAccessScopeForDevViewAs(c, c.Query("scope"))
		WriteSuccess(c, gin.H{"effective": eff, "empty": empty})
	})

	for _, preset := range []string{"user", "executor"} {
		// scope=reviewer short-circuits to empty page; omitted or scope=all narrows to requester pre-pagination.
		for _, tc := range []struct {
			in   string
			want string
		}{
			{"", `"effective":"requester","empty":false`},
			{"all", `"effective":"requester","empty":false`},
			{"requester", `"effective":"requester","empty":false`},
			{"reviewer", `"effective":"","empty":true`},
		} {
			sReq := httptest.NewRequest(http.MethodGet, "/test/effective-scope?scope="+tc.in, nil)
			sReq.Header.Set(DevViewAsHeader, preset)
			sW := httptest.NewRecorder()
			r.ServeHTTP(sW, sReq)
			if !strings.Contains(sW.Body.String(), tc.want) {
				t.Fatalf("preset=%s scope=%q: got %s, want %s", preset, tc.in, sW.Body.String(), tc.want)
			}
		}

		// scope=reviewer short-circuits to empty page without touching DB when approval is stripped.
		req := httptest.NewRequest(http.MethodGet, "/api/v4/query-access-requests?scope=reviewer", nil)
		req.Header.Set(DevViewAsHeader, preset)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"items":[]`) {
			t.Fatalf("preset=%s scope=reviewer: code=%d body=%s", preset, w.Code, w.Body.String())
		}

		req2 := httptest.NewRequest(http.MethodGet, "/test/filter-req", nil)
		req2.Header.Set(DevViewAsHeader, preset)
		w2 := httptest.NewRecorder()
		r.ServeHTTP(w2, req2)
		if !strings.Contains(w2.Body.String(), `"req-self"`) || strings.Contains(w2.Body.String(), `"req-other"`) || strings.Contains(w2.Body.String(), `"can_decide":true`) {
			t.Fatalf("preset=%s filter-req must keep only self with can_decide=false: %s", preset, w2.Body.String())
		}

		req3 := httptest.NewRequest(http.MethodGet, "/test/filter-grant", nil)
		req3.Header.Set(DevViewAsHeader, preset)
		w3 := httptest.NewRecorder()
		r.ServeHTTP(w3, req3)
		if !strings.Contains(w3.Body.String(), `"grant-self"`) || strings.Contains(w3.Body.String(), `"grant-other"`) || strings.Contains(w3.Body.String(), `"can_revoke":true`) {
			t.Fatalf("preset=%s filter-grant must keep only self with can_revoke=false: %s", preset, w3.Body.String())
		}

		req4 := httptest.NewRequest(http.MethodGet, "/test/write-req-self", nil)
		req4.Header.Set(DevViewAsHeader, preset)
		w4 := httptest.NewRecorder()
		r.ServeHTTP(w4, req4)
		if !strings.Contains(w4.Body.String(), `"can_decide":false`) {
			t.Fatalf("preset=%s write-req-self must strip can_decide to false: %s", preset, w4.Body.String())
		}

		req5 := httptest.NewRequest(http.MethodGet, "/test/write-req-other", nil)
		req5.Header.Set(DevViewAsHeader, preset)
		w5 := httptest.NewRecorder()
		r.ServeHTTP(w5, req5)
		if !strings.Contains(w5.Body.String(), `"err_code":1002`) {
			t.Fatalf("preset=%s write-req-other must return 1002: %s", preset, w5.Body.String())
		}

		req6 := httptest.NewRequest(http.MethodGet, "/test/write-grant-self", nil)
		req6.Header.Set(DevViewAsHeader, preset)
		w6 := httptest.NewRecorder()
		r.ServeHTTP(w6, req6)
		if !strings.Contains(w6.Body.String(), `"can_revoke":false`) {
			t.Fatalf("preset=%s write-grant-self must strip can_revoke to false: %s", preset, w6.Body.String())
		}
	}
}

package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/cookieY/YearningX/backend/internal/identity/session"
)

func TestOperationsDashboardForUserScopesQueriesAndBypassesAdminCache(t *testing.T) {
	st := &opsStubState{}
	gdb := newOpsStubDB(t, st)
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	svc := &Service{
		DB:  gdb,
		Now: func() time.Time { return now },
	}

	// Prime the admin cache first.
	adminView, err := svc.OperationsDashboard(context.Background(), 30)
	if err != nil {
		t.Fatalf("admin OperationsDashboard failed: %v", err)
	}
	if adminView.UserTotal != 10 || adminView.DatasourceTotal != 4 {
		t.Fatalf("unexpected admin totals: %+v", adminView)
	}

	st.mu.Lock()
	st.queries = nil
	st.mu.Unlock()

	memberView, err := svc.OperationsDashboardForUser(context.Background(), "0198da00-0000-7000-8000-000000000055", 30)
	if err != nil {
		t.Fatalf("OperationsDashboardForUser failed: %v", err)
	}
	if memberView.UserTotal != 0 || memberView.DatasourceTotal != 0 {
		t.Fatalf("non-admin view must not expose platform user/datasource totals: %+v", memberView)
	}

	st.mu.Lock()
	queries := append([]string(nil), st.queries...)
	st.mu.Unlock()
	for _, q := range queries {
		lq := strings.ToLower(q)
		if strings.Contains(lq, "from users") || strings.Contains(lq, "from datasources") {
			t.Fatalf("scoped member query touched global users/datasources table: %s", q)
		}
	}
	joined := strings.ToLower(strings.Join(queries, "\n"))
	for _, want := range []string{
		"where user_id_snapshot =",
		"o.submitter_user_id_snapshot =",
		"a_u.reviewer_user_id_snapshot =",
		"ea_e.executor_user_id_snapshot =",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("scoped member queries missing predicate %q; queries:\n%s", want, joined)
		}
	}

	// Calling InvalidateOperationsCache must drop cached admin snapshot so next call hits DB again.
	before := st.txStarts.Load()
	svc.InvalidateOperationsCache()
	if _, err := svc.OperationsDashboard(context.Background(), 30); err != nil {
		t.Fatalf("OperationsDashboard after invalidate failed: %v", err)
	}
	if st.txStarts.Load() != before+1 {
		t.Fatalf("expected DB query after cache invalidation, txStarts=%d want %d", st.txStarts.Load(), before+1)
	}
}

func TestOperationsHandlerDispatchesByAdminRole(t *testing.T) {
	st := &opsStubState{}
	gdb := newOpsStubDB(t, st)
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	svc := &Service{
		DB:  gdb,
		Now: func() time.Time { return now },
	}
	gin.SetMode(gin.TestMode)

	callAs := func(id *session.Identity) OperationsView {
		mem := session.NewMemoryStore()
		mem.Put(&session.Session{
			ID:        "tok-" + id.UserID,
			ExpiresAt: time.Now().Add(time.Hour),
			Identity:  id,
		})
		r := gin.New()
		r.Use(session.Middleware(mem, session.Options{CookieName: "yearning_session"}))
		RegisterRoutes(r, &Deps{
			Service: svc,
			Guard:   func(_ string) gin.HandlerFunc { return func(c *gin.Context) { c.Next() } },
		})
		req := httptest.NewRequest(http.MethodGet, "/admin/dashboard/operations", nil)
		req.AddCookie(&http.Cookie{Name: "yearning_session", Value: "tok-" + id.UserID})
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body=%s)", w.Code, w.Body.String())
		}
		var env struct {
			ErrCode int            `json:"err_code"`
			Data    OperationsView `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if env.ErrCode != 0 {
			t.Fatalf("err_code = %d, want 0", env.ErrCode)
		}
		return env.Data
	}

	adminView := callAs(&session.Identity{UserID: "u-admin", Username: "admin", IsBuiltinAdmin: true})
	if adminView.UserTotal != 10 || adminView.DatasourceTotal != 4 {
		t.Fatalf("admin should receive global totals, got %+v", adminView)
	}

	memberView := callAs(&session.Identity{UserID: "u-member", Username: "member", Role: "user"})
	if memberView.UserTotal != 0 || memberView.DatasourceTotal != 0 {
		t.Fatalf("regular user should receive zero global user/datasource counts, got %+v", memberView)
	}
}

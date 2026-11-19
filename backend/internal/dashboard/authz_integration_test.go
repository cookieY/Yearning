//go:build integration

package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/cookieY/YearningX/backend/internal/identity/session"
)

// fakeStore maps cookie values to preconfigured identities (test only).
type fakeStore map[string]*session.Session

func (f fakeStore) Get(_ context.Context, id string) (*session.Session, error) {
	s, ok := f[id]
	if !ok {
		return nil, errNoSession{}
	}
	return s, nil
}

var _ session.Store = fakeStore{}

// errNoSession is the missing-session error matching Store semantics.
type errNoSession struct{}

func (errNoSession) Error() string { return "dashboard test: no session" }

// TestDashboardAuthz §18.71/§18.83 authorization matrix: platform operations stats (operations) and
// system health (system-health) are open to all authenticated sessions (anonymous 401, member 200);
// review-quality has been removed per owner 2026-10-23 directive (route does not exist).
func TestDashboardAuthz(t *testing.T) {
	_, s := newTestService(t)
	gin.SetMode(gin.TestMode)
	admin := &session.Identity{UserID: "u-admin", Username: "admin", IsBuiltinAdmin: true}
	member := &session.Identity{UserID: "u-member", Username: "member", IsBuiltinAdmin: false}
	store := fakeStore{
		"sess-admin":  {Identity: admin},
		"sess-member": {Identity: member},
	}

	newEngine := func() *gin.Engine {
		engine := gin.New()
		engine.Use(session.Middleware(store, session.Options{CookieName: "yearning_session"}))
		RegisterRoutes(engine, &Deps{Service: s})
		return engine
	}

	do := func(cookie string, path string) int {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if cookie != "" {
			req.AddCookie(&http.Cookie{Name: "yearning_session", Value: cookie})
		}
		rec := httptest.NewRecorder()
		newEngine().ServeHTTP(rec, req)
		return rec.Code
	}

	// operations: anonymous 401; both member and admin return 200 with err_code 0.
	if code := do("", "/admin/dashboard/operations"); code != http.StatusUnauthorized {
		t.Fatalf("anonymous operations: want 401 got %d", code)
	}
	for _, cookie := range []string{"sess-member", "sess-admin"} {
		code := do(cookie, "/admin/dashboard/operations")
		if code != http.StatusOK {
			t.Fatalf("%s operations: want 200 got %d", cookie, code)
		}
	}
	// member operations response has err_code 0 (real aggregation surface, not short-circuited by guard).
	req := httptest.NewRequest(http.MethodGet, "/admin/dashboard/operations", nil)
	req.AddCookie(&http.Cookie{Name: "yearning_session", Value: "sess-member"})
	rec := httptest.NewRecorder()
	newEngine().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("member operations payload: %d", rec.Code)
	}
	var envelope struct {
		ErrCode int `json:"err_code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil || envelope.ErrCode != 0 {
		t.Fatalf("member operations envelope: %v %+v", err, envelope)
	}
	// §18.83 system-health is open to all authenticated sessions: member and admin return 200, anonymous returns 401.
	for _, cookie := range []string{"sess-member", "sess-admin"} {
		if code := do(cookie, "/admin/dashboard/system-health"); code != http.StatusOK {
			t.Fatalf("%s system-health: want 200 got %d", cookie, code)
		}
	}
	if code := do("", "/admin/dashboard/system-health"); code != http.StatusUnauthorized {
		t.Fatalf("anonymous system-health: want 401 got %d", code)
	}
	// review-quality has been removed: route is not mounted. Authenticated sessions hit 404 directly (no route);
	// anonymous requests are intercepted by session middleware with 401, both proving the read surface no longer exists.
	if code := do("", "/admin/dashboard/review-quality"); code != http.StatusUnauthorized {
		t.Fatalf("anonymous review-quality: want 401 got %d", code)
	}
	for _, cookie := range []string{"sess-member", "sess-admin"} {
		if code := do(cookie, "/admin/dashboard/review-quality"); code != http.StatusNotFound {
			t.Fatalf("%s review-quality: want 404 got %d", cookie, code)
		}
	}
}

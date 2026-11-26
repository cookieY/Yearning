package dashboard

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/cookieY/YearningX/backend/internal/identity/session"
)

func TestDashboardD14FailClosedGuardAndEnvelopeContracts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := &Service{}

	routes := []struct {
		method string
		path   string
		admin  bool
	}{
		{http.MethodGet, "/dashboard/me", false},
		{http.MethodGet, "/admin/dashboard/operations", false},
		{http.MethodGet, "/admin/dashboard/system-health", false},
		{http.MethodGet, "/announcements/current", false},
		{http.MethodGet, "/releases/latest", false},
		{http.MethodGet, "/admin/announcement-revisions", true},
		{http.MethodPost, "/admin/announcement-revisions", true},
		{http.MethodPut, "/admin/announcement-publication", true},
		{http.MethodGet, "/admin/audit-events", true},
	}

	// 1. D14 fail-closed invariant: nil Guard must abort every route with HTTP 500 Problem Details.
	rNil := gin.New()
	RegisterRoutes(rNil, &Deps{Service: svc})
	for _, rt := range routes {
		req := httptest.NewRequest(rt.method, rt.path, bytes.NewBufferString(`{}`))
		w := httptest.NewRecorder()
		rNil.ServeHTTP(w, req)
		if w.Code != http.StatusInternalServerError {
			t.Fatalf("nil Guard %s %s expected 500 fail-closed, got %d body=%s", rt.method, rt.path, w.Code, w.Body.String())
		}
		if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
			t.Fatalf("nil Guard %s %s Content-Type = %q, want application/problem+json", rt.method, rt.path, ct)
		}
		if !strings.Contains(w.Body.String(), `"status":500`) {
			t.Fatalf("nil Guard %s %s expected RFC 9457 body, got %s", rt.method, rt.path, w.Body.String())
		}
	}

	// 2. Unauthenticated requests with explicit pass-through guard must return RFC 9457 401/403 Problem Details.
	rNoSess := gin.New()
	RegisterRoutes(rNoSess, &Deps{
		Service: svc,
		Guard:   func(_ string) gin.HandlerFunc { return func(c *gin.Context) { c.Next() } },
	})
	for _, rt := range routes {
		req := httptest.NewRequest(rt.method, rt.path, bytes.NewBufferString(`{}`))
		w := httptest.NewRecorder()
		rNoSess.ServeHTTP(w, req)
		wantStatus := http.StatusUnauthorized
		if rt.admin {
			wantStatus = http.StatusForbidden
		}
		if w.Code != wantStatus {
			t.Fatalf("unauth %s %s expected %d, got %d", rt.method, rt.path, wantStatus, w.Code)
		}
		if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
			t.Fatalf("unauth %s %s Content-Type = %q, want application/problem+json", rt.method, rt.path, ct)
		}
		if len(w.Body.Bytes()) == 0 {
			t.Fatalf("unauth %s %s returned empty body instead of Problem Details", rt.method, rt.path)
		}
	}

	// 3. Admin session: invalid JSON on createAnnouncementRevision & invalid If-Match on publishAnnouncement emit full 1001 envelopes.
	mem := session.NewMemoryStore()
	mem.Put(&session.Session{
		ID: "s-admin",
		Identity: &session.Identity{
			UserID:         "0198da00-0000-7000-8000-000000000001",
			Username:       "admin",
			Role:           "admin",
			IsBuiltinAdmin: true,
		},
	})
	rAdmin := gin.New()
	rAdmin.Use(func(c *gin.Context) { c.Set("request_id", "req-dash-1"); c.Next() })
	rAdmin.Use(session.Middleware(mem, session.Options{CookieName: "sid", AnonymousAllowed: true}))
	RegisterRoutes(rAdmin, &Deps{
		Service: svc,
		Guard:   func(_ string) gin.HandlerFunc { return func(c *gin.Context) { c.Next() } },
	})

	reqBadJSON := httptest.NewRequest(http.MethodPost, "/admin/announcement-revisions", bytes.NewBufferString(`{bad`))
	reqBadJSON.Header.Set("Content-Type", "application/json")
	reqBadJSON.AddCookie(&http.Cookie{Name: "sid", Value: "s-admin"})
	wBad := httptest.NewRecorder()
	rAdmin.ServeHTTP(wBad, reqBadJSON)
	var env map[string]any
	if err := json.Unmarshal(wBad.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode bad json response: %v", err)
	}
	if wBad.Code != http.StatusOK || env["err_code"] != float64(1001) || env["request_id"] != "req-dash-1" || env["retryable"] != false {
		t.Fatalf("unexpected createAnnouncementRevision 1001 envelope: status=%d env=%+v", wBad.Code, env)
	}

	for _, hdr := range []string{"", "1", `"0"`, `"abc"`} {
		reqPub := httptest.NewRequest(http.MethodPut, "/admin/announcement-publication",
			bytes.NewBufferString(`{"announcement_revision_id":"0198da00-0000-7000-8000-000000000099"}`))
		reqPub.Header.Set("Content-Type", "application/json")
		if hdr != "" {
			reqPub.Header.Set("If-Match", hdr)
		}
		reqPub.AddCookie(&http.Cookie{Name: "sid", Value: "s-admin"})
		wPub := httptest.NewRecorder()
		rAdmin.ServeHTTP(wPub, reqPub)
		var pubEnv map[string]any
		if err := json.Unmarshal(wPub.Body.Bytes(), &pubEnv); err != nil {
			t.Fatalf("decode publish If-Match=%q: %v", hdr, err)
		}
		if wPub.Code != http.StatusOK || pubEnv["err_code"] != float64(1001) || pubEnv["request_id"] != "req-dash-1" || pubEnv["retryable"] != false {
			t.Fatalf("publish If-Match=%q unexpected envelope: status=%d env=%+v", hdr, wPub.Code, pubEnv)
		}
	}
}

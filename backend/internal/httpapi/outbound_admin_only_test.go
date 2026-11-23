// outbound_admin_only_test.go locks the intranet-first trust assumption: every endpoint that creates,
// mutates, deletes, or probes outbound connection targets (datasources, AI providers, identity providers,
// notification channels, and system settings) denies non-admin authenticated users with HTTP 403 before
// reaching any handler or dialing any target.
package httpapi

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/cookieY/YearningX/backend/internal/identity/session"
	"github.com/cookieY/YearningX/backend/internal/notification"
)

func TestOutboundConfigurationAndProbeEndpointsAdminOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)

	mem := session.NewMemoryStore()
	mem.Put(&session.Session{
		ID: "non-admin-token",
		Identity: &session.Identity{
			UserID:         "0198da00-0000-7000-8000-000000000002",
			Username:       "regular-developer",
			IsBuiltinAdmin: false,
		},
		ExpiresAt: time.Now().Add(time.Hour),
	})

	sessions := newGuardTestSessionStore(t)
	logger := zap.NewNop()
	router := NewServeRouter(ServeDeps{
		StrictWiring: true,
		Logger:       logger,
		Readiness:    NewReadiness(logger),
		SessionStore: mem,
		SessionOption: session.Options{
			CookieName:       sessionCookieName,
			AnonymousAllowed: true,
		},
		B3:  &B3Deps{Sessions: sessions},
		B6:  &B6Deps{Sessions: sessions},
		B7:  &B7Deps{},
		B13: &B13Deps{},
		Notification: &notification.Deps{
			Guard: func(op string) gin.HandlerFunc { return RequireOperation(op, sessions) },
		},
	})

	endpoints := []struct {
		method string
		path   string
		body   string
	}{
		// Datasources
		{http.MethodPost, "/api/v4/admin/datasources", `{}`},
		{http.MethodPut, "/api/v4/admin/datasources/ds-1", `{}`},
		{http.MethodDelete, "/api/v4/admin/datasources/ds-1", ""},
		{http.MethodPost, "/api/v4/admin/datasources/ds-1/connection-tests", `{}`},
		// AI providers
		{http.MethodPost, "/api/v4/admin/ai-providers", `{}`},
		{http.MethodPut, "/api/v4/admin/ai-providers/ai-1", `{}`},
		{http.MethodDelete, "/api/v4/admin/ai-providers/ai-1", ""},
		{http.MethodPost, "/api/v4/admin/ai-providers/models", `{}`},
		{http.MethodPost, "/api/v4/admin/ai-providers/ai-1/rate-versions", `{}`},
		{http.MethodPost, "/api/v4/admin/ai-providers/ai-1/connection-tests", `{}`},
		// Identity providers (LDAP / OIDC)
		{http.MethodPost, "/api/v4/admin/identity-providers", `{}`},
		{http.MethodPut, "/api/v4/admin/identity-providers/idp-1", `{}`},
		{http.MethodDelete, "/api/v4/admin/identity-providers/idp-1", ""},
		{http.MethodPost, "/api/v4/admin/identity-providers/idp-1/connection-tests", `{}`},
		// Notification channels (Webhook / SMTP)
		{http.MethodPost, "/api/v4/admin/notification-channels", `{}`},
		{http.MethodPut, "/api/v4/admin/notification-channels/ch-1", `{}`},
		{http.MethodDelete, "/api/v4/admin/notification-channels/ch-1", ""},
		{http.MethodPost, "/api/v4/admin/notification-channels/ch-1/test-deliveries", `{}`},
		// System settings (including execution.osc_tool, release feed / general.public_base_url)
		{http.MethodPut, "/api/v4/admin/settings/execution", `{}`},
		{http.MethodPut, "/api/v4/admin/settings/general", `{}`},
	}

	for _, ep := range endpoints {
		req := httptest.NewRequest(ep.method, ep.path, bytes.NewBufferString(ep.body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(csrfHeader, "valid-csrf")
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "non-admin-token"})

		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Errorf("%s %s status = %d; want 403 Forbidden for non-admin user", ep.method, ep.path, w.Code)
		}
	}
}

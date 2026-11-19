//go:build integration

// security_integration_test.go real-router regression tests for HTTP boundary security fixes
// (SEC-0004/0005/0006/0058/NEW-AUTH-01):
//   - Cross-site POST login family returns 403 (blocking login CSRF); OIDC callback is a cross-site
//     top-level navigation endpoint without same-origin middleware, defended by state cookie check (Review R2);
//   - OIDC callback with missing/mismatched state cookie returns 403;
//   - 11 failed LDAP logins trigger 429, with complete success/failure audit trail;
//   - 11 OIDC start calls trigger 429 + denied audit, and callback missing-provider / client-construction-failure
//     branches record failed audits (SEC-0006);
//   - XFF does not alter audit source_ip without trusted proxies, and resolves client IP from forwarding
//     chain when trusted proxies are configured (NEW-AUTH-01);
//   - Non-existent accounts are rate-limited identically (eliminating 429 account existence enumeration).
package httpapi

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/cookieY/YearningX/backend/internal/identity/session"
)

func securityRequest(engine *gin.Engine, method, path, body, origin, cookie string) *httptest.ResponseRecorder {
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)
	return w
}

// TestCrossOriginAuthEntriesRejected SEC-0004 regression: cross-site Origin on
// /auth/login, /auth/register, and /auth/ldap/login must return 403. OIDC callback is
// exempted (Review R2 [High] fix): it is a cross-site top-level navigation endpoint from IdP 302 redirect
// where real browsers send Sec-Fetch-Site: cross-site and IdP Referer; mounting RequireSameOrigin
// would break valid logins -- defended instead by the HttpOnly+SameSite=Lax state cookie set during
// start (403 state mismatch in handler) and PKCE.
func TestCrossOriginAuthEntriesRejected(t *testing.T) {
	fx := newB3Fixture(t)
	fx.seedAdmin(t)
	crossOrigin := "https://evil.example"
	posts := map[string]string{
		"/api/v4/auth/login":      `{"username":"admin","password":"x"}`,
		"/api/v4/auth/register":   `{"username":"bob","display_name":"B","password":"longpassword1"}`,
		"/api/v4/auth/ldap/login": `{"username":"bob","password":"x"}`,
	}
	for path, body := range posts {
		w := securityRequest(fx.engine, "POST", path, body, crossOrigin, "")
		if w.Code != http403 {
			t.Fatalf("cross-site POST %s = %d, want 403 (login CSRF)", path, w.Code)
		}
	}
	// OIDC callback with cross-site Origin must not be intercepted early by RequireSameOrigin:
	// it must pass through to the handler where state cookie validation decides -- returning 403
	// "oidc state mismatch" when cookie is absent rather than "cross-site request rejected".
	w := securityRequest(fx.engine, "GET",
		"/api/v4/auth/oidc/sso/callback?code=x&state=y", "", crossOrigin, "")
	if w.Code != http403 || !strings.Contains(w.Body.String(), "state mismatch") {
		t.Fatalf("cross-site OIDC callback = %d %s, want handler-level state mismatch (not same-origin rejection)",
			w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "cross-site") {
		t.Fatal("OIDC callback must not be rejected by RequireSameOrigin (cross-site top-level navigation)")
	}
	// Navigation form (no Origin/Referer, browser top-level GET 302 redirect): likewise passes
	// through same-origin middleware to handler state check (no cookie -> 403 state mismatch;
	// valid state behavior covered in TestOidcCallbackStateCookieBinding).
	w = securityRequest(fx.engine, "GET",
		"/api/v4/auth/oidc/sso/callback?code=x&state=y", "", "", "")
	if w.Code != http403 || !strings.Contains(w.Body.String(), "state mismatch") {
		t.Fatalf("navigation-shape OIDC callback = %d %s, want handler-level state mismatch",
			w.Code, w.Body.String())
	}
	// Sec-Fetch-Site: cross-site is also rejected (browser navigation without Origin).
	req := httptest.NewRequest("POST", "/api/v4/auth/login", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	rec := httptest.NewRecorder()
	fx.engine.ServeHTTP(rec, req)
	if rec.Code != http403 {
		t.Fatalf("Sec-Fetch-Site cross-site login = %d, want 403", rec.Code)
	}
	// Same-origin Origin is not blocked (normal browser path proceeds to downstream guards rather than 403 same-origin rejection).
	w = securityRequest(fx.engine, "POST", "/api/v4/auth/login",
		`{"username":"admin","password":"x"}`, "http://example.com", "")
	if w.Code == http403 && strings.Contains(w.Body.String(), "cross-site") {
		t.Fatal("same-origin request must not be rejected by origin check")
	}
}

// TestOidcCallbackStateCookieBinding SEC-0004 regression: callback state must match
// the HttpOnly cookie set during start; missing or mismatched cookie returns 403.
func TestOidcCallbackStateCookieBinding(t *testing.T) {
	fx := newB3Fixture(t)
	fx.seedAdmin(t)
	path := "/api/v4/auth/oidc/sso/callback?code=x&state=abc"
	// Missing cookie: 403.
	w := securityRequest(fx.engine, "GET", path, "", "", "")
	if w.Code != http403 {
		t.Fatalf("callback without state cookie = %d, want 403", w.Code)
	}
	// Cookie mismatches query state: 403.
	w = securityRequest(fx.engine, "GET", path, "", "", oidcStateCookieName+"=different")
	if w.Code != http403 {
		t.Fatalf("callback with mismatched state cookie = %d, want 403", w.Code)
	}
	if !strings.Contains(w.Body.String(), "state mismatch") {
		t.Fatalf("mismatch must be reported as state mismatch, got %s", w.Body.String())
	}
}

// TestLdapLoginRateLimitAndAudit SEC-0006 regression: 11 failed LDAP logins trigger
// 429; each failure writes an audit record (failure details omitted from external response).
func TestLdapLoginRateLimitAndAudit(t *testing.T) {
	fx := newB3Fixture(t)
	fx.seedAdmin(t)
	if _, body, _ := fx.admin.login("admin", adminPassword); errCode(body) != 0 {
		t.Fatalf("admin login failed: %v", body)
	}
	// LDAP Provider points to a local port that refuses connections: authentication fails fast (connection refused),
	// exercising the real rate-limiting and auditing paths.
	code, body, raw := fx.admin.do("POST", "/api/v4/admin/identity-providers", map[string]any{
		"provider_key": "ldap-main", "provider_kind": "ldap", "display_name": "LDAP", "enabled": true,
		"configuration": map[string]any{
			"host": "127.0.0.1", "port": 1, "transport": "ldaps", "server_name": "ldap.example.com",
			"base_dn":     "dc=example,dc=com",
			"user_filter": "(uid={username})", "username_attribute": "uid",
			"display_name_attribute": "cn",
			"connect_timeout_ms":     200, "bind_timeout_ms": 200, "search_timeout_ms": 200,
		},
		"bind_password": map[string]any{"value": "ldap-bind-secret"},
	}, true)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("create ldap provider: %d %s", code, raw)
	}

	client := testClient{t: t, engine: fx.engine, cookies: map[string]string{}}
	for i := 1; i <= 10; i++ {
		c, b, _ := client.do("POST", "/api/v4/auth/ldap/login",
			map[string]any{"username": "corpuser", "password": fmt.Sprintf("wrong-%d", i)}, false)
		if c != 200 || errCode(b) != 1101 {
			t.Fatalf("ldap attempt %d = %d %v, want 1101 (uniform sanitized message)", i, c, b)
		}
	}
	c, _, _ := client.do("POST", "/api/v4/auth/ldap/login",
		map[string]any{"username": "corpuser", "password": "wrong-11"}, false)
	if c != 429 {
		t.Fatalf("11th failed ldap attempt = %d, want 429", c)
	}
	var failed int64
	_ = fx.db.Raw(`select count(*) from audit_events where event_type = 'login_ldap' and outcome = 'failed'`).Scan(&failed).Error
	if failed != 10 {
		t.Fatalf("login_ldap failed audit rows = %d, want 10", failed)
	}
	var denied int64
	_ = fx.db.Raw(`select count(*) from audit_events where event_type = 'login_ldap' and outcome = 'denied'`).Scan(&denied).Error
	if denied != 1 {
		t.Fatalf("login_ldap denied (429) audit rows = %d, want 1", denied)
	}
}

// TestAuditSourceIPUsesRemoteAddr SEC-0005 regression: forged XFF does not affect audit
// source_ip (ClientIP=RemoteAddr after SetTrustedProxies(nil)).
func TestAuditSourceIPUsesRemoteAddr(t *testing.T) {
	fx := newB3Fixture(t)
	fx.seedAdmin(t)
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest("POST", "/api/v4/auth/login",
			strings.NewReader(`{"username":"admin","password":"wrong"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Forwarded-For", "198.51.100."+fmt.Sprint(i))
		w := httptest.NewRecorder()
		fx.engine.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("login attempt %d = %d", i, w.Code)
		}
	}
	var spoofed int64
	_ = fx.db.Raw(`select count(*) from audit_events where source_ip like '198.51.100.%'`).Scan(&spoofed).Error
	if spoofed != 0 {
		t.Fatal("audit source_ip must never take XFF values (trusted proxies disabled)")
	}
	var remote int64
	_ = fx.db.Raw(`select count(*) from audit_events where source_ip = '192.0.2.1'`).Scan(&remote).Error
	if remote == 0 {
		t.Fatal("audit source_ip must equal httptest RemoteAddr 192.0.2.1")
	}
}

// TestLoginRateLimitAppliesToUnknownUsers SEC-0058 regression: non-existent accounts also
// enter rate limiting (429 on 11th attempt), so 429 is no longer an account existence oracle.
func TestLoginRateLimitAppliesToUnknownUsers(t *testing.T) {
	fx := newB3Fixture(t)
	fx.seedAdmin(t)
	client := testClient{t: t, engine: fx.engine, cookies: map[string]string{}}
	for i := 1; i <= 10; i++ {
		c, b, _ := client.do("POST", "/api/v4/auth/login",
			map[string]any{"username": "ghost-user", "password": "whatever-long"}, false)
		if c != 200 || errCode(b) != 1101 {
			t.Fatalf("unknown-user attempt %d = %d %v, want uniform 1101", i, c, b)
		}
	}
	c, _, _ := client.do("POST", "/api/v4/auth/login",
		map[string]any{"username": "ghost-user", "password": "whatever-long"}, false)
	if c != 429 {
		t.Fatalf("11th unknown-user attempt = %d, want 429 (no enumeration oracle)", c)
	}
}

// TestOidcStartRateLimitAndAudit SEC-0006 regression: /auth/oidc/:key/start
// rate limiting precedes provider lookup and IdP discovery -- unauthenticated callers hitting start
// at high frequency cannot drive arbitrary outbound IdP requests; 11th attempt returns 429 and records
// a denied audit event. Issuer points to a local refusing port so discovery fails fast without caching.
func TestOidcStartRateLimitAndAudit(t *testing.T) {
	fx := newB3Fixture(t)
	fx.seedAdmin(t)
	if _, body, _ := fx.admin.login("admin", adminPassword); errCode(body) != 0 {
		t.Fatalf("admin login failed: %v", body)
	}
	code, body, raw := fx.admin.do("POST", "/api/v4/admin/identity-providers", map[string]any{
		"provider_key": "sso", "provider_kind": "oidc", "display_name": "SSO", "enabled": true,
		"configuration": map[string]any{
			"issuer_url": "https://127.0.0.1:1", "client_id": "client-a", "scopes": []string{"openid"},
			"username_claim":     "preferred_username",
			"connect_timeout_ms": 200, "request_timeout_ms": 200,
		},
		"client_secret": map[string]any{"value": "oidc-client-secret"},
	}, true)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("create oidc provider: %d %s", code, raw)
	}
	for i := 1; i <= 10; i++ {
		w := securityRequest(fx.engine, "GET", "/api/v4/auth/oidc/sso/start", "", "", "")
		if w.Code != 500 {
			t.Fatalf("start attempt %d = %d, want 500 (discovery fails fast, quota consumed)", i, w.Code)
		}
	}
	w := securityRequest(fx.engine, "GET", "/api/v4/auth/oidc/sso/start", "", "", "")
	if w.Code != 429 {
		t.Fatalf("11th start attempt = %d, want 429", w.Code)
	}
	var denied int64
	_ = fx.db.Raw(`select count(*) from audit_events where event_type = 'login_oidc' and outcome = 'denied'`).Scan(&denied).Error
	if denied != 1 {
		t.Fatalf("login_oidc denied (start 429) audit rows = %d, want 1", denied)
	}
}

// TestOidcCallbackFailureBranchAudits SEC-0006 regression: fills in audit logging for the two
// previously unaudited callback failure branches -- missing provider (401) and client construction
// failure (500) both record login_oidc/failed audits while keeping external messages sanitized.
func TestOidcCallbackFailureBranchAudits(t *testing.T) {
	fx := newB3Fixture(t)
	fx.seedAdmin(t)
	if _, body, _ := fx.admin.login("admin", adminPassword); errCode(body) != 0 {
		t.Fatalf("admin login failed: %v", body)
	}
	code, body, raw := fx.admin.do("POST", "/api/v4/admin/identity-providers", map[string]any{
		"provider_key": "sso", "provider_kind": "oidc", "display_name": "SSO", "enabled": true,
		"configuration": map[string]any{
			"issuer_url": "https://127.0.0.1:1", "client_id": "client-a", "scopes": []string{"openid"},
			"username_claim":     "preferred_username",
			"connect_timeout_ms": 200, "request_timeout_ms": 200,
		},
		"client_secret": map[string]any{"value": "oidc-client-secret"},
	}, true)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("create oidc provider: %d %s", code, raw)
	}
	// Missing provider: returns 401 + failed audit after passing state cookie check.
	w := securityRequest(fx.engine, "GET",
		"/api/v4/auth/oidc/nope/callback?code=x&state=s1", "", "", oidcStateCookieName+"=s1")
	if w.Code != 401 || !strings.Contains(w.Body.String(), "provider unavailable") {
		t.Fatalf("callback missing provider = %d %s, want 401 provider unavailable", w.Code, w.Body.String())
	}
	// Provider exists but discovery fails: returns 500 internal error + failed audit.
	w = securityRequest(fx.engine, "GET",
		"/api/v4/auth/oidc/sso/callback?code=x&state=s2", "", "", oidcStateCookieName+"=s2")
	if w.Code != 500 || !strings.Contains(w.Body.String(), "internal error") {
		t.Fatalf("callback discovery failure = %d %s, want 500 internal error", w.Code, w.Body.String())
	}
	var failed int64
	_ = fx.db.Raw(`select count(*) from audit_events where event_type = 'login_oidc' and outcome = 'failed'`).Scan(&failed).Error
	if failed != 2 {
		t.Fatalf("login_oidc failed audit rows = %d, want 2 (missing-provider + discovery-failure)", failed)
	}
}

// TestAuditSourceIPBehindTrustedProxy NEW-AUTH-01 regression: requests from within trusted
// proxy CIDRs resolve audit source_ip from XFF (= forwarded client IP), while XFF from outside
// trusted CIDRs is still ignored (source_ip=RemoteAddr) -- verifying ServeDeps.TrustedProxies wiring
// into gin SetTrustedProxies and B3Deps.
func TestAuditSourceIPBehindTrustedProxy(t *testing.T) {
	fx := newB3Fixture(t)
	fx.seedAdmin(t)
	logger, _ := observedLogger()
	proxyEngine := NewServeRouter(ServeDeps{
		Logger: logger,
		Readiness: NewReadiness(logger, ReadinessCheck{Name: "db", Check: func(ctx context.Context) error {
			return fx.db.WithContext(ctx).Raw("select 1").Scan(&[]int{}).Error
		}}),
		SessionStore:  fx.sessions,
		SessionOption: session.Options{CookieName: "yearning_session", Secure: false, AnonymousAllowed: true},
		B3: &B3Deps{
			Identity: fx.svc, Sessions: fx.sessions, Workflow: fx.ws,
			Datasource: fx.dss, DB: fx.db, Crypter: fx.crypter, SecureCookie: false,
		},
		TrustedProxies: []string{"10.0.0.0/8"},
	})

	// Forwarded by trusted proxy (RemoteAddr 10.9.9.9): audit source_ip = XFF client IP.
	req := httptest.NewRequest("POST", "/api/v4/auth/login",
		strings.NewReader(`{"username":"admin","password":"wrong-password"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-For", "198.51.100.7")
	req.RemoteAddr = "10.9.9.9:5555"
	w := httptest.NewRecorder()
	proxyEngine.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("trusted-proxy login = %d %s", w.Code, w.Body.String())
	}

	// Same XFF forged by untrusted source: audit source_ip = RemoteAddr.
	req = httptest.NewRequest("POST", "/api/v4/auth/login",
		strings.NewReader(`{"username":"admin","password":"wrong-password-2"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-For", "198.51.100.7")
	req.RemoteAddr = "203.0.113.5:7777"
	w = httptest.NewRecorder()
	proxyEngine.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("untrusted login = %d %s", w.Code, w.Body.String())
	}

	var fromProxy int64
	_ = fx.db.Raw(`select count(*) from audit_events where source_ip = '198.51.100.7'`).Scan(&fromProxy).Error
	if fromProxy != 1 {
		t.Fatalf("audit rows with forwarded client IP = %d, want 1", fromProxy)
	}
	var direct int64
	_ = fx.db.Raw(`select count(*) from audit_events where source_ip = '203.0.113.5'`).Scan(&direct).Error
	if direct != 1 {
		t.Fatalf("audit rows with untrusted RemoteAddr = %d, want 1 (XFF must be ignored)", direct)
	}
	var spoofed int64
	_ = fx.db.Raw(`select count(*) from audit_events where source_ip = '10.9.9.9'`).Scan(&spoofed).Error
	if spoofed != 0 {
		t.Fatal("trusted proxy address must never appear as source_ip when XFF parseable")
	}
}

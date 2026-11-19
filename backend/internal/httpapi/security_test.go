// security_test.go unit tests for HTTP boundary security middleware:
// same-origin validation (SEC-0004), trusted proxies disabled (SEC-0005), security response headers
// (SEC-0012/0033), no-store (SEC-0034), and request body size limit (SEC-0023).
package httpapi

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

func newSecurityTestEngine(t *testing.T, mw ...gin.HandlerFunc) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RequestIDMiddleware())
	r.Use(mw...)
	// Isomorphic to the real login handler: binds JSON and returns business error 1001 on failure,
	// making BodyLimit truncation behavior observable.
	r.POST("/api/v4/auth/login", func(c *gin.Context) {
		var req map[string]any
		if err := c.ShouldBindJSON(&req); err != nil {
			WriteBusinessError(c, &BusinessError{
				Code: ErrCodeValidationFailed, Name: "VALIDATION_FAILED", Message: "invalid request parameters",
			})
			return
		}
		WriteSuccess(c, nil)
	})
	r.GET("/api/v4/health/live", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "live"}) })
	r.GET("/", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"spa": true}) })
	return r
}

func doRequest(r http.Handler, method, path string, body io.Reader, header map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, body)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// TestRequireSameOriginPolicy SEC-0004: cross-site Origin/Referer and
// Sec-Fetch-Site: cross-site are rejected with 403; same-origin and headerless requests are allowed.
func TestRequireSameOriginPolicy(t *testing.T) {
	r := newSecurityTestEngine(t, RequireSameOrigin())
	payload := strings.NewReader(`{"username":"a","password":"b"}`)
	cases := []struct {
		name   string
		header map[string]string
		want   int
	}{
		{"no origin headers (non-browser client)", nil, http.StatusOK},
		{"same-origin Origin", map[string]string{"Origin": "http://example.com"}, http.StatusOK},
		{"cross-site Origin", map[string]string{"Origin": "https://evil.example"}, http.StatusForbidden},
		{"Origin null (sandboxed)", map[string]string{"Origin": "null"}, http.StatusForbidden},
		{"Origin host mismatch with port", map[string]string{"Origin": "http://example.com:8443"}, http.StatusForbidden},
		{"cross-site Referer", map[string]string{"Referer": "https://evil.example/attack"}, http.StatusForbidden},
		{"same-origin Referer", map[string]string{"Referer": "http://example.com/login"}, http.StatusOK},
		{"unparseable Referer", map[string]string{"Referer": "://bad url"}, http.StatusForbidden},
		{"Sec-Fetch-Site cross-site", map[string]string{"Sec-Fetch-Site": "cross-site"}, http.StatusForbidden},
		{"Sec-Fetch-Site same-origin", map[string]string{"Sec-Fetch-Site": "same-origin"}, http.StatusOK},
		{"Sec-Fetch-Site none (direct nav)", map[string]string{"Sec-Fetch-Site": "none"}, http.StatusOK},
		{"Sec-Fetch-Site cross-site beats same-origin Origin",
			map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "http://example.com"}, http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := doRequest(r, http.MethodPost, "/api/v4/auth/login", payload, tc.header)
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d (headers %v)", w.Code, tc.want, tc.header)
			}
			if tc.want == http.StatusForbidden {
				if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
					t.Fatalf("rejection must be problem+json, got %s", ct)
				}
			}
		})
	}
}

// TestTrustedProxiesDisabled SEC-0005/NEW-AUTH-01: with empty/nil trusted proxy list,
// ClientIP uses only RemoteAddr so XFF never affects rate-limit keys or audit source_ip.
func TestTrustedProxiesDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	if err := r.SetTrustedProxies(nil); err != nil {
		t.Fatal(err)
	}
	var observed string
	r.GET("/ip", func(c *gin.Context) {
		observed = c.ClientIP()
		c.Status(http.StatusOK)
	})
	for i := 0; i < 3; i++ {
		w := doRequest(r, http.MethodGet, "/ip", nil,
			map[string]string{"X-Forwarded-For": "198.51.100." + string(rune('0'+i)) + ", 203.0.113.9"})
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d", w.Code)
		}
	}
	// httptest RemoteAddr is fixed to 192.0.2.1:1234; after rotating XFF three times, ClientIP
	// must still be RemoteAddr rather than the leftmost XFF value -- otherwise rate-limit keys could be bypassed per request.
	if observed != "192.0.2.1" {
		t.Fatalf("ClientIP = %q, want RemoteAddr 192.0.2.1 (XFF must be ignored)", observed)
	}
	// Empty slice has the same semantics as nil (router passes deps.TrustedProxies directly; empty config
	// must preserve direct-connection semantics).
	r2 := gin.New()
	if err := r2.SetTrustedProxies([]string{}); err != nil {
		t.Fatal(err)
	}
	var observed2 string
	r2.GET("/ip", func(c *gin.Context) {
		observed2 = c.ClientIP()
		c.Status(http.StatusOK)
	})
	w := doRequest(r2, http.MethodGet, "/ip", nil,
		map[string]string{"X-Forwarded-For": "198.51.100.7"})
	if w.Code != http.StatusOK || observed2 != "192.0.2.1" {
		t.Fatalf("empty-list ClientIP = %q, want RemoteAddr (empty list == nil)", observed2)
	}
}

// TestTrustedProxyClientIPMatrix NEW-AUTH-01: under a trusted proxy allowlist, XFF is
// trusted only from trusted CIDRs -- direct connection (XFF ignored), trusted proxy (ClientIP = forwarded IP),
// untrusted proxy (XFF ignored).
func TestTrustedProxyClientIPMatrix(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	if err := r.SetTrustedProxies([]string{"10.0.0.0/8", "192.168.1.7"}); err != nil {
		t.Fatal(err)
	}
	var observed string
	r.GET("/ip", func(c *gin.Context) {
		observed = c.ClientIP()
		c.Status(http.StatusOK)
	})
	cases := []struct {
		name       string
		remoteAddr string
		xff        string
		want       string
	}{
		{"direct client, XFF ignored", "203.0.113.5:44321", "198.51.100.7", "203.0.113.5"},
		{"trusted proxy, XFF honored", "10.1.2.3:5555", "198.51.100.7", "198.51.100.7"},
		{"trusted proxy single IP honored", "192.168.1.7:9999", "198.51.100.7", "198.51.100.7"},
		{"untrusted proxy, XFF ignored", "203.0.113.9:6666", "198.51.100.7", "203.0.113.9"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/ip", nil)
			req.RemoteAddr = tc.remoteAddr
			req.Header.Set("X-Forwarded-For", tc.xff)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d", w.Code)
			}
			if observed != tc.want {
				t.Fatalf("ClientIP = %q, want %q", observed, tc.want)
			}
		})
	}
}

// TestServeRouterRejectsInvalidTrustedProxies NEW-AUTH-01 wiring check:
// invalid trusted proxy CIDR fails hard at router assembly time (consistent with existing panic semantics),
// proving ServeDeps.TrustedProxies is wired into SetTrustedProxies.
func TestServeRouterRejectsInvalidTrustedProxies(t *testing.T) {
	defer func() {
		if rec := recover(); rec == nil {
			t.Fatal("invalid trusted proxies must panic at assembly")
		}
	}()
	NewServeRouter(ServeDeps{
		Logger:         zap.NewNop(),
		Readiness:      NewReadiness(zap.NewNop(), ReadinessCheck{Name: "db", Check: func(context.Context) error { return nil }}),
		TrustedProxies: []string{"not-a-cidr-or-ip"},
	})
}

// TestSecurityHeadersOnAPIAndSPA SEC-0012/SEC-0033: both API and SPA responses
// carry CSP/nosniff/XFO/Referrer-Policy; HSTS appears only over TLS.
func TestSecurityHeadersOnAPIAndSPA(t *testing.T) {
	r := newSecurityTestEngine(t, SecurityHeadersMiddleware())
	for _, path := range []string{"/api/v4/health/live", "/"} {
		w := doRequest(r, http.MethodGet, path, nil, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d", path, w.Code)
		}
		if got := w.Header().Get("Content-Security-Policy"); got != contentSecurityPolicy {
			t.Fatalf("GET %s CSP = %q, want %q", path, got, contentSecurityPolicy)
		}
		for header, want := range map[string]string{
			"X-Content-Type-Options": "nosniff",
			"X-Frame-Options":        "DENY",
			"Referrer-Policy":        "no-referrer",
		} {
			if got := w.Header().Get(header); got != want {
				t.Fatalf("GET %s %s = %q, want %q", path, header, got, want)
			}
		}
		if got := w.Header().Get("Strict-Transport-Security"); got != "" {
			t.Fatalf("GET %s HSTS must be absent on plain HTTP, got %q", path, got)
		}
	}
	// Key CSP semantic assertions: no unsafe-inline script, no external sources,
	// frame-ancestors/object-src deny embedding, worker restricted to same origin.
	for _, banned := range []string{"script-src 'unsafe-inline'", "script-src 'unsafe-eval'", "http:", "https:"} {
		if strings.Contains(contentSecurityPolicy, banned) {
			t.Fatalf("CSP must not contain %q: %s", banned, contentSecurityPolicy)
		}
	}
	for _, required := range []string{
		"default-src 'self'", "script-src 'self'", "img-src 'self' data:",
		"worker-src 'self'", "frame-ancestors 'none'", "object-src 'none'",
		"base-uri 'self'", "form-action 'self'",
	} {
		if !strings.Contains(contentSecurityPolicy, required) {
			t.Fatalf("CSP must contain %q: %s", required, contentSecurityPolicy)
		}
	}
	// TLS requests must carry HSTS.
	req := httptest.NewRequest(http.MethodGet, "/api/v4/health/live", nil)
	req.TLS = &tls.ConnectionState{}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if got := w.Header().Get("Strict-Transport-Security"); got != hstsHeaderValue {
		t.Fatalf("HSTS over TLS = %q, want %q", got, hstsHeaderValue)
	}
}

// TestAPINoStoreMiddleware SEC-0034: /api and /api/* responses uniformly carry no-store,
// while SPA paths are unaffected.
func TestAPINoStoreMiddleware(t *testing.T) {
	r := newSecurityTestEngine(t, APINoStoreMiddleware())
	for _, path := range []string{"/api/v4/query-executions/x/pages", "/api/v4/change-orders/x", "/api/v4/review-findings/x/evidence", "/api"} {
		w := doRequest(r, http.MethodGet, path, nil, nil)
		// NoRoute 404 under API prefix also carries no-store: middleware sets the header before routing.
		if got := w.Header().Get("Cache-Control"); got != "no-store" {
			t.Fatalf("GET %s Cache-Control = %q, want no-store", path, got)
		}
	}
	w := doRequest(r, http.MethodGet, "/", nil, nil)
	if got := w.Header().Get("Cache-Control"); got == "no-store" {
		t.Fatal("SPA path must not be forced to no-store")
	}
}

// TestBodyLimitMiddleware SEC-0023: declared length exceeding limit immediately returns 413 Problem Details;
// normal requests pass through; chunked streams of unknown length are truncated by MaxBytesReader
// (business handler receives decode failure rather than buffering the entire payload).
func TestBodyLimitMiddleware(t *testing.T) {
	r := newSecurityTestEngine(t, BodyLimitMiddleware(64))
	valid := strings.NewReader(`{"username":"a","password":"b"}`)
	w := doRequest(r, http.MethodPost, "/api/v4/auth/login", valid, map[string]string{"Content-Type": "application/json"})
	if w.Code != http.StatusOK {
		t.Fatalf("normal body must pass, got %d", w.Code)
	}

	oversized := strings.NewReader(`{"username":"a","password":"b"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v4/auth/login", oversized)
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = 1024 // Declared length exceeds 64-byte limit
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("declared oversized body = %d, want 413", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
		t.Fatalf("413 must be problem+json, got %s", ct)
	}
	var problem struct {
		Status int `json:"status"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &problem); err != nil || problem.Status != http.StatusRequestEntityTooLarge {
		t.Fatalf("413 problem body = %s", w.Body.String())
	}

	// Stream of unknown length (ContentLength=0, chunked transfer encoding): truncated upon exceeding limit,
	// handler receives decode error -> business envelope 1001 rather than consuming the oversized body.
	chunked := struct{ io.Reader }{strings.NewReader(strings.Repeat("x", 256))}
	w = doRequest(r, http.MethodPost, "/api/v4/auth/login", chunked,
		map[string]string{"Content-Type": "application/json"})
	var body struct {
		ErrCode int `json:"err_code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("chunked oversized response = %s", w.Body.String())
	}
	if body.ErrCode != ErrCodeValidationFailed {
		t.Fatalf("chunked oversized must fail validation (bounded read), err_code = %d", body.ErrCode)
	}
}

// TestBodyLimitDefaultApplied SEC-0023: limit <= 0 falls back to
// DefaultMaxRequestBodyBytes (>32MiB business boundary).
func TestBodyLimitDefaultApplied(t *testing.T) {
	if DefaultMaxRequestBodyBytes <= 32<<20 {
		t.Fatalf("default body limit %d must exceed the 32MiB business boundary", DefaultMaxRequestBodyBytes)
	}
	r := newSecurityTestEngine(t, BodyLimitMiddleware(0))
	// Valid 32MiB business payload (declared length) must pass the middleware layer (without reading body).
	req := httptest.NewRequest(http.MethodPost, "/api/v4/auth/login", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = 32 << 20
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("declared 32MiB body must pass limit %d, got %d", DefaultMaxRequestBodyBytes, w.Code)
	}
}

// TestRequestSchemeTrustMatrix NEW-AUTH-01: OIDC redirect_uri scheme
// trusts the forwarded header only when RemoteAddr belongs to a trusted proxy CIDR and XFP is in {http,https};
// otherwise inferred from request TLS state -- untrusted XFP tampering must never alter
// redirect_uri scheme.
func TestRequestSchemeTrustMatrix(t *testing.T) {
	cases := []struct {
		name       string
		proxies    []string
		remoteAddr string
		xfp        string
		tls        bool
		want       string
	}{
		{"trusted proxy XFP=https honored", []string{"10.0.0.0/8"}, "10.1.2.3:5555", "https", false, "https"},
		{"trusted proxy XFP=http honored", []string{"10.0.0.0/8"}, "10.1.2.3:5555", "http", true, "http"},
		{"untrusted proxy XFP ignored (plain falls to http)", []string{"10.0.0.0/8"}, "203.0.113.5:44321", "https", false, "http"},
		{"untrusted proxy XFP ignored (TLS falls to https)", []string{"10.0.0.0/8"}, "203.0.113.5:44321", "https", true, "https"},
		{"no proxy config: XFP ignored", nil, "10.1.2.3:5555", "https", false, "http"},
		{"no proxy config: TLS state wins", nil, "10.1.2.3:5555", "https", true, "https"},
		{"malformed XFP value ignored", []string{"10.0.0.0/8"}, "10.1.2.3:5555", "ftp", false, "http"},
		{"malformed XFP value with TLS ignored", []string{"10.0.0.0/8"}, "10.1.2.3:5555", "ftp", true, "https"},
		{"empty XFP ignored", []string{"10.0.0.0/8"}, "10.1.2.3:5555", "", true, "https"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := &B3Deps{TrustedProxies: tc.proxies}
			req := httptest.NewRequest(http.MethodGet, "/api/v4/auth/oidc/p/start", nil)
			req.RemoteAddr = tc.remoteAddr
			if tc.xfp != "" {
				req.Header.Set("X-Forwarded-Proto", tc.xfp)
			}
			if tc.tls {
				req.TLS = &tls.ConnectionState{}
			}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = req
			if got := d.requestScheme(c); got != tc.want {
				t.Fatalf("requestScheme = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestParseTrustedProxyCIDRs CIDR parsing: CIDRs kept as-is, bare IPs promoted to host routes,
// invalid entries skipped, whitespace ignored; matches both IPv4 and IPv6.
func TestParseTrustedProxyCIDRs(t *testing.T) {
	cidrs := parseTrustedProxyCIDRs([]string{
		"10.0.0.0/8", "192.168.1.7", "fd00:ec2::/48", " 2001:db8::5 ", "garbage", "",
	})
	if len(cidrs) != 4 {
		t.Fatalf("parsed = %d entries, want 4 (invalid/blank skipped)", len(cidrs))
	}
	checks := []struct {
		addr string
		want bool
	}{
		{"10.200.1.2", true}, {"11.0.0.1", false},
		{"192.168.1.7", true}, {"192.168.1.8", false},
		{"fd00:ec2::99", true}, {"fd00:ec3::1", false},
		{"2001:db8::5", true}, {"2001:db8::6", false},
	}
	for _, c := range checks {
		ip := net.ParseIP(c.addr)
		if ip == nil {
			t.Fatalf("bad test addr %q", c.addr)
		}
		in := false
		for _, cidr := range cidrs {
			if cidr.Contains(ip) {
				in = true
				break
			}
		}
		if in != c.want {
			t.Errorf("%q membership = %v, want %v", c.addr, in, c.want)
		}
	}
	// Empty config parses to an empty table: no address is trusted.
	if got := parseTrustedProxyCIDRs(nil); len(got) != 0 {
		t.Fatalf("nil config parsed = %d entries, want 0", len(got))
	}
	if remoteAddrInTrustedProxies("10.1.2.3:5555", nil) {
		t.Fatal("empty trusted table must trust nobody")
	}
}

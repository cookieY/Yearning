// security.go HTTP boundary security middleware:
//   - RequireSameOrigin: same-origin validation for anonymous auth entry points (SEC-0004);
//   - SecurityHeadersMiddleware: unified CSP/nosniff/XFO/Referrer-Policy/HSTS
//     security response headers (SEC-0012/SEC-0033);
//   - APINoStoreMiddleware: Cache-Control: no-store for /api/* responses (SEC-0034);
//   - BodyLimitMiddleware: unified request body size limit (SEC-0023).
package httpapi

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
)

// RequireSameOrigin validates request origin on anonymous auth entry points (SEC-0004;
// api-conventions S8 "state-changing requests must pass same-origin checks").
// Login CSRF relies on cross-site forms / top-level POSTs planting an attacker session into a victim browser:
// browser cross-site requests carrying an Origin/Referer mismatching Host or
// Sec-Fetch-Site: cross-site are rejected with 403; non-browser clients omitting all three
// headers (CLI, probes, httptest cases) are allowed through.
//
// Not applicable to IdP redirect navigation endpoints (e.g. OIDC callback, Review R2 [High]): those
// requests are cross-site top-level GET navigations initiated by IdP 302 redirects -- real browsers
// send Sec-Fetch-Site: cross-site and an IdP-domain Referer, both of which this middleware would reject
// with 403. Browser-side defense for such endpoints is provided by one-time credential binding: OIDC
// callback verifies the HttpOnly+SameSite=Lax state cookie set during start (403 state mismatch) and PKCE.
// Mount only on site-initiated anonymous write entry points (login/register/ldap login/oidc start).
func RequireSameOrigin() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !sameOriginRequest(c.Request) {
			WriteProblem(c, http.StatusForbidden, "cross-site request rejected")
			c.Abort()
			return
		}
		c.Next()
	}
}

// sameOriginRequest reports whether the request origin is acceptable:
//   - Sec-Fetch-Site present and equal to cross-site: reject (set by browser,
//     cannot be forged as same-origin in cross-site contexts);
//   - Origin present: must be a valid URL other than "null" with host matching request Host;
//   - Origin absent but Referer present: parsed host must match request Host;
//   - All three absent: treated as non-browser client and allowed.
func sameOriginRequest(r *http.Request) bool {
	if site := strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")); site != "" {
		if strings.EqualFold(site, "cross-site") {
			return false
		}
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin != "" {
		if strings.EqualFold(origin, "null") {
			return false
		}
		u, err := url.Parse(origin)
		if err != nil || u.Host == "" || !equalHost(u.Host, r.Host) {
			return false
		}
		return true
	}
	if referrer := strings.TrimSpace(r.Header.Get("Referer")); referrer != "" {
		u, err := url.Parse(referrer)
		if err != nil || u.Host == "" || !equalHost(u.Host, r.Host) {
			return false
		}
	}
	return true
}

// equalHost compares hosts (including port) case-insensitively.
func equalHost(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

// contentSecurityPolicy unified CSP for SPA and API responses (SEC-0012). CSP directives are verified
// against frontend/dist artifacts: index.html has no inline scripts; Monaco workers load from same origin
// via new Worker(new URL('/assets/*.worker-*.js')); SQL editor CSS uses inline data:image/png and
// data:image/svg+xml images; fonts are self-hosted woff2/ttf (/fonts and /assets). Thus script/worker/font/connect
// are restricted to 'self', style retains 'unsafe-inline' (for runtime style tags injected by UI libraries),
// and img allows data:.
const contentSecurityPolicy = "default-src 'self'; script-src 'self'; " +
	"style-src 'self' 'unsafe-inline'; img-src 'self' data:; " +
	"font-src 'self'; connect-src 'self'; worker-src 'self'; " +
	"frame-ancestors 'none'; object-src 'none'; base-uri 'self'; " +
	"form-action 'self'"

// hstsHeaderValue HSTS is emitted only on TLS requests (SEC-0033). Under RFC 6797 user agents
// ignore HSTS over plaintext HTTP, so we emit it only over TLS for semantic accuracy; deployments terminating
// TLS at a load balancer take effect once trusted proxy detection is wired (see SEC-0005 fix notes).
const hstsHeaderValue = "max-age=31536000"

// SecurityHeadersMiddleware attaches security response headers to all responses (API and SPA).
// Must be registered before Recovery so panic fallback responses also carry security headers.
func SecurityHeadersMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.Writer.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		if c.Request.TLS != nil {
			h.Set("Strict-Transport-Security", hstsHeaderValue)
		}
		c.Next()
	}
}

// APINoStoreMiddleware sets Cache-Control: no-store on all /api and /api/* responses
// (SEC-0034): sensitive GETs such as query result pages, Evidence, change order details, and Reveals
// must never be cached by browsers or shared proxies; SPA static assets (content-hashed filenames)
// are unaffected and retain the assets handler caching policy.
func APINoStoreMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if p := c.Request.URL.Path; p == "/api" || strings.HasPrefix(p, "/api/") {
			c.Writer.Header().Set("Cache-Control", "no-store")
		}
		c.Next()
	}
}

// DefaultMaxRequestBodyBytes default unified request body size limit (SEC-0023): maximum
// business payload boundary is a 32MiB SQL batch, so the global default uses a 2x margin of 64MiB.
// Deployments requiring a tighter cap can override via ServeDeps.MaxRequestBodyBytes.
const DefaultMaxRequestBodyBytes int64 = 64 << 20

// BodyLimitMiddleware caps request body size using http.MaxBytesReader (SEC-0023).
// Requests with Content-Length exceeding the limit are immediately rejected with 413 Problem Details
// (api-conventions: protocol-layer failures use real HTTP status codes); chunked streams with unknown
// length are truncated by MaxBytesReader upon exceeding the limit to prevent memory exhaustion.
func BodyLimitMiddleware(limit int64) gin.HandlerFunc {
	if limit <= 0 {
		limit = DefaultMaxRequestBodyBytes
	}
	return func(c *gin.Context) {
		if c.Request.ContentLength > limit {
			WriteProblem(c, http.StatusRequestEntityTooLarge, "request body too large")
			c.Abort()
			return
		}
		if c.Request.Body != nil {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limit)
		}
		c.Next()
	}
}

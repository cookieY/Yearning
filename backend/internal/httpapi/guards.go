// guards.go operation-level authorization guards and CSRF validation.
//
// Default deny: every route binds an operationId, and requests failing policy evaluation
// are rejected before reaching the business layer (401 unauthenticated / 403 management forbidden);
// business actor relationship failures are returned by the business layer as HTTP 200 + err_code without leaking resource existence.
package httpapi

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/cookieY/YearningX/backend/internal/authorization"
	"github.com/cookieY/YearningX/backend/internal/authorization/bizerr"
	"github.com/cookieY/YearningX/backend/internal/identity"
	"github.com/cookieY/YearningX/backend/internal/identity/session"
)

// csrfHeader is the CSRF token header required on state-changing requests.
const csrfHeader = "X-CSRF-Token"

// RequireOperation returns a guard middleware bound to the given operation.
// Also enforces double-submit CSRF validation on state-changing methods (ADR-0004).
func RequireOperation(op string, sessions *identity.SessionStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := session.FromContext(c.Request.Context())
		if !authorization.Decide(op, id) {
			class := authorization.Classify(id)
			if class == authorization.ActorAnonymous {
				WriteProblem(c, http.StatusUnauthorized, "authentication required")
			} else {
				WriteProblem(c, http.StatusForbidden, "permission denied")
			}
			c.Abort()
			return
		}
		if authorization.IsMutating(c.Request.Method) && sessions != nil &&
			!authorization.IsAnonymousOperation(op) {
			if !verifyMutationCSRF(c, sessions) {
				return
			}
		}
		c.Next()
	}
}

// RequireBusinessOperation enforces default-deny business operation registration, session authentication,
// and double-submit CSRF validation before delegating actor-relationship checks to the application layer.
func RequireBusinessOperation(op string, sessions *identity.SessionStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := session.FromContext(c.Request.Context())
		if id == nil {
			WriteProblem(c, http.StatusUnauthorized, "authentication required")
			c.Abort()
			return
		}
		if !authorization.IsBusinessRelationshipOperation(op) {
			WriteProblem(c, http.StatusForbidden, "permission denied")
			c.Abort()
			return
		}
		if authorization.IsMutating(c.Request.Method) {
			if !verifyMutationCSRF(c, sessions) {
				return
			}
		}
		c.Next()
	}
}

func verifyMutationCSRF(c *gin.Context, sessions *identity.SessionStore) bool {
	if sessions == nil {
		WriteProblem(c, http.StatusForbidden, "csrf validation unavailable")
		c.Abort()
		return false
	}
	raw, err := c.Cookie(sessionCookieName)
	if err != nil || raw == "" {
		WriteProblem(c, http.StatusUnauthorized, "authentication required")
		c.Abort()
		return false
	}
	token := c.GetHeader(csrfHeader)
	if token == "" {
		WriteProblem(c, http.StatusForbidden, "csrf token missing")
		c.Abort()
		return false
	}
	want, err := sessions.CSRFHashByToken(c.Request.Context(), raw)
	if err != nil || identity.TokenHash(token) != want {
		WriteProblem(c, http.StatusForbidden, "csrf token invalid")
		c.Abort()
		return false
	}
	return true
}

// sessionCookieName matches OpenAPI securitySchemes.
const sessionCookieName = "yearning_session"

// csrfCookieName CSRF token issued as a readable cookie (double-submit pattern).
const csrfCookieName = "yearning_csrf"

// oidcStateCookieName browser-binding cookie for OIDC state (SEC-0004): start issues a short-lived
// HttpOnly+SameSite=Lax cookie, and callback verifies and clears it, blocking session swapping where
// an attacker hands a callback URL to a victim. Path is scoped to the OIDC entry prefix.
const oidcStateCookieName = "yearning_oidc_state"

// issueSessionCookies writes session and CSRF cookies.
func issueSessionCookies(c *gin.Context, issued *identity.IssuedSession, secure bool) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name: sessionCookieName, Value: issued.Token, Path: "/",
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode,
		Expires: issued.ExpiresAt,
	})
	http.SetCookie(c.Writer, &http.Cookie{
		Name: csrfCookieName, Value: issued.CSRFToken, Path: "/",
		HttpOnly: false, Secure: secure, SameSite: http.SameSiteLaxMode,
		Expires: issued.ExpiresAt,
	})
}

// clearSessionCookies clears cookies upon logout.
func clearSessionCookies(c *gin.Context, secure bool) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name: sessionCookieName, Value: "", Path: "/",
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode,
		MaxAge: -1,
	})
	http.SetCookie(c.Writer, &http.Cookie{
		Name: csrfCookieName, Value: "", Path: "/",
		HttpOnly: false, Secure: secure, SameSite: http.SameSiteLaxMode,
		MaxAge: -1,
	})
}

// parseIfMatch parses the If-Match optimistic concurrency version (quoted "N").
func parseIfMatch(c *gin.Context) (int64, bool) {
	raw := strings.TrimSpace(c.GetHeader("If-Match"))
	if len(raw) < 3 || raw[0] != '"' || raw[len(raw)-1] != '"' {
		WriteBusinessError(c, &BusinessError{
			Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED",
			Message: "If-Match header must be a quoted version number",
		})
		return 0, false
	}
	version, err := strconv.ParseInt(raw[1:len(raw)-1], 10, 64)
	if err != nil || version < 1 {
		WriteBusinessError(c, &BusinessError{
			Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED",
			Message: "Invalid If-Match version",
		})
		return 0, false
	}
	return version, true
}

// requestMeta extracts audit metadata from the request.
func requestMeta(c *gin.Context) identity.RequestMeta {
	return identity.RequestMeta{
		RequestID: RequestID(c),
		SourceIP:  c.ClientIP(),
		UserAgent: c.GetHeader("User-Agent"),
	}
}

// actorOf extracts the currently authenticated management actor.
func actorOf(c *gin.Context) identity.ActorRef {
	id := session.FromContext(c.Request.Context())
	if id == nil {
		return identity.ActorRef{UserID: "", Username: "anonymous"}
	}
	return identity.ActorRef{UserID: id.UserID, Username: id.Username}
}

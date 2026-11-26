// handlers_identity.go identity domain HTTP handlers: authentication, local/LDAP/OIDC login,
// current user profile, user management, and identity Providers. Responses follow OpenAPI envelopes;
// Secrets are write-only (view layer contains no credential fields).
package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	"github.com/cookieY/YearningX/backend/internal/authorization/bizerr"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/datasource"
	"github.com/cookieY/YearningX/backend/internal/identity"
	"github.com/cookieY/YearningX/backend/internal/identity/session"
	"github.com/cookieY/YearningX/backend/internal/platform/systz"
	"github.com/cookieY/YearningX/backend/internal/settings"
	"github.com/cookieY/YearningX/backend/internal/workflow"
)

// B3Deps aggregates B3 domain services and platform dependencies.
type B3Deps struct {
	Identity     *identity.Service
	Sessions     *identity.SessionStore
	Workflow     *workflow.Service
	Datasource   *datasource.Service
	Recorder     *auditlog.Recorder
	DB           *gorm.DB
	Crypter      *crypto.Crypter
	SecureCookie bool
	// TrustedProxies trusted proxy CIDRs (NEW-AUTH-01, shared with ServeDeps and wired by
	// NewServeRouter): X-Forwarded-Proto is trusted only when RemoteAddr falls within these CIDRs.
	// Empty = trust no proxies (direct connection semantics, XFP never trusted).
	TrustedProxies []string
}

// issueSession issues session credentials for an authenticated user.
func (d *B3Deps) issueSession(c *gin.Context, userID string) (*identity.IssuedSession, error) {
	_, token, csrf, expiresAt, err := d.Sessions.IssueSession(c.Request.Context(), userID)
	if err != nil {
		return nil, err
	}
	return &identity.IssuedSession{Token: token, CSRFToken: csrf, ExpiresAt: expiresAt}, nil
}

// ---- Authentication ----

type loginRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

func (d *B3Deps) HandleLogin(c *gin.Context) {
	var req loginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid request parameters"})
		return
	}
	issued, err := d.Identity.LoginLocal(c.Request.Context(), req.Username, req.Password, requestMeta(c))
	if err != nil {
		if !writeDomainError(c, err, 0) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	issueSessionCookies(c, issued, d.SecureCookie)
	WriteSuccess(c, nil)
}

func (d *B3Deps) HandleLogout(c *gin.Context) {
	raw, err := c.Cookie(sessionCookieName)
	if err == nil && raw != "" {
		_ = d.Sessions.RevokeByToken(c.Request.Context(), raw, "logout")
	}
	clearSessionCookies(c, d.SecureCookie)
	WriteSuccess(c, nil)
}

type registerRequest struct {
	Username    string  `json:"username" binding:"required"`
	DisplayName string  `json:"display_name" binding:"required"`
	Email       *string `json:"email"`
	Password    string  `json:"password" binding:"required"`
}

func (d *B3Deps) HandleRegister(c *gin.Context) {
	var req registerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid request parameters"})
		return
	}
	email := ""
	if req.Email != nil {
		email = *req.Email
	}
	user, err := d.Identity.Register(c.Request.Context(), req.Username, req.DisplayName, email, req.Password, requestMeta(c))
	if err != nil {
		if !writeDomainError(c, err, 0) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	view := identity.UserViewOf(user)
	WriteSuccess(c, &view)
}

func (d *B3Deps) HandleListAuthProviders(c *gin.Context) {
	ctx := c.Request.Context()
	oidcProviders, err := d.Identity.ListEnabledOIDC(ctx)
	if err != nil {
		WriteProblem(c, http.StatusInternalServerError, "internal error")
		return
	}
	oidc := make([]gin.H, 0, len(oidcProviders))
	for _, p := range oidcProviders {
		oidc = append(oidc, gin.H{
			"key":       p.ProviderKey,
			"label":     p.DisplayName,
			"start_url": "/api/v4/auth/oidc/" + p.ProviderKey + "/start",
		})
	}
	branding := settings.ReadPublicBranding(ctx, d.DB)
	WriteSuccess(c, gin.H{
		"local":    true,
		"ldap":     d.Identity.LDAPEnabled(ctx),
		"oidc":     oidc,
		"branding": branding,
	})
}

func (d *B3Deps) HandleLdapLogin(c *gin.Context) {
	var req loginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid request parameters"})
		return
	}
	ctx := c.Request.Context()
	row, cfg, secret, err := d.Identity.LDAPProvider(ctx)
	if err != nil {
		if !writeDomainError(c, err, 0) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	// SEC-0006: LDAP login uses failure rate limiting isomorphic to LoginLocal
	// (by provider + username + source IP) to prevent online brute-force attacks against enterprise directories.
	rateKey := "ext:ldap:" + row.ProviderKey + "|" + strings.ToLower(req.Username) + "|" + requestMeta(c).SourceIP
	if !d.Identity.AllowLoginAttempt(rateKey) {
		d.Identity.AuditExternalLogin(ctx, "login_ldap", req.Username, "denied", nil, requestMeta(c))
		WriteProblem(c, http.StatusTooManyRequests, "login rate limited")
		return
	}
	result, err := identity.LDAPAuthenticate(cfg, string(secret), req.Username, req.Password)
	if err != nil || result == nil {
		// External authentication failures use a uniform message without disclosing account or configuration details.
		d.Identity.AuditExternalLogin(ctx, "login_ldap", req.Username, "failed", nil, requestMeta(c))
		WriteBusinessError(c, &BusinessError{Code: bizerr.CodeInvalidCredentials, Name: "INVALID_CREDENTIALS", Message: "Invalid username or password"})
		return
	}
	user, err := d.Identity.UpsertExternalUser(ctx, "ldap", row.ProviderKey, result.Username,
		result.Username, result.DisplayName, result.Email, requestMeta(c))
	if err != nil {
		d.Identity.AuditExternalLogin(ctx, "login_ldap", result.Username, "failed", nil, requestMeta(c))
		if !writeDomainError(c, err, 0) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	d.Identity.RecordLoginSuccess(rateKey)
	d.Identity.AuditExternalLogin(ctx, "login_ldap", result.Username, "succeeded", user, requestMeta(c))
	issued, err := d.issueSession(c, user.ID)
	if err != nil {
		WriteProblem(c, http.StatusInternalServerError, "internal error")
		return
	}
	issueSessionCookies(c, issued, d.SecureCookie)
	WriteSuccess(c, nil)
}

// oidcStateCookieTTL TTL for state-binding cookie: covers stateTTL (5 minutes)
// plus redirect latency margin; state expiry is enforced by VerifyState while cookie expires on its own.
const oidcStateCookieTTL = 10 * time.Minute

// clearOidcStateCookie clears the state-binding cookie upon callback completion (success or failure),
// preventing residual cookies from being reused in subsequent flows.
func clearOidcStateCookie(c *gin.Context, secure bool) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name: oidcStateCookieName, Value: "", Path: "/api/v4/auth/oidc",
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode,
		MaxAge: -1,
	})
}

func (d *B3Deps) HandleOidcStart(c *gin.Context) {
	providerKey := c.Param("provider_key")
	ctx := c.Request.Context()
	// SEC-0006: start entry rate limiting precedes all business checks (provider lookup, IdP
	// discovery) -- isomorphic to LoginLocal SEC-0058, preventing rate-limit behavior from becoming
	// a provider existence oracle; key is built from path parameters so unauthenticated callers cannot
	// drive outbound IdP discovery at arbitrary rates. Bucket is isolated from callback key (ext:oidc:).
	rateKey := "ext:oidc-start:" + providerKey + "|" + requestMeta(c).SourceIP
	if !d.Identity.AllowLoginAttempt(rateKey) {
		d.Identity.AuditExternalLogin(ctx, "login_oidc", "", "denied", nil, requestMeta(c))
		WriteProblem(c, http.StatusTooManyRequests, "login rate limited")
		return
	}
	_, cfg, secret, err := d.Identity.OIDCProviderByKey(ctx, providerKey)
	if err != nil {
		WriteProblem(c, http.StatusUnauthorized, "provider unavailable")
		return
	}
	// Discovery metadata is cached in-process with TTL (identity.NewOIDCProviderClient),
	// so repeated starts within TTL do not amplify traffic to the IdP.
	client, err := identity.NewOIDCProviderClient(ctx, cfg, string(secret))
	if err != nil {
		WriteProblem(c, http.StatusInternalServerError, "internal error")
		return
	}
	scheme := d.requestScheme(c)
	redirect := scheme + "://" + c.Request.Host + "/api/v4/auth/oidc/" + providerKey + "/callback"
	authURL, state, err := client.StartLogin(d.Crypter, providerKey, redirect)
	if err != nil {
		WriteProblem(c, http.StatusInternalServerError, "internal error")
		return
	}
	// SEC-0004: bind state to initiating browser -- short-lived HttpOnly+SameSite=Lax cookie;
	// callback must carry the matching state to proceed, blocking login CSRF / session swapping where
	// an attacker hands a callback URL to a victim. Lax cookie is sent on top-level GET redirect back to callback.
	http.SetCookie(c.Writer, &http.Cookie{
		Name: oidcStateCookieName, Value: state, Path: "/api/v4/auth/oidc",
		MaxAge:   int(oidcStateCookieTTL.Seconds()),
		HttpOnly: true, Secure: d.SecureCookie, SameSite: http.SameSiteLaxMode,
	})
	c.Redirect(http.StatusFound, authURL)
}

func (d *B3Deps) HandleOidcCallback(c *gin.Context) {
	providerKey := c.Param("provider_key")
	code := c.Query("code")
	state := c.Query("state")
	ctx := c.Request.Context()
	// SEC-0004: callback state must match the HttpOnly cookie set during start,
	// otherwise treat as a forwarded callback URL (login CSRF / session swap), reject with 403, and clear cookie.
	cookieState, err := c.Cookie(oidcStateCookieName)
	if err != nil || cookieState == "" || cookieState != state {
		clearOidcStateCookie(c, d.SecureCookie)
		WriteProblem(c, http.StatusForbidden, "oidc state mismatch")
		return
	}
	clearOidcStateCookie(c, d.SecureCookie)
	// SEC-0006: OIDC callback applies failure rate limiting (by provider + source IP)
	// to prevent high-frequency callbacks from amplifying IdP load or brute-forcing the callback surface.
	rateKey := "ext:oidc:" + providerKey + "|" + requestMeta(c).SourceIP
	if !d.Identity.AllowLoginAttempt(rateKey) {
		d.Identity.AuditExternalLogin(ctx, "login_oidc", "", "denied", nil, requestMeta(c))
		WriteProblem(c, http.StatusTooManyRequests, "login rate limited")
		return
	}
	_, cfg, secret, err := d.Identity.OIDCProviderByKey(ctx, providerKey)
	if err != nil {
		// SEC-0006: record audit log when provider is missing or disabled (previously 401 wrote no audit,
		// leaving a blind spot on callback surface); external message remains sanitized without distinguishing missing vs disabled.
		d.Identity.AuditExternalLogin(ctx, "login_oidc", "", "failed", nil, requestMeta(c))
		WriteProblem(c, http.StatusUnauthorized, "provider unavailable")
		return
	}
	client, err := identity.NewOIDCProviderClient(ctx, cfg, string(secret))
	if err != nil {
		// SEC-0006: client construction failure (discovery/config issue, 500 fallback) also
		// records an audit event without exposing failure details externally.
		d.Identity.AuditExternalLogin(ctx, "login_oidc", "", "failed", nil, requestMeta(c))
		WriteProblem(c, http.StatusInternalServerError, "internal error")
		return
	}
	ident, err := client.FinishLogin(ctx, d.Crypter, providerKey, code, state)
	if err != nil {
		d.Identity.AuditExternalLogin(ctx, "login_oidc", identUsername(ident), "failed", nil, requestMeta(c))
		WriteProblem(c, http.StatusUnauthorized, "oidc login failed")
		return
	}
	user, err := d.Identity.UpsertExternalUser(ctx, "oidc", providerKey, ident.Subject,
		ident.Username, ident.DisplayName, ident.Email, requestMeta(c))
	if err != nil {
		// Uniform failure exit: does not distinguish failures other than binding conflicts and external admin collision.
		d.Identity.AuditExternalLogin(ctx, "login_oidc", ident.Username, "failed", nil, requestMeta(c))
		WriteProblem(c, http.StatusUnauthorized, "oidc login failed")
		return
	}
	d.Identity.RecordLoginSuccess(rateKey)
	d.Identity.AuditExternalLogin(ctx, "login_oidc", ident.Username, "succeeded", user, requestMeta(c))
	issued, err := d.issueSession(c, user.ID)
	if err != nil {
		WriteProblem(c, http.StatusInternalServerError, "internal error")
		return
	}
	issueSessionCookies(c, issued, d.SecureCookie)
	c.Redirect(http.StatusFound, "/")
}

// identUsername falls back to empty string for audit snapshot when ident is nil on FinishLogin failure.
func identUsername(ident *identity.OIDCIdentity) string {
	if ident == nil {
		return ""
	}
	return ident.Username
}

// ---- Current user ----

func (d *B3Deps) HandleCurrentUser(c *gin.Context) {
	id := session.FromContext(c.Request.Context())
	if id == nil {
		WriteProblem(c, http.StatusUnauthorized, "authentication required")
		return
	}
	var user identity.UserModel
	if err := d.DB.WithContext(c.Request.Context()).
		Where("id = ?", id.UserID).First(&user).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			WriteProblem(c, http.StatusUnauthorized, "authentication required")
			return
		}
		WriteProblem(c, http.StatusInternalServerError, "internal error")
		return
	}
	view := identity.UserViewOf(&user)
	provider, err := d.Identity.AuthProviderOf(c.Request.Context(), user.ID)
	if err != nil {
		WriteProblem(c, http.StatusInternalServerError, "internal error")
		return
	}
	systemTimezone := systz.Name(c.Request.Context(), d.DB)
	isBuiltinAdmin, role, canAccessAdmin := currentUserRoleClaims(id, view)
	WriteSuccess(c, gin.H{
		"id": view.ID, "username": view.Username, "display_name": view.DisplayName,
		"email": view.Email, "is_builtin_admin": isBuiltinAdmin, "role": role,
		"version": view.Version, "created_at": view.CreatedAt, "updated_at": view.UpdatedAt,
		"can_access_admin": canAccessAdmin, "auth_provider": provider, "system_timezone": systemTimezone,
	})
}

// HandleChangeMyPassword self-service password change (changeMyPassword). Revokes all sessions
// for the user upon success; client should redirect to the login page.
func (d *B3Deps) HandleChangeMyPassword(c *gin.Context) {
	id := session.FromContext(c.Request.Context())
	if id == nil {
		WriteProblem(c, http.StatusUnauthorized, "authentication required")
		return
	}
	var req struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		WriteBusinessError(c, &BusinessError{
			Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED",
			Message: "Invalid request parameters",
		})
		return
	}
	if err := d.Identity.ChangeMyPassword(c.Request.Context(), id.UserID,
		req.OldPassword, req.NewPassword, requestMeta(c)); err != nil {
		if !writeDomainError(c, err, 0) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	WriteSuccess(c, nil)
}

// ---- Management surface: Users ----

func (d *B3Deps) HandleListUsers(c *gin.Context) {
	kind, ok := filterEnumQuery(c, "kind", "builtin_admin", "local_user")
	if !ok {
		return
	}
	items, next, err := d.Identity.ListUsers(c.Request.Context(), intQuery(c, "limit", 50), c.Query("after"), c.Query("q"), kind)
	if err != nil {
		if !writeDomainError(c, err, 0) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	writePage(c, items, next)
}

func (d *B3Deps) HandleCreateUser(c *gin.Context) {
	var req struct {
		Username    string  `json:"username" binding:"required"`
		DisplayName string  `json:"display_name" binding:"required"`
		Email       *string `json:"email"`
		Password    string  `json:"password" binding:"required"`
		Role        *string `json:"role"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid request parameters"})
		return
	}
	email := ""
	if req.Email != nil {
		email = *req.Email
	}
	role := "user"
	if req.Role != nil && *req.Role != "" {
		role = *req.Role
	}
	view, err := d.Identity.CreateUser(c.Request.Context(), actorOf(c), req.Username, req.DisplayName, email, req.Password, requestMeta(c), role)
	if err != nil {
		if !writeDomainError(c, err, 0) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	WriteSuccess(c, view)
}

func (d *B3Deps) HandleUpdateUser(c *gin.Context) {
	version, ok := parseIfMatch(c)
	if !ok {
		return
	}
	var req struct {
		DisplayName *string `json:"display_name"`
		Email       *string `json:"email"`
		Role        *string `json:"role"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid request parameters"})
		return
	}
	view, err := d.Identity.UpdateUser(c.Request.Context(), actorOf(c), c.Param("user_id"), version, req.DisplayName, req.Email, requestMeta(c), req.Role)
	if err != nil {
		if !writeDomainError(c, err, 0) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	WriteSuccess(c, view)
}

// HandleResetUserPassword resets a local non-builtin user's password as an administrator (resetUserPassword).
func (d *B3Deps) HandleResetUserPassword(c *gin.Context) {
	var req struct {
		NewPassword string `json:"new_password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid request parameters"})
		return
	}
	if err := d.Identity.ResetUserPassword(c.Request.Context(), actorOf(c), c.Param("user_id"), req.NewPassword, requestMeta(c)); err != nil {
		if !writeDomainError(c, err, 0) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	WriteSuccess(c, nil)
}

func (d *B3Deps) HandleDeleteUser(c *gin.Context) {
	version, ok := parseIfMatch(c)
	if !ok {
		return
	}
	err := d.Identity.DeleteUser(c.Request.Context(), actorOf(c), c.Param("user_id"), version, requestMeta(c))
	if err != nil {
		if !writeDomainError(c, err, 0) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	WriteSuccess(c, nil)
}

func (d *B3Deps) HandleUserDeletionImpact(c *gin.Context) {
	impact, err := d.Identity.GetDeletionImpact(c.Request.Context(), actorOf(c), c.Param("user_id"))
	if err != nil {
		if !writeDomainError(c, err, 0) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	WriteSuccess(c, impact)
}

// ---- Management surface: Identity Providers ----

type providerWriteRequest struct {
	ProviderKey   string          `json:"provider_key"`
	ProviderKind  string          `json:"provider_kind"`
	DisplayName   string          `json:"display_name"`
	Enabled       bool            `json:"enabled"`
	Configuration json.RawMessage `json:"configuration"`
	BindPassword  *secretInput    `json:"bind_password"`
	ClientSecret  *secretInput    `json:"client_secret"`
}

type secretInput struct {
	Value string `json:"value"`
}

func (r *providerWriteRequest) secret() []byte {
	for _, s := range []*secretInput{r.BindPassword, r.ClientSecret} {
		if s != nil && s.Value != "" {
			return []byte(s.Value)
		}
	}
	return nil
}

func (d *B3Deps) HandleListIdentityProviders(c *gin.Context) {
	items, next, err := d.Identity.ListProviders(c.Request.Context(), intQuery(c, "limit", 50), c.Query("after"))
	if err != nil {
		if !writeDomainError(c, err, 0) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	writePage(c, items, next)
}

func (d *B3Deps) HandleCreateIdentityProvider(c *gin.Context) {
	var req providerWriteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid request parameters"})
		return
	}
	view, err := d.Identity.CreateProvider(c.Request.Context(), actorOf(c), req.ProviderKey, req.ProviderKind, req.DisplayName, req.Enabled, req.Configuration, req.secret(), requestMeta(c))
	if err != nil {
		if !writeDomainError(c, err, 0) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	WriteSuccess(c, view)
}

func (d *B3Deps) HandleGetIdentityProvider(c *gin.Context) {
	view, err := d.Identity.GetProvider(c.Request.Context(), c.Param("identity_provider_id"))
	if err != nil {
		if !writeDomainError(c, err, 0) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	WriteSuccess(c, view)
}

func (d *B3Deps) HandleReplaceIdentityProvider(c *gin.Context) {
	version, ok := parseIfMatch(c)
	if !ok {
		return
	}
	var req providerWriteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid request parameters"})
		return
	}
	view, err := d.Identity.ReplaceProvider(c.Request.Context(), actorOf(c), c.Param("identity_provider_id"), version, req.DisplayName, req.Enabled, req.Configuration, req.secret(), requestMeta(c))
	if err != nil {
		if !writeDomainError(c, err, 0) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	WriteSuccess(c, view)
}

func (d *B3Deps) HandleDeleteIdentityProvider(c *gin.Context) {
	version, ok := parseIfMatch(c)
	if !ok {
		return
	}
	err := d.Identity.DeleteProvider(c.Request.Context(), actorOf(c), c.Param("identity_provider_id"), version, requestMeta(c))
	if err != nil {
		if !writeDomainError(c, err, 0) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	WriteSuccess(c, nil)
}

// HandleTestIdentityProviderConnection runs a connectivity test using saved configuration and Secret;
// result contains only stage and outcome, omitting bind password, Client Secret, or claims.
func (d *B3Deps) HandleTestIdentityProviderConnection(c *gin.Context) {
	ctx := c.Request.Context()
	view, err := d.Identity.GetProvider(ctx, c.Param("identity_provider_id"))
	if err != nil {
		if !writeDomainError(c, err, 0) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	stages := []string{"load_secret"}
	switch view.ProviderKind {
	case "ldap":
		_, cfg, secret, err := d.Identity.LDAPProviderByID(ctx, view.ID)
		if err != nil {
			// F12 #013: configuration/secret load failures use domain error mapping (1002, etc.)
			// instead of always returning 500.
			if !writeDomainError(c, err, 0) {
				WriteProblem(c, http.StatusInternalServerError, "internal error")
			}
			return
		}
		stages = append(stages, "connect", "bind", "search")
		if err := identity.LDAPConnectionTest(cfg, string(secret)); err != nil {
			WriteBusinessError(c, &BusinessError{Code: 3010, Name: "DATASOURCE_UNAVAILABLE", Message: "LDAP connection test failed"})
			return
		}
	default:
		// Load by ID (symmetric with LDAP): disabled Providers can also run connectivity tests.
		_, cfg, secret, err := d.Identity.OIDCProviderByID(ctx, view.ID)
		if err != nil {
			if !writeDomainError(c, err, 0) {
				WriteProblem(c, http.StatusInternalServerError, "internal error")
			}
			return
		}
		stages = append(stages, "discovery", "jwks")
		if _, err := identity.NewOIDCProviderClient(ctx, cfg, string(secret)); err != nil {
			WriteBusinessError(c, &BusinessError{Code: 3010, Name: "DATASOURCE_UNAVAILABLE", Message: "OIDC discovery failed"})
			return
		}
	}
	WriteSuccess(c, gin.H{"stages": stages, "outcome": "succeeded"})
}

// ---- Trusted proxy & scheme helpers ----

// parseTrustedProxyCIDRs parses trusted proxy CIDRs (NEW-AUTH-01; self-contained with net package,
// zero new dependencies). Aligned with gin: CIDR first, bare IP becomes /32 or /128; invalid items
// skipped (router SetTrustedProxies already fails hard on invalid config; this is a defensive handler fallback).
func parseTrustedProxyCIDRs(entries []string) []*net.IPNet {
	cidrs := make([]*net.IPNet, 0, len(entries))
	for _, e := range entries {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if !strings.Contains(e, "/") {
			if ip := net.ParseIP(e); ip != nil {
				bits := 32
				if ip.To4() == nil {
					bits = 128
				}
				e = fmt.Sprintf("%s/%d", ip.String(), bits)
			}
		}
		if _, cidr, err := net.ParseCIDR(e); err == nil {
			cidrs = append(cidrs, cidr)
		}
	}
	return cidrs
}

// remoteAddrInTrustedProxies reports whether request RemoteAddr falls within trusted proxy CIDRs.
// Empty / unconfigured CIDRs always return false (trust no proxies).
func remoteAddrInTrustedProxies(remoteAddr string, cidrs []*net.IPNet) bool {
	if len(cidrs) == 0 {
		return false
	}
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, cidr := range cidrs {
		if cidr.Contains(ip) {
			return true
		}
	}
	return false
}

// requestScheme infers the externally visible request scheme (for building OIDC redirect_uri):
// trusts X-Forwarded-Proto only when connection comes from a trusted proxy CIDR and XFP is http or https,
// otherwise infers from request TLS state (NEW-AUTH-01: previously trusted XFP unconditionally,
// allowing untrusted sources to tamper with redirect_uri scheme; empty trusted list falls back to direct semantics).
func (d *B3Deps) requestScheme(c *gin.Context) string {
	proto := c.GetHeader("X-Forwarded-Proto")
	if (proto == "http" || proto == "https") &&
		remoteAddrInTrustedProxies(c.Request.RemoteAddr, parseTrustedProxyCIDRs(d.TrustedProxies)) {
		return proto
	}
	if c.Request.TLS != nil {
		return "https"
	}
	return "http"
}

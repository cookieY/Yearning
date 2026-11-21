// service.go identity application service: local/LDAP/OIDC login, registration, admin lockout,
// external identity binding, admin password reset, and built-in admin bootstrap.
package identity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	"github.com/cookieY/YearningX/backend/internal/authorization/bizerr"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/platform/txutil"
)

// ErrExternalAdminForbidden indicates an external identity attempted to claim the admin identity.
var ErrExternalAdminForbidden = errors.New("identity: external identity cannot claim admin")

// ErrRegistrationDisabled indicates self-service registration is disabled.
var ErrRegistrationDisabled = errors.New("identity: registration disabled")

// ErrRateLimited indicates login or registration attempts exceeded the rate limit.
var ErrRateLimited = errors.New("identity: rate limited")

// ErrRegistrationRejected is the uniform registration rejection sentinel (SEC-0061): reserved names,
// invalid parameters, and already-taken usernames share this sentinel and map to the same transport
// message — ensuring registration errors never leak username existence (matching login error posture).
var ErrRegistrationRejected = errors.New("identity: registration rejected")

// RequestMeta carries request context metadata required for auditing.
type RequestMeta struct {
	RequestID string
	SourceIP  string
	UserAgent string
}

// Service is the identity application service.
type Service struct {
	db       *gorm.DB
	sessions *SessionStore
	crypter  *crypto.Crypter
	audit    *auditlog.Recorder
	limit    *loginRateLimiter
}

// DefaultSessionTTL is the default server-side session lifetime.
const DefaultSessionTTL = 12 * time.Hour

// NewService constructs the identity service. crypter is required only when reading or writing
// Provider Secrets (and may be nil in CLI password reset flows).
func NewService(db *gorm.DB, sessions *SessionStore, crypter *crypto.Crypter, audit *auditlog.Recorder) (*Service, error) {
	if db == nil || sessions == nil || audit == nil {
		return nil, errors.New("identity: nil dependency")
	}
	return &Service{db: db, sessions: sessions, crypter: crypter, audit: audit, limit: newLoginRateLimiter()}, nil
}

// Recorder returns the attached auditlog.Recorder.
func (s *Service) Recorder() *auditlog.Recorder {
	if s == nil {
		return nil
	}
	return s.audit
}

// loginRateLimiter rate-limits failed login attempts for standard accounts: at most failures attempts per key per window.
type loginRateLimiter struct {
	mu      sync.Mutex
	buckets map[string][]time.Time
}

const (
	rateWindow   = time.Minute
	rateMaxFails = 10
)

func newLoginRateLimiter() *loginRateLimiter {
	return &loginRateLimiter{buckets: map[string][]time.Time{}}
}

// maxBuckets is the threshold triggering a full sweep to prevent attackers from growing the map unboundedly with distinct keys.
const maxBuckets = 8192

func (l *loginRateLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	kept := l.buckets[key][:0]
	for _, t := range l.buckets[key] {
		if now.Sub(t) < rateWindow {
			kept = append(kept, t)
		}
	}
	if len(kept) == 0 {
		// Evict expired window entries on the fly so the map does not grow unboundedly with the keyspace.
		delete(l.buckets, key)
	} else {
		l.buckets[key] = kept
	}
	if len(l.buckets) > maxBuckets {
		for k, ts := range l.buckets {
			if len(ts) == 0 || now.Sub(ts[len(ts)-1]) >= rateWindow {
				delete(l.buckets, k)
			}
		}
	}
	if kept := l.buckets[key]; len(kept) >= rateMaxFails {
		return false
	}
	l.buckets[key] = append(l.buckets[key], now)
	return true
}

// success removes the attempt record after a successful login: the window counts only failures
// (per the contract "at most failures attempts per key per window" — otherwise a local user legitimately
// logging in more than 10 times per minute would be rate-limited, as observed in F12 integration testing).
func (l *loginRateLimiter) success(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.buckets, key)
}

// AllowLoginAttempt checks the failed-login rate limit for external identities (LDAP/OIDC)
// (SEC-0006): shares the same limiter and window as LoginLocal (at most 10 failures per key per minute),
// using caller-constructed keys that include the provider dimension, e.g.
// "ext:ldap:<provider_key>|<username>|<ip>" or "ext:oidc:<provider_key>|<ip>".
// The ext: prefix isolates buckets from local login keys (<username>|<ip>).
func (s *Service) AllowLoginAttempt(key string) bool { return s.limit.allow(key) }

// RecordLoginSuccess clears the failure counter for key after a successful external login
// (counting only failures within the window, matching LoginLocal semantics).
func (s *Service) RecordLoginSuccess(key string) { s.limit.success(key) }

// AuditExternalLogin records an audit event for external identity login (SEC-0006).
// action is login_ldap/login_oidc and outcome is succeeded/failed/denied;
// on failure paths where no local user row exists yet, pass user=nil (leaving ActorID empty while
// preserving the submitted username snapshot); messages never include detailed failure reasons.
func (s *Service) AuditExternalLogin(ctx context.Context, action, username, outcome string, user *UserModel, meta RequestMeta) {
	var userID *string
	actorUsername := username
	if user != nil {
		userID = &user.ID
		if user.Username != "" {
			actorUsername = user.Username
		}
	}
	_ = s.audit.Audit(ctx, nil, auditlog.AuditInput{
		EventType: action, ActorKind: "user", ActorID: userID,
		ActorUsername: strPtr(actorUsername),
		ResourceType:  "user", ResourceID: userID, Action: action, Outcome: outcome,
		RequestID: strPtr(meta.RequestID), SourceIP: strPtr(meta.SourceIP),
		UserAgent: strPtr(meta.UserAgent),
	})
}

// IssuedSession holds the session material issued upon successful authentication.
type IssuedSession struct {
	SessionID string
	Token     string
	CSRFToken string
	ExpiresAt time.Time
}

// LoginLocal authenticates a local account. The built-in admin follows the 5-failure lockout state machine;
// standard accounts return a uniform error message on failure without leaking account existence.
func (s *Service) LoginLocal(ctx context.Context, username, password string, meta RequestMeta) (*IssuedSession, error) {
	if username == "" || password == "" {
		return nil, bizerr.ErrValidationFailed
	}
	// SEC-0058: Rate limiting must run before checking account existence, keyed by submitted username + client IP.
	// Rate-limiting only after a user row hit would turn HTTP 429 into an account-existence oracle
	// (existing accounts return 429 while non-existent accounts always return "invalid username or password");
	// checking the rate limit first makes existing and non-existent accounts behave identically.
	key := strings.ToLower(username) + "|" + meta.SourceIP
	if !s.limit.allow(key) {
		return nil, ErrRateLimited
	}
	var user UserModel
	err := s.db.WithContext(ctx).
		Where("lower(username) = lower(?)", username).First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		// Constant-time path: perform a dummy password verification before rejecting so account existence is indistinguishable.
		_, _, _ = VerifyPassword(password, "$argon2id$v=19$m=65536,t=3,p=2$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
		return nil, bizerr.ErrInvalidCredentials
	}
	if err != nil {
		return nil, err
	}
	if user.IsBuiltinAdmin {
		if err := s.adminLocalLogin(ctx, &user, password, meta); err != nil {
			return nil, err
		}
		s.limit.success(key)
	} else {
		ok, upgrade, err := VerifyPassword(password, derefString(user.PasswordHash))
		if err != nil || !ok {
			s.auditLogin(ctx, "user", &user.ID, user.Username, "login_local", "failed", meta)
			return nil, bizerr.ErrInvalidCredentials
		}
		s.limit.success(key)
		if err := s.upgradeHashIfNeeded(ctx, &user, password, upgrade); err != nil {
			return nil, err
		}
		s.auditLogin(ctx, "user", &user.ID, user.Username, "login_local", "succeeded", meta)
	}
	return s.issue(ctx, user.ID)
}

// adminLocalLogin handles built-in admin login: row locking serializes failure counting and lockout
// (admin_login_security consistency constraints are enforced by the database).
// Note: failure counts and lockout state must be committed, so business authentication failures are
// returned after the transaction commits rather than as closure errors that would trigger a rollback.
func (s *Service) adminLocalLogin(ctx context.Context, user *UserModel, password string, meta RequestMeta) error {
	var bizErr error
	txErr := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		bizErr = nil
		var sec AdminLoginSecurityModel
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("user_id = ?", user.ID).First(&sec).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			sec = AdminLoginSecurityModel{UserID: user.ID, FailedAttempts: 0, UpdatedAt: time.Now().UTC()}
			if err := tx.Create(&sec).Error; err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		if sec.FailedAttempts >= 5 {
			s.auditLoginTx(ctx, tx, "user", &user.ID, user.Username, "admin_login_locked", "denied", meta)
			bizErr = bizerr.AdminLockedError{}
			return nil
		}
		ok, upgrade, err := VerifyPassword(password, derefString(user.PasswordHash))
		if err != nil || !ok {
			sec.FailedAttempts++
			sec.UpdatedAt = time.Now().UTC()
			if sec.FailedAttempts >= 5 {
				now := time.Now().UTC()
				sec.LockedAt = &now
				s.auditLoginTx(ctx, tx, "user", &user.ID, user.Username, "admin_login_locked", "denied", meta)
			} else {
				s.auditLoginTx(ctx, tx, "user", &user.ID, user.Username, "admin_login_failed", "failed", meta)
			}
			if err := tx.Model(&AdminLoginSecurityModel{}).
				Where("user_id = ?", sec.UserID).
				Updates(map[string]any{
					"failed_attempts": sec.FailedAttempts,
					"locked_at":       sec.LockedAt,
					"updated_at":      sec.UpdatedAt,
				}).Error; err != nil {
				return err
			}
			bizErr = bizerr.ErrInvalidCredentials
			return nil
		}
		// Reset failure counter upon successful login.
		if err := tx.Model(&AdminLoginSecurityModel{}).
			Where("user_id = ?", user.ID).
			Updates(map[string]any{"failed_attempts": 0, "locked_at": nil, "updated_at": time.Now().UTC()}).Error; err != nil {
			return err
		}
		if err := s.upgradeHashIfNeeded(ctx, user, password, upgrade); err != nil {
			return err
		}
		s.auditLoginTx(ctx, tx, "user", &user.ID, user.Username, "admin_login", "succeeded", meta)
		return nil
	})
	if txErr != nil {
		return txErr
	}
	return bizErr
}

// upgradeHashIfNeeded transparently upgrades a migrated hash (Django PBKDF2) to Argon2id after a successful login.
func (s *Service) upgradeHashIfNeeded(ctx context.Context, user *UserModel, password string, needsUpgrade bool) error {
	if !needsUpgrade {
		return nil
	}
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	return s.db.WithContext(ctx).Model(&UserModel{}).
		Where("id = ?", user.ID).
		Update("password_hash", hash).Error
}

func (s *Service) issue(ctx context.Context, userID string) (*IssuedSession, error) {
	id, token, csrf, expiresAt, err := s.sessions.IssueSession(ctx, userID)
	if err != nil {
		return nil, err
	}
	return &IssuedSession{SessionID: id, Token: token, CSRFToken: csrf, ExpiresAt: expiresAt}, nil
}

// Register handles self-service registration: gated by general.registration_enabled;
// newly registered accounts are immediately active with zero business privileges. SEC-0061: rate-limits
// by source IP at entry and collapses reserved names, invalid parameters, and taken usernames into
// ErrRegistrationRejected so username existence is never exposed via 1005 or reserved-name messages.
func (s *Service) Register(ctx context.Context, username, displayName, email, password string, meta RequestMeta) (*UserModel, error) {
	// SEC-0061: Rate limiting runs before any business check (including reading registration_enabled or
	// checking username availability), isomorphic to LoginLocal SEC-0058 — rate-limiting only the
	// "already exists" branch would make HTTP 429 an existence oracle. Keyed by "reg:" + client IP
	// (pure IP dimension: once error messages are generalized, per-username keys add no anti-enumeration
	// value, while account creation / probing abuse is capped per source IP), isolated from login buckets
	// (<username>|<ip>, ext:*) with the same threshold (10/min). Registration has no clear-on-success
	// semantics: pure window counter (once registered, subsequent attempts for that username would fail anyway).
	if !s.limit.allow("reg:" + meta.SourceIP) {
		return nil, ErrRateLimited
	}
	enabled, err := s.RegistrationEnabled(ctx)
	if err != nil {
		return nil, err
	}
	if !enabled {
		return nil, ErrRegistrationDisabled
	}
	if strings.EqualFold(username, "admin") || len(password) < 12 || username == "" || displayName == "" {
		return nil, ErrRegistrationRejected
	}
	user, err := s.createUserRow(ctx, username, displayName, email, password)
	if err != nil {
		if errors.Is(err, bizerr.ErrAlreadyExists) {
			// SEC-0061: Taken usernames must not surface as 1005; collapse into the uniform rejection sentinel.
			return nil, ErrRegistrationRejected
		}
		return nil, err
	}
	_ = s.audit.Audit(ctx, nil, auditlog.AuditInput{
		EventType: "user.registered", ActorKind: "user", ActorID: &user.ID, ActorUsername: &user.Username,
		ResourceType: "user", ResourceID: &user.ID, Action: "register", Outcome: "succeeded",
		RequestID: strPtr(meta.RequestID), SourceIP: strPtr(meta.SourceIP), UserAgent: strPtr(meta.UserAgent),
	})
	return user, nil
}

// RegistrationEnabled reads the registration switch in the general namespace; defaults to false.
func (s *Service) RegistrationEnabled(ctx context.Context) (bool, error) {
	var row struct {
		Settings []byte `gorm:"column:settings"`
	}
	err := s.db.WithContext(ctx).Table("system_settings").
		Select("settings").Where("namespace = ?", "general").First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var parsed struct {
		RegistrationEnabled bool `json:"registration_enabled"`
	}
	if err := json.Unmarshal(row.Settings, &parsed); err != nil {
		return false, nil
	}
	return parsed.RegistrationEnabled, nil
}

// createUserRow creates a standard user (zero business privileges, immediately active).
func (s *Service) createUserRow(ctx context.Context, username, displayName, email, password string) (*UserModel, error) {
	return s.createUserRowWithRole(ctx, username, displayName, email, password, "user")
}

// createUserRowWithRole creates a user with the specified management role ('user' | 'admin').
func (s *Service) createUserRowWithRole(ctx context.Context, username, displayName, email, password, role string) (*UserModel, error) {
	hash, err := HashPassword(password)
	if err != nil {
		return nil, err
	}
	if role == "" {
		role = "user"
	}
	now := time.Now().UTC()
	var emailPtr *string
	if email != "" {
		emailPtr = &email
	}
	user := UserModel{
		ID: uuid.NewString(), Username: username, PasswordHash: &hash,
		DisplayName: displayName, Email: emailPtr, Role: role, Version: 1,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.db.WithContext(ctx).Create(&user).Error; err != nil {
		if isUniqueErr(err) {
			return nil, bizerr.ErrAlreadyExists
		}
		return nil, err
	}
	return &user, nil
}

// ActorRef describes the actor initiating a management operation.
type ActorRef struct {
	UserID   string
	Username string
}

// UpsertExternalUser persists an external identity login:
// already bound -> reuse user; unbound and username available -> create zero-privilege user and bind;
// username is admin or already taken locally -> reject.
func (s *Service) UpsertExternalUser(ctx context.Context, providerKind, providerKey, subject, username, displayName, email string, meta RequestMeta) (*UserModel, error) {
	if strings.EqualFold(username, "admin") || subject == "admin" {
		return nil, ErrExternalAdminForbidden
	}
	var created UserModel
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing ExternalIdentityModel
		err := tx.Where("provider_kind = ? and provider_key = ? and external_subject = ?",
			providerKind, providerKey, subject).First(&existing).Error
		if err == nil {
			return tx.Where("id = ?", existing.UserID).First(&created).Error
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		// Reject merging when the username is already taken by a local account or another external identity
		// (accounts are never auto-merged by display name or username alone).
		var taken UserModel
		takeErr := tx.Where("lower(username) = lower(?)", username).First(&taken).Error
		if takeErr == nil {
			return bizerr.ErrInvalidCredentials
		}
		if !errors.Is(takeErr, gorm.ErrRecordNotFound) {
			return takeErr
		}
		now := time.Now().UTC()
		var emailPtr *string
		if email != "" {
			emailPtr = &email
		}
		created = UserModel{
			ID: uuid.NewString(), Username: username, DisplayName: ifEmpty(displayName, username),
			Email: emailPtr, Role: "user", Version: 1, CreatedAt: now, UpdatedAt: now,
		}
		if err := tx.Create(&created).Error; err != nil {
			if isUniqueErr(err) {
				return bizerr.ErrInvalidCredentials
			}
			return err
		}
		binding := ExternalIdentityModel{
			ID: uuid.NewString(), UserID: created.ID, ProviderKind: providerKind,
			ProviderKey: providerKey, ExternalSubject: subject, CreatedAt: now,
		}
		if err := tx.Create(&binding).Error; err != nil {
			if isUniqueErr(err) {
				return bizerr.ErrInvalidCredentials
			}
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &created, nil
}

// ResetAdminPassword implements the CLI password reset: acquires row lock, validates password policy,
// zeroes failure counters, clears lockout, revokes all admin sessions, and writes an audit event.
// Does not require the previous password; safe to run concurrently while the service is online.
func (s *Service) ResetAdminPassword(ctx context.Context, newPassword string, meta RequestMeta) error {
	if len(newPassword) < 12 {
		return bizerr.ErrValidationFailed
	}
	hash, err := HashPassword(newPassword)
	if err != nil {
		return err
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var admin UserModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("is_builtin_admin = true and username = 'admin'").First(&admin).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return bizerr.ErrNotFound
			}
			return err
		}
		if err := tx.Model(&UserModel{}).Where("id = ?", admin.ID).
			Updates(map[string]any{"password_hash": hash, "updated_at": time.Now().UTC()}).Error; err != nil {
			return err
		}
		var sec AdminLoginSecurityModel
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("user_id = ?", admin.ID).First(&sec).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			sec = AdminLoginSecurityModel{UserID: admin.ID, UpdatedAt: time.Now().UTC()}
			if err := tx.Create(&sec).Error; err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		if err := tx.Model(&AdminLoginSecurityModel{}).
			Where("user_id = ?", admin.ID).
			Updates(map[string]any{"failed_attempts": 0, "locked_at": nil, "updated_at": time.Now().UTC()}).Error; err != nil {
			return err
		}
		if err := s.sessions.RevokeByUser(ctx, tx, admin.ID, "admin_password_reset"); err != nil {
			return err
		}
		return s.audit.Audit(ctx, tx, auditlog.AuditInput{
			EventType: "admin.password_reset_cli", ActorKind: "cli",
			ActorID: &admin.ID, ActorUsername: &admin.Username,
			ResourceType: "user", ResourceID: &admin.ID,
			Action: "reset_admin_password", Outcome: "succeeded",
			RequestID: strPtr(meta.RequestID),
		})
	})
}

// AuthProviderOf returns the authority managing the user's password: returns provider_kind when bound
// to an external identity source (picking the earliest binding for determinism when multiple exist),
// or "local" otherwise. CurrentUser.auth_provider is derived from this so the frontend knows whether to render the password change form.
func (s *Service) AuthProviderOf(ctx context.Context, userID string) (string, error) {
	var binding ExternalIdentityModel
	err := s.db.WithContext(ctx).
		Where("user_id = ?", userID).
		Order("created_at asc").First(&binding).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "local", nil
	}
	if err != nil {
		return "", err
	}
	return binding.ProviderKind, nil
}

// ChangeMyPassword handles self-service password changes (POST /users/me/password).
// Semantics: validate 12-char password policy -> reject external identity accounts -> verify old password
// under row lock -> write new Argon2id hash -> revoke all sessions of the user (including the current
// session so the client returns to the login page, matching CLI reset semantics) -> record audit event
// user.password_changed.
// The built-in admin may change their password here, but admin login lockout can only be cleared via CLI
// reset (auth PRD: locked admin cannot log in even with the correct password), so admin_login_security is not touched.
func (s *Service) ChangeMyPassword(ctx context.Context, userID, oldPassword, newPassword string, meta RequestMeta) error {
	if oldPassword == "" || len(newPassword) < 12 {
		return bizerr.ErrValidationFailed
	}
	newHash, err := HashPassword(newPassword)
	if err != nil {
		return err
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var user UserModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", userID).First(&user).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return bizerr.ErrNotFound
			}
			return err
		}
		var external int64
		if err := tx.Model(&ExternalIdentityModel{}).
			Where("user_id = ?", userID).Count(&external).Error; err != nil {
			return err
		}
		if external > 0 {
			return bizerr.ErrPasswordManagedByIdentitySource
		}
		if user.PasswordHash == nil {
			return bizerr.ErrInvalidCredentials
		}
		ok, _, err := VerifyPassword(oldPassword, *user.PasswordHash)
		if err != nil {
			// Malformed hash: treat as an internal error rather than surfacing it as an incorrect old password.
			return err
		}
		if !ok {
			return bizerr.ErrInvalidCredentials
		}
		if err := tx.Model(&UserModel{}).Where("id = ?", userID).
			Updates(map[string]any{"password_hash": newHash, "updated_at": time.Now().UTC()}).Error; err != nil {
			return err
		}
		if err := s.sessions.RevokeByUser(ctx, tx, userID, "password_changed"); err != nil {
			return err
		}
		return s.audit.Audit(ctx, tx, auditlog.AuditInput{
			EventType: "user.password_changed", ActorKind: "user",
			ActorID: &user.ID, ActorUsername: &user.Username,
			ResourceType: "user", ResourceID: &user.ID,
			Action: "change_password", Outcome: "succeeded",
			RequestID: strPtr(meta.RequestID),
		})
	})
}

// BootstrapAdmin bootstraps the sole built-in admin when the users table is empty (initial install).
// Returns ErrAlreadyExists without overwriting if any user row already exists.
func (s *Service) BootstrapAdmin(ctx context.Context, password string) error {
	if len(password) < 12 {
		return bizerr.ErrValidationFailed
	}
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Model(&UserModel{}).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return bizerr.ErrAlreadyExists
		}
		now := time.Now().UTC()
		admin := UserModel{
			ID: uuid.NewString(), Username: "admin", PasswordHash: &hash,
			DisplayName: "Administrator", IsBuiltinAdmin: true, Role: "admin", Version: 1,
			CreatedAt: now, UpdatedAt: now,
		}
		return tx.Create(&admin).Error
	})
}

// ProviderSecret decrypts a Provider Secret with AAD bound to table/PK/column/purpose.
// Returns a structured error without panicking when crypter is nil (service constructed for CLI reset).
func (s *Service) ProviderSecret(ctx context.Context, p *IdentityProviderModel) ([]byte, error) {
	if s.crypter == nil {
		return nil, fmt.Errorf("identity: crypter unavailable")
	}
	if p.SecretCiphertext == nil || p.SecretKeyID == nil || p.SecretAlgorithm == nil {
		return nil, fmt.Errorf("identity: provider secret not configured")
	}
	return s.crypter.Open(crypto.PurposePayload, crypto.Envelope{
		Ciphertext: p.SecretCiphertext,
		Nonce:      p.SecretNonce,
		KeyID:      *p.SecretKeyID,
		Algorithm:  *p.SecretAlgorithm,
	}, crypto.AAD{
		DeploymentID: s.crypter.DeploymentID(),
		Table:        "identity_providers",
		PrimaryKey:   p.ID,
		Column:       "secret",
		Purpose:      "provider_secret",
	})
}

func (s *Service) auditLogin(ctx context.Context, actorKind string, userID *string, username, action, outcome string, meta RequestMeta) {
	_ = s.audit.Audit(ctx, nil, s.loginAudit(actorKind, userID, username, action, outcome, meta))
}

func (s *Service) auditLoginTx(ctx context.Context, tx *gorm.DB, actorKind string, userID *string, username, action, outcome string, meta RequestMeta) {
	_ = s.audit.Audit(ctx, tx, s.loginAudit(actorKind, userID, username, action, outcome, meta))
}

func (s *Service) loginAudit(actorKind string, userID *string, username, action, outcome string, meta RequestMeta) auditlog.AuditInput {
	return auditlog.AuditInput{
		EventType: action, ActorKind: actorKind, ActorID: userID, ActorUsername: &username,
		ResourceType: "user", ResourceID: userID, Action: action, Outcome: outcome,
		RequestID: strPtr(meta.RequestID), SourceIP: strPtr(meta.SourceIP), UserAgent: strPtr(meta.UserAgent),
	}
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func ifEmpty(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

func isUniqueErr(err error) bool { return txutil.IsUniqueViolation(err) }

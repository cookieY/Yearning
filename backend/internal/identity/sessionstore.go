// sessionstore.go PostgreSQL-backed server-side Session Store (ADR-0004).
// Cookies carry only high-entropy opaque tokens; only token hashes are stored in the database, and raw tokens never enter the DB or logs.
package identity

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/identity/session"
	"github.com/google/uuid"
)

// ErrSessionInvalid indicates a session is missing, revoked, or expired; callers never distinguish the cause.
var ErrSessionInvalid = errors.New("identity: session invalid")

// SessionStore is the production Session store implementation.
type SessionStore struct {
	db  *gorm.DB
	ttl time.Duration
}

// NewSessionStore constructs a PG Session Store; ttl must be positive.
func NewSessionStore(db *gorm.DB, ttl time.Duration) (*SessionStore, error) {
	if db == nil {
		return nil, errors.New("identity: nil db")
	}
	if ttl <= 0 {
		return nil, errors.New("identity: session ttl must be positive")
	}
	return &SessionStore{db: db, ttl: ttl}, nil
}

// Get implements session.Store: looks up an active session by raw token.
// Deleted users (cascade-revoked) or revoked/expired sessions uniformly return ErrNotFound.
func (s *SessionStore) Get(ctx context.Context, rawToken string) (*session.Session, error) {
	if rawToken == "" {
		return nil, session.ErrNotFound
	}
	var row AuthSessionModel
	err := s.db.WithContext(ctx).
		Where("token_hash = ? and revoked_at is null and expires_at > now()", TokenHash(rawToken)).
		First(&row).Error
	if err != nil {
		return nil, session.ErrNotFound
	}
	var user UserModel
	if err := s.db.WithContext(ctx).Select("id", "username", "is_builtin_admin", "role").
		Where("id = ?", row.UserID).First(&user).Error; err != nil {
		return nil, session.ErrNotFound
	}
	role := user.Role
	if user.IsBuiltinAdmin {
		role = "admin"
	} else if role == "" {
		role = "user"
	}
	return &session.Session{
		ID:        row.ID,
		Identity:  &session.Identity{UserID: user.ID, Username: user.Username, IsBuiltinAdmin: user.IsBuiltinAdmin, Role: role},
		ExpiresAt: row.ExpiresAt,
	}, nil
}

// CSRFHashByToken returns the CSRF token hash for the session matching rawToken, used by CSRF middleware.
func (s *SessionStore) CSRFHashByToken(ctx context.Context, rawToken string) (string, error) {
	var row AuthSessionModel
	err := s.db.WithContext(ctx).
		Where("token_hash = ? and revoked_at is null and expires_at > now()", TokenHash(rawToken)).
		First(&row).Error
	if err != nil {
		return "", session.ErrNotFound
	}
	return row.CSRFHash, nil
}

// IssueSession issues a new session for a user, returning the session ID, raw session token, and raw CSRF token (raw tokens are returned only once here).
func (s *SessionStore) IssueSession(ctx context.Context, userID string) (id, token, csrf string, expiresAt time.Time, err error) {
	token, err = RandomToken()
	if err != nil {
		return "", "", "", time.Time{}, err
	}
	csrf, err = RandomToken()
	if err != nil {
		return "", "", "", time.Time{}, err
	}
	uid, err := uuid.NewV7()
	if err != nil {
		return "", "", "", time.Time{}, err
	}
	id = uid.String()
	now := time.Now().UTC()
	expiresAt = now.Add(s.ttl)
	row := AuthSessionModel{
		ID:         id,
		UserID:     userID,
		TokenHash:  TokenHash(token),
		CSRFHash:   TokenHash(csrf),
		CreatedAt:  now,
		LastSeenAt: now,
		ExpiresAt:  expiresAt,
	}
	if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
		return "", "", "", time.Time{}, err
	}
	return id, token, csrf, expiresAt, nil
}

// RevokeByToken revokes the session matching rawToken (logout).
func (s *SessionStore) RevokeByToken(ctx context.Context, rawToken, reason string) error {
	now := time.Now().UTC()
	return s.db.WithContext(ctx).
		Model(&AuthSessionModel{}).
		Where("token_hash = ? and revoked_at is null", TokenHash(rawToken)).
		Updates(map[string]any{"revoked_at": now, "revoke_reason": reason}).Error
}

// RevokeByUser revokes all active sessions for a user (user deletion, admin password reset).
func (s *SessionStore) RevokeByUser(ctx context.Context, tx *gorm.DB, userID, reason string) error {
	writer := s.db
	if tx != nil {
		writer = tx
	}
	now := time.Now().UTC()
	return writer.WithContext(ctx).
		Model(&AuthSessionModel{}).
		Where("user_id = ? and revoked_at is null", userID).
		Updates(map[string]any{"revoked_at": now, "revoke_reason": reason}).Error
}

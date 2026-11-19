// models.go GORM models for the identity domain (gorm-model-registry: identity/repository/postgres).
// Explicit table and column names, no gorm.Model embedding, no json tags, and pointer types for nullable columns.
package identity

import "time"

// UserModel maps to the users table.
type UserModel struct {
	ID             string    `gorm:"column:id;primaryKey"`
	Username       string    `gorm:"column:username"`
	PasswordHash   *string   `gorm:"column:password_hash"`
	DisplayName    string    `gorm:"column:display_name"`
	Email          *string   `gorm:"column:email"`
	IsBuiltinAdmin bool      `gorm:"column:is_builtin_admin"`
	Role           string    `gorm:"column:role"`
	Version        int64     `gorm:"column:version"`
	CreatedAt      time.Time `gorm:"column:created_at"`
	UpdatedAt      time.Time `gorm:"column:updated_at"`
}

func (UserModel) TableName() string { return "users" }

// IdentityProviderModel maps to the identity_providers table.
type IdentityProviderModel struct {
	ID                string    `gorm:"column:id;primaryKey"`
	ProviderKey       string    `gorm:"column:provider_key"`
	ProviderKind      string    `gorm:"column:provider_kind"`
	DisplayName       string    `gorm:"column:display_name"`
	Enabled           bool      `gorm:"column:enabled"`
	Configuration     []byte    `gorm:"column:configuration"`
	ConfigurationHash string    `gorm:"column:configuration_hash"`
	SecretCiphertext  []byte    `gorm:"column:secret_ciphertext"`
	SecretNonce       []byte    `gorm:"column:secret_nonce"`
	SecretKeyID       *string   `gorm:"column:secret_key_id"`
	SecretAlgorithm   *string   `gorm:"column:secret_algorithm"`
	Version           int64     `gorm:"column:version"`
	CreatedByUserID   string    `gorm:"column:created_by_user_id"`
	CreatedAt         time.Time `gorm:"column:created_at"`
	UpdatedAt         time.Time `gorm:"column:updated_at"`
}

func (IdentityProviderModel) TableName() string { return "identity_providers" }

// ExternalIdentityModel maps to the external_identities table.
type ExternalIdentityModel struct {
	ID              string    `gorm:"column:id;primaryKey"`
	UserID          string    `gorm:"column:user_id"`
	ProviderKind    string    `gorm:"column:provider_kind"`
	ProviderKey     string    `gorm:"column:provider_key"`
	ExternalSubject string    `gorm:"column:external_subject"`
	CreatedAt       time.Time `gorm:"column:created_at"`
}

func (ExternalIdentityModel) TableName() string { return "external_identities" }

// AuthSessionModel maps to the auth_sessions table.
type AuthSessionModel struct {
	ID           string     `gorm:"column:id;primaryKey"`
	UserID       string     `gorm:"column:user_id"`
	TokenHash    string     `gorm:"column:token_hash"`
	CSRFHash     string     `gorm:"column:csrf_hash"`
	CreatedAt    time.Time  `gorm:"column:created_at"`
	LastSeenAt   time.Time  `gorm:"column:last_seen_at"`
	ExpiresAt    time.Time  `gorm:"column:expires_at"`
	RevokedAt    *time.Time `gorm:"column:revoked_at"`
	RevokeReason *string    `gorm:"column:revoke_reason"`
}

func (AuthSessionModel) TableName() string { return "auth_sessions" }

// AdminLoginSecurityModel maps to the admin_login_security table.
type AdminLoginSecurityModel struct {
	UserID         string     `gorm:"column:user_id;primaryKey"`
	FailedAttempts int16      `gorm:"column:failed_attempts"`
	LockedAt       *time.Time `gorm:"column:locked_at"`
	UpdatedAt      time.Time  `gorm:"column:updated_at"`
}

func (AdminLoginSecurityModel) TableName() string { return "admin_login_security" }

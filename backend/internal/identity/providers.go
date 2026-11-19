// providers.go IdentityProvider management: global singleton LDAP, multi-instance OIDC,
// immutable Provider Key, write-only Secret, and deletion blocked (disable allowed) when external identity bindings exist.
package identity

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	"github.com/cookieY/YearningX/backend/internal/authorization/bizerr"
	"github.com/cookieY/YearningX/backend/internal/crypto"
)

// ProviderView is the API response view for a Provider; exposes only secret_configured and never the Secret itself.
type ProviderView struct {
	ID               string          `json:"id"`
	ProviderKey      string          `json:"provider_key"`
	ProviderKind     string          `json:"provider_kind"`
	DisplayName      string          `json:"display_name"`
	Enabled          bool            `json:"enabled"`
	Configuration    json.RawMessage `json:"configuration"`
	SecretConfigured bool            `json:"secret_configured"`
	Version          int64           `json:"version"`
	CreatedAt        time.Time       `json:"created_at"`
	UpdatedAt        time.Time       `json:"updated_at"`
}

// ProviderViewOf converts a provider model into its response view.
func ProviderViewOf(p *IdentityProviderModel) ProviderView {
	return ProviderView{
		ID: p.ID, ProviderKey: p.ProviderKey, ProviderKind: p.ProviderKind,
		DisplayName: p.DisplayName, Enabled: p.Enabled,
		Configuration:    json.RawMessage(p.Configuration),
		SecretConfigured: p.SecretCiphertext != nil,
		Version:          p.Version, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
	}
}

// validateProviderConfig validates provider configuration according to its kind.
func validateProviderConfig(kind string, config json.RawMessage) error {
	switch kind {
	case "ldap":
		var cfg LDAPConfiguration
		if err := json.Unmarshal(config, &cfg); err != nil {
			return bizerr.ErrValidationFailed
		}
		if err := cfg.Validate(); err != nil {
			return bizerr.ErrValidationFailed
		}
	case "oidc":
		var cfg OIDCConfiguration
		if err := json.Unmarshal(config, &cfg); err != nil {
			return bizerr.ErrValidationFailed
		}
		if err := cfg.Validate(); err != nil {
			return bizerr.ErrValidationFailed
		}
	default:
		return bizerr.ErrValidationFailed
	}
	return nil
}

// CreateProvider creates a Provider: LDAP is a global singleton; OIDC requires client_secret;
// Secret is immediately envelope-encrypted with AAD bound to the row.
func (s *Service) CreateProvider(ctx context.Context, actor ActorRef, providerKey, kind, displayName string, enabled bool, config json.RawMessage, secret []byte, meta RequestMeta) (*ProviderView, error) {
	if kind != "ldap" && kind != "oidc" {
		return nil, bizerr.ErrValidationFailed
	}
	if providerKey == "" || displayName == "" {
		return nil, bizerr.ErrValidationFailed
	}
	if err := validateProviderConfig(kind, config); err != nil {
		return nil, err
	}
	if kind == "oidc" && len(secret) == 0 {
		return nil, bizerr.ErrValidationFailed
	}
	if len(secret) > 0 && s.crypter == nil {
		return nil, fmt.Errorf("identity: crypter unavailable")
	}
	now := time.Now().UTC()
	row := IdentityProviderModel{
		ID:                newUserID(),
		ProviderKey:       providerKey,
		ProviderKind:      kind,
		DisplayName:       displayName,
		Enabled:           enabled,
		Configuration:     config,
		ConfigurationHash: configHash(config),
		Version:           1,
		CreatedByUserID:   actor.UserID,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	if len(secret) > 0 {
		env, err := s.crypter.Seal(crypto.PurposePayload, secret, crypto.AAD{
			DeploymentID: s.crypter.DeploymentID(),
			Table:        "identity_providers", PrimaryKey: row.ID,
			Column: "secret", Purpose: "provider_secret",
		})
		if err != nil {
			return nil, err
		}
		row.SecretCiphertext, row.SecretNonce = env.Ciphertext, env.Nonce
		row.SecretKeyID, row.SecretAlgorithm = strPtr(env.KeyID), strPtr(env.Algorithm)
	}
	err := s.db.WithContext(ctx).Create(&row).Error
	if err != nil {
		if isUniqueErr(err) {
			// Unique provider_key conflict or singleton LDAP index conflict.
			return nil, bizerr.ErrAlreadyExists
		}
		return nil, err
	}
	view := ProviderViewOf(&row)
	_ = s.audit.Audit(ctx, nil, auditlog.AuditInput{
		EventType: "identity_provider.created", ActorKind: "user",
		ActorID: &actor.UserID, ActorUsername: &actor.Username,
		ResourceType: "identity_provider", ResourceID: &row.ID,
		Action: "create_identity_provider", Outcome: "succeeded",
		RequestID: strPtr(meta.RequestID),
		Metadata:  map[string]any{"provider_key": providerKey, "provider_kind": kind},
	})
	return &view, nil
}

// ReplaceProvider updates a Provider: provider_key is immutable; Secret is preserved when omitted.
func (s *Service) ReplaceProvider(ctx context.Context, actor ActorRef, providerID string, expectVersion int64, displayName string, enabled bool, config json.RawMessage, secret []byte, meta RequestMeta) (*ProviderView, error) {
	var row IdentityProviderModel
	if err := s.db.WithContext(ctx).Where("id = ?", providerID).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, bizerr.ErrNotFound
		}
		return nil, err
	}
	if row.Version != expectVersion {
		return nil, bizerr.ErrVersionConflict
	}
	if displayName == "" {
		return nil, bizerr.ErrValidationFailed
	}
	if err := validateProviderConfig(row.ProviderKind, config); err != nil {
		return nil, err
	}
	updates := map[string]any{
		"display_name": displayName, "enabled": enabled,
		"configuration": config, "configuration_hash": configHash(config),
		"version": gorm.Expr("version + 1"), "updated_at": time.Now().UTC(),
	}
	if len(secret) > 0 {
		if s.crypter == nil {
			return nil, fmt.Errorf("identity: crypter unavailable")
		}
		env, err := s.crypter.Seal(crypto.PurposePayload, secret, crypto.AAD{
			DeploymentID: s.crypter.DeploymentID(),
			Table:        "identity_providers", PrimaryKey: row.ID,
			Column: "secret", Purpose: "provider_secret",
		})
		if err != nil {
			return nil, err
		}
		updates["secret_ciphertext"] = env.Ciphertext
		updates["secret_nonce"] = env.Nonce
		updates["secret_key_id"] = env.KeyID
		updates["secret_algorithm"] = env.Algorithm
	}
	res := s.db.WithContext(ctx).Model(&IdentityProviderModel{}).
		Where("id = ? and version = ?", providerID, expectVersion).Updates(updates)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		if s.providerExists(ctx, providerID) {
			return nil, bizerr.ErrVersionConflict
		}
		return nil, bizerr.ErrNotFound
	}
	if err := s.db.WithContext(ctx).Where("id = ?", providerID).First(&row).Error; err != nil {
		return nil, err
	}
	view := ProviderViewOf(&row)
	_ = s.audit.Audit(ctx, nil, auditlog.AuditInput{
		EventType: "identity_provider.updated", ActorKind: "user",
		ActorID: &actor.UserID, ActorUsername: &actor.Username,
		ResourceType: "identity_provider", ResourceID: &providerID,
		Action: "replace_identity_provider", Outcome: "succeeded",
		RequestID: strPtr(meta.RequestID),
	})
	return &view, nil
}

// DeleteProvider deletes a Provider: forbidden when ExternalIdentity bindings exist (1106/1006).
func (s *Service) DeleteProvider(ctx context.Context, actor ActorRef, providerID string, expectVersion int64, meta RequestMeta) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row IdentityProviderModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", providerID).First(&row).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return bizerr.ErrNotFound
			}
			return err
		}
		if row.Version != expectVersion {
			return bizerr.ErrConcurrentModification
		}
		var bindings int64
		if err := tx.Model(&ExternalIdentityModel{}).
			Where("provider_key = ?", row.ProviderKey).Count(&bindings).Error; err != nil {
			return err
		}
		if bindings > 0 {
			return bizerr.ErrReferenced
		}
		if err := tx.Where("id = ?", providerID).Delete(&IdentityProviderModel{}).Error; err != nil {
			return err
		}
		return s.audit.Audit(ctx, tx, auditlog.AuditInput{
			EventType: "identity_provider.deleted", ActorKind: "user",
			ActorID: &actor.UserID, ActorUsername: &actor.Username,
			ResourceType: "identity_provider", ResourceID: &providerID,
			Action: "delete_identity_provider", Outcome: "succeeded",
			RequestID: strPtr(meta.RequestID),
		})
	})
}

// ListProviders / GetProvider read provider views.
func (s *Service) ListProviders(ctx context.Context, limit int, after string) ([]ProviderView, *string, error) {
	if limit < 1 || limit > 200 {
		return nil, nil, bizerr.ErrValidationFailed
	}
	q := s.db.WithContext(ctx).Model(&IdentityProviderModel{}).
		Order("created_at asc, id asc").Limit(limit + 1)
	if after != "" {
		var cursor IdentityProviderModel
		if err := s.db.WithContext(ctx).Select("created_at", "id").
			Where("id = ?", after).First(&cursor).Error; err != nil {
			return nil, nil, bizerr.ErrNotFound
		}
		q = q.Where("(created_at, id) > (?, ?)", cursor.CreatedAt, cursor.ID)
	}
	var rows []IdentityProviderModel
	if err := q.Find(&rows).Error; err != nil {
		return nil, nil, err
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	views := make([]ProviderView, 0, len(rows))
	for i := range rows {
		views = append(views, ProviderViewOf(&rows[i]))
	}
	var next *string
	if hasMore && len(rows) > 0 {
		next = &rows[len(rows)-1].ID
	}
	return views, next, nil
}

// GetProvider returns a single Provider view.
func (s *Service) GetProvider(ctx context.Context, providerID string) (*ProviderView, error) {
	var row IdentityProviderModel
	if err := s.db.WithContext(ctx).Where("id = ?", providerID).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, bizerr.ErrNotFound
		}
		return nil, err
	}
	view := ProviderViewOf(&row)
	return &view, nil
}

// LDAPProvider returns the enabled LDAP Provider (global singleton).
func (s *Service) LDAPProvider(ctx context.Context) (*IdentityProviderModel, *LDAPConfiguration, []byte, error) {
	var row IdentityProviderModel
	err := s.db.WithContext(ctx).
		Where("provider_kind = 'ldap' and enabled = true").First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil, nil, bizerr.ErrNotFound
	}
	if err != nil {
		return nil, nil, nil, err
	}
	var cfg LDAPConfiguration
	if err := json.Unmarshal(row.Configuration, &cfg); err != nil {
		return nil, nil, nil, fmt.Errorf("identity: ldap configuration malformed")
	}
	secret, err := s.ProviderSecret(ctx, &row)
	if err != nil {
		return nil, nil, nil, err
	}
	return &row, &cfg, secret, nil
}

// OIDCProviderByKey returns the enabled OIDC Provider matching key.
func (s *Service) OIDCProviderByKey(ctx context.Context, providerKey string) (*IdentityProviderModel, *OIDCConfiguration, []byte, error) {
	var row IdentityProviderModel
	err := s.db.WithContext(ctx).
		Where("provider_kind = 'oidc' and provider_key = ? and enabled = true", providerKey).
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil, nil, bizerr.ErrNotFound
	}
	if err != nil {
		return nil, nil, nil, err
	}
	var cfg OIDCConfiguration
	if err := json.Unmarshal(row.Configuration, &cfg); err != nil {
		return nil, nil, nil, fmt.Errorf("identity: oidc configuration malformed")
	}
	secret, err := s.ProviderSecret(ctx, &row)
	if err != nil {
		return nil, nil, nil, err
	}
	return &row, &cfg, secret, nil
}

// OIDCProviderByID loads an OIDC Provider by ID (including decrypted Secret, regardless of enabled),
// used for connectivity testing — symmetric with LDAPProviderByID so administrators can verify
// connectivity on a disabled Provider before enabling it (F12 #013: previously loaded by key+enabled,
// causing disabled providers to return ErrNotFound which the handler mapped to 500).
func (s *Service) OIDCProviderByID(ctx context.Context, providerID string) (*IdentityProviderModel, *OIDCConfiguration, []byte, error) {
	var row IdentityProviderModel
	if err := s.db.WithContext(ctx).
		Where("id = ? and provider_kind = 'oidc'", providerID).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, nil, bizerr.ErrNotFound
		}
		return nil, nil, nil, err
	}
	var cfg OIDCConfiguration
	if err := json.Unmarshal(row.Configuration, &cfg); err != nil {
		return nil, nil, nil, fmt.Errorf("identity: oidc configuration malformed")
	}
	secret, err := s.ProviderSecret(ctx, &row)
	if err != nil {
		return nil, nil, nil, err
	}
	return &row, &cfg, secret, nil
}

// ListEnabledOIDC returns all enabled OIDC Providers (displayed on the login page).
func (s *Service) ListEnabledOIDC(ctx context.Context) ([]ProviderView, error) {
	var rows []IdentityProviderModel
	if err := s.db.WithContext(ctx).
		Where("provider_kind = 'oidc' and enabled = true").
		Order("created_at asc").Find(&rows).Error; err != nil {
		return nil, err
	}
	views := make([]ProviderView, 0, len(rows))
	for i := range rows {
		views = append(views, ProviderViewOf(&rows[i]))
	}
	return views, nil
}

// LDAPEnabled reports whether an enabled LDAP Provider exists (displayed on the login page).
func (s *Service) LDAPEnabled(ctx context.Context) bool {
	var count int64
	_ = s.db.WithContext(ctx).Model(&IdentityProviderModel{}).
		Where("provider_kind = 'ldap' and enabled = true").Count(&count).Error
	return count > 0
}

func (s *Service) providerExists(ctx context.Context, id string) bool {
	var count int64
	_ = s.db.WithContext(ctx).Model(&IdentityProviderModel{}).Where("id = ?", id).Count(&count).Error
	return count > 0
}

func configHash(config json.RawMessage) string {
	sum := sha256.Sum256(config)
	return fmt.Sprintf("%x", sum)
}

// LDAPProviderByID loads an LDAP Provider by ID (including decrypted Secret) for connectivity testing.
func (s *Service) LDAPProviderByID(ctx context.Context, providerID string) (*IdentityProviderModel, *LDAPConfiguration, []byte, error) {
	var row IdentityProviderModel
	if err := s.db.WithContext(ctx).
		Where("id = ? and provider_kind = 'ldap'", providerID).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, nil, bizerr.ErrNotFound
		}
		return nil, nil, nil, err
	}
	var cfg LDAPConfiguration
	if err := json.Unmarshal(row.Configuration, &cfg); err != nil {
		return nil, nil, nil, fmt.Errorf("identity: ldap configuration malformed")
	}
	secret, err := s.ProviderSecret(ctx, &row)
	if err != nil {
		return nil, nil, nil, err
	}
	return &row, &cfg, secret, nil
}

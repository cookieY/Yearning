// providerstore.go AI Provider configuration entity: CRUD, AEAD secret envelope (plaintext in memory only),
// selection chain assembly, and unsubmitted Run invalidation on configuration changes (PRD 9.2).
package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/platform/pagecursor"
	"github.com/cookieY/YearningX/backend/internal/platform/txutil"
	"github.com/cookieY/YearningX/backend/internal/review/domain"
	"github.com/cookieY/YearningX/backend/internal/review/promptprofile"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
)

// ErrProviderInUse indicates active Runs reference the Provider at deletion time (fail closed).
var ErrProviderInUse = errors.New("review: provider referenced by runs")

// providerUniqueConflictDetail F-UX-002: distinguishes the conflict axis by violated constraint name —
// the selection_priority unique axis includes the numeric value, while the name unique axis names "name".
// The platform DB is strictly PG (the MySQL driver is used only for target business DBs, where ai_providers
// does not reside), and known constraint names follow the <table_name>_<column_name>_key pattern generated
// by inline UNIQUE in 000001_platform.up.sql; exact match takes precedence with substring fallback against
// future naming drift. When the constraint name is unavailable (driver differences / future wrapper layers),
// it degrades to listing both axes rather than returning a generic "resource with same identity already exists".
// Note: never enable gorm TranslateError on the platform DB handle — doing so would translate 23505 into
// gorm.ErrDuplicatedKey, breaking both the section 18.75-B 1005 mapping and this Detail distinction back into a 500.
func providerUniqueConflictDetail(err error, priority int) string {
	constraint := txutil.UniqueViolationConstraint(err)
	switch {
	case constraint == "ai_providers_selection_priority_key", strings.Contains(constraint, "selection_priority"):
		// Wording starts with "AI Provider": security_audit's SQL Sprintf rule matches literal prefixes
		// (select|insert|update|delete|with) + format verbs, so a "selection..." prefix would trigger a false-positive SQL concatenation alert (CI red lesson).
		return fmt.Sprintf("AI Provider selection_priority=%d is already used by another AI Provider", priority)
	case constraint == "ai_providers_name_key", strings.Contains(constraint, "name"):
		return "name is already used by another AI Provider"
	default:
		return "name or selection_priority is already used by another AI Provider"
	}
}

// AIProviderView is the admin read view (excludes secrets; api_key_configured indicates whether a key is configured).
// S008: Configuration strongly types the three generation parameters on read-back (unconfigured keys are
// omitted without fabricating vendor defaults) along with the system-managed key structured_output_mode
// (persisted probe negotiation value, read-only annotation — rejected on user write surfaces).
type AIProviderView struct {
	ID                string                       `json:"id"`
	Name              string                       `json:"name"`
	ProviderKind      string                       `json:"provider_kind"`
	BaseURL           string                       `json:"base_url"`
	ModelName         string                       `json:"model_name"`
	Enabled           bool                         `json:"enabled"`
	SelectionPriority int                          `json:"selection_priority"`
	APIKeyConfigured  bool                         `json:"api_key_configured"`
	Configuration     *AIProviderConfigurationView `json:"configuration,omitempty"`
	Version           int64                        `json:"version"`
	CreatedAt         time.Time                    `json:"created_at"`
	UpdatedAt         time.Time                    `json:"updated_at"`
}

// AIProviderConfigurationView is the strongly-typed configuration in the admin read view:
// three generation parameters (nil key = unconfigured = omitted in requests) + system-negotiated output mode (empty = unnegotiated).
type AIProviderConfigurationView struct {
	ReasoningEffort      *string  `json:"reasoning_effort,omitempty"`
	MaxTokens            *int64   `json:"max_tokens,omitempty"`
	ContextWindow        *int64   `json:"context_window,omitempty"`
	Temperature          *float64 `json:"temperature,omitempty"`
	StructuredOutputMode string   `json:"structured_output_mode,omitempty"`
	SystemPromptProfile  *string  `json:"system_prompt_profile,omitempty"`
}

// configurationView folds the row's configuration jsonb into the strongly-typed read view (stored values
// are backfilled verbatim: persisted max_tokens/temperature are returned in full without dropping temperature=0).
func configurationView(raw []byte) *AIProviderConfigurationView {
	cfg := decodeConfiguration(raw)
	if cfg == nil {
		return nil
	}
	out := &AIProviderConfigurationView{}
	hasUserKey := false
	if v, ok := cfg["reasoning_effort"]; ok {
		if s, ok := v.(string); ok && s != "" {
			out.ReasoningEffort = &s
			hasUserKey = true
		}
	}
	if v, ok := cfg["max_tokens"]; ok {
		if f, ok := v.(float64); ok {
			n := int64(f)
			out.MaxTokens = &n
			hasUserKey = true
		}
	}
	if cw := provider.ConfigPositiveInt(cfg, "context_window"); cw > 0 {
		n := int64(cw)
		out.ContextWindow = &n
		hasUserKey = true
	}
	if v, ok := cfg["temperature"]; ok {
		if f, ok := v.(float64); ok {
			out.Temperature = &f
			hasUserKey = true
		}
	}
	if v, ok := cfg[provider.ConfigKeyOutputMode]; ok {
		if s, ok := v.(string); ok {
			out.StructuredOutputMode = s
		}
	}
	if v, ok := cfg["system_prompt_profile"]; ok {
		if s, ok := v.(string); ok && (s == promptprofile.ProfileDefault || s == promptprofile.ProfileSAN) {
			cp := s
			out.SystemPromptProfile = &cp
			hasUserKey = true
		}
	}
	if !hasUserKey && out.StructuredOutputMode == "" {
		return nil
	}
	return out
}

func sanitizeProviderConfiguration(cfg map[string]any) map[string]any {
	if cfg == nil {
		return nil
	}
	out := make(map[string]any, len(cfg))
	for k, v := range cfg {
		if v == nil {
			continue
		}
		out[k] = v
	}
	return out
}

// aiProviderRow maps directly to a database row (avoiding the GORM model registration surface).
type aiProviderRow struct {
	ID                string
	Name              string
	ProviderKind      string
	BaseURL           string
	ModelName         string
	Enabled           bool
	SelectionPriority int
	Configuration     []byte
	SecretCiphertext  []byte
	SecretNonce       []byte
	SecretKeyID       string
	Version           int64
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// CreateAIProviderInput holds creation parameters. Privacy and output contract hashes are not supplied
// by the caller: the system derives them from platform constants (provider.PrivacyContractHash /
// provider.OutputSchemaHash) and persists them as configuration-time audit records.
type CreateAIProviderInput struct {
	Name              string
	ProviderKind      string
	BaseURL           string
	ModelName         string
	Enabled           bool
	SelectionPriority int
	APIKey            string
	Configuration     map[string]any
}

// CreateAIProvider creates a Provider (persisting the secret envelope). Configuration is validated via
// Endpoint.Validate (https + 64-char hex hashes); changing enabled configuration invalidates unsubmitted Reviews (M4).
func (s *Services) CreateAIProvider(ctx context.Context, actorUserID string, in CreateAIProviderInput) (*AIProviderView, error) {
	if in.Name == "" || in.ProviderKind == "" || in.BaseURL == "" || in.ModelName == "" || in.APIKey == "" {
		return nil, &domain.Error{Code: 1001, Name: "VALIDATION_FAILED"}
	}
	if err := ValidateProviderConfiguration(in.Configuration); err != nil {
		return nil, err
	}
	cfgMap := sanitizeProviderConfiguration(in.Configuration)
	effProf, _, profErr := promptprofile.ResolveFromConfiguration(in.ModelName, cfgMap)
	if profErr != nil {
		return nil, &domain.Error{
			Code: 1001, Name: "VALIDATION_FAILED",
			Fields: map[string][]string{"system_prompt_profile": {profErr.Error()}},
		}
	}
	privHash := provider.PrivacyContractHashForProfile(effProf)
	if err := validateBaseURL(in.BaseURL); err != nil {
		return nil, err
	}
	if err := (provider.Endpoint{
		ProviderID: "preview", Name: in.Name, ProviderKind: in.ProviderKind,
		BaseURL: in.BaseURL, ModelName: in.ModelName, SelectionPriority: in.SelectionPriority,
		PrivacyContractHash: privHash, OutputSchemaHash: provider.OutputSchemaHash,
		APIKey: in.APIKey, Configuration: cfgMap,
	}).Validate(); err != nil {
		return nil, &domain.Error{Code: 1001, Name: "VALIDATION_FAILED", Detail: err.Error()}
	}
	var negotiatedMode string
	if in.Enabled {
		// Pre-enable health gate: real output Schema probe + output mode negotiation (PRD 9.2
		// chain admission; executed before persistence so failures never write to the DB).
		mode, err := s.probeCandidate(ctx, provider.Endpoint{
			ProviderID: "preview", Name: in.Name, ProviderKind: in.ProviderKind,
			BaseURL: in.BaseURL, ModelName: in.ModelName, SelectionPriority: in.SelectionPriority,
			PrivacyContractHash: privHash, OutputSchemaHash: provider.OutputSchemaHash,
			APIKey: in.APIKey, Configuration: cfgMap,
		})
		if err != nil {
			return nil, err
		}
		negotiatedMode = mode
	}
	id := newID()
	env, err := s.Crypter.SealColumn(crypto.PurposeProviderSecret, crypto.ColumnRef{
		Table:      "ai_providers",
		PrimaryKey: id,
		Column:     "secret_ciphertext",
	}, []byte(in.APIKey))
	if err != nil {
		return nil, err
	}
	if negotiatedMode != "" {
		// section 18.76-A: the probe-negotiated mode is persisted alongside configuration as a system-managed key.
		cfgMap = withOutputMode(cfgMap, negotiatedMode)
	}
	cfg := []byte("{}")
	if cfgMap != nil {
		cfg, err = json.Marshal(cfgMap)
		if err != nil {
			return nil, err
		}
	}
	now := nowUTC()
	res := s.DB.WithContext(ctx).Exec(`
		insert into ai_providers (id, name, provider_kind, base_url, model_name, enabled,
			selection_priority, privacy_contract_hash, output_schema_hash, configuration,
			secret_ciphertext, secret_nonce, secret_key_id, secret_algorithm,
			version, created_by_user_id, created_at, updated_at)
		values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'aes-256-gcm', 1, ?, ?, ?)`,
		id, in.Name, in.ProviderKind, in.BaseURL, in.ModelName, in.Enabled,
		in.SelectionPriority, privHash, provider.OutputSchemaHash, cfg,
		env.Ciphertext, env.Nonce, env.KeyID, actorUserID, now, now)
	// section 18.75-B: both name and selection_priority have unique indexes — constraint violations map to
	// the declared RESOURCE_ALREADY_EXISTS (1005) instead of leaking as a silent 500. F-UX-002: the two
	// unique axes previously shared an undifferentiated 1005 where the UI only saw "resource with same
	// identity already exists" and operators could not tell which field collided — Detail now distinguishes
	// by constraint name and surfaces via meta.detail.
	if res.Error != nil {
		if txutil.IsUniqueViolation(res.Error) {
			return nil, &domain.Error{Code: 1005, Name: "RESOURCE_ALREADY_EXISTS",
				Detail: providerUniqueConflictDetail(res.Error, in.SelectionPriority)}
		}
		return nil, res.Error
	}
	if res.RowsAffected != 1 {
		return nil, ErrConcurrent
	}
	if in.Enabled {
		// M4: adding an enabled Provider alters the effective chain -> invalidate unsubmitted Reviews.
		if err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			return invalidateAllUnsubmittedRuns(ctx, tx)
		}); err != nil {
			return nil, err
		}
	}
	return &AIProviderView{ID: id, Name: in.Name, ProviderKind: in.ProviderKind,
		BaseURL: in.BaseURL, ModelName: in.ModelName, Enabled: in.Enabled,
		SelectionPriority: in.SelectionPriority, APIKeyConfigured: true,
		Configuration: configurationView(cfg),
		Version:       1, CreatedAt: now, UpdatedAt: now}, nil
}

// ListAIProviders paginates Providers (limit+1 prefetch; search = name/BaseURL/model substring, section 18.73).
func (s *Services) ListAIProviders(ctx context.Context, limit int, after, search string) ([]*AIProviderView, *string, error) {
	q := s.DB.WithContext(ctx).Table("ai_providers")
	if after != "" {
		created, id, err := pagecursor.DecodeUUID(after)
		if err != nil {
			return nil, nil, ErrValidation
		}
		q = q.Where("(created_at, id) < (?, ?)", created, id)
	}
	if search != "" {
		needle := "%" + search + "%"
		q = q.Where("name ILIKE ? or base_url ILIKE ? or model_name ILIKE ?", needle, needle, needle)
	}
	var rows []aiProviderRow
	if err := q.Select("id, name, provider_kind, base_url, model_name, enabled, selection_priority, configuration, secret_ciphertext, secret_nonce, secret_key_id, version, created_at, updated_at").
		Order("created_at desc").Order("id desc").Limit(limit + 1).Scan(&rows).Error; err != nil {
		return nil, nil, err
	}
	var next *string
	if len(rows) > limit {
		rows = rows[:limit]
		v := pagecursor.Encode(rows[len(rows)-1].CreatedAt, rows[len(rows)-1].ID)
		next = &v
	}
	out := make([]*AIProviderView, 0, len(rows))
	for i := range rows {
		out = append(out, &AIProviderView{
			ID: rows[i].ID, Name: rows[i].Name, ProviderKind: rows[i].ProviderKind,
			BaseURL: rows[i].BaseURL, ModelName: rows[i].ModelName, Enabled: rows[i].Enabled,
			SelectionPriority: rows[i].SelectionPriority,
			APIKeyConfigured:  len(rows[i].SecretCiphertext) > 0,
			Configuration:     configurationView(rows[i].Configuration),
			Version:           rows[i].Version, CreatedAt: rows[i].CreatedAt, UpdatedAt: rows[i].UpdatedAt,
		})
	}
	return out, next, nil
}

// GetAIProvider fetches a Provider by ID.
func (s *Services) GetAIProvider(ctx context.Context, id string) (*AIProviderView, error) {
	var row aiProviderRow
	err := s.DB.WithContext(ctx).Table("ai_providers").
		Select("id, name, provider_kind, base_url, model_name, enabled, selection_priority, configuration, secret_ciphertext, secret_nonce, secret_key_id, version, created_at, updated_at").
		Where("id = ?", id).Scan(&row).Error
	if err != nil {
		return nil, err
	}
	if row.ID == "" {
		return nil, ErrNotFound
	}
	return &AIProviderView{ID: row.ID, Name: row.Name, ProviderKind: row.ProviderKind,
		BaseURL: row.BaseURL, ModelName: row.ModelName, Enabled: row.Enabled,
		SelectionPriority: row.SelectionPriority, APIKeyConfigured: len(row.SecretCiphertext) > 0,
		Configuration: configurationView(row.Configuration),
		Version:       row.Version, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}, nil
}

// ReplaceAIProviderInput holds replacement parameters (empty APIKey = retain existing secret). Contract
// hashes share the same source as creation: system-derived and refreshed in the audit record on write.
type ReplaceAIProviderInput struct {
	ExpectVersion     int64
	Name              string
	ProviderKind      string
	BaseURL           string
	ModelName         string
	Enabled           bool
	SelectionPriority int
	APIKey            string
	Configuration     map[string]any
}

// ReplaceAIProvider performs optimistic-lock replacement; any change invalidates unsubmitted Reviews.
// replaceProviderRow is the flat row struct for Replace pre-read / transactional read (GORM Scan mapping
// on embedded anonymous structs is unreliable, so fields are flattened explicitly).
type replaceProviderRow struct {
	ID                string
	Name              string
	ProviderKind      string
	BaseURL           string
	ModelName         string
	Enabled           bool
	SelectionPriority int
	Configuration     []byte
	SecretCiphertext  []byte
	SecretNonce       []byte
	SecretKeyID       string
	Version           int64
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

func (s *Services) ReplaceAIProvider(ctx context.Context, id string, in ReplaceAIProviderInput) (*AIProviderView, error) {
	// Execute input validation before currency checks or network probes (network calls must not hold DB locks).
	if err := ValidateProviderConfiguration(in.Configuration); err != nil {
		return nil, err
	}
	if err := validateBaseURL(in.BaseURL); err != nil {
		return nil, err
	}
	if in.Configuration != nil {
		if _, _, profErr := promptprofile.ResolveFromConfiguration(in.ModelName, sanitizeProviderConfiguration(in.Configuration)); profErr != nil {
			return nil, &domain.Error{
				Code: 1001, Name: "VALIDATION_FAILED",
				Fields: map[string][]string{"system_prompt_profile": {profErr.Error()}},
			}
		}
	}
	if id == "" || in.ModelName == "" {
		return nil, &domain.Error{Code: 1001, Name: "VALIDATION_FAILED", Detail: "incomplete endpoint"}
	}
	// S008 Price-04 enable-side validation: when budget is enabled and a currently effective rate exists,
	// the rate currency must match the budget currency (save-side validation lives in CreateRateVersion).
	if in.Enabled {
		if err := s.CheckProviderEnableRateCurrency(ctx, s.DB, id); err != nil {
			return nil, err
		}
	}
	// Non-empty probedConfiguration = probe succeeded and negotiated an output mode; persist this effective
	// configuration (including the system-managed key structured_output_mode) as a whole inside the transaction.
	var probedConfiguration map[string]any
	if in.Enabled {
		var pre replaceProviderRow
		if err := s.DB.WithContext(ctx).Table("ai_providers").
			Select("id, name, provider_kind, base_url, model_name, enabled, selection_priority, configuration, secret_ciphertext, secret_nonce, secret_key_id, version, created_at, updated_at").
			Where("id = ?", id).Scan(&pre).Error; err != nil {
			return nil, err
		}
		// Key rotation also triggers re-probing (M1); the probe validates the effective configuration
		// that will be persisted (M2: use explicitly supplied config if present, otherwise retain existing).
		if pre.ID != "" && (pre.Enabled != in.Enabled || configChanged(pre, in) || in.APIKey != "") {
			probeKey := in.APIKey
			if probeKey == "" && len(pre.SecretCiphertext) > 0 {
				plain, err := s.Crypter.OpenColumn(crypto.PurposeProviderSecret, crypto.ColumnRef{
					Table:      "ai_providers",
					PrimaryKey: id,
					Column:     "secret_ciphertext",
				}, crypto.Envelope{
					Ciphertext: pre.SecretCiphertext, Nonce: pre.SecretNonce, KeyID: pre.SecretKeyID,
					Algorithm: "aes-256-gcm",
				})
				if err != nil {
					return nil, &domain.Error{Code: 1001, Name: "VALIDATION_FAILED"}
				}
				probeKey = string(plain)
			}
			effective := sanitizeProviderConfiguration(effectiveConfiguration(pre.Configuration, in.Configuration))
			effProf, _, profErr := promptprofile.ResolveFromConfiguration(in.ModelName, effective)
			if profErr != nil {
				return nil, &domain.Error{
					Code: 1001, Name: "VALIDATION_FAILED",
					Fields: map[string][]string{"system_prompt_profile": {profErr.Error()}},
				}
			}
			privHash := provider.PrivacyContractHashForProfile(effProf)
			mode, err := s.probeCandidate(ctx, provider.Endpoint{
				ProviderID: id, Name: in.Name, ProviderKind: in.ProviderKind,
				BaseURL: in.BaseURL, ModelName: in.ModelName, SelectionPriority: in.SelectionPriority,
				PrivacyContractHash: privHash, OutputSchemaHash: provider.OutputSchemaHash,
				APIKey: probeKey, Configuration: effective,
			})
			if err != nil {
				return nil, err
			}
			probedConfiguration = withOutputMode(effective, mode)
		}
	}
	var out *AIProviderView
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row replaceProviderRow
		if err := tx.WithContext(ctx).Table("ai_providers").
			Select("id, name, provider_kind, base_url, model_name, enabled, selection_priority, configuration, secret_ciphertext, secret_nonce, secret_key_id, version, created_at, updated_at").
			Where("id = ?", id).Scan(&row).Error; err != nil {
			return err
		}
		if row.ID == "" {
			return ErrNotFound
		}
		if row.Version != in.ExpectVersion {
			return ErrVersionConflict
		}
		// writtenCfg = effective configuration actually persisted (probed config takes precedence; explicit
		// replacement without probing preserves existing system keys — R1 L-4); read-back view shares the same source as persistence.
		var writtenCfg map[string]any
		if probedConfiguration != nil {
			writtenCfg = sanitizeProviderConfiguration(probedConfiguration)
		} else if in.Configuration != nil {
			sanitized := sanitizeProviderConfiguration(in.Configuration)
			writtenCfg = sanitized
			if existing := decodeConfiguration(row.Configuration); existing != nil {
				if mode, ok := existing[provider.ConfigKeyOutputMode].(string); ok && mode != "" {
					writtenCfg = withOutputMode(sanitized, mode)
				}
			}
		}
		effCfgForProfile := writtenCfg
		if effCfgForProfile == nil {
			effCfgForProfile = decodeConfiguration(row.Configuration)
		}
		effProf, _, profErr := promptprofile.ResolveFromConfiguration(in.ModelName, effCfgForProfile)
		if profErr != nil {
			return &domain.Error{
				Code: 1001, Name: "VALIDATION_FAILED",
				Fields: map[string][]string{"system_prompt_profile": {profErr.Error()}},
			}
		}
		privHash := provider.PrivacyContractHashForProfile(effProf)
		if err := (provider.Endpoint{
			ProviderID: id, Name: in.Name, ProviderKind: in.ProviderKind,
			BaseURL: in.BaseURL, ModelName: in.ModelName, SelectionPriority: in.SelectionPriority,
			PrivacyContractHash: privHash, OutputSchemaHash: provider.OutputSchemaHash,
			APIKey: "preview", Configuration: effCfgForProfile,
		}).Validate(); err != nil {
			return &domain.Error{Code: 1001, Name: "VALIDATION_FAILED"}
		}
		sets := map[string]any{
			"name": in.Name, "provider_kind": in.ProviderKind, "base_url": in.BaseURL,
			"model_name": in.ModelName, "enabled": in.Enabled,
			"selection_priority":    in.SelectionPriority,
			"privacy_contract_hash": privHash, "output_schema_hash": provider.OutputSchemaHash,
			"version": row.Version + 1, "updated_at": nowUTC(),
		}
		if writtenCfg != nil {
			cfg, err := json.Marshal(writtenCfg)
			if err != nil {
				return err
			}
			sets["configuration"] = cfg
		}
		if in.APIKey != "" {
			env, err := s.Crypter.SealColumn(crypto.PurposeProviderSecret, crypto.ColumnRef{
				Table:      "ai_providers",
				PrimaryKey: id,
				Column:     "secret_ciphertext",
			}, []byte(in.APIKey))
			if err != nil {
				return err
			}
			sets["secret_ciphertext"] = env.Ciphertext
			sets["secret_nonce"] = env.Nonce
			sets["secret_key_id"] = env.KeyID
		}
		// Keep semantics (M2): when configuration or secret is not explicitly supplied, retain the existing
		// column values rather than writing NULL (both columns are NOT NULL).
		updateSQL := `update ai_providers set name = ?, provider_kind = ?, base_url = ?, model_name = ?,
			 enabled = ?, selection_priority = ?, privacy_contract_hash = ?, output_schema_hash = ?,
			 version = ?, updated_at = ?`
		args := []any{sets["name"], sets["provider_kind"], sets["base_url"], sets["model_name"],
			sets["enabled"], sets["selection_priority"], sets["privacy_contract_hash"], sets["output_schema_hash"],
			sets["version"], sets["updated_at"]}
		if cfg, ok := sets["configuration"]; ok {
			updateSQL += `, configuration = ?`
			args = append(args, cfg)
		}
		if ct, ok := sets["secret_ciphertext"]; ok {
			updateSQL += `, secret_ciphertext = ?, secret_nonce = ?, secret_key_id = ?`
			args = append(args, ct, sets["secret_nonce"], sets["secret_key_id"])
		}
		updateSQL += ` where id = ? and version = ?`
		args = append(args, id, row.Version)
		res := tx.WithContext(ctx).Exec(updateSQL, args...)
		if res.Error != nil {
			// section 18.75-B: renaming or changing priority into a unique index collision likewise maps to 1005;
			// Detail distinguishes the conflict axis by constraint name per F-UX-002.
			if txutil.IsUniqueViolation(res.Error) {
				return &domain.Error{Code: 1005, Name: "RESOURCE_ALREADY_EXISTS",
					Detail: providerUniqueConflictDetail(res.Error, in.SelectionPriority)}
			}
			return res.Error
		}
		if res.RowsAffected != 1 {
			return ErrVersionConflict
		}
		// Provider chain changed -> invalidate all unsubmitted Reviews.
		if err := invalidateAllUnsubmittedRuns(ctx, tx); err != nil {
			return err
		}
		// Read-back = effective configuration after persistence (writtenCfg shares the same source as the sets
		// branch; if neither modified it, retain the existing row value).
		savedCfg := writtenCfg
		if savedCfg == nil {
			savedCfg = decodeConfiguration(row.Configuration)
		}
		cfgBytes, merr := json.Marshal(savedCfg)
		if merr != nil {
			return merr
		}
		out = &AIProviderView{ID: id, Name: in.Name, ProviderKind: in.ProviderKind,
			BaseURL: in.BaseURL, ModelName: in.ModelName, Enabled: in.Enabled,
			SelectionPriority: in.SelectionPriority, APIKeyConfigured: true,
			Configuration: configurationView(cfgBytes),
			Version:       row.Version + 1, CreatedAt: row.CreatedAt, UpdatedAt: nowUTC()}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// DeleteAIProvider deletes a Provider (when no active references exist).
func (s *Services) DeleteAIProvider(ctx context.Context, id string, expectVersion int64) error {
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.WithContext(ctx).Exec(
			`delete from ai_providers where id = ? and version = ?`, id, expectVersion)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			// C1004-only profile: stale If-Match surfaces as concurrent
			// modification, identical to the tx-level race.
			return ErrConcurrent
		}
		return invalidateAllUnsubmittedRuns(ctx, tx)
	})
}

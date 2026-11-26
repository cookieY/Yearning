// providerstore_probe.go AI Provider chain loading, connection probing, model catalog discovery, and configuration validation.
package application

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"

	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/review/domain"
	"github.com/cookieY/YearningX/backend/internal/review/persistence"
	"github.com/cookieY/YearningX/backend/internal/review/promptprofile"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
)

// LoadProviderChain assembles the ordered enabled chain (ascending selection_priority;
// decrypted secrets reside in memory only), with dial injecting Adapter construction (OpenAI in production / Fake in tests).
func (s *Services) LoadProviderChain(ctx context.Context, db *gorm.DB,
	dial func(provider.Endpoint) (provider.Client, error)) (*provider.Chain, error) {
	// At runtime, contract hashes are taken from platform constants (same source as writes) rather than
	// reading historical row values: after a fingerprint upgrade, existing rows stay consistent within the chain without needing manual re-saving.
	var rows []struct {
		ID                string
		Name              string
		ProviderKind      string
		BaseURL           string
		ModelName         string
		Enabled           bool
		SelectionPriority int
		SecretCiphertext  []byte
		SecretNonce       []byte
		SecretKeyID       string
		Configuration     []byte
	}
	err := db.WithContext(ctx).Raw(`
		select id::text, name, provider_kind, base_url, model_name, enabled, selection_priority,
		       secret_ciphertext, secret_nonce, secret_key_id, configuration::text as configuration
		from ai_providers where enabled order by selection_priority`).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	endpoints := []provider.Endpoint{}
	for _, r := range rows {
		key, err := s.Crypter.OpenColumn(crypto.PurposeProviderSecret, crypto.ColumnRef{
			Table:      "ai_providers",
			PrimaryKey: r.ID,
			Column:     "secret_ciphertext",
		}, crypto.Envelope{
			Ciphertext: r.SecretCiphertext, Nonce: r.SecretNonce, KeyID: r.SecretKeyID, Algorithm: "aes-256-gcm",
		})
		if err != nil {
			return nil, err
		}
		cfgMap := decodeConfiguration(r.Configuration)
		effProf, _, profErr := promptprofile.ResolveFromConfiguration(r.ModelName, cfgMap)
		if profErr != nil {
			effProf = promptprofile.ProfileDefault
		}
		endpoints = append(endpoints, provider.Endpoint{
			ProviderID: r.ID, Name: r.Name, ProviderKind: r.ProviderKind,
			BaseURL: r.BaseURL, ModelName: r.ModelName,
			SelectionPriority:   r.SelectionPriority,
			PrivacyContractHash: provider.PrivacyContractHashForProfile(effProf), OutputSchemaHash: provider.OutputSchemaHash,
			APIKey: string(key), Configuration: cfgMap,
		})
	}
	sort.SliceStable(endpoints, func(i, j int) bool {
		return endpoints[i].SelectionPriority < endpoints[j].SelectionPriority
	})
	return provider.NewChain(endpoints, dial)
}

// invalidateAllUnsubmittedRuns invalidates all non-outdated Runs of unsubmitted drafts
// when the active AI Provider chain changes (inputs_changed).
func invalidateAllUnsubmittedRuns(ctx context.Context, tx *gorm.DB) error {
	var rows []persistence.ReviewRunModel
	err := tx.WithContext(ctx).Raw(`
		select r.id::text as id, r.draft_id::text as draft_id, r.draft_revision, r.state, r.version
		from review_runs r
		join change_drafts d on d.id = r.draft_id
		where r.state <> 'outdated'
		  and d.state <> 'submitted'`).Scan(&rows).Error
	if err != nil {
		return err
	}
	return invalidateRuns(ctx, tx, rows)
}

// TestAIProviderConnection executes a targeted health probe against a single Provider.
func (s *Services) TestAIProviderConnection(ctx context.Context, id string) error {
	var row struct {
		ID               string
		Name             string
		ProviderKind     string
		BaseURL          string
		ModelName        string
		SecretCiphertext []byte
		SecretNonce      []byte
		SecretKeyID      string
		Configuration    []byte
	}
	if err := s.DB.WithContext(ctx).Raw(`
		select id::text, name, provider_kind, base_url, model_name,
		       secret_ciphertext, secret_nonce, secret_key_id, configuration::text as configuration
		from ai_providers where id = ?`, id).Scan(&row).Error; err != nil {
		return err
	}
	if row.ID == "" {
		return ErrNotFound
	}
	key, err := s.Crypter.OpenColumn(crypto.PurposeProviderSecret, crypto.ColumnRef{
		Table:      "ai_providers",
		PrimaryKey: row.ID,
		Column:     "secret_ciphertext",
	}, crypto.Envelope{
		Ciphertext: row.SecretCiphertext, Nonce: row.SecretNonce, KeyID: row.SecretKeyID, Algorithm: "aes-256-gcm",
	})
	if err != nil {
		return err
	}
	cfgMap := decodeConfiguration(row.Configuration)
	effProf, _, profErr := promptprofile.ResolveFromConfiguration(row.ModelName, cfgMap)
	if profErr != nil {
		effProf = promptprofile.ProfileDefault
	}
	adapter, err := probeDial(provider.Endpoint{
		ProviderID: row.ID, Name: row.Name, ProviderKind: row.ProviderKind,
		BaseURL: row.BaseURL, ModelName: row.ModelName,
		PrivacyContractHash: provider.PrivacyContractHashForProfile(effProf), OutputSchemaHash: provider.OutputSchemaHash,
		APIKey: string(key), Configuration: cfgMap,
	})
	if err != nil {
		// Dial failure follows the same semantics as probe failure (2011 + rejection reason) rather than a raw 1001.
		return fmt.Errorf("%w: dial: %v", &domain.Error{
			Code: 2011, Name: "AI_PROVIDER_FAILED", Detail: probeUpstreamDetail(err),
		}, err)
	}
	if err := adapter.Health(ctx); err != nil {
		// section 18.75-A fix (owner live test: inline connection test only showed a generic message): the Detail
		// surfacing fixed for probes applies equally to targeted health probes — errors.As still unwraps 2011,
		// and Detail reaches the inline row directly via meta.detail.
		return fmt.Errorf("%w: %v", &domain.Error{
			Code: 2011, Name: "AI_PROVIDER_FAILED", Detail: probeUpstreamDetail(err),
		}, err)
	}
	return nil
}

// probeDial constructs a probe Adapter (package-level variable: injectable in tests).
var probeDial = func(ep provider.Endpoint) (provider.Client, error) {
	return provider.NewOpenAIAdapter(ep, nil)
}

// ListProviderModels fetches the upstream OpenAI-Compatible model catalog (section 18.76-B,
// Provider form autocomplete, not a resource read). base_url is required; when api_key is omitted,
// provider_id must be supplied to reuse the stored secret (edit mode never echoes secrets).
func (s *Services) ListProviderModels(ctx context.Context, baseURL, apiKey, providerID string) ([]string, error) {
	if baseURL == "" || (apiKey == "" && providerID == "") {
		return nil, &domain.Error{Code: 1001, Name: "VALIDATION_FAILED"}
	}
	if err := validateBaseURL(baseURL); err != nil {
		return nil, err
	}
	if apiKey == "" {
		var row struct {
			ID               string
			SecretCiphertext []byte
			SecretNonce      []byte
			SecretKeyID      string
		}
		if err := s.DB.WithContext(ctx).Raw(`
			select id::text, secret_ciphertext, secret_nonce, secret_key_id
			from ai_providers where id = ?`, providerID).Scan(&row).Error; err != nil {
			return nil, err
		}
		if row.ID == "" {
			return nil, ErrNotFound
		}
		plain, err := s.Crypter.OpenColumn(crypto.PurposeProviderSecret, crypto.ColumnRef{
			Table:      "ai_providers",
			PrimaryKey: row.ID,
			Column:     "secret_ciphertext",
		}, crypto.Envelope{
			Ciphertext: row.SecretCiphertext, Nonce: row.SecretNonce, KeyID: row.SecretKeyID,
			Algorithm: "aes-256-gcm",
		})
		if err != nil {
			return nil, &domain.Error{Code: 2011, Name: "AI_PROVIDER_FAILED"}
		}
		apiKey = string(plain)
	}
	models, err := provider.ListModels(ctx, baseURL, apiKey)
	if err != nil {
		// Same channel as section 18.75-A: upstream rejection snapshot reaches the operator form directly.
		return nil, fmt.Errorf("%w: models: %v", &domain.Error{
			Code: 2011, Name: "AI_PROVIDER_FAILED", Detail: probeUpstreamDetail(err),
		}, err)
	}
	return models, nil
}

// decodeConfiguration parses the configuration jsonb column.
func decodeConfiguration(raw []byte) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	out := map[string]any{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

// ValidateProviderConfiguration validates controlled keys in configuration
// (S008: max_tokens 256..200000; temperature 0..2; reasoning_effort in {low,medium,high} — vendor
// default = omit the key completely; unknown keys are rejected with 1001).
// The system-managed key structured_output_mode is written exclusively by probe negotiation; user input
// containing this key is rejected as an unknown key (section 18.76-A: unforgeable by clients).
func ValidateProviderConfiguration(cfg map[string]any) error {
	for k, v := range cfg {
		switch k {
		case "max_tokens":
			f, ok := v.(float64)
			if !ok || f < 256 || f > 200000 || f != math.Trunc(f) {
				// RCP-20261102-S008: 1001 carries field-level rejection reasons (meta.fields)
				// for frontend form field targeting; client-side mirror validation remains the first line of defense.
				return &domain.Error{Code: 1001, Name: "VALIDATION_FAILED",
					Detail: "max_tokens must be an integer between 256 and 200000",
					Fields: map[string][]string{"max_tokens": {"must be an integer between 256 and 200000"}}}
			}
		case "context_window":
			if !isPositiveIntConfigValue(v) {
				return &domain.Error{Code: 1001, Name: "VALIDATION_FAILED",
					Detail: "context_window must be a positive integer",
					Fields: map[string][]string{"context_window": {"must be a positive integer"}}}
			}
		case "temperature":
			f, ok := v.(float64)
			if !ok || f < 0 || f > 2 {
				return &domain.Error{Code: 1001, Name: "VALIDATION_FAILED",
					Detail: "temperature must be a number between 0 and 2",
					Fields: map[string][]string{"temperature": {"must be a number between 0 and 2"}}}
			}
		case "reasoning_effort":
			s, ok := v.(string)
			if !ok || (s != "low" && s != "medium" && s != "high") {
				return &domain.Error{Code: 1001, Name: "VALIDATION_FAILED",
					Detail: "reasoning_effort must be one of low, medium, high",
					Fields: map[string][]string{"reasoning_effort": {"must be one of low, medium, high"}}}
			}
		case "system_prompt_profile":
			s, ok := v.(string)
			if !ok || promptprofile.ValidateExplicit(s) != nil {
				return &domain.Error{Code: 1001, Name: "VALIDATION_FAILED",
					Detail: "system_prompt_profile must be one of default, san",
					Fields: map[string][]string{"system_prompt_profile": {"must be one of default, san"}}}
			}
		default:
			return &domain.Error{Code: 1001, Name: "VALIDATION_FAILED",
				Detail: fmt.Sprintf("unknown configuration key %q (allowed: max_tokens, context_window, temperature, reasoning_effort, system_prompt_profile)", k),
				Fields: map[string][]string{k: {"unknown configuration key (allowed: max_tokens, context_window, temperature, reasoning_effort, system_prompt_profile)"}}}
		}
	}
	return nil
}

func isPositiveIntConfigValue(v any) bool {
	switch n := v.(type) {
	case float64:
		return !math.IsNaN(n) && !math.IsInf(n, 0) && n >= 1 && n <= float64(math.MaxInt32) && n == math.Trunc(n)
	case int:
		return n >= 1 && n <= math.MaxInt32
	case int64:
		return n >= 1 && n <= math.MaxInt32
	default:
		return false
	}
}

func validateBaseURL(baseURL string) error {
	if _, err := provider.ParseBaseURL(baseURL); err != nil {
		return &domain.Error{
			Code: 1001, Name: "VALIDATION_FAILED", Detail: err.Error(),
			Fields: map[string][]string{"base_url": {err.Error()}},
		}
	}
	return nil
}

// probeCandidate executes the pre-enable health probe against a candidate Endpoint and negotiates the
// structured output mode (real output Schema path; failure = cannot enable, PRD 9.2 chain admission).
// Returns the effective mode (json_schema|json_object) to be persisted by the caller.
func (s *Services) probeCandidate(ctx context.Context, ep provider.Endpoint) (string, error) {
	adapter, err := probeDial(ep)
	if err != nil {
		return "", fmt.Errorf("%w: dial: %v", &domain.Error{Code: 2011, Name: "AI_PROVIDER_FAILED"}, err)
	}
	mode, err := adapter.NegotiateOutputMode(ctx)
	if err != nil {
		// section 18.75-A: preserve the upstream rejection reason (HTTP status and error response snapshot) —
		// errors.As still unwraps domain.Error(2011), and Detail reaches the operator form via the response envelope.
		detail := probeUpstreamDetail(err)
		return "", fmt.Errorf("%w: probe: %v", &domain.Error{
			Code: 2011, Name: "AI_PROVIDER_FAILED", Detail: detail,
		}, err)
	}
	return mode, nil
}

// withOutputMode returns a configuration copy with the system-managed key structured_output_mode appended
// (section 18.76-A; unforgeable by clients: ValidateProviderConfiguration rejects unknown keys, so this
// key is written exclusively from probe negotiation results).
func withOutputMode(cfg map[string]any, mode string) map[string]any {
	out := make(map[string]any, len(cfg)+1)
	for k, v := range cfg {
		out[k] = v
	}
	out[provider.ConfigKeyOutputMode] = mode
	return out
}

// probeUpstreamDetail extracts an operator-readable upstream rejection reason from a probe error chain:
// the raw snapshot from statusError, or the error string otherwise; uniformly length-capped (section 18.75-A).
func probeUpstreamDetail(err error) string {
	const max = 300
	detail := err.Error()
	if len(detail) > max {
		detail = detail[:max]
	}
	return detail
}

// configChanged reports whether an enabled configuration has changed (triggering a re-probe).
// S008 Param-04: an explicitly provided configuration (including an empty object {} = clear all user
// overrides) must trigger a real change check — nil check replaces len > 0 so an empty override is no
// longer mistaken for "unmodified" and skipped for probing; omission (nil) = retain existing (continuing M2 semantics).
func configChanged(row replaceProviderRow, in ReplaceAIProviderInput) bool {
	if row.Name != in.Name || row.BaseURL != in.BaseURL || row.ModelName != in.ModelName ||
		row.SelectionPriority != in.SelectionPriority {
		return true
	}
	return in.Configuration != nil
}

// effectiveConfiguration returns the effective configuration: use explicitly provided config if non-nil, otherwise retain existing.
func effectiveConfiguration(existing []byte, in map[string]any) map[string]any {
	if in != nil {
		return in
	}
	return decodeConfiguration(existing)
}

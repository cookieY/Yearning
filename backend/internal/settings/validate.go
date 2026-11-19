// Package settings implements the S006 typed settings domain (PRD 10; frozen surface in
// settings-namespaces.json): read/replace/schema/revisions/impact assessment across five namespaces.
// Forbidden fields are rejected; high-impact changes require impact assessment + confirmation token;
// all mutations are recorded in audit_events.
package settings

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	// F-SET-001: IANA time zone validation requires a tzdata database; embedding a copy ensures
	// time.LoadLocation works identically inside minimal containers (without /usr/share/zoneinfo)
	// and uses the exact same data source as runtime resolution for cross-deployment consistency.
	_ "time/tzdata"
)

// Namespace enumerates the five frozen settings namespaces.
const (
	NsGeneral   = "general"
	NsQuery     = "query"
	NsExecution = "execution"
	NsAIBudget  = "ai-budget"
	NsBranding  = "branding"
)

// ValidNamespace is the whitelist of valid settings namespaces.
func ValidNamespace(ns string) bool {
	switch ns {
	case NsGeneral, NsQuery, NsExecution, NsAIBudget, NsBranding:
		return true
	}
	return false
}

// fieldSpec defines the specification of a single field (frozen values from settings-namespaces.json).
type fieldSpec struct {
	kind     string // bool|int|string|enum|iana_timezone|iso_4217_currency|plain_text|nullable_uuid|enum_array|execution_window_array|osc_policy
	min, max int64
	values   []string
	def      any
	impact   string // immediate|high|next_query|next_request|next_reservation|next_usage_entry|next_operation|next_session|next_page_load|next_step
	minLen   int
	maxLen   int
}

// namespaces holds the frozen field specifications (matching settings-namespaces.json field for field;
// execution.osc is a composite policy).
var namespaces = map[string]map[string]fieldSpec{
	NsGeneral: {
		"registration_enabled": {kind: "bool", def: false, impact: "immediate"},
		"system_timezone":      {kind: "iana_timezone", def: "Asia/Shanghai", impact: "next_operation"},
		"default_locale":       {kind: "enum", values: []string{"zh-CN", "en-US"}, def: "zh-CN", impact: "next_session"},
		// S007: controlled public base URL of the system (sole source for callback links in notifications;
		// empty string omits links from messages; never derived from request Host headers).
		"public_base_url": {kind: "string", minLen: 0, maxLen: 512, def: "", impact: "immediate"},
	},
	NsQuery: {
		"approval_enabled":                         {kind: "bool", def: true, impact: "high"},
		"default_timeout_ms":                       {kind: "int", min: 1, max: 300000, def: 30000, impact: "next_query"},
		"maximum_approved_access_duration_minutes": {kind: "int", min: 1, max: 525600, def: 43200, impact: "next_request"},
		"max_result_rows":                          {kind: "int", min: 1, max: 100000, def: 1000, impact: "next_query"},
	},
	NsExecution: {
		"restriction_enabled": {kind: "bool", def: false, impact: "high"},
		"windows":             {kind: "execution_window_array", def: []any{}, impact: "high"},
		"osc":                 {kind: "osc_policy", def: map[string]any{"tool": "none", "defaults": map[string]any{}}, impact: "high"},
	},
	NsAIBudget: {
		"enforced":                {kind: "bool", def: false, impact: "immediate"},
		"currency":                {kind: "iso_4217_currency", def: "USD", impact: "next_reservation"},
		"daily_budget_minor":      {kind: "int", min: 1, max: 9223372036854775807, def: 10000, impact: "next_reservation"},
		"alert_threshold_percent": {kind: "int", min: 1, max: 100, def: 80, impact: "next_usage_entry"},
		// OBS-002 ruling: per-token pricing unit cost (in minor currency units per million tokens);
		// both 0 falls back to the legacy fixed per-stage estimate.
		"input_price_per_million_minor":  {kind: "int", min: 0, max: 9223372036854775807, def: 0, impact: "next_reservation"},
		"output_price_per_million_minor": {kind: "int", min: 0, max: 9223372036854775807, def: 0, impact: "next_reservation"},
	},
	NsBranding: {
		"product_name":      {kind: "string", minLen: 1, maxLen: 64, def: "Yearning", impact: "next_page_load"},
		"login_description": {kind: "plain_text", minLen: 0, maxLen: 500, def: "", impact: "next_page_load"},
		"logo_asset_id":     {kind: "nullable_uuid", def: nil, impact: "next_page_load"},
		"favicon_asset_id":  {kind: "nullable_uuid", def: nil, impact: "next_page_load"},
	},
}

// forbiddenFields lists fields prohibited by contract (rejected if present in any namespace).
var forbiddenFields = map[string]bool{
	"maximum_rows": true, "maximum_bytes": true, "user_concurrency": true,
	"datasource_concurrency": true, "export_row_limit": true,
	"export_file_size_limit": true, "per_order_token_limit": true,
	"per_order_cost_limit": true, "rpc": true, "auto_task": true,
}

// Validate validates and normalizes namespace settings (unknown keys, forbidden keys, or range violations return an error).
// Returns the normalized settings map and the impact map of changed fields.
func Validate(namespace string, raw []byte) (map[string]any, map[string]string, error) {
	specs, ok := namespaces[namespace]
	if !ok {
		return nil, nil, fmt.Errorf("%w: namespace", ErrValidation)
	}
	var in map[string]any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, nil, fmt.Errorf("%w: json", ErrValidation)
		}
	}
	impacts := map[string]string{}
	out := map[string]any{}
	for k, v := range in {
		if forbiddenFields[k] {
			return nil, nil, fmt.Errorf("%w: forbidden field %s", ErrValidation, k)
		}
		spec, ok := specs[k]
		if !ok {
			return nil, nil, fmt.Errorf("%w: unknown field %s", ErrValidation, k)
		}
		norm, err := validateValue(k, spec, v)
		if err != nil {
			return nil, nil, err
		}
		out[k] = norm
		impacts[k] = spec.impact
	}
	return out, impacts, nil
}

// ErrValidation is a stable domain validation error.
var ErrValidation = fmt.Errorf("settings: validation failed")

func validateValue(key string, spec fieldSpec, v any) (any, error) {
	switch spec.kind {
	case "bool":
		b, ok := v.(bool)
		if !ok {
			return nil, fmt.Errorf("%w: %s bool", ErrValidation, key)
		}
		return b, nil
	case "int":
		f, ok := v.(float64)
		if !ok || f != float64(int64(f)) {
			return nil, fmt.Errorf("%w: %s int", ErrValidation, key)
		}
		n := int64(f)
		if n < spec.min || n > spec.max {
			return nil, fmt.Errorf("%w: %s range", ErrValidation, key)
		}
		return n, nil
	case "string", "plain_text":
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("%w: %s string", ErrValidation, key)
		}
		if len(s) < spec.minLen || len(s) > spec.maxLen {
			return nil, fmt.Errorf("%w: %s length", ErrValidation, key)
		}
		return s, nil
	case "enum":
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("%w: %s enum", ErrValidation, key)
		}
		for _, a := range spec.values {
			if s == a {
				return s, nil
			}
		}
		return nil, fmt.Errorf("%w: %s enum value", ErrValidation, key)
	case "iana_timezone":
		s, ok := v.(string)
		if !ok || !tzPattern(s) {
			return nil, fmt.Errorf("%w: %s timezone", ErrValidation, key)
		}
		return s, nil
	case "iso_4217_currency":
		s, ok := v.(string)
		if !ok || len(s) != 3 || !alphaUpper(s) {
			return nil, fmt.Errorf("%w: %s currency", ErrValidation, key)
		}
		return s, nil
	case "nullable_uuid":
		if v == nil {
			return nil, nil
		}
		s, ok := v.(string)
		if !ok || !uuidShape(s) {
			return nil, fmt.Errorf("%w: %s uuid", ErrValidation, key)
		}
		return s, nil
	case "enum_array":
		arr, ok := v.([]any)
		if !ok {
			return nil, fmt.Errorf("%w: %s array", ErrValidation, key)
		}
		out := make([]any, 0, len(arr))
		for _, it := range arr {
			s, ok := it.(string)
			if !ok {
				return nil, fmt.Errorf("%w: %s array item", ErrValidation, key)
			}
			found := false
			for _, a := range spec.values {
				if s == a {
					found = true
				}
			}
			if !found && len(spec.values) > 0 {
				return nil, fmt.Errorf("%w: %s array value", ErrValidation, key)
			}
			out = append(out, s)
		}
		return out, nil
	case "execution_window_array":
		arr, ok := v.([]any)
		if !ok || len(arr) > 14 {
			return nil, fmt.Errorf("%w: %s windows", ErrValidation, key)
		}
		for _, it := range arr {
			m, ok := it.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("%w: %s window item", ErrValidation, key)
			}
			if _, ok := m["days"]; !ok {
				return nil, fmt.Errorf("%w: %s window days", ErrValidation, key)
			}
			if _, ok := m["start"]; !ok {
				return nil, fmt.Errorf("%w: %s window start", ErrValidation, key)
			}
			if _, ok := m["end"]; !ok {
				return nil, fmt.Errorf("%w: %s window end", ErrValidation, key)
			}
		}
		return arr, nil
	case "osc_policy":
		m, ok := v.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%w: %s policy", ErrValidation, key)
		}
		tool, _ := m["tool"].(string)
		switch tool {
		case "none", "gh-ost", "pt-osc":
		default:
			return nil, fmt.Errorf("%w: %s tool", ErrValidation, key)
		}
		if d, ok := m["defaults"]; ok {
			dm, ok := d.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("%w: %s defaults", ErrValidation, key)
			}
			for dk := range dm {
				switch dk {
				case "max_load", "critical_load", "critical_load_interval_millis",
					"critical_load_hibernate_seconds", "chunk_size", "dml_batch_size",
					"max_lag", "nice_ratio", "cut_over_lock_timeout_seconds",
					"retries", "default_retries", "heartbeat_interval_millis",
					"exponential_backoff_max_interval", "exact_rowcount",
					"concurrent_rowcount", "ok_to_drop_table",
					"timestamp_old_table", "cut_over_exponential_backoff",
					"attempt_instant_ddl":
				default:
					return nil, fmt.Errorf("%w: %s default key %s", ErrValidation, key, dk)
				}
			}
		}
		return m, nil
	}
	return nil, fmt.Errorf("%w: %s kind", ErrValidation, key)
}

// tzPattern validates IANA time zone names (F-SET-001: legacy validation only checked character sets,
// allowing concatenated strings like "UTCUTCAsia/Shanghai" to pass and persist; replaced with real time zone
// lookup sharing the runtime data source -- any name rejected by LoadLocation is rejected).
// "Local" depends on the host environment and is rejected as a persisted value; "Asia//Shanghai" loads via
// filesystem path normalization but is not a canonical zone name and is rejected due to its empty path segment.
func tzPattern(s string) bool {
	if len(s) == 0 || len(s) > 64 || s == "Local" {
		return false
	}
	for _, seg := range strings.Split(s, "/") {
		if seg == "" {
			return false
		}
	}
	if _, err := time.LoadLocation(s); err != nil {
		return false
	}
	return true
}

func alphaUpper(s string) bool {
	for _, r := range s {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return true
}

func uuidShape(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if r != '-' {
				return false
			}
			continue
		}
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
			return false
		}
	}
	return true
}

// SchemaJSON generates the JSON Schema for a namespace (for getSettingsSchema; contains no secret values).
func SchemaJSON(namespace string) (string, error) {
	specs, ok := namespaces[namespace]
	if !ok {
		return "", ErrNotFound
	}
	props := map[string]any{}
	required := []string{}
	for name, spec := range specs {
		switch spec.kind {
		case "bool":
			props[name] = map[string]any{"type": "boolean"}
		case "int":
			props[name] = map[string]any{"type": "integer", "minimum": spec.min, "maximum": spec.max}
		case "enum":
			props[name] = map[string]any{"type": "string", "enum": spec.values}
		case "enum_array":
			props[name] = map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": spec.values}}
		case "string", "plain_text":
			props[name] = map[string]any{"type": "string", "minLength": spec.minLen, "maxLength": spec.maxLen}
		case "nullable_uuid":
			props[name] = map[string]any{"type": []string{"string", "null"}}
		default:
			props[name] = map[string]any{"type": "object"}
		}
		required = append(required, name)
	}
	schema := map[string]any{
		"$schema":              "https://json-schema.org/draft/2020-12/schema",
		"type":                 "object",
		"properties":           props,
		"required":             sorted(required),
		"additionalProperties": false,
	}
	b, err := json.Marshal(schema)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func sorted(in []string) []string {
	out := append([]string{}, in...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// service.go implements the settings domain application service: read, replace (optimistic lock + audit + Outbox invalidation),
// and impact assessment (confirmation token required for high-impact changes).
package settings

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/crypto"
)

// ErrNotFound / ErrConflict / ErrTokenInvalid are stable domain errors.
var (
	ErrNotFound     = fmt.Errorf("settings: not found")
	ErrConflict     = fmt.Errorf("settings: version conflict")
	ErrTokenInvalid = fmt.Errorf("settings: impact token invalid")
)

// Service is the settings domain service.
type Service struct {
	DB      *gorm.DB
	Crypter *crypto.Crypter
	Now     func() time.Time
}

// New constructs a Service.
func New(db *gorm.DB, crypter *crypto.Crypter) *Service {
	return &Service{DB: db, Crypter: crypter, Now: func() time.Time { return time.Now().UTC() }}
}

// Row is a projection of a system_settings table row.
type Row struct {
	Namespace     string    `gorm:"column:namespace"`
	SchemaVersion int       `gorm:"column:schema_version"`
	Settings      []byte    `gorm:"column:settings"`
	Version       int64     `gorm:"column:version"`
	UpdatedBy     string    `gorm:"column:updated_by_user_id"`
	UpdatedAt     time.Time `gorm:"column:updated_at"`
}

// NamespaceView conforms to the OpenAPI SettingsNamespace schema.
type NamespaceView struct {
	Namespace     string         `json:"namespace"`
	SchemaVersion int            `json:"schema_version"`
	Settings      map[string]any `json:"settings"`
	Version       int64          `json:"version"`
	UpdatedBy     string         `json:"updated_by"`
	UpdatedAt     string         `json:"updated_at"`
}

// storageNamespace maps a contract namespace name to its system_settings storage key.
// SEC-0010: the contract/OpenAPI surface uses ai-budget, whereas the database CHECK constraint (frozen in 000001)
// only allows ai_budget -- previously writing ai-budget violated CHECK and budget reads on ai_budget always returned empty,
// keeping enforced permanently false. This mapping exists only at the persistence boundary; API views, audit logs, outbox,
// and revision history continue to use the contract name ai-budget.
func storageNamespace(ns string) string {
	if ns == NsAIBudget {
		return "ai_budget"
	}
	return ns
}

// Get reads a settings namespace (returning a synthetic view with all defaults if not yet persisted).
func (s *Service) Get(ctx context.Context, namespace string) (*NamespaceView, error) {
	if !ValidNamespace(namespace) {
		return nil, ErrNotFound
	}
	var row Row
	err := s.DB.WithContext(ctx).Raw(`
		select namespace, schema_version, settings, version, updated_by_user_id, updated_at
		from system_settings where namespace = ?`, storageNamespace(namespace)).Scan(&row).Error
	if err != nil {
		return nil, err
	}
	settings := defaultsOf(namespace)
	if len(row.Settings) > 0 {
		_ = json.Unmarshal(row.Settings, &settings)
	}
	if row.Namespace == "" {
		// Uninitialized namespace: synthesize defaults (version 1).
		return &NamespaceView{Namespace: namespace, SchemaVersion: 1,
			Settings: settings, Version: 1, UpdatedBy: zeroUUID(),
			UpdatedAt: s.Now().Format(time.RFC3339Nano)}, nil
	}
	return &NamespaceView{Namespace: namespace, SchemaVersion: row.SchemaVersion,
		Settings: settings, Version: row.Version, UpdatedBy: row.UpdatedBy,
		UpdatedAt: row.UpdatedAt.UTC().Format(time.RFC3339Nano)}, nil
}

// ReplaceInput holds parameters for replacing namespace settings.
type ReplaceInput struct {
	Namespace     string
	Settings      json.RawMessage
	ExpectVersion int64
	ActorID       string
	ActorUsername string
	// ImpactToken is the confirmation token for high-impact namespaces (empty allowed for low-impact changes).
	ImpactToken string
}

// Replace replaces namespace settings (validation -> high-impact token verification -> optimistic lock update ->
// audit trail + outbox invalidation event). Audit metadata stores changed_paths and
// impact_summary (the source for revision history projections, PRD 10 §4).
func (s *Service) Replace(ctx context.Context, in ReplaceInput) (*NamespaceView, error) {
	if !ValidNamespace(in.Namespace) {
		return nil, ErrNotFound
	}
	normalized, impacts, err := Validate(in.Namespace, in.Settings)
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(normalized)
	if err != nil {
		return nil, err
	}
	// Impact snapshot (input for revision projection): computed from the same source as Assess.
	var current Row
	if err := s.DB.WithContext(ctx).Raw(`
		select namespace, schema_version, settings, version, updated_by_user_id, updated_at
		from system_settings where namespace = ?`, storageNamespace(in.Namespace)).Scan(&current).Error; err != nil {
		return nil, err
	}
	cur := defaultsOf(in.Namespace)
	if len(current.Settings) > 0 {
		_ = json.Unmarshal(current.Settings, &cur)
	}
	summary, err := s.summarize(ctx, in.Namespace, cur, normalized, impacts)
	if err != nil {
		return nil, err
	}
	if summary.Level == "high" {
		if in.ImpactToken == "" {
			return nil, ErrTokenInvalid
		}
		if err := s.VerifyImpactToken(in.ImpactToken, in.Namespace, in.Settings,
			in.ExpectVersion, in.ActorID); err != nil {
			return nil, err
		}
	} else if in.ImpactToken != "" {
		if err := s.VerifyImpactToken(in.ImpactToken, in.Namespace, in.Settings,
			in.ExpectVersion, in.ActorID); err != nil {
			return nil, err
		}
	}
	now := s.Now()
	// Single transaction: CAS update (or initial insert) + audit log + outbox invalidation event; rolls back as a unit on any failure.
	txErr := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Exec(`
			update system_settings set settings = ?, version = version + 1,
			    updated_by_user_id = ?, updated_at = ?
			where namespace = ? and version = ?`,
			payload, in.ActorID, now, storageNamespace(in.Namespace), in.ExpectVersion)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			// Uninitialized namespace: insert via CAS. Inserted version is ExpectVersion+1,
			// ensuring any successful write advances past the version held by the client (first write becomes version 2):
			// otherwise a first write against the synthetic version=1 view would remain at version 1, allowing a replay
			// carrying the same If-Match + token to apply a second time and violating the single-consumption invariant
			// that "the first successful PUT must increment the version" (PRD 10 §4).
			ins := tx.Exec(`
				insert into system_settings (namespace, schema_version, settings, version,
				    updated_by_user_id, created_at, updated_at)
				values (?, 1, ?, ?, ?, ?, ?)
				on conflict (namespace) do nothing`,
				storageNamespace(in.Namespace), payload, in.ExpectVersion+1, in.ActorID, now, now)
			if ins.Error != nil {
				return ins.Error
			}
			if ins.RowsAffected != 1 {
				return ErrConflict
			}
		}
		// Audit trail (source for revision history; contains no secret values as settings hold no secrets).
		if auditErr := tx.Exec(`
			insert into audit_events (id, event_type, actor_kind, actor_id, actor_username_snapshot,
			    resource_type, resource_id, action, outcome, metadata, occurred_at, expires_at)
			values (gen_random_uuid(), 'settings.replaced', 'user', ?, ?, 'system_settings', NULL,
			    'replace', 'succeeded', ?::jsonb, ?, ?::timestamptz + interval '90 days')`,
			in.ActorID, in.ActorUsername,
			jsonOrEmpty(map[string]any{
				"namespace":      in.Namespace,
				"version":        in.ExpectVersion + 1,
				"changed_paths":  summary.ChangedPaths,
				"impact_summary": summary,
			}), now, now).Error; auditErr != nil {
			return auditErr
		}
		return tx.Exec(`
			insert into outbox_events (id, event_type, subject, payload, occurred_at, available_at)
			values (gen_random_uuid(), 'settings.replaced', 'settings/' || ?, ?::jsonb, ?, ?)`,
			in.Namespace, jsonOrEmpty(map[string]any{"namespace": in.Namespace}), now, now).Error
	})
	if txErr != nil {
		if errors.Is(txErr, ErrConflict) {
			return nil, ErrConflict
		}
		return nil, txErr
	}
	return s.Get(ctx, in.Namespace)
}

// ImpactTokenTTL is the validity period for two-step confirmation tokens (PRD 10 §4: 300 seconds).
const ImpactTokenTTL = 300 * time.Second

// ImpactAssessment conforms to the OpenAPI SettingsImpactAssessment schema.
type ImpactAssessment struct {
	Namespace              string        `json:"namespace"`
	CurrentVersion         int64         `json:"current_version"`
	ProposedSettingsSha256 string        `json:"proposed_settings_sha256"`
	Impact                 ImpactSummary `json:"impact"`
	ImpactToken            string        `json:"impact_token"`
	ExpiresAt              string        `json:"expires_at"`
}

// ImpactSummary conforms to the OpenAPI SettingsImpactSummary schema.
type ImpactSummary struct {
	Level        string         `json:"level"`
	ChangedPaths []string       `json:"changed_paths"`
	Effects      []ImpactEffect `json:"effects"`
}

// ImpactEffect conforms to the OpenAPI SettingsImpactEffect schema: kind identifies the affected resource category,
// and count is the actual number of currently existing resources that will be affected by the change.
type ImpactEffect struct {
	Kind        string `json:"kind"`
	Count       int64  `json:"count"`
	Consequence string `json:"consequence"`
}

// Assess evaluates change impact and issues a short-lived confirmation token without writing to the database.
// The token binds namespace + current version + normalized proposal hash + admin session + expiration time,
// is single-use (invalidated once version increments), and has TTL=ImpactTokenTTL.
func (s *Service) Assess(ctx context.Context, namespace string, proposed json.RawMessage, actorID string) (*ImpactAssessment, error) {
	if !ValidNamespace(namespace) {
		return nil, ErrNotFound
	}
	normalized, impacts, err := Validate(namespace, proposed)
	if err != nil {
		return nil, err
	}
	var current Row
	err = s.DB.WithContext(ctx).Raw(`
		select version, settings from system_settings where namespace = ?`, storageNamespace(namespace)).Scan(&current).Error
	if err != nil {
		return nil, err
	}
	cur := defaultsOf(namespace)
	if len(current.Settings) > 0 {
		_ = json.Unmarshal(current.Settings, &cur)
	}
	// Uninitialized namespaces participate in token binding at synthetic version 1 (consistent with Get).
	version := current.Version
	if version == 0 {
		version = 1
	}
	summary, err := s.summarize(ctx, namespace, cur, normalized, impacts)
	if err != nil {
		return nil, err
	}
	blob, _ := json.Marshal(normalized)
	tok, expires, terr := s.impactToken(namespace, blob, version, actorID)
	if terr != nil {
		return nil, terr
	}
	sha := sha256Hex(blob)
	return &ImpactAssessment{
		Namespace: namespace, CurrentVersion: version,
		ProposedSettingsSha256: sha, Impact: summary,
		ImpactToken: tok, ExpiresAt: expires.Format(time.RFC3339Nano),
	}, nil
}

// summarize computes changed paths, overall impact level, and live resource effect counts.
// Mapping rules (backed by PRD 10 §3/§4; only PRD-endorsed resource surfaces enter effects):
//   - query.approval_enabled false -> true: active direct-access grants are revoked immediately (revoked,
//     count = unexpired active query_grants); true -> false: existing grants remain unchanged.
//   - execution high-impact fields (restriction_enabled/windows/osc): unstarted scheduled executions
//     are affected immediately (applies_immediately, count = execution_schedules in state='scheduled');
//     already dispatched tasks are not interrupted.
//   - immediate fields (general.registration_enabled/ai-budget.enforced):
//     no existing resources to count (future_operations count=0, applies_immediately).
//   - low-impact fields: recorded only in changed_paths without generating effects entries.
func (s *Service) summarize(ctx context.Context, namespace string, cur, normalized map[string]any, impacts map[string]string) (ImpactSummary, error) {
	summary := ImpactSummary{Level: "low", ChangedPaths: []string{}, Effects: []ImpactEffect{}}
	changed := func(key string, newVal any) bool {
		if cur == nil {
			return true
		}
		old, ok := cur[key]
		return !ok || fmt.Sprint(old) != fmt.Sprint(newVal)
	}
	seenEffect := map[string]bool{}
	addEffect := func(kind, consequence string, count int64) {
		key := kind + "/" + consequence
		if seenEffect[key] {
			return
		}
		seenEffect[key] = true
		summary.Effects = append(summary.Effects, ImpactEffect{
			Kind: kind, Count: count, Consequence: consequence})
	}
	boolVal := func(m map[string]any, key string, def bool) bool {
		if m == nil {
			return def
		}
		v, ok := m[key]
		if !ok {
			return def
		}
		b, ok := v.(bool)
		if !ok {
			return def
		}
		return b
	}
	for k, im := range impacts {
		if !changed(k, normalized[k]) {
			continue
		}
		summary.ChangedPaths = append(summary.ChangedPaths, "/"+k)
		if im == "high" || im == "immediate" {
			summary.Level = "high"
		}
		switch {
		case namespace == NsQuery && k == "approval_enabled":
			count, err := s.countActiveQueryGrants(ctx)
			if err != nil {
				return summary, err
			}
			if boolVal(cur, "approval_enabled", true) {
				// true -> false: existing grants remain valid; approval is simply no longer required.
				addEffect("active_query_grants", "unchanged", count)
			} else {
				// false -> true: direct-access grants are revoked immediately (PRD 10 §3).
				addEffect("active_query_grants", "revoked", count)
			}
		case namespace == NsExecution && (k == "restriction_enabled" || k == "windows" || k == "osc"):
			count, err := s.countScheduledExecutions(ctx)
			if err != nil {
				return summary, err
			}
			addEffect("scheduled_executions", "applies_immediately", count)
		case im == "immediate":
			addEffect("future_operations", "applies_immediately", 0)
		}
	}
	// Deterministic output: map iteration order is randomized, so sort keys to keep assessment/audit/revision
	// projection shapes stable for identical proposals (token binding is unaffected as the normalized blob is already canonical).
	sort.Strings(summary.ChangedPaths)
	sort.Slice(summary.Effects, func(i, j int) bool {
		if summary.Effects[i].Kind != summary.Effects[j].Kind {
			return summary.Effects[i].Kind < summary.Effects[j].Kind
		}
		return summary.Effects[i].Consequence < summary.Effects[j].Consequence
	})
	return summary, nil
}

// countActiveQueryGrants counts currently valid (active and unexpired) direct query access grants.
// Errors are propagated: swallowing errors would misreport high-impact changes as having count=0.
func (s *Service) countActiveQueryGrants(ctx context.Context) (int64, error) {
	var count int64
	if err := s.DB.WithContext(ctx).Raw(`
		select count(*) from query_grants
		where state = 'active' and expires_at > now()`).Scan(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

// countScheduledExecutions counts scheduled executions that have not yet started (already dispatched tasks are unaffected).
func (s *Service) countScheduledExecutions(ctx context.Context) (int64, error) {
	var count int64
	if err := s.DB.WithContext(ctx).Raw(`
		select count(*) from execution_schedules where state = 'scheduled'`).Scan(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

// impactToken generates a short-lived confirmation token (derived from the HMAC keyring fingerprint key);
// binds namespace + current version + normalized proposal + admin session + expiration time.
// F12 #014: HMAC failure no longer silently returns an empty token (which violated contract minLength 32
// and hid missing underlying keys) -- fails closed by returning a system capacity error.
func (s *Service) impactToken(namespace string, blob []byte, version int64, actorID string) (string, time.Time, error) {
	expires := s.Now().Add(ImpactTokenTTL)
	mac, err := s.Crypter.HMAC("settings_impact_token", impactBlob(namespace, blob, version, actorID, expires))
	if err != nil {
		return "", expires, fmt.Errorf("settings: impact token issuance unavailable: %w", err)
	}
	return hex.EncodeToString(mac[:16]) + "." + fmt.Sprint(expires.Unix()), expires, nil
}

// VerifyImpactToken verifies a confirmation token (time window + bound namespace + version + proposal + session).
// Version binding makes tokens single-use: once a successful PUT increments the version, replaying the same token against the old version fails.
func (s *Service) VerifyImpactToken(token, namespace string, proposed json.RawMessage, version int64, actorID string) error {
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return ErrTokenInvalid
	}
	var expUnix int64
	if _, err := fmt.Sscanf(parts[1], "%d", &expUnix); err != nil {
		return ErrTokenInvalid
	}
	expires := time.Unix(expUnix, 0)
	if s.Now().After(expires) || expires.Sub(s.Now()) > ImpactTokenTTL+time.Minute {
		return ErrTokenInvalid
	}
	var normalized map[string]any
	if len(proposed) > 0 {
		if err := json.Unmarshal(proposed, &normalized); err != nil {
			return ErrTokenInvalid
		}
	}
	blob, _ := json.Marshal(normalized)
	mac, err := s.Crypter.HMAC("settings_impact_token", impactBlob(namespace, blob, version, actorID, expires))
	if err != nil {
		return ErrTokenInvalid
	}
	if !hmac.Equal([]byte(hex.EncodeToString(mac[:16])), []byte(parts[0])) {
		return ErrTokenInvalid
	}
	return nil
}

// impactBlob builds the token HMAC input: a JSON-array-encoded binding tuple (free of delimiter ambiguity,
// with the proposal blob normalized via base64).
func impactBlob(namespace string, blob []byte, version int64, actorID string, expires time.Time) []byte {
	binding, _ := json.Marshal([]any{namespace, version, blob, actorID, expires.Unix()})
	return binding
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Revision is a settings revision projection (sourced from audit_events settings.replaced; conforms to OpenAPI
// SettingsRevision: changed_by is the actor UUID, and changed_paths/impact_summary come from event metadata).
type Revision struct {
	Namespace     string         `json:"namespace"`
	Version       int64          `json:"version"`
	ChangedBy     string         `json:"changed_by"`
	ChangedAt     time.Time      `json:"changed_at"`
	ChangedPaths  []string       `json:"changed_paths"`
	ImpactSummary *ImpactSummary `json:"impact_summary"`
}

// revisionRow is the row projection for ListRevisions.
type revisionRow struct {
	ID            string          `gorm:"column:id"`
	ActorID       string          `gorm:"column:actor_id"`
	At            time.Time       `gorm:"column:occurred_at"`
	Version       int64           `gorm:"column:version"`
	ChangedPaths  json.RawMessage `gorm:"column:changed_paths"`
	ImpactSummary json.RawMessage `gorm:"column:impact_summary"`
}

// ListRevisions returns settings revision history (typed projection over audit_events in a Page envelope).
// The after cursor is the audit event ID at the end of the previous page.
func (s *Service) ListRevisions(ctx context.Context, namespace string, limit int, after string) ([]Revision, *string, error) {
	if !ValidNamespace(namespace) {
		return nil, nil, ErrNotFound
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	q := `
		select id, actor_id, occurred_at, (metadata->>'version')::bigint as version,
		       metadata->'changed_paths' as changed_paths,
		       metadata->'impact_summary' as impact_summary
		from audit_events
		where event_type = 'settings.replaced' and metadata->>'namespace' = ?`
	args := []any{namespace}
	if after != "" {
		var cursor struct {
			At time.Time `gorm:"column:occurred_at"`
		}
		if err := s.DB.WithContext(ctx).Raw(`
			select occurred_at from audit_events
			where id = ? and event_type = 'settings.replaced' and metadata->>'namespace' = ?`,
			after, namespace).Scan(&cursor).Error; err != nil {
			return nil, nil, err
		}
		if cursor.At.IsZero() {
			return nil, nil, ErrNotFound
		}
		q += ` and (occurred_at, id) < (?, ?)`
		args = append(args, cursor.At, after)
	}
	q += ` order by occurred_at desc, id desc limit ?`
	args = append(args, limit+1)
	var rows []revisionRow
	if err := s.DB.WithContext(ctx).Raw(q, args...).Scan(&rows).Error; err != nil {
		return nil, nil, err
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	out := make([]Revision, 0, len(rows))
	for _, r := range rows {
		rev := Revision{Namespace: namespace, Version: r.Version, ChangedBy: r.ActorID,
			ChangedAt: r.At, ChangedPaths: []string{}}
		_ = json.Unmarshal(r.ChangedPaths, &rev.ChangedPaths)
		if len(r.ImpactSummary) > 0 {
			var summary ImpactSummary
			if err := json.Unmarshal(r.ImpactSummary, &summary); err == nil {
				if summary.ChangedPaths == nil {
					summary.ChangedPaths = []string{}
				}
				if summary.Effects == nil {
					summary.Effects = []ImpactEffect{}
				}
				rev.ImpactSummary = &summary
			}
		}
		out = append(out, rev)
	}
	var next *string
	if hasMore && len(rows) > 0 {
		next = &rows[len(rows)-1].ID
	}
	return out, next, nil
}

// SettingsSchemaView conforms to OpenAPI SettingsSchema: json_schema and ui_hints
// are generated from the same frozen field table as Validate (no duplicate field inventory).
type SettingsSchemaView struct {
	Namespace     string          `json:"namespace"`
	SchemaVersion int             `json:"schema_version"`
	JSONSchema    json.RawMessage `json:"json_schema"`
	UIHints       map[string]any  `json:"ui_hints"`
}

// SchemaView builds the schema view for a namespace (schema_version is taken from the current row, or 1 if uninitialized).
func (s *Service) SchemaView(ctx context.Context, namespace string) (*SettingsSchemaView, error) {
	schema, err := SchemaJSON(namespace)
	if err != nil {
		return nil, err
	}
	specs, ok := namespaces[namespace]
	if !ok {
		return nil, ErrNotFound
	}
	hints := map[string]any{}
	for name, spec := range specs {
		hints[name] = map[string]any{"impact": spec.impact, "default": spec.def}
	}
	schemaVersion := 1
	var row struct {
		SchemaVersion int `gorm:"column:schema_version"`
	}
	if err := s.DB.WithContext(ctx).Raw(`
		select schema_version from system_settings where namespace = ?`,
		namespace).Scan(&row).Error; err != nil {
		return nil, err
	}
	if row.SchemaVersion > 0 {
		schemaVersion = row.SchemaVersion
	}
	return &SettingsSchemaView{
		Namespace: namespace, SchemaVersion: schemaVersion,
		JSONSchema: json.RawMessage(schema), UIHints: hints,
	}, nil
}

func defaultsOf(namespace string) map[string]any {
	out := map[string]any{}
	for k, spec := range namespaces[namespace] {
		out[k] = spec.def
	}
	return out
}

func zeroUUID() string { return "00000000-0000-0000-0000-000000000000" }

func jsonOrEmpty(m map[string]any) string {
	b, err := json.Marshal(m)
	if err != nil {
		return "{}"
	}
	return string(b)
}

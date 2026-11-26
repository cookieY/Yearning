// prompttools.go Prompt Tool (review skill) management: CRUD, three-field definition,
// config_hash, enablement gate, and deletion guard (PRD 9.3.1).
//
// Operable only by builtin_admin (guarded at the HTTP layer); regular users only see
// name/purpose/Hash in Snapshots. Three definition fields: knowledge_text, max_severity,
// and version; max_severity is retained solely for v1 compatibility and never lowers effective Critical on mandatory rules.
package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/platform/pagecursor"
	"github.com/cookieY/YearningX/backend/internal/platform/txutil"
	"github.com/cookieY/YearningX/backend/internal/review/agent"
	"github.com/cookieY/YearningX/backend/internal/review/domain"
)

// PromptToolDefinition holds the three-field definition (runtime contract
// prompt_tool_definition_fields).
type PromptToolDefinition struct {
	KnowledgeText string `json:"knowledge_text"`
	MaxSeverity   string `json:"max_severity"`
	Version       int    `json:"version"`
}

// ValidMaxSeverity checks the severity level enum.
func ValidMaxSeverity(v string) bool {
	switch v {
	case "info", "low", "medium", "high", "critical":
		return true
	}
	return false
}

// PromptToolView is the admin read view (full definition visible: admin only).
type PromptToolView struct {
	ID          string               `json:"id"`
	Name        string               `json:"name"`
	Description string               `json:"description"`
	State       string               `json:"state"`
	Engine      string               `json:"engine"`
	Parameters  map[string]any       `json:"parameters"`
	Definition  PromptToolDefinition `json:"definition"`
	ConfigHash  string               `json:"config_hash"`
	Version     int64                `json:"version"`
	IsBuiltin   bool                 `json:"is_builtin"`
	CreatedAt   time.Time            `json:"created_at"`
	UpdatedAt   time.Time            `json:"updated_at"`
}

// MaxPromptToolDescriptionRunes caps skill description length (section 18.80): counted in Unicode runes,
// matching the OpenAPI maxLength convention.
const MaxPromptToolDescriptionRunes = 100

// ValidPromptToolDescription validates description constraints: nullable, <= 100 runes. Pure display
// metadata — does not participate in config_hash or definition hash (editing description does not invalidate unsubmitted Reviews).
func ValidPromptToolDescription(description string) bool {
	return utf8.RuneCountInString(description) <= MaxPromptToolDescriptionRunes
}

// ValidPromptToolEngine validates the database engine targeted by a skill (B8: multi-database support).
func ValidPromptToolEngine(e string) bool {
	return e == "all" || e == "mysql" || e == "postgresql"
}

// ErrReferencedTool 1006: referenced by a rule set or Review; physical deletion is forbidden.
var ErrReferencedTool = errors.New("review: prompt tool referenced")

// ValidatePromptToolDefinition validates constraints on the three definition fields (isomorphic to knowledge.Definition).
func ValidatePromptToolDefinition(d PromptToolDefinition) error {
	if strings.TrimSpace(d.KnowledgeText) == "" || len(d.KnowledgeText) > 64*1024 {
		return &domain.Error{Code: 1001, Name: "VALIDATION_FAILED"}
	}
	if !ValidMaxSeverity(d.MaxSeverity) {
		return &domain.Error{Code: 1001, Name: "VALIDATION_FAILED"}
	}
	if d.Version < 1 {
		return &domain.Error{Code: 1001, Name: "VALIDATION_FAILED"}
	}
	return nil
}

// promptToolConfigHash computes the immutable configuration hash (canonical JSON of name + definition).
func promptToolConfigHash(name, engine string, params map[string]any, d PromptToolDefinition) (string, error) {
	payload, err := json.Marshal(map[string]any{
		"name": name, "engine": engine, "parameters": normalizeParams(params), "definition": d,
	})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

// promptToolDefinitionHash computes the definition-surface hash (excluding parameters): used when
// replacing built-in rows to enforce "definition is immutable" without rejecting parameter adjustments.
func promptToolDefinitionHash(name, engine string, d PromptToolDefinition) (string, error) {
	payload, err := json.Marshal(map[string]any{"name": name, "engine": engine, "definition": d})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

// normalizeParams normalizes parameters (converting float64 integers decoded from jsonb back to int64
// to guarantee deterministic hashing and validation).
func normalizeParams(params map[string]any) map[string]any {
	out := make(map[string]any, len(params))
	for k, v := range params {
		if f, ok := v.(float64); ok && f == float64(int64(f)) {
			out[k] = int64(f)
			continue
		}
		out[k] = v
	}
	return out
}

// validatePromptToolParameters validates parameter constraints: key count and naming, value types
// (positive integer / short string / boolean / short string array), and bounds.
func validatePromptToolParameters(params map[string]any) error {
	if len(params) > 16 {
		return &domain.Error{Code: 1001, Name: "VALIDATION_FAILED"}
	}
	for k, v := range params {
		if !regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`).MatchString(k) {
			return &domain.Error{Code: 1001, Name: "VALIDATION_FAILED"}
		}
		switch t := v.(type) {
		case float64:
			if t != float64(int64(t)) || t < 1 || t > 1e15 {
				return &domain.Error{Code: 1001, Name: "VALIDATION_FAILED"}
			}
		case int64:
			if t < 1 || t > 1e15 {
				return &domain.Error{Code: 1001, Name: "VALIDATION_FAILED"}
			}
		case int:
			if int64(t) < 1 || int64(t) > 1e15 {
				return &domain.Error{Code: 1001, Name: "VALIDATION_FAILED"}
			}
		case string:
			if t == "" || len(t) > 128 {
				return &domain.Error{Code: 1001, Name: "VALIDATION_FAILED"}
			}
		case bool:
		case []string:
			if len(t) > 16 {
				return &domain.Error{Code: 1001, Name: "VALIDATION_FAILED"}
			}
			for _, item := range t {
				if item == "" || len(item) > 64 {
					return &domain.Error{Code: 1001, Name: "VALIDATION_FAILED"}
				}
			}
		case []any:
			if len(t) > 16 {
				return &domain.Error{Code: 1001, Name: "VALIDATION_FAILED"}
			}
			for _, item := range t {
				str, ok := item.(string)
				if !ok || str == "" || len(str) > 64 {
					return &domain.Error{Code: 1001, Name: "VALIDATION_FAILED"}
				}
			}
		default:
			return &domain.Error{Code: 1001, Name: "VALIDATION_FAILED"}
		}
	}
	return nil
}

// paramsTypeOf returns the parameter value type tag (used for built-in parameter type consistency checks).
func paramsTypeOf(v any) string {
	switch v.(type) {
	case float64, int64, int:
		return "number"
	case string:
		return "string"
	case bool:
		return "bool"
	case []string, []any:
		return "strings"
	}
	return "unknown"
}

// RunPromptToolEval runs the enablement gate (Schema subset + privacy + injection + severity consistency).
// params participate in rendering (placeholder injection scanning runs on the parameterized text).
func RunPromptToolEval(name string, params map[string]any, d PromptToolDefinition) error {
	if err := agent.ValidatePolicyParameters(d.KnowledgeText, params); err != nil {
		return errors.New("privacy: unresolved object reference or parameter")
	}
	if err := ValidatePromptToolDefinition(d); err != nil {
		return fmt.Errorf("definition: %w", err)
	}
	rendered := renderPromptToolInjection(name, params, d)
	for _, p := range privacyScanPatterns {
		if p.MatchString(rendered) {
			return errors.New("privacy: credential-like pattern in knowledge_text")
		}
	}
	for _, p := range injectionScanPatterns {
		if p.MatchString(rendered) {
			return errors.New("injection: instruction-override pattern in knowledge_text")
		}
	}
	return nil
}

var (
	privacyScanPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\b[a-z0-9._%+-]+:[^@\s]{4,}@[a-z0-9.-]+\.[a-z]{2,}`),
		regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{16,}`),
		regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{20,}\.`),
		regexp.MustCompile(`(?i)\b(password|passwd|secret|api[_-]?key|token)\s*[:=]\s*\S{8,}`),
		regexp.MustCompile(`\b[0-9a-f]{32,}\b`),
	}
	injectionScanPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)ignore\s+(all\s+)?(previous|prior|above)\s+(instructions|rules|prompts)`),
		regexp.MustCompile(`(?i)disregard\s+(the\s+)?(previous|above|system)`),
		regexp.MustCompile(`(?i)forget\s+(everything|all|previous|your)`),
		regexp.MustCompile(`(?i)you\s+are\s+now\b`),
		regexp.MustCompile(`(?i)(system|assistant)\s*:\s*`),
		regexp.MustCompile(`(?i)override\s+(the\s+)?(system|instructions|rules)`),
		regexp.MustCompile("(?i)</?(system|assistant|instruction|prompt)>"),
	}
)

// renderPromptToolInjection renders the injection text (delimited consistently with package agent).
func renderPromptToolInjection(name string, params map[string]any, d PromptToolDefinition) string {
	var b strings.Builder
	b.WriteString("<audit-skill name=\"")
	b.WriteString(strings.ReplaceAll(name, "\"", "&quot;"))
	b.WriteString("\">\nrule: ")
	b.WriteString(agent.RenderParameters(strings.TrimSpace(d.KnowledgeText), params))
	b.WriteString("\neffective_severity: critical (every actual violation of this mandatory rule is Critical)")
	b.WriteString("\nlegacy_max_severity_compatibility: info..")
	b.WriteString(d.MaxSeverity)
	b.WriteString(" (compatibility metadata only; it does not lower or authorize a mandatory rule violation)")
	b.WriteString("\n</audit-skill>")
	return b.String()
}

// CreatePromptTool creates a draft skill (engine=all; compatible with existing B7 signature,
// while engine-specific creation goes through CreatePromptToolForEngine).
func (s *Services) CreatePromptTool(ctx context.Context, actorUserID, name, state string, def PromptToolDefinition) (*PromptToolView, error) {
	return s.CreatePromptToolForEngine(ctx, actorUserID, name, state, "all", nil, def, "")
}

// CreatePromptToolForEngine creates a skill (including tunable parameters). engine restricts the target
// database engine (all|mysql|postgresql). description is pure display metadata (section 18.80)
// and does not participate in config_hash.
func (s *Services) CreatePromptToolForEngine(ctx context.Context, actorUserID, name, state, engine string, params map[string]any, def PromptToolDefinition, description string) (*PromptToolView, error) {
	if name == "" {
		return nil, &domain.Error{Code: 1001, Name: "VALIDATION_FAILED"}
	}
	if !ValidPromptToolDescription(description) {
		return nil, &domain.Error{Code: 1001, Name: "VALIDATION_FAILED"}
	}
	if state == "" {
		state = "draft"
	}
	if state != "draft" && state != "enabled" && state != "disabled" {
		return nil, &domain.Error{Code: 1001, Name: "VALIDATION_FAILED"}
	}
	if engine == "" {
		engine = "all"
	}
	if !ValidPromptToolEngine(engine) {
		return nil, &domain.Error{Code: 1001, Name: "VALIDATION_FAILED"}
	}
	if err := validatePromptToolParameters(params); err != nil {
		return nil, err
	}
	if err := ValidatePromptToolDefinition(def); err != nil {
		return nil, err
	}
	if state == "enabled" {
		if err := RunPromptToolEval(name, params, def); err != nil {
			return nil, &domain.Error{Code: 1001, Name: "EVAL_GATE_FAILED"}
		}
	}
	hash, err := promptToolConfigHash(name, engine, params, def)
	if err != nil {
		return nil, err
	}
	defJSON, err := json.Marshal(def)
	if err != nil {
		return nil, err
	}
	paramsJSON, err := json.Marshal(normalizeParams(params))
	if err != nil {
		return nil, err
	}
	id := newID()
	findingKey := "skill." + id
	now := nowUTC()
	res := s.DB.WithContext(ctx).Exec(`
		insert into prompt_tools (id, name, description, state, engine, parameters, definition, config_hash, version, finding_key, created_by_user_id, created_at, updated_at)
		values (?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?)`,
		id, name, description, state, engine, paramsJSON, defJSON, hash, findingKey, actorUserID, now, now)
	if res.Error != nil {
		if txutil.IsUniqueViolation(res.Error) {
			return nil, &domain.Error{Code: 1005, Name: "RESOURCE_ALREADY_EXISTS"}
		}
		return nil, res.Error
	}
	return &PromptToolView{ID: id, Name: name, Description: description, State: state, Engine: engine, Parameters: normalizeParams(params), Definition: def,
		ConfigHash: hash, Version: 1, CreatedAt: now, UpdatedAt: now}, nil
}

// promptToolRow maps a database row.
type promptToolRow struct {
	ID          string
	Name        string
	Description string
	State       string
	Engine      string
	Parameters  []byte
	Definition  []byte
	ConfigHash  string
	Version     int64
	IsBuiltin   bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

const promptToolColumns = "id, name, description, state, engine, parameters, definition, config_hash, version, is_builtin, created_at, updated_at"

func promptToolView(r promptToolRow) *PromptToolView {
	def := PromptToolDefinition{}
	_ = json.Unmarshal(r.Definition, &def)
	engine := r.Engine
	if engine == "" {
		engine = "all"
	}
	params := map[string]any{}
	if len(r.Parameters) > 0 {
		_ = json.Unmarshal(r.Parameters, &params)
	}
	params = normalizeParams(params)
	return &PromptToolView{ID: r.ID, Name: r.Name, Description: r.Description, State: r.State, Engine: engine, Parameters: params, Definition: def,
		ConfigHash: r.ConfigHash, Version: r.Version, IsBuiltin: r.IsBuiltin,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
}

// ListPromptTools paginates skills (limit+1 prefetch; returns next-page cursor; search = name substring, section 18.73).
func (s *Services) ListPromptTools(ctx context.Context, limit int, after, search string) ([]*PromptToolView, *string, error) {
	q := s.DB.WithContext(ctx).Table("prompt_tools")
	if after != "" {
		created, id, err := pagecursor.DecodeUUID(after)
		if err != nil {
			return nil, nil, ErrValidation
		}
		q = q.Where("(created_at, id) < (?, ?)", created, id)
	}
	if search != "" {
		q = q.Where("name ILIKE ?", "%"+search+"%")
	}
	var rows []promptToolRow
	if err := q.Select(promptToolColumns).
		Order("created_at desc").Order("id desc").Limit(limit + 1).Scan(&rows).Error; err != nil {
		return nil, nil, err
	}
	var next *string
	if len(rows) > limit {
		rows = rows[:limit]
		v := pagecursor.Encode(rows[len(rows)-1].CreatedAt, rows[len(rows)-1].ID)
		next = &v
	}
	out := make([]*PromptToolView, 0, len(rows))
	for i := range rows {
		out = append(out, promptToolView(rows[i]))
	}
	return out, next, nil
}

// GetPromptTool fetches a skill by ID.
func (s *Services) GetPromptTool(ctx context.Context, id string) (*PromptToolView, error) {
	var row promptToolRow
	err := s.DB.WithContext(ctx).Table("prompt_tools").
		Select(promptToolColumns).
		Where("id = ?", id).Scan(&row).Error
	if err != nil {
		return nil, err
	}
	if row.ID == "" {
		return nil, ErrNotFound
	}
	return promptToolView(row), nil
}

// ReplacePromptTool performs optimistic-lock replacement (engine=all compatibility wrapper).
func (s *Services) ReplacePromptTool(ctx context.Context, id string, expectVersion int64,
	name, state string, def PromptToolDefinition) (*PromptToolView, error) {
	// Omitted engine/params/description = reuse persisted row values (so toggling state on a mysql row or
	// a parameterized row does not wipe them).
	return s.ReplacePromptToolForEngine(ctx, id, expectVersion, name, state, "", nil, def, "")
}

// ReplacePromptToolForEngine performs optimistic-lock replacement; changing enabled runs through the
// gate; a Hash change invalidates unsubmitted Runs referencing the old Hash. Three-part governance for
// built-in rows: the definition surface (name/engine/definition) is immutable (rejected on definition
// hash mismatch), parameters are tunable (key set and types must match the persisted row while values
// are free), and enabled state is toggleable. description (section 18.80) participates in no hash:
// non-built-in rows write it with full-replacement semantics (empty string = clear), whereas built-in
// row descriptions are owned by system seed data and ignore payload values.
// Empty engine / nil params mean "reuse current persisted values" (used by the B7 compatibility wrapper
// and pure state-toggle paths to avoid hashing a mysql row as all or wiping parameters on parameterized rows).
func (s *Services) ReplacePromptToolForEngine(ctx context.Context, id string, expectVersion int64,
	name, state, engine string, params map[string]any, def PromptToolDefinition, description string) (*PromptToolView, error) {
	if engine != "" && !ValidPromptToolEngine(engine) {
		return nil, &domain.Error{Code: 1001, Name: "VALIDATION_FAILED"}
	}
	if !ValidPromptToolDescription(description) {
		return nil, &domain.Error{Code: 1001, Name: "VALIDATION_FAILED"}
	}
	if params != nil {
		if err := validatePromptToolParameters(params); err != nil {
			return nil, err
		}
	}
	var out *PromptToolView
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row promptToolRow
		if err := tx.WithContext(ctx).Table("prompt_tools").
			Select(promptToolColumns).
			Where("id = ? for update", id).Scan(&row).Error; err != nil {
			return err
		}
		if row.ID == "" {
			return ErrNotFound
		}
		if row.Version != expectVersion {
			return ErrVersionConflict
		}
		if state != "draft" && state != "enabled" && state != "disabled" {
			return &domain.Error{Code: 1001, Name: "VALIDATION_FAILED"}
		}
		if err := ValidatePromptToolDefinition(def); err != nil {
			return err
		}
		rowEngine := row.Engine
		if rowEngine == "" {
			rowEngine = "all"
		}
		if engine == "" {
			engine = rowEngine // Reuse persisted row value when omitted
		}
		rowParams := map[string]any{}
		if len(row.Parameters) > 0 {
			_ = json.Unmarshal(row.Parameters, &rowParams)
		}
		rowParams = normalizeParams(rowParams)
		if params == nil {
			params = rowParams // Reuse persisted row value when omitted
		}
		// newHash must be computed after resolving defaulted engine/params: config_hash always equals
		// the hash recomputed from the persisted row data (high-severity defect caught during closure verification).
		newHash, err := promptToolConfigHash(name, engine, params, def)
		if err != nil {
			return err
		}
		// B8 ruling (three-part governance): the definition surface (name/engine/definition) of built-in rows
		// is immutable — verified by comparing the definition hash (excluding parameters); parameter key sets
		// and types must match the persisted row (values are tunable so users can customize thresholds); state is toggleable.
		if row.IsBuiltin {
			rowDef := PromptToolDefinition{}
			if err := json.Unmarshal(row.Definition, &rowDef); err != nil {
				return err
			}
			rowDefHash, err := promptToolDefinitionHash(row.Name, rowEngine, rowDef)
			if err != nil {
				return err
			}
			payloadDefHash, err := promptToolDefinitionHash(name, engine, def)
			if err != nil {
				return err
			}
			if rowDefHash != payloadDefHash {
				return &domain.Error{Code: 1001, Name: "VALIDATION_FAILED"}
			}
			next := normalizeParams(params)
			if len(next) != len(rowParams) {
				return &domain.Error{Code: 1001, Name: "VALIDATION_FAILED"}
			}
			for k, v := range rowParams {
				nv, ok := next[k]
				if !ok || paramsTypeOf(nv) != paramsTypeOf(v) {
					return &domain.Error{Code: 1001, Name: "VALIDATION_FAILED"}
				}
			}
		}
		if state == "enabled" && (row.State != "enabled" || newHash != row.ConfigHash) {
			if err := RunPromptToolEval(name, params, def); err != nil {
				return &domain.Error{Code: 1001, Name: "EVAL_GATE_FAILED"}
			}
		}
		// section 18.80: built-in row descriptions are owned by system seed data — payload description is always
		// ignored and the persisted value is retained; non-built-in rows write with full-replacement semantics.
		if row.IsBuiltin {
			description = row.Description
		}
		defJSON, err := json.Marshal(def)
		if err != nil {
			return err
		}
		now := nowUTC()
		paramsJSON, err := json.Marshal(normalizeParams(params))
		if err != nil {
			return err
		}
		res := tx.WithContext(ctx).Exec(`
			update prompt_tools set name = ?, description = ?, state = ?, engine = ?, parameters = ?, definition = ?, config_hash = ?,
				version = version + 1, updated_at = ?
			where id = ? and version = ?`, name, description, state, engine, paramsJSON, defJSON, newHash, now, id, expectVersion)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return ErrVersionConflict
		}
		// Any definition/parameter change (hash change) or enabled-state change alters the semantics of frozen
		// injections — invalidating all unsubmitted Runs referencing the old configuration (R006). description
		// changes do not enter this branch (participating in no hash and altering no frozen semantics).
		if newHash != row.ConfigHash || state != row.State {
			if err := InvalidateInputRefs(ctx, tx, "prompt_tool", id, row.ConfigHash); err != nil {
				return err
			}
		}
		out = &PromptToolView{ID: id, Name: name, Description: description, State: state, Engine: engine, Parameters: normalizeParams(params), Definition: def,
			ConfigHash: newHash, Version: expectVersion + 1, IsBuiltin: row.IsBuiltin,
			CreatedAt: row.CreatedAt, UpdatedAt: now}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// DeletePromptTool deletes a skill: rejects built-in skills and skills referenced by a rule set or Review.
func (s *Services) DeletePromptTool(ctx context.Context, id string, expectVersion int64) error {
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var isBuiltin bool
		if err := tx.WithContext(ctx).Raw(
			`select is_builtin from prompt_tools where id = ?`, id).Row().Scan(&isBuiltin); err != nil {
			return err
		}
		if isBuiltin {
			return &domain.Error{Code: 1001, Name: "VALIDATION_FAILED"}
		}
		var n int64
		if err := tx.WithContext(ctx).Raw(
			`select count(*) from rule_set_prompt_tools where prompt_tool_id = ?`, id).Scan(&n).Error; err != nil {
			return err
		}
		if n > 0 {
			return ErrReferencedTool
		}
		referenced, err := InputRefsExist(ctx, tx, "prompt_tool", id)
		if err != nil {
			return err
		}
		if referenced {
			return ErrReferencedTool
		}
		res := tx.WithContext(ctx).Exec(
			`delete from prompt_tools where id = ? and version = ?`, id, expectVersion)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			// C1004-only profile: stale If-Match surfaces as concurrent
			// modification, identical to the tx-level race.
			return ErrConcurrent
		}
		return nil
	})
}

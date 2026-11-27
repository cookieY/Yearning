// service.go implements internal knowledge entry lifecycle management: create, optimistic-lock replace,
// enablement evaluation gate (schema, privacy scan, injection check, severity), and reference invalidation.
package knowledge

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/platform/ptr"
	"github.com/cookieY/YearningX/backend/internal/review/privacy"
	"github.com/google/uuid"
)

// Invalidator marks unsubmitted Reviews stale when an enabled entry is modified or disabled.
type Invalidator func(ctx context.Context, tx *gorm.DB, toolKind string, inputRefID, oldHash string) error

// KindName identifies knowledge entries in review_tool_calls.tool_kind.
const KindName = "knowledge_entry"

// Service executes knowledge entry application workflows and enablement gates.
type Service struct {
	Repo       *Repository
	Invalidate Invalidator // Optional in unit tests; invalidates unsubmitted Reviews on hash change.
}

// EvalReport summarizes the four validation checks executed before enabling a knowledge entry.
type EvalReport struct {
	Pass           bool     `json:"pass"`
	SchemaSubsetOK bool     `json:"schema_subset_ok"`
	PrivacyOK      bool     `json:"privacy_ok"`
	InjectionOK    bool     `json:"injection_ok"`
	SeverityOK     bool     `json:"severity_ok"`
	Findings       []string `json:"findings,omitempty"`
	CheckedAt      string   `json:"checked_at"`
}

// CreateInput holds parameters for creating a new knowledge entry.
type CreateInput struct {
	Name            string
	Purpose         string
	State           State
	ScopeType       ScopeType
	DatasourceID    string
	DatabaseName    string
	TableName       string
	Definition      Definition
	Provenance      Provenance
	SourceFindingID string
}

func prepareNewEntry(actorUserID string, in CreateInput) (*Entry, error) {
	state := in.State
	if state == "" {
		state = StateDraft
	}
	e := &Entry{
		ID: uuid.NewString(), Name: in.Name, Purpose: in.Purpose,
		State: state, ScopeType: in.ScopeType,
		DatasourceID: in.DatasourceID, DatabaseName: in.DatabaseName, TableName: in.TableName,
		Definition: in.Definition, Version: 1,
		Provenance: in.Provenance, SourceFindingID: in.SourceFindingID,
		CreatedByUserID: actorUserID,
		CreatedAt:       time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if e.Provenance == "" {
		e.Provenance = ProvenanceManual
	}
	e.FindingKey = "exp." + e.ID
	if err := e.Validate(); err != nil {
		return nil, err
	}
	if e.State == StateEnabled {
		report := RunEval(e)
		if !report.Pass {
			return nil, fmt.Errorf("%w: eval gate failed: %s", ErrValidation, strings.Join(report.Findings, "; "))
		}
	}
	hash, err := ComputeConfigHash(e)
	if err != nil {
		return nil, err
	}
	e.ConfigHash = hash
	return e, nil
}

// Create validates and inserts a knowledge entry (running RunEval when created directly in enabled state).
func (s *Service) Create(ctx context.Context, actorUserID string, in CreateInput) (*Entry, error) {
	e, err := prepareNewEntry(actorUserID, in)
	if err != nil {
		return nil, err
	}
	if err := s.Repo.Insert(ctx, s.Repo.DB, e); err != nil {
		return nil, err
	}
	return e, nil
}

// ReplaceInput holds optimistic-lock replacement fields for updating a knowledge entry.
type ReplaceInput struct {
	ExpectVersion int64
	Name          string
	Purpose       string
	State         State
	ScopeType     ScopeType
	DatasourceID  string
	DatabaseName  string
	TableName     string
	Definition    Definition
}

// Replace atomically updates an entry under optimistic locking, enforces RunEval on enablement,
// and triggers Invalidator if an enabled entry's config_hash or active state changed.
func (s *Service) Replace(ctx context.Context, id string, in ReplaceInput) (*Entry, error) {
	var out *Entry
	err := s.Repo.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		e, err := s.Repo.LoadForUpdate(ctx, tx, id)
		if err != nil {
			return err
		}
		if e.Version != in.ExpectVersion {
			return ErrVersionConflict
		}
		oldState := e.State
		oldHash := e.ConfigHash
		oldVersion := e.Version
		*e = Entry{
			ID: e.ID, Name: in.Name, Purpose: in.Purpose, State: in.State,
			ScopeType: in.ScopeType, DatasourceID: in.DatasourceID,
			DatabaseName: in.DatabaseName, TableName: in.TableName,
			Definition: in.Definition, Version: e.Version + 1,
			FindingKey: e.FindingKey,
			Provenance: e.Provenance, SourceFindingID: e.SourceFindingID,
			CreatedByUserID: e.CreatedByUserID,
			CreatedAt:       e.CreatedAt, UpdatedAt: time.Now().UTC(),
		}
		if err := e.Validate(); err != nil {
			return err
		}
		hash, err := ComputeConfigHash(e)
		if err != nil {
			return err
		}
		// Run enablement gate when enabling or updating an already-enabled entry's content.
		if in.State == StateEnabled && (oldState != StateEnabled || hash != oldHash) {
			report := RunEval(e)
			if !report.Pass {
				return fmt.Errorf("%w: eval gate failed: %s", ErrValidation, strings.Join(report.Findings, "; "))
			}
		}
		e.ConfigHash = hash
		defJSON, err := json.Marshal(e.Definition)
		if err != nil {
			return err
		}
		sets := map[string]any{
			"name": e.Name, "purpose": e.Purpose, "state": string(e.State),
			"scope_type":    string(e.ScopeType),
			"datasource_id": ptr.NonZero(e.DatasourceID), "database_name": ptr.NonZero(e.DatabaseName),
			"table_name": ptr.NonZero(e.TableName), "definition": defJSON,
			"config_hash": e.ConfigHash, "version": e.Version, "updated_at": e.UpdatedAt,
		}
		if err := s.Repo.UpdateVersioned(ctx, tx, e, oldVersion, map[string]any{
			"name": sets["name"], "purpose": sets["purpose"], "state": sets["state"],
			"scope_type": sets["scope_type"], "datasource_id": sets["datasource_id"],
			"database_name": sets["database_name"], "table_name": sets["table_name"],
			"definition": sets["definition"], "config_hash": sets["config_hash"],
			"version": e.Version, "updated_at": e.UpdatedAt,
		}); err != nil {
			return err
		}
		// Invalidate unsubmitted Reviews if an enabled entry's config hash changed or was disabled.
		if s.Invalidate != nil && shouldInvalidateOldReferences(oldState, in.State, oldHash, hash) {
			if err := s.Invalidate(ctx, tx, KindName, e.ID, oldHash); err != nil {
				return err
			}
		}
		out = e
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func shouldInvalidateOldReferences(oldState, newState State, oldHash, newHash string) bool {
	return oldState == StateEnabled && (newState != StateEnabled || oldHash != newHash)
}

func shouldInvalidateOnDelete(state State) bool {
	return state == StateEnabled
}

// Delete removes a knowledge entry guarded by optimistic version matching.
// Historical review_tool_calls and submitted order snapshots retain immutable copies,
// so prior references do not block physical deletion; deleting an enabled entry
// invalidates referencing unsubmitted review runs in the same transaction.
func (s *Service) Delete(ctx context.Context, id string, expectVersion int64) error {
	return s.Repo.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		e, err := s.Repo.LoadForUpdate(ctx, tx, id)
		if err != nil {
			return err
		}
		if e.Version != expectVersion {
			return ErrConcurrent
		}
		if err := s.Repo.DeleteVersioned(ctx, tx, id, expectVersion); err != nil {
			return err
		}
		if s.Invalidate != nil && shouldInvalidateOnDelete(e.State) {
			if err := s.Invalidate(ctx, tx, KindName, e.ID, e.ConfigHash); err != nil {
				return err
			}
		}
		return nil
	})
}

// Credential and secret patterns rejected from knowledge rule bodies before prompt injection.
var privacyPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b[a-z0-9._%+-]+:[^@\s]{4,}@[a-z0-9.-]+\.[a-z]{2,}`), // user:pass@host
	regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{16,}`),                                // API key prefix
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{20,}\.`),                              // JWT token header
	regexp.MustCompile(`(?i)\b(password|passwd|secret|api[_-]?key|token)\s*[:=]\s*\S{8,}`),
	regexp.MustCompile(`\b[0-9a-f]{32,}\b`), // long hex credential/hash string
}

// Prompt-injection patterns rejected from knowledge rule bodies.
var injectionPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)ignore\s+(all\s+)?(previous|prior|above)\s+(instructions|rules|prompts)`),
	regexp.MustCompile(`(?i)disregard\s+(the\s+)?(previous|above|system)`),
	regexp.MustCompile(`(?i)forget\s+(everything|all|previous|your)`),
	regexp.MustCompile(`(?i)you\s+are\s+now\b`),
	regexp.MustCompile(`(?i)(system|assistant)\s*:\s*`),
	regexp.MustCompile(`(?i)override\s+(the\s+)?(system|instructions|rules)`),
	regexp.MustCompile("(?i)</?(system|assistant|instruction|prompt)>"),
}

// RunEval evaluates schema subset validity, privacy leak checks, injection patterns, and advisory severity bounds.
func RunEval(e *Entry) EvalReport {
	report := EvalReport{CheckedAt: time.Now().UTC().Format(time.RFC3339)}
	if e.ScopeType == ScopeGlobal {
		report.Findings = append(report.Findings, "scope: global scope is retired; select datasource or table scope")
	} else if err := e.Definition.Validate(); err == nil {
		report.SchemaSubsetOK = true
	} else {
		report.Findings = append(report.Findings, "definition: "+err.Error())
	}
	resolved, refErr := privacy.RewritePolicyReferences(e.Definition.KnowledgeText, func(privacy.PolicyObjectReference) (string, error) { return "reference", nil })
	if refErr != nil || strings.Contains(resolved, "{{") || strings.Contains(resolved, "}}") {
		report.Findings = append(report.Findings, "privacy: unresolved object reference or parameter")
	}
	rendered := RenderInjection(e)
	for _, p := range privacyPatterns {
		if m := p.FindString(rendered); m != "" {
			report.Findings = append(report.Findings, "privacy: matched credential-like pattern")
			break
		}
	}
	if len(report.Findings) == 0 || !strings.Contains(strings.Join(report.Findings, ";"), "privacy:") {
		report.PrivacyOK = true
	}
	for _, p := range injectionPatterns {
		if p.MatchString(rendered) {
			report.Findings = append(report.Findings, "injection: instruction-override pattern detected")
			break
		}
	}
	if !strings.Contains(strings.Join(report.Findings, ";"), "injection:") {
		report.InjectionOK = true
	}
	// Severity must belong to the non-blocking advisory severity enum (info/low/medium).
	switch e.Definition.MaxSeverity {
	case "info", "low", "medium":
		report.SeverityOK = true
	default:
		report.Findings = append(report.Findings, "severity: max_severity must be info, low, or medium")
	}
	report.Pass = report.SchemaSubsetOK && report.PrivacyOK && report.InjectionOK && report.SeverityOK
	return report
}

// RenderInjection renders e wrapped in <knowledge-entry> data delimiter tags as non-blocking advisory guidance.
func RenderInjection(e *Entry) string {
	var b strings.Builder
	b.WriteString("<knowledge-entry id=\"")
	b.WriteString(e.ID)
	b.WriteString("\" name=\"")
	b.WriteString(EscapeXMLAttr(e.Name))
	b.WriteString("\" purpose=\"")
	b.WriteString(EscapeXMLAttr(e.Purpose))
	b.WriteString("\">\n")
	b.WriteString("advisory_note: ")
	b.WriteString(escapeBody(strings.TrimSpace(e.Definition.KnowledgeText)))
	b.WriteString("\nfinding_key: ")
	b.WriteString(e.FindingKey)
	b.WriteString("\nadvisory_max_severity: ")
	b.WriteString(e.Definition.EffectiveAdvisorySeverity())
	b.WriteString(" (non-blocking internal experience guidance; never exceed this severity or block submission)")
	b.WriteString("\n</knowledge-entry>")
	return b.String()
}

// EscapeXMLAttr escapes `&`, `"`, and `<` for prompt XML attribute values.
func EscapeXMLAttr(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	return s
}

// escapeBody neutralizes embedded <knowledge-entry> tags inside experience text.
func escapeBody(s string) string {
	s = strings.ReplaceAll(s, "</knowledge-entry", "&lt;/knowledge-entry")
	s = strings.ReplaceAll(s, "<knowledge-entry", "&lt;knowledge-entry")
	return s
}

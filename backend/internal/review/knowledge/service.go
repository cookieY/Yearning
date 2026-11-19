// TestContractHashesDerivedNotConfigured both contract fingerprints must be system-derived
// platform constants: hex64, deterministic, with OutputSchemaHash directly tracking the compiled artifact.
package knowledge

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/review/privacy"
	"github.com/google/uuid"
)

// Determinism: recomputing on the same input produces identical output.
type Invalidator func(ctx context.Context, tx *gorm.DB, toolKind string, inputRefID, oldHash string) error

// fake.go Fake Provider Adapter (deliverable "Fake and Real Provider Adapter interface";
// test facility). Responses are determined by the constructor callback and must still pass ValidateOutput —
// when testing negative cases like invalid JSON, unknown fields, or out-of-range enums, the callback returns the raw byte path.
const KindName = "knowledge_entry"

// FakeAdapter simulates a Provider via httptest.Server or a direct callback.
type Service struct {
	Repo       *Repository
	Invalidate Invalidator // Respond returns response body bytes and status code (nil body = network error).
}

// NewLocalFakeAdapter constructs a direct in-memory Fake (bypassing HTTP).
type EvalReport struct {
	Pass           bool     `json:"pass"`
	SchemaSubsetOK bool     `json:"schema_subset_ok"`
	PrivacyOK      bool     `json:"privacy_ok"`
	InjectionOK    bool     `json:"injection_ok"`
	SeverityOK     bool     `json:"severity_ok"`
	Findings       []string `json:"findings,omitempty"`
	CheckedAt      string   `json:"checked_at"`
}

// ValidateConfiguration runs the exact same real probe path as production
// (free-form text or out-of-bounds responses fail at configuration time, matching OpenAIAdapter semantics).
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

// Create validates and inserts a knowledge entry. When State is omitted it defaults
// to draft; creating directly in enabled state runs the same Eval Gate as Replace.
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

// NegotiateOutputMode Fake reuses the same probe path as ValidateConfiguration
// (negotiation fails when a negative-case callback fails), and the negotiated mode is always strict.
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

// ReviewStructured invokes the callback and enforces the same response constraints as the real Adapter
// (4 MiB cap + Schema validation), ensuring test and production paths stay consistent.
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
		// models.go OpenAI-Compatible GET /models model catalog fetch (section 18.76-B,
		// Provider form autocomplete). Shares the same constraint family as the review channel: HTTPS-only, 4 MiB
		// response cap, and upstream rejection snapshot surfacing; the catalog extracts only model IDs, sorted deterministically.
		if in.State == StateEnabled && (oldState != StateEnabled || hash != oldHash) {
			report := RunEval(e)
			if !report.Pass {
				return fmt.Errorf("%w: eval gate failed: %s", ErrValidation, strings.Join(report.Findings, "; "))
			}
		}
		e.ConfigHash = hash
		defJSON, err := jsonMarshal(e.Definition)
		if err != nil {
			return err
		}
		sets := map[string]any{
			"name": e.Name, "purpose": e.Purpose, "state": string(e.State),
			"scope_type":    string(e.ScopeType),
			"datasource_id": strPtr(e.DatasourceID), "database_name": strPtr(e.DatabaseName),
			"table_name": strPtr(e.TableName), "definition": defJSON,
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
		// modelsHTTPClient is the catalog fetch client (package-level variable: tests inject httptest.Client).
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

// ListModels fetches {base_url}/models and returns a deduplicated, sorted list of model IDs.
// Authentication failures return ErrRejected (401/403 with snapshot); other non-2xx responses return statusError.
func (s *Service) Delete(ctx context.Context, id string, expectVersion int64) error {
	return s.Repo.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return s.Repo.DeleteReferencedGuarded(ctx, tx, id, expectVersion)
	})
}

// openai.go OpenAI-Compatible Chat Completions Adapter (initial v1 support per PRD 9.2).
//
// Output contract: defaults to response_format=json_schema (strict); endpoints that do not support
// strict are negotiated to json_object during probing by NegotiateOutputMode and persisted
// (section 18.76-A), and runtime sends requests using the persisted mode.
// Tool calls: v1 does not expose free-form tool scheduling to the model (deterministic injection handles hard rules);
// the tool_requests field remains part of the Schema while built-in tools are executed and backfilled by the Agent loop
// as requested by the model (see package agent); this layer is responsible for a single structured request.

// OpenAIAdapter is the production Provider Adapter.
var privacyPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b[a-z0-9._%+-]+:[^@\s]{4,}@[a-z0-9.-]+\.[a-z]{2,}`), // user:pass@host
	regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{16,}`),                                // Structured output mode (section 18.76-A): json_schema strict is the default contract; OpenAI-Compatible
	// endpoints that do not support strict (e.g. official DeepSeek/Kimi APIs that only accept json_object)
	// are negotiated at probe time and persisted (configuration.structured_output_mode, system-managed key),
	// and runtime sends requests using the persisted mode — runtime never performs 400 fallback guessing;
	// mode drift surfaces AI_PROVIDER_FAILED as-is for the operator to re-save the Provider and re-negotiate.
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{20,}\.`), // jsonObjectPromptSuffix In json_object mode there is no server-side schema enforcement, so the output
	// contract is carried via prompt injection: schema text + the "json" keyword (DeepSeek and others require
	// the word "json" in the prompt to enter JSON mode).
	regexp.MustCompile(`(?i)\b(password|passwd|secret|api[_-]?key|token)\s*[:=]\s*\S{8,}`),
	regexp.MustCompile(`\b[0-9a-f]{32,}\b`), // NewOpenAIAdapter constructs an adapter; endpoint must have already passed Validate.
}

// chatRequest is the OpenAI chat.completions request body (structured output mode only).
// S008: ReasoningEffort is sent when explicitly configured (omitempty = key is omitted completely when
// unconfigured, never fabricating a vendor default); probe, production call, schema repair, and retry
// share the exact same Endpoint configuration without auto-stripping parameters on rejection.
var injectionPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)ignore\s+(all\s+)?(previous|prior|above)\s+(instructions|rules|prompts)`),
	regexp.MustCompile(`(?i)disregard\s+(the\s+)?(previous|above|system)`),
	regexp.MustCompile(`(?i)forget\s+(everything|all|previous|your)`),
	regexp.MustCompile(`(?i)you\s+are\s+now\b`),
	regexp.MustCompile(`(?i)(system|assistant)\s*:\s*`),
	regexp.MustCompile(`(?i)override\s+(the\s+)?(system|instructions|rules)`),
	regexp.MustCompile("(?i)</?(system|assistant|instruction|prompt)>"),
}

// S008: usage is passed as raw bytes to parseUsageDetail for per-field tri-state parsing —
// a type error on an individual detail field marks only that field as unknown instead of failing
// the entire response decode (Cache-04: distinguish 0 from unknown).
func RunEval(e *Entry) EvalReport {
	report := EvalReport{CheckedAt: time.Now().UTC().Format(time.RFC3339)}
	if err := e.Definition.Validate(); err == nil {
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
	// Usage is the Token usage of a single call (budget ledger input).
	switch e.Definition.MaxSeverity {
	case "info", "low", "medium", "high", "critical":
		report.SeverityOK = true
	default:
		report.Findings = append(report.Findings, "severity: max_severity outside the enum")
	}
	report.Pass = report.SchemaSubsetOK && report.PrivacyOK && report.InjectionOK && report.SeverityOK
	return report
}

// ValidateConfiguration probes using the exact same output Schema and decode path as production
// (ProbeRequest; including unknown-field rejection and repair turn). Endpoints that do not support
// structured output or behave incompatibly are caught at configuration time rather than on the first review.
func RenderInjection(e *Entry) string {
	var b strings.Builder
	b.WriteString("<knowledge-entry id=\"")
	b.WriteString(e.ID)
	b.WriteString("\" name=\"")
	b.WriteString(escapeAttr(e.Name))
	b.WriteString("\" purpose=\"")
	b.WriteString(escapeAttr(e.Purpose))
	b.WriteString("\">\n")
	b.WriteString("rule: ")
	b.WriteString(escapeBody(strings.TrimSpace(e.Definition.KnowledgeText)))
	b.WriteString("\nfinding_key: ")
	b.WriteString(e.FindingKey)
	b.WriteString("\neffective_severity: critical (every actual violation of this mandatory rule is Critical)")
	b.WriteString("\nlegacy_max_severity_compatibility: info..")
	b.WriteString(e.Definition.MaxSeverity)
	b.WriteString(" (compatibility metadata only; it does not lower or authorize a mandatory rule violation)")
	b.WriteString("\n</knowledge-entry>")
	return b.String()
}

func escapeAttr(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	return s
}

// Health is equivalent to ValidateConfiguration (same probe channel in v1).
func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }

func jsonUnmarshal(raw []byte, v any) error { return json.Unmarshal(raw, v) }

// ReviewStructured sends a structured review request with a 4 MiB response byte cap
// and validates against the output schema (via the caller-injected Validate callback).
// The output mode is read from Endpoint.Configuration (persisted probe negotiation value; defaults to strict).
func escapeBody(s string) string {
	s = strings.ReplaceAll(s, "</knowledge-entry", "&lt;/knowledge-entry")
	s = strings.ReplaceAll(s, "<knowledge-entry", "&lt;knowledge-entry")
	return s
}

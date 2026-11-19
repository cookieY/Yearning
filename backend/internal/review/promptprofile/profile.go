package promptprofile

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	// ProfileDefault is the anonymized, schema-verbose prompt profile for general LLMs and transitional v1/v2/v3 adapters.
	ProfileDefault = "default"
	// ProfileSAN is the compact Raw-SQL Native prompt profile for unified SAN adapters.
	ProfileSAN = "san"

	// SourceExplicit indicates system_prompt_profile was explicitly configured on the provider.
	SourceExplicit = "explicit"
	// SourceAuto indicates system_prompt_profile was inferred from model_name.
	SourceAuto = "auto"

	// MaxInlineLiteralChars bounds single-quoted SQL string literals in Raw-SQL Native requests.
	MaxInlineLiteralChars = 256
	literalPreviewChars   = 64
)

var (
	// ErrInvalidProfile indicates an unsupported explicit system_prompt_profile value.
	ErrInvalidProfile = errors.New("promptprofile: invalid system_prompt_profile")
	// ErrInvalidAssistantInput indicates an invalid actor_role or capability_hint for assistant prompt construction.
	ErrInvalidAssistantInput = errors.New("promptprofile: invalid assistant prompt input")
	// ErrInvalidMaxLiteralChars indicates a non-positive literal truncation bound.
	ErrInvalidMaxLiteralChars = errors.New("promptprofile: max_chars must be >= 1")
)

var (
	legacyAnonymizedModelRe = regexp.MustCompile(`^(?:xmodel|san|sql-audit-nano)(?:[-_.](?:2b|4b))?[-_.](?:v1|v2|v3)(?:$|[^a-z0-9])`)
	legacyAnonymizedTagRe   = regexp.MustCompile(`^(?:(?:2b|4b)[-_.])?(?:v1|v2|v3)(?:$|[^a-z0-9])`)
	paramPlaceholderRe      = regexp.MustCompile(`\{\{[a-z][a-z0-9_]{0,31}\}\}`)
)

var escapeRuleTags = []string{"</rules", "<rules", "</skill", "<skill", "</exp", "<exp"}

var builtinToolNames = []string{
	"datasource_identity",
	"index_metadata",
	"safe_explain",
	"schema_metadata",
	"table_statistics",
}

// BuiltinToolNames returns a fresh copy of the canonical built-in tool names in sorted order.
func BuiltinToolNames() []string {
	out := make([]string, len(builtinToolNames))
	copy(out, builtinToolNames)
	return out
}

// ValidateExplicit checks whether an explicit profile value is supported.
func ValidateExplicit(explicit string) error {
	if explicit != ProfileDefault && explicit != ProfileSAN {
		return fmt.Errorf("%w: %q (expected %q or %q)", ErrInvalidProfile, explicit, ProfileDefault, ProfileSAN)
	}
	return nil
}

// Resolve determines the effective System Prompt Profile ("default" or "san") and its resolution source ("explicit" or "auto").
func Resolve(modelName string, explicit *string) (string, string, error) {
	if explicit != nil {
		if err := ValidateExplicit(*explicit); err != nil {
			return "", "", err
		}
		return *explicit, SourceExplicit, nil
	}

	tail := strings.ToLower(strings.TrimSpace(modelName))
	if idx := strings.LastIndex(tail, "/"); idx >= 0 {
		tail = tail[idx+1:]
	}
	base, tag, _ := strings.Cut(tail, ":")
	if legacyAnonymizedModelRe.MatchString(base) {
		return ProfileDefault, SourceAuto, nil
	}

	isSanFamily := base == "san" || base == "sql-audit-nano" ||
		strings.HasPrefix(base, "san-") || strings.HasPrefix(base, "san_") || strings.HasPrefix(base, "san.") ||
		strings.HasPrefix(base, "sql-audit-nano-") || strings.HasPrefix(base, "sql-audit-nano_") || strings.HasPrefix(base, "sql-audit-nano.")
	isLegacyXmodelFamily := base == "xmodel" ||
		strings.HasPrefix(base, "xmodel-") || strings.HasPrefix(base, "xmodel_") || strings.HasPrefix(base, "xmodel.")

	if (isSanFamily || isLegacyXmodelFamily) && tag != "" && legacyAnonymizedTagRe.MatchString(tag) {
		return ProfileDefault, SourceAuto, nil
	}
	if isSanFamily {
		return ProfileSAN, SourceAuto, nil
	}
	return ProfileDefault, SourceAuto, nil
}

// ExtractExplicitProfile extracts an optional string system_prompt_profile from provider configuration maps.
func ExtractExplicitProfile(cfg map[string]any) (*string, error) {
	if len(cfg) == 0 {
		return nil, nil
	}
	raw, exists := cfg["system_prompt_profile"]
	if !exists || raw == nil {
		return nil, nil
	}
	switch v := raw.(type) {
	case string:
		if err := ValidateExplicit(v); err != nil {
			return nil, err
		}
		cp := v
		return &cp, nil
	case *string:
		if v == nil {
			return nil, nil
		}
		if err := ValidateExplicit(*v); err != nil {
			return nil, err
		}
		cp := *v
		return &cp, nil
	default:
		return nil, fmt.Errorf("%w: must be a string", ErrInvalidProfile)
	}
}

// ResolveFromConfiguration resolves the effective profile from model_name and configuration map.
func ResolveFromConfiguration(modelName string, cfg map[string]any) (string, string, error) {
	explicit, err := ExtractExplicitProfile(cfg)
	if err != nil {
		return "", "", err
	}
	return Resolve(modelName, explicit)
}

// IsSAN reports whether the effective resolved profile is ProfileSAN. Invalid explicit configs fail closed as false.
func IsSAN(modelName string, cfg map[string]any) bool {
	profile, _, err := ResolveFromConfiguration(modelName, cfg)
	return err == nil && profile == ProfileSAN
}

// TruncateOversizedSQLLiterals shortens single-quoted string literals longer than maxChars runes while keeping
// all identifiers, normal literals, and comments intact.
func TruncateOversizedSQLLiterals(sqlText string, maxChars int) (string, error) {
	if maxChars < 1 {
		return "", fmt.Errorf("%w: got %d", ErrInvalidMaxLiteralChars, maxChars)
	}
	if sqlText == "" || !strings.ContainsRune(sqlText, '\'') {
		return sqlText, nil
	}

	runes := []rune(sqlText)
	n := len(runes)
	previewLen := min(literalPreviewChars, maxChars)
	var out strings.Builder
	out.Grow(len(sqlText))

	i := 0
	for i < n {
		ch := runes[i]

		// Preserve quoted identifiers without inspection.
		if ch == '`' || ch == '"' {
			quote := ch
			j := i + 1
			for j < n {
				if runes[j] == quote {
					if j+1 < n && runes[j+1] == quote {
						j += 2
						continue
					}
					j++
					break
				}
				j++
			}
			out.WriteString(string(runes[i:j]))
			i = j
			continue
		}

		// Preserve SQL comments without altering single quotes inside comment prose.
		if ch == '-' && i+1 < n && runes[i+1] == '-' {
			j := i + 2
			for j < n && runes[j] != '\n' {
				j++
			}
			if j < n && runes[j] == '\n' {
				j++
			}
			out.WriteString(string(runes[i:j]))
			i = j
			continue
		}
		if ch == '/' && i+1 < n && runes[i+1] == '*' {
			j := i + 2
			closed := false
			for j+1 < n {
				if runes[j] == '*' && runes[j+1] == '/' {
					j += 2
					closed = true
					break
				}
				j++
			}
			if !closed {
				j = n
			}
			out.WriteString(string(runes[i:j]))
			i = j
			continue
		}

		if ch != '\'' {
			out.WriteRune(ch)
			i++
			continue
		}

		start := i
		j := i + 1
		closed := false
		for j < n {
			c := runes[j]
			if c == '\\' && j+1 < n {
				j += 2
				continue
			}
			if c == '\'' {
				if j+1 < n && runes[j+1] == '\'' {
					j += 2
					continue
				}
				closed = true
				break
			}
			j++
		}
		if !closed {
			out.WriteString(string(runes[start:]))
			break
		}

		inner := runes[start+1 : j]
		if len(inner) > maxChars {
			out.WriteByte('\'')
			out.WriteString(string(inner[:previewLen]))
			out.WriteString("...<truncated:")
			out.WriteString(strconv.Itoa(len(inner)))
			out.WriteString("_chars>'")
		} else {
			out.WriteString(string(runes[start : j+1]))
		}
		i = j + 1
	}

	return out.String(), nil
}

// TruncateSQL shortens oversized SQL literals using MaxInlineLiteralChars.
func TruncateSQL(sqlText string) string {
	out, err := TruncateOversizedSQLLiterals(sqlText, MaxInlineLiteralChars)
	if err != nil {
		return sqlText
	}
	return out
}

// StatementOrdinalRange matches the JSON shape of provider.StatementOrdinalRange.
type StatementOrdinalRange struct {
	Start int32 `json:"start"`
	End   int32 `json:"end"`
}

// ObjectReference matches the JSON shape of provider.AnonymizedObjectReference.
type ObjectReference struct {
	Token                  string                  `json:"token"`
	Kind                   string                  `json:"kind"`
	Relation               string                  `json:"relation"`
	SchemaToken            string                  `json:"schema_token,omitempty"`
	ParentToken            string                  `json:"parent_token,omitempty"`
	StatementOrdinal       int32                   `json:"statement_ordinal"`
	StatementOrdinalRanges []StatementOrdinalRange `json:"statement_ordinal_ranges"`
}

// RuleInput captures a compact review rule or knowledge entry for <rules> injection.
type RuleInput struct {
	FindingKey    string
	KnowledgeText string
	Parameters    map[string]any
}

// ReviewSystemPromptInput defines the inputs for BuildSANReviewSystemPrompt.
type ReviewSystemPromptInput struct {
	OutputLocale       string
	EngineProfile      string
	ServerVersion      string
	ObjectReferences   []ObjectReference
	Skills             []RuleInput
	Knowledge          []RuleInput
	ToolNames          []string
	HasExplicitTools   bool
	EvidenceIncomplete bool
	LocaleRetry        bool
	SchemaRetry        bool
}

// NormalizeReviewLocale mirrors review/agent locale normalization ("en-US" else "zh-CN").
func NormalizeReviewLocale(outputLocale string) string {
	if outputLocale == "en-US" {
		return "en-US"
	}
	return "zh-CN"
}

// NormalizeOutputLocale normalizes runtime output_locale identically to assistant and diagnostics pipelines.
func NormalizeOutputLocale(outputLocale string) string {
	trimmed := strings.ToLower(strings.TrimSpace(outputLocale))
	if trimmed == "en-us" || strings.HasPrefix(trimmed, "en") {
		return "en-US"
	}
	return "zh-CN"
}

// RenderParameters renders {{key}} placeholders deterministically across skills.
func RenderParameters(text string, params map[string]any) string {
	if !strings.Contains(text, "{{") {
		return text
	}
	var lowerKeys map[string]string
	if len(params) > 0 {
		lowerKeys = make(map[string]string, len(params))
		for _, k := range slices.Sorted(maps.Keys(params)) {
			lowerKeys[strings.ToLower(k)] = k
		}
	}
	return paramPlaceholderRe.ReplaceAllStringFunc(text, func(m string) string {
		key := strings.Trim(m, "{}")
		v, ok := params[key]
		if !ok && lowerKeys != nil {
			if canonicalKey, found := lowerKeys[strings.ToLower(key)]; found {
				v, ok = params[canonicalKey]
			}
		}
		if !ok {
			return "not configured"
		}
		switch t := v.(type) {
		case bool:
			return strconv.FormatBool(t)
		case int:
			return strconv.Itoa(t)
		case int64:
			return strconv.FormatInt(t, 10)
		case float64:
			if t == float64(int64(t)) {
				return strconv.FormatInt(int64(t), 10)
			}
			return strconv.FormatFloat(t, 'f', -1, 64)
		case string:
			return t
		case []string:
			return strings.Join(t, ",")
		case []any:
			parts := make([]string, 0, len(t))
			for _, item := range t {
				parts = append(parts, fmt.Sprint(item))
			}
			return strings.Join(parts, ",")
		default:
			return "not configured"
		}
	})
}

func escapeRuleAttr(val string) string {
	r := strings.NewReplacer("&", "&amp;", `"`, "&quot;", "<", "&lt;")
	return r.Replace(val)
}

func escapeRuleBody(text string) string {
	collapsed := strings.Join(strings.Fields(text), " ")
	for _, tag := range escapeRuleTags {
		collapsed = strings.ReplaceAll(collapsed, tag, "&lt;"+tag[1:])
	}
	return collapsed
}

func renderRuleLine(tag string, item RuleInput, withParams bool) string {
	raw := strings.TrimSpace(item.KnowledgeText)
	if withParams {
		raw = RenderParameters(raw, item.Parameters)
	}
	return fmt.Sprintf(`<%s key="%s">%s</%s>`, tag, escapeRuleAttr(item.FindingKey), escapeRuleBody(raw), tag)
}

func marshalCompactJSON(v any) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "{}"
	}
	return strings.TrimSuffix(buf.String(), "\n")
}

// BuildSANReviewSystemPrompt builds the compact, versionless Raw-SQL Native system prompt for ai-review.
func BuildSANReviewSystemPrompt(in ReviewSystemPromptInput) string {
	locale := NormalizeReviewLocale(in.OutputLocale)
	targetEngine := strings.TrimSpace(in.EngineProfile)
	if targetEngine == "" {
		targetEngine = "unknown"
	}
	targetVersion := strings.TrimSpace(in.ServerVersion)
	if targetVersion == "" {
		targetVersion = "unknown"
	}

	lines := []string{
		"[SAN_HEAD=ai-review]",
		fmt.Sprintf("locale=%s; target=%s@%s", locale, targetEngine, targetVersion),
		"Return strict JSON only (review_output). Cite evidence_ids when fact_basis=evidence; reuse prehydrated normalized_evidence without duplicate tool_requests.",
	}

	if len(in.ObjectReferences) > 0 {
		normalized := make([]ObjectReference, len(in.ObjectReferences))
		for i, ref := range in.ObjectReferences {
			normalized[i] = ref
			if normalized[i].StatementOrdinalRanges == nil {
				normalized[i].StatementOrdinalRanges = []StatementOrdinalRange{}
			}
		}
		lines = append(lines, "objects: "+marshalCompactJSON(struct {
			Objects []ObjectReference `json:"objects"`
		}{Objects: normalized}))
	}

	if len(in.Skills) > 0 || len(in.Knowledge) > 0 {
		lines = append(lines, "<rules>")
		for _, s := range in.Skills {
			lines = append(lines, renderRuleLine("skill", s, true))
		}
		for _, k := range in.Knowledge {
			lines = append(lines, renderRuleLine("exp", k, false))
		}
		lines = append(lines, "</rules>")
	}

	effectiveTools := in.ToolNames
	if !in.HasExplicitTools && in.ToolNames == nil {
		effectiveTools = BuiltinToolNames()
	}
	if len(effectiveTools) > 0 {
		lines = append(lines, "tools: "+strings.Join(effectiveTools, ", "))
	}

	if in.EvidenceIncomplete {
		lines = append(lines, "flag: evidence_incomplete (return outcome=partial)")
	}
	if in.LocaleRetry {
		lines = append(lines, "flag: retry_locale (regenerate human text in locale)")
	}
	if in.SchemaRetry {
		lines = append(lines, "flag: retry_schema (fix JSON schema compliance)")
	}

	return strings.Join(lines, "\n")
}

// BuildSANReviewProbePrompt returns the canonical compact health-probe system prompt for SAN ai-review.
func BuildSANReviewProbePrompt() string {
	return strings.Join([]string{
		"[SAN_HEAD=ai-review]",
		"locale=en-US; target=probe/probe@0; probe=true",
		`Return strict JSON only (review_output). Health probe: return outcome="complete", findings=[], tool_requests=[].`,
	}, "\n")
}

// HelpArticleInput captures one retrieved platform help article for SAN ai-assistant prompt injection.
type HelpArticleInput struct {
	ID            string
	Title         string
	RoutePath     string
	Summary       string
	Steps         []string
	DetailedGuide string
	PrimaryMatch  bool
}

// AssistantSystemPromptInput defines inputs for BuildSANAssistantSystemPrompt.
type AssistantSystemPromptInput struct {
	OutputLocale   string
	ActorRole      string
	CapabilityHint string
	HasMetadata    bool
	Articles       []HelpArticleInput
}

// BuildSANAssistantSystemPrompt builds the compact, versionless Raw-SQL Native system prompt for ai-assistant.
func BuildSANAssistantSystemPrompt(in AssistantSystemPromptInput) (string, error) {
	if in.ActorRole != "authenticated_user" && in.ActorRole != "builtin_admin" {
		return "", fmt.Errorf("%w: invalid actor_role %q", ErrInvalidAssistantInput, in.ActorRole)
	}
	switch in.CapabilityHint {
	case "sql_optimize", "sql_generate", "platform_help", "out_of_scope":
	default:
		return "", fmt.Errorf("%w: invalid capability_hint %q", ErrInvalidAssistantInput, in.CapabilityHint)
	}

	locale := NormalizeOutputLocale(in.OutputLocale)
	hasMetaStr := "false"
	if in.HasMetadata {
		hasMetaStr = "true"
	}

	lines := []string{
		"[SAN_HEAD=ai-assistant]",
		fmt.Sprintf("locale=%s; role=%s; hint=%s; has_metadata=%s", locale, in.ActorRole, in.CapabilityHint, hasMetaStr),
		"Return strict JSON only (assistant_output: out_of_scope first, then resolved_capability, session_title, assistant_reply, sql, assumptions, next_steps, semantic_warnings, needs_clarify, cited_article_ids). Enforce scope gate (DB/SQL/Yearning only; reject destructive DDL/DCL as out_of_scope=true).",
	}

	includeHelpContext := in.CapabilityHint == "platform_help"
	hasAnyPrimary := false
	for _, art := range in.Articles {
		if art.PrimaryMatch {
			hasAnyPrimary = true
			includeHelpContext = true
		}
	}

	if includeHelpContext {
		catalogLines, detailedText := renderAssistantHelpSections(in.Articles, in.CapabilityHint, hasAnyPrimary)
		lines = append(lines, "<help_context>", "[catalog]")
		lines = append(lines, catalogLines...)
		lines = append(lines, "[guides]")
		if detailedText != "" {
			lines = append(lines, detailedText)
		}
		lines = append(lines, "</help_context>")
	}

	return strings.Join(lines, "\n"), nil
}

func renderAssistantHelpSections(articles []HelpArticleInput, capabilityHint string, hasAnyPrimary bool) ([]string, string) {
	catalogLines := make([]string, 0, len(articles))
	guideChunks := make([]string, 0, len(articles))

	for idx, art := range articles {
		catalogLines = append(catalogLines, fmt.Sprintf("- [%s] (%s): %s", art.ID, art.RoutePath, art.Summary))

		isPrimary := capabilityHint == "platform_help" || art.PrimaryMatch || (!hasAnyPrimary && idx < 4)
		if !isPrimary {
			continue
		}

		matchTag := ""
		if art.PrimaryMatch {
			matchTag = " [PRIMARY MATCH]"
		}
		block := []string{
			fmt.Sprintf("#### [%s]%s %s (route: `%s`)", art.ID, matchTag, art.Title, art.RoutePath),
			"Summary: " + art.Summary,
		}
		if len(art.Steps) > 0 {
			block = append(block, "Steps:")
			for stepIdx, step := range art.Steps {
				block = append(block, fmt.Sprintf("%d. %s", stepIdx+1, step))
			}
		}
		if trimmed := strings.TrimSpace(art.DetailedGuide); trimmed != "" {
			block = append(block, "Details & FAQ:", trimmed)
		}
		guideChunks = append(guideChunks, strings.Join(block, "\n"))
	}

	return catalogLines, strings.TrimSpace(strings.Join(guideChunks, "\n\n"))
}

// BuildSANDiagnoseSystemPrompt builds the compact, versionless Raw-SQL Native system prompt for ai-diagnose.
func BuildSANDiagnoseSystemPrompt(outputLocale string) string {
	locale := NormalizeOutputLocale(outputLocale)
	return strings.Join([]string{
		"[SAN_HEAD=ai-diagnose]",
		"locale=" + locale,
		"Return strict JSON only (diagnose_output: observed_facts, root_cause_hypotheses, optimization_recommendations, verification_steps). No EXPLAIN is present; never assert confirmed plan facts.",
	}, "\n")
}

// UTF8RuneLen returns the rune count for external callers needing literal-size diagnostics.
func UTF8RuneLen(s string) int {
	return utf8.RuneCountInString(s)
}

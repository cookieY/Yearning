// Package application implements the Database AI Assistant application orchestration:
// session lifecycle, per-turn anonymization and restoration, authorized metadata tools,
// automatic context compaction, formal AI precheck bridging, and idempotent order submission.
package application

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/assistant/domain"
	"github.com/cookieY/YearningX/backend/internal/assistant/help"
	"github.com/cookieY/YearningX/backend/internal/assistant/tool"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
	"github.com/cookieY/YearningX/backend/internal/review/promptprofile"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
)

// AnonymizedTurn represents a single desensitized conversation turn (free of raw identifiers and business literals).
type AnonymizedTurn struct {
	Sequence   int64  `json:"sequence"`
	Role       string `json:"role"`
	Capability string `json:"capability"`
	Content    string `json:"content"`
}

// SANColumnFact represents raw column metadata sent only to explicit SAN ("san") endpoints.
type SANColumnFact struct {
	Name            string `json:"name"`
	Ordinal         int    `json:"ordinal"`
	DataType        string `json:"data_type"`
	Nullable        bool   `json:"nullable"`
	IsPrimaryKey    bool   `json:"is_primary_key,omitempty"`
	IsUnique        bool   `json:"is_unique,omitempty"`
	DefaultBehavior string `json:"default_behavior,omitempty"`
	Comment         string `json:"comment,omitempty"`
}

// SANIndexFact represents raw index metadata sent only to explicit SAN ("san") endpoints.
type SANIndexFact struct {
	Name            string   `json:"name"`
	Primary         bool     `json:"primary,omitempty"`
	Unique          bool     `json:"unique,omitempty"`
	Columns         []string `json:"columns"`
	IncludedColumns []string `json:"included_columns,omitempty"`
	ExpressionCount int      `json:"expression_count,omitempty"`
}

// SANTableDescription represents raw table metadata sent only to explicit SAN ("san") endpoints.
type SANTableDescription struct {
	Schema  string          `json:"schema"`
	Table   string          `json:"table"`
	Columns []SANColumnFact `json:"columns"`
	Indexes []SANIndexFact  `json:"indexes,omitempty"`
}

// SynthesizeInput carries the complete anonymized context sent to the model synthesizer.
// Raw fields are json:"-" and held strictly in memory for explicit SAN ("san") endpoints.
type SynthesizeInput struct {
	Capability         domain.Capability                  `json:"capability"`
	EngineProfile      string                             `json:"engine_profile,omitempty"`
	OutputLocale       string                             `json:"output_locale,omitempty"`
	AnonymizedPrompt   string                             `json:"anonymized_prompt"`
	AnonymizedInputSQL string                             `json:"anonymized_input_sql,omitempty"`
	AnonymizedPlanText string                             `json:"anonymized_plan_text,omitempty"`
	AnonymizedMetadata []*tool.AnonymizedTableDescription `json:"anonymized_metadata,omitempty"`
	AnonymizedHistory  []AnonymizedTurn                   `json:"anonymized_history,omitempty"`
	AnonymizedSummary  string                             `json:"anonymized_summary,omitempty"`
	BoundPlaceholders  []string                           `json:"bound_placeholders,omitempty"`
	PlatformKnowledge  []help.Article                     `json:"-"`
	Actor              domain.Actor                       `json:"-"`
	OnDelta            func(kind string, delta string)    `json:"-"`
	OnSANDelta         func(kind string, delta string)    `json:"-"`
	RawPrompt          string                             `json:"-"`
	RawInputSQL        string                             `json:"-"`
	RawPlanText        string                             `json:"-"`
	RawMetadata        []SANTableDescription              `json:"-"`
	RawHistory         []AnonymizedTurn                   `json:"-"`
	RawSummary         string                             `json:"-"`
}

// SynthesizeOutput represents the structured anonymized draft and Markdown reply returned by the synthesizer.
type SynthesizeOutput struct {
	OutOfScope         bool     `json:"out_of_scope,omitempty"`
	ResolvedCapability string   `json:"resolved_capability,omitempty"`
	SessionTitle       string   `json:"session_title,omitempty"`
	AssistantReply     string   `json:"assistant_reply"`
	AnonymizedSQL      string   `json:"anonymized_sql,omitempty"`
	SQL                string   `json:"sql,omitempty"`
	Assumptions        []string `json:"assumptions,omitempty"`
	NextSteps          []string `json:"next_steps,omitempty"`
	SemanticWarnings   []string `json:"semantic_warnings,omitempty"`
	NeedsClarify       bool     `json:"needs_clarify,omitempty"`
	CitedArticleIDs    []string `json:"cited_article_ids,omitempty"`
	InputTokens        int64    `json:"-"`
	OutputTokens       int64    `json:"-"`
	UsedSANProfile     bool     `json:"-"`
	ProviderID         string   `json:"-"`
	ModelName          string   `json:"-"`
}

// Synthesizer abstracts model generation, SQL optimization, and session summarization.
type Synthesizer interface {
	Synthesize(ctx context.Context, in SynthesizeInput) (*SynthesizeOutput, error)
	Summarize(ctx context.Context, turns []AnonymizedTurn) (string, int64, int64, error)
}

// ProviderSynthesizer invokes the configured OpenAI-compatible AI Provider chain under the Prompt-Based Scope Gate.
// In production (Fallback == nil), Synthesize fails closed with 2011 AI_PROVIDER_FAILED when no enabled AI provider exists;
// offline unit tests may pass a test Synthesizer as an explicit Fallback.
type ProviderSynthesizer struct {
	DB         *gorm.DB
	Crypter    *crypto.Crypter
	HTTPClient *provider.HTTPClient
	Fallback   Synthesizer
}

// NewProviderSynthesizer creates a synthesizer backed by the configured AI Provider chain and system prompt gate.
func NewProviderSynthesizer(db *gorm.DB, crypter *crypto.Crypter, fallback Synthesizer) *ProviderSynthesizer {
	return &ProviderSynthesizer{
		DB:         db,
		Crypter:    crypter,
		HTTPClient: provider.NewHTTPClient(),
		Fallback:   fallback,
	}
}

type providerEndpointRow struct {
	ID                string
	Name              string
	ProviderKind      string
	BaseURL           string
	ModelName         string
	SelectionPriority int
	SecretCiphertext  []byte
	SecretNonce       []byte
	SecretKeyID       string
	Configuration     []byte
}

func (p *ProviderSynthesizer) loadActiveEndpoints(ctx context.Context) ([]provider.Endpoint, error) {
	if p == nil || p.DB == nil || p.Crypter == nil {
		return nil, nil
	}
	var rows []providerEndpointRow
	if err := p.DB.WithContext(ctx).Raw(`
		select id::text, name, provider_kind, base_url, model_name, selection_priority,
		       secret_ciphertext, secret_nonce, secret_key_id, configuration::text as configuration
		from ai_providers where enabled = true order by selection_priority asc`).Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]provider.Endpoint, 0, len(rows))
	for _, r := range rows {
		key, err := p.Crypter.Open(crypto.PurposePayload, crypto.Envelope{
			Ciphertext: r.SecretCiphertext,
			Nonce:      r.SecretNonce,
			KeyID:      r.SecretKeyID,
			Algorithm:  "aes-256-gcm",
		}, crypto.AAD{
			DeploymentID: p.Crypter.DeploymentID(),
			Table:        "ai_providers",
			PrimaryKey:   r.ID,
			Column:       "secret_ciphertext",
			Purpose:      "provider_secret",
		})
		if err != nil {
			continue
		}
		var cfg map[string]any
		if len(r.Configuration) > 0 {
			_ = json.Unmarshal(r.Configuration, &cfg)
		}
		out = append(out, provider.Endpoint{
			ProviderID:        r.ID,
			Name:              r.Name,
			ProviderKind:      r.ProviderKind,
			BaseURL:           r.BaseURL,
			ModelName:         r.ModelName,
			SelectionPriority: r.SelectionPriority,
			APIKey:            string(key),
			Configuration:     cfg,
		})
	}
	return out, nil
}

func (p *ProviderSynthesizer) Synthesize(ctx context.Context, in SynthesizeInput) (*SynthesizeOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	endpoints, err := p.loadActiveEndpoints(ctx)
	if err != nil {
		return nil, domain.ErrProviderFailed("Failed to load active AI providers")
	}
	return p.synthesizeWithEndpoints(ctx, endpoints, in)
}

func (p *ProviderSynthesizer) synthesizeWithEndpoints(ctx context.Context, endpoints []provider.Endpoint, in SynthesizeInput) (*SynthesizeOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(endpoints) == 0 {
		if p.Fallback != nil {
			return p.Fallback.Synthesize(ctx, in)
		}
		return nil, domain.ErrProviderFailed("No active AI provider is configured; ask an administrator to configure and enable an AI provider under Review Engine -> Providers")
	}

	var (
		defaultPrompt    string
		defaultUserBytes []byte
		hasDefaultBuild  bool
		sanPrompt        string
		sanUserBytes     []byte
		hasSANBuild      bool
	)

	var lastErr error
	for _, ep := range endpoints {
		isSAN := ep.EffectivePromptProfile() == promptprofile.ProfileSAN
		var sysPrompt string
		var userBytes []byte
		if isSAN {
			if !hasSANBuild {
				var buildErr error
				sanPrompt, sanUserBytes, buildErr = buildSANAssistantMessages(in)
				if buildErr != nil {
					lastErr = buildErr
					continue
				}
				hasSANBuild = true
			}
			sysPrompt, userBytes = sanPrompt, sanUserBytes
		} else {
			if !hasDefaultBuild {
				defaultPrompt = help.BuildScopeGateSystemPromptWithLocale(in.Capability, in.PlatformKnowledge, len(in.AnonymizedMetadata) > 0, in.OutputLocale)
				userPayload := map[string]any{
					"response_format":      "json",
					"capability_hint":      in.Capability,
					"engine_profile":       in.EngineProfile,
					"output_locale":        in.OutputLocale,
					"anonymized_prompt":    in.AnonymizedPrompt,
					"anonymized_input_sql": in.AnonymizedInputSQL,
					"anonymized_plan_text": in.AnonymizedPlanText,
					"anonymized_metadata":  in.AnonymizedMetadata,
					"anonymized_history":   in.AnonymizedHistory,
					"anonymized_summary":   in.AnonymizedSummary,
					"bound_placeholders":   in.BoundPlaceholders,
				}
				var merr error
				defaultUserBytes, merr = json.Marshal(userPayload)
				if merr != nil {
					return nil, merr
				}
				hasDefaultBuild = true
			}
			sysPrompt, userBytes = defaultPrompt, defaultUserBytes
		}
		deltaFn := in.OnDelta
		if isSAN && in.OnSANDelta != nil {
			deltaFn = in.OnSANDelta
		}
		attemptCommitted := false
		var attemptDeltaFn func(string, string)
		if deltaFn != nil {
			attemptDeltaFn = func(kind string, delta string) {
				if (kind == "delta" || kind == "reasoning") && delta != "" {
					attemptCommitted = true
				}
				deltaFn(kind, delta)
			}
		}
		out, callErr := p.callEndpoint(ctx, ep, sysPrompt, string(userBytes), in.Capability, attemptDeltaFn)
		if callErr == nil && out != nil {
			out.UsedSANProfile = isSAN
			out.ProviderID = ep.ProviderID
			out.ModelName = ep.ModelName
			return out, nil
		}
		lastErr = callErr
		if attemptCommitted {
			// Live SSE deltas from this endpoint were already emitted to the caller;
			// falling back would splice partial replies or truncate emitted byte offsets.
			return nil, lastErr
		}
	}
	if lastErr != nil {
		return nil, lastErr
	}
	if p.Fallback != nil {
		return p.Fallback.Synthesize(ctx, in)
	}
	return nil, domain.ErrProviderFailed("No active AI provider is configured; ask an administrator to configure and enable an AI provider under Review Engine -> Providers")
}

func (p *ProviderSynthesizer) Summarize(ctx context.Context, turns []AnonymizedTurn) (string, int64, int64, error) {
	if p != nil && p.Fallback != nil {
		return p.Fallback.Summarize(ctx, turns)
	}
	return summarizeAnonymizedTurns(ctx, turns)
}

func buildSANAssistantMessages(in SynthesizeInput) (string, []byte, error) {
	actorRole := "authenticated_user"
	if in.Actor.IsBuiltinAdmin {
		actorRole = "builtin_admin"
	}
	articles := help.ToPromptProfileArticles(in.PlatformKnowledge)
	if in.Capability != domain.CapabilityPlatformHelp {
		// RetrieveRelevantArticles assigns PrimaryMatch=true to topK fallback items even when score == 0.
		// Keep <help_context> omitted on non-help turns unless at least one keyword-matched or synthetic test article matched.
		for i := range in.PlatformKnowledge {
			if len(in.PlatformKnowledge[i].Keywords) > 0 && in.PlatformKnowledge[i].RetrievalScore == 0 {
				articles[i].PrimaryMatch = false
			}
		}
	}
	sysPrompt, err := promptprofile.BuildSANAssistantSystemPrompt(promptprofile.AssistantSystemPromptInput{
		OutputLocale:   in.OutputLocale,
		ActorRole:      actorRole,
		CapabilityHint: string(in.Capability),
		HasMetadata:    len(in.RawMetadata) > 0 || len(in.AnonymizedMetadata) > 0,
		Articles:       articles,
	})
	if err != nil {
		return "", nil, err
	}
	promptText := in.RawPrompt
	if promptText == "" {
		promptText = in.AnonymizedPrompt
	}
	payload := map[string]any{
		"response_format": "json",
		"capability_hint": string(in.Capability),
		"engine_profile":  in.EngineProfile,
		"output_locale":   promptprofile.NormalizeOutputLocale(in.OutputLocale),
		"prompt":          promptText,
	}
	if strings.TrimSpace(in.RawInputSQL) != "" {
		payload["input_sql"] = promptprofile.TruncateSQL(in.RawInputSQL)
	}
	if strings.TrimSpace(in.RawPlanText) != "" {
		payload["plan_text"] = in.RawPlanText
	}
	if len(in.RawMetadata) > 0 {
		payload["metadata"] = in.RawMetadata
	}
	if len(in.RawHistory) > 0 {
		payload["history"] = in.RawHistory
	}
	if strings.TrimSpace(in.RawSummary) != "" {
		payload["summary"] = in.RawSummary
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", nil, err
	}
	return sysPrompt, raw, nil
}

func (p *ProviderSynthesizer) callEndpoint(
	ctx context.Context,
	ep provider.Endpoint,
	sysPrompt string,
	userContent string,
	defaultCap domain.Capability,
	onDelta func(kind string, delta string),
) (*SynthesizeOutput, error) {
	resp, err := provider.PostChatCompletionWithOverflowHeal(ctx, p.HTTPClient, ep, provider.ChatCompletionTransportRequest{
		CallerTag:          "assistant_synthesizer",
		ErrorDomain:        "assistant",
		SystemPrompt:       sysPrompt,
		UserContent:        userContent,
		Stream:             onDelta != nil,
		EnsureUserJSONHint: true,
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if onDelta != nil && strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		return consumeProviderSSEStream(resp.Body, defaultCap, onDelta)
	}

	body, err := provider.ReadBoundedResponseBody(resp.Body)
	if err != nil {
		return nil, err
	}

	var parsed struct {
		Choices []struct {
			Message struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int64 `json:"prompt_tokens"`
			CompletionTokens int64 `json:"completion_tokens"`
			InputTokens      int64 `json:"input_tokens"`
			OutputTokens     int64 `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil || len(parsed.Choices) == 0 {
		return nil, errors.New("assistant: invalid chat completion response")
	}

	if onDelta != nil && parsed.Choices[0].Message.ReasoningContent != "" {
		onDelta("reasoning", parsed.Choices[0].Message.ReasoningContent)
	}

	content := strings.TrimSpace(parsed.Choices[0].Message.Content)
	content = stripMarkdownJSONFence(content)

	out, decErr := parseSynthesizeOutput(content, defaultCap)
	if decErr != nil {
		return nil, decErr
	}
	out.AnonymizedSQL = stripMarkdownSQLFence(out.AnonymizedSQL)
	out.InputTokens = parsed.Usage.PromptTokens
	if out.InputTokens == 0 {
		out.InputTokens = parsed.Usage.InputTokens
	}
	out.OutputTokens = parsed.Usage.CompletionTokens
	if out.OutputTokens == 0 {
		out.OutputTokens = parsed.Usage.OutputTokens
	}
	if onDelta != nil && !out.OutOfScope && out.AssistantReply != "" {
		onDelta("delta", out.AssistantReply)
	}
	return out, nil
}

func consumeProviderSSEStream(
	r io.Reader,
	defaultCap domain.Capability,
	onDelta func(kind string, delta string),
) (*SynthesizeOutput, error) {
	scanner := bufio.NewScanner(io.LimitReader(r, provider.MaxResponseByte+1))
	scanner.Buffer(make([]byte, 0, 64*1024), 256*1024)

	var rawBuilder strings.Builder
	var promptTokens, completionTokens int64
	emittedReplyBytes := 0

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content          string `json:"content"`
					ReasoningContent string `json:"reasoning_content"`
				} `json:"delta"`
			} `json:"choices"`
			Usage *struct {
				PromptTokens     int64 `json:"prompt_tokens"`
				CompletionTokens int64 `json:"completion_tokens"`
				InputTokens      int64 `json:"input_tokens"`
				OutputTokens     int64 `json:"output_tokens"`
			} `json:"usage,omitempty"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}
		if chunk.Usage != nil {
			if chunk.Usage.PromptTokens > 0 {
				promptTokens = chunk.Usage.PromptTokens
			} else if chunk.Usage.InputTokens > 0 {
				promptTokens = chunk.Usage.InputTokens
			}
			if chunk.Usage.CompletionTokens > 0 {
				completionTokens = chunk.Usage.CompletionTokens
			} else if chunk.Usage.OutputTokens > 0 {
				completionTokens = chunk.Usage.OutputTokens
			}
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		d := chunk.Choices[0].Delta
		if d.ReasoningContent != "" {
			onDelta("reasoning", d.ReasoningContent)
		}
		if d.Content != "" {
			rawBuilder.WriteString(d.Content)
			currentRaw := stripMarkdownJSONFence(rawBuilder.String())
			if !extractPartialJSONBoolField(currentRaw, "out_of_scope") {
				partialReply := extractPartialJSONStringField(currentRaw, "assistant_reply")
				if len(partialReply) > emittedReplyBytes {
					validEnd := validUTF8PrefixEnd(partialReply, emittedReplyBytes)
					if validEnd > emittedReplyBytes {
						onDelta("delta", partialReply[emittedReplyBytes:validEnd])
						emittedReplyBytes = validEnd
					}
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	content := stripMarkdownJSONFence(strings.TrimSpace(rawBuilder.String()))
	out, err := parseSynthesizeOutput(content, defaultCap)
	if err != nil {
		return nil, err
	}
	out.AnonymizedSQL = stripMarkdownSQLFence(out.AnonymizedSQL)
	out.InputTokens = promptTokens
	out.OutputTokens = completionTokens
	if !out.OutOfScope && len(out.AssistantReply) > emittedReplyBytes {
		onDelta("delta", out.AssistantReply[emittedReplyBytes:])
	}
	return out, nil
}

func validUTF8PrefixEnd(s string, from int) int {
	end := len(s)
	for end > from && !utf8.ValidString(s[from:end]) {
		end--
	}
	return end
}

func parseSynthesizeOutput(content string, defaultCap domain.Capability) (*SynthesizeOutput, error) {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return nil, errors.New("assistant: empty model completion content")
	}
	var out SynthesizeOutput
	if err := json.Unmarshal([]byte(trimmed), &out); err == nil {
		if strings.TrimSpace(out.AnonymizedSQL) == "" && strings.TrimSpace(out.SQL) != "" {
			out.AnonymizedSQL = out.SQL
		}
		return &out, nil
	}
	// If the model returned a JSON object truncated by max_tokens or minor escape quirks, extract structured fields rather than exposing raw JSON.
	if strings.HasPrefix(trimmed, "{") && (strings.Contains(trimmed, `"assistant_reply"`) || strings.Contains(trimmed, `"out_of_scope"`)) {
		out.OutOfScope = extractPartialJSONBoolField(trimmed, "out_of_scope")
		out.NeedsClarify = extractPartialJSONBoolField(trimmed, "needs_clarify")
		out.ResolvedCapability = extractPartialJSONStringField(trimmed, "resolved_capability")
		if out.ResolvedCapability == "" {
			out.ResolvedCapability = string(defaultCap)
		}
		out.SessionTitle = extractPartialJSONStringField(trimmed, "session_title")
		out.AssistantReply = extractPartialJSONStringField(trimmed, "assistant_reply")
		out.AnonymizedSQL = extractPartialJSONStringField(trimmed, "anonymized_sql")
		out.SQL = extractPartialJSONStringField(trimmed, "sql")
		if strings.TrimSpace(out.AnonymizedSQL) == "" && strings.TrimSpace(out.SQL) != "" {
			out.AnonymizedSQL = out.SQL
		}
		out.Assumptions = extractPartialJSONStringArray(trimmed, "assumptions")
		out.NextSteps = extractPartialJSONStringArray(trimmed, "next_steps")
		out.SemanticWarnings = extractPartialJSONStringArray(trimmed, "semantic_warnings")
		out.CitedArticleIDs = extractPartialJSONStringArray(trimmed, "cited_article_ids")
		if out.OutOfScope || out.AssistantReply != "" || out.AnonymizedSQL != "" {
			return &out, nil
		}
	}
	// If the model emitted plain Markdown directly, wrap it safely as AssistantReply.
	return &SynthesizeOutput{
		ResolvedCapability: string(defaultCap),
		AssistantReply:     trimmed,
	}, nil
}

// UnwrapAssistantReplyJSON extracts the inner Markdown reply if a historical persisted message retained an outer JSON envelope.
func UnwrapAssistantReplyJSON(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if !strings.HasPrefix(trimmed, "{") || !strings.Contains(trimmed, `"assistant_reply"`) {
		return raw
	}
	var out SynthesizeOutput
	if err := json.Unmarshal([]byte(trimmed), &out); err == nil && strings.TrimSpace(out.AssistantReply) != "" {
		return out.AssistantReply
	}
	if extracted := extractPartialJSONStringField(trimmed, "assistant_reply"); strings.TrimSpace(extracted) != "" {
		return extracted
	}
	return raw
}

func extractPartialJSONBoolField(raw, key string) bool {
	needle := `"` + key + `"`
	idx := strings.Index(raw, needle)
	if idx < 0 {
		return false
	}
	rest := strings.TrimSpace(raw[idx+len(needle):])
	if !strings.HasPrefix(rest, ":") {
		return false
	}
	rest = strings.TrimSpace(rest[1:])
	return strings.HasPrefix(rest, "true")
}

func extractPartialJSONStringField(raw, key string) string {
	needle := `"` + key + `"`
	idx := strings.Index(raw, needle)
	if idx < 0 {
		return ""
	}
	rest := strings.TrimSpace(raw[idx+len(needle):])
	if !strings.HasPrefix(rest, ":") {
		return ""
	}
	rest = strings.TrimSpace(rest[1:])
	if !strings.HasPrefix(rest, `"`) {
		return ""
	}
	return scanJSONStringLiteral(rest[1:])
}

func extractPartialJSONStringArray(raw, key string) []string {
	needle := `"` + key + `"`
	idx := strings.Index(raw, needle)
	if idx < 0 {
		return nil
	}
	rest := strings.TrimSpace(raw[idx+len(needle):])
	if !strings.HasPrefix(rest, ":") {
		return nil
	}
	rest = strings.TrimSpace(rest[1:])
	if !strings.HasPrefix(rest, "[") {
		return nil
	}
	rest = rest[1:]
	var items []string
	for len(rest) > 0 {
		rest = strings.TrimLeft(rest, " \t\r\n,")
		if len(rest) == 0 || rest[0] == ']' {
			break
		}
		if rest[0] != '"' {
			break
		}
		val, consumed := scanJSONStringLiteralWithLength(rest[1:])
		if val != "" {
			items = append(items, val)
		}
		if consumed < 0 || 1+consumed >= len(rest) {
			break
		}
		rest = rest[1+consumed:]
	}
	return items
}

func scanJSONStringLiteral(s string) string {
	val, _ := scanJSONStringLiteralWithLength(s)
	return val
}

func scanJSONStringLiteralWithLength(s string) (string, int) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ch == '"' {
			return b.String(), i + 1
		}
		if ch == '\\' {
			if i+1 >= len(s) {
				break
			}
			next := s[i+1]
			switch next {
			case 'n':
				b.WriteByte('\n')
				i++
			case 'r':
				b.WriteByte('\r')
				i++
			case 't':
				b.WriteByte('\t')
				i++
			case '"', '\\', '/':
				b.WriteByte(next)
				i++
			default:
				b.WriteByte(next)
				i++
			}
			continue
		}
		b.WriteByte(ch)
	}
	return b.String(), -1
}

func stripMarkdownJSONFence(s string) string {
	trimmed := strings.TrimSpace(s)
	if strings.HasPrefix(trimmed, "```") {
		if nl := strings.IndexByte(trimmed, '\n'); nl >= 0 {
			trimmed = trimmed[nl+1:]
		}
		if end := strings.LastIndex(trimmed, "```"); end >= 0 {
			trimmed = trimmed[:end]
		}
	}
	return strings.TrimSpace(trimmed)
}

func stripMarkdownSQLFence(s string) string {
	trimmed := stripMarkdownJSONFence(s)
	return strings.TrimSpace(trimmed)
}

func isEnglishLocale(locale string) bool {
	trimmed := strings.ToLower(strings.TrimSpace(locale))
	return trimmed == "en-us" || strings.HasPrefix(trimmed, "en")
}

func summarizeAnonymizedTurns(ctx context.Context, turns []AnonymizedTurn) (string, int64, int64, error) {
	if err := ctx.Err(); err != nil {
		return "", 0, 0, err
	}
	var b strings.Builder
	b.WriteString("Anonymized summary of earlier conversation turns (retains task goals and draft references only, with no real identifiers or mappings):\n")
	var inTok int64
	for _, t := range turns {
		inTok += estimateTokenCount(t.Content)
		snippet := strings.TrimSpace(t.Content)
		if len(snippet) > 120 {
			snippet = snippet[:120] + "..."
		}
		fmt.Fprintf(&b, "- [#%d %s/%s]: %s\n", t.Sequence, t.Role, t.Capability, snippet)
	}
	summary := strings.TrimSpace(b.String())
	return summary, inTok, estimateTokenCount(summary), nil
}

func estimateTokenCount(text string) int64 {
	n := int64(len(text)) / 3
	if n < 1 && len(text) > 0 {
		return 1
	}
	return n
}

// ExtractTablesFromSQL extracts candidate table names following FROM / JOIN / INTO / UPDATE / TABLE clauses.
func ExtractTablesFromSQL(sqlText string, profile fingerprint.Profile) []string {
	lex, err := fingerprint.Tokenize([]byte(sqlText), profile.Flags())
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	toks := lex.Tokens
	for i := 0; i < len(toks); i++ {
		if toks[i].Kind != fingerprint.TokWord {
			continue
		}
		kw := strings.ToLower(toks[i].Text)
		if kw != "from" && kw != "join" && kw != "into" && kw != "update" && kw != "table" {
			continue
		}
		if i+1 >= len(toks) {
			continue
		}
		candIdx := i + 1
		// Handle schema.table qualification.
		if candIdx+2 < len(toks) && toks[candIdx+1].IsPunct(".") {
			candIdx = candIdx + 2
		}
		cand := toks[candIdx]
		if cand.Kind == fingerprint.TokWord || cand.Kind == fingerprint.TokQuotedIdent {
			name := cand.Literal
			if cand.Kind == fingerprint.TokQuotedIdent || name == "" {
				name = cand.Text
			}
			low := strings.ToLower(name)
			if low != "select" && low != "set" && low != "where" && low != "(" && !seen[low] {
				seen[low] = true
				out = append(out, name)
			}
		}
	}
	return out
}

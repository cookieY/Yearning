// Package application implements the Database AI Assistant application orchestration:
// session lifecycle, per-turn anonymization and restoration, authorized metadata tools,
// automatic context compaction, formal AI precheck bridging, and idempotent order submission.
package application

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
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
}

// Synthesizer abstracts model generation, SQL optimization, and session summarization.
type Synthesizer interface {
	Synthesize(ctx context.Context, in SynthesizeInput) (*SynthesizeOutput, error)
	Summarize(ctx context.Context, turns []AnonymizedTurn) (string, int64, int64, error)
}

// ProviderSynthesizer invokes the configured OpenAI-compatible AI Provider chain under the Prompt-Based Scope Gate.
// In production (Fallback == nil), Synthesize fails closed with 2011 AI_PROVIDER_FAILED when no enabled AI provider exists;
// offline unit tests may pass DeterministicSynthesizer as an explicit Fallback.
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
	return (&DeterministicSynthesizer{}).Summarize(ctx, turns)
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
	u, err := provider.ParseBaseURL(ep.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("assistant: invalid provider base_url: %w", err)
	}
	endpointURL := strings.TrimRight(u.String(), "/") + "/chat/completions"
	if ep.EffectivePromptProfile() != promptprofile.ProfileSAN && !strings.Contains(strings.ToLower(userContent), "json") {
		userContent += "\n\nRespond with a valid json object."
	}

	promptMsgs := []provider.PromptMessage{
		{Role: "system", Content: sysPrompt},
		{Role: "user", Content: userContent},
	}
	reqBody := map[string]any{
		"model": ep.ModelName,
		"messages": []map[string]string{
			{"role": "system", "content": sysPrompt},
			{"role": "user", "content": userContent},
		},
		"response_format": map[string]string{
			"type": "json_object",
		},
	}
	if onDelta != nil {
		reqBody["stream"] = true
		reqBody["stream_options"] = map[string]any{
			"include_usage": true,
		}
	}
	effMax := provider.ResolveMaxTokens(ep.Configuration, promptMsgs)
	if effMax > 0 {
		reqBody["max_tokens"] = effMax
	}
	if ep.Configuration != nil {
		if v, ok := ep.Configuration["temperature"].(float64); ok {
			reqBody["temperature"] = v
		}
		if v, ok := ep.Configuration["reasoning_effort"].(string); ok && v != "" {
			reqBody["reasoning_effort"] = v
		}
	}

	hc := p.HTTPClient
	if hc == nil {
		hc = provider.NewHTTPClient()
	}

	var resp *http.Response
	healedSentMax := 0
	healedNextMax := 0
	healedMaxCtx := 0
	healedPromptTokens := 0
	for attempt := 0; attempt < 2; attempt++ {
		rawReq, err := json.Marshal(reqBody)
		if err != nil {
			return nil, err
		}
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpointURL, bytes.NewReader(rawReq))
		if err != nil {
			return nil, err
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Authorization", "Bearer "+ep.APIKey)

		resp, err = hc.Inner.Do(httpReq)
		if err != nil {
			if healedSentMax > 0 {
				provider.RecordOverflowHeal(provider.OverflowHealOutcomeFastFail, "assistant_synthesizer", ep.ModelName, healedSentMax, healedNextMax, healedMaxCtx, healedPromptTokens)
			}
			return nil, err
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			if healedSentMax > 0 {
				outcome := provider.OverflowHealOutcomeHealed
				if healedNextMax == 0 {
					outcome = provider.OverflowHealOutcomeFallbackNoMaxTokens
				}
				provider.RecordOverflowHeal(outcome, "assistant_synthesizer", ep.ModelName, healedSentMax, healedNextMax, healedMaxCtx, healedPromptTokens)
			}
			break
		}
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		resp.Body.Close()
		statusErr := &provider.HTTPStatusError{
			Code:     resp.StatusCode,
			Snapshot: provider.UpstreamSnapshot(errBody),
			RawBody:  strings.TrimSpace(string(errBody)),
		}
		if attempt == 0 {
			rawErr := string(errBody)
			retry, nextMax, promptTooLong := provider.ResolveContextOverflowRetry(effMax, resp.StatusCode, rawErr)
			maxCtx, promptTokens, _ := provider.ParseContextLengthOverflow(rawErr)
			if promptTooLong {
				provider.RecordOverflowHeal(provider.OverflowHealOutcomeFastFail, "assistant_synthesizer", ep.ModelName, effMax, 0, maxCtx, promptTokens)
				return nil, fmt.Errorf("assistant: prompt exceeds model context window: %w", statusErr)
			}
			if retry {
				healedSentMax = effMax
				healedNextMax = nextMax
				healedMaxCtx = maxCtx
				healedPromptTokens = promptTokens
				if nextMax > 0 {
					reqBody["max_tokens"] = nextMax
				} else {
					delete(reqBody, "max_tokens")
				}
				effMax = nextMax
				continue
			}
		} else if healedSentMax > 0 {
			provider.RecordOverflowHeal(provider.OverflowHealOutcomeFastFail, "assistant_synthesizer", ep.ModelName, healedSentMax, healedNextMax, healedMaxCtx, healedPromptTokens)
		}
		return nil, statusErr
	}
	defer resp.Body.Close()

	if onDelta != nil && strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		return consumeProviderSSEStream(resp.Body, defaultCap, onDelta)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, provider.MaxResponseByte+1))
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

// DeterministicSynthesizer generates structured anonymized SQL drafts and Markdown explanations from desensitized schema facts.
// It consumes only anonymized tokens (schema_1, table_1, column_1, :val_str_1) and never touches raw data.
type DeterministicSynthesizer struct{}

func (d *DeterministicSynthesizer) Synthesize(ctx context.Context, in SynthesizeInput) (*SynthesizeOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	isEN := isEnglishLocale(in.OutputLocale)
	// Simulate the Prompt-Based Scope Gate rejection for out-of-scope non-database requests in offline tests.
	if help.IsOutOfScope(in.AnonymizedPrompt, string(in.Capability)) {
		msg := "\u6570\u636e\u5e93 AI \u52a9\u624b\u4ec5\u652f\u6301\u300c\u5e73\u53f0\u4f7f\u7528\u54a8\u8be2\u300d\u300c\u81ea\u7136\u8bed\u8a00\u751f\u6210 SQL\u300d\u4e0e\u300cSQL \u5206\u6790\u4f18\u5316\u300d\u4e09\u9879\u80fd\u529b\uff0c\u4e0d\u652f\u6301\u6570\u636e\u5e93\u9886\u57df\u4e4b\u5916\u7684\u8bf7\u6c42\u3002"
		if isEN {
			msg = "The Database AI Assistant only supports Platform Usage Help, Natural Language SQL Generation, and SQL Analysis & Optimization; non-database requests are out of scope."
		}
		return &SynthesizeOutput{
			OutOfScope:         true,
			ResolvedCapability: string(domain.CapabilityOutOfScope),
			AssistantReply:     msg,
			InputTokens:        estimateTokenCount(in.AnonymizedPrompt),
			OutputTokens:       32,
		}, nil
	}
	var out *SynthesizeOutput
	var err error
	switch in.Capability {
	case domain.CapabilitySQLOptimize:
		out, err = d.synthesizeOptimization(in)
	case domain.CapabilitySQLGenerate:
		out, err = d.synthesizeGeneration(in)
	default:
		replyText, citations := help.AnswerPlatformHelpWithLocale(in.Actor, in.AnonymizedPrompt, in.OutputLocale)
		var citedIDs []string
		for _, c := range citations {
			citedIDs = append(citedIDs, c.CitationID)
		}
		out = &SynthesizeOutput{
			ResolvedCapability: string(domain.CapabilityPlatformHelp),
			AssistantReply:     replyText,
			CitedArticleIDs:    citedIDs,
			InputTokens:        estimateTokenCount(in.AnonymizedPrompt),
			OutputTokens:       estimateTokenCount(replyText),
		}
	}
	if err == nil && out != nil && !out.OutOfScope && in.OnDelta != nil && out.AssistantReply != "" {
		in.OnDelta("delta", out.AssistantReply)
	}
	return out, err
}

func (d *DeterministicSynthesizer) synthesizeGeneration(in SynthesizeInput) (*SynthesizeOutput, error) {
	isEN := isEnglishLocale(in.OutputLocale)
	if len(in.AnonymizedMetadata) == 0 {
		msg := "\u4e3a\u4e86\u51c6\u786e\u751f\u6210 SQL\uff0c\u8bf7\u5148\u9009\u62e9\u6216\u8bf4\u660e\u76ee\u6807\u6570\u636e\u6e90\u4e0e\u76ee\u6807\u8868\u540d\u79f0\u3002"
		if isEN {
			msg = "To generate accurate SQL, please first select or specify the target datasource and table name."
		}
		return &SynthesizeOutput{
			AssistantReply: msg,
			NeedsClarify:   true,
			InputTokens:    estimateTokenCount(in.AnonymizedPrompt),
			OutputTokens:   32,
		}, nil
	}

	tbl := in.AnonymizedMetadata[0]
	lowPrompt := strings.ToLower(in.AnonymizedPrompt)

	isInsert := strings.Contains(lowPrompt, "insert") || strings.Contains(lowPrompt, "\u63d2\u5165") || strings.Contains(lowPrompt, "\u65b0\u589e")
	isUpdate := strings.Contains(lowPrompt, "update") || strings.Contains(lowPrompt, "\u66f4\u65b0") || strings.Contains(lowPrompt, "\u4fee\u6539")
	isDelete := strings.Contains(lowPrompt, "delete") || strings.Contains(lowPrompt, "\u5220\u9664")
	isCreateIndex := strings.Contains(lowPrompt, "create index") || strings.Contains(lowPrompt, "\u521b\u5efa\u7d22\u5f15") || strings.Contains(lowPrompt, "\u52a0\u7d22\u5f15")
	isAlterTable := strings.Contains(lowPrompt, "alter table") || strings.Contains(lowPrompt, "\u65b0\u589e\u5b57\u6bb5") || strings.Contains(lowPrompt, "\u6dfb\u52a0\u5b57\u6bb5")

	qualifiedTable := tbl.TableToken
	if tbl.SchemaToken != "" {
		qualifiedTable = tbl.SchemaToken + "." + tbl.TableToken
	}

	if isInsert {
		// Filter out auto-increment, generated, and sequence columns for INSERT.
		var insertCols []tool.AnonymizedColumnFact
		var requiredNoDefault []string
		for _, c := range tbl.Columns {
			if c.DefaultBehavior == tool.DefaultAutoIncrement || c.DefaultBehavior == tool.DefaultGenerated || c.DefaultBehavior == tool.DefaultSequence {
				continue
			}
			insertCols = append(insertCols, c)
			if !c.Nullable && c.DefaultBehavior == tool.DefaultNone {
				requiredNoDefault = append(requiredNoDefault, c.ColumnToken)
			}
		}
		if len(insertCols) == 0 && len(tbl.Columns) > 0 {
			insertCols = tbl.Columns
		}
		// Ask for clarification when required columns without defaults lack bound value placeholders (PRD §5.4-3).
		if len(requiredNoDefault) > len(in.BoundPlaceholders) && len(in.BoundPlaceholders) == 0 {
			msg := "\u76ee\u6807\u8868\u5b58\u5728\u5fc5\u586b\u4e14\u65e0\u9ed8\u8ba4\u503c\u7684\u5b57\u6bb5\uff0c\u8bf7\u5728\u63cf\u8ff0\u4e2d\u8865\u5145\u8981\u63d2\u5165\u7684\u5177\u4f53\u5b57\u6bb5\u503c\uff08\u4f8b\u5982\u7f16\u7801\u3001\u540d\u79f0\u6216\u72b6\u6001\u503c\uff09\u540e\u518d\u751f\u6210\u53ef\u63d0\u4ea4\u7684 INSERT \u8bed\u53e5\u3002"
			if isEN {
				msg = "The target table has required columns without default values. Please provide specific column values in your prompt before generating a submittable INSERT statement."
			}
			return &SynthesizeOutput{
				AssistantReply: msg,
				NeedsClarify:   true,
				InputTokens:    estimateTokenCount(in.AnonymizedPrompt),
				OutputTokens:   48,
			}, nil
		}

		var colTokens []string
		var valTokens []string
		phIdx := 0
		for _, c := range insertCols {
			if phIdx < len(in.BoundPlaceholders) {
				colTokens = append(colTokens, c.ColumnToken)
				valTokens = append(valTokens, in.BoundPlaceholders[phIdx])
				phIdx++
			}
		}
		if len(colTokens) == 0 && len(insertCols) > 0 && len(in.BoundPlaceholders) > 0 {
			colTokens = append(colTokens, insertCols[0].ColumnToken)
			valTokens = append(valTokens, in.BoundPlaceholders[0])
		}

		anonSQL := strings.Join([]string{
			"INSERT INTO",
			qualifiedTable,
			"(" + strings.Join(colTokens, ", ") + ")",
			"VALUES",
			"(" + strings.Join(valTokens, ", ") + ")",
		}, " ")
		if isEN {
			return &SynthesizeOutput{
				AssistantReply: "Generated a single INSERT statement draft from the target table metadata and bound literals. Please verify the restored schema, table, and values in the SQL card below, then run AI Precheck before submitting a change order.",
				AnonymizedSQL:  anonSQL,
				Assumptions: []string{
					"Auto-increment primary keys and server-default columns are left to the database engine.",
					"Business literals are safely bound in column order.",
				},
				NextSteps: []string{
					"Verify the restored schema, table, column names, and values in the SQL draft card.",
					"Click 'Run AI Precheck' to pass the Submission Gate before clicking 'Submit Change Order'.",
				},
				InputTokens:  estimateTokenCount(in.AnonymizedPrompt) + 64,
				OutputTokens: 96,
			}, nil
		}
		return &SynthesizeOutput{
			AssistantReply: "\u5df2\u6839\u636e\u76ee\u6807\u8868\u5143\u6570\u636e\u4e0e\u60a8\u63d0\u4f9b\u7684\u4fe1\u606f\u751f\u6210\u5355\u6761 INSERT \u8bed\u53e5\u8349\u7a3f\uff0c\u5e76\u5b8c\u6210\u4e86\u65b9\u8a00\u8bed\u6cd5\u4e0e\u53c2\u6570\u7ed1\u5b9a\u6821\u9a8c\u3002\u8bf7\u6838\u5bf9\u4e0b\u65b9 SQL \u5361\u7247\u4e2d\u7684\u76ee\u6807\u5e93\u8868\u4e0e\u5b57\u6bb5\u503c\uff0c\u786e\u8ba4\u65e0\u8bef\u540e\u53ef\u5148\u6267\u884c\u6b63\u5f0f AI \u9884\u5ba1\u518d\u63d0\u4ea4\u53d8\u66f4\u5de5\u5355\u3002",
			AnonymizedSQL:  anonSQL,
			Assumptions: []string{
				"\u81ea\u589e\u4e3b\u952e\u6216\u670d\u52a1\u7aef\u9ed8\u8ba4\u503c\u5b57\u6bb5\u4ea4\u7531\u6570\u636e\u5e93\u81ea\u52a8\u751f\u6210\uff0c\u672a\u5728\u663e\u5f0f\u63d2\u5165\u5217\u8868\u4e2d\u8986\u76d6\u3002",
				"\u4e1a\u52a1\u5b57\u9762\u503c\u5df2\u6309\u76ee\u6807\u8868\u5b57\u6bb5\u987a\u5e8f\u4e0e\u7c7b\u578b\u5b89\u5168\u7ed1\u5b9a\u3002",
			},
			NextSteps: []string{
				"\u6838\u5bf9 SQL \u5361\u7247\u4e2d\u8fd8\u539f\u540e\u7684\u5e93\u540d\u3001\u8868\u540d\u3001\u5217\u540d\u4e0e\u63d2\u5165\u503c\u662f\u5426\u7b26\u5408\u9884\u671f\u3002",
				"\u70b9\u51fb\u300c\u6267\u884c AI \u9884\u5ba1\u300d\u901a\u8fc7\u5e73\u53f0\u6b63\u5f0f\u5ba1\u6838\u95e8\u7981\u540e\uff0c\u518d\u70b9\u51fb\u300c\u786e\u8ba4\u63d0\u4ea4\u5de5\u5355\u300d\u3002",
			},
			InputTokens:  estimateTokenCount(in.AnonymizedPrompt) + 64,
			OutputTokens: 96,
		}, nil
	}

	if isUpdate {
		if len(tbl.Columns) == 0 || len(in.BoundPlaceholders) == 0 {
			msg := "\u751f\u6210 UPDATE \u8bed\u53e5\u9700\u8981\u660e\u786e\u8981\u4fee\u6539\u7684\u76ee\u6807\u5b57\u6bb5\u65b0\u503c\u4ee5\u53ca WHERE \u8fc7\u6ee4\u6761\u4ef6\u503c\uff0c\u8bf7\u8865\u5145\u540e\u91cd\u8bd5\u3002"
			if isEN {
				msg = "Generating an UPDATE statement requires both the target column value and a WHERE filter condition value. Please provide them and try again."
			}
			return &SynthesizeOutput{
				AssistantReply: msg,
				NeedsClarify:   true,
				InputTokens:    estimateTokenCount(in.AnonymizedPrompt),
				OutputTokens:   40,
			}, nil
		}
		targetCol := tbl.Columns[0].ColumnToken
		filterCol := tbl.Columns[0].ColumnToken
		if len(tbl.Columns) > 1 {
			targetCol = tbl.Columns[1].ColumnToken
		}
		setPh := in.BoundPlaceholders[0]
		wherePh := in.BoundPlaceholders[0]
		if len(in.BoundPlaceholders) > 1 {
			wherePh = in.BoundPlaceholders[1]
		}
		anonSQL := strings.Join([]string{
			"UPDATE",
			qualifiedTable,
			"SET",
			targetCol, "=", setPh,
			"WHERE",
			filterCol, "=", wherePh,
		}, " ")
		if isEN {
			return &SynthesizeOutput{
				AssistantReply: "Generated an UPDATE statement draft with an explicit WHERE filter. Please verify the affected scope before running AI Precheck and submitting a change order.",
				AnonymizedSQL:  anonSQL,
				Assumptions:    []string{"Uses primary key or unique identifier column in WHERE clause to prevent full-table updates."},
				NextSteps:      []string{"Verify matched rows in Query Workspace (/query) first.", "Run AI Precheck and pass the Submission Gate before submitting."},
				InputTokens:    estimateTokenCount(in.AnonymizedPrompt) + 64,
				OutputTokens:   96,
			}, nil
		}
		return &SynthesizeOutput{
			AssistantReply: "\u5df2\u4e3a\u60a8\u751f\u6210\u5e26 WHERE \u8fc7\u6ee4\u6761\u4ef6\u7684 UPDATE \u8bed\u53e5\u8349\u7a3f\u3002\u8bf7\u5728\u786e\u8ba4\u76ee\u6807\u8bb0\u5f55\u8303\u56f4\u540e\u6267\u884c\u6b63\u5f0f AI \u9884\u5ba1\u4e0e\u5de5\u5355\u63d0\u4ea4\u3002",
			AnonymizedSQL:  anonSQL,
			Assumptions: []string{
				"\u4f7f\u7528\u4e3b\u952e\u6216\u552f\u4e00\u6807\u8bc6\u5217\u4f5c\u4e3a WHERE \u7cbe\u786e\u8fc7\u6ee4\u6761\u4ef6\uff0c\u907f\u514d\u5168\u8868\u66f4\u65b0\u3002",
			},
			NextSteps: []string{
				"\u5148\u5728\u67e5\u8be2\u5de5\u4f5c\u53f0\u6838\u5bf9 WHERE \u6761\u4ef6\u547d\u4e2d\u7684\u884c\u6570\u3002",
				"\u6267\u884c\u6b63\u5f0f AI \u9884\u5ba1\u5e76\u901a\u8fc7 Submission Gate \u540e\u63d0\u4ea4\u53d8\u66f4\u5de5\u5355\u3002",
			},
			InputTokens:  estimateTokenCount(in.AnonymizedPrompt) + 64,
			OutputTokens: 96,
		}, nil
	}

	if isDelete {
		if len(tbl.Columns) == 0 || len(in.BoundPlaceholders) == 0 {
			msg := "\u751f\u6210 DELETE \u8bed\u53e5\u5fc5\u987b\u6307\u5b9a\u660e\u786e\u7684 WHERE \u8fc7\u6ee4\u6761\u4ef6\u503c\uff0c\u8bf7\u8865\u5145\u4e3b\u952e\u6216\u8fc7\u6ee4\u6761\u4ef6\u503c\u3002"
			if isEN {
				msg = "Generating a DELETE statement requires an explicit WHERE filter value. Please specify the primary key or filter condition value."
			}
			return &SynthesizeOutput{
				AssistantReply: msg,
				NeedsClarify:   true,
				InputTokens:    estimateTokenCount(in.AnonymizedPrompt),
				OutputTokens:   40,
			}, nil
		}
		filterCol := tbl.Columns[0].ColumnToken
		wherePh := in.BoundPlaceholders[0]
		anonSQL := strings.Join([]string{
			"DELETE FROM",
			qualifiedTable,
			"WHERE",
			filterCol, "=", wherePh,
		}, " ")
		if isEN {
			return &SynthesizeOutput{
				AssistantReply: "Generated a DELETE statement draft with an explicit WHERE filter. Please verify the target object and filter values before running AI Precheck.",
				AnonymizedSQL:  anonSQL,
				Assumptions:    []string{"Deletes only records matching the exact WHERE filter."},
				NextSteps:      []string{"Verify rollback policy (rollback_enabled) and affected rows before running AI Precheck."},
				InputTokens:    estimateTokenCount(in.AnonymizedPrompt) + 64,
				OutputTokens:   80,
			}, nil
		}
		return &SynthesizeOutput{
			AssistantReply: "\u5df2\u751f\u6210\u5e26\u7cbe\u786e WHERE \u6761\u4ef6\u7684 DELETE \u8bed\u53e5\u8349\u7a3f\u3002\u8bf7\u6838\u5bf9\u76ee\u6807\u5bf9\u8c61\u4e0e\u6761\u4ef6\u503c\u540e\u518d\u53d1\u8d77 AI \u9884\u5ba1\u4e0e\u53d8\u66f4\u5de5\u5355\u3002",
			AnonymizedSQL:  anonSQL,
			Assumptions: []string{
				"\u4ec5\u5220\u9664\u547d\u4e2d WHERE \u7cbe\u786e\u8fc7\u6ee4\u6761\u4ef6\u7684\u76ee\u6807\u8bb0\u5f55\u3002",
			},
			NextSteps: []string{
				"\u786e\u8ba4\u56de\u6eda\u7b56\u7565\uff08rollback_enabled\uff09\u4e0e\u5f71\u54cd\u8303\u56f4\u540e\u6267\u884c\u6b63\u5f0f AI \u9884\u5ba1\u3002",
			},
			InputTokens:  estimateTokenCount(in.AnonymizedPrompt) + 64,
			OutputTokens: 80,
		}, nil
	}

	if isCreateIndex && len(tbl.Columns) > 0 {
		targetCol := tbl.Columns[0].ColumnToken
		if len(tbl.Columns) > 1 {
			targetCol = tbl.Columns[1].ColumnToken
		}
		idxTok := "index_1"
		if len(tbl.Indexes) > 0 {
			idxTok = tbl.Indexes[0].IndexToken
		}
		anonSQL := fmt.Sprintf("CREATE INDEX %s ON %s (%s)", idxTok, qualifiedTable, targetCol)
		if isEN {
			return &SynthesizeOutput{
				AssistantReply: "Generated a CREATE INDEX DDL draft. Please verify the index name and column order before running AI Precheck.",
				AnonymizedSQL:  anonSQL,
				Assumptions:    []string{"Creates a single-column B-Tree index on the target column."},
				NextSteps:      []string{"Run formal AI Precheck and evaluate the maintenance window before submitting."},
				InputTokens:    estimateTokenCount(in.AnonymizedPrompt) + 64,
				OutputTokens:   80,
			}, nil
		}
		return &SynthesizeOutput{
			AssistantReply: "\u5df2\u751f\u6210 CREATE INDEX \u7d22\u5f15\u53d8\u66f4\u8bed\u53e5\u8349\u7a3f\uff0c\u8bf7\u6838\u5bf9\u7d22\u5f15\u540d\u4e0e\u5217\u987a\u5e8f\u540e\u6267\u884c AI \u9884\u5ba1\u3002",
			AnonymizedSQL:  anonSQL,
			Assumptions:    []string{"\u6309\u76ee\u6807\u5217\u521b\u5efa\u5355\u5217 B-Tree \u7d22\u5f15\u3002"},
			NextSteps:      []string{"\u6267\u884c\u6b63\u5f0f AI \u9884\u5ba1\u5e76\u8bc4\u4f30\u5efa\u8868/\u5efa\u7d22\u5f15\u7a97\u53e3\u671f\u540e\u63d0\u4ea4\u53d8\u66f4\u5de5\u5355\u3002"},
			InputTokens:    estimateTokenCount(in.AnonymizedPrompt) + 64,
			OutputTokens:   80,
		}, nil
	}

	if isAlterTable && len(tbl.Columns) > 0 {
		targetCol := tbl.Columns[len(tbl.Columns)-1].ColumnToken
		anonSQL := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s VARCHAR(64)", qualifiedTable, targetCol)
		if isEN {
			return &SynthesizeOutput{
				AssistantReply: "Generated an ALTER TABLE schema change draft. Please verify the column type and nullability constraints before running AI Precheck.",
				AnonymizedSQL:  anonSQL,
				Assumptions:    []string{"Added column defaults to nullable to minimize online DDL table lock impact."},
				NextSteps:      []string{"Run formal AI Precheck and submit the DDL change order."},
				InputTokens:    estimateTokenCount(in.AnonymizedPrompt) + 64,
				OutputTokens:   80,
			}, nil
		}
		return &SynthesizeOutput{
			AssistantReply: "\u5df2\u751f\u6210 ALTER TABLE \u7ed3\u6784\u53d8\u66f4\u8349\u7a3f\uff0c\u8bf7\u6838\u5bf9\u5b57\u6bb5\u7c7b\u578b\u4e0e\u53ef\u7a7a\u7ea6\u675f\u540e\u53d1\u8d77\u6b63\u5f0f AI \u9884\u5ba1\u3002",
			AnonymizedSQL:  anonSQL,
			Assumptions:    []string{"\u65b0\u589e\u5b57\u6bb5\u9ed8\u8ba4\u5141\u8bb8\u4e3a\u7a7a\u4ee5\u51cf\u5c0f\u5728\u7ebf DDL \u9501\u8868\u5f71\u54cd\u3002"},
			NextSteps:      []string{"\u6267\u884c\u6b63\u5f0f AI \u9884\u5ba1\u5e76\u6309\u89c4\u8303\u63d0\u4ea4 DDL \u53d8\u66f4\u5de5\u5355\u3002"},
			InputTokens:    estimateTokenCount(in.AnonymizedPrompt) + 64,
			OutputTokens:   80,
		}, nil
	}

	// Default to generating a read-only SELECT statement with an explicit column projection.
	var selectCols []string
	for i, c := range tbl.Columns {
		if i >= 6 {
			break
		}
		selectCols = append(selectCols, c.ColumnToken)
	}
	if len(selectCols) == 0 {
		selectCols = []string{"1"}
	}
	var anonSQL string
	if len(in.BoundPlaceholders) > 0 && len(tbl.Columns) > 0 {
		filterCol := tbl.Columns[0].ColumnToken
		if len(tbl.Columns) > 1 {
			filterCol = tbl.Columns[1].ColumnToken
		}
		anonSQL = strings.Join([]string{
			"SELECT",
			strings.Join(selectCols, ", "),
			"FROM",
			qualifiedTable,
			"WHERE",
			filterCol, "=", in.BoundPlaceholders[0],
			"LIMIT 100",
		}, " ")
	} else {
		anonSQL = strings.Join([]string{
			"SELECT",
			strings.Join(selectCols, ", "),
			"FROM",
			qualifiedTable,
			"LIMIT 100",
		}, " ")
	}

	if isEN {
		return &SynthesizeOutput{
			AssistantReply: "Generated a read-only SELECT query draft with an explicit column projection and a default `LIMIT 100` guard. You can copy this SQL and run it in Query Workspace (`/query`).",
			AnonymizedSQL:  anonSQL,
			Assumptions: []string{
				"Uses explicit column projection instead of SELECT * to reduce scan and network overhead.",
				"Appends LIMIT 100 by default to prevent unbounded result-set scans.",
			},
			NextSteps: []string{
				"Copy the restored SQL and run it inside an active session in Query Workspace (/query).",
			},
			InputTokens:  estimateTokenCount(in.AnonymizedPrompt) + 64,
			OutputTokens: 96,
		}, nil
	}
	return &SynthesizeOutput{
		AssistantReply: "\u5df2\u6839\u636e\u76ee\u6807\u8868\u5b57\u6bb5\u7ed3\u6784\u4e3a\u60a8\u751f\u6210\u663e\u5f0f\u5217\u6e05\u5355\u7684\u53ea\u8bfb SELECT \u67e5\u8be2\u8349\u7a3f\uff08\u9ed8\u8ba4\u9644\u5e26 LIMIT 100 \u4fdd\u62a4\u4e0a\u754c\uff09\u3002\u60a8\u53ef\u4ee5\u590d\u5236\u8be5 SQL \u5230\u300c\u67e5\u8be2\u5de5\u4f5c\u53f0\u300d\uff08/query\uff09\u4e2d\u6267\u884c\u3002",
		AnonymizedSQL:  anonSQL,
		Assumptions: []string{
			"\u4f7f\u7528\u663e\u5f0f\u5b57\u6bb5\u5217\u8868\u66ff\u4ee3 SELECT *\uff0c\u51cf\u5c11\u4e0d\u5fc5\u8981\u7684\u5927\u5b57\u6bb5\u626b\u63cf\u4e0e\u7f51\u7edc\u5f00\u9500\u3002",
			"\u9ed8\u8ba4\u9644\u52a0 LIMIT 100 \u9632\u6b62\u65e0\u754c\u7ed3\u679c\u96c6\u626b\u63cf\u3002",
		},
		NextSteps: []string{
			"\u590d\u5236\u8fd8\u539f\u540e\u7684 SQL \u524d\u5f80\u67e5\u8be2\u5de5\u4f5c\u53f0\uff08/query\uff09\u5728\u6709\u6548\u67e5\u8be2\u4f1a\u8bdd\u4e2d\u8fd0\u884c\u3002",
		},
		InputTokens:  estimateTokenCount(in.AnonymizedPrompt) + 64,
		OutputTokens: 96,
	}, nil
}

func (d *DeterministicSynthesizer) synthesizeOptimization(in SynthesizeInput) (*SynthesizeOutput, error) {
	isEN := isEnglishLocale(in.OutputLocale)
	rawAnon := strings.TrimSpace(in.AnonymizedInputSQL)
	if rawAnon == "" {
		msg := "\u8bf7\u63d0\u4f9b\u9700\u8981\u5206\u6790\u548c\u4f18\u5316\u7684 SQL \u8bed\u53e5\u4ee5\u53ca\u76ee\u6807\u6570\u636e\u6e90\u3002"
		if isEN {
			msg = "Please provide the SQL statement to analyze and optimize along with the target datasource."
		}
		return &SynthesizeOutput{
			AssistantReply: msg,
			NeedsClarify:   true,
			InputTokens:    estimateTokenCount(in.AnonymizedPrompt),
			OutputTokens:   32,
		}, nil
	}

	var (
		rewritten        = rawAnon
		reasons          []string
		semanticWarnings []string
	)

	// 1. Rewrite SELECT * into an explicit column projection when table column metadata is available.
	if len(in.AnonymizedMetadata) > 0 && len(in.AnonymizedMetadata[0].Columns) > 0 {
		tbl := in.AnonymizedMetadata[0]
		var colList []string
		for i, c := range tbl.Columns {
			if i >= 8 {
				break
			}
			colList = append(colList, c.ColumnToken)
		}
		upper := strings.ToUpper(rewritten)
		if strings.HasPrefix(upper, "SELECT * FROM ") {
			restClause := rewritten[len("SELECT * "):]
			rewritten = strings.Join([]string{"SELECT", strings.Join(colList, ", "), restClause}, " ")
			if isEN {
				reasons = append(reasons, "Rewrote `SELECT *` into an explicit column projection to avoid reading redundant columns and improve covering-index eligibility.")
				semanticWarnings = append(semanticWarnings, "Projected columns were narrowed from `*` to explicit business columns; verify the column list if callers depend on full column ordering.")
			} else {
				reasons = append(reasons, "\u5c06 `SELECT *` \u6539\u5199\u4e3a\u663e\u5f0f\u5217\u6295\u5f71\uff0c\u907f\u514d\u8bfb\u53d6\u5197\u4f59\u5217\u5e76\u63d0\u5347\u8986\u76d6\u7d22\u5f15\uff08Covering Index\uff09\u547d\u4e2d\u673a\u4f1a\u3002")
				semanticWarnings = append(semanticWarnings, "\u6295\u5f71\u5217\u5df2\u7531 `*` \u6536\u655b\u4e3a\u663e\u5f0f\u4e1a\u52a1\u5217\uff1b\u82e5\u8c03\u7528\u65b9\u4f9d\u8d56\u5168\u91cf\u5217\u987a\u5e8f\u8bf7\u6838\u5bf9\u5b57\u6bb5\u6e05\u5355\u3002")
			}
		}
	}

	// 2. Append LIMIT 100 when a SELECT statement has no LIMIT and record an explicit semantic warning (PRD §5.5-5).
	upper := strings.ToUpper(rewritten)
	if strings.HasPrefix(upper, "SELECT ") && !strings.Contains(upper, " LIMIT ") && !strings.Contains(upper, " COUNT") {
		rewritten = rewritten + " LIMIT 100"
		if isEN {
			reasons = append(reasons, "Appended `LIMIT 100` upper-bound protection to prevent unbounded result-set scans.")
			semanticWarnings = append(semanticWarnings, "Added `LIMIT 100` pagination upper bound, which may change the number of returned rows (non-strict equivalence); adjust according to your pagination requirements.")
		} else {
			reasons = append(reasons, "\u8865\u5145 `LIMIT 100` \u4e0a\u754c\u4fdd\u62a4\uff0c\u9632\u6b62\u5728\u7ebf\u67e5\u8be2\u626b\u63cf\u5e76\u4f20\u8f93\u8fc7\u5927\u7ed3\u679c\u96c6\u3002")
			semanticWarnings = append(semanticWarnings, "\u6539\u5199\u65b0\u589e\u4e86 `LIMIT 100` \u5206\u9875\u4e0a\u754c\uff0c\u53ef\u80fd\u6539\u53d8\u8fd4\u56de\u7ed3\u679c\u96c6\u884c\u6570\uff0c\u975e\u4e25\u683c\u7b49\u4ef7\u6539\u5199\uff0c\u8bf7\u6839\u636e\u5b9e\u9645\u5206\u9875\u9700\u6c42\u8c03\u6574\u3002")
		}
	}

	if len(reasons) == 0 {
		if isEN {
			reasons = append(reasons, "Preserved original predicates and join semantics; verify composite index leading columns on filter predicates to reduce scan cost.")
		} else {
			reasons = append(reasons, "\u4fdd\u6301\u539f\u6709\u8c13\u8bcd\u4e0e\u8fde\u63a5\u8bed\u4e49\u4e0d\u53d8\uff0c\u5efa\u8bae\u6838\u5bf9\u8fc7\u6ee4\u5217\u4e0a\u7684\u8054\u5408\u7d22\u5f15\u524d\u5bfc\u5217\u987a\u5e8f\u4ee5\u964d\u4f4e\u626b\u63cf\u6210\u672c\u3002")
		}
	}
	if strings.TrimSpace(in.AnonymizedPlanText) != "" {
		if isEN {
			reasons = append(reasons, "Evaluated access paths against the anonymized execution plan text you provided; verify index selectivity in a read-only environment (note: the AI Assistant never connects to databases to execute SQL or EXPLAIN).")
		} else {
			reasons = append(reasons, "\u5df2\u7ed3\u5408\u60a8\u63d0\u4f9b\u7684\u8131\u654f\u6267\u884c\u8ba1\u5212\u6587\u672c\u8bc4\u4f30\u8bbf\u95ee\u8def\u5f84\uff1b\u5efa\u8bae\u5728\u6d4b\u8bd5\u6216\u53ea\u8bfb\u73af\u5883\u590d\u6838\u7d22\u5f15\u9009\u62e9\u6027\uff08\u6ce8\uff1aAI \u52a9\u624b\u4e0d\u4f1a\u81ea\u884c\u8fde\u63a5\u6570\u636e\u5e93\u6267\u884c SQL \u6216\u8fd0\u884c EXPLAIN\uff09\u3002")
		}
	}

	if isEN {
		reply := "Completed SQL structure and index metadata analysis:\n- " + strings.Join(reasons, "\n- ") +
			"\n\n**Performance & Equivalence Note**: The recommendations above are statically derived from table and index metadata without live database execution; please verify any column projection or pagination boundary changes first."
		return &SynthesizeOutput{
			AssistantReply:   reply,
			AnonymizedSQL:    rewritten,
			Assumptions:      reasons,
			SemanticWarnings: semanticWarnings,
			NextSteps: []string{
				"Verify that the optimized filter predicates, projected columns, and pagination bounds match business expectations.",
				"If adding an index or modifying data is required, run formal AI Precheck first and submit via a change order.",
			},
			InputTokens:  estimateTokenCount(in.AnonymizedPrompt+in.AnonymizedInputSQL+in.AnonymizedPlanText) + 96,
			OutputTokens: 128,
		}, nil
	}

	reply := "\u5df2\u5b8c\u6210 SQL \u7ed3\u6784\u4e0e\u7d22\u5f15\u5143\u6570\u636e\u5bf9\u7167\u5206\u6790\uff1a\n- " + strings.Join(reasons, "\n- ") +
		"\n\n**\u6027\u80fd\u4e0e\u7b49\u4ef7\u6027\u8bf4\u660e**\uff1a\u4ee5\u4e0b\u5efa\u8bae\u57fa\u4e8e\u8868\u7ed3\u6784\u4e0e\u7d22\u5f15\u5143\u6570\u636e\u9759\u6001\u63a8\u5bfc\uff0c\u672a\u7ecf\u76ee\u6807\u5e93\u5b9e\u6d4b\u6267\u884c\uff1b\u5982\u6539\u5199\u6d89\u53ca\u5217\u88c1\u526a\u6216\u5206\u9875\u4e0a\u754c\uff0c\u8bf7\u5148\u6309\u8bed\u4e49\u5dee\u5f02\u63d0\u793a\u9a8c\u8bc1\u3002"

	return &SynthesizeOutput{
		AssistantReply:   reply,
		AnonymizedSQL:    rewritten,
		Assumptions:      reasons,
		SemanticWarnings: semanticWarnings,
		NextSteps: []string{
			"\u6838\u5bf9\u4f18\u5316\u540e SQL \u7684\u8fc7\u6ee4\u6761\u4ef6\u3001\u8fd4\u56de\u5217\u4e0e\u5206\u9875\u8fb9\u754c\u662f\u5426\u7b26\u5408\u4e1a\u52a1\u9884\u671f\u3002",
			"\u5982\u6d89\u53ca\u65b0\u5efa\u7d22\u5f15\u6216\u53d8\u66f4\u6570\u636e\uff0c\u8bf7\u5148\u6267\u884c\u6b63\u5f0f AI \u9884\u5ba1\u5e76\u901a\u8fc7\u5de5\u5355\u6d41\u7a0b\u63d0\u4ea4\u3002",
		},
		InputTokens:  estimateTokenCount(in.AnonymizedPrompt+in.AnonymizedInputSQL+in.AnonymizedPlanText) + 96,
		OutputTokens: 128,
	}, nil
}

func (d *DeterministicSynthesizer) Summarize(ctx context.Context, turns []AnonymizedTurn) (string, int64, int64, error) {
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
	var rawJSON []byte
	_ = json.Unmarshal(rawJSON, &out)
	return out
}

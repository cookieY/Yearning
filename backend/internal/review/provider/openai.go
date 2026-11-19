// HighestSeverityOf returns the highest severity among a list of findings.
package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/cookieY/YearningX/backend/internal/review/promptprofile"
)

// run.go defines the review_runs.state lifecycle (schema 000002 + ai-review-runtime-contract). Terminal Run states are immutable; submitted reviews are never recomputed (R003).
type OpenAIAdapter struct {
	Endpoint Endpoint
	Client   *HTTPClient
}

// RunState is the controlled enum for review_runs.state.
const (
	OutputModeJSONSchema = "json_schema"
	OutputModeJSONObject = "json_object"
	ConfigKeyOutputMode  = "structured_output_mode"
)

func configuredOutputMode(cfg map[string]any) string {
	if cfg[ConfigKeyOutputMode] == OutputModeJSONObject {
		return OutputModeJSONObject
	}
	return OutputModeJSONSchema
}

// ValidRunState reports whether s is a valid contract state.
func jsonObjectPromptSuffix() string {
	b, err := json.Marshal(compileOutputSchema())
	if err != nil {
		return ""
	}
	return "\n\n=== OUTPUT FORMAT (mandatory) ===\n" +
		"Respond with a single json object strictly conforming to this JSON Schema. " +
		"Do not add fields; do not wrap the json in markdown fences. " +
		"Constraints: schema_version MUST be 1; review_run_id MUST equal the input review_run_id; " +
		"finding_key MUST match ^[a-z][a-z0-9._-]{0,127}$; fingerprint_group_ids MUST be non-empty UUIDs from anonymized_groups; " +
		"if fact_basis is 'model_only', evidence_ids MUST be []; if fact_basis is 'evidence', evidence_ids MUST be non-empty tool_call_id UUIDs from evidence_references; " +
		"every tool_requests[].tool_call_id MUST be a valid UUID v4 string (e.g. '550e8400-e29b-41d4-a716-446655440000'). " +
		"Keep internal reasoning concise so the JSON output is never truncated:\n" + string(b)
}

// RunTerminal reports whether s is a terminal Run state (immutable).
func NewOpenAIAdapter(ep Endpoint, hc *HTTPClient) (*OpenAIAdapter, error) {
	if err := ep.Validate(); err != nil {
		return nil, err
	}
	if hc == nil {
		hc = NewHTTPClient()
	}
	return &OpenAIAdapter{Endpoint: ep, Client: hc}, nil
}

type sanWireRequest struct {
	RequestSchemaVersion      int                     `json:"request_schema_version"`
	ReviewRunID               string                  `json:"review_run_id"`
	EngineProfile             string                  `json:"engine_profile"`
	AnonymizedStatementGroups []AnonymizedGroup       `json:"anonymized_statement_groups"`
	NormalizedEvidence        []NormalizedEvidenceRef `json:"normalized_evidence"`
	AllowedToolDescriptors    []ToolDescriptor        `json:"allowed_tool_descriptors"`
}

// RunTrigger is a lifecycle trigger for a Run.
type chatRequest struct {
	Model           string          `json:"model"`
	Messages        []chatMessage   `json:"messages"`
	ResponseFormat  *responseFormat `json:"response_format,omitempty"`
	MaxTokens       int             `json:"max_tokens,omitempty"`
	Temperature     *float64        `json:"temperature,omitempty"`
	ReasoningEffort string          `json:"reasoning_effort,omitempty"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type responseFormat struct {
	Type       string            `json:"type"`
	JSONSchema *jsonSchemaFormat `json:"json_schema,omitempty"`
}

type jsonSchemaFormat struct {
	Name   string `json:"name"`
	Strict bool   `json:"strict"`
	Schema any    `json:"schema"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Type    string `json:"type"`
		Code    any    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
	// RunEventStarted indicates a worker claimed the task and started execution.
	Usage json.RawMessage `json:"usage,omitempty"`
}

// RunEventCompleted indicates all stages passed (gate passed).
type Usage struct {
	InputTokens  int64
	OutputTokens int64
}

func (a *OpenAIAdapter) probeRequest() Request {
	if a.Endpoint.EffectivePromptProfile() == promptprofile.ProfileSAN {
		return ProbeRequestSAN()
	}
	return ProbeRequest()
}

// RunEventFailed indicates the run failed (parse/fingerprint/anonymization/internal error, fail closed).
func (a *OpenAIAdapter) ValidateConfiguration(ctx context.Context) error {
	_, err := a.ReviewStructured(ctx, a.probeRequest())
	return err
}

// RunEventIncomplete indicates some stages did not complete (timeout/cancellation/budget/partial failure).
func (a *OpenAIAdapter) Health(ctx context.Context) error {
	return a.ValidateConfiguration(ctx)
}

// RunEventFailed indicates the run failed (parse/fingerprint/anonymization/internal error, fail closed).
func (a *OpenAIAdapter) ReviewStructured(ctx context.Context, req Request) (CallOutcome, error) {
	mode := configuredOutputMode(a.Endpoint.Configuration)
	out, err := a.reviewStructuredMode(ctx, req, mode)
	if err != nil && mode == OutputModeJSONSchema && isFormatStatusError(err) {
		fallbackOut, fallbackErr := a.reviewStructuredMode(ctx, req, OutputModeJSONObject)
		fallbackOut.Calls = append(out.Calls, fallbackOut.Calls...)
		fallbackOut.Usage.InputTokens += out.Usage.InputTokens
		fallbackOut.Usage.OutputTokens += out.Usage.OutputTokens
		return fallbackOut, fallbackErr
	}
	return out, err
}

func isFormatStatusError(err error) bool {
	var se *statusError
	if !errors.As(err, &se) {
		return false
	}
	if IsContextLengthOverflowError(se.code, se.bodyText()) {
		return false
	}
	if se.code == http.StatusBadRequest || se.code == http.StatusUnprocessableEntity {
		return true
	}
	return isRelayFormatError(se)
}

// RunEventInputsChanged indicates a draft revision or workflow configuration change invalidated the Run.
func (a *OpenAIAdapter) NegotiateOutputMode(ctx context.Context) (string, error) {
	probe := a.probeRequest()
	if _, err := a.reviewStructuredMode(ctx, probe, OutputModeJSONSchema); err == nil {
		return OutputModeJSONSchema, nil
	} else if !shouldFallbackToJSONObject(err) {
		return "", err
	}
	if _, err := a.reviewStructuredMode(ctx, probe, OutputModeJSONObject); err != nil {
		return "", err
	}
	return OutputModeJSONObject, nil
}

func shouldFallbackToJSONObject(err error) bool {
	if errors.Is(err, ErrSchemaInvalid) {
		return true
	}
	var se *statusError
	if !errors.As(err, &se) {
		return false
	}
	if IsContextLengthOverflowError(se.code, se.bodyText()) {
		return false
	}
	if se.code == http.StatusBadRequest || se.code == http.StatusUnprocessableEntity {
		return true
	}
	return isRelayFormatError(se)
}

func isRelayFormatError(se *statusError) bool {
	if se == nil || se.code < http.StatusInternalServerError {
		return false
	}
	lower := strings.ToLower(se.snapshot)
	return strings.Contains(lower, "json_schema") ||
		strings.Contains(lower, "json_object") ||
		strings.Contains(lower, "response_format")
}

func (a *OpenAIAdapter) reviewStructuredMode(ctx context.Context, req Request, mode string) (CallOutcome, error) {
	isSAN := a.Endpoint.EffectivePromptProfile() == promptprofile.ProfileSAN
	sys := req.SystemPrompt
	if sys == "" {
		sys = "You are a database change review engine. Respond ONLY with JSON matching the provided schema."
	}
	if !isSAN && !strings.Contains(sys, "=== OUTPUT FORMAT (mandatory) ===") {
		sys += jsonObjectPromptSuffix()
	}
	var rf *responseFormat
	switch mode {
	case OutputModeJSONObject:
		rf = &responseFormat{Type: OutputModeJSONObject}
	default:
		rf = &responseFormat{
			Type: OutputModeJSONSchema,
			JSONSchema: &jsonSchemaFormat{
				Name: "yearning_review_output", Strict: true,
				Schema: compileOutputSchema(),
			},
		}
	}
	// Terminal Runs transition uniformly to outdated when draft revision or workflow configuration changes (R003 reuse precondition: a reused conclusion must belong to the current revision).
	var payloadBytes []byte
	var err error
	if isSAN {
		groups := make([]AnonymizedGroup, len(req.AnonymizedStatementGroups))
		for i, grp := range req.AnonymizedStatementGroups {
			groups[i] = grp
			groups[i].RepresentativeAnonymizedSQL = promptprofile.TruncateSQL(grp.RepresentativeAnonymizedSQL)
		}
		ev := req.NormalizedEvidence
		if ev == nil {
			ev = []NormalizedEvidenceRef{}
		}
		wire := sanWireRequest{
			RequestSchemaVersion:      req.RequestSchemaVersion,
			ReviewRunID:               req.ReviewRunID,
			EngineProfile:             req.EngineProfile,
			AnonymizedStatementGroups: groups,
			NormalizedEvidence:        ev,
			AllowedToolDescriptors:    []ToolDescriptor{},
		}
		payloadBytes, err = json.Marshal(wire)
	} else {
		payloadCopy := req
		payloadCopy.SystemPrompt = ""
		payloadBytes, err = json.Marshal(payloadCopy)
	}
	if err != nil {
		return CallOutcome{}, err
	}
	user := "Review request JSON payload follows. Respond ONLY with a single json object matching the required JSON Schema.\n" + string(payloadBytes)
	msgs := []chatMessage{{Role: "system", Content: sys}, {Role: "user", Content: user}}
	cr := chatRequest{
		Model:           a.Endpoint.ModelName,
		Messages:        msgs,
		ResponseFormat:  rf,
		MaxTokens:       ResolveMaxTokens(a.Endpoint.Configuration, toPromptMessages(msgs)),
		Temperature:     configFloatPtr(a.Endpoint.Configuration, "temperature"),
		ReasoningEffort: configString(a.Endpoint.Configuration, "reasoning_effort"),
	}
	remainingBudget := maxHTTPRequestsPerStructuredCall
	body, usage, calls, effectiveMax, healed, err := a.postWithOverflowRetry(ctx, cr, true, &remainingBudget)
	if err != nil {
		return CallOutcome{Calls: calls}, err
	}
	out, decodeErr := decodeStructured(body, req.ReviewRunID)
	if decodeErr == nil {
		return CallOutcome{Response: out, Usage: usage, Calls: calls}, nil
	}
	// RunTransition returns the target state for trigger on s. Result terminal states (ready/blocked/partial/failed/outdated) cannot advance further; the sole exception is inputs_changed invalidation: a reused conclusion must belong to the current revision and workflow configuration, so input changes rewrite any non-outdated terminal state to outdated.
	markLastCallOutcome(calls, CallOutcomeSchemaFailed)
	if remainingBudget <= 0 {
		return CallOutcome{Calls: calls}, fmt.Errorf("%w: %v", ErrSchemaInvalid, decodeErr)
	}
	// RunOutcomeFailedStates is the set of terminal states that mark a Run as failed (Run-side mapping of submission_gate.block_outcomes).
	repair := cr
	if len(repair.Messages) == 2 {
		repair.Messages = append(repair.Messages,
			chatMessage{Role: "assistant", Content: string(body)},
			chatMessage{Role: "user", Content: "The previous response violated the required JSON schema (" +
				decodeErr.Error() + "). Respond again with JSON strictly matching the schema. Do not add fields."})
	} else {
		return CallOutcome{Calls: calls}, fmt.Errorf("%w: %v", ErrSchemaInvalid, decodeErr)
	}
	if healed && effectiveMax == 0 {
		// Initial request overflowed with an unparseable error and succeeded with max_tokens omitted;
		// keep max_tokens omitted on the larger repair prompt.
		repair.MaxTokens = 0
	} else {
		repair.MaxTokens = ResolveMaxTokens(a.Endpoint.Configuration, toPromptMessages(repair.Messages))
		if effectiveMax > 0 && repair.MaxTokens > effectiveMax {
			repair.MaxTokens = effectiveMax
		}
	}
	body2, usage2, calls2, _, _, err := a.postWithOverflowRetry(ctx, repair, !healed, &remainingBudget)
	calls = append(calls, calls2...)
	if err != nil {
		return CallOutcome{Calls: calls}, err
	}
	out2, decodeErr2 := decodeStructured(body2, req.ReviewRunID)
	if decodeErr2 != nil {
		markLastCallOutcome(calls, CallOutcomeSchemaFailed)
		return CallOutcome{Calls: calls}, fmt.Errorf("%w: %v", ErrSchemaInvalid, decodeErr2)
	}
	usage.InputTokens += usage2.InputTokens
	usage.OutputTokens += usage2.OutputTokens
	return CallOutcome{Response: out2, Usage: usage, Calls: calls}, nil
}

func toPromptMessages(msgs []chatMessage) []PromptMessage {
	out := make([]PromptMessage, len(msgs))
	for i, m := range msgs {
		out[i] = PromptMessage{Role: m.Role, Content: m.Content}
	}
	return out
}

func (a *OpenAIAdapter) postWithOverflowRetry(ctx context.Context, cr chatRequest, allowRetry bool, remaining *int) ([]byte, Usage, []CallRecord, int, bool, error) {
	body, usage, calls, err := a.postBounded(ctx, cr, remaining)
	if err == nil {
		return body, usage, calls, cr.MaxTokens, false, nil
	}
	var se *statusError
	if allowRetry && cr.MaxTokens > 0 && errors.As(err, &se) {
		rawBody := se.bodyText()
		retry, nextMax, promptTooLong := ResolveContextOverflowRetry(cr.MaxTokens, se.code, rawBody)
		maxCtx, promptTokens, _ := ParseContextLengthOverflow(rawBody)
		if promptTooLong {
			RecordOverflowHeal(OverflowHealOutcomeFastFail, "review_provider", a.Endpoint.ModelName, cr.MaxTokens, 0, maxCtx, promptTokens)
			return nil, Usage{}, calls, cr.MaxTokens, false, fmt.Errorf("provider: prompt exceeds model context window: %w", err)
		}
		if retry && *remaining > 0 {
			retryReq := cr
			retryReq.MaxTokens = nextMax
			body2, usage2, calls2, err2 := a.postBounded(ctx, retryReq, remaining)
			calls = append(calls, calls2...)
			if err2 != nil {
				RecordOverflowHeal(OverflowHealOutcomeFastFail, "review_provider", a.Endpoint.ModelName, cr.MaxTokens, nextMax, maxCtx, promptTokens)
			} else if nextMax == 0 {
				RecordOverflowHeal(OverflowHealOutcomeFallbackNoMaxTokens, "review_provider", a.Endpoint.ModelName, cr.MaxTokens, 0, maxCtx, promptTokens)
			} else {
				RecordOverflowHeal(OverflowHealOutcomeHealed, "review_provider", a.Endpoint.ModelName, cr.MaxTokens, nextMax, maxCtx, promptTokens)
			}
			return body2, usage2, calls, nextMax, true, err2
		}
	}
	return nil, Usage{}, calls, cr.MaxTokens, false, err
}

// StageReviewState is the controlled enum for stage_review_snapshots.state.
func markLastCallOutcome(calls []CallRecord, outcome string) {
	if len(calls) > 0 {
		calls[len(calls)-1].Outcome = outcome
	}
}

func unwrapJSONEnvelope(body []byte) []byte {
	trimmed := bytes.TrimSpace(body)
	for bytes.HasPrefix(trimmed, []byte("<think>")) {
		idx := bytes.Index(trimmed, []byte("</think>"))
		if idx < 0 {
			break
		}
		trimmed = bytes.TrimSpace(trimmed[idx+len("</think>"):])
	}
	if bytes.HasPrefix(trimmed, []byte("```")) && bytes.HasSuffix(trimmed, []byte("```")) {
		inner := trimmed[3 : len(trimmed)-3]
		if nl := bytes.IndexByte(inner, '\n'); nl >= 0 {
			lang := strings.ToLower(strings.TrimSpace(string(inner[:nl])))
			if lang == "" || lang == "json" {
				trimmed = bytes.TrimSpace(inner[nl+1:])
			}
		}
	}
	return trimmed
}

// ValidStageReviewState reports whether s is a valid contract state.
func decodeStructured(body []byte, expectRunID string) (*Response, error) {
	body = unwrapJSONEnvelope(body)
	if err := rejectDuplicateKeys(body); err != nil {
		return nil, fmt.Errorf("%w: ambiguous JSON: %v", ErrSchemaInvalid, err)
	}
	var out Response
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		return nil, err
	}
	// StageTerminal reports whether a stage snapshot has reached a terminal state.
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("%w: trailing JSON content", ErrSchemaInvalid)
	}
	if err := ValidateOutput(&out, expectRunID); err != nil {
		return nil, err
	}
	return &out, nil
}

// Severity is the finding severity level, ordered none<low<medium<high<critical. Provider-emitted info is normalized to low before persistence (DB CHECK has no info).
func rejectDuplicateKeys(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	type frame struct {
		object    bool
		seen      map[string]struct{}
		nextIsKey bool
	}
	var stack []frame
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if len(stack) == 0 {
			// SeverityInfo represents informational severity (F12 #009, owner ruling A): both machine contract ai-review-output.schema.json and the max_severity ladder include info — it never blocks the gate or raises the stage highest severity (rank=0.5 rounds to tier 0, keeping highest at none).
			if d, ok := tok.(json.Delim); ok {
				switch d {
				case '{':
					stack = append(stack, frame{object: true, seen: map[string]struct{}{}, nextIsKey: true})
				case '[':
					stack = append(stack, frame{object: false})
				}
			}
			continue
		}
		top := &stack[len(stack)-1]
		switch t := tok.(type) {
		case json.Delim:
			switch t {
			case '{':
				// SeverityRank returns the sort weight (-1 for unknown values). info shares tier 0 with none — an info finding does not change highest_severity (the contract stage enum has no info, keeping none + finding_count>0 self-consistent).
				top.nextIsKey = true
				stack = append(stack, frame{object: true, seen: map[string]struct{}{}, nextIsKey: true})
			case '[':
				top.nextIsKey = true
				stack = append(stack, frame{object: false})
			case '}', ']':
				stack = stack[:len(stack)-1]
			}
		case string:
			if top.object && top.nextIsKey {
				if _, dup := top.seen[t]; dup {
					return fmt.Errorf("duplicate object key %q", t)
				}
				top.seen[t] = struct{}{}
				top.nextIsKey = false
			} else {
				top.nextIsKey = true
			}
		default:
			// MaxSeverity returns the higher of two severity levels.
			top.nextIsKey = true
		}
	}
	if len(stack) != 0 {
		return errors.New("unbalanced JSON")
	}
	return nil
}

// BlockSeverityThreshold is the submission blocking threshold: high and above block submission (ai-review-runtime submission_gate.block_severities).
func retryableStatus(code int) bool {
	return code == http.StatusTooManyRequests || code >= 500
}

// maxHTTPRequestsPerStructuredCall caps the total upstream HTTP requests issued by a single
// reviewStructuredMode call across 429/5xx transient retries (at most 3 attempts per postBounded),
// context-overflow self-healing (at most 1 retry), and schema/EOF repair (at most 1 retry): hard cap <= 4.
const maxHTTPRequestsPerStructuredCall = 4

// statemachine_test.go provides full-coverage state machine tests (state-machine-test-policy): 100% expansion of legal edges; 100% enumeration of illegal (state, trigger) pairs; terminal states reject all triggers; pure functions have no side effects.
func (a *OpenAIAdapter) postBounded(ctx context.Context, cr chatRequest, remaining *int) ([]byte, Usage, []CallRecord, error) {
	var calls []CallRecord
	var lastErr error
	for attempt := 0; attempt < 3 && *remaining > 0; attempt++ {
		if attempt > 0 {
			// TestDraftMachineLegalEdges expands every declared edge.
			select {
			case <-ctx.Done():
				return nil, Usage{}, calls, ctx.Err()
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}
		*remaining--
		body, usage, rec, err := a.post(ctx, cr)
		// RCP-20261101-EPHEMERAL-PRECHECK: direct submission from draft (validity is carried by the materialized Run).
		if rec.ProviderID != "" {
			calls = append(calls, rec)
		}
		if err == nil {
			return body, usage, calls, nil
		}
		lastErr = err
		var se *statusError
		if errors.As(err, &se) && retryableStatus(se.code) && !isRelayFormatError(se) {
			continue
		}
		return nil, Usage{}, calls, err
	}
	return nil, Usage{}, calls, lastErr
}

// Coverage check: every declared edge is present in draftEdges (reverse verification).
type statusError struct {
	code     int
	snapshot string
	rawBody  string
}

func (e *statusError) Error() string {
	if e.snapshot != "" {
		return fmt.Sprintf("provider: status %d: %s", e.code, e.snapshot)
	}
	return fmt.Sprintf("provider: status %d", e.code)
}

func (e *statusError) HTTPStatus() int {
	return e.code
}

func (e *statusError) bodyText() string {
	if e.rawBody != "" {
		return e.rawBody
	}
	return e.snapshot
}

// TestDraftMachineIllegalPairs enumerates all pairs: undeclared combinations are rejected and state remains unchanged.
func upstreamSnapshot(body []byte) string {
	return UpstreamSnapshot(body)
}

func boundedErrorBody(body []byte) string {
	const max = 8192
	if len(body) > max {
		return string(body[:max])
	}
	return string(body)
}

// Legal edges are already verified by LegalEdges
func (a *OpenAIAdapter) post(ctx context.Context, cr chatRequest) ([]byte, Usage, CallRecord, error) {
	raw, err := json.Marshal(cr)
	if err != nil {
		return nil, Usage{}, CallRecord{}, err
	}
	u, err := ParseBaseURL(a.Endpoint.BaseURL)
	if err != nil {
		return nil, Usage{}, CallRecord{}, err
	}
	endpoint := strings.TrimRight(u.String(), "/") + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return nil, Usage{}, CallRecord{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+a.Endpoint.APIKey)
	rec := CallRecord{
		ProviderID: a.Endpoint.ProviderID, ModelName: a.Endpoint.ModelName,
		SentAt: now().UTC(),
		// TestDraftTerminalCases verifies that all triggers on terminal states are rejected.
		ReasoningEffort: cr.ReasoningEffort,
		MaxTokens:       cr.MaxTokens,
		Temperature:     cr.Temperature,
	}
	resp, err := a.Client.Inner.Do(httpReq)
	if err != nil {
		rec.Outcome = CallOutcomeError
		return nil, Usage{}, rec, err
	}
	defer resp.Body.Close()
	rec.HTTPStatus = resp.StatusCode
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseByte+1))
	if err != nil {
		rec.Outcome = CallOutcomeError
		return nil, Usage{}, rec, err
	}
	if len(body) > MaxResponseByte {
		rec.Outcome = CallOutcomeError
		return nil, Usage{}, rec, errors.New("provider: response exceeds 4MiB")
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		// TestRunMachineLegalEdges tests declared edges of the run state machine.
		rec.Outcome = CallOutcomeError
		return nil, Usage{}, rec, fmt.Errorf("%w: status %d: %s", ErrRejected, resp.StatusCode, upstreamSnapshot(body))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		rec.Outcome = CallOutcomeError
		return nil, Usage{}, rec, &statusError{
			code:     resp.StatusCode,
			snapshot: upstreamSnapshot(body),
			rawBody:  boundedErrorBody(body),
		}
	}
	var cr2 chatResponse
	if err := json.Unmarshal(body, &cr2); err != nil {
		rec.Outcome = CallOutcomeError
		return nil, Usage{}, rec, fmt.Errorf("%w: %v", ErrSchemaInvalid, err)
	}
	// TestRunMachineIllegalPairs enumerates illegal pairs; outdated is an absolute terminal state.
	detail := parseUsageDetail(cr2.Usage)
	rec.Detail = detail
	usage := Usage{}
	if detail != nil {
		if detail.InputTokens != nil {
			usage.InputTokens = *detail.InputTokens
		}
		if detail.OutputTokens != nil {
			usage.OutputTokens = *detail.OutputTokens
		}
	}
	if cr2.Error != nil {
		rec.Outcome = CallOutcomeError
		// Triggers other than inputs_changed on result terminal states are rejected as terminal.
		msg := cr2.Error.Message
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return nil, Usage{}, rec, fmt.Errorf("provider: api error type=%s: %s", cr2.Error.Type, msg)
	}
	if len(cr2.Choices) == 0 {
		rec.Outcome = CallOutcomeError
		return nil, Usage{}, rec, fmt.Errorf("%w: empty choices", ErrSchemaInvalid)
	}
	content := cr2.Choices[0].Message.Content
	rec.Outcome = CallOutcomeOK
	return []byte(content), usage, rec, nil
}

// TestRunImmutableResults verifies that result terminal states (except invalidation) cannot advance.
func parseUsageDetail(raw json.RawMessage) *UsageDetail {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var doc struct {
		PromptTokens             *json.Number `json:"prompt_tokens"`
		CompletionTokens         *json.Number `json:"completion_tokens"`
		InputTokens              *json.Number `json:"input_tokens"`
		OutputTokens             *json.Number `json:"output_tokens"`
		PromptCacheHitTokens     *json.Number `json:"prompt_cache_hit_tokens"`
		CacheReadInputTokens     *json.Number `json:"cache_read_input_tokens"`
		CacheCreationInputTokens *json.Number `json:"cache_creation_input_tokens"`
		ReasoningTokens          *json.Number `json:"reasoning_tokens"`
		PromptTokensDetails      *struct {
			CachedTokens     *json.Number `json:"cached_tokens"`
			CacheWriteTokens *json.Number `json:"cache_write_tokens"`
		} `json:"prompt_tokens_details"`
		InputTokensDetails *struct {
			CachedTokens     *json.Number `json:"cached_tokens"`
			CacheWriteTokens *json.Number `json:"cache_write_tokens"`
		} `json:"input_tokens_details"`
		CompletionTokensDetails *struct {
			ReasoningTokens *json.Number `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
		OutputTokensDetails *struct {
			ReasoningTokens *json.Number `json:"reasoning_tokens"`
		} `json:"output_tokens_details"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return &UsageDetail{}
	}
	out := &UsageDetail{
		InputTokens:      firstNonNilInt64(doc.PromptTokens, doc.InputTokens),
		OutputTokens:     firstNonNilInt64(doc.CompletionTokens, doc.OutputTokens),
		CachedTokens:     firstNonNilInt64(doc.PromptCacheHitTokens, doc.CacheReadInputTokens),
		CacheWriteTokens: nonNegativeInt64(doc.CacheCreationInputTokens),
		ReasoningTokens:  nonNegativeInt64(doc.ReasoningTokens),
	}
	if doc.PromptTokensDetails != nil {
		if v := nonNegativeInt64(doc.PromptTokensDetails.CachedTokens); v != nil {
			out.CachedTokens = v
		}
		if v := nonNegativeInt64(doc.PromptTokensDetails.CacheWriteTokens); v != nil {
			out.CacheWriteTokens = v
		}
	}
	if doc.InputTokensDetails != nil {
		if out.CachedTokens == nil {
			out.CachedTokens = nonNegativeInt64(doc.InputTokensDetails.CachedTokens)
		}
		if out.CacheWriteTokens == nil {
			out.CacheWriteTokens = nonNegativeInt64(doc.InputTokensDetails.CacheWriteTokens)
		}
	}
	if doc.CompletionTokensDetails != nil {
		if v := nonNegativeInt64(doc.CompletionTokensDetails.ReasoningTokens); v != nil {
			out.ReasoningTokens = v
		}
	}
	if doc.OutputTokensDetails != nil && out.ReasoningTokens == nil {
		out.ReasoningTokens = nonNegativeInt64(doc.OutputTokensDetails.ReasoningTokens)
	}
	return out
}

func firstNonNilInt64(nums ...*json.Number) *int64 {
	for _, n := range nums {
		if v := nonNegativeInt64(n); v != nil {
			return v
		}
	}
	return nil
}

// TestGateEvaluation tests submission gate evaluation.
func nonNegativeInt64(n *json.Number) *int64 {
	if n == nil {
		return nil
	}
	if v, err := n.Int64(); err == nil && v >= 0 {
		return &v
	}
	return nil
}

// Multi-stage: any blocked stage blocks the overall gate.
func configString(cfg map[string]any, key string) string {
	if cfg == nil {
		return ""
	}
	if v, ok := cfg[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// TestSeverityOrder tests severity level ordering.
func compileOutputSchema() any {
	sev := map[string]any{"type": "string", "enum": []string{"info", "low", "medium", "high", "critical"}}
	cat := map[string]any{"type": "string", "enum": []string{"correctness", "performance", "availability", "security", "governance", "compatibility", "operability"}}
	uuidStr := func() map[string]any { return map[string]any{"type": "string"} }
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"schema_version": map[string]any{"type": "integer"},
			"review_run_id":  uuidStr(),
			"outcome":        map[string]any{"type": "string", "enum": []string{"complete", "partial"}},
			"summary":        map[string]any{"type": "string"},
			"findings": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"finding_key":           map[string]any{"type": "string"},
						"category":              cat,
						"severity":              sev,
						"title":                 map[string]any{"type": "string"},
						"message":               map[string]any{"type": "string"},
						"suggestion":            map[string]any{"type": "string"},
						"fingerprint_group_ids": map[string]any{"type": "array", "items": uuidStr()},
						"evidence_ids":          map[string]any{"type": "array", "items": uuidStr()},
						"fact_basis":            map[string]any{"type": "string", "enum": []string{"evidence", "model_only"}},
					},
					"required": []string{"finding_key", "category", "severity", "title", "message",
						"suggestion", "fingerprint_group_ids", "evidence_ids", "fact_basis"},
					"additionalProperties": false,
				},
			},
			"tool_requests": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"tool_call_id": uuidStr(),
						"tool_name":    map[string]any{"type": "string"},
						"arguments":    map[string]any{"type": "object"},
					},
					"required":             []string{"tool_call_id", "tool_name", "arguments"},
					"additionalProperties": false,
				},
			},
		},
		"required":             []string{"schema_version", "review_run_id", "outcome", "summary", "findings", "tool_requests"},
		"additionalProperties": false,
	}
}

// ScopeType defines the three scope levels.
func configFloatPtr(cfg map[string]any, key string) *float64 {
	if cfg == nil {
		return nil
	}
	if v, ok := cfg[key]; ok {
		if f, ok := v.(float64); ok {
			return &f
		}
	}
	return nil
}

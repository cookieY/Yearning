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

	"github.com/cookieY/YearningX/backend/internal/review/promptprofile"
)

// ChatCompletionsURL validates baseURL via ParseBaseURL and builds the OpenAI-compatible
// POST /chat/completions endpoint URL.
func ChatCompletionsURL(baseURL string) (string, error) {
	u, err := ParseBaseURL(baseURL)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(u.String(), "/") + "/chat/completions", nil
}

// NewChatCompletionsHTTPRequest constructs a JSON POST request with the endpoint Bearer API key.
func NewChatCompletionsHTTPRequest(ctx context.Context, endpointURL, apiKey string, payload []byte) (*http.Request, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpointURL, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	return httpReq, nil
}

// ApplyChatGenerationParams populates optional max_tokens, temperature, and reasoning_effort
// fields onto a JSON chat request map.
func ApplyChatGenerationParams(reqBody map[string]any, cfg map[string]any, effMaxTokens int) {
	if effMaxTokens > 0 {
		reqBody["max_tokens"] = effMaxTokens
	}
	if cfg == nil {
		return
	}
	if v, ok := cfg["temperature"].(float64); ok {
		reqBody["temperature"] = v
	}
	if v, ok := cfg["reasoning_effort"].(string); ok && v != "" {
		reqBody["reasoning_effort"] = v
	}
}

// ReadBoundedResponseBody reads at most MaxResponseByte bytes from r, failing if the
// response body exceeds the 4 MiB safety cap.
func ReadBoundedResponseBody(r io.Reader) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, MaxResponseByte+1))
	if err != nil {
		return nil, err
	}
	if len(body) > MaxResponseByte {
		return nil, errors.New("provider: response exceeds 4MiB")
	}
	return body, nil
}

// ChatCompletionTransportRequest describes a JSON-mode /chat/completions request dispatched
// through the shared overflow-healing HTTP transport.
type ChatCompletionTransportRequest struct {
	CallerTag            string
	ErrorDomain          string
	SystemPrompt         string
	UserContent          string
	Stream               bool
	EnsureSystemJSONHint bool
	EnsureUserJSONHint   bool
}

// PostChatCompletionWithOverflowHeal dispatches a /chat/completions HTTP POST request using
// ep and hc, injects non-SAN JSON hints when requested, applies generation parameters with
// dynamic max_tokens capping, and performs single-shot HTTP 400 context-overflow self-healing.
// The caller owns resp.Body.Close() when err == nil.
func PostChatCompletionWithOverflowHeal(
	ctx context.Context,
	hc *HTTPClient,
	ep Endpoint,
	req ChatCompletionTransportRequest,
) (*http.Response, error) {
	errDomain := strings.TrimSpace(req.ErrorDomain)
	if errDomain == "" {
		errDomain = "provider"
	}
	callerTag := strings.TrimSpace(req.CallerTag)
	if callerTag == "" {
		callerTag = "provider_chat_transport"
	}

	endpointURL, err := ChatCompletionsURL(ep.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("%s: invalid provider base_url: %w", errDomain, err)
	}

	sysPrompt := req.SystemPrompt
	userContent := req.UserContent
	// SAN endpoints carry strict byte-locked compact contracts and must never have their prompts mutated.
	if ep.EffectivePromptProfile() != promptprofile.ProfileSAN {
		if req.EnsureSystemJSONHint && !strings.Contains(strings.ToLower(sysPrompt), "json") {
			sysPrompt += "\nRespond with a valid json object."
		}
		if req.EnsureUserJSONHint && !strings.Contains(strings.ToLower(userContent), "json") {
			userContent += "\n\nRespond with a valid json object."
		}
	}

	promptMsgs := []PromptMessage{
		{Role: "system", Content: sysPrompt},
		{Role: "user", Content: userContent},
	}
	effMax := ResolveMaxTokens(ep.Configuration, promptMsgs)

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
	if req.Stream {
		reqBody["stream"] = true
		reqBody["stream_options"] = map[string]any{
			"include_usage": true,
		}
	}
	ApplyChatGenerationParams(reqBody, ep.Configuration, effMax)

	if hc == nil {
		hc = NewHTTPClient()
	}

	healedSentMax := 0
	healedNextMax := 0
	healedMaxCtx := 0
	healedPromptTokens := 0
	for attempt := 0; attempt < 2; attempt++ {
		rawReq, err := json.Marshal(reqBody)
		if err != nil {
			return nil, err
		}
		httpReq, err := NewChatCompletionsHTTPRequest(ctx, endpointURL, ep.APIKey, rawReq)
		if err != nil {
			return nil, err
		}

		resp, err := hc.Inner.Do(httpReq)
		if err != nil {
			if healedSentMax > 0 {
				RecordOverflowHeal(OverflowHealOutcomeFastFail, callerTag, ep.ModelName, healedSentMax, healedNextMax, healedMaxCtx, healedPromptTokens)
			}
			return nil, err
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			if healedSentMax > 0 {
				outcome := OverflowHealOutcomeHealed
				if healedNextMax == 0 {
					outcome = OverflowHealOutcomeFallbackNoMaxTokens
				}
				RecordOverflowHeal(outcome, callerTag, ep.ModelName, healedSentMax, healedNextMax, healedMaxCtx, healedPromptTokens)
			}
			return resp, nil
		}

		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		resp.Body.Close()
		statusErr := &HTTPStatusError{
			Code:     resp.StatusCode,
			Snapshot: UpstreamSnapshot(errBody),
			RawBody:  strings.TrimSpace(string(errBody)),
		}
		if attempt == 0 {
			rawErr := string(errBody)
			retry, nextMax, promptTooLong := ResolveContextOverflowRetry(effMax, resp.StatusCode, rawErr)
			maxCtx, promptTokens, _ := ParseContextLengthOverflow(rawErr)
			if promptTooLong {
				RecordOverflowHeal(OverflowHealOutcomeFastFail, callerTag, ep.ModelName, effMax, 0, maxCtx, promptTokens)
				return nil, fmt.Errorf("%s: prompt exceeds model context window: %w", errDomain, statusErr)
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
			RecordOverflowHeal(OverflowHealOutcomeFastFail, callerTag, ep.ModelName, healedSentMax, healedNextMax, healedMaxCtx, healedPromptTokens)
		}
		return nil, statusErr
	}
	return nil, errors.New("provider: unexpected overflow retry termination")
}

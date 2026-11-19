package provider

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"

	"go.uber.org/zap"
)

// PromptMessage represents a role/content chat message for token budget estimation.
type PromptMessage struct {
	Role    string
	Content string
}

// HTTPStatusError captures a non-2xx upstream HTTP status and a bounded response snapshot.
type HTTPStatusError struct {
	Code     int
	Snapshot string
	RawBody  string
}

func (e *HTTPStatusError) Error() string {
	if e == nil {
		return ""
	}
	if e.Snapshot != "" {
		return fmt.Sprintf("provider status %d: %s", e.Code, e.Snapshot)
	}
	return fmt.Sprintf("provider status %d", e.Code)
}

// HTTPStatus returns the HTTP status code associated with the error.
func (e *HTTPStatusError) HTTPStatus() int {
	if e == nil {
		return 0
	}
	return e.Code
}

// ExtractHTTPStatus extracts an upstream HTTP status code from err when available.
func ExtractHTTPStatus(err error) int {
	var providerErr interface{ HTTPStatus() int }
	if errors.As(err, &providerErr) {
		return providerErr.HTTPStatus()
	}
	return 0
}

// UpstreamSnapshot normalizes whitespace and caps an upstream error body to 300 bytes.
func UpstreamSnapshot(body []byte) string {
	const max = 300
	line := strings.Join(strings.Fields(string(body)), " ")
	if len(line) > max {
		line = line[:max]
	}
	return line
}

// ConfigPositiveInt reads an optional positive integer value from Endpoint.Configuration.
// Returns 0 when the key is absent, non-positive, or not an integer.
func ConfigPositiveInt(cfg map[string]any, key string) int {
	if cfg == nil {
		return 0
	}
	v, ok := cfg[key]
	if !ok || v == nil {
		return 0
	}
	switch n := v.(type) {
	case float64:
		if math.IsNaN(n) || math.IsInf(n, 0) || n <= 0 || n != math.Trunc(n) {
			return 0
		}
		if n > float64(math.MaxInt32) {
			return math.MaxInt32
		}
		return int(n)
	case int:
		if n <= 0 {
			return 0
		}
		return n
	case int64:
		if n <= 0 {
			return 0
		}
		if n > math.MaxInt32 {
			return math.MaxInt32
		}
		return int(n)
	case int32:
		if n <= 0 {
			return 0
		}
		return int(n)
	case json.Number:
		if parsed, err := n.Int64(); err == nil && parsed > 0 {
			if parsed > math.MaxInt32 {
				return math.MaxInt32
			}
			return int(parsed)
		}
	}
	return 0
}

// EstimateMessagesTokens returns a conservative lower-bound pure-Go estimate of prompt tokens
// for chat messages. Underestimating (rather than overestimating) ensures max_tokens is never
// silently depressed below the model's actual remaining context window; any tight request that
// exceeds the server's exact window is caught by the single-shot 400 self-healing retry.
func EstimateMessagesTokens(messages []PromptMessage) int {
	if len(messages) == 0 {
		return 0
	}
	var units float64
	for _, m := range messages {
		// Minimal per-message chat framing overhead.
		units += 4
		units += estimateTextUnits(m.Role)
		units += estimateTextUnits(m.Content)
	}
	// Conversation priming overhead.
	units += 3
	est := int(math.Floor(units * 0.85))
	if est < 1 {
		return 1
	}
	return est
}

func estimateTextUnits(s string) float64 {
	var units float64
	for _, r := range s {
		switch {
		case r > 0x7f:
			// Qwen CJK density is ~0.65-0.77 tokens/char; 0.68 * 0.85 (~0.58) stays a safe lower bound.
			units += 0.68
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			units += 0.10
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_':
			units += 0.25
		default:
			units += 0.35
		}
	}
	return units
}

// ResolveMaxTokens computes the effective max_tokens to send in a /chat/completions request.
// Rule A: returns 0 (omit max_tokens) when max_tokens is unconfigured or <= 0.
// Rule B: when both max_tokens > 0 and context_window > 0 are configured, dynamically caps
// max_tokens using the lower-bound prompt estimate (context_window - estPromptTokens),
// or omits it (0) if remaining < 256.
func ResolveMaxTokens(cfg map[string]any, messages []PromptMessage) int {
	configuredMax := ConfigPositiveInt(cfg, "max_tokens")
	if configuredMax <= 0 {
		return 0
	}
	contextWindow := ConfigPositiveInt(cfg, "context_window")
	if contextWindow <= 0 {
		return configuredMax
	}
	estPromptTokens := EstimateMessagesTokens(messages)
	return resolveMaxTokensWithEstimate(configuredMax, contextWindow, estPromptTokens)
}

func resolveMaxTokensWithEstimate(configuredMax, contextWindow, estPromptTokens int) int {
	if configuredMax <= 0 {
		return 0
	}
	if contextWindow <= 0 {
		return configuredMax
	}
	remaining := contextWindow - estPromptTokens
	if remaining < 256 {
		return 0
	}
	if configuredMax < remaining {
		return configuredMax
	}
	return remaining
}

var (
	maxContextLenRe     = regexp.MustCompile(`(?i)maximum context length is (\d+) tokens`)
	promptTokensInRe    = regexp.MustCompile(`(?i)(?:\b|\()(\d+) in the messages`)
	promptTokensInputRe = regexp.MustCompile(`(?i)(?:your request has|request has)\s+(\d+)\s+input tokens`)
	promptTokensAltRe   = regexp.MustCompile(`(?i)messages resulted in (\d+) tokens`)
	contextOverflowSig  = []string{
		"maximum context length",
		"max_model_len",
		"context_length_exceeded",
		"reduce the length of the messages or completion",
		"reduce the length of the messages",
	}
)

const (
	// OverflowHealOutcomeHealed records an exact max_tokens self-heal retry that succeeded.
	OverflowHealOutcomeHealed = "healed"
	// OverflowHealOutcomeFastFail records a prompt-exceeds-context fast failure (or failed heal attempt).
	OverflowHealOutcomeFastFail = "fast_fail"
	// OverflowHealOutcomeFallbackNoMaxTokens records a retry that omitted max_tokens when exact counts were unavailable.
	OverflowHealOutcomeFallbackNoMaxTokens = "fallback_no_max_tokens"
)

var (
	overflowHealHealedTotal            atomic.Int64
	overflowHealFastFailTotal          atomic.Int64
	overflowHealFallbackNoMaxToksTotal atomic.Int64
)

// ChatCompletionOverflowHealTotal returns the cumulative count for chat_completion_overflow_heal_total
// for the given outcome ("healed", "fast_fail", or "fallback_no_max_tokens").
func ChatCompletionOverflowHealTotal(outcome string) int64 {
	switch outcome {
	case OverflowHealOutcomeHealed:
		return overflowHealHealedTotal.Load()
	case OverflowHealOutcomeFastFail:
		return overflowHealFastFailTotal.Load()
	case OverflowHealOutcomeFallbackNoMaxTokens:
		return overflowHealFallbackNoMaxToksTotal.Load()
	default:
		return 0
	}
}

// RecordOverflowHeal increments chat_completion_overflow_heal_total for outcome and emits
// an Info-level structured log when a self-heal retry succeeds (never Error/Warn so routine
// first-attempt 400 heals do not trigger alerts).
func RecordOverflowHeal(outcome, caller, modelName string, sentMaxTokens, nextMaxTokens, maxCtx, promptTokens int) {
	var total int64
	switch outcome {
	case OverflowHealOutcomeHealed:
		total = overflowHealHealedTotal.Add(1)
	case OverflowHealOutcomeFastFail:
		total = overflowHealFastFailTotal.Add(1)
	case OverflowHealOutcomeFallbackNoMaxTokens:
		total = overflowHealFallbackNoMaxToksTotal.Add(1)
	default:
		return
	}
	if outcome == OverflowHealOutcomeHealed || outcome == OverflowHealOutcomeFallbackNoMaxTokens {
		zap.L().Info("chat_completion_overflow_healed",
			zap.String("outcome", outcome),
			zap.String("caller", caller),
			zap.String("model_name", modelName),
			zap.Int("sent_max_tokens", sentMaxTokens),
			zap.Int("healed_max_tokens", nextMaxTokens),
			zap.Int("max_context_tokens", maxCtx),
			zap.Int("prompt_tokens", promptTokens),
			zap.Int64("chat_completion_overflow_heal_total", total),
		)
	}
}

// IsContextLengthOverflowError reports whether an HTTP response represents an upstream
// context-window overflow error (HTTP 400 with standard vLLM or OpenAI overflow markers).
func IsContextLengthOverflowError(statusCode int, body string) bool {
	if statusCode != http.StatusBadRequest {
		return false
	}
	lower := strings.ToLower(body)
	for _, sig := range contextOverflowSig {
		if strings.Contains(lower, sig) {
			return true
		}
	}
	return false
}

// ParseContextLengthOverflow extracts (maxCtx, promptTokens) from standard vLLM/OpenAI
// context-length overflow error messages when present (supporting both legacy and newer vLLM formats).
func ParseContextLengthOverflow(body string) (maxCtx int, promptTokens int, ok bool) {
	ctxMatch := maxContextLenRe.FindStringSubmatch(body)
	if len(ctxMatch) < 2 {
		return 0, 0, false
	}
	parsedCtx, err := strconv.Atoi(ctxMatch[1])
	if err != nil || parsedCtx <= 0 {
		return 0, 0, false
	}
	var promptMatch []string
	if m := promptTokensInRe.FindStringSubmatch(body); len(m) >= 2 {
		promptMatch = m
	} else if m := promptTokensInputRe.FindStringSubmatch(body); len(m) >= 2 {
		promptMatch = m
	} else if m := promptTokensAltRe.FindStringSubmatch(body); len(m) >= 2 {
		promptMatch = m
	}
	if len(promptMatch) < 2 {
		return 0, 0, false
	}
	parsedPrompt, err := strconv.Atoi(promptMatch[1])
	if err != nil || parsedPrompt <= 0 {
		return 0, 0, false
	}
	return parsedCtx, parsedPrompt, true
}

// ResolveContextOverflowRetry decides whether a request that explicitly sent max_tokens > 0
// and failed with a context-length overflow error should retry once, and with what max_tokens.
// If nextMaxTokens == 0 and retry == true, the caller should omit max_tokens on the single retry.
// If promptTooLong == true, the prompt itself exceeds or leaves <= 64 tokens of context and must fail fast.
func ResolveContextOverflowRetry(sentMaxTokens int, statusCode int, body string) (retry bool, nextMaxTokens int, promptTooLong bool) {
	if sentMaxTokens <= 0 || !IsContextLengthOverflowError(statusCode, body) {
		return false, 0, false
	}
	maxCtx, promptTokens, ok := ParseContextLengthOverflow(body)
	if !ok {
		return true, 0, false
	}
	avail := maxCtx - promptTokens
	if avail <= 64 {
		return false, 0, true
	}
	candidate := avail - 16
	if sentMaxTokens < candidate {
		candidate = sentMaxTokens
	}
	return true, candidate, false
}

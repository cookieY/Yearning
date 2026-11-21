package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func testTransportHTTPClient(srv *httptest.Server) *HTTPClient {
	return &HTTPClient{Inner: srv.Client()}
}

func TestPostChatCompletionWithOverflowHeal_SANvsDefaultPromptsAndParams(t *testing.T) {
	t.Setenv("YEARNING_ALLOW_LOOPBACK_TARGETS", "1")

	type capturedPayload struct {
		Messages        []map[string]string `json:"messages"`
		Stream          bool                `json:"stream"`
		StreamOptions   map[string]any      `json:"stream_options"`
		MaxTokens       int                 `json:"max_tokens"`
		Temperature     float64             `json:"temperature"`
		ReasoningEffort string              `json:"reasoning_effort"`
	}
	var got capturedPayload
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer sk-test" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{}"}}]}`))
	}))
	defer srv.Close()

	hc := testTransportHTTPClient(srv)

	// Non-SAN endpoint appends JSON hints and forwards generation/stream parameters.
	epDefault := Endpoint{
		BaseURL:   srv.URL + "/v1",
		ModelName: "gpt-4o",
		APIKey:    "sk-test",
		Configuration: map[string]any{
			"max_tokens":       1024,
			"temperature":      0.2,
			"reasoning_effort": "low",
		},
	}
	resp, err := PostChatCompletionWithOverflowHeal(context.Background(), hc, epDefault, ChatCompletionTransportRequest{
		CallerTag:            "test_default",
		ErrorDomain:          "review",
		SystemPrompt:         "System instructions",
		UserContent:          "User request",
		Stream:               true,
		EnsureSystemJSONHint: true,
		EnsureUserJSONHint:   true,
	})
	if err != nil {
		t.Fatalf("unexpected default call error: %v", err)
	}
	_ = resp.Body.Close()
	if !strings.Contains(got.Messages[0]["content"], "Respond with a valid json object.") {
		t.Fatalf("expected non-SAN system JSON hint, got %q", got.Messages[0]["content"])
	}
	if !strings.Contains(got.Messages[1]["content"], "Respond with a valid json object.") {
		t.Fatalf("expected non-SAN user JSON hint, got %q", got.Messages[1]["content"])
	}
	if !got.Stream || got.StreamOptions["include_usage"] != true || got.MaxTokens != 1024 || got.Temperature != 0.2 || got.ReasoningEffort != "low" {
		t.Fatalf("unexpected captured payload: %+v", got)
	}

	// SAN endpoint must keep both system and user prompts byte-for-byte unmodified.
	epSAN := Endpoint{
		BaseURL:   srv.URL + "/v1",
		ModelName: "gpt-4o",
		APIKey:    "sk-test",
		Configuration: map[string]any{
			"system_prompt_profile": "san",
		},
	}
	resp2, err := PostChatCompletionWithOverflowHeal(context.Background(), hc, epSAN, ChatCompletionTransportRequest{
		CallerTag:            "test_san",
		ErrorDomain:          "assistant",
		SystemPrompt:         "SAN_COMPACT_SYS_EXACT",
		UserContent:          "SAN_COMPACT_USER_EXACT",
		EnsureSystemJSONHint: true,
		EnsureUserJSONHint:   true,
	})
	if err != nil {
		t.Fatalf("unexpected SAN call error: %v", err)
	}
	_ = resp2.Body.Close()
	if got.Messages[0]["content"] != "SAN_COMPACT_SYS_EXACT" || got.Messages[1]["content"] != "SAN_COMPACT_USER_EXACT" {
		t.Fatalf("SAN prompts must remain unmodified byte-for-byte, got %+v", got.Messages)
	}
}

func TestPostChatCompletionWithOverflowHeal_SelfHealAndErrors(t *testing.T) {
	t.Setenv("YEARNING_ALLOW_LOOPBACK_TARGETS", "1")

	var mode atomic.Int32
	var seenMaxTokens []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			MaxTokens int `json:"max_tokens"`
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		seenMaxTokens = append(seenMaxTokens, body.MaxTokens)

		switch mode.Load() {
		case 0: // Exact max_tokens heal on attempt 1
			if len(seenMaxTokens) == 1 {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":{"message":"This model's maximum context length is 4096 tokens. However, you requested 4200 tokens (3900 in the messages, 300 in the completion)."}}`))
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		case 1: // Fallback omit max_tokens when exact counts absent
			if len(seenMaxTokens) == 1 {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":{"message":"context_length_exceeded: reduce the length of the messages or completion"}}`))
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		case 2: // Prompt too long -> fast fail
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"This model's maximum context length is 4096 tokens. However, your request has 4080 input tokens."}}`))
		case 3: // Non-overflow 502 status error
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte("bad gateway"))
		}
	}))
	defer srv.Close()

	hc := testTransportHTTPClient(srv)
	ep := Endpoint{
		BaseURL:       srv.URL,
		ModelName:     "qwen-test",
		APIKey:        "sk-test",
		Configuration: map[string]any{"max_tokens": 500},
	}

	// Case 0: Exact heal (4096 - 3900 - 16 = 180)
	beforeHealed := ChatCompletionOverflowHealTotal(OverflowHealOutcomeHealed)
	seenMaxTokens = nil
	resp, err := PostChatCompletionWithOverflowHeal(context.Background(), hc, ep, ChatCompletionTransportRequest{
		CallerTag:   "test_heal",
		ErrorDomain: "review",
		UserContent: "hello json",
	})
	if err != nil {
		t.Fatalf("expected healed retry success, got %v", err)
	}
	_ = resp.Body.Close()
	if len(seenMaxTokens) != 2 || seenMaxTokens[0] != 500 || seenMaxTokens[1] != 180 {
		t.Fatalf("expected retry max_tokens [500, 180], got %v", seenMaxTokens)
	}
	if ChatCompletionOverflowHealTotal(OverflowHealOutcomeHealed) != beforeHealed+1 {
		t.Fatal("expected OverflowHealOutcomeHealed counter increment")
	}

	// Case 1: Fallback omitting max_tokens
	mode.Store(1)
	seenMaxTokens = nil
	beforeFallback := ChatCompletionOverflowHealTotal(OverflowHealOutcomeFallbackNoMaxTokens)
	resp, err = PostChatCompletionWithOverflowHeal(context.Background(), hc, ep, ChatCompletionTransportRequest{
		CallerTag:   "test_fallback",
		ErrorDomain: "assistant",
		UserContent: "hello json",
	})
	if err != nil {
		t.Fatalf("expected fallback retry success, got %v", err)
	}
	_ = resp.Body.Close()
	if len(seenMaxTokens) != 2 || seenMaxTokens[0] != 500 || seenMaxTokens[1] != 0 {
		t.Fatalf("expected fallback retry omitting max_tokens ([500, 0]), got %v", seenMaxTokens)
	}
	if ChatCompletionOverflowHealTotal(OverflowHealOutcomeFallbackNoMaxTokens) != beforeFallback+1 {
		t.Fatal("expected OverflowHealOutcomeFallbackNoMaxTokens counter increment")
	}

	// Case 2: Prompt too long fast fail
	mode.Store(2)
	beforeFastFail := ChatCompletionOverflowHealTotal(OverflowHealOutcomeFastFail)
	_, err = PostChatCompletionWithOverflowHeal(context.Background(), hc, ep, ChatCompletionTransportRequest{
		CallerTag:   "test_fast_fail",
		ErrorDomain: "review",
		UserContent: "hello json",
	})
	if err == nil || !strings.Contains(err.Error(), "review: prompt exceeds model context window") {
		t.Fatalf("expected fast-fail prompt window error, got %v", err)
	}
	if ExtractHTTPStatus(err) != http.StatusBadRequest {
		t.Fatalf("expected 400 ExtractHTTPStatus, got %d", ExtractHTTPStatus(err))
	}
	if ChatCompletionOverflowHealTotal(OverflowHealOutcomeFastFail) != beforeFastFail+1 {
		t.Fatal("expected OverflowHealOutcomeFastFail counter increment")
	}

	// Case 3: HTTP 502 error propagates as *HTTPStatusError
	mode.Store(3)
	_, err = PostChatCompletionWithOverflowHeal(context.Background(), hc, ep, ChatCompletionTransportRequest{
		CallerTag:   "test_502",
		ErrorDomain: "review",
		UserContent: "hello json",
	})
	var se *HTTPStatusError
	if !errors.As(err, &se) || se.Code != http.StatusBadGateway {
		t.Fatalf("expected *HTTPStatusError 502, got %v", err)
	}

	// Case 4: Invalid base URL rejected cleanly
	_, err = PostChatCompletionWithOverflowHeal(context.Background(), hc, Endpoint{BaseURL: "http://example.com"}, ChatCompletionTransportRequest{
		ErrorDomain: "assistant",
	})
	if err == nil || !strings.Contains(err.Error(), "assistant: invalid provider base_url") {
		t.Fatalf("expected invalid provider base_url error, got %v", err)
	}

	// Case 5: ReadBoundedResponseBody rejects oversized payloads
	oversized := bytes.Repeat([]byte("a"), MaxResponseByte+1)
	if _, err := ReadBoundedResponseBody(bytes.NewReader(oversized)); err == nil || !strings.Contains(err.Error(), "exceeds 4MiB") {
		t.Fatalf("expected 4MiB limit error, got %v", err)
	}
}

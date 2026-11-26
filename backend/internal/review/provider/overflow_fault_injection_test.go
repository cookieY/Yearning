package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// TestOpenAIAdapterOverflowHealAndRelayFallbackEndToEnd exercises DEF-22
// end-to-end against in-process httptest endpoints: (1) chat completion carrying
// oversized max_tokens receives vLLM HTTP 400 context_length_exceeded and heals
// on retry with reduced max_tokens; (2) relay HTTP 500 json_schema error
// negotiates json_object fallback cleanly and succeeds on retry.
func TestOpenAIAdapterOverflowHealAndRelayFallbackEndToEnd(t *testing.T) {
	t.Setenv("YEARNING_ALLOW_LOOPBACK_TARGETS", "1")
	client := NewHTTPClient()

	t.Run("vllm_context_length_overflow_heals", func(t *testing.T) {
		var overflowCalls atomic.Int64
		overflowSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body struct {
				MaxTokens int `json:"max_tokens"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			overflowCalls.Add(1)
			if body.MaxTokens > 2192 {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","code":"context_length_exceeded","message":"'max_tokens' is too large: 4096. This model's maximum context length is 8192 tokens and your request has 6000 input tokens"}}`))
				return
			}
			out := map[string]any{
				"choices": []map[string]any{{
					"finish_reason": "stop",
					"message": map[string]any{
						"content": `{"schema_version":1,"review_run_id":"` + ProbeRunID + `","outcome":"complete","summary":"healed","findings":[],"tool_requests":[]}`,
					},
				}},
				"usage": map[string]any{"prompt_tokens": 60, "completion_tokens": 20},
			}
			_ = json.NewEncoder(w).Encode(out)
		}))
		defer overflowSrv.Close()

		ep := Endpoint{
			ProviderID: "p-overflow", Name: "overflow", ProviderKind: "openai_compatible",
			BaseURL: overflowSrv.URL, ModelName: "fault-overflow",
			PrivacyContractHash: strings.Repeat("a", 64), OutputSchemaHash: strings.Repeat("b", 64),
			APIKey: "k", Configuration: map[string]any{"max_tokens": 4096, "structured_output_mode": "json_object"},
		}
		adapter, err := NewOpenAIAdapter(ep, client)
		if err != nil {
			t.Fatal(err)
		}
		outcome, err := adapter.ReviewStructured(context.Background(), ProbeRequest())
		if err != nil {
			t.Fatalf("expected healed ReviewStructured outcome, got err=%v", err)
		}
		if overflowCalls.Load() != 2 || len(outcome.Calls) != 2 || outcome.Calls[0].Outcome != CallOutcomeError || outcome.Calls[1].Outcome != CallOutcomeOK || outcome.Response == nil {
			t.Fatalf("calls=%d outcome=%+v, want 2 calls (error -> healed ok)", overflowCalls.Load(), outcome.Calls)
		}
	})

	t.Run("relay_500_json_schema_falls_back_to_json_object", func(t *testing.T) {
		var modes []string
		relaySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body struct {
				ResponseFormat struct {
					Type string `json:"type"`
				} `json:"response_format"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			modes = append(modes, body.ResponseFormat.Type)
			if body.ResponseFormat.Type == OutputModeJSONSchema {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error":{"message":"upstream relay failed: response_format json_schema is not supported by channel"}}`))
				return
			}
			out := map[string]any{
				"choices": []map[string]any{{
					"finish_reason": "stop",
					"message": map[string]any{
						"content": `{"schema_version":1,"review_run_id":"` + ProbeRunID + `","outcome":"complete","summary":"fallback ok","findings":[],"tool_requests":[]}`,
					},
				}},
				"usage": map[string]any{"prompt_tokens": 45, "completion_tokens": 18},
			}
			_ = json.NewEncoder(w).Encode(out)
		}))
		defer relaySrv.Close()

		ep := Endpoint{
			ProviderID: "p-relay", Name: "relay", ProviderKind: "openai_compatible",
			BaseURL: relaySrv.URL, ModelName: "fault-relay",
			PrivacyContractHash: strings.Repeat("a", 64), OutputSchemaHash: strings.Repeat("b", 64),
			APIKey: "k", Configuration: map[string]any{"max_tokens": 1024},
		}
		adapter, err := NewOpenAIAdapter(ep, client)
		if err != nil {
			t.Fatal(err)
		}
		outcome, err := adapter.ReviewStructured(context.Background(), ProbeRequest())
		if err != nil {
			t.Fatalf("expected json_object fallback to succeed, got err=%v", err)
		}
		if len(modes) != 2 || modes[0] != OutputModeJSONSchema || modes[1] != OutputModeJSONObject {
			t.Fatalf("modes=%v, want [json_schema json_object]", modes)
		}
		if len(outcome.Calls) != 2 || outcome.Calls[0].HTTPStatus != http.StatusInternalServerError || outcome.Calls[1].Outcome != CallOutcomeOK {
			t.Fatalf("calls=%+v, want [500 error, 200 ok]", outcome.Calls)
		}
	})
}

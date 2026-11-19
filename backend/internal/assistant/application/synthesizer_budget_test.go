package application

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/assistant/domain"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
)

func newSynthesizerCaptureServer(t *testing.T, handler func(attempt int, req map[string]any, w http.ResponseWriter)) (*httptest.Server, *[]map[string]any) {
	t.Helper()
	var mu sync.Mutex
	var reqs []map[string]any
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal(buf, &m); err != nil {
			t.Fatalf("unmarshal request body: %v", err)
		}
		mu.Lock()
		reqs = append(reqs, m)
		attempt := len(reqs)
		mu.Unlock()
		handler(attempt, m, w)
	}))
	t.Cleanup(srv.Close)
	return srv, &reqs
}

func synthEndpoint(url string, cfg map[string]any) provider.Endpoint {
	return provider.Endpoint{
		ProviderID:          "p-synth",
		Name:                "vllm-san",
		ProviderKind:        "openai_compatible",
		BaseURL:             url,
		ModelName:           "san",
		APIKey:              "sk-test",
		PrivacyContractHash: strings.Repeat("a", 64),
		OutputSchemaHash:    strings.Repeat("b", 64),
		Configuration:       cfg,
	}
}

func synthValidCompletion(reply string) map[string]any {
	payload, _ := json.Marshal(map[string]any{
		"out_of_scope":    false,
		"assistant_reply": reply,
	})
	return map[string]any{
		"choices": []map[string]any{
			{"message": map[string]any{"content": string(payload)}},
		},
		"usage": map[string]any{"prompt_tokens": 120, "completion_tokens": 20},
	}
}

func TestSynthesizerOmitsMaxTokensWhenUnconfigured(t *testing.T) {
	srv, reqs := newSynthesizerCaptureServer(t, func(_ int, _ map[string]any, w http.ResponseWriter) {
		_ = json.NewEncoder(w).Encode(synthValidCompletion("ok"))
	})
	ps := &ProviderSynthesizer{HTTPClient: &provider.HTTPClient{Inner: srv.Client()}}
	out, err := ps.callEndpoint(context.Background(), synthEndpoint(srv.URL, nil), "system", "user", domain.CapabilityPlatformHelp, nil)
	if err != nil {
		t.Fatalf("callEndpoint failed: %v", err)
	}
	if out.AssistantReply != "ok" {
		t.Fatalf("unexpected reply: %+v", out)
	}
	if len(*reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(*reqs))
	}
	if _, exists := (*reqs)[0]["max_tokens"]; exists {
		t.Fatalf("expected max_tokens omitted when unconfigured, got %v", (*reqs)[0]["max_tokens"])
	}
}

func TestSynthesizerDynamicBudgetWithContextWindowAndLongPrompt(t *testing.T) {
	srv, reqs := newSynthesizerCaptureServer(t, func(_ int, _ map[string]any, w http.ResponseWriter) {
		_ = json.NewEncoder(w).Encode(synthValidCompletion("budgeted"))
	})
	ep := synthEndpoint(srv.URL, map[string]any{
		"max_tokens":     float64(4096),
		"context_window": float64(8192),
	})
	ps := &ProviderSynthesizer{HTTPClient: &provider.HTTPClient{Inner: srv.Client()}}
	longPrompt := strings.Repeat("SELECT id, user_id, status FROM audit_events WHERE created_at > '2026-01-01'; ", 360)
	out, err := ps.callEndpoint(context.Background(), ep, "system prompt", longPrompt, domain.CapabilitySQLOptimize, nil)
	if err != nil {
		t.Fatalf("callEndpoint failed: %v", err)
	}
	if out.AssistantReply != "budgeted" {
		t.Fatalf("unexpected reply: %+v", out)
	}
	if len(*reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(*reqs))
	}
	mt, ok := (*reqs)[0]["max_tokens"].(float64)
	if !ok || mt < 2000 || mt >= 3000 {
		t.Fatalf("expected dynamically reduced lower-bound max_tokens in [2000, 3000), got %v", (*reqs)[0]["max_tokens"])
	}
}

func TestSynthesizerSelfHealsContextOverflowOnce(t *testing.T) {
	srv, reqs := newSynthesizerCaptureServer(t, func(attempt int, _ map[string]any, w http.ResponseWriter) {
		if attempt == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"This model's maximum context length is 8192 tokens. However, you requested 9408 tokens (5312 in the messages, 4096 in the completion). Please reduce the length of the messages or completion."}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(synthValidCompletion("healed"))
	})
	ep := synthEndpoint(srv.URL, map[string]any{"max_tokens": float64(4096)})
	ps := &ProviderSynthesizer{HTTPClient: &provider.HTTPClient{Inner: srv.Client()}}
	out, err := ps.callEndpoint(context.Background(), ep, "system", "user", domain.CapabilityPlatformHelp, nil)
	if err != nil {
		t.Fatalf("expected self-heal retry to succeed, got %v", err)
	}
	if out.AssistantReply != "healed" {
		t.Fatalf("unexpected reply: %+v", out)
	}
	if len(*reqs) != 2 {
		t.Fatalf("expected 2 requests (initial + 1 retry), got %d", len(*reqs))
	}
	if (*reqs)[0]["max_tokens"] != float64(4096) {
		t.Fatalf("expected first request max_tokens=4096, got %v", (*reqs)[0]["max_tokens"])
	}
	if (*reqs)[1]["max_tokens"] != float64(2864) {
		t.Fatalf("expected retry max_tokens=2864, got %v", (*reqs)[1]["max_tokens"])
	}
}

func TestSynthesizerPromptExceedingContextFailsFastWithoutRetry(t *testing.T) {
	srv, reqs := newSynthesizerCaptureServer(t, func(_ int, _ map[string]any, w http.ResponseWriter) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"This model's maximum context length is 8192 tokens. However, you requested 12396 tokens (8300 in the messages, 4096 in the completion)."}}`))
	})
	ep := synthEndpoint(srv.URL, map[string]any{"max_tokens": float64(4096)})
	ps := &ProviderSynthesizer{HTTPClient: &provider.HTTPClient{Inner: srv.Client()}}
	_, err := ps.callEndpoint(context.Background(), ep, "system", "user", domain.CapabilityPlatformHelp, nil)
	if err == nil {
		t.Fatal("expected prompt overflow error")
	}
	if provider.ExtractHTTPStatus(err) != http.StatusBadRequest {
		t.Fatalf("expected wrapped HTTPStatusError 400, got %d (%v)", provider.ExtractHTTPStatus(err), err)
	}
	if len(*reqs) != 1 {
		t.Fatalf("expected fast fail with 1 request, got %d", len(*reqs))
	}
}

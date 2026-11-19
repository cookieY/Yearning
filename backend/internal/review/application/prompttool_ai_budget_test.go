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

	"github.com/cookieY/YearningX/backend/internal/review/domain"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
)

func newPromptToolCaptureServer(t *testing.T, handler func(attempt int, req map[string]any, w http.ResponseWriter)) (*httptest.Server, *[]map[string]any) {
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

func TestCallEndpointJSONOmitsMaxTokensWhenUnconfigured(t *testing.T) {
	srv, reqs := newPromptToolCaptureServer(t, func(_ int, _ map[string]any, w http.ResponseWriter) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": `{"ok":true}`}}},
		})
	})
	ep := provider.Endpoint{
		ProviderID: "p1", Name: "san-ep", BaseURL: srv.URL, ModelName: "san", APIKey: "sk-test",
	}
	got, err := callEndpointJSON(context.Background(), &provider.HTTPClient{Inner: srv.Client()}, ep, "sys", "usr")
	if err != nil {
		t.Fatalf("callEndpointJSON failed: %v", err)
	}
	if got != `{"ok":true}` {
		t.Fatalf("unexpected response: %q", got)
	}
	if len(*reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(*reqs))
	}
	if _, exists := (*reqs)[0]["max_tokens"]; exists {
		t.Fatalf("expected max_tokens omitted when unconfigured, got %v", (*reqs)[0]["max_tokens"])
	}
}

func TestCallEndpointJSONDynamicBudgetWithContextWindowAndLongPrompt(t *testing.T) {
	srv, reqs := newPromptToolCaptureServer(t, func(_ int, _ map[string]any, w http.ResponseWriter) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": `{"ok":true}`}}},
		})
	})
	ep := provider.Endpoint{
		ProviderID: "p1", Name: "san-ep", BaseURL: srv.URL, ModelName: "san", APIKey: "sk-test",
		Configuration: map[string]any{
			"max_tokens":     float64(4096),
			"context_window": float64(8192),
		},
	}
	longPrompt := strings.Repeat("SELECT id, order_no, status FROM trade_orders WHERE tenant_id = 42; ", 410)
	got, err := callEndpointJSON(context.Background(), &provider.HTTPClient{Inner: srv.Client()}, ep, "sys", longPrompt)
	if err != nil {
		t.Fatalf("callEndpointJSON failed: %v", err)
	}
	if got != `{"ok":true}` {
		t.Fatalf("unexpected response: %q", got)
	}
	if len(*reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(*reqs))
	}
	mt, ok := (*reqs)[0]["max_tokens"].(float64)
	if !ok || mt < 2000 || mt >= 3000 {
		t.Fatalf("expected dynamically reduced lower-bound max_tokens in [2000, 3000), got %v", (*reqs)[0]["max_tokens"])
	}
}

func TestCallEndpointJSONSelfHealsContextOverflowOnce(t *testing.T) {
	srv, reqs := newPromptToolCaptureServer(t, func(attempt int, _ map[string]any, w http.ResponseWriter) {
		if attempt == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"This model's maximum context length is 8192 tokens. However, you requested 9408 tokens (5312 in the messages, 4096 in the completion). Please reduce the length of the messages or completion."}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": `{"healed":true}`}}},
		})
	})
	ep := provider.Endpoint{
		ProviderID: "p1", Name: "san-ep", BaseURL: srv.URL, ModelName: "san", APIKey: "sk-test",
		Configuration: map[string]any{"max_tokens": float64(4096)},
	}
	got, err := callEndpointJSON(context.Background(), &provider.HTTPClient{Inner: srv.Client()}, ep, "sys", "usr")
	if err != nil {
		t.Fatalf("expected self-heal retry to succeed, got %v", err)
	}
	if got != `{"healed":true}` {
		t.Fatalf("unexpected response: %q", got)
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

func TestCallEndpointJSONPromptExceedingContextFailsFastWithoutRetry(t *testing.T) {
	srv, reqs := newPromptToolCaptureServer(t, func(_ int, _ map[string]any, w http.ResponseWriter) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"This model's maximum context length is 8192 tokens. However, you requested 12396 tokens (8300 in the messages, 4096 in the completion)."}}`))
	})
	ep := provider.Endpoint{
		ProviderID: "p1", Name: "san-ep", BaseURL: srv.URL, ModelName: "san", APIKey: "sk-test",
		Configuration: map[string]any{"max_tokens": float64(4096)},
	}
	_, err := callEndpointJSON(context.Background(), &provider.HTTPClient{Inner: srv.Client()}, ep, "sys", "usr")
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

func TestValidateAndViewContextWindowConfiguration(t *testing.T) {
	if err := ValidateProviderConfiguration(map[string]any{"context_window": float64(8192), "max_tokens": float64(4096)}); err != nil {
		t.Fatalf("expected valid context_window accepted, got %v", err)
	}
	for _, bad := range []any{float64(0), float64(-1), float64(1.5), "8192"} {
		err := ValidateProviderConfiguration(map[string]any{"context_window": bad})
		if !domain.IsCode(err, 1001) {
			t.Fatalf("expected 1001 for invalid context_window %v, got %v", bad, err)
		}
	}
	view := configurationView([]byte(`{"max_tokens":4096,"context_window":8192}`))
	if view == nil || view.ContextWindow == nil || *view.ContextWindow != 8192 {
		t.Fatalf("expected context_window read back in view, got %+v", view)
	}
	viewWithout := configurationView([]byte(`{"max_tokens":4096}`))
	if viewWithout == nil || viewWithout.ContextWindow != nil {
		t.Fatalf("expected nil context_window when absent, got %+v", viewWithout)
	}
}

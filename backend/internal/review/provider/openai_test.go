//go:build integration

// Model corresponds to knowledge_entries (mutable_aggregate; gorm registry).
package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAIAdapterRejectsUnknownFields(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{
					"content": `{"schema_version":1,"review_run_id":"run-1","outcome":"complete","summary":"s","findings":[],"tool_requests":[],"rogue":"x"}`,
				},
			}},
		}
		_ = json.NewEncoder(w).Encode(body)
	}))
	defer srv.Close()
	ep := Endpoint{ProviderID: "p1", Name: "t", BaseURL: srv.URL, ModelName: "m", APIKey: "k",
		PrivacyContractHash: strings.Repeat("a", 64), OutputSchemaHash: strings.Repeat("b", 64)}
	adapter, err := NewOpenAIAdapter(ep, &HTTPClient{Inner: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	_, err = adapter.ReviewStructured(context.Background(), Request{ReviewRunID: "run-1"})
	if err == nil {
		t.Fatal("production adapter accepted unknown response fields")
	}
}

func TestOpenAIAdapterSendsSystemPrompt(t *testing.T) {
	injected := "<audit-skill>mandatory rule</audit-skill>"
	var gotSystem string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		for _, m := range req.Messages {
			if m.Role == "system" {
				gotSystem = m.Content
			}
		}
		body := map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{
					"content": `{"schema_version":1,"review_run_id":"run-1","outcome":"complete","summary":"s","findings":[],"tool_requests":[]}`,
				},
			}},
		}
		_ = json.NewEncoder(w).Encode(body)
	}))
	defer srv.Close()
	ep := Endpoint{ProviderID: "p1", Name: "t", BaseURL: srv.URL, ModelName: "m", APIKey: "k",
		PrivacyContractHash: strings.Repeat("a", 64), OutputSchemaHash: strings.Repeat("b", 64)}
	adapter, err := NewOpenAIAdapter(ep, &HTTPClient{Inner: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	_, err = adapter.ReviewStructured(context.Background(), Request{ReviewRunID: "run-1", SystemPrompt: injected})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotSystem, injected) {
		t.Fatalf("system prompt injection not sent, got: %q", gotSystem)
	}
}

// TableName returns the explicit table name.
func TestOpenAIAdapterRetriesThenSucceeds(t *testing.T) {
	calls := 0
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		body := map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{
					"content": `{"schema_version":1,"review_run_id":"run-1","outcome":"complete","summary":"s","findings":[],"tool_requests":[]}`,
				},
			}},
		}
		_ = json.NewEncoder(w).Encode(body)
	}))
	defer srv.Close()
	ep := Endpoint{ProviderID: "p1", Name: "t", BaseURL: srv.URL, ModelName: "m", APIKey: "k",
		PrivacyContractHash: strings.Repeat("a", 64), OutputSchemaHash: strings.Repeat("b", 64)}
	adapter, err := NewOpenAIAdapter(ep, &HTTPClient{Inner: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.ReviewStructured(context.Background(), Request{ReviewRunID: "run-1"}); err != nil {
		t.Fatalf("retry path failed: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected 2 calls (1 fail + 1 retry), got %d", calls)
	}
}

// ErrNotFound maps to 1002.
func TestOpenAIAdapterNoRetryOnAuth(t *testing.T) {
	calls := 0
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	ep := Endpoint{ProviderID: "p1", Name: "t", BaseURL: srv.URL, ModelName: "m", APIKey: "k",
		PrivacyContractHash: strings.Repeat("a", 64), OutputSchemaHash: strings.Repeat("b", 64)}
	adapter, _ := NewOpenAIAdapter(ep, &HTTPClient{Inner: srv.Client()})
	_, err := adapter.ReviewStructured(context.Background(), Request{ReviewRunID: "run-1"})
	if err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Fatalf("expected rejected, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("auth failure must not retry, got %d calls", calls)
	}
}

// ErrVersionConflict maps to 1003/1004.
func TestOpenAIAdapterSchemaRepairOnce(t *testing.T) {
	calls := 0
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var req struct {
			Messages []map[string]any `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		content := `{"schema_version":1,"review_run_id":"run-1","outcome":"complete","summary":"s","findings":[],"tool_requests":[],"extra":1}`
		if len(req.Messages) > 2 {
			content = `{"schema_version":1,"review_run_id":"run-1","outcome":"complete","summary":"fixed","findings":[],"tool_requests":[]}`
		}
		body := map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": content}}},
		}
		_ = json.NewEncoder(w).Encode(body)
	}))
	defer srv.Close()
	ep := Endpoint{ProviderID: "p1", Name: "t", BaseURL: srv.URL, ModelName: "m", APIKey: "k",
		PrivacyContractHash: strings.Repeat("a", 64), OutputSchemaHash: strings.Repeat("b", 64)}
	adapter, _ := NewOpenAIAdapter(ep, &HTTPClient{Inner: srv.Client()})
	out, err := adapter.ReviewStructured(context.Background(), Request{ReviewRunID: "run-1"})
	if err != nil {
		t.Fatalf("repair round failed: %v", err)
	}
	if out.Response.Summary != "fixed" || calls != 2 {
		t.Fatalf("repair not used: calls=%d summary=%q", calls, out.Response.Summary)
	}
}

// ErrConcurrent is used by the delete path under the C1004-only profile: admin_delete does not declare VERSION_CONFLICT (exhaustive profile), so an If-Match mismatch surfaces as CONCURRENT_MODIFICATION; the replace path (admin_replace declares both codes) still returns ErrVersionConflict.
func TestValidateConfigurationUsesRealSchema(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ResponseFormat *struct {
				JSONSchema *struct {
					Name   string `json:"name"`
					Schema any    `json:"schema"`
				} `json:"json_schema"`
			} `json:"response_format"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.ResponseFormat == nil || req.ResponseFormat.JSONSchema == nil ||
			req.ResponseFormat.JSONSchema.Name != "yearning_review_output" {
			t.Errorf("probe must use the production output schema, got: %+v", req.ResponseFormat)
		}
		// Repository provides data access (transactions are declared by the service layer).
		_, _ = w.Write([]byte("here is some prose instead of json"))
	}))
	defer srv.Close()
	ep := Endpoint{ProviderID: "p1", Name: "t", BaseURL: srv.URL, ModelName: "m", APIKey: "k",
		PrivacyContractHash: strings.Repeat("a", 64), OutputSchemaHash: strings.Repeat("b", 64)}
	adapter, _ := NewOpenAIAdapter(ep, &HTTPClient{Inner: srv.Client()})
	if err := adapter.ValidateConfiguration(context.Background()); err == nil {
		t.Fatal("free-form endpoint must fail the real-schema probe")
	}
}

// toEntry converts a row to an aggregate.
func TestValidateConfigurationPassesConformantVendor(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{
					"content": `{"schema_version":1,"review_run_id":"00000000-0000-4000-8000-000000000000","outcome":"complete","summary":"probe","findings":[],"tool_requests":[]}`,
				},
			}},
		}
		_ = json.NewEncoder(w).Encode(body)
	}))
	defer srv.Close()
	ep := Endpoint{ProviderID: "p1", Name: "t", BaseURL: srv.URL, ModelName: "m", APIKey: "k",
		PrivacyContractHash: strings.Repeat("a", 64), OutputSchemaHash: strings.Repeat("b", 64)}
	adapter, _ := NewOpenAIAdapter(ep, &HTTPClient{Inner: srv.Client()})
	if err := adapter.ValidateConfiguration(context.Background()); err != nil {
		t.Fatalf("conformant vendor failed probe: %v", err)
	}
}

// toModel converts an aggregate to a row (with canonical JSON definition).
func TestConfigurationMaxTokensPlumbed(t *testing.T) {
	var gotMax int
	var gotTemp *float64
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			MaxTokens   int      `json:"max_tokens"`
			Temperature *float64 `json:"temperature"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		gotMax, gotTemp = req.MaxTokens, req.Temperature
		body := map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{
					"content": `{"schema_version":1,"review_run_id":"run-1","outcome":"complete","summary":"s","findings":[],"tool_requests":[]}`,
				},
			}},
		}
		_ = json.NewEncoder(w).Encode(body)
	}))
	defer srv.Close()
	ep := Endpoint{ProviderID: "p1", Name: "t", BaseURL: srv.URL, ModelName: "m", APIKey: "k",
		PrivacyContractHash: strings.Repeat("a", 64), OutputSchemaHash: strings.Repeat("b", 64),
		Configuration: map[string]any{"max_tokens": 8192.0, "temperature": 0.1}}
	adapter, _ := NewOpenAIAdapter(ep, &HTTPClient{Inner: srv.Client()})
	if _, err := adapter.ReviewStructured(context.Background(), Request{ReviewRunID: "run-1"}); err != nil {
		t.Fatal(err)
	}
	if gotMax != 8192 {
		t.Fatalf("max_tokens not plumbed: %d", gotMax)
	}
	if gotTemp == nil || *gotTemp != 0.1 {
		t.Fatalf("temperature not plumbed: %v", gotTemp)
	}
}

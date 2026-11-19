// Validate checks Definition constraints: non-empty body, template is a Finding subset, allowlist is within controlledSeverities, and version >= 1.
package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func modeTestEndpoint(base string) Endpoint {
	return Endpoint{ProviderID: "p1", Name: "t", BaseURL: base, ModelName: "m", APIKey: "k",
		PrivacyContractHash: strings.Repeat("a", 64), OutputSchemaHash: strings.Repeat("b", 64)}
}

func modeTestAdapter(t *testing.T, srv *httptest.Server, cfg map[string]any) *OpenAIAdapter {
	t.Helper()
	ep := modeTestEndpoint(srv.URL)
	ep.Configuration = cfg
	adapter, err := NewOpenAIAdapter(ep, &HTTPClient{Inner: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}

func validChatBody() map[string]any {
	return map[string]any{
		"choices": []map[string]any{{
			"message": map[string]any{
				"content": `{"schema_version":1,"review_run_id":"` + ProbeRunID + `","outcome":"complete","summary":"s","findings":[],"tool_requests":[]}`,
			},
		}},
	}
}

// ComputeConfigHash computes the immutable configuration hash (canonical JSON of name+scope+definition). Excludes state/timestamps/creator — identical definitions in different states do not alter the semantic key outside the hash.
func requestMode(t *testing.T, r *http.Request) (string, string) {
	t.Helper()
	var req struct {
		ResponseFormat *struct {
			Type string `json:"type"`
		} `json:"response_format"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		t.Fatal(err)
	}
	mode := ""
	if req.ResponseFormat != nil {
		mode = req.ResponseFormat.Type
	}
	sys := ""
	for _, m := range req.Messages {
		if m.Role == "system" {
			sys = m.Content
		}
	}
	return mode, sys
}

func TestNegotiateOutputModeStrictFirst(t *testing.T) {
	calls := 0
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		mode, _ := requestMode(t, r)
		if mode != OutputModeJSONSchema {
			t.Fatalf("expected strict first, got %q", mode)
		}
		_ = json.NewEncoder(w).Encode(validChatBody())
	}))
	defer srv.Close()
	mode, err := modeTestAdapter(t, srv, nil).NegotiateOutputMode(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if mode != OutputModeJSONSchema || calls != 1 {
		t.Fatalf("mode=%q calls=%d", mode, calls)
	}
}

func TestNegotiateOutputModeFallsBackOn400(t *testing.T) {
	calls := 0
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		mode, sys := requestMode(t, r)
		if mode == OutputModeJSONSchema {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"only json_object is supported"}}`))
			return
		}
		// TouchedObjects is the exact set of objects touched by a single Stage (scope matching input; derived by the application layer from decrypted payloads during freezing, with plaintext residing only in memory). DatasourceID, DatabaseName, and Tables must come from the same Stage and must not be split into unrelated sets across multiple Stages, otherwise same-named tables would falsely match across datasources or schemas.
		if !strings.Contains(sys, "OUTPUT FORMAT") || !strings.Contains(sys, "JSON Schema") || !strings.Contains(sys, "json") {
			t.Fatalf("json_object replay missing schema prompt: %.120q", sys)
		}
		_ = json.NewEncoder(w).Encode(validChatBody())
	}))
	defer srv.Close()
	mode, err := modeTestAdapter(t, srv, nil).NegotiateOutputMode(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if mode != OutputModeJSONObject || calls != 2 {
		t.Fatalf("mode=%q calls=%d", mode, calls)
	}
}

func TestNegotiateOutputModeBothFailSurfacesFallbackReason(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mode, _ := requestMode(t, r)
		w.WriteHeader(http.StatusBadRequest)
		if mode == OutputModeJSONSchema {
			_, _ = w.Write([]byte(`{"error":{"message":"json_schema unsupported"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"error":{"message":"Model Not Exist"}}`))
	}))
	defer srv.Close()
	_, err := modeTestAdapter(t, srv, nil).NegotiateOutputMode(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "Model Not Exist") {
		t.Fatalf("fallback attempt reason not surfaced: %v", err)
	}
}

func TestNegotiateOutputModeNoFallbackOnAuth(t *testing.T) {
	calls := 0
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Authentication Fails"}}`))
	}))
	defer srv.Close()
	_, err := modeTestAdapter(t, srv, nil).NegotiateOutputMode(context.Background())
	if !errors.Is(err, ErrRejected) {
		t.Fatalf("expected ErrRejected, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("auth failure must not trigger fallback, calls=%d", calls)
	}
}

func TestReviewStructuredUsesConfiguredMode(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mode, sys := requestMode(t, r)
		if mode != OutputModeJSONObject {
			t.Fatalf("expected configured json_object, got %q", mode)
		}
		if !strings.Contains(sys, "OUTPUT FORMAT") {
			t.Fatal("json_object runtime must inject schema prompt")
		}
		_ = json.NewEncoder(w).Encode(validChatBody())
	}))
	defer srv.Close()
	adapter := modeTestAdapter(t, srv, map[string]any{ConfigKeyOutputMode: OutputModeJSONObject})
	if _, err := adapter.ReviewStructured(context.Background(), Request{ReviewRunID: ProbeRunID}); err != nil {
		t.Fatal(err)
	}
}

func TestListModelsSuccessSortedDedup(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer k" {
			t.Fatal("missing bearer credential")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"id": "deepseek-chat"}, {"id": "deepseek-reasoner"}, {"id": "deepseek-chat"}},
		})
	}))
	defer srv.Close()
	restore := modelsHTTPClient
	modelsHTTPClient = &HTTPClient{Inner: srv.Client()}
	defer func() { modelsHTTPClient = restore }()
	models, err := ListModels(context.Background(), srv.URL, "k")
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0] != "deepseek-chat" || models[1] != "deepseek-reasoner" {
		t.Fatalf("unexpected models %v", models)
	}
}

func TestListModelsUpstreamFailures(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Authentication Fails, Your api key: ****xx is invalid"}}`))
	}))
	defer srv.Close()
	restore := modelsHTTPClient
	modelsHTTPClient = &HTTPClient{Inner: srv.Client()}
	defer func() { modelsHTTPClient = restore }()
	_, err := ListModels(context.Background(), srv.URL, "bad")
	if !errors.Is(err, ErrRejected) || !strings.Contains(err.Error(), "Authentication Fails") {
		t.Fatalf("expected rejected with snapshot, got %v", err)
	}
}

func TestListModelsHTTPSEnforced(t *testing.T) {
	if _, err := ListModels(context.Background(), "http://api.example.com", "k"); err == nil {
		t.Fatal("plain http base_url must be rejected")
	}
}

func TestResponsesAPIRelayRequiresJSONInUserMessageAndFallsBackOn500FormatError(t *testing.T) {
	calls := 0
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var req struct {
			ResponseFormat *struct {
				Type string `json:"type"`
			} `json:"response_format"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		userContent := ""
		for _, m := range req.Messages {
			if m.Role == "user" {
				userContent = m.Content
			}
		}
		if !strings.Contains(strings.ToLower(userContent), "json") {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"message":"Response input messages must contain the word 'json' in some form to use '[upstream]' of type 'json_object'."}}`))
			return
		}
		if req.ResponseFormat != nil && req.ResponseFormat.Type == OutputModeJSONSchema {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"message":"upstream does not support response_format json_schema"}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(validChatBody())
	}))
	defer srv.Close()
	mode, err := modeTestAdapter(t, srv, nil).NegotiateOutputMode(context.Background())
	if err != nil {
		t.Fatalf("expected relay negotiation to succeed, got: %v", err)
	}
	if mode != OutputModeJSONObject || calls != 2 {
		t.Fatalf("expected OutputModeJSONObject in 2 calls (no 500 retry loop on format error), got mode=%q calls=%d", mode, calls)
	}
}

func TestReviewStructuredFallsBackOnJSONSchemaRejectionAndUnwrapsMarkdownFence(t *testing.T) {
	calls := 0
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		mode, _ := requestMode(t, r)
		if mode == OutputModeJSONSchema {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"response_format json_schema is not supported by this model"}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{
					"content": "<think>\nchecking sql\n</think>\n```json\n{\"schema_version\":1,\"review_run_id\":\"" + ProbeRunID + "\",\"outcome\":\"complete\",\"summary\":\"ok\",\"findings\":[],\"tool_requests\":[]}\n```",
				},
			}},
		})
	}))
	defer srv.Close()
	out, err := modeTestAdapter(t, srv, nil).ReviewStructured(context.Background(), Request{ReviewRunID: ProbeRunID})
	if err != nil {
		t.Fatalf("expected runtime fallback and markdown fence unwrapping to succeed, got: %v", err)
	}
	if out.Response == nil || out.Response.Summary != "ok" || len(out.Calls) != 2 {
		t.Fatalf("unexpected outcome: %+v", out)
	}
}

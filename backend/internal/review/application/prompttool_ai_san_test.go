package application

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/platform/addrguard"
	"github.com/cookieY/YearningX/backend/internal/review/promptprofile"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestCallEndpointsJSON_SANVsDefaultAndMixedFallback(t *testing.T) {
	t.Cleanup(addrguard.SetLoopbackBypassForTest(true))
	ctx := context.Background()

	type seenReq struct {
		URL    string
		Model  string
		System string
		User   string
	}
	var seen []seenReq

	hc := &provider.HTTPClient{
		Inner: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				body, _ := io.ReadAll(req.Body)
				var payload struct {
					Model    string `json:"model"`
					Messages []struct {
						Role    string `json:"role"`
						Content string `json:"content"`
					} `json:"messages"`
				}
				_ = json.Unmarshal(body, &payload)
				sr := seenReq{URL: req.URL.String(), Model: payload.Model}
				for _, m := range payload.Messages {
					switch m.Role {
					case "system":
						sr.System = m.Content
					case "user":
						sr.User = m.Content
					}
				}
				seen = append(seen, sr)

				if payload.Model == "san-4b" {
					return &http.Response{
						StatusCode: http.StatusUnauthorized,
						Header:     http.Header{"Content-Type": []string{"application/json"}},
						Body:       io.NopCloser(strings.NewReader(`{"error":"unauthorized"}`)),
					}, nil
				}
				respBytes, _ := json.Marshal(map[string]any{
					"choices": []map[string]any{
						{"message": map[string]any{"content": "```json\n{\"ok\":true}\n```"}},
					},
				})
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(string(respBytes))),
				}, nil
			}),
		},
	}

	endpoints := []provider.Endpoint{
		{
			ProviderID:        "p-san",
			Name:              "local-san",
			ProviderKind:      "openai_compatible",
			BaseURL:           "http://192.168.10.20:8000/v1",
			ModelName:         "san-4b",
			SelectionPriority: 1,
			APIKey:            "sk-san",
		},
		{
			ProviderID:        "p-v31",
			Name:              "vllm-v31",
			ProviderKind:      "openai_compatible",
			BaseURL:           "http://10.0.1.30:8000/v1",
			ModelName:         "san-v3.1",
			SelectionPriority: 2,
			APIKey:            "sk-v31",
		},
	}

	raw, usedSAN, err := callEndpointsJSON(ctx, hc, endpoints, func(ep provider.Endpoint) (string, string, error) {
		if ep.EffectivePromptProfile() == promptprofile.ProfileSAN {
			return "[SAN_HEAD=ai-diagnose]", `{"sql_template":"SELECT * FROM ledger_db.transfers WHERE secret_col = 'v'"}`, nil
		}
		return "Verbose system prompt", `{"desensitized_template":"SELECT * FROM table_1 WHERE column_1 = :val_str_1"}`, nil
	})
	if err != nil {
		t.Fatalf("callEndpointsJSON error: %v", err)
	}
	if usedSAN {
		t.Fatal("expected usedSAN=false after fallback to san-v3.1")
	}
	if strings.TrimSpace(raw) != `{"ok":true}` {
		t.Fatalf("unexpected stripped json content: %q", raw)
	}
	if len(seen) != 2 {
		t.Fatalf("expected 2 requests across mixed chain, got %d", len(seen))
	}
	// First request (SAN) MUST NOT get "Respond with a valid json object." appended.
	if strings.Contains(seen[0].System, "Respond with a valid json object.") || strings.Contains(seen[0].User, "Respond with a valid json object.") {
		t.Fatalf("expected SAN endpoint to skip json suffix, got sys=%q user=%q", seen[0].System, seen[0].User)
	}
	if !strings.Contains(seen[0].User, "ledger_db.transfers") {
		t.Fatalf("expected raw template sent to SAN endpoint, got %q", seen[0].User)
	}
	// Second request (san-v3.1 -> default) MUST receive ONLY anonymized payload and get default json suffix.
	if strings.Contains(seen[1].User, "ledger_db") || strings.Contains(seen[1].User, "transfers") || strings.Contains(seen[1].User, "secret_col") {
		t.Fatalf("cross-profile leak to san-v3.1 fallback: %q", seen[1].User)
	}
	if !strings.Contains(seen[1].System, "Respond with a valid json object.") {
		t.Fatalf("expected default endpoint to append json suffix when system prompt lacks json keyword, got %q", seen[1].System)
	}

	// Verify SAN win directly on RFC1918 endpoint reports usedSAN=true and omits json suffix.
	seen = nil
	rawSAN, usedSAN2, err := callEndpointsJSON(ctx, hc, []provider.Endpoint{
		{
			ProviderID:        "p-san-ok",
			Name:              "local-san-ok",
			ProviderKind:      "openai_compatible",
			BaseURL:           "http://192.168.10.20:8000/v1",
			ModelName:         "sql-audit-nano",
			SelectionPriority: 1,
			APIKey:            "sk-san",
			Configuration:     map[string]any{"system_prompt_profile": "san"},
		},
	}, func(ep provider.Endpoint) (string, string, error) {
		// Force non-401 mock branch using different model string override? Keep ep.ModelName != san-4b!
		return promptprofile.BuildSANDiagnoseSystemPrompt("zh-CN"), `{"sql_template":"SELECT 1"}`, nil
	})
	if err != nil || !usedSAN2 || strings.TrimSpace(rawSAN) != `{"ok":true}` {
		t.Fatalf("SAN endpoint direct success failed: raw=%q usedSAN=%v err=%v", rawSAN, usedSAN2, err)
	}
	if len(seen) != 1 || strings.Contains(seen[0].User, "Respond with a valid json object.") {
		t.Fatalf("expected single SAN call without json suffix, got %+v", seen)
	}

	// Verify public HTTP endpoint is rejected.
	if _, _, err := callEndpointsJSON(ctx, hc, []provider.Endpoint{
		{
			ProviderID:   "p-pub-http",
			Name:         "bad-http",
			ProviderKind: "openai_compatible",
			BaseURL:      "http://8.8.8.8:8000/v1",
			ModelName:    "san",
			APIKey:       "sk-bad",
		},
	}, func(ep provider.Endpoint) (string, string, error) {
		return "sys", `{"sql_template":"SELECT 1"}`, nil
	}); err == nil {
		t.Fatal("expected public http:// endpoint to be rejected by ParseBaseURL")
	}
}

func TestCallEndpointsJSON_ErrorsAndGenerationParams(t *testing.T) {
	t.Cleanup(addrguard.SetLoopbackBypassForTest(true))
	ctx := context.Background()

	var nilSvc *Services
	if _, err := nilSvc.CallActiveProviderJSON(ctx, "sys", "usr"); err == nil {
		t.Fatal("expected error from nil Services CallActiveProviderJSON")
	}
	if _, _, err := (&Services{}).CallActiveProviderJSONPerEndpoint(ctx, func(provider.Endpoint) (string, string, error) {
		return "sys", "usr", nil
	}); err == nil {
		t.Fatal("expected error from uninitialized Services CallActiveProviderJSONPerEndpoint")
	}
	if _, _, err := callEndpointsJSON(ctx, nil, nil, func(provider.Endpoint) (string, string, error) {
		return "sys", "usr", nil
	}); err == nil {
		t.Fatal("expected empty provider chain error")
	}
	if _, _, err := callEndpointsJSON(ctx, nil, []provider.Endpoint{{Name: "ep1"}}, nil); err == nil {
		t.Fatal("expected nil prompt builder error")
	}

	builderErr := errors.New("builder boom")
	if _, _, err := callEndpointsJSON(ctx, nil, []provider.Endpoint{
		{BaseURL: "http://127.0.0.1:8000/v1", ModelName: "san-4b"},
	}, func(provider.Endpoint) (string, string, error) {
		return "", "", builderErr
	}); !errors.Is(err, builderErr) {
		t.Fatalf("expected builder error, got %v", err)
	}

	var captured map[string]any
	hc := &provider.HTTPClient{
		Inner: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				var body map[string]any
				_ = json.NewDecoder(req.Body).Decode(&body)
				captured = body
				model, _ := body["model"].(string)
				switch model {
				case "bad-status":
					return &http.Response{
						StatusCode: http.StatusInternalServerError,
						Header:     http.Header{"Content-Type": []string{"application/json"}},
						Body:       io.NopCloser(strings.NewReader(`{"error":"server"}`)),
					}, nil
				case "bad-json":
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": []string{"application/json"}},
						Body:       io.NopCloser(strings.NewReader(`{not-json`)),
					}, nil
				case "empty-content":
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": []string{"application/json"}},
						Body:       io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"   "}}]}`)),
					}, nil
				default:
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": []string{"application/json"}},
						Body:       io.NopCloser(strings.NewReader("{\"choices\":[{\"message\":{\"content\":\"```\\n{\\\"ok\\\":2}\\n```\"}}]}")),
					}, nil
				}
			}),
		},
	}

	// Default endpoint forwards max_tokens > 4096, temperature, reasoning_effort and appends JSON instruction.
	raw, usedSAN, err := callEndpointsJSON(ctx, hc, []provider.Endpoint{
		{ProviderID: "p-bad-status", Name: "s1", ProviderKind: "openai_compatible", BaseURL: "http://127.0.0.1:8000/v1", ModelName: "bad-status", APIKey: "k1"},
		{ProviderID: "p-bad-json", Name: "s2", ProviderKind: "openai_compatible", BaseURL: "http://127.0.0.1:8000/v1", ModelName: "bad-json", APIKey: "k2"},
		{ProviderID: "p-empty", Name: "s3", ProviderKind: "openai_compatible", BaseURL: "http://127.0.0.1:8000/v1", ModelName: "empty-content", APIKey: "k3"},
		{
			ProviderID:   "p-ok",
			Name:         "s4",
			ProviderKind: "openai_compatible",
			BaseURL:      "http://127.0.0.1:8000/v1",
			ModelName:    "xmodel-v3.1",
			APIKey:       "k4",
			Configuration: map[string]any{
				"max_tokens":       float64(8192),
				"temperature":      0.25,
				"reasoning_effort": "high",
			},
		},
	}, func(provider.Endpoint) (string, string, error) {
		return "plain system prompt", "plain user prompt", nil
	})
	if err != nil {
		t.Fatalf("expected fallback to succeed, got %v", err)
	}
	if usedSAN || raw != `{"ok":2}` {
		t.Fatalf("unexpected fallback result: usedSAN=%v raw=%q", usedSAN, raw)
	}
	if captured["max_tokens"] != float64(8192) || captured["temperature"] != 0.25 || captured["reasoning_effort"] != "high" {
		t.Fatalf("expected generation params forwarded, got %+v", captured)
	}
	msgs, _ := captured["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	sysMsg, _ := msgs[0].(map[string]any)["content"].(string)
	usrMsg, _ := msgs[1].(map[string]any)["content"].(string)
	if !strings.Contains(sysMsg, "valid json object") || !strings.Contains(usrMsg, "valid json object") {
		t.Fatalf("expected default endpoint messages to include json instruction: sys=%q usr=%q", sysMsg, usrMsg)
	}

	// Empty completion without error synthesizes fallback error.
	if _, _, err := callEndpointsJSON(ctx, hc, []provider.Endpoint{
		{ProviderID: "p-empty", Name: "s3", ProviderKind: "openai_compatible", BaseURL: "http://127.0.0.1:8000/v1", ModelName: "empty-content", APIKey: "k3"},
	}, func(provider.Endpoint) (string, string, error) {
		return "sys json", "usr json", nil
	}); err == nil || !strings.Contains(err.Error(), "provider call failed") {
		t.Fatalf("expected provider call failed error for empty content, got %v", err)
	}

	// Transport error propagates.
	hcErr := &provider.HTTPClient{
		Inner: &http.Client{
			Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return nil, errors.New("dial timeout")
			}),
		},
	}
	if _, _, err := callEndpointsJSON(ctx, hcErr, []provider.Endpoint{
		{ProviderID: "p-err", Name: "s5", ProviderKind: "openai_compatible", BaseURL: "http://127.0.0.1:8000/v1", ModelName: "san-4b", APIKey: "k5"},
	}, func(provider.Endpoint) (string, string, error) {
		return "sys", "usr", nil
	}); err == nil || !strings.Contains(err.Error(), "dial timeout") {
		t.Fatalf("expected transport error, got %v", err)
	}
}

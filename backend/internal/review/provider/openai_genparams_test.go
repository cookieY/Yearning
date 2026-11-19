// State defines the entry state (matching prompt_tools).
package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Provenance defines the entry source.
type genParamsCaptureServer struct {
	srv    *httptest.Server
	mu     sync.Mutex
	bodies []string
}

func newGenParamsCaptureServer(t *testing.T, responder func(int) string) *genParamsCaptureServer {
	t.Helper()
	c := &genParamsCaptureServer{}
	c.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		c.mu.Lock()
		c.bodies = append(c.bodies, string(buf))
		n := len(c.bodies)
		c.mu.Unlock()
		_, _ = w.Write([]byte(responder(n - 1)))
	}))
	t.Cleanup(c.srv.Close)
	return c
}

func (c *genParamsCaptureServer) body(t *testing.T, i int) map[string]any {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if i >= len(c.bodies) {
		t.Fatalf("captured body %d missing (got %d)", i, len(c.bodies))
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(c.bodies[i]), &m); err != nil {
		t.Fatalf("captured body is not JSON: %v", err)
	}
	return m
}

func conformantBody() string {
	return `{"choices":[{"message":{"content":"{\"schema_version\":1,\"review_run_id\":\"run-1\",\"outcome\":\"complete\",\"summary\":\"s\",\"findings\":[],\"tool_requests\":[]}"}}],"usage":{"prompt_tokens":100,"completion_tokens":20}}`
}

func genParamsEndpoint(url string, cfg map[string]any) Endpoint {
	return Endpoint{ProviderID: "p1", Name: "t", BaseURL: url, ModelName: "m", APIKey: "k",
		PrivacyContractHash: strings.Repeat("a", 64), OutputSchemaHash: strings.Repeat("b", 64),
		Configuration: cfg}
}

// Definition is the entry definition (isomorphic to Prompt Tool definition): knowledge_text body, v1-compatible max_severity, and version.
func TestGenParamsExplicitValuesSentVerbatim(t *testing.T) {
	c := newGenParamsCaptureServer(t, func(int) string { return conformantBody() })
	ep := genParamsEndpoint(c.srv.URL, map[string]any{
		"reasoning_effort": "high", "max_tokens": 4096.0, "temperature": 0.35,
	})
	adapter, _ := NewOpenAIAdapter(ep, &HTTPClient{Inner: c.srv.Client()})
	if _, err := adapter.ReviewStructured(context.Background(), Request{ReviewRunID: "run-1"}); err != nil {
		t.Fatal(err)
	}
	body := c.body(t, 0)
	if body["reasoning_effort"] != "high" {
		t.Fatalf("reasoning_effort not sent verbatim: %v", body["reasoning_effort"])
	}
	if mt, ok := body["max_tokens"].(float64); !ok || mt != 4096 {
		t.Fatalf("max_tokens not sent: %v", body["max_tokens"])
	}
	if temp, ok := body["temperature"].(float64); !ok || temp != 0.35 {
		t.Fatalf("temperature not sent: %v", body["temperature"])
	}
}

// Entry is the entry aggregate (write paths go through the application service). JSON tags align with the OpenAPI KnowledgeEntry schema (snake_case; CreatedByUserID is not exposed).
func TestGenParamsOmittedKeysAbsentFromRequest(t *testing.T) {
	c := newGenParamsCaptureServer(t, func(int) string { return conformantBody() })
	ep := genParamsEndpoint(c.srv.URL, nil)
	adapter, _ := NewOpenAIAdapter(ep, &HTTPClient{Inner: c.srv.Client()})
	if _, err := adapter.ReviewStructured(context.Background(), Request{ReviewRunID: "run-1"}); err != nil {
		t.Fatal(err)
	}
	body := c.body(t, 0)
	for _, key := range []string{"reasoning_effort", "max_tokens", "temperature"} {
		if _, present := body[key]; present {
			t.Fatalf("unconfigured %s must be omitted from request, got %v", key, body[key])
		}
	}
}

func TestGenParamsExplicitZeroTemperatureSurvives(t *testing.T) {
	c := newGenParamsCaptureServer(t, func(int) string { return conformantBody() })
	ep := genParamsEndpoint(c.srv.URL, map[string]any{"temperature": 0.0})
	adapter, _ := NewOpenAIAdapter(ep, &HTTPClient{Inner: c.srv.Client()})
	if _, err := adapter.ReviewStructured(context.Background(), Request{ReviewRunID: "run-1"}); err != nil {
		t.Fatal(err)
	}
	body := c.body(t, 0)
	temp, present := body["temperature"].(float64)
	if !present || temp != 0 {
		t.Fatalf("temperature=0 is a valid explicit value and must be sent, got %v (present=%v)", body["temperature"], present)
	}
}

// empty = global
func TestReasoningEffortEnumSentOnEachLevel(t *testing.T) {
	for _, level := range []string{"low", "medium", "high"} {
		c := newGenParamsCaptureServer(t, func(int) string { return conformantBody() })
		ep := genParamsEndpoint(c.srv.URL, map[string]any{"reasoning_effort": level})
		adapter, _ := NewOpenAIAdapter(ep, &HTTPClient{Inner: c.srv.Client()})
		if _, err := adapter.ReviewStructured(context.Background(), Request{ReviewRunID: "run-1"}); err != nil {
			t.Fatal(err)
		}
		if got := c.body(t, 0)["reasoning_effort"]; got != level {
			t.Fatalf("level %s not sent verbatim: %v", level, got)
		}
	}
}

// required for datasource/table
func TestReasoningUnsupportedFailsReadably(t *testing.T) {
	c := newGenParamsCaptureServer(t, func(int) string {
		return `{"error":{"type":"invalid_request_error","message":"reasoning_effort not supported for this model"}}`
	})
	ep := genParamsEndpoint(c.srv.URL, map[string]any{"reasoning_effort": "high"})
	adapter, _ := NewOpenAIAdapter(ep, &HTTPClient{Inner: c.srv.Client()})
	_, err := adapter.ReviewStructured(context.Background(), Request{ReviewRunID: "run-1"})
	if err == nil {
		t.Fatal("unsupported reasoning_effort must fail readably")
	}
	if !strings.Contains(err.Error(), "reasoning_effort") {
		t.Fatalf("rejection reason should surface the upstream cause, got: %v", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.bodies) != 1 {
		t.Fatalf("must not auto-delete parameters and retry: %d requests captured", len(c.bodies))
	}
}

// required for table
type usageCaptureServer struct {
	srv   *httptest.Server
	out   CallOutcome
	outMu sync.Mutex
	err   error
}

func newUsageCaptureServer(t *testing.T, usageJSON string) *usageCaptureServer {
	t.Helper()
	u := &usageCaptureServer{}
	u.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"schema_version\":1,\"review_run_id\":\"run-1\",\"outcome\":\"complete\",\"summary\":\"s\",\"findings\":[],\"tool_requests\":[]}"}}],` + usageJSON + `}`))
	}))
	t.Cleanup(u.srv.Close)
	return u
}

func (u *usageCaptureServer) run(t *testing.T) {
	t.Helper()
	ep := genParamsEndpoint(u.srv.URL, nil)
	adapter, _ := NewOpenAIAdapter(ep, &HTTPClient{Inner: u.srv.Client()})
	outcome, err := adapter.ReviewStructured(context.Background(), Request{ReviewRunID: "run-1"})
	u.outMu.Lock()
	u.out, u.err = outcome, err
	u.outMu.Unlock()
}

// §18.66: system-derived key (exp.<id> or inherited from migration), not exposed to user surface
func TestUsageThreeStateParsing(t *testing.T) {
	cases := []struct {
		name      string
		usageJSON string
		check     func(*testing.T, *UsageDetail)
	}{
		{"full detail", `"usage":{"prompt_tokens":1000,"completion_tokens":50,"prompt_tokens_details":{"cached_tokens":700,"cache_write_tokens":100},"completion_tokens_details":{"reasoning_tokens":30}}`,
			func(t *testing.T, d *UsageDetail) {
				if d == nil || d.InputTokens == nil || *d.InputTokens != 1000 ||
					d.OutputTokens == nil || *d.OutputTokens != 50 ||
					d.CachedTokens == nil || *d.CachedTokens != 700 ||
					d.CacheWriteTokens == nil || *d.CacheWriteTokens != 100 ||
					d.ReasoningTokens == nil || *d.ReasoningTokens != 30 {
					t.Fatalf("full detail misparsed: %+v", d)
				}
			}},
		{"explicit zero cache", `"usage":{"prompt_tokens":100,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":0}}`,
			func(t *testing.T, d *UsageDetail) {
				if d == nil || d.CachedTokens == nil || *d.CachedTokens != 0 {
					t.Fatalf("explicit zero must stay zero, not unknown: %+v", d)
				}
				if d.CacheWriteTokens != nil {
					t.Fatalf("missing cache_write must be unknown: %+v", d)
				}
			}},
		{"missing usage object", `"no_usage":true`,
			func(t *testing.T, d *UsageDetail) {
				if d != nil {
					t.Fatalf("missing usage must be nil detail: %+v", d)
				}
			}},
		{"negative and non-integer are unknown", `"usage":{"prompt_tokens":100,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":-3,"cache_write_tokens":1.5}}`,
			func(t *testing.T, d *UsageDetail) {
				if d.CachedTokens != nil || d.CacheWriteTokens != nil {
					t.Fatalf("negative/non-integer must be unknown, not zero: %+v", d)
				}
				if d.InputTokens == nil || *d.InputTokens != 100 {
					t.Fatalf("valid totals still parsed: %+v", d)
				}
			}},
		{"malformed usage object keeps response valid", `"usage":"not-an-object"`,
			func(t *testing.T, d *UsageDetail) {
				if d == nil {
					t.Fatal("usage object present but malformed must yield all-unknown detail, not nil")
				}
				if d.InputTokens != nil || d.CachedTokens != nil {
					t.Fatalf("malformed usage must be all-unknown: %+v", d)
				}
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u := newUsageCaptureServer(t, tc.usageJSON)
			u.run(t)
			if u.err != nil {
				t.Fatalf("response must stay valid regardless of usage shape: %v", u.err)
			}
			u.outMu.Lock()
			defer u.outMu.Unlock()
			if len(u.out.Calls) != 1 {
				t.Fatalf("expected one call record, got %d", len(u.out.Calls))
			}
			tc.check(t, u.out.Calls[0].Detail)
		})
	}
}

// ErrValidation indicates invalid input (maps to 1001).
func TestCallRecordCarriesParamsAndSendTime(t *testing.T) {
	temp := 0.7
	c := newGenParamsCaptureServer(t, func(int) string { return conformantBody() })
	ep := genParamsEndpoint(c.srv.URL, map[string]any{
		"reasoning_effort": "low", "max_tokens": 1024.0, "temperature": 0.7,
	})
	adapter, _ := NewOpenAIAdapter(ep, &HTTPClient{Inner: c.srv.Client()})
	outcome, err := adapter.ReviewStructured(context.Background(), Request{ReviewRunID: "run-1"})
	if err != nil {
		t.Fatal(err)
	}
	rec := outcome.Calls[0]
	if rec.ReasoningEffort != "low" || rec.MaxTokens != 1024 || rec.Temperature == nil || *rec.Temperature != temp {
		t.Fatalf("call record parameter snapshot mismatch: %+v", rec)
	}
	if rec.SentAt.IsZero() || rec.Outcome != CallOutcomeOK || rec.HTTPStatus != 200 {
		t.Fatalf("call record bookkeeping mismatch: %+v", rec)
	}
}

// ErrReferenced (1006): referenced by a Review; physical deletion is forbidden.
func TestSchemaFailedCallKeepsReportedUsage(t *testing.T) {
	c := newGenParamsCaptureServer(t, func(int) string {
		return `{"choices":[{"message":{"content":"not json at all"}}],"usage":{"prompt_tokens":120,"completion_tokens":8}}`
	})
	ep := genParamsEndpoint(c.srv.URL, nil)
	adapter, _ := NewOpenAIAdapter(ep, &HTTPClient{Inner: c.srv.Client()})
	outcome, err := adapter.ReviewStructured(context.Background(), Request{ReviewRunID: "run-1"})
	if err == nil {
		t.Fatal("schema-invalid output must fail")
	}
	// Validate validates entry fields and Definition constraints.
	if len(outcome.Calls) != 2 {
		t.Fatalf("expected initial+repair call records, got %d", len(outcome.Calls))
	}
	for i, rec := range outcome.Calls {
		if rec.Detail == nil || rec.Detail.InputTokens == nil || *rec.Detail.InputTokens != 120 {
			t.Fatalf("call %d lost reported usage: %+v", i, rec)
		}
	}
	if outcome.Calls[0].Outcome != CallOutcomeSchemaFailed {
		t.Fatalf("initial call must be schema_failed: %+v", outcome.Calls[0])
	}
}

// controlledSeverities is the controlled severity enum (must not be exceeded).
func TestRetryAttemptsEachRecorded(t *testing.T) {
	attempts := 0
	var mu sync.Mutex
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		attempts++
		n := attempts
		mu.Unlock()
		if n <= 2 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
			return
		}
		_, _ = w.Write([]byte(conformantBody()))
	}))
	defer srv.Close()
	ep := genParamsEndpoint(srv.URL, nil)
	adapter, _ := NewOpenAIAdapter(ep, &HTTPClient{Inner: srv.Client()})
	outcome, err := adapter.ReviewStructured(context.Background(), Request{ReviewRunID: "run-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(outcome.Calls) != 3 {
		t.Fatalf("each real HTTP attempt must be recorded: %d", len(outcome.Calls))
	}
	if outcome.Calls[0].HTTPStatus != 429 || outcome.Calls[0].Outcome != CallOutcomeError {
		t.Fatalf("first attempt must record 429 error: %+v", outcome.Calls[0])
	}
	if outcome.Calls[2].Outcome != CallOutcomeOK {
		t.Fatalf("final attempt must be ok: %+v", outcome.Calls[2])
	}
}

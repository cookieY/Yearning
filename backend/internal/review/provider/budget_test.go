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

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

type capturedChatRequest struct {
	Raw  map[string]any
	Body string
}

func newBudgetCaptureServer(t *testing.T, handler func(attempt int, req capturedChatRequest, w http.ResponseWriter)) (*httptest.Server, *[]capturedChatRequest) {
	t.Helper()
	var mu sync.Mutex
	var reqs []capturedChatRequest
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal(buf, &m); err != nil {
			t.Fatalf("unmarshal request body: %v", err)
		}
		capReq := capturedChatRequest{Raw: m, Body: string(buf)}
		mu.Lock()
		reqs = append(reqs, capReq)
		attempt := len(reqs)
		mu.Unlock()
		handler(attempt, capReq, w)
	}))
	t.Cleanup(srv.Close)
	return srv, &reqs
}

func budgetEndpoint(url string, cfg map[string]any) Endpoint {
	return Endpoint{
		ProviderID:          "p-budget",
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

func TestBudgetHelpersAndBoundaries(t *testing.T) {
	if got := ConfigPositiveInt(nil, "context_window"); got != 0 {
		t.Fatalf("expected 0 for nil cfg, got %d", got)
	}
	if got := ConfigPositiveInt(map[string]any{"context_window": float64(8192)}, "context_window"); got != 8192 {
		t.Fatalf("expected 8192 for float64, got %d", got)
	}
	if got := ConfigPositiveInt(map[string]any{"context_window": 16384}, "context_window"); got != 16384 {
		t.Fatalf("expected 16384 for int, got %d", got)
	}
	if got := ConfigPositiveInt(map[string]any{"context_window": float64(-10)}, "context_window"); got != 0 {
		t.Fatalf("expected 0 for negative float64, got %d", got)
	}
	if got := ConfigPositiveInt(map[string]any{"context_window": 12.5}, "context_window"); got != 0 {
		t.Fatalf("expected 0 for non-integer float64, got %d", got)
	}

	// Non-ASCII / CJK density remains higher than equivalent byte length of ASCII prose,
	// while staying a conservative lower bound so max_tokens is never silently under-budgeted.
	asciiEst := EstimateMessagesTokens([]PromptMessage{{Role: "user", Content: strings.Repeat("a", 200)}})
	cjkEst := EstimateMessagesTokens([]PromptMessage{{Role: "user", Content: strings.Repeat("\u6d4b\u8bd5", 100)}})
	if cjkEst <= asciiEst {
		t.Fatalf("expected CJK estimate (%d) > ASCII estimate (%d)", cjkEst, asciiEst)
	}
	// ~6000 real SQL tokens (430 repeats of ~14 tokens each) must estimate <= 6000 so
	// initial max_tokens >= 2192 (never silently truncated below actual remaining space).
	sql6k := strings.Repeat("SELECT col_alpha, col_beta FROM orders_ledger WHERE status = 1; ", 430)
	est6k := EstimateMessagesTokens([]PromptMessage{{Role: "user", Content: sql6k}})
	if est6k > 6000 {
		t.Fatalf("expected lower-bound estimate <= 6000 for ~6000-token SQL, got %d", est6k)
	}

	// Exact Rule B remaining boundaries: 255 omits max_tokens (0), 256 preserves 256.
	if got := resolveMaxTokensWithEstimate(4096, 8192, 8192-255); got != 0 {
		t.Fatalf("expected 0 (omit max_tokens) at remaining=255, got %d", got)
	}
	if got := resolveMaxTokensWithEstimate(4096, 8192, 8192-256); got != 256 {
		t.Fatalf("expected 256 at remaining=256, got %d", got)
	}

	// Context overflow detection and parsing: both legacy and newer vLLM error formats.
	vllmOld400 := `{"error":{"message":"This model's maximum context length is 8192 tokens. However, you requested 9092 tokens (6000 in the messages, 3092 in the completion). Please reduce the length of the messages or completion."}}`
	vllmNew400 := `{"error":{"message":"'max_tokens' or 'max_completion_tokens' is too large: 3092. This model's maximum context length is 8192 tokens and your request has 6000 input tokens (3092 > 8192 - 6000)"}}`
	for _, sample := range []string{vllmOld400, vllmNew400} {
		if !IsContextLengthOverflowError(http.StatusBadRequest, sample) {
			t.Fatalf("expected IsContextLengthOverflowError true for sample %q", sample)
		}
		gotCtx, gotPrompt, ok := ParseContextLengthOverflow(sample)
		if !ok || gotCtx != 8192 || gotPrompt != 6000 {
			t.Fatalf("expected (8192, 6000, true) for sample %q, got (%d, %d, %v)", sample, gotCtx, gotPrompt, ok)
		}
	}
	vllm400 := `{"error":{"message":"This model's maximum context length is 8192 tokens. However, you requested 9408 tokens (5312 in the messages, 4096 in the completion). Please reduce the length of the messages or completion."}}`
	if IsContextLengthOverflowError(http.StatusInternalServerError, vllm400) {
		t.Fatal("expected IsContextLengthOverflowError false for non-400 status")
	}
	retry, nextMax, tooLong := ResolveContextOverflowRetry(4096, http.StatusBadRequest, vllm400)
	if !retry || nextMax != 2864 || tooLong {
		t.Fatalf("unexpected retry resolution: retry=%v nextMax=%d tooLong=%v", retry, nextMax, tooLong)
	}
	// Unparseable context overflow message retries once with max_tokens omitted (0).
	retry2, nextMax2, tooLong2 := ResolveContextOverflowRetry(4096, http.StatusBadRequest, `{"error":{"code":"context_length_exceeded"}}`)
	if !retry2 || nextMax2 != 0 || tooLong2 {
		t.Fatalf("expected omit-max_tokens retry for unparseable overflow, got retry=%v nextMax=%d tooLong=%v", retry2, nextMax2, tooLong2)
	}
}

func TestOpenAIAdapterOmitsMaxTokensIncludingEmptyAndEOFRepair(t *testing.T) {
	for _, tc := range []struct {
		name      string
		firstBody string
	}{
		{name: "empty_content", firstBody: ""},
		{name: "truncated_json_eof", firstBody: `{"schema_version":1,"review_run_id":"run-1"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, reqs := newBudgetCaptureServer(t, func(attempt int, _ capturedChatRequest, w http.ResponseWriter) {
				content := tc.firstBody
				if attempt == 2 {
					content = `{"schema_version":1,"review_run_id":"run-1","outcome":"complete","summary":"repaired","findings":[],"tool_requests":[]}`
				}
				_ = json.NewEncoder(w).Encode(map[string]any{
					"choices": []map[string]any{{"message": map[string]any{"content": content}}},
				})
			})
			adapter, err := NewOpenAIAdapter(budgetEndpoint(srv.URL, nil), &HTTPClient{Inner: srv.Client()})
			if err != nil {
				t.Fatal(err)
			}
			out, err := adapter.ReviewStructured(context.Background(), Request{ReviewRunID: "run-1"})
			if err != nil {
				t.Fatalf("ReviewStructured failed: %v", err)
			}
			if out.Response == nil || out.Response.Summary != "repaired" {
				t.Fatalf("unexpected outcome: %+v", out)
			}
			if len(*reqs) != 2 {
				t.Fatalf("expected 2 calls (initial + repair), got %d", len(*reqs))
			}
			for i, r := range *reqs {
				if _, exists := r.Raw["max_tokens"]; exists {
					t.Fatalf("attempt %d must omit max_tokens when unconfigured, got %v", i+1, r.Raw["max_tokens"])
				}
			}
		})
	}
}

func TestOpenAIAdapterDynamicBudgetNeverSilentlyTruncatesAndHealsToExactRemaining(t *testing.T) {
	srv, reqs := newBudgetCaptureServer(t, func(attempt int, _ capturedChatRequest, w http.ResponseWriter) {
		if attempt == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"This model's maximum context length is 8192 tokens. However, you requested 8500 tokens (6000 in the messages, 2500 in the completion)."}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{
					"content": `{"schema_version":1,"review_run_id":"run-1","outcome":"complete","summary":"ok","findings":[],"tool_requests":[]}`,
				},
			}},
		})
	})
	ep := budgetEndpoint(srv.URL, map[string]any{
		"max_tokens":     float64(4096),
		"context_window": float64(8192),
	})
	adapter, err := NewOpenAIAdapter(ep, &HTTPClient{Inner: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	// ~6000 tokens of SQL review input: lower bound reduces max_tokens below 4096 while keeping >= 2192,
	// and exact server feedback heals to 8192 - 6000 - 16 = 2176 on retry.
	longSQL := strings.Repeat("SELECT col_alpha, col_beta FROM orders_ledger WHERE status = 1; ", 430)
	out, err := adapter.ReviewStructured(context.Background(), Request{
		RequestSchemaVersion: 1,
		ReviewRunID:          "run-1",
		EngineProfile:        "mysql/8.0",
		AnonymizedStatementGroups: []AnonymizedGroup{{
			GroupID:                     "11111111-1111-4111-8111-111111111111",
			RepresentativeShape:         "SELECT",
			RepresentativeAnonymizedSQL: longSQL,
			StatementCount:              1,
		}},
	})
	if err != nil {
		t.Fatalf("ReviewStructured failed: %v", err)
	}
	if out.Response == nil || out.Response.Summary != "ok" {
		t.Fatalf("unexpected outcome: %+v", out)
	}
	if len(*reqs) != 2 {
		t.Fatalf("expected 2 requests (initial + 1 self-heal), got %d", len(*reqs))
	}
	mt1, ok := (*reqs)[0].Raw["max_tokens"].(float64)
	if !ok || mt1 < 2192 || mt1 >= 4096 {
		t.Fatalf("expected initial lower-bound max_tokens in [2192, 4096), got %v", (*reqs)[0].Raw["max_tokens"])
	}
	if (*reqs)[1].Raw["max_tokens"] != float64(2176) {
		t.Fatalf("expected self-healed retry max_tokens=2176, got %v", (*reqs)[1].Raw["max_tokens"])
	}
}

func TestOpenAIAdapterSelfHealsContextOverflowOnceWithoutJSONObjectDowngrade(t *testing.T) {
	srv, reqs := newBudgetCaptureServer(t, func(attempt int, req capturedChatRequest, w http.ResponseWriter) {
		if attempt == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"This model's maximum context length is 8192 tokens. However, you requested 9408 tokens (5312 in the messages, 4096 in the completion). Please reduce the length of the messages or completion."}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{
					"content": `{"schema_version":1,"review_run_id":"run-1","outcome":"complete","summary":"healed","findings":[],"tool_requests":[]}`,
				},
			}},
		})
	})
	ep := budgetEndpoint(srv.URL, map[string]any{"max_tokens": float64(4096)})
	adapter, err := NewOpenAIAdapter(ep, &HTTPClient{Inner: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	out, err := adapter.ReviewStructured(context.Background(), Request{ReviewRunID: "run-1"})
	if err != nil {
		t.Fatalf("expected self-heal retry to succeed, got %v", err)
	}
	if out.Response == nil || out.Response.Summary != "healed" {
		t.Fatalf("unexpected outcome: %+v", out)
	}
	if len(*reqs) != 2 {
		t.Fatalf("expected exactly 2 calls (initial + 1 retry), got %d", len(*reqs))
	}
	if (*reqs)[0].Raw["max_tokens"] != float64(4096) {
		t.Fatalf("expected first request max_tokens=4096, got %v", (*reqs)[0].Raw["max_tokens"])
	}
	if (*reqs)[1].Raw["max_tokens"] != float64(2864) {
		t.Fatalf("expected retry max_tokens=2864, got %v", (*reqs)[1].Raw["max_tokens"])
	}
	rf, _ := (*reqs)[1].Raw["response_format"].(map[string]any)
	if rf == nil || rf["type"] != OutputModeJSONSchema {
		t.Fatalf("expected response_format.type=%q preserved on retry, got %v", OutputModeJSONSchema, rf)
	}
}

func TestOpenAIAdapterUnparseableOverflowOmitsMaxTokensAndCapsTotalRetries(t *testing.T) {
	srv, reqs := newBudgetCaptureServer(t, func(attempt int, _ capturedChatRequest, w http.ResponseWriter) {
		switch attempt {
		case 1:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"context_length_exceeded: maximum context length exceeded"}}`))
		case 2:
			// Simulate truncated JSON to exercise schema repair after overflow retry;
			// total requests must stay bounded at <= 3 even if repair hits 400.
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": map[string]any{"content": `{"schema_version":1`}}},
			})
		default:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"context_length_exceeded"}}`))
		}
	})
	ep := budgetEndpoint(srv.URL, map[string]any{"max_tokens": float64(4096)})
	adapter, err := NewOpenAIAdapter(ep, &HTTPClient{Inner: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	_, err = adapter.ReviewStructured(context.Background(), Request{ReviewRunID: "run-1"})
	if err == nil {
		t.Fatal("expected error when repair also hits overflow")
	}
	if len(*reqs) != 3 {
		t.Fatalf("expected total requests capped at 3 (initial + 1 overflow retry + 1 repair), got %d", len(*reqs))
	}
	if _, exists := (*reqs)[1].Raw["max_tokens"]; exists {
		t.Fatalf("expected unparseable overflow retry to omit max_tokens, got %v", (*reqs)[1].Raw["max_tokens"])
	}
	if _, exists := (*reqs)[2].Raw["max_tokens"]; exists {
		t.Fatalf("expected schema repair after fallback_no_max_tokens heal to keep max_tokens omitted, got %v", (*reqs)[2].Raw["max_tokens"])
	}
}

func TestOpenAIAdapterPromptExceedingContextFailsFastWithoutRetry(t *testing.T) {
	srv, reqs := newBudgetCaptureServer(t, func(_ int, _ capturedChatRequest, w http.ResponseWriter) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"This model's maximum context length is 8192 tokens. However, you requested 12396 tokens (8300 in the messages, 4096 in the completion)."}}`))
	})
	ep := budgetEndpoint(srv.URL, map[string]any{"max_tokens": float64(4096)})
	adapter, err := NewOpenAIAdapter(ep, &HTTPClient{Inner: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	_, err = adapter.ReviewStructured(context.Background(), Request{ReviewRunID: "run-1"})
	if err == nil {
		t.Fatal("expected prompt overflow to fail")
	}
	if len(*reqs) != 1 {
		t.Fatalf("expected fast fail with 1 request (no retry, no json_object fallback), got %d", len(*reqs))
	}
}

func TestOpenAIAdapterStillFallsBackOnJSONSchemaUnsupported(t *testing.T) {
	srv, reqs := newBudgetCaptureServer(t, func(attempt int, _ capturedChatRequest, w http.ResponseWriter) {
		if attempt == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"response_format type json_schema is not supported"}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{
					"content": `{"schema_version":1,"review_run_id":"run-1","outcome":"complete","summary":"fallback-ok","findings":[],"tool_requests":[]}`,
				},
			}},
		})
	})
	adapter, err := NewOpenAIAdapter(budgetEndpoint(srv.URL, nil), &HTTPClient{Inner: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	out, err := adapter.ReviewStructured(context.Background(), Request{ReviewRunID: "run-1"})
	if err != nil {
		t.Fatalf("expected json_object fallback to succeed, got %v", err)
	}
	if out.Response == nil || out.Response.Summary != "fallback-ok" {
		t.Fatalf("unexpected outcome: %+v", out)
	}
	if len(*reqs) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(*reqs))
	}
	rf1, _ := (*reqs)[0].Raw["response_format"].(map[string]any)
	rf2, _ := (*reqs)[1].Raw["response_format"].(map[string]any)
	if rf1["type"] != OutputModeJSONSchema || rf2["type"] != OutputModeJSONObject {
		t.Fatalf("expected json_schema -> json_object fallback, got %v -> %v", rf1, rf2)
	}
}

func TestOpenAIAdapterNewVLLMFormatHealTelemetryAndInfoLog(t *testing.T) {
	core, logs := observer.New(zapcore.DebugLevel)
	prev := zap.ReplaceGlobals(zap.New(core))
	defer prev()

	healedBefore := ChatCompletionOverflowHealTotal(OverflowHealOutcomeHealed)
	fastFailBefore := ChatCompletionOverflowHealTotal(OverflowHealOutcomeFastFail)
	fallbackBefore := ChatCompletionOverflowHealTotal(OverflowHealOutcomeFallbackNoMaxTokens)

	// 1. Newer vLLM 400 format heals precisely to 2176 and logs strictly at Info level.
	srvHeal, reqsHeal := newBudgetCaptureServer(t, func(attempt int, _ capturedChatRequest, w http.ResponseWriter) {
		if attempt == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"'max_tokens' or 'max_completion_tokens' is too large: 3092. This model's maximum context length is 8192 tokens and your request has 6000 input tokens (3092 > 8192 - 6000)"}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{
					"content": `{"schema_version":1,"review_run_id":"run-1","outcome":"complete","summary":"new-vllm-healed","findings":[],"tool_requests":[]}`,
				},
			}},
		})
	})
	adapter, err := NewOpenAIAdapter(budgetEndpoint(srvHeal.URL, map[string]any{"max_tokens": float64(4096)}), &HTTPClient{Inner: srvHeal.Client()})
	if err != nil {
		t.Fatal(err)
	}
	out, err := adapter.ReviewStructured(context.Background(), Request{ReviewRunID: "run-1"})
	if err != nil || out.Response == nil || out.Response.Summary != "new-vllm-healed" {
		t.Fatalf("expected new vLLM format heal to succeed, got out=%+v err=%v", out, err)
	}
	if len(*reqsHeal) != 2 || (*reqsHeal)[1].Raw["max_tokens"] != float64(2176) {
		t.Fatalf("expected exact retry max_tokens=2176, got %+v", *reqsHeal)
	}
	if ChatCompletionOverflowHealTotal(OverflowHealOutcomeHealed) != healedBefore+1 {
		t.Fatal("expected healed counter incremented by 1")
	}
	healEntries := logs.FilterMessage("chat_completion_overflow_healed").All()
	if len(healEntries) != 1 || healEntries[0].Level != zapcore.InfoLevel {
		t.Fatalf("expected 1 Info-level heal log, got %+v", healEntries)
	}
	if logs.FilterLevelExact(zapcore.ErrorLevel).Len() != 0 || logs.FilterLevelExact(zapcore.WarnLevel).Len() != 0 {
		t.Fatalf("successful self-heal must not emit Warn/Error logs, got %+v", logs.All())
	}

	// 2. Fast-fail increments fast_fail outcome counter.
	srvFastFail, _ := newBudgetCaptureServer(t, func(_ int, _ capturedChatRequest, w http.ResponseWriter) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"This model's maximum context length is 8192 tokens. However, you requested 12396 tokens (8300 in the messages, 4096 in the completion)."}}`))
	})
	adapterFF, _ := NewOpenAIAdapter(budgetEndpoint(srvFastFail.URL, map[string]any{"max_tokens": float64(4096)}), &HTTPClient{Inner: srvFastFail.Client()})
	_, _ = adapterFF.ReviewStructured(context.Background(), Request{ReviewRunID: "run-1"})
	if ChatCompletionOverflowHealTotal(OverflowHealOutcomeFastFail) != fastFailBefore+1 {
		t.Fatal("expected fast_fail counter incremented by 1")
	}

	// 3. Unparseable overflow increments fallback_no_max_tokens outcome counter.
	srvFallback, _ := newBudgetCaptureServer(t, func(attempt int, _ capturedChatRequest, w http.ResponseWriter) {
		if attempt == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"context_length_exceeded"}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{
					"content": `{"schema_version":1,"review_run_id":"run-1","outcome":"complete","summary":"fallback-ok","findings":[],"tool_requests":[]}`,
				},
			}},
		})
	})
	adapterFB, _ := NewOpenAIAdapter(budgetEndpoint(srvFallback.URL, map[string]any{"max_tokens": float64(4096)}), &HTTPClient{Inner: srvFallback.Client()})
	if _, err := adapterFB.ReviewStructured(context.Background(), Request{ReviewRunID: "run-1"}); err != nil {
		t.Fatalf("expected fallback_no_max_tokens retry to succeed, got %v", err)
	}
	if ChatCompletionOverflowHealTotal(OverflowHealOutcomeFallbackNoMaxTokens) != fallbackBefore+1 {
		t.Fatal("expected fallback_no_max_tokens counter incremented by 1")
	}

	// 4. Failed fallback_no_max_tokens retry must count as fast_fail and must not emit healed Info log.
	healedLogsBefore := logs.FilterMessage("chat_completion_overflow_healed").Len()
	fastFailBeforeFailedFB := ChatCompletionOverflowHealTotal(OverflowHealOutcomeFastFail)
	fallbackBeforeFailedFB := ChatCompletionOverflowHealTotal(OverflowHealOutcomeFallbackNoMaxTokens)
	srvFallbackFail, _ := newBudgetCaptureServer(t, func(_ int, _ capturedChatRequest, w http.ResponseWriter) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"context_length_exceeded"}}`))
	})
	adapterFBFail, _ := NewOpenAIAdapter(budgetEndpoint(srvFallbackFail.URL, map[string]any{"max_tokens": float64(4096)}), &HTTPClient{Inner: srvFallbackFail.Client()})
	if _, err := adapterFBFail.ReviewStructured(context.Background(), Request{ReviewRunID: "run-1"}); err == nil {
		t.Fatal("expected failed fallback_no_max_tokens retry to return error")
	}
	if ChatCompletionOverflowHealTotal(OverflowHealOutcomeFastFail) != fastFailBeforeFailedFB+1 {
		t.Fatal("expected failed fallback_no_max_tokens retry to increment fast_fail")
	}
	if ChatCompletionOverflowHealTotal(OverflowHealOutcomeFallbackNoMaxTokens) != fallbackBeforeFailedFB {
		t.Fatal("failed fallback_no_max_tokens retry must not increment fallback_no_max_tokens")
	}
	if logs.FilterMessage("chat_completion_overflow_healed").Len() != healedLogsBefore {
		t.Fatal("failed fallback_no_max_tokens retry must not emit chat_completion_overflow_healed log")
	}
}

func TestOpenAIAdapterTotalHTTPRequestsHardCapAcross5xxOverflowAndRepair(t *testing.T) {
	// Combined 5xx (1) + overflow 400 (1) + truncated EOF (1) + schema repair (1) = 4 requests total.
	srv, reqs := newBudgetCaptureServer(t, func(attempt int, _ capturedChatRequest, w http.ResponseWriter) {
		switch attempt {
		case 1:
			w.WriteHeader(http.StatusBadGateway)
		case 2:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"This model's maximum context length is 8192 tokens. However, you requested 9408 tokens (5312 in the messages, 4096 in the completion)."}}`))
		case 3:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": map[string]any{"content": `{"schema_version":1`}}},
			})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{
					"message": map[string]any{
						"content": `{"schema_version":1,"review_run_id":"run-1","outcome":"complete","summary":"capped-ok","findings":[],"tool_requests":[]}`,
					},
				}},
			})
		}
	})
	adapter, err := NewOpenAIAdapter(budgetEndpoint(srv.URL, map[string]any{"max_tokens": float64(4096)}), &HTTPClient{Inner: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	out, err := adapter.ReviewStructured(context.Background(), Request{ReviewRunID: "run-1"})
	if err != nil || out.Response == nil || out.Response.Summary != "capped-ok" {
		t.Fatalf("expected combined flow to succeed within cap, got out=%+v err=%v", out, err)
	}
	if len(*reqs) != maxHTTPRequestsPerStructuredCall {
		t.Fatalf("expected exactly %d requests, got %d", maxHTTPRequestsPerStructuredCall, len(*reqs))
	}

	// Exhausting 5xx + overflow + 5xx must never exceed maxHTTPRequestsPerStructuredCall (4).
	srvCap, reqsCap := newBudgetCaptureServer(t, func(attempt int, _ capturedChatRequest, w http.ResponseWriter) {
		if attempt == 3 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"This model's maximum context length is 8192 tokens. However, you requested 9408 tokens (5312 in the messages, 4096 in the completion)."}}`))
			return
		}
		w.WriteHeader(http.StatusBadGateway)
	})
	adapterCap, _ := NewOpenAIAdapter(budgetEndpoint(srvCap.URL, map[string]any{"max_tokens": float64(4096)}), &HTTPClient{Inner: srvCap.Client()})
	if _, err := adapterCap.ReviewStructured(context.Background(), Request{ReviewRunID: "run-1"}); err == nil {
		t.Fatal("expected error after exhausting cap")
	}
	if len(*reqsCap) > maxHTTPRequestsPerStructuredCall {
		t.Fatalf("total requests %d exceeded hard cap %d", len(*reqsCap), maxHTTPRequestsPerStructuredCall)
	}
}

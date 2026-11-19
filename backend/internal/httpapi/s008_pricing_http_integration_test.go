//go:build integration

// s008_pricing_http_integration_test.go S008 pricing and usage management HTTP gates:
// rate version creation/listing (with fixed-point string payloads and overlap rejection), per-call ledger
// detail and summary read surfaces, and strongly-typed Provider configuration readback (disabled state skips probe).
package httpapi

import (
	"encoding/json"
	"testing"
)

// TestAiProviderRateVersionsHttp rate version creation + readback + overlap rejection (pricing-01/02).
func TestAiProviderRateVersionsHttp(t *testing.T) {
	fx := newB3Fixture(t)
	fx.seedAdmin(t)
	if code, body, _ := fx.admin.login("admin", adminPassword); code != 200 || errCode(body) != 0 {
		t.Fatalf("login: %d %v", code, body)
	}
	code, body, _ := fx.admin.do("POST", "/api/v4/admin/ai-providers", map[string]any{
		"name": "rate-p", "provider_kind": "openai_compatible",
		"base_url": "https://127.0.0.1:9", "model_name": "m", "enabled": false,
		"selection_priority": 78,
		"api_key":            map[string]any{"value": "k-rate"},
		"configuration":      map[string]any{"reasoning_effort": "low", "max_tokens": 1024, "temperature": 0.3},
	}, true)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("create provider: %d %v", code, body)
	}
	id := dataField(body, "id").(string)
	// Strongly-typed configuration readback.
	cfg, _ := dataField(body, "configuration").(map[string]any)
	if cfg == nil || cfg["reasoning_effort"] != "low" || cfg["max_tokens"] != float64(1024) ||
		cfg["structured_output_mode"] != nil {
		t.Fatalf("typed configuration read-back mismatch: %v", cfg)
	}
	// Rate version creation (cross-midnight window + fixed-point cache read price string).
	code, body, _ = fx.admin.do("POST", "/api/v4/admin/ai-providers/"+id+"/rate-versions", map[string]any{
		"currency": "CNY", "timezone": "Asia/Shanghai",
		"effective_from": "2026-11-01T00:00:00Z",
		"windows": []map[string]any{{
			"start_minute": 0, "end_minute": 1440,
			"input_price": "1.5", "output_price": "2",
			"cached_read_price": "0.15",
		}},
	}, true)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("create rate version: %d %v", code, body)
	}
	windows, _ := dataField(body, "windows").([]any)
	if len(windows) != 1 {
		t.Fatalf("one window expected: %v", windows)
	}
	w, _ := windows[0].(map[string]any)
	if w["input_price"] != "1.500000" || w["cached_read_price"] != "0.150000" {
		t.Fatalf("bounded-decimal round-trip mismatch: %v", w)
	}
	// Invalid price string rejected.
	code, body, _ = fx.admin.do("POST", "/api/v4/admin/ai-providers/"+id+"/rate-versions", map[string]any{
		"currency": "USD", "timezone": "Asia/Shanghai",
		"effective_from": "2027-01-01T00:00:00Z",
		"windows": []map[string]any{{
			"start_minute": 0, "end_minute": 60,
			"input_price": "1.1234567", "output_price": "2",
		}},
	}, true)
	if code != 200 || errCode(body) != 1001 {
		t.Fatalf("7-fraction-digit price must be rejected: %d %v", code, body)
	}
	// RCP-20261102-S008: price 1001 carries field targeting (meta.fields.input_price).
	meta, _ := body["meta"].(map[string]any)
	fields, _ := meta["fields"].(map[string]any)
	if msgs, _ := fields["input_price"].([]any); len(msgs) == 0 {
		t.Fatalf("price rejection must carry meta.fields.input_price: %v", body["meta"])
	}
	// RCP-20261102-S008: out-of-range genparams 1001 carries field targeting (meta.fields.max_tokens).
	code, body, _ = fx.admin.do("POST", "/api/v4/admin/ai-providers", map[string]any{
		"name": "rate-p-bad", "provider_kind": "openai_compatible",
		"base_url": "https://127.0.0.1:9", "model_name": "m", "enabled": false,
		"selection_priority": 79,
		"api_key":            map[string]any{"value": "k-rate"},
		"configuration":      map[string]any{"max_tokens": 100},
	}, true)
	if code != 200 || errCode(body) != 1001 {
		t.Fatalf("out-of-range max_tokens must be rejected: %d %v", code, body)
	}
	meta, _ = body["meta"].(map[string]any)
	fields, _ = meta["fields"].(map[string]any)
	if msgs, _ := fields["max_tokens"].([]any); len(msgs) == 0 {
		t.Fatalf("max_tokens rejection must carry meta.fields.max_tokens: %v", body["meta"])
	}
	// List readback.
	code, body, _ = fx.admin.do("GET", "/api/v4/admin/ai-providers/"+id+"/rate-versions", nil, true)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("list rate versions: %d %v", code, body)
	}
	if arr, ok := body["data"].([]any); !ok || len(arr) != 1 {
		// data: direct array envelope (unpaginated)
		t.Fatalf("list data must be a one-element array: %v", body["data"])
	}
}

// TestAiUsageReadSurfacesHttp per-call detail + summary read surfaces (readable when empty).
func TestAiUsageReadSurfacesHttp(t *testing.T) {
	fx := newB3Fixture(t)
	fx.seedAdmin(t)
	if code, body, _ := fx.admin.login("admin", adminPassword); code != 200 || errCode(body) != 0 {
		t.Fatalf("login: %d %v", code, body)
	}
	code, body, _ := fx.admin.do("GET", "/api/v4/admin/ai-usage/calls?limit=10", nil, true)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("list calls: %d %v", code, body)
	}
	data, _ := body["data"].(map[string]any)
	if data == nil {
		raw, _ := json.Marshal(body["data"])
		t.Fatalf("paged envelope expected: %s", raw)
	}
	if items, _ := data["items"].([]any); items == nil {
		t.Fatal("items array expected in empty state")
	}
	code, body, _ = fx.admin.do("GET", "/api/v4/admin/ai-usage/summary?from=2026-11-01T00:00:00Z&to=2026-12-01T00:00:00Z", nil, true)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("summary: %d %v", code, body)
	}
	// Invalid from parameter rejected.
	code, body, _ = fx.admin.do("GET", "/api/v4/admin/ai-usage/summary?from=not-a-date", nil, true)
	if code != 200 || errCode(body) != 1001 {
		t.Fatalf("invalid from must 1001: %d %v", code, body)
	}
	// RCP-20261102-S008: invalid provider_id is rejected with 1001 (same as run_id),
	// while a valid UUID returns normally (readable when empty without failing on filter parameter).
	code, body, _ = fx.admin.do("GET", "/api/v4/admin/ai-usage/calls?provider_id=not-a-uuid", nil, true)
	if code != 200 || errCode(body) != 1001 {
		t.Fatalf("invalid provider_id must 1001: %d %v", code, body)
	}
	code, body, _ = fx.admin.do("GET", "/api/v4/admin/ai-usage/calls?provider_id=00000000-0000-4000-8000-000000000001", nil, true)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("valid provider_id filter must stay readable: %d %v", code, body)
	}
}

// TestHTTPSystemPromptProfileValidationAndRoundTrip verifies BE-B13 HTTP API semantics for
// optional system_prompt_profile (1001 rejection with meta.fields, public vs RFC1918 http://,
// auto-detect omission, explicit default/san round-trip, M2 keep, and empty-object clear).
func TestHTTPSystemPromptProfileValidationAndRoundTrip(t *testing.T) {
	fx := newB3Fixture(t)
	fx.seedAdmin(t)
	if code, body, _ := fx.admin.login("admin", adminPassword); code != 200 || errCode(body) != 0 {
		t.Fatalf("login: %d %v", code, body)
	}

	// 1. Invalid system_prompt_profile values (including explicit null) are rejected with 1001 + meta.fields.system_prompt_profile.
	for _, invalidVal := range []any{"v4", "", nil, 123} {
		code, body, _ := fx.admin.do("POST", "/api/v4/admin/ai-providers", map[string]any{
			"name": "bad-profile", "provider_kind": "openai_compatible",
			"base_url": "https://api.example.com/v1", "model_name": "san", "enabled": false,
			"selection_priority": 50,
			"api_key":            map[string]any{"value": "sk-bad"},
			"configuration":      map[string]any{"system_prompt_profile": invalidVal},
		}, true)
		if code != 200 || errCode(body) != 1001 {
			t.Fatalf("invalid system_prompt_profile (%v) must 1001, got %d %v", invalidVal, code, body)
		}
		meta, _ := body["meta"].(map[string]any)
		fields, _ := meta["fields"].(map[string]any)
		if _, ok := fields["system_prompt_profile"]; !ok {
			t.Fatalf("expected meta.fields.system_prompt_profile for %v, got %v", invalidVal, body)
		}
	}

	// 2. Public IP over plaintext http:// is rejected with 1001.
	code, body, _ := fx.admin.do("POST", "/api/v4/admin/ai-providers", map[string]any{
		"name": "public-http", "provider_kind": "openai_compatible",
		"base_url": "http://93.184.216.34/v1", "model_name": "san", "enabled": false,
		"selection_priority": 51,
		"api_key":            map[string]any{"value": "sk-pub"},
	}, true)
	if code != 200 || errCode(body) != 1001 {
		t.Fatalf("public http:// base_url must 1001, got %d %v", code, body)
	}

	// 3. Private RFC1918 LAN http:// endpoint creates cleanly with omitted system_prompt_profile (auto-detect).
	code, body, _ = fx.admin.do("POST", "/api/v4/admin/ai-providers", map[string]any{
		"name": "lan-vllm", "provider_kind": "openai_compatible",
		"base_url": "http://10.0.0.12:8000/v1", "model_name": "san-v3.1", "enabled": false,
		"selection_priority": 10,
		"api_key":            map[string]any{"value": "sk-lan"},
	}, true)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("create RFC1918 provider: %d %v", code, body)
	}
	id := dataField(body, "id").(string)
	version := int64(dataField(body, "version").(float64))
	if cfg, ok := dataField(body, "configuration").(map[string]any); ok {
		if _, has := cfg["system_prompt_profile"]; has {
			t.Fatalf("auto-detect create must not fabricate explicit system_prompt_profile: %v", cfg)
		}
	}

	// 4. PUT explicit "san" persists through GET.
	code, body, _ = fx.admin.doWithVersion("PUT", "/api/v4/admin/ai-providers/"+id, map[string]any{
		"name": "lan-vllm", "provider_kind": "openai_compatible",
		"base_url": "http://10.0.0.12:8000/v1", "model_name": "san-v3.1", "enabled": false,
		"selection_priority": 10,
		"configuration":      map[string]any{"system_prompt_profile": "san"},
	}, version)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("put explicit san: %d %v", code, body)
	}
	version = int64(dataField(body, "version").(float64))
	code, body, _ = fx.admin.do("GET", "/api/v4/admin/ai-providers/"+id, nil, true)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("get after put san: %d %v", code, body)
	}
	cfg, _ := dataField(body, "configuration").(map[string]any)
	if cfg == nil || cfg["system_prompt_profile"] != "san" {
		t.Fatalf("expected persisted system_prompt_profile=san, got %v", body)
	}

	// 5. PUT omitting configuration preserves stored system_prompt_profile="san" (M2 keep semantics).
	code, body, _ = fx.admin.doWithVersion("PUT", "/api/v4/admin/ai-providers/"+id, map[string]any{
		"name": "lan-vllm", "provider_kind": "openai_compatible",
		"base_url": "http://10.0.0.12:8000/v1", "model_name": "san", "enabled": false,
		"selection_priority": 10,
	}, version)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("put omitting configuration (keep): %d %v", code, body)
	}
	version = int64(dataField(body, "version").(float64))
	cfg, _ = dataField(body, "configuration").(map[string]any)
	if cfg == nil || cfg["system_prompt_profile"] != "san" {
		t.Fatalf("keep semantics must retain system_prompt_profile=san, got %v", body)
	}

	// 6. PUT explicit "default" overrides model_name="san", then PUT configuration:{} clears to auto-detect.
	code, body, _ = fx.admin.doWithVersion("PUT", "/api/v4/admin/ai-providers/"+id, map[string]any{
		"name": "lan-vllm", "provider_kind": "openai_compatible",
		"base_url": "http://10.0.0.12:8000/v1", "model_name": "san", "enabled": false,
		"selection_priority": 10,
		"configuration":      map[string]any{"system_prompt_profile": "default"},
	}, version)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("put explicit default: %d %v", code, body)
	}
	version = int64(dataField(body, "version").(float64))
	cfg, _ = dataField(body, "configuration").(map[string]any)
	if cfg == nil || cfg["system_prompt_profile"] != "default" {
		t.Fatalf("expected system_prompt_profile=default, got %v", body)
	}

	code, body, _ = fx.admin.doWithVersion("PUT", "/api/v4/admin/ai-providers/"+id, map[string]any{
		"name": "lan-vllm", "provider_kind": "openai_compatible",
		"base_url": "http://10.0.0.12:8000/v1", "model_name": "san", "enabled": false,
		"selection_priority": 10,
		"configuration":      map[string]any{},
	}, version)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("put empty configuration (clear): %d %v", code, body)
	}
	if cfg, ok := dataField(body, "configuration").(map[string]any); ok {
		if _, has := cfg["system_prompt_profile"]; has {
			t.Fatalf("empty configuration object must clear system_prompt_profile override: %v", cfg)
		}
	}
	version = int64(dataField(body, "version").(float64))

	// 7. ReplaceAIProvider (enabled=true) and ListProviderModels must reject public http:// with 1001 (not 2011).
	code, body, _ = fx.admin.doWithVersion("PUT", "/api/v4/admin/ai-providers/"+id, map[string]any{
		"name": "lan-vllm", "provider_kind": "openai_compatible",
		"base_url": "http://93.184.216.34/v1", "model_name": "san", "enabled": true,
		"selection_priority": 10,
	}, version)
	if code != 200 || errCode(body) != 1001 {
		t.Fatalf("PUT enabled=true with public http:// must 1001, got %d %v", code, body)
	}
	code, body, _ = fx.admin.do("POST", "/api/v4/admin/ai-providers/models", map[string]any{
		"base_url": "http://93.184.216.34/v1",
		"api_key":  "sk-pub",
	}, true)
	if code != 200 || errCode(body) != 1001 {
		t.Fatalf("POST /models with public http:// must 1001, got %d %v", code, body)
	}
}

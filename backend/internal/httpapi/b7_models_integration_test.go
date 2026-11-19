//go:build integration

// b7_models_integration_test.go S18.76-B: model catalog endpoint HTTP surface --
// payload validation (1001), provider_id not found (1002), upstream rejection reason surfaced
// (2011 + meta.detail, same channel as S18.75-A). Upstream success path is covered in provider
// package unit tests (httptest direct connection with injected modelsHTTPClient).
package httpapi

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestAiProviderModelCatalogEndpoint(t *testing.T) {
	fx := newB3Fixture(t)
	fx.seedAdmin(t)
	if code, body, _ := fx.admin.login("admin", adminPassword); code != 200 || errCode(body) != 0 {
		t.Fatalf("login: %d %v", code, body)
	}

	// A: missing base_url -> 1001.
	code, body, _ := fx.admin.do("POST", "/api/v4/admin/ai-providers/models", map[string]any{}, true)
	if code != 200 || errCode(body) != 1001 {
		t.Fatalf("missing base_url must map 1001, got %d %v", code, body)
	}

	// B: both api_key and provider_id missing -> 1001.
	code, body, _ = fx.admin.do("POST", "/api/v4/admin/ai-providers/models",
		map[string]any{"base_url": "https://u.example.com"}, true)
	if code != 200 || errCode(body) != 1001 {
		t.Fatalf("missing credential source must map 1001, got %d %v", code, body)
	}

	// C: provider_id not found -> 1002.
	code, body, _ = fx.admin.do("POST", "/api/v4/admin/ai-providers/models",
		map[string]any{"base_url": "https://u.example.com", "provider_id": uuid.NewString()}, true)
	if code != 200 || errCode(body) != 1002 {
		t.Fatalf("unknown provider_id must map 1002, got %d %v", code, body)
	}

	// D: upstream unreachable -> 2011 and meta.detail carries rejection reason (envelope semantics:
	// business errors ride HTTP 200).
	code, body, _ = fx.admin.do("POST", "/api/v4/admin/ai-providers/models",
		map[string]any{"base_url": "https://127.0.0.1:9", "api_key": "k-models"}, true)
	if code != 200 || errCode(body) != 2011 {
		t.Fatalf("unreachable upstream must map 2011, got %d %v", code, body)
	}
	meta, _ := body["meta"].(map[string]any)
	if s, _ := meta["detail"].(string); s == "" {
		t.Fatalf("2011 must carry upstream detail meta, got %v", body)
	}
}

// TestAiProviderConnectionTestSurfacesDetail S18.75-A fix (owner live test:
// inline connection test showed only generic message): targeted health probe failure on a disabled
// Provider (creation does not trigger probe) must carry upstream rejection reason in meta.detail.
func TestAiProviderConnectionTestSurfacesDetail(t *testing.T) {
	fx := newB3Fixture(t)
	fx.seedAdmin(t)
	if code, body, _ := fx.admin.login("admin", adminPassword); code != 200 || errCode(body) != 0 {
		t.Fatalf("login: %d %v", code, body)
	}
	code, body, _ := fx.admin.do("POST", "/api/v4/admin/ai-providers", map[string]any{
		"name": "conn-detail", "provider_kind": "openai_compatible",
		"base_url": "https://127.0.0.1:9", "model_name": "m", "enabled": false,
		"selection_priority": 77,
		"api_key":            map[string]any{"value": "k-conn"},
	}, true)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("create disabled provider: %d %v", code, body)
	}
	id := dataField(body, "id").(string)
	code, body, _ = fx.admin.do("POST", "/api/v4/admin/ai-providers/"+id+"/connection-tests", map[string]any{}, true)
	if code != 200 || errCode(body) != 2011 {
		t.Fatalf("connection test must map 2011, got %d %v", code, body)
	}
	meta, _ := body["meta"].(map[string]any)
	if s, _ := meta["detail"].(string); s == "" {
		t.Fatalf("connection-test failure must carry meta.detail, got %v", body)
	}
}

// TestDatasourceConnectionTestSurfacesDetail S18.76 fix (owner decision following
// Provider rejection reason surfacing path): datasource connection test failure must carry root cause
// in meta.detail (previously 3010 had no Detail and profile was undeclared, leaving frontend with generic fallback).
func TestDatasourceConnectionTestSurfacesDetail(t *testing.T) {
	fx := newB3Fixture(t)
	fx.seedAdmin(t)
	if code, body, _ := fx.admin.login("admin", adminPassword); code != 200 || errCode(body) != 0 {
		t.Fatalf("login: %d %v", code, body)
	}
	code, body, _ := fx.admin.do("POST", "/api/v4/admin/datasources", map[string]any{
		"name": "conn-detail-ds", "engine": "postgresql",
		"compatibility_mode": "postgresql", "deployment_kind": "native",
		"host": "127.0.0.1", "port": 9, "database_name": "target",
		"version_constraint": "17.x", "enabled": false,
		"credentials": []map[string]any{{
			"purpose": "review", "username": "u",
			"password": map[string]any{"value": "p"},
		}},
	}, true)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("create disabled datasource: %d %v", code, body)
	}
	id := dataField(body, "id").(string)
	code, body, _ = fx.admin.do("POST", "/api/v4/admin/datasources/"+id+"/connection-tests",
		map[string]any{"purpose": "review"}, true)
	if code != 200 || errCode(body) != 3010 {
		t.Fatalf("unreachable datasource must map 3010, got %d %v", code, body)
	}
	meta, _ := body["meta"].(map[string]any)
	detail, _ := meta["detail"].(string)
	if detail == "" {
		t.Fatalf("3010 must carry meta.detail, got %v", body)
	}
	// Root cause must be connection-level information (unreachable/refused), not a generic masked fallback.
	if !strings.Contains(detail, "127.0.0.1:9") {
		t.Fatalf("detail must carry the dial target, got %q", detail)
	}
}

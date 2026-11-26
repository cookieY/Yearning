package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/cookieY/YearningX/backend/internal/identity/session"
)

// Relative path to repository root: go test working directory is the package directory backend/internal/httpapi/.
const (
	repoOpenAPIPath   = "../../../api/openapi/yearning-v4.yaml"
	repoScenariosPath = "../../../testdata/development/mock-scenarios.json"
)

func newMockRouter(t *testing.T) *gin.Engine {
	t.Helper()
	engine, err := NewMockEngine(repoOpenAPIPath, repoScenariosPath, zap.NewNop())
	if err != nil {
		t.Fatalf("mock engine: %v", err)
	}
	return NewMockRouter(engine, zap.NewNop(), NewReadiness(zap.NewNop()))
}

func setScenario(t *testing.T, r *gin.Engine, id string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v4/_mock/scenario", strings.NewReader(`{"scenario_id":"`+id+`"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w
}

func TestMockEngineLoadsRealContracts(t *testing.T) {
	if _, err := os.Stat(repoOpenAPIPath); err != nil {
		t.Fatalf("openapi not reachable from test cwd: %v", err)
	}
	engine, err := NewMockEngine(repoOpenAPIPath, repoScenariosPath, zap.NewNop())
	if err != nil {
		t.Fatalf("must load real openapi+scenarios: %v", err)
	}
	if len(engine.order) < 6 {
		t.Fatalf("expected >=6 scenarios, got %d", len(engine.order))
	}
}

func TestMockScenarioSwitching(t *testing.T) {
	r := newMockRouter(t)

	// Default scenario is readable
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v4/_mock/scenario", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"scenario_id"`) {
		t.Fatalf("get scenario failed: %d %s", w.Code, w.Body.String())
	}

	// Scenario switch succeeds
	w = setScenario(t, r, "business_blocked")
	if w.Code != http.StatusOK {
		t.Fatalf("switch failed: %d %s", w.Code, w.Body.String())
	}

	// Unknown scenario -> business error (HTTP 200 + non-zero err_code)
	w = setScenario(t, r, "does_not_exist")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"err_code":1002`) {
		t.Fatalf("unknown scenario must be 1002 business error: %d %s", w.Code, w.Body.String())
	}

	// Malformed request body -> VALIDATION_FAILED
	w = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v4/_mock/scenario", bytes.NewReader([]byte("not json")))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if !strings.Contains(w.Body.String(), `"err_code":1001`) {
		t.Fatalf("invalid body must be 1001: %d %s", w.Code, w.Body.String())
	}
}

func TestMockRendersScenarioKinds(t *testing.T) {
	r := newMockRouter(t)
	// /auth/login is POST in OpenAPI
	post := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		return w
	}

	setScenario(t, r, "success_ready")
	w := post("/api/v4/auth/login")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"err_code":0`) {
		t.Fatalf("success scenario: %d %s", w.Code, w.Body.String())
	}

	setScenario(t, r, "business_blocked")
	w = post("/api/v4/auth/login")
	if w.Code != http.StatusOK {
		t.Fatalf("business error must stay 200: %d", w.Code)
	}
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body["err_code"].(float64) == 0 {
		t.Fatalf("business_blocked must carry nonzero err_code: %s", w.Body.String())
	}

	setScenario(t, r, "provider_unavailable")
	w = post("/api/v4/auth/login")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("transport scenario must use real 503: %d", w.Code)
	}
	if !strings.Contains(w.Header().Get("Content-Type"), "application/problem+json") {
		t.Fatalf("content type = %s", w.Header().Get("Content-Type"))
	}

	setScenario(t, r, "authorization_denied")
	w = post("/api/v4/auth/login")
	if w.Code != http.StatusForbidden {
		t.Fatalf("authorization denial must be 403: %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "request_id") {
		t.Fatal("problem must carry request_id")
	}
}

func TestMockUnknownRouteIsProblem404(t *testing.T) {
	r := newMockRouter(t)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v4/not/in/contract", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d", w.Code)
	}
	if !strings.Contains(w.Header().Get("Content-Type"), "application/problem+json") {
		t.Fatalf("content type = %s", w.Header().Get("Content-Type"))
	}
}

func TestMockKnownPathWrongMethodIs405(t *testing.T) {
	r := newMockRouter(t)
	// /auth/login defines only POST; GET must return 405 rather than a business response
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v4/auth/login", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestMockPathParameterRoutesMatch(t *testing.T) {
	table, err := LoadRouteTable(repoOpenAPIPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	// /admin/users/{user_id} defines only PATCH in OpenAPI
	kind, template := table.Match("/admin/users/0198d9cc-e65d-7b9d-a8aa-3c81945f99ac", "PATCH")
	if kind != Matched || template != "/admin/users/{user_id}" {
		t.Fatalf("template route must match: kind=%d template=%s", kind, template)
	}
	kind, _ = table.Match("/admin/users/not-a-valid-but-nonempty", "PATCH")
	if kind != Matched {
		t.Fatal("path with any non-empty segment should match template")
	}
	kind, _ = table.Match("/admin/users/0198d9cc-e65d-7b9d-a8aa-3c81945f99ac", "GET")
	if kind != MethodNotAllowed {
		t.Fatalf("undefined method on known path must be 405-class: %d", kind)
	}
	kind, _ = table.Match("/admin/users/", "PATCH")
	if kind != MatchUnknown {
		t.Fatalf("empty parameter segment must not match: %d", kind)
	}
}

func TestServeRouterWiresPlatformEndpoints(t *testing.T) {
	deps := ServeDeps{
		Logger: zap.NewNop(),
		Readiness: NewReadiness(zap.NewNop(),
			ReadinessCheck{Name: "metadata_postgresql", Check: func(ctx context.Context) error { return nil }},
		),
		SessionStore:  session.NewMemoryStore(),
		SessionOption: session.Options{CookieName: "yearning_session", AnonymousAllowed: true},
	}
	r := NewServeRouter(deps)

	for path, wantStatus := range map[string]int{
		"/api/v4/health/live":  200,
		"/api/v4/health/ready": 200,
		"/api/v4/version":      200,
		"/api/v4/no-such":      404,
	} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != wantStatus {
			t.Fatalf("%s = %d, want %d", path, w.Code, wantStatus)
		}
	}

	// Request ID header propagation
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v4/version", nil))
	if w.Header().Get(HeaderRequestID) == "" {
		t.Fatal("serve router must propagate X-Request-ID")
	}
}

package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/cookieY/YearningX/backend/internal/identity/session"
)

func readinessRouter(checks ...ReadinessCheck) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RequestIDMiddleware())
	readiness := NewReadiness(zap.NewNop(), checks...)
	r.GET("/api/v4/health/live", readiness.HandleLive)
	r.GET("/api/v4/health/ready", readiness.HandleReady)
	return r
}

func TestLivenessAlwaysOKWithoutDependencies(t *testing.T) {
	// Liveness reflects process survival only: must return 200 even when all dependency checks fail,
	// otherwise K8s would restart the process on dependency faults in violation of the readiness_failure contract.
	r := readinessRouter(ReadinessCheck{
		Name:  "metadata_postgresql",
		Check: func(ctx context.Context) error { return errors.New("down") },
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v4/health/live", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("liveness = %d, want 200", w.Code)
	}
	var body map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body["status"] != "live" {
		t.Fatalf("body = %v", body)
	}
}

func TestReadinessOKWhenAllChecksPass(t *testing.T) {
	r := readinessRouter(
		ReadinessCheck{Name: "metadata_postgresql", Check: func(ctx context.Context) error { return nil }},
		ReadinessCheck{Name: "key_provider", Check: func(ctx context.Context) error { return nil }},
	)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v4/health/ready", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("readiness = %d, want 200", w.Code)
	}
	var body map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body["status"] != "ready" {
		t.Fatalf("body = %v", body)
	}
}

// Acceptance Gate 4 (unit test part): Readiness must fail when PG or Key is missing.
func TestReadinessFailsWhenPostgreSQLMissing(t *testing.T) {
	r := readinessRouter(
		ReadinessCheck{Name: "metadata_postgresql", Check: func(ctx context.Context) error { return errors.New("unreachable") }},
		ReadinessCheck{Name: "key_provider", Check: func(ctx context.Context) error { return nil }},
	)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v4/health/ready", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing PG must fail readiness, got %d", w.Code)
	}
}

func TestReadinessFailsWhenKeyProviderMissing(t *testing.T) {
	r := readinessRouter(
		ReadinessCheck{Name: "metadata_postgresql", Check: func(ctx context.Context) error { return nil }},
		ReadinessCheck{Name: "key_provider", Check: func(ctx context.Context) error { return errors.New("unhealthy") }},
	)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v4/health/ready", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing key provider must fail readiness, got %d", w.Code)
	}
}

func TestVersionEndpoint(t *testing.T) {
	// SEC-0044: anonymous /version does not disclose version/commit/go_version fingerprints,
	// returning only service name and API version; authenticated sessions see the full summary.
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RequestIDMiddleware())
	store := session.NewMemoryStore()
	store.Put(&session.Session{ID: "sess-1", Identity: &session.Identity{UserID: "u1"}})
	r.Use(session.Middleware(store, session.Options{
		CookieName: "yearning_session", AnonymousAllowed: true,
	}))
	r.GET("/api/v4/version", HandleVersion)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v4/version", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var body struct {
		ErrCode int               `json:"err_code"`
		Data    map[string]string `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.ErrCode != 0 {
		t.Fatalf("err_code = %d", body.ErrCode)
	}
	if body.Data["service"] != "yearning" {
		t.Errorf("service = %s", body.Data["service"])
	}
	if body.Data["api_version"] != "v4" {
		t.Errorf("api_version = %s", body.Data["api_version"])
	}
	for _, leak := range []string{"version", "commit", "go_version"} {
		if v, ok := body.Data[leak]; ok && v != "" {
			t.Errorf("anonymous /version must not disclose %s, got %q", leak, v)
		}
	}

	authed := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v4/version", nil)
	req.AddCookie(&http.Cookie{Name: "yearning_session", Value: "sess-1"})
	r.ServeHTTP(authed, req)
	if authed.Code != http.StatusOK {
		t.Fatalf("authenticated status = %d", authed.Code)
	}
	var full struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal(authed.Body.Bytes(), &full); err != nil {
		t.Fatal(err)
	}
	if full.Data["version"] == "" || full.Data["commit"] == "" || full.Data["go_version"] == "" {
		t.Errorf("authenticated /version must include build summary, got %v", full.Data)
	}
}

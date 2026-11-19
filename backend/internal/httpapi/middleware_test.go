package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/cookieY/YearningX/backend/internal/config"
	"github.com/cookieY/YearningX/backend/internal/platform/logging"
)

func testLogger() (*zap.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	cfg := config.Defaults()
	return logging.NewWithSink(cfg.Logging, cfg.Observability, &buf), &buf
}

func TestRecoveryReturnsProblemWithoutInternals(t *testing.T) {
	logger, logs := testLogger()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RequestIDMiddleware(), RecoveryMiddleware(logger))
	r.GET("/panic", func(c *gin.Context) { panic("sensitive detail") })

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/panic", nil))

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
	if strings.Contains(w.Body.String(), "sensitive detail") {
		t.Fatal("panic value must not leak into the response")
	}
	if !strings.Contains(logs.String(), "unexpected_internal_error") {
		t.Fatal("panic must be logged as unexpected_internal_error")
	}
	if !strings.Contains(logs.String(), "stack") {
		t.Fatal("stack trace belongs in the log for unexpected internal errors")
	}
}

func TestAccessLogUsesRouteTemplate(t *testing.T) {
	logger, logs := testLogger()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RequestIDMiddleware(), AccessLogMiddleware(logger))
	r.GET("/api/v4/items/:id", func(c *gin.Context) { c.Status(http.StatusOK) })

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v4/items/0198d9cc-e65d-7b9d-a8aa-3c81945f99ac", nil)
	r.ServeHTTP(w, req)

	line := logs.String()
	if !strings.Contains(line, "\"operation\":\"/api/v4/items/:id\"") {
		t.Fatalf("operation must use route template to bound cardinality: %s", line)
	}
	if !strings.Contains(line, "\"duration_ms\"") {
		t.Fatalf("duration_ms missing: %s", line)
	}
	var event map[string]any
	if err := json.Unmarshal([]byte(strings.SplitN(line, "\n", 2)[0]), &event); err != nil {
		t.Fatalf("access log must be single JSON event: %v", err)
	}
	if event["outcome"] != "success" {
		t.Errorf("outcome = %v", event["outcome"])
	}
}

func TestAccessLogUnmatchedRoute(t *testing.T) {
	logger, logs := testLogger()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RequestIDMiddleware(), AccessLogMiddleware(logger))
	r.NoRoute(NotFoundHandler)
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/nope", nil))
	if !strings.Contains(logs.String(), "\"operation\":\"unmatched\"") {
		t.Fatalf("unmatched operation label missing: %s", logs.String())
	}
}

func TestNotFoundHandlerProblem(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RequestIDMiddleware())
	r.NoRoute(NotFoundHandler)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/unknown", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d", w.Code)
	}
	if !strings.Contains(w.Header().Get("Content-Type"), "application/problem+json") {
		t.Fatalf("content type = %s", w.Header().Get("Content-Type"))
	}
}

func TestMethodNotAllowedHandlerProblem(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.HandleMethodNotAllowed = true
	r.Use(RequestIDMiddleware())
	r.NoMethod(MethodNotAllowedHandler)
	r.POST("/only-post", func(c *gin.Context) {})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/only-post", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestBusinessErrorNotRewrittenByTransport(t *testing.T) {
	// H001 regression: business errors remain HTTP 200 after passing through Recovery / access log transport middleware.
	logger, _ := testLogger()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RequestIDMiddleware(), RecoveryMiddleware(logger), AccessLogMiddleware(logger))
	r.POST("/orders", func(c *gin.Context) {
		WriteBusinessError(c, &BusinessError{Code: 1010, Name: "INVALID_STATE_TRANSITION", Message: "Invalid state transition"})
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/orders", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("transport must not rewrite business conclusion: %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"err_code":1010`) {
		t.Fatalf("body = %s", w.Body.String())
	}
}

func TestReadinessDetailDisclosesNamesOnly(t *testing.T) {
	logger, _ := testLogger()
	r := NewReadiness(logger,
		ReadinessCheck{Name: "metadata_postgresql", Check: func(ctx context.Context) error {
			return errors.New("dial tcp 10.0.0.5:5432: secret-internal-detail")
		}},
		ReadinessCheck{Name: "key_provider", Check: func(ctx context.Context) error { return nil }},
	)
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(RequestIDMiddleware())
	engine.GET("/ready", r.HandleReady)

	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/ready", nil))

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
	if !strings.Contains(w.Body.String(), "metadata_postgresql") {
		t.Fatalf("detail must list failing check names: %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "secret-internal-detail") {
		t.Fatal("internal error details must not leak into readiness response")
	}
}

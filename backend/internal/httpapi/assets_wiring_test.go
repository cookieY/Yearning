// assets_wiring_test.go SPA route wiring tests (`-tags assets` only, BE-B13
// LEFTOVER-CLOSURE): test binaries do not inject digest via ldflags; after TestMain
// injects a valid value via the test hook, the real NewServeRouter serves SPA on NoRoute
// when embedded assets are enabled, keeps Problem 404 for API prefixes (including bare "/api"),
// and leaves known API routes unaffected.
// Compiles only with `-tags assets` when assets/dist exists (release pipeline
// frontend-release-assets runs this suite inline).
//go:build assets

package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/identity/session"
	"github.com/cookieY/YearningX/backend/internal/platform/assets"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

const wiringDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestMain(m *testing.M) {
	assets.SetDigestForTesting(wiringDigest)
	os.Exit(m.Run())
}

func newAssetsWiringRouter(t *testing.T) *gin.Engine {
	t.Helper()
	if !assets.ReleaseProvider().Enabled() {
		t.Fatal("ReleaseProvider disabled under -tags assets")
	}
	return NewServeRouter(ServeDeps{
		Logger: zap.NewNop(),
		Readiness: NewReadiness(zap.NewNop(),
			ReadinessCheck{Name: "metadata_postgresql", Check: func(ctx context.Context) error { return nil }},
		),
		SessionStore:  session.NewMemoryStore(),
		SessionOption: session.Options{CookieName: "yearning_session", AnonymousAllowed: true},
	})
}

func TestAssetsWiringNoRouteServesSPA(t *testing.T) {
	r := newAssetsWiringRouter(t)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/workspace", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET /workspace status = %d, want 200 (SPA fallback)", w.Code)
	}
	body := strings.ToLower(w.Body.String())
	if !strings.Contains(body[:min(300, len(body))], "<!doctype html") {
		t.Fatalf("frontend route did not serve SPA index: %q", w.Body.String()[:min(120, len(w.Body.String()))])
	}
}

func TestAssetsWiringRootServesIndexNoCache(t *testing.T) {
	r := newAssetsWiringRouter(t)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want 200", w.Code)
	}
	if cc := w.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Fatalf("GET / Cache-Control = %q, want no-cache", cc)
	}
}

func TestAssetsWiringAPIPrefixKeepsProblem404(t *testing.T) {
	r := newAssetsWiringRouter(t)
	for _, path := range []string{"/api/v4/no-such-op", "/api"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusNotFound {
			t.Fatalf("GET %s status = %d, want 404", path, w.Code)
		}
		if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
			t.Fatalf("GET %s Content-Type = %q, want problem+json", path, ct)
		}
	}
	// Known API routes are unaffected by NoRoute wiring.
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v4/health/live", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/v4/health/live status = %d, want 200", w.Code)
	}
}

// spa_assets_test.go tests the embedded SPA surface (exclusive to `-tags assets`): test binaries
// do not receive a digest via ldflags, so TestMain injects a valid digest via the test hook and asserts
// against the real embedded dist that Provider is enabled, self-consistent, and servable via Handler.
// This file compiles only when `-tags assets` is active and assets/dist is present (after frontend-release-assets runs) --
// if dist is missing, go:embed fails at compile time, ensuring release builds can never silently omit frontend artifacts.
//go:build assets

package assets

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	SetDigestForTesting(testDigestAssets)
	os.Exit(m.Run())
}

// testDigestAssets is independent of actual dist contents and merely satisfies the 64-character lowercase hex contract;
// authenticity of the embedded dist in release artifacts is asserted by release_frontend_scan against the build-injected digest.
const testDigestAssets = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestReleaseProviderEnabledWithRealDist(t *testing.T) {
	prov := ReleaseProvider()
	if !prov.Enabled() {
		t.Fatal("ReleaseProvider disabled under -tags assets")
	}
	if prov.Digest() != testDigestAssets {
		t.Fatalf("digest = %q, want injected test value", prov.Digest())
	}
	if err := prov.Validate(); err != nil {
		t.Fatalf("embedded provider must validate: %v", err)
	}
}

func TestHandlerServesRealEmbeddedDist(t *testing.T) {
	h := Handler(ReleaseProvider(), nil)
	if h == nil {
		t.Fatal("Handler nil for enabled provider")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(strings.ToLower(body[:min(200, len(body))]), "<!doctype html") {
		t.Fatalf("GET / did not serve SPA index: %q", body[:min(120, len(body))])
	}

	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, httptest.NewRequest(http.MethodGet, "/api/v4/no-such-op", nil))
	if w2.Code != http.StatusNotFound {
		t.Fatalf("GET /api/... status = %d, want 404", w2.Code)
	}
}

func TestHandlerFallsBackOnFrontendRouteWithRealDist(t *testing.T) {
	h := Handler(ReleaseProvider(), nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/orders", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "<div") {
		t.Fatalf("frontend route fallback failed: status=%d", w.Code)
	}
}

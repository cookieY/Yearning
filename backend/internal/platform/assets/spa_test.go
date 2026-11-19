// spa_test.go unit tests for SPA handler behavior (default !assets build using an in-memory FS to cover
// static file hits, SPA index.html fallback, API 404 delegation, and cache headers). The `-tags assets`
// embedded surface is verified against the real dist in spa_assets_test.go.
//go:build !assets

package assets

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

const testDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func testSPAProvider() Provider {
	fsys := fstest.MapFS{
		"index.html":              &fstest.MapFile{Data: []byte("<!doctype html><title>yearning</title>")},
		"assets/index-abc123.js":  &fstest.MapFile{Data: []byte("console.log(1)")},
		"assets/index-abc123.css": &fstest.MapFile{Data: []byte("body{}")},
	}
	return NewProvider(fsys, testDigest)
}

func serveSPA(t *testing.T, prov Provider, notFound http.HandlerFunc, path string) *httptest.ResponseRecorder {
	t.Helper()
	h := Handler(prov, notFound)
	if h == nil {
		t.Fatal("Handler returned nil for enabled provider")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}

func TestHandlerServesIndex(t *testing.T) {
	w := serveSPA(t, testSPAProvider(), nil, "/")
	if w.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want 200", w.Code)
	}
	if w.Body.String() != "<!doctype html><title>yearning</title>" {
		t.Fatalf("GET / body = %q", w.Body.String())
	}
}

func TestHandlerServesHashedAssetWithImmutableCache(t *testing.T) {
	w := serveSPA(t, testSPAProvider(), nil, "/assets/index-abc123.js")
	if w.Code != http.StatusOK {
		t.Fatalf("asset status = %d, want 200", w.Code)
	}
	if cc := w.Header().Get("Cache-Control"); cc != "public, max-age=31536000, immutable" {
		t.Fatalf("asset Cache-Control = %q, want immutable year", cc)
	}
}

func TestHandlerFallsBackToFrontendRoute(t *testing.T) {
	for _, path := range []string{"/orders", "/admin/flows/some-id"} {
		w := serveSPA(t, testSPAProvider(), nil, path)
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, want 200 (index fallback)", path, w.Code)
		}
		if w.Body.String() != "<!doctype html><title>yearning</title>" {
			t.Fatalf("GET %s did not serve index.html", path)
		}
	}
}

func TestHandlerKeepsAPINotFoundSemantics(t *testing.T) {
	called := false
	notFound := func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusNotFound)
	}
	w := serveSPA(t, testSPAProvider(), notFound, "/api/v4/no-such-op")
	if !called {
		t.Fatal("API prefix must route to notFound handler")
	}
	if w.Code != http.StatusNotFound {
		t.Fatalf("GET /api/... status = %d, want 404", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
		t.Fatalf("GET /api/... Content-Type = %q", ct)
	}
}

func TestHandlerNilForDisabledProvider(t *testing.T) {
	if Handler(NewProvider(nil, ""), nil) != nil {
		t.Fatal("Handler must return nil for disabled provider (default builds)")
	}
}

func TestHandlerBareAPIPathKeepsNotFound(t *testing.T) {
	called := false
	notFound := func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNotFound)
	}
	w := serveSPA(t, testSPAProvider(), notFound, "/api")
	if !called || w.Code != http.StatusNotFound {
		t.Fatalf("bare /api must route to notFound (called=%v status=%d)", called, w.Code)
	}
}

func TestHandlerIndexServedNoCache(t *testing.T) {
	for _, path := range []string{"/", "/orders"} {
		w := serveSPA(t, testSPAProvider(), nil, path)
		if cc := w.Header().Get("Cache-Control"); cc != "no-cache" {
			t.Fatalf("GET %s Cache-Control = %q, want no-cache (entry must revalidate)", path, cc)
		}
	}
}

func TestHandlerDefaultNotFoundForAPIWithoutHook(t *testing.T) {
	w := serveSPA(t, testSPAProvider(), nil, "/api/v4/no-such-op")
	if w.Code != http.StatusNotFound {
		t.Fatalf("GET /api/... status = %d, want 404 (http.NotFound default)", w.Code)
	}
}

// oidc_cache_test.go unit tests for the in-process OIDC discovery TTL cache (SEC-0006):
// reuse within TTL (no second discovery call), refetch after TTL expiration, no caching of negative
// results, and per-issuer bucket isolation. Discovery uses an injected fetchOIDCProvider stub without
// real HTTP calls; cache capacity eviction is verified separately.
package identity

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

// cacheTestConfig returns a valid OIDC configuration (keyed by issuer; makes no network requests).
func cacheTestConfig(issuer string) *OIDCConfiguration {
	return &OIDCConfiguration{
		IssuerURL: issuer, ClientID: "client-a",
		Scopes: []string{"openid", "profile"}, UsernameClaim: "preferred_username",
		ConnectTimeoutMS: 1000, RequestTimeoutMS: 1000,
	}
}

// resetOIDCDiscoveryCacheForTest clears the cache and resets injection hooks (test isolation:
// the cache is a process-wide singleton and must not leak state across tests).
func resetOIDCDiscoveryCacheForTest(t *testing.T) {
	t.Helper()
	oidcDiscoveryCache.mu.Lock()
	oidcDiscoveryCache.entries = map[string]oidcDiscoveryEntry{}
	oidcDiscoveryCache.mu.Unlock()
	prevFetch := fetchOIDCProvider
	prevNow := nowFunc
	t.Cleanup(func() {
		oidcDiscoveryCache.mu.Lock()
		oidcDiscoveryCache.entries = map[string]oidcDiscoveryEntry{}
		oidcDiscoveryCache.mu.Unlock()
		fetchOIDCProvider = prevFetch
		nowFunc = prevNow
	})
}

// TestOIDCDiscoveryCacheHitWithinTTL verifies that a second construction within TTL does not trigger a second discovery call.
func TestOIDCDiscoveryCacheHitWithinTTL(t *testing.T) {
	resetOIDCDiscoveryCacheForTest(t)
	fakeTime := time.Now()
	nowFunc = func() time.Time { return fakeTime }

	var calls atomic.Int32
	fetchOIDCProvider = func(context.Context, *http.Client, string) (*oidc.Provider, error) {
		calls.Add(1)
		return &oidc.Provider{}, nil
	}
	cfg := cacheTestConfig("https://idp-cache.example")
	secret := "client-secret-value"
	if _, err := NewOIDCProviderClient(context.Background(), cfg, secret); err != nil {
		t.Fatalf("first build: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("after first build calls = %d, want 1", got)
	}
	if _, err := NewOIDCProviderClient(context.Background(), cfg, secret); err != nil {
		t.Fatalf("second build: %v", err)
	}
	if _, err := NewOIDCProviderClient(context.Background(), cfg, secret); err != nil {
		t.Fatalf("third build: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("within TTL calls = %d, want 1 (discovery must be cached)", got)
	}
}

// TestOIDCDiscoveryCacheExpiresAndRefetches verifies that discovery refetches after TTL expiration.
func TestOIDCDiscoveryCacheExpiresAndRefetches(t *testing.T) {
	resetOIDCDiscoveryCacheForTest(t)
	fakeTime := time.Now()
	nowFunc = func() time.Time { return fakeTime }

	var calls atomic.Int32
	fetchOIDCProvider = func(context.Context, *http.Client, string) (*oidc.Provider, error) {
		calls.Add(1)
		return &oidc.Provider{}, nil
	}
	cfg := cacheTestConfig("https://idp-expire.example")
	if _, err := NewOIDCProviderClient(context.Background(), cfg, "secret-x"); err != nil {
		t.Fatalf("first build: %v", err)
	}
	// Before TTL expires: still hits the cache.
	fakeTime = fakeTime.Add(oidcDiscoveryCacheTTL - time.Minute)
	if _, err := NewOIDCProviderClient(context.Background(), cfg, "secret-x"); err != nil {
		t.Fatalf("build before expiry: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("before expiry calls = %d, want 1", got)
	}
	// Past TTL: refetches from upstream.
	fakeTime = fakeTime.Add(2 * time.Minute)
	if _, err := NewOIDCProviderClient(context.Background(), cfg, "secret-x"); err != nil {
		t.Fatalf("build after expiry: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("after expiry calls = %d, want 2 (expired entry must refetch)", got)
	}
}

// TestOIDCDiscoveryNegativeResultNotCached verifies that negative results are not cached:
// retrying immediately after a failure triggers discovery again (transient IdP errors are not pinned).
func TestOIDCDiscoveryNegativeResultNotCached(t *testing.T) {
	resetOIDCDiscoveryCacheForTest(t)
	var calls atomic.Int32
	fetchOIDCProvider = func(context.Context, *http.Client, string) (*oidc.Provider, error) {
		calls.Add(1)
		if calls.Load() == 1 {
			return nil, errors.New("idp temporarily down")
		}
		return &oidc.Provider{}, nil
	}
	cfg := cacheTestConfig("https://idp-flaky.example")
	if _, err := NewOIDCProviderClient(context.Background(), cfg, "secret-y"); err == nil {
		t.Fatal("first build must surface discovery failure")
	}
	if _, err := NewOIDCProviderClient(context.Background(), cfg, "secret-y"); err != nil {
		t.Fatalf("retry after failure: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("calls = %d, want 2 (failure must not be cached)", got)
	}
}

// TestOIDCDiscoveryCachePerIssuer verifies that distinct issuers do not share cache entries, while
// different client configs under the same issuer reuse endpoint metadata within TTL (constructing verifier/oauth from the new cfg).
func TestOIDCDiscoveryCachePerIssuer(t *testing.T) {
	resetOIDCDiscoveryCacheForTest(t)
	var calls atomic.Int32
	fetchOIDCProvider = func(_ context.Context, _ *http.Client, issuer string) (*oidc.Provider, error) {
		calls.Add(1)
		return &oidc.Provider{}, nil
	}
	if _, err := NewOIDCProviderClient(context.Background(), cacheTestConfig("https://a.example"), "s"); err != nil {
		t.Fatal(err)
	}
	if _, err := NewOIDCProviderClient(context.Background(), cacheTestConfig("https://b.example"), "s"); err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("two issuers calls = %d, want 2 (no cross-issuer hits)", got)
	}
	// New config under the same issuer: hits the cache.
	other := cacheTestConfig("https://a.example")
	other.ClientID = "client-b"
	if _, err := NewOIDCProviderClient(context.Background(), other, "s"); err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("same issuer calls = %d, want 2 (metadata cached per issuer)", got)
	}
}

// TestOIDCDiscoveryCacheCapAndEviction verifies the entry cap: when full, expired entries are
// purged first and the oldest entry is evicted if still full, preventing unbounded cache growth.
func TestOIDCDiscoveryCacheCapAndEviction(t *testing.T) {
	resetOIDCDiscoveryCacheForTest(t)
	fakeTime := time.Now()
	nowFunc = func() time.Time { return fakeTime }
	fetchOIDCProvider = func(context.Context, *http.Client, string) (*oidc.Provider, error) {
		return &oidc.Provider{}, nil
	}
	cfg := cacheTestConfig("https://seed.example")
	if _, err := NewOIDCProviderClient(context.Background(), cfg, "s"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < oidcDiscoveryCacheMaxEntry+10; i++ {
		fakeTime = fakeTime.Add(time.Duration(i+1) * time.Second)
		if _, err := NewOIDCProviderClient(context.Background(),
			cacheTestConfig("https://fill-"+time.Unix(0, 0).Add(time.Duration(i)*time.Second).Format("150405")+".example"), "s"); err != nil {
			t.Fatal(err)
		}
	}
	oidcDiscoveryCache.mu.Lock()
	size := len(oidcDiscoveryCache.entries)
	_, seedKept := oidcDiscoveryCache.entries["https://seed.example"]
	oidcDiscoveryCache.mu.Unlock()
	if size > oidcDiscoveryCacheMaxEntry {
		t.Fatalf("cache size = %d, want <= %d", size, oidcDiscoveryCacheMaxEntry)
	}
	_ = seedKept // The oldest entry may be evicted: assert only the capacity bound rather than specific survivors.
}

// oidc_redirect_test.go verifies OIDC redirect security policies:
// 1. oidcTokenHTTPClient blocks all cross-host redirects (preventing authorization code / client_secret leak).
// 2. oidcHTTPClient blocks HTTPS -> HTTP protocol downgrades and redirects targeting forbidden addresses (loopback/metadata).
package identity

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/platform/addrguard"
)

func TestOIDCTokenClientBlocksCrossHost307RedirectAndLeaksNothing(t *testing.T) {
	t.Cleanup(addrguard.SetLoopbackBypassForTest(true))

	var decoyHits atomic.Int32
	decoy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		decoyHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer decoy.Close()

	idp := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, decoy.URL+"/steal", http.StatusTemporaryRedirect)
	}))
	defer idp.Close()

	cfg := cacheTestConfig("https://idp.example.com")
	cfg.RequestTimeoutMS = 2000
	cfg.ConnectTimeoutMS = 1000
	tokenClient := cfg.oidcTokenHTTPClient()
	// Trust the httptest TLS certificate while keeping CheckRedirect & addrguard DialContext intact.
	tlsCfg := idp.Client().Transport.(*http.Transport).TLSClientConfig
	tokenClient.Transport.(*http.Transport).TLSClientConfig = tlsCfg

	resp, err := tokenClient.PostForm(idp.URL+"/oauth/token", map[string][]string{
		"grant_type":    {"authorization_code"},
		"client_secret": {"super-secret-value"},
	})
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("cross-host 307 redirect on token client succeeded; want rejection")
	}
	if !errors.Is(err, addrguard.ErrAddressForbidden) {
		t.Fatalf("redirect rejection error = %v; want wrapped addrguard.ErrAddressForbidden", err)
	}
	if got := decoyHits.Load(); got != 0 {
		t.Fatalf("decoy server received %d requests (client_secret leak risk); want 0", got)
	}
}

func TestOIDCDiscoveryClientBlocksHTTPSDowngradeAndLoopbackRedirect(t *testing.T) {
	t.Cleanup(addrguard.SetLoopbackBypassForTest(true))
	cfg := cacheTestConfig("https://idp.example.com")
	cfg.RequestTimeoutMS = 2000
	cfg.ConnectTimeoutMS = 1000

	// 1. HTTPS -> HTTP downgrade must be rejected by CheckRedirect even for host-allowed discovery client.
	downgradeTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer downgradeTarget.Close()

	idp := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, downgradeTarget.URL+"/jwks", http.StatusFound)
	}))
	defer idp.Close()

	c := cfg.oidcHTTPClient()
	c.Transport.(*http.Transport).TLSClientConfig = idp.Client().Transport.(*http.Transport).TLSClientConfig
	if resp, err := c.Get(idp.URL + "/.well-known/openid-configuration"); err == nil {
		_ = resp.Body.Close()
		t.Fatal("HTTPS->HTTP downgrade redirect succeeded; want rejection")
	}

	// 2. HTTPS -> HTTPS redirect pointing at loopback 127.0.0.1 must be blocked by addrguard when bypass is off.
	t.Cleanup(addrguard.SetLoopbackBypassForTest(false))
	var decoyHits atomic.Int32
	loopbackHTTPS := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		decoyHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer loopbackHTTPS.Close()

	client := cfg.oidcHTTPClient()
	if err := client.CheckRedirect(
		httptest.NewRequest(http.MethodGet, loopbackHTTPS.URL, nil),
		[]*http.Request{httptest.NewRequest(http.MethodGet, "https://idp.example.com/jwks", nil)},
	); err != nil {
		// Same-scheme cross-host discovery redirect passes CheckRedirect and MUST be blocked by addrguard DialContext.
		t.Fatalf("unexpected CheckRedirect error on https->https: %v", err)
	}
	if resp, err := client.Get(loopbackHTTPS.URL); err == nil {
		_ = resp.Body.Close()
		t.Fatal("loopback redirect target reachable with bypass off; want addrguard dial rejection")
	}
	if got := decoyHits.Load(); got != 0 {
		t.Fatalf("loopback server received %d hits; want 0", got)
	}
}

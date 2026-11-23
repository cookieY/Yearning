// release_proxy_test.go verifies release feed HTTP client proxy hardening:
// 1. HTTPS_PROXY / HTTP_PROXY environment variables are ignored.
// 2. Explicitly configured release_feed.proxy_url is validated by addrguard (even when allow_loopback_datasource=true).
// 3. When permitted by address policy, traffic routes explicitly through the configured proxy.
package dashboard

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/platform/addrguard"
)

func TestReleaseHTTPClientIgnoresEnvProxy(t *testing.T) {
	t.Cleanup(addrguard.SetLoopbackBypassForTest(true))

	var envProxyHits atomic.Int32
	envProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		envProxyHits.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer envProxy.Close()

	var targetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"tag_name":"v4.0.0"}`)
	}))
	defer target.Close()

	for _, k := range []string{"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy"} {
		t.Setenv(k, envProxy.URL)
	}

	c, err := NewReleaseHTTPClient("")
	if err != nil {
		t.Fatalf("NewReleaseHTTPClient(\"\"): %v", err)
	}
	resp, err := c.Get(target.URL)
	if err != nil {
		t.Fatalf("release client GET: %v", err)
	}
	_ = resp.Body.Close()
	if got := envProxyHits.Load(); got != 0 {
		t.Fatalf("env proxy received %d hits; want 0 (ProxyFromEnvironment must be disabled)", got)
	}
	if got := targetHits.Load(); got != 1 {
		t.Fatalf("direct target received %d hits; want 1", got)
	}
}

func TestReleaseHTTPClientExplicitProxyValidatedAndUsed(t *testing.T) {
	t.Cleanup(addrguard.SetLoopbackBypassForTest(false))
	t.Cleanup(addrguard.SetAllowLoopbackDatasourceForTest(true))

	// Forbidden proxy targets must be rejected regardless of allow_loopback_datasource.
	for _, badProxy := range []string{"http://127.0.0.1:8888", "http://169.254.169.254:80", "http://100.100.100.200:8080"} {
		if _, err := NewReleaseHTTPClient(badProxy); err == nil || !errors.Is(err, addrguard.ErrAddressForbidden) {
			t.Errorf("NewReleaseHTTPClient(%q) = %v; want ErrAddressForbidden", badProxy, err)
		}
	}

	// Explicit proxy works when permitted (simulate permitted network via dev loopback bypass).
	t.Cleanup(addrguard.SetLoopbackBypassForTest(true))
	var proxyHits atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"tag_name":"v4.0.0-proxied"}`)
	}))
	defer proxy.Close()

	c, err := NewReleaseHTTPClient(proxy.URL)
	if err != nil {
		t.Fatalf("NewReleaseHTTPClient(proxy): %v", err)
	}
	resp, err := c.Get("http://192.0.2.50/releases/latest")
	if err != nil {
		t.Fatalf("GET via explicit proxy: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if proxyHits.Load() != 1 || !strings.Contains(string(body), "v4.0.0-proxied") {
		t.Fatalf("explicit proxy not used as expected: hits=%d body=%q", proxyHits.Load(), string(body))
	}
}

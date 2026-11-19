//go:build integration

// b11_cursor_http_continuation_test.go F-Q-002 regression: cursor continuation must survive across
// real HTTP requests. Live cursor connections/result streams were previously bound to the exec request
// context, which a real HTTP server cancels as soon as the handler returns -- causing all continuations
// (display and export) to fail with QUERY_STATEMENT_REJECTED(4011); browser XLSX exports always continue
// (opening cursor with page_size=1) and thus always failed. In-process ServeHTTP test clients do not cancel
// request contexts, so this test uses httptest.NewServer for real HTTP round trips.
package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// httpJarClient real HTTP round-trip client (request context is cancelled when response returns).
type httpJarClient struct {
	t      *testing.T
	base   string
	client *http.Client
}

func newHTTPJarClient(t *testing.T, base string) *httpJarClient {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &httpJarClient{t: t, base: base, client: &http.Client{Jar: jar}}
}

func (c *httpJarClient) call(method, path string, body any, extra map[string]string) (int, map[string]any, string) {
	c.t.Helper()
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			c.t.Fatal(err)
		}
		rd = strings.NewReader(string(raw))
	}
	req, err := http.NewRequest(method, c.base+path, rd)
	if err != nil {
		c.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", c.base)
	if u, err := cookiejarOf(c); err == nil {
		for _, ck := range u {
			if ck.Name == "yearning_csrf" {
				req.Header.Set("X-CSRF-Token", ck.Value)
			}
		}
	}
	for k, v := range extra {
		req.Header.Set(k, v)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var parsed map[string]any
	_ = json.Unmarshal(raw, &parsed)
	return resp.StatusCode, parsed, string(raw)
}

func cookiejarOf(c *httpJarClient) ([]*http.Cookie, error) {
	u, err := url.Parse(c.base)
	if err != nil {
		return nil, err
	}
	return c.client.Jar.Cookies(u), nil
}

// TestB11CursorContinuationAcrossHTTPRequests F-Q-002: after exec request completes
// (and its request context is cancelled), export/display continuation pages must still succeed.
func TestB11CursorContinuationAcrossHTTPRequests(t *testing.T) {
	fx := newB11Fixture(t)
	sessionID := fx.b11ActiveSessionHTTP(t)

	server := httptest.NewServer(fx.engine)
	defer server.Close()
	ua := newHTTPJarClient(t, server.URL)
	code, body, _ := ua.call("POST", "/api/v4/auth/login", map[string]any{
		"username": "quser1", "password": "quser1-pass-123",
	}, nil)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("login over real http: %d", code)
	}

	// Here the exec HTTP round trip has returned: the real http.Server has cancelled that request
	// context. If continuation still binds to the request context (F-Q-002 root cause), it will fail here.
	// Each purpose opens its own execution -- cursors are strictly forward-only and become CURSOR_EXPIRED once exhausted.
	for _, purpose := range []string{"export", "display"} {
		code, body, raw := ua.call("POST", "/api/v4/query-sessions/"+sessionID+"/executions", map[string]any{
			"datasource_id": fx.qDsID, "schema_name": fx.qSchema,
			"sql": "select id, name from qtarget.accounts", "timeout_ms": 30000, "page_size": 1,
		}, nil)
		if code != 200 || errCode(body) != 0 {
			t.Fatalf("exec for %s: %d %s", purpose, code, raw)
		}
		data := body["data"].(map[string]any)
		cursor := data["page"].(map[string]any)["next_cursor"].(string)
		execID := data["execution_id"].(string)
		code, body, raw = ua.call("GET",
			fmt.Sprintf("/api/v4/query-executions/%s/pages?cursor=%s&purpose=%s&page_size=1000", execID, cursor, purpose), nil, nil)
		if code != 200 || errCode(body) != 0 {
			t.Fatalf("%s continuation after http round trip (F-Q-002): %d %s", purpose, code, raw)
		}
	}
}

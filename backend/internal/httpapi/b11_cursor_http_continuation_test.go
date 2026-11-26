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

	"github.com/google/uuid"
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
	return &httpJarClient{t: t, base: base, client: &http.Client{
		Jar:       jar,
		Transport: &http.Transport{DisableKeepAlives: true},
	}}
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

// TestB11CursorMultiPageHTTPContinuationAndExhaustion verifies DEF-09 across independent
// HTTP requests with Connection: close (DisableKeepAlives=true) so each server handler's
// request context completes and cancels before the next page request begins:
// page 1 -> page 2 -> page 3 (exhaustion) -> post-exhaustion replay returns 1009 CURSOR_EXPIRED (never 4011),
// and closing the session mid-cursor also expires continuation with 1009 CURSOR_EXPIRED.
func TestB11CursorMultiPageHTTPContinuationAndExhaustion(t *testing.T) {
	fx := newB11Fixture(t)
	sessionID := fx.b11ActiveSessionHTTP(t)

	server := httptest.NewServer(fx.engine)
	defer server.Close()
	ua := newHTTPJarClient(t, server.URL)
	code, body, raw := ua.call("POST", "/api/v4/auth/login", map[string]any{
		"username": "quser1", "password": "quser1-pass-123",
	}, nil)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("login over real http: %d %s", code, raw)
	}

	for _, purpose := range []string{"display", "export"} {
		// Page 1: 3 total rows with page_size=1 -> returns row 1 and cursor tok1.
		code, body, raw = ua.call("POST", "/api/v4/query-sessions/"+sessionID+"/executions", map[string]any{
			"datasource_id": fx.qDsID, "schema_name": fx.qSchema,
			"sql": "select id, label from qtarget.pages where id <= 3 order by id", "timeout_ms": 30000, "page_size": 1,
		}, nil)
		if code != 200 || errCode(body) != 0 {
			t.Fatalf("%s page1 exec: %d %s", purpose, code, raw)
		}
		data1 := body["data"].(map[string]any)
		execID := data1["execution_id"].(string)
		rows1 := data1["rows"].([]any)
		page1 := data1["page"].(map[string]any)
		if len(rows1) != 1 || page1["has_more"] != true {
			t.Fatalf("%s page1 unexpected rows/has_more: %s", purpose, raw)
		}
		tok1, _ := page1["next_cursor"].(string)
		if tok1 == "" {
			t.Fatalf("%s page1 missing next_cursor: %s", purpose, raw)
		}

		// Page 2: independent HTTP request after page 1 completed -> returns row 2 and cursor tok2.
		code, body, raw = ua.call("GET",
			fmt.Sprintf("/api/v4/query-executions/%s/pages?cursor=%s&purpose=%s&page_size=1", execID, tok1, purpose), nil, nil)
		if code != 200 || errCode(body) != 0 {
			t.Fatalf("%s page2 continuation (DEF-09): %d %s", purpose, code, raw)
		}
		data2 := body["data"].(map[string]any)
		rows2 := data2["rows"].([]any)
		page2 := data2["page"].(map[string]any)
		if len(rows2) != 1 || page2["has_more"] != true {
			t.Fatalf("%s page2 unexpected rows/has_more: %s", purpose, raw)
		}
		tok2, _ := page2["next_cursor"].(string)
		if tok2 == "" {
			t.Fatalf("%s page2 missing next_cursor: %s", purpose, raw)
		}

		// Page 3: returns row 3 and cursor tok3 (page_size=1 stops cleanly at boundary without peeking EOF).
		code, body, raw = ua.call("GET",
			fmt.Sprintf("/api/v4/query-executions/%s/pages?cursor=%s&purpose=%s&page_size=1", execID, tok2, purpose), nil, nil)
		if code != 200 || errCode(body) != 0 {
			t.Fatalf("%s page3 continuation: %d %s", purpose, code, raw)
		}
		data3 := body["data"].(map[string]any)
		rows3 := data3["rows"].([]any)
		page3 := data3["page"].(map[string]any)
		if len(rows3) != 1 || page3["has_more"] != true {
			t.Fatalf("%s page3 unexpected rows/has_more: %s", purpose, raw)
		}
		tok3, _ := page3["next_cursor"].(string)
		if tok3 == "" {
			t.Fatalf("%s page3 missing next_cursor: %s", purpose, raw)
		}

		// Page 4: advances to EOF, returns 0 rows, has_more=false, next_cursor=null, and exhausts cursor.
		code, body, raw = ua.call("GET",
			fmt.Sprintf("/api/v4/query-executions/%s/pages?cursor=%s&purpose=%s&page_size=1", execID, tok3, purpose), nil, nil)
		if code != 200 || errCode(body) != 0 {
			t.Fatalf("%s page4 exhaustion: %d %s", purpose, code, raw)
		}
		data4 := body["data"].(map[string]any)
		var rowCount4 int
		if r4, ok := data4["rows"].([]any); ok {
			rowCount4 = len(r4)
		}
		page4 := data4["page"].(map[string]any)
		if rowCount4 != 0 || page4["has_more"] != false || page4["next_cursor"] != nil {
			t.Fatalf("%s page4 unexpected exhaustion response: %s", purpose, raw)
		}

		// Post-exhaustion replay must return 1009 CURSOR_EXPIRED, never 4011.
		for label, tok := range map[string]string{"tok3": tok3, "tok2": tok2, "tok1": tok1} {
			code, body, raw = ua.call("GET",
				fmt.Sprintf("/api/v4/query-executions/%s/pages?cursor=%s&purpose=%s&page_size=1", execID, tok, purpose), nil, nil)
			gotCode := errCode(body)
			if gotCode == 4011 || gotCode != 1009 {
				t.Fatalf("%s post-exhaustion %s expected err_code=1009 (never 4011), got %d (%s)", purpose, label, gotCode, raw)
			}
		}
	}

	// Open a fresh cursor on page 1, close the session over HTTP, and verify continuation returns 1009 CURSOR_EXPIRED.
	code, body, raw = ua.call("POST", "/api/v4/query-sessions/"+sessionID+"/executions", map[string]any{
		"datasource_id": fx.qDsID, "schema_name": fx.qSchema,
		"sql": "select id, label from qtarget.pages where id <= 3 order by id", "timeout_ms": 30000, "page_size": 1,
	}, nil)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("open cursor before close: %d %s", code, raw)
	}
	closeData := body["data"].(map[string]any)
	closeExecID := closeData["execution_id"].(string)
	closeTok := closeData["page"].(map[string]any)["next_cursor"].(string)

	code, body, raw = ua.call("POST", "/api/v4/query-sessions/"+sessionID+"/closure", map[string]any{"reason": "done"}, map[string]string{
		"Idempotency-Key": uuid.NewString(),
	})
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("close session: %d %s", code, raw)
	}
	code, body, raw = ua.call("GET",
		fmt.Sprintf("/api/v4/query-executions/%s/pages?cursor=%s&purpose=display&page_size=1", closeExecID, closeTok), nil, nil)
	gotCode := errCode(body)
	if gotCode == 4011 || gotCode != 1009 {
		t.Fatalf("closed-session continuation expected err_code=1009 (never 4011), got %d (%s)", gotCode, raw)
	}
}

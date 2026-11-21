package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/cookieY/YearningX/backend/internal/identity/session"
)

func TestB11ListHandlersDoNotDoubleWriteJSONOnError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	deps, actorID := newHermeticB11DepsWithFailingDB(t)

	r := gin.New()
	r.Use(session.Middleware(b11MemSessionStore{
		id: &session.Identity{UserID: actorID, Username: "alice", IsBuiltinAdmin: false},
	}, session.Options{CookieName: "yearning_session"}))

	r.GET("/query/orders", deps.HandleListQueryOrders)
	r.GET("/query-access-requests", deps.HandleListQueryAccessRequests)
	r.GET("/query-grants", deps.HandleListQueryGrants)
	r.GET("/query-sessions", deps.HandleListQuerySessions)

	cases := []struct {
		name           string
		path           string
		expectBizError bool
		expectCode     float64
	}{
		{name: "query orders invalid cursor", path: "/query/orders?after=invalid_cursor", expectBizError: true, expectCode: 1001},
		{name: "query access requests db error", path: "/query-access-requests"},
		{name: "query grants db error", path: "/query-grants"},
		{name: "query sessions db error", path: "/query-sessions"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			req.AddCookie(&http.Cookie{Name: "yearning_session", Value: "tok"})
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			body := bytes.TrimSpace(rec.Body.Bytes())
			dec := json.NewDecoder(bytes.NewReader(body))
			var first map[string]any
			if err := dec.Decode(&first); err != nil {
				t.Fatalf("GET %s invalid JSON error response %q: %v", tc.path, string(body), err)
			}
			if tc.expectBizError {
				if rec.Code != http.StatusOK || first["err_code"] != tc.expectCode {
					t.Fatalf("GET %s expected HTTP 200 with err_code=%.0f, got status=%d body=%s",
						tc.path, tc.expectCode, rec.Code, string(body))
				}
			} else if rec.Code == http.StatusOK {
				t.Fatalf("GET %s expected HTTP error status, got 200 body=%s", tc.path, string(body))
			}
			var second any
			if err := dec.Decode(&second); err != io.EOF {
				t.Fatalf("GET %s double-wrote trailing JSON payload after first envelope: err=%v extra=%v body=%s",
					tc.path, err, second, string(body))
			}
		})
	}
}

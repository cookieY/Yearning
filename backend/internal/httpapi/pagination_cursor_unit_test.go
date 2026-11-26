package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRequireCursorRejectsMalformedCursorsWith1001(t *testing.T) {
	gin.SetMode(gin.TestMode)

	badCursors := []string{
		"no-pipe",
		"|only-id",
		"not-a-timestamp|550e8400-e29b-41d4-a716-446655440000",
		"2026-11-25T10:00:00Z|",
		"2026-11-25T10:00:00Z|bad id",
	}

	r := gin.New()
	r.GET("/probe", func(c *gin.Context) {
		after, ok := requireCursor(c)
		if !ok {
			return
		}
		WriteSuccess(c, gin.H{"after": after})
	})

	for _, raw := range badCursors {
		req := httptest.NewRequest(http.MethodGet, "/probe?after="+url.QueryEscape(raw), nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("cursor %q: expected HTTP 200 business envelope, got %d body=%s", raw, rec.Code, rec.Body.String())
		}
		var env struct {
			ErrCode int    `json:"err_code"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatalf("cursor %q: decode envelope: %v", raw, err)
		}
		if env.ErrCode != 1001 || env.Message == "" {
			t.Fatalf("cursor %q: got err_code=%d message=%q, want 1001", raw, env.ErrCode, env.Message)
		}
	}

	// Valid canonical microsecond and legacy RFC3339Nano cursors must pass.
	goodCursors := []string{
		"2026-11-25T10:00:00.123456Z|550e8400-e29b-41d4-a716-446655440000",
		"2026-11-25T10:00:00.123456789Z|550e8400-e29b-41d4-a716-446655440000",
		"2026-11-25T10:00:00Z|550e8400-e29b-41d4-a716-446655440000",
	}
	for _, raw := range goodCursors {
		req := httptest.NewRequest(http.MethodGet, "/probe?after="+url.QueryEscape(raw), nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("good cursor %q: HTTP %d body=%s", raw, rec.Code, rec.Body.String())
		}
		var env struct {
			ErrCode int `json:"err_code"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatalf("good cursor %q: decode: %v", raw, err)
		}
		if env.ErrCode != 0 {
			t.Fatalf("good cursor %q: unexpected err_code=%d body=%s", raw, env.ErrCode, rec.Body.String())
		}
	}
}

func TestHandleListChangeDraftsRejectsInvalidCursorWith1001(t *testing.T) {
	gin.SetMode(gin.TestMode)
	deps := &B6Deps{}
	r := gin.New()
	r.GET("/drafts", deps.HandleListChangeDrafts)

	for _, raw := range []string{"not-a-cursor", "2026-11-25T10:00:00Z|not-a-uuid", "bad-ts|550e8400-e29b-41d4-a716-446655440000"} {
		req := httptest.NewRequest(http.MethodGet, "/drafts?after="+url.QueryEscape(raw), nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("draft cursor %q: expected HTTP 200 business error, got %d", raw, rec.Code)
		}
		var env struct {
			ErrCode int    `json:"err_code"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatalf("draft cursor %q: decode: %v", raw, err)
		}
		if env.ErrCode != 1001 || env.Message == "" {
			t.Fatalf("draft cursor %q: got err_code=%d message=%q, want 1001", raw, env.ErrCode, env.Message)
		}
	}
}

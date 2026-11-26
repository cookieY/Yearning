package httpenvelope

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestEnvelopeAndProblemDetailsAndIfMatch(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// 1. WriteSuccess includes err_code=0, message=ok, data, request_id.
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set(RequestIDKey, "req-ok-1")
	WriteSuccess(c, gin.H{"k": "v"})
	if w.Code != http.StatusOK {
		t.Fatalf("WriteSuccess status = %d, want 200", w.Code)
	}
	var okEnv map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &okEnv); err != nil {
		t.Fatalf("decode WriteSuccess: %v", err)
	}
	if okEnv["err_code"] != float64(0) || okEnv["request_id"] != "req-ok-1" {
		t.Fatalf("unexpected WriteSuccess envelope: %+v", okEnv)
	}

	// 2. WriteCodedError auto-marks 1012 as retryable with retry_after_ms=1000.
	w1012 := httptest.NewRecorder()
	c1012, _ := gin.CreateTestContext(w1012)
	c1012.Set(RequestIDKey, "req-1012")
	WriteCodedError(c1012, 1012, "SYSTEM_CAPACITY_EXHAUSTED", "busy", nil)
	var errEnv struct {
		ErrCode   int            `json:"err_code"`
		Retryable bool           `json:"retryable"`
		RequestID string         `json:"request_id"`
		Meta      map[string]any `json:"meta"`
	}
	if err := json.Unmarshal(w1012.Body.Bytes(), &errEnv); err != nil {
		t.Fatalf("decode 1012: %v", err)
	}
	if errEnv.ErrCode != 1012 || !errEnv.Retryable || errEnv.RequestID != "req-1012" || errEnv.Meta["retry_after_ms"] != float64(1000) {
		t.Fatalf("unexpected 1012 envelope: %+v", errEnv)
	}

	// 3. WriteProblem emits RFC 9457 application/problem+json without err_code.
	wp := httptest.NewRecorder()
	cp, _ := gin.CreateTestContext(wp)
	cp.Set(RequestIDKey, "req-prob-500")
	WriteProblem(cp, http.StatusInternalServerError, "authorization guard not configured")
	if wp.Code != http.StatusInternalServerError {
		t.Fatalf("WriteProblem status = %d, want 500", wp.Code)
	}
	if ct := wp.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
		t.Fatalf("WriteProblem Content-Type = %q, want application/problem+json", ct)
	}
	var prob ProblemDetails
	if err := json.Unmarshal(wp.Body.Bytes(), &prob); err != nil {
		t.Fatalf("decode problem: %v", err)
	}
	if prob.Type != "about:blank" || prob.Status != 500 || prob.RequestID != "req-prob-500" || prob.Detail != "authorization guard not configured" {
		t.Fatalf("unexpected ProblemDetails: %+v", prob)
	}

	// 4. ParseIfMatch accepts quoted positive integers and rejects missing/malformed/non-positive with 1001.
	for _, bad := range []string{"", "1", `"0"`, `"-5"`, `"abc"`, `""`, `"1`} {
		wb := httptest.NewRecorder()
		cb, _ := gin.CreateTestContext(wb)
		cb.Request = httptest.NewRequest(http.MethodPut, "/", nil)
		if bad != "" {
			cb.Request.Header.Set("If-Match", bad)
		}
		if v, ok := ParseIfMatch(cb); ok || v != 0 {
			t.Fatalf("ParseIfMatch(%q) = (%d, %v), want (0, false)", bad, v, ok)
		}
		if wb.Code != http.StatusOK || !strings.Contains(wb.Body.String(), `"err_code":1001`) {
			t.Fatalf("ParseIfMatch(%q) body = %d %s", bad, wb.Code, wb.Body.String())
		}
	}
	wg := httptest.NewRecorder()
	cg, _ := gin.CreateTestContext(wg)
	cg.Request = httptest.NewRequest(http.MethodPut, "/", nil)
	cg.Request.Header.Set("If-Match", `"42"`)
	if v, ok := ParseIfMatch(cg); !ok || v != 42 {
		t.Fatalf("ParseIfMatch(\"42\") = (%d, %v), want (42, true)", v, ok)
	}
}

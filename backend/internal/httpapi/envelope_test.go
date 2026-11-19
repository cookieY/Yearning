package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func perform(handler gin.HandlerFunc, method, path string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RequestIDMiddleware())
	r.Handle(method, path, handler)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, nil)
	r.ServeHTTP(w, req)
	return w
}

func TestWriteSuccessShape(t *testing.T) {
	w := perform(func(c *gin.Context) {
		WriteSuccess(c, gin.H{"id": "abc", "version": 7})
	}, http.MethodGet, "/ok")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Fatalf("content type = %s", ct)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["err_code"].(float64) != 0 {
		t.Errorf("err_code = %v", body["err_code"])
	}
	if body["message"] != "ok" {
		t.Errorf("message = %v", body["message"])
	}
	data := body["data"].(map[string]any)
	if data["version"].(float64) != 7 {
		t.Errorf("data.version = %v", data["version"])
	}
	if body["request_id"] == "" || body["request_id"] == nil {
		t.Error("request_id must be present")
	}
}

func TestWriteSuccessNullData(t *testing.T) {
	w := perform(func(c *gin.Context) { WriteSuccess(c, nil) }, http.MethodGet, "/ok")
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["data"] != nil {
		t.Errorf("data = %v, want null", body["data"])
	}
}

func TestWriteBusinessErrorShape(t *testing.T) {
	w := perform(func(c *gin.Context) {
		WriteBusinessError(c, &BusinessError{
			Code:      1003,
			Name:      "VERSION_CONFLICT",
			Message:   "The order has been updated by another operation; please refresh and retry.",
			Retryable: false,
			Meta:      map[string]any{"current_version": 8},
		})
	}, http.MethodPost, "/submit")

	if w.Code != http.StatusOK {
		t.Fatalf("business error must stay HTTP 200, got %d", w.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["err_code"].(float64) != 1003 {
		t.Errorf("err_code = %v", body["err_code"])
	}
	if body["retryable"] != false {
		t.Errorf("retryable = %v", body["retryable"])
	}
	meta := body["meta"].(map[string]any)
	if meta["current_version"].(float64) != 8 {
		t.Errorf("meta.current_version = %v", meta["current_version"])
	}
	if _, exists := body["name"]; exists {
		t.Error("symbolic name must not leak into responses")
	}
}

func TestWriteProblemShape(t *testing.T) {
	w := perform(func(c *gin.Context) {
		WriteProblem(c, http.StatusServiceUnavailable, "metadata database unreachable")
	}, http.MethodGet, "/boom")

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/problem+json; charset=utf-8" {
		t.Fatalf("content type = %s", ct)
	}
	var problem ProblemDetails
	if err := json.Unmarshal(w.Body.Bytes(), &problem); err != nil {
		t.Fatal(err)
	}
	if problem.Type != "about:blank" {
		t.Errorf("type = %s", problem.Type)
	}
	if problem.Title != "Service Unavailable" {
		t.Errorf("title = %s", problem.Title)
	}
	if problem.Status != http.StatusServiceUnavailable {
		t.Errorf("status = %d", problem.Status)
	}
	if problem.Detail != "metadata database unreachable" {
		t.Errorf("detail = %s", problem.Detail)
	}
	if problem.RequestID == "" {
		t.Error("problem must carry request_id")
	}
}

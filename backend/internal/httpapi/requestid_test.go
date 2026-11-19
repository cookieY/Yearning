package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func serveWithRequestID(t *testing.T, incoming string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RequestIDMiddleware())
	r.GET("/ping", func(c *gin.Context) { c.String(http.StatusOK, RequestID(c)) })
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	if incoming != "" {
		req.Header.Set(HeaderRequestID, incoming)
	}
	r.ServeHTTP(w, req)
	return w
}

func TestRequestIDGeneratedWhenAbsent(t *testing.T) {
	w := serveWithRequestID(t, "")
	id := w.Body.String()
	if _, err := uuid.Parse(id); err != nil {
		t.Fatalf("generated request id must be a UUID: %q", id)
	}
	if w.Header().Get(HeaderRequestID) != id {
		t.Fatalf("response header must echo request id: %q vs %q", w.Header().Get(HeaderRequestID), id)
	}
}

func TestRequestIDAcceptsValidUpstreamUUID(t *testing.T) {
	trusted := "0198d9d0-0ec8-749d-b5d1-0cb384c603a0"
	w := serveWithRequestID(t, trusted)
	if got := w.Body.String(); got != trusted {
		t.Fatalf("valid upstream id must be adopted: %q", got)
	}
}

func TestRequestIDRejectsGarbageHeader(t *testing.T) {
	for _, garbage := range []string{"not-a-uuid", "'; DROP TABLE x; --", "0198d9d0-0ec8-749d-b5d1-0cb384c603a0-extra"} {
		w := serveWithRequestID(t, garbage)
		id := w.Body.String()
		if _, err := uuid.Parse(id); err != nil {
			t.Fatalf("garbage header must be replaced by generated UUID, got %q", id)
		}
		if id == garbage {
			t.Fatalf("garbage value must not be echoed: %q", id)
		}
	}
}

func TestRequestIDContextAccessor(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RequestIDMiddleware())
	var fromCtx string
	r.GET("/ctx", func(c *gin.Context) {
		fromCtx = RequestIDFromContext(c.Request.Context())
		c.Status(http.StatusOK)
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/ctx", nil))
	if fromCtx == "" {
		t.Fatal("request context must carry the request id")
	}
}

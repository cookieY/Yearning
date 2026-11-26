// Package httpenvelope provides canonical v4 HTTP response envelopes (H001),
// RFC 9457 Problem Details writers, and strict If-Match version parsing shared across
// httpapi and domain HTTP sub-routers without creating package import cycles.
package httpenvelope

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/cookieY/YearningX/backend/internal/authorization/bizerr"
)

// RequestIDKey is the Gin context key storing the request correlation ID.
const RequestIDKey = "request_id"

// BusinessError is the typed representation of a business-layer conclusion.
// The transport layer maps it without altering semantics.
type BusinessError struct {
	Code      int
	Name      string // Symbolic name, used only for log indexing and omitted from HTTP responses
	Message   string // Localizable message
	Retryable bool
	Meta      map[string]any // Key set stabilized per error catalog
}

func (e *BusinessError) Error() string {
	return fmt.Sprintf("business error %d(%s): %s", e.Code, e.Name, e.Message)
}

// ProblemDetails follows RFC 9457; type is fixed to about:blank and title uses the standard HTTP status phrase.
type ProblemDetails struct {
	Type      string `json:"type"`
	Title     string `json:"title"`
	Status    int    `json:"status"`
	Detail    string `json:"detail,omitempty"`
	RequestID string `json:"request_id"`
}

// RequestID returns the Request ID stored on the Gin context, or empty string if absent.
func RequestID(c *gin.Context) string {
	return c.GetString(RequestIDKey)
}

// WriteSuccess writes an HTTP 200 success envelope; data may be nil.
func WriteSuccess(c *gin.Context, data any) {
	c.JSON(http.StatusOK, gin.H{
		"err_code":   0,
		"message":    "ok",
		"data":       data,
		"request_id": RequestID(c),
	})
}

// WriteBusinessError writes an HTTP 200 business error envelope.
// Sensitive SQL, credentials, raw Provider responses, and internal stack traces must never enter Message or Meta.
func WriteBusinessError(c *gin.Context, e *BusinessError) {
	envelope := gin.H{
		"err_code":   e.Code,
		"message":    e.Message,
		"data":       nil,
		"request_id": RequestID(c),
		"retryable":  e.Retryable,
	}
	if e.Meta != nil {
		envelope["meta"] = e.Meta
	}
	c.JSON(http.StatusOK, envelope)
}

// WriteCodedError writes a canonical HTTP 200 business error envelope and automatically marks
// 1012 SYSTEM_CAPACITY_EXHAUSTED as retryable with retry_after_ms=1000 when unspecified.
func WriteCodedError(c *gin.Context, code int, name, message string, meta map[string]any) {
	if message == "" {
		message = "Business operation failed"
	}
	be := &BusinessError{
		Code:    code,
		Name:    name,
		Message: message,
		Meta:    meta,
	}
	if code == 1012 {
		be.Retryable = true
		cloned := make(map[string]any, len(meta)+1)
		for k, v := range meta {
			cloned[k] = v
		}
		if _, ok := cloned["retry_after_ms"]; !ok {
			cloned["retry_after_ms"] = 1000
		}
		be.Meta = cloned
	}
	WriteBusinessError(c, be)
}

// WriteProblem writes an RFC 9457 Problem Details response with a real HTTP error status.
// Media type is fixed to application/problem+json and carries no business err_code.
func WriteProblem(c *gin.Context, status int, detail string) {
	body, err := json.Marshal(ProblemDetails{
		Type:      "about:blank",
		Title:     http.StatusText(status),
		Status:    status,
		Detail:    detail,
		RequestID: RequestID(c),
	})
	if err != nil {
		c.Data(status, "application/problem+json; charset=utf-8", fmt.Appendf(nil, `{"type":"about:blank","status":%d}`, status))
		return
	}
	c.Data(status, "application/problem+json; charset=utf-8", body)
}

// ParseIfMatch strictly parses the If-Match optimistic concurrency version (quoted positive integer "N").
// Missing, unquoted, or non-positive values write a 1001 VALIDATION_FAILED business error and return false.
func ParseIfMatch(c *gin.Context) (int64, bool) {
	raw := strings.TrimSpace(c.GetHeader("If-Match"))
	if len(raw) < 3 || raw[0] != '"' || raw[len(raw)-1] != '"' {
		WriteBusinessError(c, &BusinessError{
			Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED",
			Message: "If-Match header must be a quoted version number",
		})
		return 0, false
	}
	version, err := strconv.ParseInt(raw[1:len(raw)-1], 10, 64)
	if err != nil || version < 1 {
		WriteBusinessError(c, &BusinessError{
			Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED",
			Message: "Invalid If-Match version",
		})
		return 0, false
	}
	return version, true
}

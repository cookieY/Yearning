// Package httpapi implements the unified v4 HTTP response protocol.
//
// H001: all JSON endpoints reaching the business layer return HTTP 200 + numeric err_code;
// protocol, authentication, security, and infrastructure failures use real HTTP status codes + application/problem+json.
// The transport layer must never rewrite business-layer conclusions based on error names, messages, or Go types.
package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
)

// Stable business error codes (1000-range in error catalog). B1 uses only the general range;
// domain-specific ranges are introduced as subsequent stages implement their business logic.
const (
	ErrCodeValidationFailed = 1001 // VALIDATION_FAILED
	ErrCodeResourceNotFound = 1002 // RESOURCE_NOT_FOUND
)

// BusinessError is the typed representation of a business-layer conclusion. The transport layer only maps it without altering semantics.
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
	// meta is an optional object rather than a nullable field: omit entirely when empty (F12 #003 -- previously
	// serialized null in violation of contract).
	if e.Meta != nil {
		envelope["meta"] = e.Meta
	}
	c.JSON(http.StatusOK, envelope)
}

// ProblemDetails follows RFC 9457; type is fixed to about:blank and title uses the standard HTTP status phrase.
type ProblemDetails struct {
	Type      string `json:"type"`
	Title     string `json:"title"`
	Status    int    `json:"status"`
	Detail    string `json:"detail,omitempty"`
	RequestID string `json:"request_id"`
}

// WriteProblem writes a Problem Details response with a real HTTP error status.
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

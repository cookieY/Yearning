// Package httpapi implements the unified v4 HTTP response protocol.
//
// H001: all JSON endpoints reaching the business layer return HTTP 200 + numeric err_code;
// protocol, authentication, security, and infrastructure failures use real HTTP status codes + application/problem+json.
// The transport layer must never rewrite business-layer conclusions based on error names, messages, or Go types.
package httpapi

import (
	"github.com/gin-gonic/gin"

	"github.com/cookieY/YearningX/backend/internal/platform/httpenvelope"
)

// Stable business error codes (1000-range in error catalog). B1 uses only the general range;
// domain-specific ranges are introduced as subsequent stages implement their business logic.
const (
	ErrCodeValidationFailed = 1001 // VALIDATION_FAILED
	ErrCodeResourceNotFound = 1002 // RESOURCE_NOT_FOUND
)

// BusinessError is the typed representation of a business-layer conclusion. The transport layer only maps it without altering semantics.
type BusinessError = httpenvelope.BusinessError

// ProblemDetails follows RFC 9457; type is fixed to about:blank and title uses the standard HTTP status phrase.
type ProblemDetails = httpenvelope.ProblemDetails

// WriteSuccess writes an HTTP 200 success envelope; data may be nil.
func WriteSuccess(c *gin.Context, data any) { httpenvelope.WriteSuccess(c, data) }

// WriteBusinessError writes an HTTP 200 business error envelope.
// Sensitive SQL, credentials, raw Provider responses, and internal stack traces must never enter Message or Meta.
func WriteBusinessError(c *gin.Context, e *BusinessError) { httpenvelope.WriteBusinessError(c, e) }

// WriteProblem writes a Problem Details response with a real HTTP error status.
// Media type is fixed to application/problem+json and carries no business err_code.
func WriteProblem(c *gin.Context, status int, detail string) {
	httpenvelope.WriteProblem(c, status, detail)
}

package httpapi

import (
	"context"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// HeaderRequestID is the sole ingress header accepted for an external Request ID.
const HeaderRequestID = "X-Request-ID"

const ginRequestIDKey = "request_id"

type ctxRequestIDKey struct{}

// RequestIDMiddleware generates or adopts a Request ID at ingress (preferring UUIDv7),
// echoes it in the response header, and injects it into both gin and request contexts.
//
// Adoption rule: accepted only when provided by upstream as a valid UUID (assuming trusted gateway);
// otherwise generated server-side, never trusting or echoing malformed values.
func RequestIDMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := newRequestID(c.GetHeader(HeaderRequestID))
		c.Set(ginRequestIDKey, id)
		c.Header(HeaderRequestID, id)
		c.Request = c.Request.WithContext(
			context.WithValue(c.Request.Context(), ctxRequestIDKey{}, id),
		)
		c.Next()
	}
}

// RequestID returns the Request ID of the current request, or empty string if middleware is absent (for test diagnostics).
func RequestID(c *gin.Context) string {
	return c.GetString(ginRequestIDKey)
}

// RequestIDFromContext reads the Request ID for non-gin callers (workers, background tasks).
func RequestIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(ctxRequestIDKey{}).(string)
	return id
}

func newRequestID(incoming string) string {
	if incoming != "" {
		if _, err := uuid.Parse(incoming); err == nil {
			return incoming
		}
	}
	if id, err := uuid.NewV7(); err == nil {
		return id.String()
	}
	return uuid.NewString()
}

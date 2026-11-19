package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/cookieY/YearningX/backend/internal/identity/session"
	"github.com/cookieY/YearningX/backend/internal/realtime"
)

const sseKeepaliveInterval = 25 * time.Second

// HandleStreamDomainEvents serves GET /api/v4/events/stream.
// Non-SSE callers receive an EventStreamHandshake JSON envelope; SSE clients receive
// a persistent user-scoped text/event-stream with 25-second keepalive pings.
func HandleStreamDomainEvents(hub *realtime.Hub) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := session.FromContext(c.Request.Context())
		if id == nil {
			WriteProblem(c, http.StatusUnauthorized, "authentication required")
			return
		}
		if !strings.Contains(strings.ToLower(c.GetHeader("Accept")), "text/event-stream") {
			WriteSuccess(c, map[string]string{"status": "ready", "transport": "sse"})
			return
		}
		if hub == nil {
			WriteProblem(c, http.StatusServiceUnavailable, "realtime hub unavailable")
			return
		}

		// Disable the server's default WriteTimeout on this long-lived SSE connection.
		_ = http.NewResponseController(c.Writer).SetWriteDeadline(time.Time{})

		c.Header("Content-Type", "text/event-stream; charset=utf-8")
		c.Header("Cache-Control", "no-store")
		c.Header("Connection", "keep-alive")
		c.Header("X-Accel-Buffering", "no")
		c.Status(http.StatusOK)

		ch, unsubscribe := hub.Subscribe(id.UserID, id.Role, id.IsBuiltinAdmin)
		defer unsubscribe()

		_, _ = fmt.Fprint(c.Writer, ": connected\n\nevent: ready\ndata: {\"status\":\"ready\",\"transport\":\"sse\"}\n\n")
		c.Writer.Flush()

		keepalive := time.NewTicker(sseKeepaliveInterval)
		defer keepalive.Stop()

		ctxDone := c.Request.Context().Done()
		for {
			select {
			case <-ctxDone:
				return
			case delivery, ok := <-ch:
				if !ok {
					return
				}
				if delivery.Resync {
					_, _ = fmt.Fprint(c.Writer, "event: resync\ndata: {\"reason\":\"buffer_overflow\"}\n\n")
					c.Writer.Flush()
					continue
				}
				payload, err := json.Marshal(delivery.Envelope)
				if err != nil {
					continue
				}
				_, _ = fmt.Fprintf(c.Writer, "id: %s\nevent: %s\ndata: %s\n\n",
					sanitizeSSEField(delivery.Envelope.ID),
					sanitizeSSEField(delivery.Envelope.Type),
					payload,
				)
				c.Writer.Flush()
			case <-keepalive.C:
				_, _ = fmt.Fprint(c.Writer, ": keepalive\n\n")
				c.Writer.Flush()
			}
		}
	}
}

func sanitizeSSEField(s string) string {
	s = strings.ReplaceAll(s, "\r", "")
	return strings.ReplaceAll(s, "\n", "")
}

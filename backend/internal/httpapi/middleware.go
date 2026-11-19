package httpapi

import (
	"net/http"
	"runtime/debug"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/cookieY/YearningX/backend/internal/platform/logging"
)

// AccessLogMiddleware emits access logs: operation uses route template to bound label cardinality,
// and field set follows required_log_fields in the observability contract.
func AccessLogMiddleware(logger *zap.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		operation := c.FullPath()
		if operation == "" {
			operation = "unmatched"
		}
		status := c.Writer.Status()
		outcome := "success"
		switch {
		case status >= 500:
			outcome = "server_error"
		case status >= 400:
			outcome = "client_error"
		}
		requestID := RequestID(c)
		logging.Access(logger, requestID, requestID, operation, c.Request.Method, outcome, time.Since(start), status)
		// S18.75-B: c.Errors attached by handlers (whether mapped to business error or 500 fallback) are
		// logged as a separate warn-level detail line -- kept out of access log fields while preserving probe root causes.
		if len(c.Errors) > 0 {
			logger.Warn("handled_error_detail",
				zap.String("request_id", requestID),
				zap.String("operation", operation),
				zap.String("errors", c.Errors.String()))
		}
	}
}

// RecoveryMiddleware catches panics: stack traces go only to logs (unexpected_internal_error_only),
// while the HTTP response returns a Problem Details payload free of internal details.
func RecoveryMiddleware(logger *zap.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if rec := recover(); rec != nil && rec != http.ErrAbortHandler {
				logger.Error("unexpected_internal_error",
					zap.String("request_id", RequestID(c)),
					zap.Any("panic", rec),
					zap.String("stack", string(debug.Stack())),
				)
				c.Abort()
				WriteProblem(c, http.StatusInternalServerError, "internal error")
			}
		}()
		c.Next()
	}
}

// NotFoundHandler returns 404 Problem Details for unmatched routes.
func NotFoundHandler(c *gin.Context) {
	WriteProblem(c, http.StatusNotFound, "route not found")
}

// MethodNotAllowedHandler returns 405 Problem Details for unsupported methods on known paths.
func MethodNotAllowedHandler(c *gin.Context) {
	WriteProblem(c, http.StatusMethodNotAllowed, "method not allowed")
}

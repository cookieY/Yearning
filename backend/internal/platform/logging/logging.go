// Package logging provides structured JSON logging built on zap.
//
// Conforms to the observability contract: one JSON event per line, required service-level fields pre-populated on the Logger,
// and sensitive values (SQL, query results, credentials, tokens, keys, ciphertexts) never written to log fields.
package logging

import (
	"io"
	"os"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/cookieY/YearningX/backend/internal/config"
	"github.com/cookieY/YearningX/backend/internal/platform/buildinfo"
)

// New constructs a JSON Logger writing to stdout with fixed service-level fields.
func New(cfg config.LoggingConfig, obs config.ObservabilityConfig) *zap.Logger {
	return NewWithSink(cfg, obs, os.Stdout)
}

// NewWithSink allows tests to substitute the output sink.
func NewWithSink(cfg config.LoggingConfig, obs config.ObservabilityConfig, sink io.Writer) *zap.Logger {
	level := zapcore.Level(0)
	switch cfg.Level {
	case "debug":
		level = zapcore.DebugLevel
	case "info":
		level = zapcore.InfoLevel
	case "warn":
		level = zapcore.WarnLevel
	case "error":
		level = zapcore.ErrorLevel
	}
	encoderConfig := zap.NewProductionEncoderConfig()
	encoderConfig.TimeKey = "timestamp"
	encoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	core := zapcore.NewCore(
		zapcore.NewJSONEncoder(encoderConfig),
		zapcore.AddSync(sink),
		level,
	)
	return zap.New(core).With(
		zap.String("service", obs.ServiceName),
		zap.String("version", buildinfo.Version),
		zap.String("deployment_id", obs.DeploymentID),
		zap.String("instance_id", obs.InstanceID),
	)
}

// Access emits a single HTTP access log entry; operation uses the route template rather than the raw path to bound cardinality.
func Access(logger *zap.Logger, requestID, correlationID, operation, method, outcome string, duration time.Duration, status int) {
	logger.Info("http_access",
		zap.String("request_id", requestID),
		zap.String("correlation_id", correlationID),
		zap.String("operation", operation),
		zap.String("method", method),
		zap.String("outcome", outcome),
		zap.Int64("duration_ms", duration.Milliseconds()),
		zap.Int("status", status),
	)
}

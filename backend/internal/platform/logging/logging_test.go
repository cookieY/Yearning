package logging

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/cookieY/YearningX/backend/internal/config"
)

func TestAccessEmitsRequiredFieldsAsJSON(t *testing.T) {
	cfg := config.Defaults()
	cfg.Observability.DeploymentID = "dep-1"
	cfg.Observability.InstanceID = "inst-1"
	var buf bytes.Buffer
	logger := NewWithSink(cfg.Logging, cfg.Observability, &buf)

	Access(logger, "0198d9d0-0ec8-749d-b5d1-0cb384c603a0", "0198d9d0-0ec8-749d-b5d1-0cb384c603a0",
		"/api/v4/health/ready", "GET", "success", 12*time.Millisecond, 200)

	var event map[string]any
	if err := json.Unmarshal(buf.Bytes(), &event); err != nil {
		t.Fatalf("log line must be JSON: %v\nline: %s", err, buf.String())
	}
	required := []string{
		"timestamp", "level", "msg", "service", "version",
		"deployment_id", "instance_id", "request_id", "correlation_id",
		"operation", "outcome", "duration_ms", "method", "status",
	}
	for _, field := range required {
		if _, ok := event[field]; !ok {
			t.Errorf("required log field %s missing in %v", field, event)
		}
	}
	if event["level"] != "info" {
		t.Errorf("access log level = %v, want info", event["level"])
	}
	if event["duration_ms"].(float64) != 12 {
		t.Errorf("duration_ms = %v, want 12", event["duration_ms"])
	}
}

func TestLevelFiltering(t *testing.T) {
	cfg := config.Defaults()
	cfg.Logging.Level = "warn"
	var buf bytes.Buffer
	logger := NewWithSink(cfg.Logging, cfg.Observability, &buf)

	logger.Debug("debug-event")
	logger.Info("info-event")
	logger.Warn("warn-event")

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 1 || !strings.Contains(lines[0], "warn-event") {
		t.Fatalf("warn level must filter debug/info: %q", buf.String())
	}
}

func TestServiceFieldsPreset(t *testing.T) {
	cfg := config.Defaults()
	cfg.Observability.DeploymentID = "dep-42"
	var buf bytes.Buffer
	logger := NewWithSink(cfg.Logging, cfg.Observability, &buf)
	logger.Info("startup")

	var event map[string]any
	if err := json.Unmarshal(buf.Bytes(), &event); err != nil {
		t.Fatalf("log line must be JSON: %v", err)
	}
	if event["service"] != "yearning" {
		t.Errorf("service = %v", event["service"])
	}
	if event["deployment_id"] != "dep-42" {
		t.Errorf("deployment_id = %v", event["deployment_id"])
	}
	if _, ok := event["version"]; !ok {
		t.Error("version field must be preset")
	}
}

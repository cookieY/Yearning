package domain

import (
	"strings"
	"testing"
)

func TestErrors_ConstructorsAndFormatting(t *testing.T) {
	var nilErr *Error
	if got := nilErr.Error(); got != "" {
		t.Fatalf("expected empty string for nil Error, got %q", got)
	}

	emptyMsgErr := &Error{Code: CodeValidationFailed, Name: "VALIDATION_FAILED"}
	if got := emptyMsgErr.Error(); got != "diagnostics error 1001 (VALIDATION_FAILED)" {
		t.Fatalf("unexpected error string without message: %q", got)
	}

	withMsgErr := ErrValidation("custom validation message")
	if !strings.Contains(withMsgErr.Error(), "custom validation message") {
		t.Fatalf("expected custom message in error string, got %q", withMsgErr.Error())
	}

	defaultValErr := ErrValidation("")
	if defaultValErr.Code != CodeValidationFailed || defaultValErr.Message == "" {
		t.Fatalf("unexpected default validation error: %+v", defaultValErr)
	}

	if err := ErrNotFound(); err.Code != CodeResourceNotFound {
		t.Fatalf("unexpected not found error: %+v", err)
	}
	if err := ErrVersionConflict(); err.Code != CodeVersionConflict {
		t.Fatalf("unexpected version conflict error: %+v", err)
	}

	if err := ErrInvalidState(""); err.Code != CodeInvalidStateTransition || err.Message == "" {
		t.Fatalf("unexpected default invalid state error: %+v", err)
	}
	if err := ErrInvalidState("custom state"); err.Message != "custom state" {
		t.Fatalf("unexpected custom invalid state error: %+v", err)
	}

	if err := ErrIdempotencyConflict(""); err.Code != CodeIdempotencyConflict || err.Message == "" {
		t.Fatalf("unexpected default idempotency conflict error: %+v", err)
	}
	if err := ErrIdempotencyConflict("custom conflict"); err.Message != "custom conflict" {
		t.Fatalf("unexpected custom idempotency conflict error: %+v", err)
	}

	if err := ErrDatasourceForbidden(""); err.Code != CodeDatasourceForbidden || err.Message == "" {
		t.Fatalf("unexpected default datasource forbidden error: %+v", err)
	}
	if err := ErrDatasourceForbidden("custom forbidden"); err.Message != "custom forbidden" {
		t.Fatalf("unexpected custom datasource forbidden error: %+v", err)
	}

	if err := ErrAnonymizationFailed(""); err.Code != CodeAnonymizationFailed || len(err.Meta) != 0 {
		t.Fatalf("unexpected empty reason anonymization error: %+v", err)
	}
	if err := ErrAnonymizationFailed("token mismatch"); err.Meta["reason"] != "token mismatch" {
		t.Fatalf("unexpected reason in anonymization error: %+v", err)
	}

	if err := ErrDatasourceDisabled(); err.Code != CodeDiagnosticsDatasourceDisabled {
		t.Fatalf("unexpected datasource disabled error: %+v", err)
	}

	if err := ErrStatsNotEnabled(""); err.Code != CodeDiagnosticsStatsNotEnabled || len(err.Meta) != 0 {
		t.Fatalf("unexpected empty detail stats not enabled error: %+v", err)
	}
	if err := ErrStatsNotEnabled("pg_stat_statements missing"); err.Meta["detail"] != "pg_stat_statements missing" {
		t.Fatalf("unexpected detail in stats not enabled error: %+v", err)
	}

	if err := ErrInsufficientDBPrivilege(""); err.Code != CodeDiagnosticsInsufficientDBPrivilege || len(err.Meta) != 0 {
		t.Fatalf("unexpected empty detail insufficient privilege error: %+v", err)
	}
	if err := ErrInsufficientDBPrivilege("permission denied"); err.Meta["detail"] != "permission denied" {
		t.Fatalf("unexpected detail in insufficient privilege error: %+v", err)
	}

	if err := ErrEngineUnsupported(""); err.Code != CodeDiagnosticsEngineUnsupported || len(err.Meta) != 0 {
		t.Fatalf("unexpected empty detail engine unsupported error: %+v", err)
	}
	if err := ErrEngineUnsupported("sqlite"); err.Meta["detail"] != "sqlite" {
		t.Fatalf("unexpected detail in engine unsupported error: %+v", err)
	}

	if err := ErrTemplateUnavailable(); err.Code != CodeDiagnosticsTemplateUnavailable {
		t.Fatalf("unexpected template unavailable error: %+v", err)
	}

	if err := ErrSnapshotRateLimited(15); err.Code != CodeDiagnosticsSnapshotRateLimited || err.Meta["retry_after_seconds"] != 15 {
		t.Fatalf("unexpected snapshot rate limited error: %+v", err)
	}

	if err := ErrCircuitPaused(""); err.Code != CodeDiagnosticsCircuitPaused || len(err.Meta) != 0 {
		t.Fatalf("unexpected empty reason circuit paused error: %+v", err)
	}
	if err := ErrCircuitPaused("timeout threshold exceeded"); err.Meta["paused_reason"] != "timeout threshold exceeded" {
		t.Fatalf("unexpected paused_reason in circuit paused error: %+v", err)
	}
}

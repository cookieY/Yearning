// Package domain defines the domain models and contract error codes for SQL Performance Diagnostics.
package domain

import "fmt"

// Contract business error codes (api/contracts/error-codes.json §7001..7008 and platform common codes).
const (
	CodeValidationFailed       = 1001
	CodeResourceNotFound       = 1002
	CodeVersionConflict        = 1003
	CodeConcurrentModification = 1004
	CodeInvalidStateTransition = 1010
	CodeIdempotencyConflict    = 1007
	CodeSystemCapacity         = 1012

	CodeAIBudgetExhausted = 2010
	CodeAIProviderFailed  = 2011

	CodeAnonymizationFailed   = 6003
	CodeSQLRestorationInvalid = 6004

	CodeDatasourceForbidden                = 7001
	CodeDiagnosticsDatasourceDisabled      = 7002
	CodeDiagnosticsEngineUnsupported       = 7003
	CodeDiagnosticsStatsNotEnabled         = 7004
	CodeDiagnosticsInsufficientDBPrivilege = 7005
	CodeDiagnosticsSnapshotRateLimited     = 7006
	CodeDiagnosticsCircuitPaused           = 7007
	CodeDiagnosticsTemplateUnavailable     = 7008
)

// Error represents a SQL Performance Diagnostics domain error carrying a contract code and error name.
type Error struct {
	Code    int
	Name    string
	Message string
	Meta    map[string]any
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Message != "" {
		return fmt.Sprintf("diagnostics error %d (%s): %s", e.Code, e.Name, e.Message)
	}
	return fmt.Sprintf("diagnostics error %d (%s)", e.Code, e.Name)
}

// BizCode implements bizerr.Coded.
func (e *Error) BizCode() int { return e.Code }

// BizName implements bizerr.Coded.
func (e *Error) BizName() string { return e.Name }

// BizMessage implements bizerr.Coded.
func (e *Error) BizMessage() string { return e.Message }

// BizMeta implements bizerr.Coded.
func (e *Error) BizMeta() map[string]any { return e.Meta }

func ErrValidation(msg string) *Error {
	if msg == "" {
		msg = "Invalid request parameters"
	}
	return &Error{Code: CodeValidationFailed, Name: "VALIDATION_FAILED", Message: msg}
}

func ErrNotFound() *Error {
	return &Error{Code: CodeResourceNotFound, Name: "RESOURCE_NOT_FOUND", Message: "Resource not found"}
}

func ErrVersionConflict() *Error {
	return &Error{Code: CodeVersionConflict, Name: "VERSION_CONFLICT", Message: "Resource version conflict; please refresh and retry"}
}

func ErrInvalidState(msg string) *Error {
	if msg == "" {
		msg = "Operation is not allowed in the current state"
	}
	return &Error{Code: CodeInvalidStateTransition, Name: "INVALID_STATE_TRANSITION", Message: msg}
}

func ErrIdempotencyConflict(msg string) *Error {
	if msg == "" {
		msg = "Idempotency key has already been used with a different request payload"
	}
	return &Error{Code: CodeIdempotencyConflict, Name: "IDEMPOTENCY_KEY_REUSED_WITH_DIFFERENT_PAYLOAD", Message: msg}
}

func ErrDatasourceForbidden(msg string) *Error {
	if msg == "" {
		msg = "Current user is not authorized to submit change orders on this datasource or workflow"
	}
	return &Error{Code: CodeDatasourceForbidden, Name: "DIAGNOSTICS_DATASOURCE_FORBIDDEN", Message: msg}
}

func ErrAnonymizationFailed(reason string) *Error {
	meta := map[string]any{}
	if reason != "" {
		meta["reason"] = reason
	}
	return &Error{
		Code:    CodeAnonymizationFailed,
		Name:    "ASSISTANT_ANONYMIZATION_FAILED",
		Message: "Unable to reliably anonymize SQL template and metadata; refused to send raw text to the model",
		Meta:    meta,
	}
}

func ErrDatasourceDisabled() *Error {
	return &Error{
		Code:    CodeDiagnosticsDatasourceDisabled,
		Name:    "DIAGNOSTICS_NOT_ENABLED",
		Message: "SQL performance diagnostics is not enabled for this datasource; enable it in datasource settings first",
	}
}

func ErrStatsNotEnabled(detail string) *Error {
	meta := map[string]any{}
	if detail != "" {
		meta["detail"] = detail
	}
	return &Error{
		Code:    CodeDiagnosticsStatsNotEnabled,
		Name:    "DIAGNOSTICS_STATS_EXTENSION_DISABLED",
		Message: "Native statement statistics (performance_schema or pg_stat_statements) are not enabled on the target database",
		Meta:    meta,
	}
}

func ErrInsufficientDBPrivilege(detail string) *Error {
	meta := map[string]any{}
	if detail != "" {
		meta["detail"] = detail
	}
	return &Error{
		Code:    CodeDiagnosticsInsufficientDBPrivilege,
		Name:    "DIAGNOSTICS_INSUFFICIENT_DB_PRIVILEGES",
		Message: "Read-only database credentials lack privileges to query system statement statistics views",
		Meta:    meta,
	}
}

func ErrEngineUnsupported(detail string) *Error {
	meta := map[string]any{}
	if detail != "" {
		meta["detail"] = detail
	}
	return &Error{
		Code:    CodeDiagnosticsEngineUnsupported,
		Name:    "DIAGNOSTICS_ENGINE_UNSUPPORTED",
		Message: "Target database engine or version does not support native statement digest statistics collection",
		Meta:    meta,
	}
}

func ErrTemplateUnavailable() *Error {
	return &Error{
		Code:    CodeDiagnosticsTemplateUnavailable,
		Name:    "DIAGNOSTICS_AI_TEMPLATE_UNAVAILABLE",
		Message: "Target database did not return a normalized SQL template for this fingerprint (evicted or restricted by privileges)",
	}
}

func ErrSnapshotRateLimited(retryAfterSeconds int) *Error {
	return &Error{
		Code:    CodeDiagnosticsSnapshotRateLimited,
		Name:    "DIAGNOSTICS_SNAPSHOT_RATE_LIMITED",
		Message: "Manual snapshot triggered too frequently; at most one snapshot per 30 seconds is allowed per datasource",
		Meta:    map[string]any{"retry_after_seconds": retryAfterSeconds},
	}
}

func ErrCircuitPaused(reason string) *Error {
	meta := map[string]any{}
	if reason != "" {
		meta["paused_reason"] = reason
	}
	return &Error{
		Code:    CodeDiagnosticsCircuitPaused,
		Name:    "DIAGNOSTICS_CIRCUIT_BREAKER_OPEN",
		Message: "Automatic diagnostics collection is paused due to consecutive timeouts or failures; restore configuration and resume",
		Meta:    meta,
	}
}

func ErrAIBudgetExhausted() *Error {
	return &Error{
		Code:    CodeAIBudgetExhausted,
		Name:    "AI_DAILY_BUDGET_EXHAUSTED",
		Message: "Daily AI budget is exhausted",
	}
}

func ErrAIProviderFailed(detail string) *Error {
	meta := map[string]any{}
	if detail != "" {
		meta["detail"] = detail
	}
	return &Error{
		Code:    CodeAIProviderFailed,
		Name:    "AI_PROVIDER_FAILED",
		Message: "No usable AI provider is configured or the AI provider request failed",
		Meta:    meta,
	}
}

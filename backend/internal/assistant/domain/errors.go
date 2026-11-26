// Package domain defines the domain types and canonical error codes for the Database AI Assistant.
package domain

import "fmt"

// Canonical business error codes (api/contracts/error-codes.json §6001..6007 and shared platform codes).
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

	CodeAssistantOutOfScope            = 6001
	CodeAssistantDatasourceForbidden   = 6002
	CodeAssistantAnonymizationFailed   = 6003
	CodeAssistantSQLRestorationInvalid = 6004
	CodeAssistantDraftRevisionStale    = 6005
	CodeAssistantContextBudgetExceeded = 6006
	CodeAssistantStreamInProgress      = 6007
)

// Error represents a domain error carrying a contract code and identifier.
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
		return fmt.Sprintf("assistant error %d (%s): %s", e.Code, e.Name, e.Message)
	}
	return fmt.Sprintf("assistant error %d (%s)", e.Code, e.Name)
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

func ErrConcurrent() *Error {
	return &Error{Code: CodeConcurrentModification, Name: "CONCURRENT_MODIFICATION", Message: "Concurrent modification conflict; please retry"}
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

func ErrCapacityExhausted(msg string) *Error {
	if msg == "" {
		msg = "Session or message quota exceeded"
	}
	return &Error{Code: CodeSystemCapacity, Name: "SYSTEM_CAPACITY_EXHAUSTED", Message: msg}
}

func ErrBudgetExhausted() *Error {
	return &Error{Code: CodeAIBudgetExhausted, Name: "AI_DAILY_BUDGET_EXHAUSTED", Message: "Daily AI budget exhausted; please try again later or contact an administrator"}
}

func ErrProviderFailed(msg string) *Error {
	if msg == "" {
		msg = "AI model service is temporarily unavailable"
	}
	return &Error{Code: CodeAIProviderFailed, Name: "AI_PROVIDER_FAILED", Message: msg}
}

func ErrOutOfScope(msg string) *Error {
	if msg == "" {
		msg = "Request is outside the Database AI Assistant scope; only Platform Usage Help, Natural Language SQL Generation, and SQL Analysis & Optimization are supported"
	}
	return &Error{Code: CodeAssistantOutOfScope, Name: "ASSISTANT_OUT_OF_SCOPE", Message: msg}
}

func ErrDatasourceForbidden() *Error {
	// Security invariant: never disclose whether a restricted datasource or object exists.
	return &Error{
		Code:    CodeAssistantDatasourceForbidden,
		Name:    "ASSISTANT_DATASOURCE_FORBIDDEN",
		Message: "Current user is not authorized to access the target datasource or read required metadata",
	}
}

func ErrAnonymizationFailed(reason string) *Error {
	meta := map[string]any{}
	if reason != "" {
		meta["reason"] = reason
	}
	return &Error{
		Code:    CodeAssistantAnonymizationFailed,
		Name:    "ASSISTANT_ANONYMIZATION_FAILED",
		Message: "Unable to reliably anonymize SQL and context; refused to send raw text to the model",
		Meta:    meta,
	}
}

func ErrSQLRestorationInvalid(reason string) *Error {
	meta := map[string]any{}
	if reason != "" {
		meta["reason"] = reason
	}
	return &Error{
		Code:    CodeAssistantSQLRestorationInvalid,
		Name:    "ASSISTANT_SQL_RESTORATION_INVALID",
		Message: "Model-generated SQL draft failed dialect parsing, object boundary, or parameter binding validation",
		Meta:    meta,
	}
}

func ErrDraftRevisionStale(reason string) *Error {
	meta := map[string]any{}
	if reason != "" {
		meta["reason"] = reason
	}
	return &Error{
		Code:    CodeAssistantDraftRevisionStale,
		Name:    "ASSISTANT_DRAFT_REVISION_STALE",
		Message: "SQL draft revision, target, or precheck state has changed; please re-validate before submitting",
		Meta:    meta,
	}
}

func ErrContextBudgetExceeded(msg string) *Error {
	if msg == "" {
		msg = "Single SQL statement or metadata context exceeds the maximum model token budget; narrow the table scope or split the SQL"
	}
	return &Error{
		Code:    CodeAssistantContextBudgetExceeded,
		Name:    "ASSISTANT_CONTEXT_BUDGET_EXCEEDED",
		Message: msg,
	}
}

func ErrStreamInProgress() *Error {
	return &Error{
		Code:    CodeAssistantStreamInProgress,
		Name:    "ASSISTANT_STREAM_IN_PROGRESS",
		Message: "An assistant reply is already streaming; please wait for completion or cancel it before sending another message",
	}
}

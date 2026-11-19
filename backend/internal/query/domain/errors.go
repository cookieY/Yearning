// errors.go defines Query domain business errors (frozen in error-codes.json 4001-4010).
package domain

import "fmt"

// Error is a domain error carrying a contract error code (mapped to BusinessError by httpapi).
// Detail is optional: caller-facing sanitized diagnostic information (such as sanitized driver failure names)
// exposed via httpapi meta.detail and recorded in event metadata; never contains credentials.
type Error struct {
	Code   int32
	Name   string
	Detail string
}

func (e *Error) Error() string { return fmt.Sprintf("query: %d %s", e.Code, e.Name) }

// WithDetail attaches sanitized diagnostic detail (chainable).
func (e *Error) WithDetail(detail string) *Error {
	e.Detail = detail
	return e
}

// Contract error codes (frozen values in error-codes.json business_errors).
const (
	CodeGrantRequired    int32 = 4001 // QUERY_GRANT_REQUIRED
	CodeDsNotGranted     int32 = 4002 // QUERY_DATASOURCE_NOT_GRANTED
	CodeExportNotGranted int32 = 4003 // QUERY_EXPORT_NOT_GRANTED
	CodeGrantRevoked     int32 = 4004 // QUERY_GRANT_REVOKED
	CodeGrantExpired     int32 = 4005 // QUERY_GRANT_EXPIRED
	CodeSessionClosed    int32 = 4006 // QUERY_SESSION_CLOSED
	CodeOnlySingleSelect int32 = 4007 // QUERY_ONLY_SINGLE_SELECT
	CodeTimeoutRange     int32 = 4008 // QUERY_TIMEOUT_OUT_OF_RANGE
	CodeQueryTimeout     int32 = 4009 // QUERY_TIMEOUT
	CodeCursorInvalid    int32 = 4010 // QUERY_CURSOR_INVALID
	// Shared error codes (1xxx range).
	CodeValidationFailed  int32 = 1001
	CodeNotFound          int32 = 1002
	CodeVersionConflict   int32 = 1003
	CodeConcurrent        int32 = 1004
	CodeIdemReused        int32 = 1007
	CodeIdemInProgress    int32 = 1008
	CodeCursorExpired     int32 = 1009
	CodeIllegalTransition int32 = 1010
	CodeSystemCapacity    int32 = 1012 // SYSTEM_CAPACITY_EXHAUSTED
	// Deterministic engine-side statement rejection (uses 2003 as the closest match per execution vocabulary gap record).
	CodeStatementRejected int32 = 2003
)

func err(code int32, name string) *Error { return &Error{Code: code, Name: name} }

// Contract error constructors (Name matches frozen names in error-codes.json).
func ErrGrantRequired() *Error    { return err(CodeGrantRequired, "QUERY_GRANT_REQUIRED") }
func ErrDsNotGranted() *Error     { return err(CodeDsNotGranted, "QUERY_DATASOURCE_NOT_GRANTED") }
func ErrExportNotGranted() *Error { return err(CodeExportNotGranted, "QUERY_EXPORT_NOT_GRANTED") }
func ErrGrantRevoked() *Error     { return err(CodeGrantRevoked, "QUERY_GRANT_REVOKED") }
func ErrGrantExpired() *Error     { return err(CodeGrantExpired, "QUERY_GRANT_EXPIRED") }
func ErrSessionClosed() *Error    { return err(CodeSessionClosed, "QUERY_SESSION_CLOSED") }
func ErrOnlySingleSelect() *Error { return err(CodeOnlySingleSelect, "QUERY_ONLY_SINGLE_SELECT") }
func ErrTimeoutRange() *Error     { return err(CodeTimeoutRange, "QUERY_TIMEOUT_OUT_OF_RANGE") }
func ErrQueryTimeout() *Error     { return err(CodeQueryTimeout, "QUERY_TIMEOUT") }
func ErrCursorInvalid() *Error    { return err(CodeCursorInvalid, "QUERY_CURSOR_INVALID") }
func ErrCursorExpired() *Error    { return err(CodeCursorExpired, "CURSOR_EXPIRED") }

// Shared error constructors.
func ErrValidation() *Error      { return err(CodeValidationFailed, "VALIDATION_FAILED") }
func ErrNotFound() *Error        { return err(CodeNotFound, "RESOURCE_NOT_FOUND") }
func ErrVersionConflict() *Error { return err(CodeVersionConflict, "VERSION_CONFLICT") }
func ErrConcurrent() *Error      { return err(CodeConcurrent, "CONCURRENT_MODIFICATION") }
func ErrIdemReused() *Error      { return err(CodeIdemReused, "IDEMPOTENCY_KEY_REUSED") }
func ErrIdemInProgress() *Error  { return err(CodeIdemInProgress, "IDEMPOTENCY_IN_PROGRESS") }
func ErrIllegal() *Error         { return err(CodeIllegalTransition, "INVALID_STATE_TRANSITION") }
func ErrSystem() *Error          { return err(CodeSystemCapacity, "SYSTEM_CAPACITY_EXHAUSTED") }

// errors.go Execution domain business errors (frozen in error-codes.json 3003-3012).
package domain

import "fmt"

// Error is a domain error carrying a contract error code (mapped to BusinessError by httpapi).
type Error struct {
	Code int32
	Name string
}

func (e *Error) Error() string { return fmt.Sprintf("execution: %d %s", e.Code, e.Name) }

// Contract error codes (frozen values in error-codes.json business_errors).
const (
	CodeAlreadyStarted   int32 = 3003 // EXECUTION_ALREADY_STARTED
	CodeRetryForbidden   int32 = 3004 // EXECUTION_RETRY_FORBIDDEN
	CodeResultUnknown    int32 = 3005 // EXECUTION_RESULT_UNKNOWN
	CodePreflightFailed  int32 = 3006 // EXECUTION_PREFLIGHT_FAILED
	CodeScheduleRange    int32 = 3007 // SCHEDULE_OUT_OF_RANGE
	CodeOutsideWindow    int32 = 3008 // OUTSIDE_EXECUTION_WINDOW
	CodeMissedSchedule   int32 = 3009 // MISSED_SCHEDULE
	CodeDatasourceDown   int32 = 3010 // DATASOURCE_UNAVAILABLE
	CodeGhOstUnsupported int32 = 3011 // GH_OST_UNSUPPORTED
	CodeEvidenceRequired int32 = 3012 // MANUAL_VERIFICATION_EVIDENCE_REQUIRED
	// Common error codes (shared 1xxx/2xxx/3xxx segments in error-codes.json).
	CodeValidationFailed  int32 = 1001
	CodeNotFound          int32 = 1002
	CodeVersionConflict   int32 = 1003
	CodeConcurrent        int32 = 1004
	CodeIdemReused        int32 = 1007
	CodeIdemInProgress    int32 = 1008
	CodeIllegalTransition int32 = 1010
	CodeSystemCapacity    int32 = 1012
	// CodeStatementRejected: deterministic rejection of a statement by the target database (row-level diagnostic;
	// vocabulary has no dedicated "engine rejection" code, so 2003 SQL_PARSE_FAILED is the closest match —
	// see vocabulary gap record in implementation-notes).
	CodeStatementRejected int32 = 2003
	CodeNotFrozenActor    int32 = 3001
)

// err constructs a domain error.
func err(code int32, name string) *Error { return &Error{Code: code, Name: name} }

// Contract error constructors (Name = frozen name in error-codes.json).
func ErrAlreadyStarted() *Error { return err(CodeAlreadyStarted, "EXECUTION_ALREADY_STARTED") }
func ErrRetryForbidden() *Error { return err(CodeRetryForbidden, "EXECUTION_RETRY_FORBIDDEN") }
func ErrResultUnknown() *Error  { return err(CodeResultUnknown, "EXECUTION_RESULT_UNKNOWN") }
func ErrPreflightFailed(reason string) *Error {
	// reason is appended after a colon (split into Meta by the handler; follows the SUBMISSION_GATE_FAILED convention).
	return err(CodePreflightFailed, "EXECUTION_PREFLIGHT_FAILED:"+reason)
}
func ErrScheduleRange() *Error    { return err(CodeScheduleRange, "SCHEDULE_OUT_OF_RANGE") }
func ErrOutsideWindow() *Error    { return err(CodeOutsideWindow, "OUTSIDE_EXECUTION_WINDOW") }
func ErrMissedSchedule() *Error   { return err(CodeMissedSchedule, "MISSED_SCHEDULE") }
func ErrDatasourceDown() *Error   { return err(CodeDatasourceDown, "DATASOURCE_UNAVAILABLE") }
func ErrGhOstUnsupported() *Error { return err(CodeGhOstUnsupported, "GH_OST_UNSUPPORTED") }
func ErrEvidenceRequired() *Error {
	return err(CodeEvidenceRequired, "MANUAL_VERIFICATION_EVIDENCE_REQUIRED")
}

// Common error constructors.
func ErrValidation() *Error      { return err(CodeValidationFailed, "VALIDATION_FAILED") }
func ErrNotFound() *Error        { return err(CodeNotFound, "RESOURCE_NOT_FOUND") }
func ErrVersionConflict() *Error { return err(CodeVersionConflict, "VERSION_CONFLICT") }
func ErrConcurrent() *Error      { return err(CodeConcurrent, "CONCURRENT_MODIFICATION") }
func ErrIdemReused() *Error      { return err(CodeIdemReused, "IDEMPOTENCY_KEY_REUSED") }
func ErrIdemInProgress() *Error  { return err(CodeIdemInProgress, "IDEMPOTENCY_IN_PROGRESS") }
func ErrIllegal() *Error         { return err(CodeIllegalTransition, "INVALID_STATE_TRANSITION") }
func ErrNotFrozen() *Error       { return err(CodeNotFrozenActor, "NOT_FROZEN_ACTOR") }
func ErrSystemCapacity() *Error  { return err(CodeSystemCapacity, "SYSTEM_CAPACITY_EXHAUSTED") }

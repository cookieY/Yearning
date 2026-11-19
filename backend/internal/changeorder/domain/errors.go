// errors.go ChangeOrder domain business error codes (error-codes.json change_order/
// submission domain + common). The HTTP layer maps these to HTTP 200 + err_code per business result conventions.
package domain

// Error is a domain error carrying a numeric business code.
type Error struct {
	Code int32
	Name string
}

func (e *Error) Error() string { return "changeorder: " + e.Name }

// Contract error codes (error-codes.json).
const (
	CodeValidationFailed     int32 = 1001
	CodeResourceNotFound     int32 = 1002
	CodeVersionConflict      int32 = 1003
	CodeConcurrent           int32 = 1004
	CodeResourceExists       int32 = 1005
	CodeIdemKeyReused        int32 = 1007
	CodeIdemInProgress       int32 = 1008
	CodeIllegalTransition    int32 = 1010
	CodeReviewOutdated       int32 = 2001
	CodeSubmissionGateFailed int32 = 2013
	CodeFlowGrantRevoked     int32 = 2014
	CodeNotFrozenActor       int32 = 3001
	CodeApprovalDecided      int32 = 3002
)

// Predefined domain error constructors.
func ErrValidation() *Error { return &Error{Code: CodeValidationFailed, Name: "VALIDATION_FAILED"} }
func ErrNotFound() *Error   { return &Error{Code: CodeResourceNotFound, Name: "RESOURCE_NOT_FOUND"} }
func ErrVersionConflict() *Error {
	return &Error{Code: CodeVersionConflict, Name: "VERSION_CONFLICT"}
}
func ErrConcurrent() *Error { return &Error{Code: CodeConcurrent, Name: "CONCURRENT_MODIFICATION"} }
func ErrIdemReused() *Error { return &Error{Code: CodeIdemKeyReused, Name: "IDEMPOTENCY_KEY_REUSED"} }
func ErrIdemInProgress() *Error {
	return &Error{Code: CodeIdemInProgress, Name: "IDEMPOTENCY_IN_PROGRESS"}
}
func ErrIllegal() *Error {
	return &Error{Code: CodeIllegalTransition, Name: "INVALID_STATE_TRANSITION"}
}
func ErrReviewOutdated() *Error {
	return &Error{Code: CodeReviewOutdated, Name: "REVIEW_OUTDATED"}
}
func ErrSubmissionGate(reason string) *Error {
	return &Error{Code: CodeSubmissionGateFailed, Name: "SUBMISSION_GATE_FAILED:" + reason}
}
func ErrFlowGrantRevoked() *Error {
	return &Error{Code: CodeFlowGrantRevoked, Name: "FLOW_GRANT_REVOKED"}
}
func ErrNotFrozenActor() *Error {
	return &Error{Code: CodeNotFrozenActor, Name: "NOT_FROZEN_ACTOR"}
}
func ErrApprovalDecided() *Error {
	return &Error{Code: CodeApprovalDecided, Name: "APPROVAL_ALREADY_DECIDED"}
}

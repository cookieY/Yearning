// errors.go defines Review-domain business error codes (error-codes.json domain=review,
// 2001-2012). Command paths may also return common/management codes (1001/1002/1010/1012/2014,
// etc.), constructed as domain.Error instances by the application layer as needed.
package domain

import "errors"

// Error carries a numeric business error code and stable name.
// Detail is an optional operator-facing snapshot of upstream rejection causes (free of secrets and raw SQL).
// Fields carries optional field-level rejection causes for VALIDATION_FAILED (snake_case field -> messages).
type Error struct {
	Code   int
	Name   string
	Detail string
	Fields map[string][]string
}

func (e *Error) Error() string { return "review: " + e.Name }

// Review-domain business error codes matching api/contracts/error-codes.json.
const (
	CodeReviewOutdated         = 2001
	CodeSQLBatchTooLarge       = 2002
	CodeSQLParseFailed         = 2003
	CodeSQLFingerprintFailed   = 2004
	CodeSQLAnonymizationFailed = 2005
	CodeTooManyFingerprints    = 2006
	CodeReviewCoverageIncomp   = 2007
	CodeReviewBlockedByRisk    = 2008
	CodeReviewSchemaInvalid    = 2009
	CodeAIBudgetExhausted      = 2010
	CodeAIProviderFailed       = 2011
	CodeReviewToolFailed       = 2012
)

// Sentinel review-domain errors mapped from SQL fingerprinting, anonymization, runtime gates, and tool calls.
var (
	ErrReviewOutdated      = &Error{Code: CodeReviewOutdated, Name: "REVIEW_OUTDATED"}
	ErrBatchTooLarge       = &Error{Code: CodeSQLBatchTooLarge, Name: "SQL_BATCH_TOO_LARGE"}
	ErrParseFailed         = &Error{Code: CodeSQLParseFailed, Name: "SQL_PARSE_FAILED"}
	ErrFingerprintFailed   = &Error{Code: CodeSQLFingerprintFailed, Name: "SQL_FINGERPRINT_FAILED"}
	ErrAnonymizationFailed = &Error{Code: CodeSQLAnonymizationFailed, Name: "SQL_ANONYMIZATION_FAILED"}
	ErrTooManyFingerprints = &Error{Code: CodeTooManyFingerprints, Name: "TOO_MANY_FINGERPRINTS"}
	ErrCoverageIncomplete  = &Error{Code: CodeReviewCoverageIncomp, Name: "REVIEW_COVERAGE_INCOMPLETE"}
	ErrBlockedByRisk       = &Error{Code: CodeReviewBlockedByRisk, Name: "REVIEW_BLOCKED_BY_RISK"}
	ErrSchemaInvalid       = &Error{Code: CodeReviewSchemaInvalid, Name: "REVIEW_SCHEMA_INVALID"}
	ErrToolFailed          = &Error{Code: CodeReviewToolFailed, Name: "REVIEW_TOOL_FAILED"}
)

// IsCode reports whether err unwraps to a review domain *Error with the given numeric code.
func IsCode(err error, code int) bool {
	var de *Error
	if errors.As(err, &de) {
		return de.Code == code
	}
	return false
}

// FailureFromFingerprintCode maps a fingerprint failure identifier to the corresponding Review *Error.
func FailureFromFingerprintCode(name string) *Error {
	switch name {
	case "SQL_BATCH_TOO_LARGE":
		return ErrBatchTooLarge
	case "SQL_PARSE_FAILED":
		return ErrParseFailed
	case "SQL_ANONYMIZATION_FAILED":
		return ErrAnonymizationFailed
	case "TOO_MANY_FINGERPRINTS":
		return ErrTooManyFingerprints
	default:
		return ErrFingerprintFailed
	}
}

// TestCallRecordCarriesParamsAndSendTime S008 section 5: call record carries the three-parameter
// snapshot (zero value = omitted) and send timestamp.
package domain

import "errors"

// TestSchemaFailedCallKeepsReportedUsage Usage-02: when output validation fails on a response
// that already reported usage, the usage is preserved in the call record (marked schema_failed, not free).
type Error struct {
	Code   int
	Name   string
	Detail string
	Fields map[string][]string
}

func (e *Error) Error() string { return "review: " + e.Name }

// Two call records (initial + schema repair), both carrying the reported usage.
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

// TestRetryAttemptsEachRecorded Price-03 prerequisite: each real HTTP attempt during backoff retry
// produces its own record (two 429s followed by success -> 3 records).
var (
	ErrReviewOutdated = &Error{Code: CodeReviewOutdated, Name: "REVIEW_OUTDATED"}
	// openai_mode_test.go section 18.76-A: output mode negotiation and sending with persisted mode;
	// section 18.76-B: GET /models model catalog fetch.
	ErrBatchTooLarge = &Error{Code: CodeSQLBatchTooLarge, Name: "SQL_BATCH_TOO_LARGE"}
	// requestMode parses response_format.type and the system message from the request body.
	ErrParseFailed = &Error{Code: CodeSQLParseFailed, Name: "SQL_PARSE_FAILED"}
	// json_object replay must carry both the schema text and the json keyword.
	ErrFingerprintFailed = &Error{Code: CodeSQLFingerprintFailed, Name: "SQL_FINGERPRINT_FAILED"}
	// openai_privacy_test.go SEC-0002/SEC-0038 regression:
	//   - Under both structured output modes (json_schema strict and json_object),
	//     Provider request bytes carry only anonymous tokens and never real database/table/column/index names;
	//   - Structured output parsing rejects duplicate JSON keys and trailing content.
	ErrAnonymizationFailed = &Error{Code: CodeSQLAnonymizationFailed, Name: "SQL_ANONYMIZATION_FAILED"}
	// tokenizedEvidenceRequest constructs a review request with tokenized facts already in place
	// (the shape produced by AgentRunner after tokenizeToolFact).
	ErrTooManyFingerprints = &Error{Code: CodeTooManyFingerprints, Name: "TOO_MANY_FINGERPRINTS"}
	// realIdentifiers are real identifiers from the test database (must never appear in request bytes).
	ErrCoverageIncomplete = &Error{Code: CodeReviewCoverageIncomp, Name: "REVIEW_COVERAGE_INCOMPLETE"}
	// TestSEC0002RequestBytesTokenOnlyBothModes runs the same tokenized request through the real HTTP
	// serialization path of both json_schema and json_object modes, verifying request bytes contain tokens
	// and zero real identifiers.
	ErrBlockedByRisk = &Error{Code: CodeReviewBlockedByRisk, Name: "REVIEW_BLOCKED_BY_RISK"}
	// strictBody builds a valid single-value response (with custom findings/tool_requests embeddable).
	ErrSchemaInvalid = &Error{Code: CodeReviewSchemaInvalid, Name: "REVIEW_SCHEMA_INVALID"}
	// TestSEC0038RejectsDuplicateKeys duplicate keys (top-level or nested) must be rejected.
	ErrToolFailed = &Error{Code: CodeReviewToolFailed, Name: "REVIEW_TOOL_FAILED"}
)

// TestSEC0038RejectsTrailingContent a second JSON value or trailing garbage bytes after the first value
// must be rejected.
func IsCode(err error, code int) bool {
	var de *Error
	if errors.As(err, &de) {
		return de.Code == code
	}
	return false
}

// Trailing whitespace is valid.
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

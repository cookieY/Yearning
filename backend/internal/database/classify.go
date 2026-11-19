// classify.go Driver error classification and sanitization.
// Raw driver text (which may contain SQL fragments, DSNs, or host details) is used only for internal classification;
// all errors leaving this package use a stable FailureName without carrying raw text.
package database

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
)

// FailureName is a stable sanitized failure name (entering statement_result/connection_test).
const (
	FailParseError          = "parse_error"
	FailDataTypeMismatch    = "data_type_mismatch"
	FailUndefinedTable      = "undefined_table"
	FailUndefinedColumn     = "undefined_column"
	FailUniqueViolation     = "unique_violation"
	FailForeignKeyViolation = "foreign_key_violation"
	FailNotNullViolation    = "not_null_violation"
	FailObjectExists        = "object_already_exists"
	FailInsufficientPriv    = "insufficient_privilege"
	FailAuthentication      = "authentication_failed"
	FailConnectionRefused   = "connection_failed"
	FailCursorExpired       = "cursor_expired"
	FailQueryCancelled      = "query_cancelled"
	FailStatementTimeout    = "statement_timeout"
	FailDriverError         = "driver_error"
	FailUnsupportedTarget   = "capability_target_unsupported"
)

// Classified holds the classification outcome of an error.
type Classified struct {
	Class       EngineCodeClass
	FailureName string
}

// ClassifyError classifies an error by engine. When engine is "", only generic context classification is performed.
// Returned error text is sanitized; returns success when err is nil.
func ClassifyError(engine string, err error) Classified {
	if err == nil {
		return Classified{Class: ClassSuccess, FailureName: ""}
	}
	if errors.Is(err, ErrCursorExpired) {
		return Classified{Class: ClassUnknown, FailureName: FailCursorExpired}
	}
	if errors.Is(err, context.Canceled) {
		return Classified{Class: ClassCancelled, FailureName: FailQueryCancelled}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return Classified{Class: ClassTimeout, FailureName: FailStatementTimeout}
	}
	if engine == "mysql" {
		return classifyMySQL(err)
	}
	if engine == "postgresql" {
		return classifyPostgres(err)
	}
	return Classified{Class: ClassUnknown, FailureName: FailDriverError}
}

func classifyMySQL(err error) Classified {
	var me *mysql.MySQLError
	if errors.As(err, &me) {
		switch me.Number {
		case 1064, 1149, 1235:
			return Classified{Class: ClassSyntaxError, FailureName: FailParseError}
		case 1264, 1292, 1366:
			return Classified{Class: ClassSyntaxError, FailureName: FailDataTypeMismatch}
		case 1146:
			return Classified{Class: ClassNotFound, FailureName: FailUndefinedTable}
		case 1054, 1091, 1051:
			// 1054 unknown column / 1091 key or column does not exist / 1051 unknown database — deterministic absence.
			return Classified{Class: ClassNotFound, FailureName: FailUndefinedColumn}
		case 1062:
			return Classified{Class: ClassConstraintViolation, FailureName: FailUniqueViolation}
		case 1050, 1060:
			// 1050 table already exists / 1060 duplicate column — deterministic object conflict.
			return Classified{Class: ClassConstraintViolation, FailureName: FailObjectExists}
		case 1451, 1452, 1216, 1217:
			return Classified{Class: ClassConstraintViolation, FailureName: FailForeignKeyViolation}
		case 1048, 1566:
			return Classified{Class: ClassConstraintViolation, FailureName: FailNotNullViolation}
		case 1142, 1143, 1370, 1227:
			return Classified{Class: ClassInsufficientPriv, FailureName: FailInsufficientPriv}
		case 1044, 1045:
			return Classified{Class: ClassConnectionFailure, FailureName: FailAuthentication}
		case 1317:
			return Classified{Class: ClassCancelled, FailureName: FailQueryCancelled}
		case 3024, 1969:
			return Classified{Class: ClassTimeout, FailureName: FailStatementTimeout}
		}
		return Classified{Class: ClassUnknown, FailureName: FailDriverError}
	}
	if errors.Is(err, mysql.ErrInvalidConn) || errors.Is(err, mysql.ErrMalformPkt) {
		return Classified{Class: ClassConnectionFailure, FailureName: FailConnectionRefused}
	}
	return Classified{Class: ClassUnknown, FailureName: FailDriverError}
}

func classifyPostgres(err error) Classified {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		switch pe.Code {
		case "42601":
			return Classified{Class: ClassSyntaxError, FailureName: FailParseError}
		case "22P02", "42883", "42804", "22001", "22003", "22007", "22008":
			return Classified{Class: ClassSyntaxError, FailureName: FailDataTypeMismatch}
		case "42P01":
			return Classified{Class: ClassNotFound, FailureName: FailUndefinedTable}
		case "42703":
			return Classified{Class: ClassNotFound, FailureName: FailUndefinedColumn}
		case "23505":
			return Classified{Class: ClassConstraintViolation, FailureName: FailUniqueViolation}
		case "42P07", "42710":
			return Classified{Class: ClassConstraintViolation, FailureName: FailObjectExists}
		case "23503":
			return Classified{Class: ClassConstraintViolation, FailureName: FailForeignKeyViolation}
		case "23502":
			return Classified{Class: ClassConstraintViolation, FailureName: FailNotNullViolation}
		case "42501", "42000":
			return Classified{Class: ClassInsufficientPriv, FailureName: FailInsufficientPriv}
		case "28P01", "28000":
			return Classified{Class: ClassConnectionFailure, FailureName: FailAuthentication}
		case "08000", "08003", "08006", "08001", "08004":
			return Classified{Class: ClassConnectionFailure, FailureName: FailConnectionRefused}
		case "57014":
			// 57014 covers both user cancellation and statement timeout; distinguished by internal text without leaking outside.
			if strings.Contains(pe.Message, "statement timeout") || strings.Contains(pe.Message, "lock timeout") {
				return Classified{Class: ClassTimeout, FailureName: FailStatementTimeout}
			}
			return Classified{Class: ClassCancelled, FailureName: FailQueryCancelled}
		}
		return Classified{Class: ClassUnknown, FailureName: FailDriverError}
	}
	var ce *pgconn.ConnectError
	if errors.As(err, &ce) {
		return Classified{Class: ClassConnectionFailure, FailureName: FailConnectionRefused}
	}
	return Classified{Class: ClassUnknown, FailureName: FailDriverError}
}

// SanitizedMessage returns a sanitized error (excluding raw driver text and SQL).
func (c Classified) SanitizedMessage() string {
	if c.FailureName == "" {
		return "database: no failure"
	}
	return fmt.Sprintf("database: %s", c.FailureName)
}

// SanitizedError is a sanitized error carrying a stable FailureName (SEC-0030):
// Error() outputs only the classification name — raw driver text (host/port/user/table name/SQL fragment)
// never leaves into responses, meta, or logs; Raw retains the underlying error solely to preserve the
// errors.Is/As chain (relied upon for timeout/cancellation detection) and never participates in string formatting.
type SanitizedError struct {
	FailureName string
	Raw         error
}

func (e *SanitizedError) Error() string {
	return "database: " + e.FailureName
}

func (e *SanitizedError) Unwrap() error { return e.Raw }

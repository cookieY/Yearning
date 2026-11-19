package database

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestClassifyMySQL(t *testing.T) {
	cases := []struct {
		err  error
		want EngineCodeClass
		name string
	}{
		{&mysql.MySQLError{Number: 1064}, ClassSyntaxError, FailParseError},
		{&mysql.MySQLError{Number: 1292}, ClassSyntaxError, FailDataTypeMismatch},
		{&mysql.MySQLError{Number: 1366}, ClassSyntaxError, FailDataTypeMismatch},
		{&mysql.MySQLError{Number: 1264}, ClassSyntaxError, FailDataTypeMismatch},
		{&mysql.MySQLError{Number: 1146}, ClassNotFound, FailUndefinedTable},
		{&mysql.MySQLError{Number: 1054}, ClassNotFound, FailUndefinedColumn},
		{&mysql.MySQLError{Number: 1062}, ClassConstraintViolation, FailUniqueViolation},
		{&mysql.MySQLError{Number: 1050}, ClassConstraintViolation, FailObjectExists},
		{&mysql.MySQLError{Number: 1142}, ClassInsufficientPriv, FailInsufficientPriv},
		{&mysql.MySQLError{Number: 1045}, ClassConnectionFailure, FailAuthentication},
		{&mysql.MySQLError{Number: 1317}, ClassCancelled, FailQueryCancelled},
		{mysql.ErrInvalidConn, ClassConnectionFailure, FailConnectionRefused},
		{errors.New("whatever driver said including sql text"), ClassUnknown, FailDriverError},
	}
	for _, c := range cases {
		got := ClassifyError("mysql", c.err)
		if got.Class != c.want || got.FailureName != c.name {
			t.Fatalf("mysql classify: want %s/%s got %s/%s", c.want, c.name, got.Class, got.FailureName)
		}
		if msg := got.SanitizedMessage(); strings.Contains(msg, "driver said") || strings.Contains(msg, "sql text") {
			t.Fatalf("sanitized message leaks driver text: %q", msg)
		}
	}
}

func TestClassifyPostgres(t *testing.T) {
	cases := []struct {
		err  error
		want EngineCodeClass
		name string
	}{
		{&pgconn.PgError{Code: "42601"}, ClassSyntaxError, FailParseError},
		{&pgconn.PgError{Code: "22P02"}, ClassSyntaxError, FailDataTypeMismatch},
		{&pgconn.PgError{Code: "42883"}, ClassSyntaxError, FailDataTypeMismatch},
		{&pgconn.PgError{Code: "42804"}, ClassSyntaxError, FailDataTypeMismatch},
		{&pgconn.PgError{Code: "22003"}, ClassSyntaxError, FailDataTypeMismatch},
		{&pgconn.PgError{Code: "22007"}, ClassSyntaxError, FailDataTypeMismatch},
		{&pgconn.PgError{Code: "42P01"}, ClassNotFound, FailUndefinedTable},
		{&pgconn.PgError{Code: "42703"}, ClassNotFound, FailUndefinedColumn},
		{&pgconn.PgError{Code: "42P07"}, ClassConstraintViolation, FailObjectExists},
		{&pgconn.PgError{Code: "23505"}, ClassConstraintViolation, FailUniqueViolation},
		{&pgconn.PgError{Code: "42501"}, ClassInsufficientPriv, FailInsufficientPriv},
		{&pgconn.PgError{Code: "57014", Message: "canceling statement due to statement timeout"}, ClassTimeout, FailStatementTimeout},
		{&pgconn.PgError{Code: "57014", Message: "canceling statement due to user request"}, ClassCancelled, FailQueryCancelled},
		{&pgconn.ConnectError{}, ClassConnectionFailure, FailConnectionRefused},
	}
	for _, c := range cases {
		got := ClassifyError("postgresql", c.err)
		if got.Class != c.want || got.FailureName != c.name {
			t.Fatalf("pg classify: want %s/%s got %s/%s", c.want, c.name, got.Class, got.FailureName)
		}
	}
}

func TestClassifyContext(t *testing.T) {
	if got := ClassifyError("", context.Canceled); got.Class != ClassCancelled {
		t.Fatalf("cancel class: %s", got.Class)
	}
	if got := ClassifyError("", context.DeadlineExceeded); got.Class != ClassTimeout {
		t.Fatalf("deadline class: %s", got.Class)
	}
	if got := ClassifyError("", nil); got.Class != ClassSuccess {
		t.Fatalf("nil class: %s", got.Class)
	}
}

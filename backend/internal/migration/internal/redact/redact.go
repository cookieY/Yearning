// Package redact strips sensitive PostgreSQL driver details (Message/Detail/Hint/Where)
// from persisted migration and schema ledger error strings (SEC-0048).
package redact

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

// ErrorWithFallback formats `*pgconn.PgError` (including wrapped errors) as
// `"postgres error " + pgErr.Code` so row values and SQL fragments never leak,
// and delegates non-PostgreSQL errors to fallback.
func ErrorWithFallback(err error, fallback func(error) string) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return "postgres error " + pgErr.Code
	}
	return fallback(err)
}

// BoundedError formats PostgreSQL driver errors via SQLSTATE and truncates other
// error messages at maxBytes.
func BoundedError(err error, maxBytes int) string {
	return ErrorWithFallback(err, func(e error) string {
		msg := e.Error()
		if maxBytes > 0 && len(msg) > maxBytes {
			return msg[:maxBytes]
		}
		return msg
	})
}

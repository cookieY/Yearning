// Package txutil provides shared PostgreSQL transaction retry helpers, SQLSTATE classifiers,
// and encrypted idempotency storage shared across domain application services.
package txutil

import (
	"context"
	"database/sql"
	"errors"
	"math/rand"
	"regexp"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	sqlstate23505Re = regexp.MustCompile(`(?i)\bSQLSTATE\s+23505\b`)
	mysql1062Re     = regexp.MustCompile(`(?i)\bError\s+1062\s*\(23000\)`)
)

// ForUpdate returns the canonical PostgreSQL FOR UPDATE row-locking clause.
func ForUpdate() clause.Locking {
	return clause.Locking{Strength: "UPDATE"}
}

// Serializable runs fn inside a PostgreSQL SERIALIZABLE transaction and retries transient
// serialization or deadlock failures (SQLSTATE 40001 / 40P01) up to 3 attempts with jittered backoff.
func Serializable(ctx context.Context, db *gorm.DB, fn func(tx *gorm.DB) error) error {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if err := ctx.Err(); err != nil {
			if lastErr != nil {
				return lastErr
			}
			return err
		}
		err := db.WithContext(ctx).Transaction(fn, &sql.TxOptions{Isolation: sql.LevelSerializable})
		if err == nil {
			return nil
		}
		if !IsSerializationFailure(err) {
			return err
		}
		lastErr = err
		if attempt < 2 {
			delay := time.Duration(20+rand.Intn(40)) * time.Millisecond
			select {
			case <-ctx.Done():
				return lastErr
			case <-time.After(delay):
			}
		}
	}
	return lastErr
}

// IsSerializationFailure reports whether err is a safely retryable serialization or deadlock SQLSTATE (40001 / 40P01).
func IsSerializationFailure(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "40001" || pgErr.Code == "40P01"
	}
	return false
}

// IsUniqueViolation reports whether err represents a unique constraint violation
// (PostgreSQL SQLSTATE 23505, MySQL error 1062, gorm.ErrDuplicatedKey, or an explicit SQLSTATE 23505 boundary).
func IsUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}
	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) {
		return mysqlErr.Number == 1062
	}
	msg := err.Error()
	return sqlstate23505Re.MatchString(msg) || mysql1062Re.MatchString(msg)
}

// UniqueViolationConstraint extracts the constraint/index name from a PostgreSQL 23505 or MySQL 1062 error.
// Returns an empty string when the underlying driver error does not carry a constraint/key name.
func UniqueViolationConstraint(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return pgErr.ConstraintName
	}
	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) && mysqlErr.Number == 1062 {
		const marker = "for key '"
		if i := strings.Index(mysqlErr.Message, marker); i >= 0 {
			rest := mysqlErr.Message[i+len(marker):]
			if j := strings.Index(rest, "'"); j >= 0 {
				return rest[:j]
			}
		}
	}
	return ""
}

// IsCheckViolation reports whether err is a PostgreSQL CHECK constraint violation (SQLSTATE 23514).
func IsCheckViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23514"
}

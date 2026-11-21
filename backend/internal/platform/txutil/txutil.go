// Package txutil provides shared PostgreSQL transaction retry helpers, SQLSTATE classifiers,
// and encrypted idempotency storage shared across domain application services.
package txutil

import (
	"context"
	"database/sql"
	"errors"
	"math/rand"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

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

// IsUniqueViolation reports whether err represents a unique constraint violation (SQLSTATE 23505 / gorm.ErrDuplicatedKey).
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
	msg := err.Error()
	return strings.Contains(msg, "SQLSTATE 23505") || strings.Contains(msg, "23505")
}

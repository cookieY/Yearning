package schema

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// TestRedactErrorDropsDriverMessage SEC-0048 regression: raw PostgreSQL error text
// (where Message may contain row values such as `Key (username)=(alice)` on duplicate key) must never
// enter persisted reports, retaining only the SQLSTATE category.
func TestRedactErrorDropsDriverMessage(t *testing.T) {
	pgErr := &pgconn.PgError{
		Code:    "23505",
		Message: `duplicate key value violates unique constraint "users_username_key" Key (username)=(alice) already exists`,
		Detail:  "Failing row contains (..., alice, ...)",
	}
	got := redactError(pgErr)
	if got != "postgres error 23505" {
		t.Fatalf("redactError = %q, want postgres error 23505", got)
	}
	for _, leak := range []string{"alice", "duplicate", "users_username_key", "Failing"} {
		if strings.Contains(got, leak) {
			t.Fatalf("redacted error leaked %q", leak)
		}
	}
	// Wrapped errors must be sanitized identically (unwrapped via errors.As).
	wrapped := fmt.Errorf("apply version 4: %w", errors.Join(errors.New("boom"), pgErr))
	got2 := redactError(wrapped)
	if got2 != "postgres error 23505" {
		t.Fatalf("wrapped pg error redaction = %q", got2)
	}
	if !strings.Contains(redactError(errors.New("plain io failure")), "internal_failure") {
		t.Fatalf("non-pg error must map to stable category, got %q", redactError(errors.New("plain io failure")))
	}
}

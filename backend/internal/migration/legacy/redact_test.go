package legacy

import (
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// TestRedactErrorDropsDriverMessage SEC-0048 regression: migration failure reports must never contain
// raw PostgreSQL driver messages (which may leak row values or SQL fragments), retaining only the SQLSTATE category.
func TestRedactErrorDropsDriverMessage(t *testing.T) {
	pgErr := &pgconn.PgError{
		Code:    "23505",
		Message: `duplicate key value violates unique constraint "users_username_key" Key (username)=(alice) already exists`,
	}
	got := redactError(pgErr)
	if got != "postgres error 23505" {
		t.Fatalf("redactError = %q, want postgres error 23505", got)
	}
	for _, leak := range []string{"alice", "duplicate", "username"} {
		if strings.Contains(got, leak) {
			t.Fatalf("redacted error leaked %q", leak)
		}
	}
	wrapped := errors.Join(errors.New("write target"), pgErr)
	if redactError(wrapped) != "postgres error 23505" {
		t.Fatalf("wrapped pg error must be classified by SQLSTATE, got %q", redactError(wrapped))
	}
	if got := redactError(errors.New("own contextual failure text")); got == "" || strings.Contains(got, "postgres") {
		t.Fatalf("non-pg error should keep bounded own text, got %q", got)
	}
}

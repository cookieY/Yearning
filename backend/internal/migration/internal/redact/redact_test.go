package redact

import (
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestRedactPGAndFallbacks(t *testing.T) {
	pgErr := &pgconn.PgError{
		Code:    "23505",
		Message: `Key (username)=(alice) already exists`,
		Detail:  "Failing row contains (alice)",
	}
	if got := BoundedError(errors.Join(errors.New("outer"), pgErr), 200); got != "postgres error 23505" {
		t.Fatalf("BoundedError(pgErr) = %q, want postgres error 23505", got)
	}
	long := strings.Repeat("x", 250)
	if got := BoundedError(errors.New(long), 200); len(got) != 200 {
		t.Fatalf("len(BoundedError) = %d, want 200", len(got))
	}
	if got := ErrorWithFallback(errors.New("io boom"), func(error) string { return "internal_failure" }); got != "internal_failure" {
		t.Fatalf("ErrorWithFallback = %q, want internal_failure", got)
	}
}

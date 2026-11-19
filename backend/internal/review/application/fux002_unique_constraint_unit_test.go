package application

import (
	"errors"
	"testing"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
)

// TestUniqueViolationConstraintParsing F-UX-002 constraint name extraction: PG uses
// ConstraintName, MySQL extracts the key name from 1062 messages, and non-unique errors return an empty string;
// providerUniqueConflictDetail degrades to listing both axes when the constraint name cannot be determined.
func TestUniqueViolationConstraintParsing(t *testing.T) {
	pgErr := &pgconn.PgError{Code: "23505", ConstraintName: "ai_providers_selection_priority_key"}
	if got := uniqueViolationConstraint(pgErr); got != "ai_providers_selection_priority_key" {
		t.Fatalf("pg arm: %q", got)
	}
	myErr := &mysql.MySQLError{Number: 1062,
		Message: "Duplicate entry '1' for key 'ai_providers.selection_priority_key'"}
	if got := uniqueViolationConstraint(myErr); got != "ai_providers.selection_priority_key" {
		t.Fatalf("mysql arm: %q", got)
	}
	if got := uniqueViolationConstraint(errors.New("boom")); got != "" {
		t.Fatalf("non-unique arm: %q", got)
	}
	otherPg := &pgconn.PgError{Code: "23502", ConstraintName: "x"}
	if got := uniqueViolationConstraint(otherPg); got != "" {
		t.Fatalf("non-23505 arm: %q", got)
	}

	if got := providerUniqueConflictDetail(pgErr, 7); got != "AI Provider selection_priority=7 is already used by another AI Provider" {
		t.Fatalf("priority detail: %q", got)
	}
	if got := providerUniqueConflictDetail(&pgconn.PgError{Code: "23505", ConstraintName: "ai_providers_name_key"}, 7); got != "name is already used by another AI Provider" {
		t.Fatalf("name detail: %q", got)
	}
	if got := providerUniqueConflictDetail(errors.New("boom"), 7); got != "name or selection_priority is already used by another AI Provider" {
		t.Fatalf("fallback detail: %q", got)
	}
}

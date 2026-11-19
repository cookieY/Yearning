// evidence_copy_test.go SEC-0035 unit-testable portion:
// verifyEvidenceRevealOwnership fails closed on an empty revealID (rejected before touching the DB).
// The SQL ownership query relies on PG-specific jsonb operators (->>) on audit_events and cannot be
// expressed against a fake/in-memory DB — the full ownership matrix is covered by the integration test
// in security_regression_integration_test.go (real PostgreSQL).
package application

import (
	"context"
	"errors"
	"testing"
)

// TestSEC0035EmptySourceRevealIDFailsClosed an empty revealID is rejected before querying the DB
// (ErrNotFound -> 1002, without leaking existence); zero-value Services proves this branch requires no DB connection.
func TestSEC0035EmptySourceRevealIDFailsClosed(t *testing.T) {
	s := &Services{}
	if err := s.verifyEvidenceRevealOwnership(context.Background(),
		"user-1", "00000000-0000-4000-8000-000000000001", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("empty reveal id want ErrNotFound, got %v", err)
	}
}

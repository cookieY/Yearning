// retention_test.go NEW-QUERY-02 regression: a single-table failure in RunOnce does not return early —
// the failed table is recorded in the aggregated error while the remaining managed tables continue to be
// cleaned up (partial success must still sweep all remaining tables).
// See retention_grants_integration_test.go (-tags integration) for the live PG path.
package application

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// stubSweeper records call order and returns preset results per table (tableSweeper stub).
type stubSweeper struct {
	order   []string
	deleted map[string]int64
	errs    map[string]error
}

func (s *stubSweeper) DeleteExpired(_ context.Context, table string) (int64, error) {
	s.order = append(s.order, table)
	return s.deleted[table], s.errs[table]
}

// TestRetentionManagedTablesMatchTargetColumns guard: the managed table list and the column mapping of
// DeleteExpired (RetentionTargets) must never drift — any newly added managed table must enter both the
// list and the map (the list governs RunOnce scope, while the map governs the expiration column for DELETE).
func TestRetentionManagedTablesMatchTargetColumns(t *testing.T) {
	tables := managedRetentionTables()
	if len(tables) != len(RetentionTargets) {
		t.Fatalf("managed list (%d) and RetentionTargets (%d) diverge", len(tables), len(RetentionTargets))
	}
	for _, table := range tables {
		if _, ok := RetentionTargets[table]; !ok {
			t.Errorf("table %q in managed list but missing from RetentionTargets", table)
		}
	}
}

func TestRetentionSweepAllManagedTables(t *testing.T) {
	stub := &stubSweeper{deleted: map[string]int64{
		"evidence_raw_payloads": 3,
		"idempotency_records":   0,
		"audit_events":          7,
		"query_cursors":         1,
	}}
	counts, err := runRetentionSweep(context.Background(), stub, managedRetentionTables())
	if err != nil {
		t.Fatalf("all tables succeeded, unexpected error: %v", err)
	}
	for _, table := range managedRetentionTables() {
		if _, ok := counts[table]; !ok {
			t.Errorf("table %q missing from counts: %v", table, counts)
		}
	}
	if counts["audit_events"] != 7 || counts["evidence_raw_payloads"] != 3 {
		t.Fatalf("unexpected deletion counts: %v", counts)
	}
}

func TestRetentionSweepContinuesAfterFirstTableFailure(t *testing.T) {
	denied := errors.New("pq: permission denied for table evidence_raw_payloads")
	stub := &stubSweeper{
		errs: map[string]error{"evidence_raw_payloads": denied},
		deleted: map[string]int64{
			"idempotency_records": 2,
			"audit_events":        5,
			"query_cursors":       0,
		},
	}
	counts, err := runRetentionSweep(context.Background(), stub, managedRetentionTables())
	if err == nil {
		t.Fatal("first table failure must surface an error")
	}
	// The three tables after the failed table are still cleaned up (call order covers all managed tables).
	if len(stub.order) != len(managedRetentionTables()) {
		t.Fatalf("sweep must not stop early: visited %v, want %d tables", stub.order, len(managedRetentionTables()))
	}
	if counts["idempotency_records"] != 2 || counts["audit_events"] != 5 {
		t.Fatalf("partial successes must still be reported: %v", counts)
	}
	if _, ok := counts["evidence_raw_payloads"]; ok {
		t.Fatalf("failed table must not report a count: %v", counts)
	}
	if !strings.Contains(err.Error(), "evidence_raw_payloads") {
		t.Fatalf("aggregate error must name the failed table: %v", err)
	}
}

func TestRetentionSweepAggregatesMultipleTableFailures(t *testing.T) {
	stub := &stubSweeper{
		errs: map[string]error{
			"idempotency_records": errors.New("denied A"),
			"query_cursors":       errors.New("denied B"),
		},
	}
	_, err := runRetentionSweep(context.Background(), stub, managedRetentionTables())
	if err == nil {
		t.Fatal("failures must aggregate into one error")
	}
	for _, want := range []string{"idempotency_records", "query_cursors", "denied A", "denied B"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("aggregate error must contain %q: %v", want, err)
		}
	}
}

// retention.go Bounded deletion of expired technical data (repository-commands:
// retention_delete_expired_rows). Executed under the yearning_retention role —
// the application role has no DELETE privilege on evidence_raw_payloads/idempotency_records,
// so production deployments must construct this service using the retention-role DSN.
// At most 10000 rows per transaction; claimed via FOR UPDATE SKIP LOCKED on the expiration index;
// deletes only, without inferring or repairing any business state.
package application

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

// RetentionBatchLimit is the per-transaction deletion cap (contract value).
const RetentionBatchLimit = 10000

// RetentionTargets maps managed target tables to their expiration columns (matching the
// yearning_retention grant list in db/roles/grants.sql; accessing tables outside this list is forbidden).
var RetentionTargets = map[string]string{
	"evidence_raw_payloads": "expires_at",
	"idempotency_records":   "expires_at",
	"audit_events":          "expires_at",
	"query_cursors":         "expires_at",
}

// RetentionService executes retention cleanup using an independent DB handle.
type RetentionService struct {
	db *gorm.DB
}

// NewRetentionService constructs the service; db must be a connection using the yearning_retention role.
func NewRetentionService(db *gorm.DB) (*RetentionService, error) {
	if db == nil {
		return nil, fmt.Errorf("retention: nil db")
	}
	return &RetentionService{db: db}, nil
}

// DeleteExpired deletes expired rows from a single table and returns the deleted count (0 if none).
func (s *RetentionService) DeleteExpired(ctx context.Context, table string) (int64, error) {
	column, ok := RetentionTargets[table]
	if !ok {
		return 0, fmt.Errorf("retention: table %q outside managed list", table)
	}
	// Claiming and deletion run in the same transaction (bounded_retention_delete contract:
	// FOR UPDATE SKIP LOCKED selection + single-transaction deletion).
	var deleted int64
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		ids := make([]string, 0, RetentionBatchLimit)
		if err := tx.WithContext(ctx).Raw(
			fmt.Sprintf(`select id::text from %s where %s < now() order by %s for update skip locked limit %d`,
				table, column, column, RetentionBatchLimit)).
			Scan(&ids).Error; err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		res := tx.WithContext(ctx).Exec(
			fmt.Sprintf(`delete from %s where id in ?`, table), ids)
		if res.Error != nil {
			return res.Error
		}
		deleted = res.RowsAffected
		return nil
	})
	return deleted, err
}

// SweepOrphans sweeps orphaned rows (RCP-20260826-01): anti-join deletes insert_only review fact
// child rows whose parent rows no longer exist, in dependency order. The application role never executes
// these deletions; this method must run on a yearning_retention role connection. At most 10000 rows per table per transaction.
func (s *RetentionService) SweepOrphans(ctx context.Context) (map[string]int64, error) {
	sweeps := []struct {
		table string
		sql   string
	}{
		{"review_finding_evidence", `
with orphan as (
    select rfe.finding_id, rfe.evidence_fact_id from review_finding_evidence rfe
    where not exists (select 1 from review_findings rf where rf.id = rfe.finding_id)
    order by rfe.finding_id, rfe.evidence_fact_id
    limit %d for update skip locked
)
delete from review_finding_evidence using orphan
where review_finding_evidence.finding_id = orphan.finding_id
  and review_finding_evidence.evidence_fact_id = orphan.evidence_fact_id`},
		{"review_findings", `
with orphan as (
    select rf.id from review_findings rf
    where not exists (select 1 from stage_review_snapshots s where s.id = rf.stage_review_id)
    order by rf.id
    limit %d for update skip locked
)
delete from review_findings using orphan where review_findings.id = orphan.id`},
		{"review_tool_calls", `
with orphan as (
    select rtc.id from review_tool_calls rtc
    where not exists (select 1 from review_runs r where r.id = rtc.review_run_id)
    order by rtc.id
    limit %d for update skip locked
)
delete from review_tool_calls using orphan where review_tool_calls.id = orphan.id`},
		{"review_run_sql_bindings", `
with orphan as (
    select b.review_run_id from review_run_sql_bindings b
    where not exists (select 1 from review_runs r where r.id = b.review_run_id)
    order by b.review_run_id
    limit %d for update skip locked
)
delete from review_run_sql_bindings using orphan where review_run_sql_bindings.review_run_id = orphan.review_run_id`},
		{"evidence_facts", `
with orphan as (
    select ef.id from evidence_facts ef
    where not exists (select 1 from stage_review_snapshots s where s.id = ef.stage_review_id)
    order by ef.id
    limit %d for update skip locked
)
delete from evidence_facts using orphan where evidence_facts.id = orphan.id`},
		{"evidence_raw_payloads", `
with orphan as (
    select erp.id from evidence_raw_payloads erp
    where not exists (select 1 from evidence_facts ef where ef.id = erp.evidence_fact_id)
    order by erp.id
    limit %d for update skip locked
)
delete from evidence_raw_payloads using orphan where evidence_raw_payloads.id = orphan.id`},
		{"sql_statements", `
with orphan as (
    select ss.id from sql_statements ss
    where not exists (select 1 from review_runs r where r.id = ss.review_run_id)
    order by ss.id
    limit %d for update skip locked
)
delete from sql_statements using orphan where sql_statements.id = orphan.id`},
		{"sql_fingerprint_groups", `
with orphan as (
    select g.id from sql_fingerprint_groups g
    where not exists (select 1 from review_runs r where r.id = g.review_run_id)
    order by g.id
    limit %d for update skip locked
)
delete from sql_fingerprint_groups using orphan where sql_fingerprint_groups.id = orphan.id`},
	}
	out := map[string]int64{}
	for _, sw := range sweeps {
		var deleted int64
		err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			res := tx.WithContext(ctx).Exec(fmt.Sprintf(sw.sql, RetentionBatchLimit))
			if res.Error != nil {
				return res.Error
			}
			deleted = res.RowsAffected
			return nil
		})
		if err != nil {
			return out, err
		}
		out[sw.table] = deleted
	}
	return out, nil
}

// tableSweeper abstracts the single-table expired-row cleanup entry: the GORM implementation is
// *RetentionService.DeleteExpired itself; RunOnce uses this seam to inject stubs verifying "partial failure does not abort".
type tableSweeper interface {
	DeleteExpired(ctx context.Context, table string) (int64, error)
}

// managedRetentionTables is the ordered list of managed tables (deterministic order matching the
// yearning_retention grant list in db/roles/grants.sql).
func managedRetentionTables() []string {
	return []string{"evidence_raw_payloads", "idempotency_records", "audit_events", "query_cursors"}
}

// RunOnce executes one deletion pass across all managed tables. NEW-QUERY-02: a single-table failure
// does not return early — the failed table is recorded in the aggregated error while cleanup continues
// for the remaining tables (a permission denied on the first table in production must not block cleanup
// of subsequent tables); the aggregated error is returned once for the caller to log at Warn.
func (s *RetentionService) RunOnce(ctx context.Context) (map[string]int64, error) {
	return runRetentionSweep(ctx, s, managedRetentionTables())
}

// runRetentionSweep sweeps tables sequentially and collects errors: deleted counts from successful tables
// are returned normally, while failed table names and causes are aggregated into a single error (giving
// the caller the full picture in a single Warn log).
func runRetentionSweep(ctx context.Context, sweeper tableSweeper, tables []string) (map[string]int64, error) {
	out := map[string]int64{}
	var failures []string
	for _, table := range tables {
		n, err := sweeper.DeleteExpired(ctx, table)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", table, err))
			continue
		}
		out[table] = n
	}
	if len(failures) > 0 {
		return out, fmt.Errorf("retention sweep incomplete (%d/%d tables failed): %s",
			len(failures), len(tables), strings.Join(failures, "; "))
	}
	return out, nil
}

// ExpiredAt computes the expiration timestamp (for tests and documentation).
func ExpiredAt(createdAt time.Time) time.Time {
	return createdAt.Add(7 * 24 * time.Hour)
}

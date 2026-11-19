// diagnostics.go implements the read-only native statement statistics collection extension
// (P101: MySQL performance_schema.events_statements_summary_by_digest and PostgreSQL pg_stat_statements).
//
// Security and performance invariants:
//   - Only permitted on read-only PurposePools (PurposeQuery / PurposeReview); PurposeExecution is rejected immediately.
//   - Routine snapshots always skip SQL text (MySQL omits DIGEST_TEXT; PostgreSQL calls pg_stat_statements(false)).
//   - Individual templates are fetched on demand by engine fingerprint (DIGEST / queryid) only when opening statement detail or running AI diagnosis.
//   - Never executes CREATE EXTENSION, SET GLOBAL, TRUNCATE, or pg_stat_statements_reset().
package database

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// DiagnosticsStatementTimeout is the hard timeout bound for a single statistics probe, snapshot, or template read (D3/D5: 2000ms).
const DiagnosticsStatementTimeout = 2 * time.Second

// StatsProbeState represents the six deterministic states of a datasource statistics capability probe (P101).
type StatsProbeState string

const (
	StatsProbeReady             StatsProbeState = "ready"
	StatsProbeDisabled          StatsProbeState = "stats_disabled"
	StatsProbeInsufficientPriv  StatsProbeState = "insufficient_privilege"
	StatsProbeUnsupportedEngine StatsProbeState = "unsupported_engine"
	StatsProbeNoSamples         StatsProbeState = "no_samples"
	StatsProbeCollectionFailed  StatsProbeState = "collection_failed"
)

// ErrTemplateUnavailable indicates that the target database cannot return a normalized SQL template (e.g. insufficient privilege or evicted).
var ErrTemplateUnavailable = errors.New("database: statement template unavailable")

// StatsProbeResult describes the outcome of a native statistics capability probe.
type StatsProbeResult struct {
	State                StatsProbeState
	EngineFamily         string
	SanitizedFailureName string
}

// NativeStatementStatRow is a single aggregated statement statistics row read during a routine snapshot (without SQL text).
type NativeStatementStatRow struct {
	EngineFingerprint                 string
	DatabaseOrSchema                  string
	CallsTotal                        int64
	TotalExecTimeMS                   float64
	RowsExaminedOrSharedBlksReadTotal int64
	RowsSentOrRowsReturnedTotal       int64
	LockTimeMSOrTempBlksWrittenTotal  float64
	PlansTotal                        *int64
	TotalPlanTimeMS                   *float64
	TempBlksReadTotal                 *int64
	SharedBlksHitTotal                *int64
}

// StatementStatsReader is the read-only native statement statistics extension interface (kept separate from MetadataReader to preserve the static gate method set).
type StatementStatsReader interface {
	ProbeStatementStats(ctx context.Context, pool *PurposePool) (*StatsProbeResult, error)
	ReadStatementStatsSnapshot(ctx context.Context, pool *PurposePool, topN int) ([]NativeStatementStatRow, error)
	ReadStatementTemplate(ctx context.Context, pool *PurposePool, engineFingerprint, databaseOrSchema string) (string, error)
}

// ProbeStatementStats probes the target database's native statement statistics capability via the Adapter.
func ProbeStatementStats(ctx context.Context, a Adapter, pool *PurposePool) (*StatsProbeResult, error) {
	sr, ok := a.(StatementStatsReader)
	if !ok {
		return &StatsProbeResult{
			State:                StatsProbeUnsupportedEngine,
			SanitizedFailureName: FailUnsupportedTarget,
		}, nil
	}
	return sr.ProbeStatementStats(ctx, pool)
}

// ReadStatementStatsSnapshot reads a Top-N routine statement statistics snapshot via the Adapter (without SQL text).
func ReadStatementStatsSnapshot(ctx context.Context, a Adapter, pool *PurposePool, topN int) ([]NativeStatementStatRow, error) {
	sr, ok := a.(StatementStatsReader)
	if !ok {
		return nil, ErrUnsupportedTarget
	}
	return sr.ReadStatementStatsSnapshot(ctx, pool, topN)
}

// ReadStatementTemplate reads a single normalized SQL template on demand by fingerprint via the Adapter.
func ReadStatementTemplate(ctx context.Context, a Adapter, pool *PurposePool, engineFingerprint, databaseOrSchema string) (string, error) {
	sr, ok := a.(StatementStatsReader)
	if !ok {
		return "", ErrUnsupportedTarget
	}
	return sr.ReadStatementTemplate(ctx, pool, engineFingerprint, databaseOrSchema)
}

func validateDiagnosticsPool(pool *PurposePool) error {
	if pool == nil {
		return errors.New("database: nil purpose pool")
	}
	if !ReadOnlyPurpose(pool.Purpose()) {
		return ErrReadOnlyViolation
	}
	return nil
}

func clampTopN(topN int) int {
	if topN <= 0 {
		return 200
	}
	if topN < 50 {
		return 50
	}
	if topN > 500 {
		return 500
	}
	return topN
}

const (
	mysqlProbePerfSchemaSQL = `SELECT @@performance_schema`
	mysqlProbeConsumerSQL   = `SELECT ENABLED FROM performance_schema.setup_consumers WHERE NAME = 'events_statements_current'`
	mysqlProbeCountSQL      = `SELECT COUNT(*) FROM performance_schema.events_statements_summary_by_digest WHERE DIGEST IS NOT NULL`

	// mysqlSnapshotWithoutTextSQL intentionally omits DIGEST_TEXT and selects only fingerprint and numeric counters.
	mysqlSnapshotWithoutTextSQL = `SELECT
  DIGEST,
  COALESCE(SCHEMA_NAME, ''),
  COUNT_STAR,
  ROUND(SUM_TIMER_WAIT / 1000000000.0, 3) AS total_exec_time_ms,
  SUM_ROWS_EXAMINED,
  SUM_ROWS_SENT,
  ROUND(SUM_LOCK_TIME / 1000000000.0, 3) AS lock_time_ms
FROM performance_schema.events_statements_summary_by_digest
WHERE DIGEST IS NOT NULL
  AND COALESCE(SCHEMA_NAME, '') NOT IN ('performance_schema', 'information_schema', 'mysql', 'sys')
ORDER BY SUM_TIMER_WAIT DESC
LIMIT ?`

	mysqlTemplateByDigestAndSchemaSQL = `SELECT COALESCE(DIGEST_TEXT, '')
FROM performance_schema.events_statements_summary_by_digest
WHERE DIGEST = ? AND COALESCE(SCHEMA_NAME, '') = ?
ORDER BY SUM_TIMER_WAIT DESC
LIMIT 1`

	mysqlTemplateByDigestFallbackSQL = `SELECT COALESCE(DIGEST_TEXT, '')
FROM performance_schema.events_statements_summary_by_digest
WHERE DIGEST = ?
ORDER BY SUM_TIMER_WAIT DESC
LIMIT 1`

	pgProbeExtensionSQL = `SELECT extversion FROM pg_extension WHERE extname = 'pg_stat_statements'`
	pgProbeCountSQL     = `SELECT COUNT(*) FROM pg_stat_statements(false) WHERE queryid IS NOT NULL AND queryid <> 0`

	// pgSnapshotWithoutTextSQL calls pg_stat_statements(false) to skip loading query text.
	pgSnapshotWithoutTextSQL = `SELECT
  s.queryid::text,
  COALESCE(d.datname, ''),
  s.calls,
  s.total_exec_time,
  s.shared_blks_read,
  s.rows,
  s.temp_blks_written::float8,
  s.plans,
  s.total_plan_time,
  s.temp_blks_read,
  s.shared_blks_hit
FROM pg_stat_statements(false) s
LEFT JOIN pg_database d ON d.oid = s.dbid
WHERE s.queryid IS NOT NULL AND s.queryid <> 0
ORDER BY s.total_exec_time DESC
LIMIT $1`

	pgTemplateByQueryIDSQL = `SELECT COALESCE(s.query, '')
FROM pg_stat_statements(true) s
LEFT JOIN pg_database d ON d.oid = s.dbid
WHERE s.queryid::text = $1
  AND ($2 = '' OR COALESCE(d.datname, '') = $2)
ORDER BY s.total_exec_time DESC
LIMIT 1`
)

// ProbeStatementStats probes the target database's native statistics status (6-state classification).
func (a *baseAdapter) ProbeStatementStats(ctx context.Context, pool *PurposePool) (*StatsProbeResult, error) {
	if err := validateDiagnosticsPool(pool); err != nil {
		return nil, err
	}
	qctx, cancel := context.WithTimeout(ctx, DiagnosticsStatementTimeout)
	defer cancel()

	switch a.engine() {
	case "mysql":
		return a.probeMySQLStatementStats(qctx, pool)
	case "postgresql":
		return a.probePostgresStatementStats(qctx, pool)
	default:
		return &StatsProbeResult{
			State:                StatsProbeUnsupportedEngine,
			EngineFamily:         a.engine(),
			SanitizedFailureName: FailUnsupportedTarget,
		}, nil
	}
}

func (a *baseAdapter) probeMySQLStatementStats(ctx context.Context, pool *PurposePool) (*StatsProbeResult, error) {
	res := &StatsProbeResult{EngineFamily: "mysql"}
	err := pool.withReadQueryer(ctx, true, func(q queryer) error {
		var perfOn sql.NullString
		if err := q.QueryRowContext(ctx, mysqlProbePerfSchemaSQL).Scan(&perfOn); err != nil {
			res.State, res.SanitizedFailureName = classifyDiagnosticsProbeError("mysql", err)
			return nil
		}
		val := strings.TrimSpace(strings.ToUpper(perfOn.String))
		if val == "0" || val == "OFF" || val == "" {
			res.State = StatsProbeDisabled
			res.SanitizedFailureName = "performance_schema_disabled"
			return nil
		}
		var consumerEnabled sql.NullString
		if err := q.QueryRowContext(ctx, mysqlProbeConsumerSQL).Scan(&consumerEnabled); err == nil {
			if strings.EqualFold(strings.TrimSpace(consumerEnabled.String), "NO") {
				res.State = StatsProbeDisabled
				res.SanitizedFailureName = "events_statements_consumer_disabled"
				return nil
			}
		}
		var count int64
		if err := q.QueryRowContext(ctx, mysqlProbeCountSQL).Scan(&count); err != nil {
			res.State, res.SanitizedFailureName = classifyDiagnosticsProbeError("mysql", err)
			return nil
		}
		if count <= 0 {
			res.State = StatsProbeNoSamples
			return nil
		}
		res.State = StatsProbeReady
		return nil
	})
	if err != nil && res.State == "" {
		state, name := classifyDiagnosticsProbeError("mysql", err)
		res.State = state
		res.SanitizedFailureName = name
	}
	return res, nil
}

func (a *baseAdapter) probePostgresStatementStats(ctx context.Context, pool *PurposePool) (*StatsProbeResult, error) {
	res := &StatsProbeResult{EngineFamily: "postgresql"}
	err := pool.withReadQueryer(ctx, true, func(q queryer) error {
		var extVersion sql.NullString
		err := q.QueryRowContext(ctx, pgProbeExtensionSQL).Scan(&extVersion)
		if errors.Is(err, sql.ErrNoRows) {
			res.State = StatsProbeDisabled
			res.SanitizedFailureName = "pg_stat_statements_not_installed"
			return nil
		}
		if err != nil {
			res.State, res.SanitizedFailureName = classifyDiagnosticsProbeError("postgresql", err)
			return nil
		}
		var count int64
		if err := q.QueryRowContext(ctx, pgProbeCountSQL).Scan(&count); err != nil {
			res.State, res.SanitizedFailureName = classifyDiagnosticsProbeError("postgresql", err)
			return nil
		}
		if count <= 0 {
			res.State = StatsProbeNoSamples
			return nil
		}
		res.State = StatsProbeReady
		return nil
	})
	if err != nil && res.State == "" {
		state, name := classifyDiagnosticsProbeError("postgresql", err)
		res.State = state
		res.SanitizedFailureName = name
	}
	return res, nil
}

func classifyDiagnosticsProbeError(engine string, err error) (StatsProbeState, string) {
	if err == nil {
		return StatsProbeReady, ""
	}
	rawLower := strings.ToLower(err.Error())
	if strings.Contains(rawLower, "pg_stat_statements must be loaded via shared_preload_libraries") {
		return StatsProbeDisabled, "pg_stat_statements_not_preloaded"
	}
	cls := ClassifyError(engine, err)
	switch cls.Class {
	case ClassInsufficientPriv:
		return StatsProbeInsufficientPriv, cls.FailureName
	case ClassNotFound:
		return StatsProbeUnsupportedEngine, cls.FailureName
	default:
		if strings.Contains(rawLower, "unknown column") ||
			strings.Contains(rawLower, "does not exist") ||
			strings.Contains(rawLower, "42883") ||
			strings.Contains(rawLower, "42703") ||
			strings.Contains(rawLower, "1146") ||
			strings.Contains(rawLower, "1054") {
			return StatsProbeUnsupportedEngine, FailUnsupportedTarget
		}
		if strings.Contains(rawLower, "access denied") ||
			strings.Contains(rawLower, "permission denied") ||
			strings.Contains(rawLower, "42501") ||
			strings.Contains(rawLower, "1142") ||
			strings.Contains(rawLower, "1227") {
			return StatsProbeInsufficientPriv, FailInsufficientPriv
		}
		return StatsProbeCollectionFailed, cls.FailureName
	}
}

// ReadStatementStatsSnapshot reads a routine statistics snapshot (skipping SQL text).
func (a *baseAdapter) ReadStatementStatsSnapshot(ctx context.Context, pool *PurposePool, topN int) ([]NativeStatementStatRow, error) {
	if err := validateDiagnosticsPool(pool); err != nil {
		return nil, err
	}
	limit := clampTopN(topN)
	qctx, cancel := context.WithTimeout(ctx, DiagnosticsStatementTimeout)
	defer cancel()

	var out []NativeStatementStatRow
	err := pool.withReadQueryer(qctx, true, func(q queryer) error {
		switch a.engine() {
		case "mysql":
			rows, err := q.QueryContext(qctx, mysqlSnapshotWithoutTextSQL, limit)
			if err != nil {
				return errors.New("database: mysql statement stats query failed")
			}
			defer func() { _ = rows.Close() }()
			for rows.Next() {
				var row NativeStatementStatRow
				if err := rows.Scan(
					&row.EngineFingerprint,
					&row.DatabaseOrSchema,
					&row.CallsTotal,
					&row.TotalExecTimeMS,
					&row.RowsExaminedOrSharedBlksReadTotal,
					&row.RowsSentOrRowsReturnedTotal,
					&row.LockTimeMSOrTempBlksWrittenTotal,
				); err != nil {
					return errors.New("database: mysql statement stats scan failed")
				}
				out = append(out, row)
			}
			return rows.Err()
		case "postgresql":
			rows, err := q.QueryContext(qctx, pgSnapshotWithoutTextSQL, limit)
			if err != nil {
				return errors.New("database: postgresql statement stats query failed")
			}
			defer func() { _ = rows.Close() }()
			for rows.Next() {
				var row NativeStatementStatRow
				var plans, tempRead, sharedHit sql.NullInt64
				var planTime sql.NullFloat64
				if err := rows.Scan(
					&row.EngineFingerprint,
					&row.DatabaseOrSchema,
					&row.CallsTotal,
					&row.TotalExecTimeMS,
					&row.RowsExaminedOrSharedBlksReadTotal,
					&row.RowsSentOrRowsReturnedTotal,
					&row.LockTimeMSOrTempBlksWrittenTotal,
					&plans,
					&planTime,
					&tempRead,
					&sharedHit,
				); err != nil {
					return errors.New("database: postgresql statement stats scan failed")
				}
				if plans.Valid {
					v := plans.Int64
					row.PlansTotal = &v
				}
				if planTime.Valid {
					v := planTime.Float64
					row.TotalPlanTimeMS = &v
				}
				if tempRead.Valid {
					v := tempRead.Int64
					row.TempBlksReadTotal = &v
				}
				if sharedHit.Valid {
					v := sharedHit.Int64
					row.SharedBlksHitTotal = &v
				}
				out = append(out, row)
			}
			return rows.Err()
		default:
			return ErrUnsupportedTarget
		}
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ReadStatementTemplate reads a single normalized SQL template on demand (called only when opening statement detail or running AI diagnosis).
func (a *baseAdapter) ReadStatementTemplate(ctx context.Context, pool *PurposePool, engineFingerprint, databaseOrSchema string) (string, error) {
	if err := validateDiagnosticsPool(pool); err != nil {
		return "", err
	}
	fp := strings.TrimSpace(engineFingerprint)
	if fp == "" {
		return "", ErrStatementRequired
	}
	qctx, cancel := context.WithTimeout(ctx, DiagnosticsStatementTimeout)
	defer cancel()

	var tmpl string
	err := pool.withReadQueryer(qctx, true, func(q queryer) error {
		switch a.engine() {
		case "mysql":
			err := q.QueryRowContext(qctx, mysqlTemplateByDigestAndSchemaSQL, fp, databaseOrSchema).Scan(&tmpl)
			if errors.Is(err, sql.ErrNoRows) {
				err = q.QueryRowContext(qctx, mysqlTemplateByDigestFallbackSQL, fp).Scan(&tmpl)
			}
			if errors.Is(err, sql.ErrNoRows) {
				return ErrTemplateUnavailable
			}
			if err != nil {
				return ErrTemplateUnavailable
			}
			return nil
		case "postgresql":
			err := q.QueryRowContext(qctx, pgTemplateByQueryIDSQL, fp, databaseOrSchema).Scan(&tmpl)
			if errors.Is(err, sql.ErrNoRows) {
				return ErrTemplateUnavailable
			}
			if err != nil {
				return ErrTemplateUnavailable
			}
			return nil
		default:
			return ErrUnsupportedTarget
		}
	})
	if err != nil {
		return "", err
	}
	trimmed := strings.TrimSpace(tmpl)
	if trimmed == "" || strings.EqualFold(trimmed, "<insufficient privilege>") || strings.EqualFold(trimmed, "<command string not enabled>") {
		return "", ErrTemplateUnavailable
	}
	return trimmed, nil
}

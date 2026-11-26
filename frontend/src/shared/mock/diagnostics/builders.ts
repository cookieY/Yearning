import type {
  DiagnosticStatementDetail,
  DiagnosticsOverview,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { CATALOG_DATASOURCES } from "@/shared/mock/flow-catalog";
import {
  LINKED_COMPLETED_ORDER_ID,
  MYSQL_DS,
  diagnosticsState,
  requireCatalogItem,
  statementsForDatasource,
} from "./state";

function buildBuckets() {
  return [
    { start: "2026-11-19T08:00:00Z", end: "2026-11-19T08:10:00Z", total: 3420, calls: 112, avg: 30.5 },
    { start: "2026-11-19T08:10:00Z", end: "2026-11-19T08:20:00Z", total: 4180, calls: 124, avg: 33.7 },
    { start: "2026-11-19T08:20:00Z", end: "2026-11-19T08:30:00Z", total: 4950, calls: 136, avg: 36.4 },
    { start: "2026-11-19T08:30:00Z", end: "2026-11-19T08:40:00Z", total: 5610, calls: 148, avg: 37.9 },
    { start: "2026-11-19T08:40:00Z", end: "2026-11-19T08:50:00Z", total: 5120, calls: 128, avg: 40.0 },
    { start: "2026-11-19T08:50:00Z", end: "2026-11-19T09:00:00Z", total: 5422, calls: 120, avg: 45.2 },
  ];
}

export function buildOverview(datasourceId: string): DiagnosticsOverview {
  const ds =
    CATALOG_DATASOURCES.find((item) => item.id === datasourceId) ?? MYSQL_DS;
  const statements = statementsForDatasource(datasourceId);
  const buckets = buildBuckets();
  const primaryStmt = requireCatalogItem(statements, 0, "primary diagnostic statement");
  const lastStmt = requireCatalogItem(
    statements,
    statements.length - 1,
    "latest diagnostic statement",
  );
  const lastCollected = diagnosticsState.world.lastCollectedAt.get(ds.id) ?? "2026-11-19T09:00:00Z";

  return {
    datasource_id: ds.id,
    engine: ds.engine,
    compatibility_mode: ds.compatibility_mode,
    probe_state: "ready",
    state: "active",
    coverage_mode: "window_delta",
    from: "2026-11-19T08:00:00Z",
    to: "2026-11-19T09:00:00Z",
    compare_from: "2026-11-19T07:00:00Z",
    compare_to: "2026-11-19T08:00:00Z",
    last_collected_at: lastCollected,
    snapshot_count: diagnosticsState.world.snapshotCounts.get(ds.id) ?? 12,
    load_trend: buckets.map((b) => ({
      bucket_start: b.start,
      bucket_end: b.end,
      total_exec_time_ms: b.total,
      calls: b.calls,
      avg_exec_time_ms: b.avg,
      reset_detected: false,
    })),
    top_statements: statements.map((s) => ({
      statement_id: s.statement_id,
      engine_fingerprint: s.engine_fingerprint,
      database_or_schema: s.database_or_schema,
      template_preview: s.template_preview,
      calls: s.calls,
      total_exec_time_ms: s.total_exec_time_ms,
      avg_exec_time_ms: s.avg_exec_time_ms,
      share_of_total_time: s.share_of_total_time,
    })),
    scatter_points: statements.map((s) => ({
      statement_id: s.statement_id,
      engine_fingerprint: s.engine_fingerprint,
      database_or_schema: s.database_or_schema,
      calls: s.calls,
      avg_exec_time_ms: s.avg_exec_time_ms,
      total_exec_time_ms: s.total_exec_time_ms,
      rows_examined_or_shared_blks_read: s.rows_examined_or_shared_blks_read,
    })),
    heatmap: statements.map((s, idx) => ({
      statement_id: s.statement_id,
      engine_fingerprint: s.engine_fingerprint,
      database_or_schema: s.database_or_schema,
      buckets: buckets.map((b, bIdx) => ({
        bucket_start: b.start,
        total_exec_time_ms: Math.round(b.total * (0.55 - idx * 0.15) * (0.85 + bIdx * 0.05)),
        calls: Math.max(8, Math.round(s.calls / buckets.length)),
        unobserved_below_top_n: false,
      })),
    })),
    regressions: [
      {
        statement_id: primaryStmt.statement_id,
        engine_fingerprint: primaryStmt.engine_fingerprint,
        database_or_schema: primaryStmt.database_or_schema,
        baseline_calls: 160,
        baseline_avg_ms: 26.8,
        current_calls: primaryStmt.calls,
        current_avg_ms: primaryStmt.avg_exec_time_ms,
        delta_avg_ms: Number((primaryStmt.avg_exec_time_ms - 26.8).toFixed(2)),
        regression_ratio: Number((primaryStmt.avg_exec_time_ms / 26.8).toFixed(2)),
        insufficient_samples: false,
      },
    ],
    new_high_load: [
      {
        statement_id: lastStmt.statement_id,
        engine_fingerprint: lastStmt.engine_fingerprint,
        database_or_schema: lastStmt.database_or_schema,
        current_calls: lastStmt.calls,
        current_total_ms: lastStmt.total_exec_time_ms,
        current_avg_ms: lastStmt.avg_exec_time_ms,
        unobserved_note: "Newly entered Top-N digest window within the last hour.",
      },
    ],
    statements,
  };
}

export function buildStatementDetail(
  datasourceId: string,
  statementId: string,
): DiagnosticStatementDetail | null {
  const ds =
    CATALOG_DATASOURCES.find((item) => item.id === datasourceId) ?? MYSQL_DS;
  const statements = statementsForDatasource(datasourceId);
  const summary =
    statements.find((item) => item.statement_id === statementId) ?? statements[0];
  if (!summary) return null;

  const buckets = buildBuckets();
  const confirmedLink = diagnosticsState.world.orderLinks.get(statementId) ?? null;
  const isPg = ds.compatibility_mode === "postgresql";

  return {
    statement_id: summary.statement_id,
    datasource_id: ds.id,
    engine: ds.engine,
    compatibility_mode: ds.compatibility_mode,
    engine_fingerprint: summary.engine_fingerprint,
    yearning_fingerprint: summary.yearning_fingerprint,
    database_or_schema: summary.database_or_schema,
    template_status: "available",
    template_sql: summary.template_preview,
    template_note: null,
    coverage_mode: "window_delta",
    from: "2026-11-19T08:00:00Z",
    to: "2026-11-19T09:00:00Z",
    summary,
    cost_unit_notes: isPg
      ? {
          rows_examined: "PostgreSQL pg_stat_statements shared_blks_read delta",
          lock_time_ms: "PostgreSQL temp_blks_written delta in window",
        }
      : {
          rows_examined: "MySQL performance_schema SUM_ROWS_EXAMINED delta",
          lock_time_ms: "MySQL SUM_LOCK_TIME converted from picoseconds to ms",
        },
    trend_series: buckets.map((b, index) => ({
      bucket_start: b.start,
      bucket_end: b.end,
      calls: Math.round(summary.calls / buckets.length) + index * 2,
      avg_exec_time_ms: Number((summary.avg_exec_time_ms * (0.78 + index * 0.08)).toFixed(2)),
      total_exec_time_ms: Math.round((summary.total_exec_time_ms / buckets.length) * (0.8 + index * 0.07)),
      reset_detected: false,
      unobserved_below_top_n: false,
    })),
    cost_series: buckets.map((b, index) => ({
      bucket_start: b.start,
      bucket_end: b.end,
      rows_examined_or_shared_blks_read: Math.round(
        (summary.rows_examined_or_shared_blks_read / buckets.length) * (0.85 + index * 0.06),
      ),
      rows_sent_or_rows_returned: Math.max(1, Math.round(summary.rows_sent_or_rows_returned / buckets.length)),
      lock_time_ms_or_temp_blks_written: Math.max(1, Math.round(summary.lock_time_ms_or_temp_blks_written / buckets.length)),
      shared_blks_hit: isPg ? 14200 + index * 600 : null,
    })),
    order_comparison: {
      linked_change_order_id: confirmedLink?.change_order_id ?? LINKED_COMPLETED_ORDER_ID,
      linked_order_display_no: confirmedLink?.order_display_no ?? "YR-20260901-000006",
      relation_kind: confirmedLink?.relation_kind ?? "index_or_schema_change",
      reference_executed_at: confirmedLink?.order_executed_at ?? "2026-11-19T08:30:00Z",
      insufficient_samples: false,
      before_window: {
        from: "2026-11-19T08:00:00Z",
        to: "2026-11-19T08:30:00Z",
        bucket_count: 3,
        calls: 112,
        total_exec_time_ms: 6140,
        avg_exec_time_ms: 54.82,
      },
      after_window: {
        from: "2026-11-19T08:30:00Z",
        to: "2026-11-19T09:00:00Z",
        bucket_count: 3,
        calls: 128,
        total_exec_time_ms: 9412,
        avg_exec_time_ms: 73.53,
      },
      candidate_orders_in_window: [
        {
          change_order_id: LINKED_COMPLETED_ORDER_ID,
          display_no: "YR-20260901-000006",
          title: "Add archival partition column on orders",
          state: "completed",
          executed_at: "2026-11-19T08:30:00Z",
          relation_hint: "index_or_schema_change",
          confirmed_link_id: confirmedLink?.id ?? null,
        },
      ],
    },
    latest_ai_report: diagnosticsState.world.aiReports.get(summary.statement_id) ?? null,
  };
}

import type {
  DatasourceDiagnosticsConfig,
  DiagnosticAiReport,
  DiagnosticOrderLink,
  DiagnosticsStatementSummary,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { CATALOG_DATASOURCES, CATALOG_FLOWS } from "@/shared/mock/flow-catalog";
import { createMockResponder, formatSequenceUuid } from "@/shared/mock/kit";

const DIAG_REQUEST_ID = "67676767-6767-4676-8676-676767676767";

export function requireCatalogItem<T>(items: readonly T[], index: number, label: string): T {
  const item = items[index];
  if (item === undefined) {
    throw new Error(`missing seeded ${label} at index ${String(index)}`);
  }
  return item;
}

export const MYSQL_DS = requireCatalogItem(CATALOG_DATASOURCES, 0, "mysql datasource");
export const PG_DS = requireCatalogItem(CATALOG_DATASOURCES, 2, "postgresql datasource");
export const TIDB_DS = requireCatalogItem(CATALOG_DATASOURCES, 3, "tidb datasource");
export const DEFAULT_FLOW_ID = requireCatalogItem(CATALOG_FLOWS, 0, "default change flow").id;

// Real seeded gallery order IDs from review-fixture.ts so clicking order links opens a live detail page.
export const LINKED_COMPLETED_ORDER_ID = "7e6f1a2b-0000-4000-8000-900000000006";
export const SUBMITTED_OPT_ORDER_ID = "7e6f1a2b-0000-4000-8000-900000000001";

const STMT_ORDERS_SCAN = "d1111111-1111-4111-8111-111111110001";
const STMT_ITEMS_JOIN = "d1111111-1111-4111-8111-111111110002";
const STMT_PAYMENTS_SORT = "d1111111-1111-4111-8111-111111110003";
const STMT_ANALYTICS_AGG = "d1111111-1111-4111-8111-111111110004";

export interface DiagnosticsWorld {
  configs: Map<string, DatasourceDiagnosticsConfig>;
  aiReports: Map<string, DiagnosticAiReport>;
  orderLinks: Map<string, DiagnosticOrderLink>;
  lastCollectedAt: Map<string, string>;
  snapshotCounts: Map<string, number>;
  seq: number;
}

export const { successEnvelope, businessErrorPayload: businessError } =
  createMockResponder(DIAG_REQUEST_ID);

export function buildSeedAiReport(datasourceId: string, statementId: string): DiagnosticAiReport {
  return {
    report_id: `d5555555-5555-4555-8555-${statementId.slice(-12)}`,
    datasource_id: datasourceId,
    statement_id: statementId,
    window_start: "2026-11-19T08:00:00Z",
    window_end: "2026-11-19T09:00:00Z",
    coverage_mode: "window_delta",
    desensitized_context_summary:
      "Desensitized 2 tables (tbl_1, tbl_2) and 4 columns; 0 literal parameter values sent to LLM.",
    observed_facts: [
      {
        metric: "avg_exec_time_ms",
        value: "64.8 ms (p95 spike: 118.4 ms)",
        window: "08:00-09:00 UTC",
      },
      {
        metric: "rows_examined_per_call",
        value: "2,150 rows examined vs. 1 row returned",
        window: "08:00-09:00 UTC",
      },
      {
        metric: "share_of_total_time",
        value: "54.2% of total instance SQL execution time",
        window: "08:00-09:00 UTC",
      },
    ],
    root_cause_hypotheses: [
      {
        hypothesis:
          "Missing composite index on (customer_email, status) forces full scan across active order partitions.",
        confidence: "high",
        evidence_basis:
          "rows_examined_or_shared_blks_read = 516,000 across 240 calls with selectivity < 0.05%",
      },
      {
        hypothesis:
          "Secondary ORDER BY created_at DESC triggers temporary filesort once matched rows exceed sort_buffer_size.",
        confidence: "medium",
        evidence_basis:
          "Average latency grew +142% compared to the baseline window as order volume expanded.",
      },
    ],
    optimization_recommendations: [
      {
        kind: "create_index",
        title: "Add covering composite index idx_orders_email_status_created",
        rationale:
          "Eliminates full scan and filesort by aligning leading equality columns with descending sort order.",
        applicable_conditions: [
          "InnoDB online DDL (ALGORITHM=INPLACE, LOCK=NONE) is supported on the target MySQL 8.0 node.",
        ],
        candidate_sql:
          "CREATE INDEX idx_orders_email_status_created\n  ON orders_db.orders (customer_email, status, created_at DESC);",
        risk_notes: [
          "Adds ~42 MB secondary index footprint; verify replication lag stays below 2s during build.",
        ],
      },
      {
        kind: "sql_rewrite",
        title: "Project explicit columns instead of SELECT *",
        rationale:
          "Reduces clustered-index bookmark lookups and network payload size on wide order metadata rows.",
        applicable_conditions: [
          "Caller only requires id, status, total_cents, and created_at.",
        ],
        candidate_sql: null,
        risk_notes: [
          "Coordinate column list with application ORM mapping before deploying.",
        ],
      },
    ],
    verification_steps: [
      "Run EXPLAIN ANALYZE on a staging snapshot to confirm Index Range Scan + Backward index scan with zero filesort.",
      "Monitor rows_examined delta and avg_exec_time_ms in SQL Diagnostics over the next 30-minute window.",
    ],
    candidate_sql:
      "CREATE INDEX idx_orders_email_status_created ON orders_db.orders (customer_email, status, created_at DESC);",
    candidate_statement_kind: "ddl",
    submission_state: "draft_ready",
    flow_id: DEFAULT_FLOW_ID,
    ephemeral_run_id: null,
    submitted_order_id: null,
    submitted_order_display_no: null,
    created_at: "2026-11-19T09:02:00Z",
  };
}

function buildSeedWorld(): DiagnosticsWorld {
  const configs = new Map<string, DatasourceDiagnosticsConfig>();
  const lastCollectedAt = new Map<string, string>();
  const snapshotCounts = new Map<string, number>();

  for (const ds of [MYSQL_DS, PG_DS, TIDB_DS]) {
    configs.set(ds.id, {
      datasource_id: ds.id,
      enabled: true,
      interval_seconds: 60,
      effective_interval_seconds: 60,
      top_n: 200,
      state: "active",
      last_probe_state: "ready",
      last_probe_at: "2026-11-19T09:00:00Z",
      last_collected_at: "2026-11-19T09:00:00Z",
      consecutive_failures: 0,
      paused_reason: null,
      version: 1,
      updated_at: "2026-11-19T09:00:00Z",
    });
    lastCollectedAt.set(ds.id, "2026-11-19T09:00:00Z");
    snapshotCounts.set(ds.id, 12);
  }

  const aiReports = new Map<string, DiagnosticAiReport>();
  aiReports.set(STMT_ORDERS_SCAN, buildSeedAiReport(MYSQL_DS.id, STMT_ORDERS_SCAN));

  return {
    configs,
    aiReports,
    orderLinks: new Map(),
    lastCollectedAt,
    snapshotCounts,
    seq: 100,
  };
}

export const diagnosticsState: { world: DiagnosticsWorld } = {
  world: buildSeedWorld(),
};

export function nextUuid(prefix: string): string {
  diagnosticsState.world.seq += 1;
  return formatSequenceUuid(prefix, diagnosticsState.world.seq);
}

export function resetDiagnosticsFixture(): void {
  diagnosticsState.world = buildSeedWorld();
}

const MYSQL_STATEMENTS: DiagnosticsStatementSummary[] = [
  {
    statement_id: STMT_ORDERS_SCAN,
    engine_fingerprint: "fp_orders_email_status_scan",
    yearning_fingerprint: "yr_fp_orders_01",
    database_or_schema: "orders_db",
    template_status: "available",
    template_preview:
      "SELECT * FROM orders WHERE customer_email = ? AND status = ? ORDER BY created_at DESC LIMIT ?",
    calls: 240,
    total_exec_time_ms: 15552,
    avg_exec_time_ms: 64.8,
    rows_examined_or_shared_blks_read: 516000,
    rows_sent_or_rows_returned: 240,
    lock_time_ms_or_temp_blks_written: 34,
    share_of_total_time: 0.542,
    coverage_mode: "window_delta",
    first_seen_at: "2026-11-19T08:00:00Z",
    last_seen_at: "2026-11-19T09:00:00Z",
  },
  {
    statement_id: STMT_ITEMS_JOIN,
    engine_fingerprint: "fp_order_items_sku_join",
    yearning_fingerprint: "yr_fp_order_items_02",
    database_or_schema: "orders_db",
    template_status: "available",
    template_preview:
      "SELECT oi.sku_id, SUM(oi.qty) FROM order_items oi JOIN orders o ON o.id = oi.order_id WHERE o.updated_at >= ? GROUP BY oi.sku_id",
    calls: 118,
    total_exec_time_ms: 7840,
    avg_exec_time_ms: 66.44,
    rows_examined_or_shared_blks_read: 295000,
    rows_sent_or_rows_returned: 1420,
    lock_time_ms_or_temp_blks_written: 18,
    share_of_total_time: 0.273,
    coverage_mode: "window_delta",
    first_seen_at: "2026-11-19T08:00:00Z",
    last_seen_at: "2026-11-19T09:00:00Z",
  },
  {
    statement_id: STMT_PAYMENTS_SORT,
    engine_fingerprint: "fp_payments_reconcile_sort",
    yearning_fingerprint: "yr_fp_payments_03",
    database_or_schema: "orders_db",
    template_status: "available",
    template_preview:
      "SELECT id, amount_cents, settled_at FROM payment_ledger WHERE merchant_id = ? AND state IN (?, ?) ORDER BY settled_at DESC",
    calls: 410,
    total_exec_time_ms: 5310,
    avg_exec_time_ms: 12.95,
    rows_examined_or_shared_blks_read: 94000,
    rows_sent_or_rows_returned: 4100,
    lock_time_ms_or_temp_blks_written: 9,
    share_of_total_time: 0.185,
    coverage_mode: "window_delta",
    first_seen_at: "2026-11-19T08:00:00Z",
    last_seen_at: "2026-11-19T09:00:00Z",
  },
];

const PG_STATEMENTS: DiagnosticsStatementSummary[] = [
  {
    statement_id: STMT_ANALYTICS_AGG,
    engine_fingerprint: "pg_fp_daily_gmv_rollup",
    yearning_fingerprint: "yr_fp_analytics_01",
    database_or_schema: "analytics",
    template_status: "available",
    template_preview:
      "SELECT date_trunc($1, paid_at), region, SUM(gmv_cents) FROM fact_orders WHERE paid_at >= $2 GROUP BY 1, 2",
    calls: 64,
    total_exec_time_ms: 11240,
    avg_exec_time_ms: 175.63,
    rows_examined_or_shared_blks_read: 48200,
    rows_sent_or_rows_returned: 1920,
    lock_time_ms_or_temp_blks_written: 640,
    share_of_total_time: 0.78,
    coverage_mode: "window_delta",
    first_seen_at: "2026-11-19T08:00:00Z",
    last_seen_at: "2026-11-19T09:00:00Z",
  },
];

export function statementsForDatasource(datasourceId: string): DiagnosticsStatementSummary[] {
  if (datasourceId === PG_DS.id) return PG_STATEMENTS;
  return MYSQL_STATEMENTS;
}

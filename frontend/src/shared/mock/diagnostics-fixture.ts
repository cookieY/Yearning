import { HttpResponse, http } from "msw";
import type { DefaultBodyType, HttpHandler } from "msw";
import type {
  ConfirmDiagnosticOrderLinkRequest,
  DatasourceDiagnosticsConfig,
  DiagnosticAiReport,
  DiagnosticDatasourceSummary,
  DiagnosticOrderLink,
  DiagnosticStatementDetail,
  DiagnosticsOverview,
  DiagnosticsStatementSummary,
  PrecheckDiagnosticOptimizationDraftRequest,
  PrecheckDiagnosticOptimizationDraftResponse,
  SubmitDiagnosticOptimizationOrderResponse,
  TriggerManualDiagnosticsSnapshotResponse,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { CATALOG_DATASOURCES, CATALOG_FLOWS } from "@/shared/mock/flow-catalog";
import { currentIdentityId } from "@/shared/mock/identity-store";

/**
 * Stateful browser MSW fixture backing SQL Performance Diagnostics
 * (`/api/v4/admin/diagnostics/*` and `/api/v4/admin/datasources/:id/diagnostics-config`).
 * Anchored on the shared mock catalog datasources so overview charts, drill-down
 * statement diagnostics, AI optimization reports, and change-order links all work
 * end to end in mock dev and the public pure-frontend interactive demo.
 */

const DIAG_REQUEST_ID = "67676767-6767-4676-8676-676767676767";

function requireCatalogItem<T>(items: readonly T[], index: number, label: string): T {
  const item = items[index];
  if (item === undefined) {
    throw new Error(`missing seeded ${label} at index ${String(index)}`);
  }
  return item;
}

const MYSQL_DS = requireCatalogItem(CATALOG_DATASOURCES, 0, "mysql datasource");
const PG_DS = requireCatalogItem(CATALOG_DATASOURCES, 2, "postgresql datasource");
const TIDB_DS = requireCatalogItem(CATALOG_DATASOURCES, 3, "tidb datasource");
const DEFAULT_FLOW_ID = requireCatalogItem(CATALOG_FLOWS, 0, "default change flow").id;

// Real seeded gallery order IDs from review-fixture.ts so clicking order links opens a live detail page.
const LINKED_COMPLETED_ORDER_ID = "7e6f1a2b-0000-4000-8000-900000000006";
const SUBMITTED_OPT_ORDER_ID = "7e6f1a2b-0000-4000-8000-900000000001";

const STMT_ORDERS_SCAN = "d1111111-1111-4111-8111-111111110001";
const STMT_ITEMS_JOIN = "d1111111-1111-4111-8111-111111110002";
const STMT_PAYMENTS_SORT = "d1111111-1111-4111-8111-111111110003";
const STMT_ANALYTICS_AGG = "d1111111-1111-4111-8111-111111110004";

interface DiagnosticsWorld {
  configs: Map<string, DatasourceDiagnosticsConfig>;
  aiReports: Map<string, DiagnosticAiReport>;
  orderLinks: Map<string, DiagnosticOrderLink>;
  lastCollectedAt: Map<string, string>;
  snapshotCounts: Map<string, number>;
  seq: number;
}

function successEnvelope(data: DefaultBodyType) {
  return {
    err_code: 0,
    message: "ok",
    data,
    request_id: DIAG_REQUEST_ID,
  };
}

function businessError(errCode: number, message: string) {
  return {
    err_code: errCode,
    message,
    data: null,
    request_id: DIAG_REQUEST_ID,
    retryable: false,
  };
}

function nowIso(): string {
  return new Date().toISOString();
}

function nextUuid(prefix: string): string {
  world.seq += 1;
  return `${prefix}-1111-4111-8111-${String(world.seq).padStart(12, "0")}`;
}

function buildSeedAiReport(datasourceId: string, statementId: string): DiagnosticAiReport {
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

let world: DiagnosticsWorld = buildSeedWorld();

export function resetDiagnosticsFixture(): void {
  world = buildSeedWorld();
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

function statementsForDatasource(datasourceId: string): DiagnosticsStatementSummary[] {
  if (datasourceId === PG_DS.id) return PG_STATEMENTS;
  return MYSQL_STATEMENTS;
}

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

function buildOverview(datasourceId: string): DiagnosticsOverview {
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
  const lastCollected = world.lastCollectedAt.get(ds.id) ?? "2026-11-19T09:00:00Z";

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
    snapshot_count: world.snapshotCounts.get(ds.id) ?? 12,
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

function buildStatementDetail(
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
  const confirmedLink = world.orderLinks.get(statementId) ?? null;
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
    latest_ai_report: world.aiReports.get(summary.statement_id) ?? null,
  };
}

const pause = (ms: number): Promise<void> =>
  new Promise((resolve) => setTimeout(resolve, ms));

export function diagnosticsFixtureHandlers(): HttpHandler[] {
  return [
    http.get("*/admin/datasources/:datasourceId/diagnostics-config", ({ params }) => {
      const dsId = String(params.datasourceId);
      const existing = world.configs.get(dsId) ?? {
        datasource_id: dsId,
        enabled: false,
        interval_seconds: 60 as const,
        effective_interval_seconds: 60,
        top_n: 200,
        state: "disabled" as const,
        last_probe_state: "ready" as const,
        last_probe_at: null,
        last_collected_at: null,
        consecutive_failures: 0,
        paused_reason: null,
        version: 1,
        updated_at: "2026-11-19T09:00:00Z",
      };
      return HttpResponse.json(successEnvelope(existing));
    }),

    http.put("*/admin/datasources/:datasourceId/diagnostics-config", async ({ params, request }) => {
      const dsId = String(params.datasourceId);
      const body = (await request.json().catch(() => ({}))) as Partial<DatasourceDiagnosticsConfig>;
      const prev = world.configs.get(dsId);
      const enabled = body.enabled ?? prev?.enabled ?? true;
      const interval = body.interval_seconds ?? prev?.interval_seconds ?? 60;
      const updated: DatasourceDiagnosticsConfig = {
        datasource_id: dsId,
        enabled,
        interval_seconds: interval,
        effective_interval_seconds: interval,
        top_n: body.top_n ?? prev?.top_n ?? 200,
        state: enabled ? "active" : "disabled",
        last_probe_state: "ready",
        last_probe_at: nowIso(),
        last_collected_at: prev?.last_collected_at ?? nowIso(),
        consecutive_failures: 0,
        paused_reason: null,
        version: (prev?.version ?? 1) + 1,
        updated_at: nowIso(),
      };
      world.configs.set(dsId, updated);
      return HttpResponse.json(successEnvelope(updated));
    }),

    http.get("*/admin/diagnostics/datasources", () => {
      const items: DiagnosticDatasourceSummary[] = [
        {
          id: MYSQL_DS.id,
          name: MYSQL_DS.name,
          environment: "prod",
          engine: MYSQL_DS.engine,
          compatibility_mode: MYSQL_DS.compatibility_mode,
          host: "10.0.12.10",
          port: 3306,
          database_name: "orders_db",
          diagnostics_enabled: true,
          state: "active",
          probe_state: "ready",
          interval_seconds: 60,
          effective_interval_seconds: 60,
          top_n: 200,
          last_collected_at: world.lastCollectedAt.get(MYSQL_DS.id) ?? "2026-11-19T09:00:00Z",
        },
        {
          id: PG_DS.id,
          name: PG_DS.name,
          environment: "prod",
          engine: PG_DS.engine,
          compatibility_mode: PG_DS.compatibility_mode,
          host: "10.0.12.24",
          port: 5432,
          database_name: "analytics",
          diagnostics_enabled: true,
          state: "active",
          probe_state: "ready",
          interval_seconds: 60,
          effective_interval_seconds: 60,
          top_n: 200,
          last_collected_at: world.lastCollectedAt.get(PG_DS.id) ?? "2026-11-19T09:00:00Z",
        },
        {
          id: TIDB_DS.id,
          name: TIDB_DS.name,
          environment: "staging",
          engine: TIDB_DS.engine,
          compatibility_mode: TIDB_DS.compatibility_mode,
          host: "10.0.18.40",
          port: 4000,
          database_name: "warehouse_db",
          diagnostics_enabled: true,
          state: "active",
          probe_state: "ready",
          interval_seconds: 120,
          effective_interval_seconds: 120,
          top_n: 200,
          last_collected_at: world.lastCollectedAt.get(TIDB_DS.id) ?? "2026-11-19T09:00:00Z",
        },
      ];
      return HttpResponse.json(successEnvelope({ items }));
    }),

    http.get("*/admin/diagnostics/datasources/:datasourceId/overview", ({ params }) => {
      const dsId = String(params.datasourceId);
      return HttpResponse.json(successEnvelope(buildOverview(dsId)));
    }),

    http.post("*/admin/diagnostics/datasources/:datasourceId/snapshots", async ({ params }) => {
      const dsId = String(params.datasourceId);
      const ts = nowIso();
      world.lastCollectedAt.set(dsId, ts);
      world.snapshotCounts.set(dsId, (world.snapshotCounts.get(dsId) ?? 12) + 1);
      await pause(240);
      const res: TriggerManualDiagnosticsSnapshotResponse = {
        snapshot_id: nextUuid("d7777777"),
        datasource_id: dsId,
        trigger_kind: "manual",
        probe_state: "ready",
        statement_count: statementsForDatasource(dsId).length,
        duration_ms: 84,
        reset_detected: false,
        gap_detected: false,
        sampled_at: ts,
      };
      return HttpResponse.json(successEnvelope(res));
    }),

    http.get(
      "*/admin/diagnostics/datasources/:datasourceId/statements/:statementId",
      ({ params }) => {
        const dsId = String(params.datasourceId);
        const stmtId = String(params.statementId);
        const detail = buildStatementDetail(dsId, stmtId);
        if (!detail) {
          return HttpResponse.json(businessError(1002, "diagnostic statement not found"));
        }
        return HttpResponse.json(successEnvelope(detail));
      },
    ),

    http.post(
      "*/admin/diagnostics/datasources/:datasourceId/statements/:statementId/order-links",
      async ({ params, request }) => {
        const dsId = String(params.datasourceId);
        const stmtId = String(params.statementId);
        const body = (await request.json().catch(() => ({}))) as Partial<ConfirmDiagnosticOrderLinkRequest>;
        const link: DiagnosticOrderLink = {
          id: nextUuid("d8888888"),
          datasource_id: dsId,
          statement_id: stmtId,
          target_statement_id: body.target_statement_id ?? null,
          change_order_id: body.change_order_id ?? LINKED_COMPLETED_ORDER_ID,
          order_display_no: "YR-20260901-000006",
          relation_kind: body.relation_kind ?? "index_or_schema_change",
          order_executed_at: "2026-11-19T08:30:00Z",
          confirmed_by_user_id: currentIdentityId(),
          created_at: nowIso(),
        };
        world.orderLinks.set(stmtId, link);
        return HttpResponse.json(successEnvelope(link));
      },
    ),

    http.post(
      "*/admin/diagnostics/datasources/:datasourceId/statements/:statementId/ai-reports",
      async ({ params }) => {
        const dsId = String(params.datasourceId);
        const stmtId = String(params.statementId);
        await pause(380);
        const report: DiagnosticAiReport = {
          ...buildSeedAiReport(dsId, stmtId),
          report_id: nextUuid("d9999999"),
          created_at: nowIso(),
        };
        world.aiReports.set(stmtId, report);
        return HttpResponse.json(successEnvelope(report));
      },
    ),

    http.post(
      "*/admin/diagnostics/datasources/:datasourceId/ai-reports/:reportId/prechecks",
      async ({ params, request }) => {
        const reportId = String(params.reportId);
        const body = (await request.json().catch(() => ({}))) as Partial<PrecheckDiagnosticOptimizationDraftRequest>;
        let matchedKey: string | null = null;
        let matchedReport: DiagnosticAiReport | null = null;
        for (const [key, value] of world.aiReports.entries()) {
          if (value.report_id === reportId) {
            matchedKey = key;
            matchedReport = value;
            break;
          }
        }
        if (!matchedKey || !matchedReport) {
          return HttpResponse.json(businessError(1002, "diagnostic AI report not found"));
        }
        const runId = nextUuid("daaaaaaa");
        const updated: DiagnosticAiReport = {
          ...matchedReport,
          flow_id: body.flow_id ?? matchedReport.flow_id ?? DEFAULT_FLOW_ID,
          candidate_sql: body.candidate_sql ?? matchedReport.candidate_sql,
          submission_state: "prechecked",
          ephemeral_run_id: runId,
        };
        world.aiReports.set(matchedKey, updated);
        await pause(260);
        const res: PrecheckDiagnosticOptimizationDraftResponse = {
          report: updated,
          run_id: runId,
          result_ref: `/review-runs/${runId}`,
        };
        return HttpResponse.json(successEnvelope(res));
      },
    ),

    http.post(
      "*/admin/diagnostics/datasources/:datasourceId/ai-reports/:reportId/order-submissions",
      async ({ params }) => {
        const reportId = String(params.reportId);
        let matchedKey: string | null = null;
        let matchedReport: DiagnosticAiReport | null = null;
        for (const [key, value] of world.aiReports.entries()) {
          if (value.report_id === reportId) {
            matchedKey = key;
            matchedReport = value;
            break;
          }
        }
        if (!matchedKey || !matchedReport) {
          return HttpResponse.json(businessError(1002, "diagnostic AI report not found"));
        }
        const displayNo = "YR-20260901-000001";
        const updated: DiagnosticAiReport = {
          ...matchedReport,
          submission_state: "submitted",
          submitted_order_id: SUBMITTED_OPT_ORDER_ID,
          submitted_order_display_no: displayNo,
        };
        world.aiReports.set(matchedKey, updated);
        await pause(220);
        const res: SubmitDiagnosticOptimizationOrderResponse = {
          report: updated,
          order_id: SUBMITTED_OPT_ORDER_ID,
          order_display_no: displayNo,
          order_state: "submitted",
        };
        return HttpResponse.json(successEnvelope(res));
      },
    ),
  ];
}

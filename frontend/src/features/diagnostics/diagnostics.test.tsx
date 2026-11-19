import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter, Route, Routes } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import "@/shared/i18n";
import i18n from "@/shared/i18n";
import type {
  DiagnosticAiReport,
  DiagnosticDatasourceSummary,
  DiagnosticStatementDetail,
  DiagnosticsOverview,
  DiagnosticsStatementSummary,
  Flow,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import * as adminApi from "@/api/generated/client/administration/administration";
import * as changeDraftsApi from "@/api/generated/client/change-drafts/change-drafts";
import { DiagnosticStatementDetailPanel } from "@/features/diagnostics/diagnostic-statement-detail";
import {
  DiagnosticsOverviewCharts,
  formatReadableSqlLabel,
  formatShortTime,
} from "@/features/diagnostics/diagnostics-overview-charts";
import AdminDiagnosticsPage from "@/routes/admin/admin-diagnostics-page";
import { describeError } from "@/shared/api/error-display";
import { BusinessError } from "@/shared/api/mutator";

vi.mock("@/features/auth/session-provider", () => ({
  useSession: () => ({
    status: "authenticated",
    user: {
      id: "0198d9cc-e65d-7b9d-a8aa-3c81945f99ac",
      username: "ops_admin",
      display_name: "Ops Admin",
      email: "ops@yearning.io",
      is_builtin_admin: false,
      role: "admin",
      can_access_admin: true,
      version: 1,
      created_at: "2026-11-07T00:00:00Z",
      updated_at: "2026-11-07T00:00:00Z",
    },
  }),
}));

vi.mock("@/api/generated/client/administration/administration", async (importOriginal) => {
  const actual = await importOriginal<typeof adminApi>();
  return {
    ...actual,
    listDiagnosticDatasources: vi.fn(),
    getDiagnosticsOverview: vi.fn(),
    triggerManualDiagnosticsSnapshot: vi.fn(),
    getDiagnosticStatementDetail: vi.fn(),
    confirmDiagnosticOrderLink: vi.fn(),
    diagnoseStatementWithAi: vi.fn(),
    precheckDiagnosticOptimizationDraft: vi.fn(),
    submitDiagnosticOptimizationOrder: vi.fn(),
  };
});

vi.mock("@/api/generated/client/change-drafts/change-drafts", () => ({
  listCurrentUserFlows: vi.fn(),
}));

const DS_ID = "22222222-2222-4222-8222-222222222222";
const STMT_ID = "33333333-3333-4333-8333-333333333333";
const FLOW_ID = "11111111-1111-4111-8111-111111111111";
const ORDER_ID = "44444444-4444-4444-8444-444444444444";
const REPORT_ID = "55555555-5555-4555-8555-555555555555";
const RUN_ID = "66666666-6666-4666-8666-666666666666";

const SAMPLE_DATASOURCES: DiagnosticDatasourceSummary[] = [
  {
    id: DS_ID,
    name: "prod-mysql-orders",
    environment: "prod",
    engine: "mysql",
    compatibility_mode: "mysql",
    host: "10.0.0.12",
    port: 3306,
    database_name: "orders_db",
    diagnostics_enabled: true,
    state: "active",
    probe_state: "ready",
    interval_seconds: 60,
    effective_interval_seconds: 60,
    top_n: 200,
    last_collected_at: "2026-11-07T12:00:00Z",
  },
];

const SAMPLE_FLOW: Flow = {
  id: FLOW_ID,
  name: "Orders Prod Change Flow",
  flow_type: "change_review",
  enabled: true,
  status: "enabled",
  version: 1,
  created_at: "2026-11-07T00:00:00Z",
  updated_at: "2026-11-07T00:00:00Z",
  stages: [
    {
      position: 1,
      datasource_id: DS_ID,
      datasource_name: "prod-mysql-orders",
      approval_steps: [],
      execution_actors: [],
    },
  ],
};

const SAMPLE_STATEMENT_SUMMARY: DiagnosticsStatementSummary = {
  statement_id: STMT_ID,
  engine_fingerprint: "fp_orders_customer_scan",
  yearning_fingerprint: "yr_fp_orders_01",
  database_or_schema: "orders_db",
  template_status: "available",
  template_preview: "SELECT * FROM orders WHERE customer_email = ?",
  calls: 90,
  total_exec_time_ms: 4200,
  avg_exec_time_ms: 46.67,
  rows_examined_or_shared_blks_read: 180000,
  rows_sent_or_rows_returned: 90,
  lock_time_ms_or_temp_blks_written: 12,
  share_of_total_time: 0.85,
  coverage_mode: "window_delta",
  first_seen_at: "2026-11-07T11:00:00Z",
  last_seen_at: "2026-11-07T12:00:00Z",
};

const SAMPLE_OVERVIEW: DiagnosticsOverview = {
  datasource_id: DS_ID,
  engine: "mysql",
  compatibility_mode: "mysql",
  probe_state: "ready",
  state: "active",
  coverage_mode: "window_delta",
  from: "2026-11-07T11:00:00Z",
  to: "2026-11-07T12:00:00Z",
  compare_from: "2026-11-07T10:00:00Z",
  compare_to: "2026-11-07T11:00:00Z",
  last_collected_at: "2026-11-07T12:00:00Z",
  snapshot_count: 6,
  load_trend: [
    {
      bucket_start: "2026-11-07T11:30:00Z",
      bucket_end: "2026-11-07T11:45:00Z",
      total_exec_time_ms: 1400,
      calls: 40,
      avg_exec_time_ms: 35,
      reset_detected: false,
    },
    {
      bucket_start: "2026-11-07T11:45:00Z",
      bucket_end: "2026-11-07T12:00:00Z",
      total_exec_time_ms: 2800,
      calls: 50,
      avg_exec_time_ms: 56,
      reset_detected: false,
    },
  ],
  top_statements: [
    {
      statement_id: STMT_ID,
      engine_fingerprint: "fp_orders_customer_scan",
      database_or_schema: "orders_db",
      template_preview: "SELECT * FROM orders WHERE customer_email = ?",
      calls: 90,
      total_exec_time_ms: 4200,
      avg_exec_time_ms: 46.67,
      share_of_total_time: 0.85,
    },
  ],
  scatter_points: [
    {
      statement_id: STMT_ID,
      engine_fingerprint: "fp_orders_customer_scan",
      database_or_schema: "orders_db",
      calls: 90,
      avg_exec_time_ms: 46.67,
      total_exec_time_ms: 4200,
      rows_examined_or_shared_blks_read: 180000,
    },
  ],
  heatmap: [
    {
      statement_id: STMT_ID,
      engine_fingerprint: "fp_orders_customer_scan",
      database_or_schema: "orders_db",
      buckets: [
        {
          bucket_start: "2026-11-07T11:30:00Z",
          total_exec_time_ms: 1400,
          calls: 40,
          unobserved_below_top_n: false,
        },
        {
          bucket_start: "2026-11-07T11:45:00Z",
          total_exec_time_ms: 2800,
          calls: 50,
          unobserved_below_top_n: false,
        },
      ],
    },
  ],
  regressions: [
    {
      statement_id: STMT_ID,
      engine_fingerprint: "fp_orders_customer_scan",
      database_or_schema: "orders_db",
      baseline_calls: 6,
      baseline_avg_ms: 18.5,
      current_calls: 90,
      current_avg_ms: 46.67,
      delta_avg_ms: 28.17,
      regression_ratio: 1.52,
      insufficient_samples: true,
    },
  ],
  new_high_load: [
    {
      statement_id: STMT_ID,
      engine_fingerprint: "fp_orders_customer_scan",
      database_or_schema: "orders_db",
      current_calls: 90,
      current_total_ms: 4200,
      current_avg_ms: 46.67,
      unobserved_note: "Unobserved in baseline window only means no record within Top-N.",
    },
  ],
  statements: [SAMPLE_STATEMENT_SUMMARY],
};

const SAMPLE_AI_REPORT: DiagnosticAiReport = {
  report_id: REPORT_ID,
  datasource_id: DS_ID,
  statement_id: STMT_ID,
  window_start: "2026-11-07T11:00:00Z",
  window_end: "2026-11-07T12:00:00Z",
  coverage_mode: "window_delta",
  desensitized_context_summary:
    "Desensitized 1 table (tbl_1) and 1 column (col_1); 0 literal values sent to LLM.",
  observed_facts: [
    {
      metric: "avg_exec_time_ms",
      value: "46.67 ms",
      window: "11:00–12:00",
    },
  ],
  root_cause_hypotheses: [
    {
      hypothesis: "Missing composite index on customer_email causing full table scan.",
      confidence: "high",
      evidence_basis: "rows_examined_or_shared_blks_read = 180000 for 90 calls",
    },
  ],
  optimization_recommendations: [
    {
      kind: "create_index",
      title: "Add index idx_orders_customer_email",
      rationale: "Reduces scanned rows per query from 2000 to 1.",
      applicable_conditions: ["Low write amplification on orders_db.orders"],
      candidate_sql:
        "-- online index creation\nCREATE INDEX idx_orders_customer_email ON orders (customer_email) WHERE status = 'active' AND priority > 0;",
      risk_notes: ["Verify table size and online DDL settings before execution"],
    },
  ],
  verification_steps: [
    "Check rows_examined delta in SQL Performance Diagnostics after index creation.",
  ],
  candidate_sql: "CREATE INDEX idx_orders_customer_email ON orders (customer_email);",
  candidate_statement_kind: "ddl",
  submission_state: "draft_ready",
  flow_id: null,
  ephemeral_run_id: null,
  submitted_order_id: null,
  submitted_order_display_no: null,
  created_at: "2026-11-07T12:01:00Z",
};

const SAMPLE_DETAIL: DiagnosticStatementDetail = {
  statement_id: STMT_ID,
  datasource_id: DS_ID,
  engine: "mysql",
  compatibility_mode: "mysql",
  engine_fingerprint: "fp_orders_customer_scan",
  yearning_fingerprint: "yr_fp_orders_01",
  database_or_schema: "orders_db",
  template_status: "available",
  template_sql: "SELECT * FROM `tbl_1` WHERE `col_1` = ?",
  template_note: null,
  coverage_mode: "window_delta",
  from: "2026-11-07T11:00:00Z",
  to: "2026-11-07T12:00:00Z",
  summary: SAMPLE_STATEMENT_SUMMARY,
  cost_unit_notes: {
    rows_examined: "MySQL SUM_ROWS_EXAMINED delta in window",
    lock_time_ms: "MySQL SUM_LOCK_TIME converted from picoseconds to ms",
  },
  trend_series: [
    {
      bucket_start: "2026-11-07T11:30:00Z",
      bucket_end: "2026-11-07T11:45:00Z",
      calls: 40,
      avg_exec_time_ms: 35,
      total_exec_time_ms: 1400,
      reset_detected: false,
      unobserved_below_top_n: false,
    },
    {
      bucket_start: "2026-11-07T11:45:00Z",
      bucket_end: "2026-11-07T12:00:00Z",
      calls: 50,
      avg_exec_time_ms: 56,
      total_exec_time_ms: 2800,
      reset_detected: false,
      unobserved_below_top_n: false,
    },
  ],
  cost_series: [
    {
      bucket_start: "2026-11-07T11:30:00Z",
      bucket_end: "2026-11-07T11:45:00Z",
      rows_examined_or_shared_blks_read: 80000,
      rows_sent_or_rows_returned: 40,
      lock_time_ms_or_temp_blks_written: 5,
      shared_blks_hit: null,
    },
  ],
  order_comparison: {
    linked_change_order_id: null,
    linked_order_display_no: null,
    relation_kind: null,
    reference_executed_at: "2026-11-07T11:40:00Z",
    insufficient_samples: true,
    before_window: {
      from: "2026-11-07T11:00:00Z",
      to: "2026-11-07T11:40:00Z",
      bucket_count: 2,
      calls: 40,
      total_exec_time_ms: 1400,
      avg_exec_time_ms: 35,
    },
    after_window: {
      from: "2026-11-07T11:40:00Z",
      to: "2026-11-07T12:00:00Z",
      bucket_count: 2,
      calls: 50,
      total_exec_time_ms: 1100,
      avg_exec_time_ms: 22,
    },
    candidate_orders_in_window: [
      {
        change_order_id: ORDER_ID,
        display_no: "YR-20261107-000188",
        title: "Add customer_email index",
        state: "completed",
        executed_at: "2026-11-07T11:40:00Z",
        relation_hint: "Timestamp overlap on datasource",
        confirmed_link_id: null,
      },
    ],
  },
  latest_ai_report: null,
};

function renderDiagnostics(initialEntry = "/admin/diagnostics"): ReturnType<typeof render> {
  const queryClient = new QueryClient({
    defaultOptions: {
      queries: { retry: false },
      mutations: { retry: false },
    },
  });
  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={[initialEntry]}>
        <Routes>
          <Route path="/admin/diagnostics" element={<AdminDiagnosticsPage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("SQL Performance Diagnostics (FE-F13-SQL-DIAGNOSTICS)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(adminApi.listDiagnosticDatasources).mockResolvedValue({
      items: SAMPLE_DATASOURCES,
    } as never);
    vi.mocked(adminApi.getDiagnosticsOverview).mockResolvedValue(SAMPLE_OVERVIEW as never);
    vi.mocked(adminApi.getDiagnosticStatementDetail).mockResolvedValue(SAMPLE_DETAIL as never);
    vi.mocked(changeDraftsApi.listCurrentUserFlows).mockResolvedValue({
      items: [SAMPLE_FLOW],
    } as never);
  });

  it("renders the 6 overview charts, accessible table view toggle, and sortable statement table", async () => {
    renderDiagnostics(`/admin/diagnostics?datasource_id=${DS_ID}`);

    expect(await screen.findByTestId("diag-overview-charts")).toBeInTheDocument();
    expect(screen.getByTestId("diag-chart-load-trend")).toBeInTheDocument();
    expect(screen.getByTestId("diag-chart-top10")).toBeInTheDocument();
    expect(screen.getByTestId("diag-chart-scatter")).toBeInTheDocument();
    expect(screen.getByTestId("diag-chart-heatmap")).toBeInTheDocument();
    expect(screen.getByTestId("diag-chart-regressions")).toBeInTheDocument();
    expect(screen.getByTestId("diag-chart-new-high-load")).toBeInTheDocument();

    // Insufficient samples badge on regression item
    expect(
      screen.getByTestId(`diag-regression-insufficient-${STMT_ID}`),
    ).toBeInTheDocument();

    // Unobserved note on new high-load item
    expect(screen.getByTestId(`diag-unobserved-note-${STMT_ID}`)).toHaveTextContent(
      "Unobserved in baseline window only means no record within Top-N.",
    );

    // Toggle accessible table view so no metric relies on hover
    fireEvent.click(screen.getByTestId("diag-toggle-table-metrics"));
    expect(await screen.findByTestId("diag-load-trend-a11y-table")).toBeInTheDocument();

    // Sortable statement table
    expect(screen.getByTestId("diag-statements-table")).toBeInTheDocument();
    expect(screen.getByTestId(`diag-statement-row-${STMT_ID}`)).toBeInTheDocument();
    fireEvent.click(screen.getByTestId("diag-sort-avg_exec_time_ms"));
    await waitFor(() => {
      expect(adminApi.getDiagnosticsOverview).toHaveBeenCalledWith(
        DS_ID,
        expect.objectContaining({ sort_by: "avg_exec_time_ms" }),
      );
    });
  });

  it("triggers a manual snapshot and surfaces rate-limit error 7003 when throttled", async () => {
    vi.mocked(adminApi.triggerManualDiagnosticsSnapshot).mockResolvedValueOnce({
      snapshot_id: "77777777-7777-4777-8777-777777777777",
      datasource_id: DS_ID,
      trigger_kind: "manual",
      probe_state: "ready",
      statement_count: 15,
      duration_ms: 42,
      reset_detected: false,
      gap_detected: false,
      sampled_at: "2026-11-07T12:02:00Z",
    } as never);

    renderDiagnostics(`/admin/diagnostics?datasource_id=${DS_ID}`);
    const snapBtn = await screen.findByTestId("diag-manual-snapshot-btn");
    fireEvent.click(snapBtn);

    expect(await screen.findByTestId("diag-snapshot-notice")).toBeInTheDocument();

    // Now simulate rate-limit 7003
    vi.mocked(adminApi.triggerManualDiagnosticsSnapshot).mockRejectedValueOnce(
      new BusinessError({
        err_code: 7003,
        message: "manual snapshot rate limited",
        request_id: "req-7003-id",
        retryable: true,
      }),
    );
    fireEvent.click(snapBtn);
    expect(await screen.findByTestId("diag-snapshot-error")).toHaveTextContent(
      "req-7003-id",
    );
  });

  it("opens statement detail via URL query param, confirms order link, runs 4-section AI diagnosis, prechecks, and submits change order", async () => {
    vi.mocked(adminApi.confirmDiagnosticOrderLink).mockResolvedValue({
      id: "88888888-8888-4888-8888-888888888888",
      datasource_id: DS_ID,
      statement_id: STMT_ID,
      target_statement_id: null,
      change_order_id: ORDER_ID,
      order_display_no: "YR-20261107-000188",
      relation_kind: "same_fingerprint",
      order_executed_at: "2026-11-07T11:40:00Z",
      confirmed_by_user_id: "0198d9cc-e65d-7b9d-a8aa-3c81945f99ac",
      created_at: "2026-11-07T12:03:00Z",
    } as never);

    vi.mocked(adminApi.diagnoseStatementWithAi).mockResolvedValue(
      SAMPLE_AI_REPORT as never,
    );

    const precheckedReport: DiagnosticAiReport = {
      ...SAMPLE_AI_REPORT,
      submission_state: "prechecked",
      flow_id: FLOW_ID,
      ephemeral_run_id: RUN_ID,
    };
    vi.mocked(adminApi.precheckDiagnosticOptimizationDraft).mockResolvedValue({
      report: precheckedReport,
      run_id: RUN_ID,
      result_ref: "ephemeral_reviews/66666666-6666-4666-8666-666666666666",
    } as never);

    const submittedReport: DiagnosticAiReport = {
      ...precheckedReport,
      submission_state: "submitted",
      submitted_order_id: ORDER_ID,
      submitted_order_display_no: "YR-20261107-000199",
    };
    vi.mocked(adminApi.submitDiagnosticOptimizationOrder).mockResolvedValue({
      report: submittedReport,
      order_id: ORDER_ID,
      order_display_no: "YR-20261107-000199",
      order_state: "pending_approval",
    } as never);

    renderDiagnostics(
      `/admin/diagnostics?datasource_id=${DS_ID}&statement_id=${STMT_ID}`,
    );

    // Detail panel & 3 detail charts
    expect(await screen.findByTestId("diag-statement-detail-panel")).toBeInTheDocument();
    expect(screen.getByTestId("diag-detail-template-sql")).toHaveTextContent(
      "SELECT * FROM `tbl_1` WHERE `col_1` = ?",
    );
    expect(screen.getByTestId("diag-detail-chart-trend")).toBeInTheDocument();
    expect(screen.getByTestId("diag-detail-chart-cost")).toBeInTheDocument();
    expect(screen.getByTestId("diag-detail-cost-notes")).toHaveTextContent(
      "rows_examined",
    );
    expect(screen.getByTestId("diag-detail-chart-comparison")).toBeInTheDocument();

    // Confirm order link
    fireEvent.click(screen.getByTestId("diag-confirm-order-link-btn"));
    expect(await screen.findByTestId("diag-confirmed-order-no")).toHaveTextContent(
      "YR-20261107-000188",
    );

    // Run 4-section AI diagnosis
    fireEvent.click(screen.getByTestId("diag-run-ai-btn"));
    expect(await screen.findByTestId("diag-ai-report")).toBeInTheDocument();
    expect(screen.getByTestId("diag-ai-observed-facts")).toBeInTheDocument();
    expect(screen.getByTestId("diag-ai-hypotheses")).toBeInTheDocument();
    expect(screen.getByTestId("diag-ai-recommendations")).toBeInTheDocument();
    expect(screen.getByTestId("diag-ai-verification-steps")).toBeInTheDocument();

    // Advance recommendation: run formal AI precheck
    const precheckBtn = screen.getByTestId("diag-precheck-btn");
    fireEvent.click(precheckBtn);
    await waitFor(() => {
      expect(adminApi.precheckDiagnosticOptimizationDraft).toHaveBeenCalledWith(
        DS_ID,
        REPORT_ID,
        expect.objectContaining({
          flow_id: FLOW_ID,
        }),
        expect.any(Object),
      );
    });

    // Submit optimization change order
    const submitBtn = await screen.findByTestId("diag-submit-order-btn");
    await waitFor(() => {
      expect(submitBtn).not.toBeDisabled();
    });
    fireEvent.click(submitBtn);

    expect(await screen.findByTestId("diag-submitted-order-no")).toHaveTextContent(
      "YR-20261107-000199",
    );
  });

  it("maps business error codes 7001–7008 to localized error keys", () => {
    const cases: Array<{ code: number; operationId: string; expectedKey: string }> = [
      {
        code: 7001,
        operationId: "getDiagnosticsOverview",
        expectedKey: "errors.DIAGNOSTICS_DATASOURCE_FORBIDDEN",
      },
      {
        code: 7002,
        operationId: "triggerManualDiagnosticsSnapshot",
        expectedKey: "errors.DIAGNOSTICS_NOT_ENABLED",
      },
      {
        code: 7003,
        operationId: "triggerManualDiagnosticsSnapshot",
        expectedKey: "errors.DIAGNOSTICS_ENGINE_UNSUPPORTED",
      },
      {
        code: 7004,
        operationId: "triggerManualDiagnosticsSnapshot",
        expectedKey: "errors.DIAGNOSTICS_STATS_EXTENSION_DISABLED",
      },
      {
        code: 7005,
        operationId: "triggerManualDiagnosticsSnapshot",
        expectedKey: "errors.DIAGNOSTICS_INSUFFICIENT_DB_PRIVILEGES",
      },
      {
        code: 7006,
        operationId: "triggerManualDiagnosticsSnapshot",
        expectedKey: "errors.DIAGNOSTICS_SNAPSHOT_RATE_LIMITED",
      },
      {
        code: 7007,
        operationId: "triggerManualDiagnosticsSnapshot",
        expectedKey: "errors.DIAGNOSTICS_CIRCUIT_BREAKER_OPEN",
      },
      {
        code: 7008,
        operationId: "diagnoseStatementWithAi",
        expectedKey: "errors.DIAGNOSTICS_AI_TEMPLATE_UNAVAILABLE",
      },
    ];

    for (const item of cases) {
      const err = new BusinessError({
        err_code: item.code,
        message: "diag error",
        request_id: "req-diag-test",
        retryable: false,
      });
      const display = describeError(err, item.operationId);
      expect(display.contractViolation).toBe(false);
      expect(display.messageKey).toBe(item.expectedKey);
      expect(i18n.exists(display.messageKey)).toBe(true);
    }
  });

  it("covers formatReadableSqlLabel and DiagnosticsOverviewCharts interactive & empty branches", () => {
    expect(formatReadableSqlLabel({ templatePreview: "SELECT 1" })).toBe("SELECT 1");
    expect(
      formatReadableSqlLabel({
        templatePreview:
          "SELECT id, customer_email, status, total_amount FROM orders WHERE customer_email = ? AND status = ?",
        maxLength: 40,
      }),
    ).toContain("SELECT orders · WHERE");
    expect(
      formatReadableSqlLabel({
        templatePreview:
          "SELECT o.id, o.customer_email, o.status, o.total_amount FROM orders JOIN customers ON o.cid = customers.id",
        maxLength: 60,
      }),
    ).toContain("SELECT orders · JOIN customers");
    expect(
      formatReadableSqlLabel({
        templatePreview:
          "UPDATE orders SET status = ?, updated_at = NOW(), audit_note = ? WHERE id = ? AND tenant_id = ?",
        maxLength: 32,
      }),
    ).toMatch(/…$/);
    expect(
      formatReadableSqlLabel({
        templatePreview: null,
        databaseOrSchema: "orders_db",
        fallbackIndex: 2,
      }),
    ).toBe("orders_db · SQL #3");
    expect(
      formatReadableSqlLabel({
        templatePreview: "",
        databaseOrSchema: "orders_db",
      }),
    ).toBe("orders_db · SQL");
    expect(
      formatReadableSqlLabel({
        templatePreview: "",
        databaseOrSchema: "",
        fallbackIndex: 0,
      }),
    ).toBe("SQL #1");
    expect(
      formatReadableSqlLabel({
        templatePreview: "",
        databaseOrSchema: "",
      }),
    ).toBe("SQL Statement");
    expect(formatShortTime("")).toBe("—");
    expect(formatShortTime("invalid-iso")).toBe("invalid-iso");

    const onBrushWindowChange = vi.fn();
    const onSelectStatement = vi.fn();

    const { rerender } = render(
      <DiagnosticsOverviewCharts
        loadTrend={[
          ...SAMPLE_OVERVIEW.load_trend,
          {
            bucket_start: "invalid-date",
            bucket_end: "2026-11-07T12:15:00Z",
            total_exec_time_ms: 800,
            calls: 15,
            avg_exec_time_ms: 53.3,
            reset_detected: false,
          },
        ]}
        topStatements={[
          ...SAMPLE_OVERVIEW.top_statements,
          {
            statement_id: "stmt-no-preview",
            engine_fingerprint: "fp_orders_no_preview",
            database_or_schema: "orders_db",
            template_preview: null,
            calls: 10,
            total_exec_time_ms: 500,
            avg_exec_time_ms: 50,
            share_of_total_time: 0.1,
          },
          {
            statement_id: "stmt-insert",
            engine_fingerprint: "fp_insert",
            database_or_schema: "orders_db",
            template_preview: "INSERT INTO order_items_long_table_name VALUES (?)",
            calls: 8,
            total_exec_time_ms: 400,
            avg_exec_time_ms: 50,
            share_of_total_time: 0.08,
          },
          {
            statement_id: "stmt-update",
            engine_fingerprint: "fp_update",
            database_or_schema: "orders_db",
            template_preview: "UPDATE orders SET status = ?",
            calls: 7,
            total_exec_time_ms: 350,
            avg_exec_time_ms: 50,
            share_of_total_time: 0.07,
          },
          {
            statement_id: "stmt-delete",
            engine_fingerprint: "fp_delete",
            database_or_schema: "orders_db",
            template_preview: "DELETE FROM order_logs WHERE id = ?",
            calls: 6,
            total_exec_time_ms: 300,
            avg_exec_time_ms: 50,
            share_of_total_time: 0.06,
          },
          {
            statement_id: "stmt-create",
            engine_fingerprint: "fp_create",
            database_or_schema: "orders_db",
            template_preview: "CREATE TABLE IF NOT EXISTS temp_orders (id int)",
            calls: 5,
            total_exec_time_ms: 250,
            avg_exec_time_ms: 50,
            share_of_total_time: 0.05,
          },
          {
            statement_id: "stmt-alter",
            engine_fingerprint: "fp_alter",
            database_or_schema: "orders_db",
            template_preview: "ALTER TABLE orders ADD COLUMN note text",
            calls: 4,
            total_exec_time_ms: 200,
            avg_exec_time_ms: 50,
            share_of_total_time: 0.04,
          },
          {
            statement_id: "stmt-drop",
            engine_fingerprint: "fp_drop",
            database_or_schema: "orders_db",
            template_preview: "DROP TABLE IF EXISTS temp_orders",
            calls: 3,
            total_exec_time_ms: 150,
            avg_exec_time_ms: 50,
            share_of_total_time: 0.03,
          },
          {
            statement_id: "stmt-set",
            engine_fingerprint: "fp_set",
            database_or_schema: "orders_db",
            template_preview: "SET search_path = public",
            calls: 2,
            total_exec_time_ms: 100,
            avg_exec_time_ms: 50,
            share_of_total_time: 0.02,
          },
          {
            statement_id: "stmt-show",
            engine_fingerprint: "fp_show",
            database_or_schema: "",
            template_preview: "SHOW VARIABLES",
            calls: 1,
            total_exec_time_ms: 50,
            avg_exec_time_ms: 50,
            share_of_total_time: 0.01,
          },
        ]}
        scatterPoints={[
          ...SAMPLE_OVERVIEW.scatter_points,
          {
            statement_id: "stmt-scatter-2",
            engine_fingerprint: "fp_orders_scatter_2",
            database_or_schema: "orders_db",
            calls: 12,
            avg_exec_time_ms: 40,
            total_exec_time_ms: 480,
            rows_examined_or_shared_blks_read: 1000,
          },
        ]}
        heatmap={[
          {
            statement_id: STMT_ID,
            engine_fingerprint: "fp_orders_customer_scan",
            database_or_schema: "orders_db",
            buckets: [
              {
                bucket_start: "2026-11-07T11:00:00Z",
                total_exec_time_ms: 3500,
                calls: 20,
                unobserved_below_top_n: false,
              },
              {
                bucket_start: "2026-11-07T11:15:00Z",
                total_exec_time_ms: 2000,
                calls: 15,
                unobserved_below_top_n: false,
              },
              {
                bucket_start: "2026-11-07T11:30:00Z",
                total_exec_time_ms: 800,
                calls: 10,
                unobserved_below_top_n: false,
              },
              {
                bucket_start: "2026-11-07T11:45:00Z",
                total_exec_time_ms: 100,
                calls: 2,
                unobserved_below_top_n: false,
              },
              {
                bucket_start: "2026-11-07T12:00:00Z",
                total_exec_time_ms: 0,
                calls: 0,
                unobserved_below_top_n: false,
              },
              {
                bucket_start: "2026-11-07T12:15:00Z",
                total_exec_time_ms: 0,
                calls: 0,
                unobserved_below_top_n: true,
              },
            ],
          },
        ]}
        regressions={SAMPLE_OVERVIEW.regressions}
        newHighLoad={SAMPLE_OVERVIEW.new_high_load}
        statements={SAMPLE_OVERVIEW.statements}
        showAccessibleTables={true}
        brushWindow={{
          startTime: "2026-11-07T11:00:00Z",
          endTime: "2026-11-07T12:00:00Z",
        }}
        onBrushWindowChange={onBrushWindowChange}
        onSelectStatement={onSelectStatement}
      />,
    );

    // Click brush select latest & reset
    fireEvent.click(screen.getByTestId("diag-brush-select-latest"));
    expect(onBrushWindowChange).toHaveBeenCalled();

    fireEvent.click(screen.getByTestId("diag-brush-reset"));
    expect(onBrushWindowChange).toHaveBeenLastCalledWith(null);

    // Click top10 item, scatter item, heatmap row, regression row, new high load row
    fireEvent.click(screen.getByTestId(`diag-top10-item-${STMT_ID}`));
    expect(onSelectStatement).toHaveBeenLastCalledWith(STMT_ID);

    fireEvent.click(screen.getByTestId(`diag-scatter-item-${STMT_ID}`));
    expect(onSelectStatement).toHaveBeenLastCalledWith(STMT_ID);

    fireEvent.click(screen.getByTestId(`diag-heatmap-row-${STMT_ID}`));
    expect(onSelectStatement).toHaveBeenLastCalledWith(STMT_ID);

    fireEvent.click(screen.getByTestId(`diag-regression-row-${STMT_ID}`));
    expect(onSelectStatement).toHaveBeenLastCalledWith(STMT_ID);

    fireEvent.click(screen.getByTestId(`diag-new-high-load-row-${STMT_ID}`));
    expect(onSelectStatement).toHaveBeenLastCalledWith(STMT_ID);

    // Rerender with empty arrays and brushWindow=null to cover empty states
    rerender(
      <DiagnosticsOverviewCharts
        loadTrend={[]}
        topStatements={[]}
        scatterPoints={[]}
        heatmap={[]}
        regressions={[]}
        newHighLoad={[]}
        statements={[]}
        showAccessibleTables={false}
        brushWindow={null}
        onBrushWindowChange={onBrushWindowChange}
        onSelectStatement={onSelectStatement}
      />,
    );
  });

  it("covers DiagnosticStatementDetailPanel template_unavailable, empty candidates, and error handlers", async () => {
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });

    vi.mocked(adminApi.diagnoseStatementWithAi).mockRejectedValueOnce(
      new BusinessError({
        err_code: 7008,
        message: "template unavailable",
        request_id: "req-err-ai",
        retryable: false,
      }),
    );

    const onClose = vi.fn();
    render(
      <QueryClientProvider client={queryClient}>
        <MemoryRouter>
          <DiagnosticStatementDetailPanel
            detail={{
              ...SAMPLE_DETAIL,
              template_status: "unavailable",
              template_sql: null,
              order_comparison: {
                ...SAMPLE_DETAIL.order_comparison,
                before_window: null,
                candidate_orders_in_window: [],
              },
            }}
            autoFocusAi={false}
            onClose={onClose}
          />
        </MemoryRouter>
      </QueryClientProvider>,
    );

    const runAiBtn = screen.getByTestId("diag-run-ai-btn");
    fireEvent.click(runAiBtn);
    expect(await screen.findByTestId("diag-ai-error")).toBeInTheDocument();

    fireEvent.click(screen.getByTestId("diag-detail-close-btn"));
    expect(onClose).toHaveBeenCalled();

    // Render with latest_ai_report + candidate_orders_in_window to cover form inputs & error branches
    vi.mocked(adminApi.confirmDiagnosticOrderLink).mockRejectedValueOnce(
      new BusinessError({
        err_code: 7006,
        message: "rate limited",
        request_id: "req-err-link",
        retryable: true,
      }),
    );
    vi.mocked(adminApi.precheckDiagnosticOptimizationDraft).mockRejectedValueOnce(
      new BusinessError({
        err_code: 7007,
        message: "circuit breaker open",
        request_id: "req-err-pre",
        retryable: true,
      }),
    );
    vi.mocked(adminApi.submitDiagnosticOptimizationOrder).mockRejectedValueOnce(
      new BusinessError({
        err_code: 7005,
        message: "insufficient privileges",
        request_id: "req-err-sub",
        retryable: false,
      }),
    );

    render(
      <QueryClientProvider client={queryClient}>
        <MemoryRouter>
          <DiagnosticStatementDetailPanel
            detail={{
              ...SAMPLE_DETAIL,
              latest_ai_report: {
                ...SAMPLE_AI_REPORT,
                submission_state: "prechecked",
                ephemeral_run_id: RUN_ID,
              },
            }}
            autoFocusAi={false}
            onClose={onClose}
          />
        </MemoryRouter>
      </QueryClientProvider>,
    );

    const confirmLinkBtn = screen.getAllByTestId("diag-confirm-order-link-btn").at(-1);
    if (!confirmLinkBtn) throw new Error("missing confirm link button");
    fireEvent.click(confirmLinkBtn);
    await waitFor(() => {
      expect(adminApi.confirmDiagnosticOrderLink).toHaveBeenCalled();
    });

    fireEvent.change(screen.getByTestId("diag-order-title-input"), {
      target: { value: "Custom Optimize Title" },
    });
    expect(screen.getByTestId("diag-candidate-sql-textarea")).toHaveTextContent(
      "CREATE INDEX idx_orders_customer_email ON orders (customer_email);",
    );
    expect(
      screen
        .getByTestId("diag-candidate-sql-textarea")
        .querySelector(".code-token-keyword"),
    ).not.toBeNull();
    fireEvent.change(screen.getByTestId("diag-order-remark-input"), {
      target: { value: "Custom remark" },
    });

    fireEvent.click(screen.getByTestId("diag-precheck-btn"));
    expect(await screen.findByTestId("diag-order-submission-error")).toBeInTheDocument();

    fireEvent.click(screen.getByTestId("diag-submit-order-btn"));
    await waitFor(() => {
      expect(adminApi.submitDiagnosticOptimizationOrder).toHaveBeenCalled();
    });
  });
});

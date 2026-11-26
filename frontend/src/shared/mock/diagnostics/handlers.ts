import { HttpResponse, http } from "msw";
import type { HttpHandler } from "msw";
import type {
  ConfirmDiagnosticOrderLinkRequest,
  DatasourceDiagnosticsConfig,
  DiagnosticAiReport,
  DiagnosticDatasourceSummary,
  DiagnosticOrderLink,
  PrecheckDiagnosticOptimizationDraftRequest,
  PrecheckDiagnosticOptimizationDraftResponse,
  SubmitDiagnosticOptimizationOrderResponse,
  TriggerManualDiagnosticsSnapshotResponse,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { apiRoute } from "@/shared/mock/api-route";
import { currentIdentityId } from "@/shared/mock/identity-store";
import { nowIso } from "@/shared/mock/kit";
import { buildOverview, buildStatementDetail } from "./builders";
import {
  DEFAULT_FLOW_ID,
  LINKED_COMPLETED_ORDER_ID,
  MYSQL_DS,
  PG_DS,
  SUBMITTED_OPT_ORDER_ID,
  TIDB_DS,
  buildSeedAiReport,
  businessError,
  diagnosticsState,
  nextUuid,
  statementsForDatasource,
  successEnvelope,
} from "./state";

const pause = (ms: number): Promise<void> =>
  new Promise((resolve) => setTimeout(resolve, ms));

export function diagnosticsFixtureHandlers(): HttpHandler[] {
  return [
    http.get(apiRoute("/admin/datasources/:datasourceId/diagnostics-config"), ({ params }) => {
      const dsId = String(params.datasourceId);
      const existing = diagnosticsState.world.configs.get(dsId) ?? {
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

    http.put(apiRoute("/admin/datasources/:datasourceId/diagnostics-config"), async ({ params, request }) => {
      const dsId = String(params.datasourceId);
      const body = (await request.json().catch(() => ({}))) as Partial<DatasourceDiagnosticsConfig>;
      const prev = diagnosticsState.world.configs.get(dsId);
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
      diagnosticsState.world.configs.set(dsId, updated);
      return HttpResponse.json(successEnvelope(updated));
    }),

    http.get(apiRoute("/admin/diagnostics/datasources"), () => {
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
          last_collected_at: diagnosticsState.world.lastCollectedAt.get(MYSQL_DS.id) ?? "2026-11-19T09:00:00Z",
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
          last_collected_at: diagnosticsState.world.lastCollectedAt.get(PG_DS.id) ?? "2026-11-19T09:00:00Z",
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
          last_collected_at: diagnosticsState.world.lastCollectedAt.get(TIDB_DS.id) ?? "2026-11-19T09:00:00Z",
        },
      ];
      return HttpResponse.json(successEnvelope({ items }));
    }),

    http.get(apiRoute("/admin/diagnostics/datasources/:datasourceId/overview"), ({ params }) => {
      const dsId = String(params.datasourceId);
      return HttpResponse.json(successEnvelope(buildOverview(dsId)));
    }),

    http.post(apiRoute("/admin/diagnostics/datasources/:datasourceId/snapshots"), async ({ params }) => {
      const dsId = String(params.datasourceId);
      const ts = nowIso();
      diagnosticsState.world.lastCollectedAt.set(dsId, ts);
      diagnosticsState.world.snapshotCounts.set(dsId, (diagnosticsState.world.snapshotCounts.get(dsId) ?? 12) + 1);
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
      apiRoute("/admin/diagnostics/datasources/:datasourceId/statements/:statementId"),
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
      apiRoute("/admin/diagnostics/datasources/:datasourceId/statements/:statementId/order-links"),
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
        diagnosticsState.world.orderLinks.set(stmtId, link);
        return HttpResponse.json(successEnvelope(link));
      },
    ),

    http.post(
      apiRoute("/admin/diagnostics/datasources/:datasourceId/statements/:statementId/ai-reports"),
      async ({ params }) => {
        const dsId = String(params.datasourceId);
        const stmtId = String(params.statementId);
        await pause(380);
        const report: DiagnosticAiReport = {
          ...buildSeedAiReport(dsId, stmtId),
          report_id: nextUuid("d9999999"),
          created_at: nowIso(),
        };
        diagnosticsState.world.aiReports.set(stmtId, report);
        return HttpResponse.json(successEnvelope(report));
      },
    ),

    http.post(
      apiRoute("/admin/diagnostics/datasources/:datasourceId/ai-reports/:reportId/prechecks"),
      async ({ params, request }) => {
        const reportId = String(params.reportId);
        const body = (await request.json().catch(() => ({}))) as Partial<PrecheckDiagnosticOptimizationDraftRequest>;
        let matchedKey: string | null = null;
        let matchedReport: DiagnosticAiReport | null = null;
        for (const [key, value] of diagnosticsState.world.aiReports.entries()) {
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
        diagnosticsState.world.aiReports.set(matchedKey, updated);
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
      apiRoute("/admin/diagnostics/datasources/:datasourceId/ai-reports/:reportId/order-submissions"),
      async ({ params }) => {
        const reportId = String(params.reportId);
        let matchedKey: string | null = null;
        let matchedReport: DiagnosticAiReport | null = null;
        for (const [key, value] of diagnosticsState.world.aiReports.entries()) {
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
        diagnosticsState.world.aiReports.set(matchedKey, updated);
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

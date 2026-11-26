import { HttpResponse, http } from "msw";
import type { HttpHandler } from "msw";
import { FINGERPRINT_MAX_UNIQUE } from "@/features/review/bulk-constants";
import { digestSqlText } from "@/features/review/bulk-import/sql-digest";
import { apiRoute } from "@/shared/mock/api-route";
import { readStoredAuthBehavior } from "@/shared/mock/auth-scenario-store";
import { CATALOG_FLOW_IDS, reviewCatalogFlowViews } from "@/shared/mock/flow-catalog";
import {
  MOCK_IDENTITY_IDS,
  currentIdentity,
  currentIdentityId,
  holdsActorSlot,
  readStoredIdentity,
} from "@/shared/mock/identity-store";
import { fixtureUuid as uuid, nowIsoSeconds as now } from "@/shared/mock/kit";
import { queryFlowsCatalogPage } from "@/shared/mock/query-fixture";
import {
  advanceEphemeralRun,
  grantedSubmitterDatasource,
  metadataWorld,
} from "./precheck-engine";
import {
  businessError,
  draftPublic,
  emit,
  pageOf,
  parseRequestedSchedule,
  problem,
  revisionConflict,
  successEnvelope,
  world,
} from "./state";
import {
  FIXTURE_DATASOURCE_ID,
  FIXTURE_FLOW_ID,
  FIXTURE_SQL_MARKER,
  type FixtureDraft,
  type FixtureRun,
} from "./types";

export function reviewDraftHandlers(): HttpHandler[] {
  return [
    http.get(apiRoute("/users/me/flows"), ({ request }) => {
      const flowType = new URL(request.url).searchParams.get("flow_type");
      const behavior = readStoredAuthBehavior();
      if (flowType === "query_access") {
        return HttpResponse.json(
          successEnvelope(pageOf(queryFlowsCatalogPage(behavior !== "expired").items as unknown[], null, null)),
        );
      }
      if (flowType !== "change_review" || !(behavior === "admin" || (behavior !== "expired" && readStoredIdentity() === "owner"))) {
        return HttpResponse.json(successEnvelope(pageOf([], null, null)));
      }
      const catalogFlows = reviewCatalogFlowViews(MOCK_IDENTITY_IDS.reviewer, MOCK_IDENTITY_IDS.executor, MOCK_IDENTITY_IDS.owner);
      const flow = {
        id: FIXTURE_FLOW_ID,
        name: "默认审核流程",
        flow_type: "change_review",
        enabled: true,
        status: "enabled",
        rule_set_id: null,
        stages: [
          {
            position: 1,
            datasource_id: FIXTURE_DATASOURCE_ID,
            datasource_name: "orders-mysql",
            datasource_engine: "mysql",
            datasource_compatibility_mode: "mysql",
            datasource_database_name: "app",
            approval_steps: [
              { position: 1, actors: [{ user_id: MOCK_IDENTITY_IDS.reviewer }, { user_id: MOCK_IDENTITY_IDS.owner }] },
            ],
            execution_actors: [{ user_id: MOCK_IDENTITY_IDS.executor }, { user_id: MOCK_IDENTITY_IDS.owner }],
          },
        ],
        approval_steps: undefined,
        query_capabilities: undefined,
        version: world.flowVersion,
        created_at: "2026-08-01T00:00:00Z",
        updated_at: "2026-08-01T00:00:00Z",
      };
      return HttpResponse.json(successEnvelope(pageOf([flow, ...catalogFlows], null, null)));
    }),

    http.get(apiRoute("/users/me/flows/:flowId/datasources/:datasourceId/metadata/schemas"), ({ params }) => {
      if (!grantedSubmitterDatasource(String(params.flowId), String(params.datasourceId))) {
        return businessError(2014, "flow not granted to the current user");
      }
      const schemas = metadataWorld(String(params.datasourceId)).schemas;
      return HttpResponse.json(successEnvelope({ items: schemas.map((name) => ({ name })) }));
    }),
    http.get(apiRoute("/users/me/flows/:flowId/datasources/:datasourceId/metadata/tables"), ({ params, request }) => {
      if (!grantedSubmitterDatasource(String(params.flowId), String(params.datasourceId))) {
        return businessError(2014, "flow not granted to the current user");
      }
      const schemaName = new URL(request.url).searchParams.get("schema_name") ?? "";
      const meta = metadataWorld(String(params.datasourceId));
      const schema = meta.schemas.find((candidate) => candidate === schemaName);
      if (schema === undefined) return businessError(1002, "schema not found");
      return HttpResponse.json(
        successEnvelope({
          items: meta.tables
            .filter((table) => table.schema_name === schemaName)
            .map((table) => ({ ...table })),
        }),
      );
    }),
    http.get(apiRoute("/users/me/flows/:flowId/datasources/:datasourceId/metadata/columns"), ({ params, request }) => {
      if (!grantedSubmitterDatasource(String(params.flowId), String(params.datasourceId))) {
        return businessError(2014, "flow not granted to the current user");
      }
      const url = new URL(request.url);
      const schemaName = url.searchParams.get("schema_name") ?? "";
      const tableName = url.searchParams.get("table_name") ?? "";
      const meta = metadataWorld(String(params.datasourceId));
      return HttpResponse.json(
        successEnvelope({
          items: meta.columns
            .filter((column) => column.schema_name === schemaName && column.table_name === tableName)
            .map((column) => ({ ...column })),
        }),
      );
    }),

    http.post(apiRoute("/change-drafts"), async ({ request }) => {
      const body = (await request.json()) as {
        flow_id: string;
        title: string;
        description?: string;
        requested_scheduled_for?: unknown;
        target_database?: unknown;
      };
      if (body.flow_id !== FIXTURE_FLOW_ID && !CATALOG_FLOW_IDS.has(body.flow_id)) {
        return businessError(2014, "flow not granted to the current user");
      }
      if (body.title.trim() === "") {
        return businessError(1001, "title is required");
      }
      let requested: string | null = null;
      if (body.requested_scheduled_for !== undefined) {
        requested = parseRequestedSchedule(body.requested_scheduled_for);
        if (requested === null) {
          return businessError(1001, "requested_scheduled_for must be a future RFC3339 time");
        }
      }
      const targetDatabase =
        typeof body.target_database === "string" && body.target_database.trim() !== ""
          ? body.target_database.trim()
          : null;
      const draft: FixtureDraft = {
        id: uuid(),
        owner_user_id: currentIdentityId(),
        flow_id: body.flow_id,
        title: body.title,
        description: body.description ?? null,
        requested_scheduled_for: requested,
        target_database: targetDatabase,
        revision: 1,
        state: "draft",
        has_sql: false,
        sql: null,
        sql_size_bytes: null,
        statement_count: null,
        review_run_id: null,
        version: 1,
        created_at: now(),
        updated_at: now(),
      };
      world.drafts.set(draft.id, draft);
      return HttpResponse.json(successEnvelope(draftPublic(draft)));
    }),

    http.get(apiRoute("/change-drafts"), ({ request }) => {
      const url = new URL(request.url);
      const limit = Number(url.searchParams.get("limit") ?? "50");
      const after = url.searchParams.get("after");
      const drafts = [...world.drafts.values()]
        .filter((draft) => holdsActorSlot(draft.owner_user_id))
        .sort((a, b) => a.created_at.localeCompare(b.created_at));
      return HttpResponse.json(successEnvelope(pageOf(drafts.map(draftPublic), limit, after)));
    }),

    http.get(apiRoute("/change-drafts/:draftId"), ({ params }) => {
      const draft = world.drafts.get(String(params.draftId));
      if (draft === undefined) return businessError(1002, "draft not found");
      return HttpResponse.json(successEnvelope(draftPublic(draft)));
    }),

    http.patch(apiRoute("/change-drafts/:draftId"), async ({ request, params }) => {
      const draft = world.drafts.get(String(params.draftId));
      if (draft === undefined) return businessError(1002, "draft not found");
      if (revisionConflict(request, draft)) {
        return businessError(1003, "draft revision changed elsewhere");
      }
      const body = (await request.json()) as {
        title?: string;
        description?: string;
        requested_scheduled_for?: unknown;
        target_database?: unknown;
      };
      if (typeof body.title === "string") draft.title = body.title;
      if (typeof body.description === "string") draft.description = body.description;
      if (body.target_database !== undefined) {
        if (body.target_database === null) {
          draft.target_database = null;
        } else if (typeof body.target_database === "string") {
          const trimmed = body.target_database.trim();
          draft.target_database = trimmed === "" ? null : trimmed;
        }
      }
      if (body.requested_scheduled_for !== undefined) {
        if (body.requested_scheduled_for === null) {
          draft.requested_scheduled_for = null;
        } else {
          const parsed = parseRequestedSchedule(body.requested_scheduled_for);
          if (parsed === null) {
            return businessError(1001, "requested_scheduled_for must be a future RFC3339 time");
          }
          draft.requested_scheduled_for = parsed;
        }
      }
      draft.version += 1;
      draft.updated_at = now();
      return HttpResponse.json(successEnvelope(draftPublic(draft)));
    }),

    http.delete(apiRoute("/change-drafts/:draftId"), ({ params }) => {
      const draft = world.drafts.get(String(params.draftId));
      if (draft === undefined) return businessError(1002, "draft not found");
      if (draft.state === "submitted") return businessError(1010, "submitted draft cannot be deleted");
      world.drafts.delete(draft.id);
      return HttpResponse.json(successEnvelope(null));
    }),

    http.put(apiRoute("/change-drafts/:draftId/sql"), async ({ request, params }) => {
      const draft = world.drafts.get(String(params.draftId));
      if (draft === undefined) return businessError(1002, "draft not found");
      if (revisionConflict(request, draft)) {
        return businessError(1003, "draft revision changed elsewhere");
      }
      const body = (await request.json()) as { sql: string };
      draft.sql = body.sql;
      draft.has_sql = body.sql.trim().length > 0;
      draft.sql_size_bytes = new TextEncoder().encode(body.sql).length;
      draft.statement_count = digestSqlText(body.sql).statementCount;
      draft.revision += 1;
      draft.version += 1;
      draft.updated_at = now();
      const wasReady = draft.state === "ready";
      if (draft.state === "ready") draft.state = "outdated";
      if (wasReady) {
        emit(
          `change-drafts/${draft.id}`,
          "io.yearning.v4.change_draft.state_changed",
          {
            aggregate_id: draft.id,
            from: "ready",
            to: "outdated",
            reason_code: "review_inputs_changed",
            aggregate_version: draft.version,
          },
          { kind: "system" },
        );
      }
      return HttpResponse.json(successEnvelope(draftPublic(draft)));
    }),

    http.post(apiRoute("/change-drafts/:draftId/sql-reveals"), ({ params }) => {
      const draft = world.drafts.get(String(params.draftId));
      if (draft === undefined) return businessError(1002, "draft not found");
      if (!holdsActorSlot(draft.owner_user_id)) {
        return problem(403, "forbidden", "only the draft owner may reveal the SQL");
      }
      return HttpResponse.json(
        successEnvelope({
          reveal_id: uuid(),
          sql: draft.sql ?? `${FIXTURE_SQL_MARKER} SELECT 1`,
          watermark: `${currentIdentity().username} · ${now()} · draft:${draft.id}`,
          valid_until: new Date(Date.now() + 10 * 60 * 1000).toISOString().replace(/\.\d{3}Z$/, "Z"),
        }),
      );
    }),

    http.post(apiRoute("/review-runs"), async ({ request }) => {
      const body = (await request.json().catch(() => null)) as {
        flow_id?: unknown;
        draft_id?: unknown;
        sql?: unknown;
        output_locale?: unknown;
        target_database?: unknown;
      } | null;
      if (
        body === null || typeof body.flow_id !== "string" || body.flow_id === "" ||
        typeof body.sql !== "string" || typeof body.draft_id !== "string" && body.draft_id !== undefined ||
        (body.target_database !== undefined && body.target_database !== null && typeof body.target_database !== "string")
      ) {
        return businessError(1001, "请求参数不合法");
      }
      let draftForRun: FixtureDraft | undefined;
      if (typeof body.draft_id === "string" && body.draft_id !== "") {
        draftForRun = world.drafts.get(body.draft_id);
        if (draftForRun === undefined) return businessError(1002, "draft not found");
        if (draftForRun.flow_id !== body.flow_id) return businessError(1001, "请求参数不合法");
        if (draftForRun.state === "submitted") return businessError(1010, "draft already submitted");
      }
      if (body.sql === "") {
        return businessError(1001, "业务处理失败", {
          detail: "SQL为空：请先在编辑器中输入SQL，再发起AI预审",
        });
      }
      const summaryLocale =
        (typeof body.output_locale === "string" && (body.output_locale === "en-US" || body.output_locale === "zh-CN"))
          ? body.output_locale
          : request.headers.get("accept-language")?.startsWith("en-US")
            ? "en-US"
            : "zh-CN";

      const digest = digestSqlText(body.sql);
      const runId = uuid();
      const runTargetDb =
        body.target_database === undefined
          ? (draftForRun?.target_database ?? null)
          : typeof body.target_database === "string" && body.target_database.trim() !== ""
            ? body.target_database.trim()
            : null;
      const run: FixtureRun = {
        id: runId,
        draft_id: typeof body.draft_id === "string" && body.draft_id !== "" ? body.draft_id : "",
        draft_revision: 0,
        state: "queued",
        statement_count: digest.statementCount,
        fingerprint_group_count: Math.min(digest.groupCount, FINGERPRINT_MAX_UNIQUE),
        stage_results: [
          {
            stage_position: 1,
            datasource_id: FIXTURE_DATASOURCE_ID,
            state: "pending",
            highest_severity: "none",
            gate_passed: false,
            finding_count: 0,
            evidence_count: 0,
            snapshot_hash: `snap-${runId.slice(0, 8)}-pending`,
            summary: null,
            summary_locale: null,
          },
        ],
        gate: { passed: false, reason_codes: ["stage_review_incomplete"] },
        failure_code: null,
        version: 1,
        created_at: now(),
        started_at: null,
        finished_at: null,
      };
      run.sql = body.sql;
      run.target_database = runTargetDb;
      run.digest = digest;
      world.runs.set(run.id, run);
      advanceEphemeralRun(run, summaryLocale);
      return HttpResponse.json(
        successEnvelope({ run_id: run.id, result_ref: `/review-runs/${run.id}` }),
      );
    }),
  ];
}

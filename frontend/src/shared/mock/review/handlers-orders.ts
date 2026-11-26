import { HttpResponse, http } from "msw";
import type { HttpHandler } from "msw";
import { canVoid, withdrawOutcome } from "@/features/orders/order-state";
import { dateKeyInTimeZone } from "@/shared/lib/timezones";
import { apiRoute } from "@/shared/mock/api-route";
import {
  MOCK_IDENTITY_IDS,
  currentIdentityId,
  holdsActorSlot,
  holdsApprovalSlot,
  holdsExecutionSlot,
} from "@/shared/mock/identity-store";
import { fixtureUuid as uuid, nowIsoSeconds as now } from "@/shared/mock/kit";
import { validateListOrdersParams } from "@/shared/mock/request-validation";
import { mockSystemTimezone } from "@/shared/mock/settings-bridge";
import { seedExecutionScenarioOrder } from "./execution-engine";
import {
  seedBulkStatementOrders,
  seedPendingApprovalOrders,
  seedPendingExecutionOrder,
} from "./seed-demos";
import { seedOrderGallery, seedPartialExecutionOrder } from "./seed-gallery";
import {
  businessError,
  draftPublic,
  emit,
  fixtureActor,
  fixtureUser,
  orderPublic,
  pageOf,
  parseRequestedSchedule,
  recordOrderEvent,
  successEnvelope,
  world,
} from "./state";
import { FIXTURE_FLOW_ID, type FixtureDraft, type FixtureOrder } from "./types";

export function reviewOrderHandlers(): HttpHandler[] {
  return [
    http.post(apiRoute("/change-orders"), async ({ request }) => {
      const body = (await request.json().catch(() => null)) as {
        flow_id?: unknown;
        draft_id?: unknown;
        sql?: unknown;
        run_id?: unknown;
        target_database?: unknown;
        metadata?: {
          title?: unknown;
          description?: unknown;
          requested_scheduled_for?: unknown;
        } | null;
      } | null;
      if (
        body === null || typeof body.flow_id !== "string" || body.flow_id === "" ||
        typeof body.sql !== "string" || typeof body.run_id !== "string" || body.run_id === "" ||
        (body.target_database !== undefined && body.target_database !== null && typeof body.target_database !== "string")
      ) {
        return businessError(1001, "请求参数不合法");
      }
      const run = world.runs.get(body.run_id);
      if (run === undefined || run.sql === undefined) {
        return businessError(2001, "业务处理失败", {
          detail: "预审结果已过期，请重新预审",
        });
      }
      if (run.state !== "ready" || !run.gate.passed) {
        return businessError(2013, "submission gate failed");
      }
      if (run.sql !== body.sql) {
        return businessError(2001, "业务处理失败", {
          detail: "SQL与预审文本不一致，请重新预审",
        });
      }
      let draft: FixtureDraft | undefined;
      if (typeof body.draft_id === "string" && body.draft_id !== "") {
        draft = world.drafts.get(body.draft_id);
        if (draft === undefined) return businessError(1002, "draft not found");
        if (draft.flow_id !== body.flow_id) return businessError(1001, "请求参数不合法");
        if (draft.state === "submitted") return businessError(1010, "draft already submitted");
      }
      const reqTargetDb =
        body.target_database === undefined
          ? (draft?.target_database ?? null)
          : typeof body.target_database === "string" && body.target_database.trim() !== ""
            ? body.target_database.trim()
            : null;
      if ((run.target_database ?? null) !== reqTargetDb) {
        return businessError(2001, "业务处理失败", {
          detail: "目标数据库与预审不一致，请重新预审",
        });
      }
      if (draft === undefined) {
        const title = typeof body.metadata?.title === "string" ? body.metadata.title : "";
        if (title === "") {
          return businessError(1001, "业务处理失败", { detail: "标题必填" });
        }
        const description = typeof body.metadata?.description === "string" ? body.metadata.description : null;
        const requested = parseRequestedSchedule(body.metadata?.requested_scheduled_for);
        const shellId = uuid();
        draft = {
          id: shellId,
          owner_user_id: currentIdentityId(),
          flow_id: body.flow_id,
          title,
          description,
          requested_scheduled_for: requested,
          target_database: reqTargetDb,
          revision: 1,
          state: "draft",
          has_sql: true,
          sql: body.sql,
          sql_size_bytes: body.sql.length,
          statement_count: run.statement_count,
          review_run_id: null,
          version: 1,
          created_at: now(),
          updated_at: now(),
        };
        world.drafts.set(shellId, draft);
      }
      draft.state = "submitted";
      if (body.target_database !== undefined) draft.target_database = reqTargetDb;
      draft.has_sql = true;
      draft.sql = body.sql;
      draft.sql_size_bytes = body.sql.length;
      draft.statement_count = run.statement_count;
      draft.review_run_id = run.id;
      draft.version += 1;
      draft.updated_at = now();
      run.sql = undefined;
      const submittedAt = now();
      world.orderSequence += 1;
      const orderId = uuid();
      const order: FixtureOrder = {
        id: orderId,
        display_number: `YR-${submittedAt.slice(0, 10).replace(/-/g, "")}-${String(world.orderSequence).padStart(6, "0")}`,
        submitter_user_id: draft.owner_user_id,
        title: draft.title,
        state: "stage_approval_active",
        current_stage_position: 1,
        requested_scheduled_for: draft.requested_scheduled_for,
        stages: [
          {
            id: uuid(),
            position: 1,
            datasource_name: "orders-mysql",
            state: "approval_active",
            approval_steps: [
              {
                id: uuid(),
                position: 1,
                state: "active",
                decided_at: null,
                actors: [fixtureActor(MOCK_IDENTITY_IDS.reviewer), fixtureActor(MOCK_IDENTITY_IDS.owner)],
              },
            ],
            execution_actors: [fixtureUser(MOCK_IDENTITY_IDS.executor), fixtureUser(MOCK_IDENTITY_IDS.owner)],
          },
        ],
        has_sql: true,
        sql_hash: `hash-${run.id.slice(0, 8)}`,
        snapshot_hash: `snap-${run.id.slice(0, 8)}`,
        manually_verified: false,
        version: 1,
        submitted_at: submittedAt,
        terminal_at: null,
        review_run_id: run.id,
        sql_text: body.sql,
      };
      world.orders.set(order.id, order);
      world.orderComments.set(order.id, []);
      recordOrderEvent(order, "change_order.submitted", "user", draft.owner_user_id, `工单 ${order.display_number} 提交，审核快照已冻结（阶段 1）`, 1, "submitted");
      emit(
        `change-orders/${order.id}`,
        "io.yearning.v4.change_order.submitted",
        {
          order_id: order.id,
          display_number: order.display_number,
          submitter_user_id: draft.owner_user_id,
          stage_count: order.stages.length,
          snapshot_hash: order.snapshot_hash,
          aggregate_version: order.version,
        },
        { kind: "user", user_id: draft.owner_user_id },
      );
      emit(
        `change-drafts/${draft.id}`,
        "io.yearning.v4.change_draft.state_changed",
        {
          aggregate_id: draft.id,
          from: "draft",
          to: "submitted",
          reason_code: "submitted",
          aggregate_version: draft.version,
        },
        { kind: "user", user_id: draft.owner_user_id },
      );
      return HttpResponse.json(successEnvelope(orderPublic(order, request)));
    }),

    http.get(apiRoute("/change-orders"), ({ request }) => {
      const url = new URL(request.url);
      const invalid = validateListOrdersParams(url);
      if (invalid !== null) return businessError(invalid.err_code, invalid.message);
      const limit = Number(url.searchParams.get("limit") ?? "50");
      const after = url.searchParams.get("after");
      const state = url.searchParams.get("state");
      const q = url.searchParams.get("q");
      const datasource = url.searchParams.get("datasource");
      const submittedFrom = url.searchParams.get("submitted_from");
      const submittedTo = url.searchParams.get("submitted_to");
      seedPartialExecutionOrder();
      seedExecutionScenarioOrder();
      seedOrderGallery();
      seedPendingApprovalOrders();
      seedPendingExecutionOrder();
      seedBulkStatementOrders();
      const relatedTo = (order: FixtureOrder): boolean =>
        holdsActorSlot(order.submitter_user_id) ||
        order.stages.some(
          (stage) =>
            stage.approval_steps.some((step) => step.actors.some((actor) => holdsApprovalSlot(actor.id, request))) ||
            stage.execution_actors.some((actor) => holdsExecutionSlot(actor.id, request)),
        );
      const orders = [...world.orders.values()]
        .filter(relatedTo)
        .filter((order) => state === null || order.state === state)
        .filter((order) => {
          if (q === null || q === "") return true;
          const needle = q.toLowerCase();
          return (
            order.display_number.toLowerCase().includes(needle) ||
            order.title.toLowerCase().includes(needle)
          );
        })
        .filter((order) =>
          datasource === null ||
          datasource === "" ||
          order.stages.some((stage) => stage.datasource_name === datasource),
        )
        .filter((order) => {
          const day = dateKeyInTimeZone(order.submitted_at, mockSystemTimezone());
          if (submittedFrom !== null && submittedFrom !== "" && day < submittedFrom) return false;
          if (submittedTo !== null && submittedTo !== "" && day > submittedTo) return false;
          return true;
        })
        .sort((a, b) => b.submitted_at.localeCompare(a.submitted_at))
        .map((order) => orderPublic(order, request));
      return HttpResponse.json(successEnvelope(pageOf(orders, limit, after)));
    }),

    http.get(apiRoute("/change-orders/:orderId"), ({ request, params }) => {
      seedExecutionScenarioOrder();
      seedOrderGallery();
      seedPendingApprovalOrders();
      seedPendingExecutionOrder();
      seedBulkStatementOrders();
      const order = world.orders.get(String(params.orderId));
      if (order === undefined) return businessError(1002, "order not found");
      return HttpResponse.json(successEnvelope(orderPublic(order, request)));
    }),

    http.post(apiRoute("/change-orders/:orderId/withdrawal"), async ({ request, params }) => {
      const order = world.orders.get(String(params.orderId));
      if (order === undefined) return businessError(1002, "order not found");
      if (!holdsActorSlot(order.submitter_user_id)) {
        return businessError(1002, "order not found");
      }
      if (request.headers.get("If-Match") !== `"${String(order.version)}"`) {
        return businessError(1004, "order changed elsewhere");
      }
      const outcome = withdrawOutcome(order.state);
      if (outcome === null) {
        return businessError(1010, `withdraw is not legal from state ${order.state}`);
      }
      const body = (await request.json()) as { reason: string };
      if (body.reason.trim() === "") return businessError(1001, "reason is required");
      const from = order.state;
      order.state = outcome;
      order.terminal_at = now();
      order.version += 1;
      const summary =
        outcome === "withdrawn_after_partial_execution"
          ? `提交人撤回工单（阶段 ${String(order.current_stage_position ?? 1)} 已有执行事实，变更不会自动回滚）`
          : "提交人撤回工单";
      recordOrderEvent(order, "change_order.withdrawn", "user", order.submitter_user_id, summary, order.current_stage_position, order.state);
      emit(
        `change-orders/${order.id}`,
        "io.yearning.v4.change_order.state_changed",
        {
          aggregate_id: order.id,
          from,
          to: order.state,
          reason_code: "submitter_withdrawn",
          aggregate_version: order.version,
        },
        { kind: "user", user_id: order.submitter_user_id },
      );
      return HttpResponse.json(successEnvelope(orderPublic(order, request)));
    }),

    http.post(apiRoute("/change-orders/:orderId/voidance"), async ({ request, params }) => {
      const order = world.orders.get(String(params.orderId));
      if (order === undefined) return businessError(1002, "order not found");
      if (!holdsActorSlot(order.submitter_user_id)) {
        return businessError(1002, "order not found");
      }
      if (request.headers.get("If-Match") !== `"${String(order.version)}"`) {
        return businessError(1004, "order changed elsewhere");
      }
      if (!canVoid(order.state)) {
        return businessError(1010, `void is not legal from state ${order.state}`);
      }
      const body = (await request.json()) as { reason: string };
      if (body.reason.trim() === "") return businessError(1001, "reason is required");
      const from = order.state;
      order.state = "voided";
      order.terminal_at = now();
      order.version += 1;
      recordOrderEvent(order, "change_order.voided", "user", order.submitter_user_id, "提交人作废工单；已完成记录保留", order.current_stage_position, order.state);
      emit(
        `change-orders/${order.id}`,
        "io.yearning.v4.change_order.state_changed",
        {
          aggregate_id: order.id,
          from,
          to: order.state,
          reason_code: "submitter_voided",
          aggregate_version: order.version,
        },
        { kind: "user", user_id: order.submitter_user_id },
      );
      return HttpResponse.json(successEnvelope(orderPublic(order, request)));
    }),

    http.get(apiRoute("/change-orders/:orderId/timeline"), ({ params, request }) => {
      const order = world.orders.get(String(params.orderId));
      if (order === undefined) return businessError(1002, "order not found");
      const url = new URL(request.url);
      const limit = Number(url.searchParams.get("limit") ?? "50");
      const after = url.searchParams.get("after");
      const entries = [...(world.orderTimeline.get(order.id) ?? [])].reverse();
      return HttpResponse.json(successEnvelope(pageOf(entries, limit, after)));
    }),

    http.post(apiRoute("/change-orders/:orderId/draft-copies"), async ({ request, params }) => {
      const order = world.orders.get(String(params.orderId));
      if (order === undefined) return businessError(1002, "order not found");
      const body = (await request.json()) as { target_flow_id: string; title: string; description?: string };
      if (body.target_flow_id !== FIXTURE_FLOW_ID) {
        return businessError(2014, "flow not granted to the current user");
      }
      if (body.title.trim() === "") return businessError(1001, "title is required");
      const draft: FixtureDraft = {
        id: uuid(),
        owner_user_id: currentIdentityId(),
        flow_id: body.target_flow_id,
        title: body.title,
        description: body.description ?? null,
        requested_scheduled_for: null,
        target_database: null,
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

    http.post(apiRoute("/change-orders/:orderId/rollback-drafts"), ({ params }) => {
      const order = world.orders.get(String(params.orderId));
      if (order === undefined) return businessError(1002, "order not found");
      if (order.state !== "completed") {
        return businessError(1010, "order is not completed");
      }
      const draft: FixtureDraft = {
        id: uuid(),
        owner_user_id: currentIdentityId(),
        flow_id: FIXTURE_FLOW_ID,
        title: `回滚：${order.title}`,
        description: `基于已完成工单 ${order.display_number} 的已验证回滚证据自动生成的回滚工单。`,
        requested_scheduled_for: null,
        target_database: null,
        revision: 1,
        state: "draft",
        has_sql: true,
        sql: "DELETE FROM demo_items WHERE id = 1;",
        sql_size_bytes: 36,
        statement_count: 1,
        review_run_id: null,
        version: 1,
        created_at: now(),
        updated_at: now(),
      };
      world.drafts.set(draft.id, draft);
      return HttpResponse.json(successEnvelope(draftPublic(draft)));
    }),
  ];
}

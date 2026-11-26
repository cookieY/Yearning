import { HttpResponse, http } from "msw";
import type { HttpHandler } from "msw";
import { apiRoute } from "@/shared/mock/api-route";
import {
  currentIdentityId,
  holdsExecutionSlot,
} from "@/shared/mock/identity-store";
import { fixtureUuid as uuid, nowIsoSeconds as now } from "@/shared/mock/kit";
import { readStoredScenario } from "@/shared/mock/scenario-store";
import { executionWindowAllows } from "@/shared/mock/settings-bridge";
import {
  CANCEL_RESOLUTION_MS,
  advanceAttempt,
  emitExecutionState,
  isAttemptTerminal,
  makeStatements,
  resolveCancellation,
  seedExecutionScenarioOrder,
} from "./execution-engine";
import {
  attemptPublic,
  businessError,
  emit,
  executionBehavior,
  liveScheduleFor,
  orderPublic,
  pageOf,
  recordOrderEvent,
  schedulePublic,
  sqlStatementCount,
  statementPublic,
  successEnvelope,
  world,
} from "./state";
import type {
  FixtureExecutionAttempt,
  FixtureSchedule,
  FixtureVerification,
} from "./types";

const ALLOWED_OSC_OVERRIDE_KEYS = new Set([
  "max_load", "critical_load", "critical_load_interval_millis", "critical_load_hibernate_seconds",
  "chunk_size", "dml_batch_size", "max_lag", "nice_ratio", "cut_over_lock_timeout_seconds",
  "retries", "heartbeat_interval_millis", "exponential_backoff_max_interval", "exact_rowcount",
  "concurrent_rowcount", "ok_to_drop_table", "timestamp_old_table", "cut_over_exponential_backoff",
  "attempt_instant_ddl",
]);

export function reviewExecutionHandlers(): HttpHandler[] {
  return [
    http.post(apiRoute("/change-orders/:orderId/execution-attempts"), async ({ request, params }) => {
      seedExecutionScenarioOrder();
      const order = world.orders.get(String(params.orderId));
      if (order === undefined) return businessError(1002, "order not found");
      if (request.headers.get("If-Match") !== `"${String(order.version)}"`) {
        return businessError(1004, "order changed elsewhere");
      }
      const stage =
        order.stages.find((candidate) =>
          ["approval_active", "execution_pending", "scheduled", "running", "result_unknown"].includes(
            candidate.state,
          ),
        ) ?? order.stages.at(-1);
      if (stage === undefined) {
        return businessError(1010, `execution is not legal from state ${order.state}`);
      }
      if (!stage.execution_actors.some((actor) => holdsExecutionSlot(actor.id, request))) {
        return businessError(3001, "current user is not a frozen executor of this stage");
      }
      const facts = [...world.attempts.values()].filter(
        (candidate) => candidate.stage_id === stage.id,
      );
      if (facts.some((candidate) => candidate.send_boundary === "sent")) {
        return businessError(3004, "a prior attempt crossed the send boundary; copy to a new draft");
      }
      if (facts.some((candidate) => !isAttemptTerminal(candidate.state))) {
        return businessError(3003, "an execution attempt is already live for this stage");
      }
      if (order.state !== "stage_execution_pending" || stage.state !== "execution_pending") {
        return businessError(1010, `execution is not legal from state ${order.state}`);
      }
      if (!executionWindowAllows(new Date())) {
        return businessError(3008, "outside the allowed execution window");
      }
      const body = (await request.json().catch(() => ({}))) as {
        ddl_execution_mode?: string;
        osc_overrides?: Record<string, unknown>;
      };
      if (
        body.ddl_execution_mode !== undefined &&
        body.ddl_execution_mode !== "gh_ost" &&
        body.ddl_execution_mode !== "online_ddl"
      ) {
        return businessError(1001, "ddl_execution_mode must be gh_ost or online_ddl");
      }
      if (
        body.osc_overrides !== undefined &&
        (typeof body.osc_overrides !== "object" ||
          Array.isArray(body.osc_overrides) ||
          Object.keys(body.osc_overrides).some((key) => !ALLOWED_OSC_OVERRIDE_KEYS.has(key)))
      ) {
        return businessError(1001, "osc_overrides contains unknown keys");
      }
      const behavior = executionBehavior();
      if (readStoredScenario() === "execution-preflight") {
        return businessError(3006, "preflight failed: schema signature changed");
      }
      const effectiveKind =
        body.ddl_execution_mode === "online_ddl" && behavior.kind === "gh_ost"
          ? "ddl"
          : behavior.kind;
      const attempt: FixtureExecutionAttempt = {
        id: uuid(),
        order_id: order.id,
        stage_id: stage.id,
        execution_kind: effectiveKind,
        state: "created",
        send_boundary: "not_started",
        osc:
          effectiveKind === "gh_ost"
            ? {
                tool: "gh-ost",
                tool_version: "1.1.6",
                binary_sha256: "a".repeat(64),
                plan_hash: "b".repeat(64),
                phase: "planned",
                progress_basis_points: 0,
                rows_copied: 0,
                residual_state: "not_checked",
                last_heartbeat_at: null,
              }
            : null,
        version: 1,
        created_at: now(),
        statements: makeStatements(
          behavior.statementCount > 0
            ? behavior.statementCount
            : sqlStatementCount(order.sql_text, 2),
          effectiveKind === "dml" ? "dml" : "ddl",
        ),
        outcome:
          body.ddl_execution_mode === "online_ddl" && behavior.outcome === "ghost"
            ? "succeeded"
            : behavior.outcome,
      };
      world.attempts.set(attempt.id, attempt);
      stage.state = "running";
      order.state = "running";
      order.version += 1;
      recordOrderEvent(order, "stage.execution_started", "user", currentIdentityId(), `阶段 ${String(stage.position)}（${stage.datasource_name}）开始执行${behavior.kind === "gh_ost" ? "（gh-ost 在线变更）" : ""}`, stage.position, "running");
      emitExecutionState(order, "stage_execution_pending", "execution_started", stage.id, {
        kind: "user",
        user_id: currentIdentityId(),
      });
      advanceAttempt(order, attempt);
      return HttpResponse.json(successEnvelope(attemptPublic(attempt)));
    }),

    http.get(apiRoute("/execution-attempts/:attemptId"), ({ params }) => {
      const attempt = world.attempts.get(String(params.attemptId));
      if (attempt === undefined) return businessError(1002, "attempt not found");
      return HttpResponse.json(successEnvelope(attemptPublic(attempt)));
    }),

    http.get(apiRoute("/execution-attempts/:attemptId/statements"), ({ params, request }) => {
      const attempt = world.attempts.get(String(params.attemptId));
      if (attempt === undefined) return businessError(1002, "attempt not found");
      const url = new URL(request.url);
      const limit = Number(url.searchParams.get("limit") ?? "50");
      const after = url.searchParams.get("after");
      const items = attempt.statements.map(statementPublic);
      return HttpResponse.json(successEnvelope(pageOf(items, limit, after)));
    }),

    http.post(apiRoute("/execution-attempts/:attemptId/cancellation"), async ({ request, params }) => {
      const attempt = world.attempts.get(String(params.attemptId));
      if (attempt === undefined) return businessError(1002, "attempt not found");
      const order = world.orders.get(attempt.order_id);
      const stage = order?.stages.find((candidate) => candidate.id === attempt.stage_id);
      if (order === undefined || stage === undefined) return businessError(1002, "order not found");
      if (!stage.execution_actors.some((actor) => holdsExecutionSlot(actor.id, request))) {
        return businessError(3001, "current user is not a frozen executor of this stage");
      }
      if (isAttemptTerminal(attempt.state)) {
        return HttpResponse.json(successEnvelope(attemptPublic(attempt)));
      }
      if (request.headers.get("If-Match") !== `"${String(attempt.version)}"`) {
        return businessError(1004, "attempt changed elsewhere");
      }
      if (attempt.state === "cancelling") {
        return HttpResponse.json(successEnvelope(attemptPublic(attempt)));
      }
      if (attempt.state !== "running") {
        return businessError(1010, `cancel is not legal from state ${attempt.state}`);
      }
      const body = (await request.json()) as { reason?: string };
      if (typeof body.reason !== "string" || body.reason.trim() === "") {
        return businessError(1001, "reason is required");
      }
      attempt.state = "cancelling";
      attempt.version += 1;
      setTimeout(() => {
        resolveCancellation(order, attempt);
      }, CANCEL_RESOLUTION_MS);
      return HttpResponse.json(successEnvelope(attemptPublic(attempt)));
    }),

    http.post(apiRoute("/execution-attempts/:attemptId/verifications"), async ({ request, params }) => {
      const attempt = world.attempts.get(String(params.attemptId));
      if (attempt === undefined) return businessError(1002, "attempt not found");
      const order = world.orders.get(attempt.order_id);
      const stage = order?.stages.find((candidate) => candidate.id === attempt.stage_id);
      if (order === undefined || stage === undefined) return businessError(1002, "order not found");
      const body = (await request.json().catch(() => null)) as {
        result?: string;
        reason?: string;
        evidence?: Array<{ kind?: string; content?: string }>;
      } | null;
      if (body === null) return businessError(1001, "body is required");
      if (body.result !== "confirmed_succeeded" && body.result !== "confirmed_failed" && body.result !== "confirmed_partial" && body.result !== "still_unknown") {
        return businessError(1001, "result must be one of the four fixed verdicts");
      }
      if (typeof body.reason !== "string" || body.reason.length < 1 || body.reason.length > 4096) {
        return businessError(1001, "reason must be 1..4096 characters");
      }
      if (!Array.isArray(body.evidence) || body.evidence.length < 1) {
        return businessError(3012, "at least one database-side evidence item is required");
      }
      for (const entry of body.evidence) {
        if (entry.kind !== "text" && entry.kind !== "database_fact" && entry.kind !== "external_reference") {
          return businessError(1001, "evidence kind is invalid");
        }
        if (typeof entry.content !== "string" || entry.content.length < 1 || entry.content.length > 16384) {
          return businessError(1001, "evidence content must be 1..16384 characters");
        }
      }
      if (!stage.execution_actors.some((actor) => holdsExecutionSlot(actor.id, request))) {
        return businessError(3001, "current user is not a frozen executor of this stage");
      }
      if (attempt.state !== "result_unknown") {
        return businessError(1010, `verification is not legal from attempt state ${attempt.state}`);
      }
      if (request.headers.get("If-Match") !== `"${String(attempt.version)}"`) {
        return businessError(1004, "attempt changed elsewhere");
      }
      if (order.state !== "result_unknown") {
        return businessError(1010, `verification is not legal from order state ${order.state}`);
      }
      const verification: FixtureVerification = {
        id: uuid(),
        attempt_id: attempt.id,
        result: body.result,
        reason: body.reason,
        evidence: body.evidence.map((entry) => ({
          kind: entry.kind as "text" | "database_fact" | "external_reference",
          content: entry.content as string,
        })),
        verified_by_user_id: currentIdentityId(),
        occurred_at: now(),
      };
      const entries = world.verifications.get(attempt.id) ?? [];
      entries.push(verification);
      world.verifications.set(attempt.id, entries);
      order.version += 1;
      const from = order.state;
      if (body.result !== "still_unknown") {
        const toState =
          body.result === "confirmed_succeeded"
            ? "completed"
            : body.result === "confirmed_failed"
              ? "failed"
              : "partial_failed";
        order.state = toState;
        order.terminal_at = verification.occurred_at;
        order.manually_verified = body.result === "confirmed_succeeded";
        stage.state =
          body.result === "confirmed_succeeded"
            ? "succeeded"
            : body.result === "confirmed_failed"
              ? "failed"
              : "partial_failed";
        recordOrderEvent(
          order,
          "change_order.unknown_resolved",
          "user",
          currentIdentityId(),
          `人工核验结论 ${body.result}：工单${toState === "completed" ? "完成" : "终止"}（执行结果由人工确认）`,
          stage.position,
          toState,
        );
      } else {
        recordOrderEvent(order, "change_order.unknown_resolved", "user", currentIdentityId(), "人工核验结论 still_unknown：继续阻断后续阶段", stage.position, "result_unknown");
      }
      emit(
        `change-orders/${order.id}`,
        "io.yearning.v4.execution.verification_recorded",
        {
          order_id: order.id,
          attempt_id: attempt.id,
          verified_by_user_id: currentIdentityId(),
          result: body.result,
          evidence_count: body.evidence.length,
          aggregate_version: order.version,
        },
        { kind: "user", user_id: currentIdentityId() },
      );
      emit(
        `change-orders/${order.id}`,
        "io.yearning.v4.execution.state_changed",
        {
          aggregate_id: order.id,
          from,
          to: order.state,
          reason_code: `unknown_resolved:${body.result}`,
          stage_id: stage.id,
          aggregate_version: order.version,
        },
        { kind: "user", user_id: currentIdentityId() },
      );
      return HttpResponse.json(successEnvelope(orderPublic(order, request)));
    }),

    http.post(apiRoute("/change-orders/:orderId/execution-schedules"), async ({ request, params }) => {
      seedExecutionScenarioOrder();
      const order = world.orders.get(String(params.orderId));
      if (order === undefined) return businessError(1002, "order not found");
      if (request.headers.get("If-Match") !== `"${String(order.version)}"`) {
        return businessError(1004, "order changed elsewhere");
      }
      const stage =
        order.stages.find((candidate) =>
          ["approval_active", "execution_pending", "scheduled", "running", "result_unknown"].includes(
            candidate.state,
          ),
        ) ?? order.stages.at(-1);
      if (stage === undefined) {
        return businessError(1010, `scheduling is not legal from state ${order.state}`);
      }
      if (!stage.execution_actors.some((actor) => holdsExecutionSlot(actor.id, request))) {
        return businessError(3001, "current user is not a frozen executor of this stage");
      }
      if (order.state !== "stage_execution_pending" || stage.state !== "execution_pending") {
        return businessError(1010, `scheduling is not legal from state ${order.state}`);
      }
      const facts = [...world.attempts.values()].filter(
        (candidate) => candidate.stage_id === stage.id,
      );
      if (
        facts.some((candidate) => !isAttemptTerminal(candidate.state)) ||
        facts.some((candidate) => candidate.send_boundary === "sent") ||
        liveScheduleFor(order.id) !== undefined
      ) {
        return businessError(3003, "this stage already has a live execution instance");
      }
      const body = (await request.json()) as { scheduled_for?: string };
      const scheduledFor = typeof body.scheduled_for === "string" ? body.scheduled_for : "";
      const due = Date.parse(scheduledFor);
      if (scheduledFor === "" || Number.isNaN(due)) {
        return businessError(1001, "scheduled_for must be an RFC3339 timestamp");
      }
      const leadMs = due - Date.now();
      if (leadMs < 5 * 60 * 1000 || leadMs > 30 * 24 * 3600 * 1000) {
        return businessError(3007, "scheduled_for must be 5 minutes to 30 days ahead");
      }
      if (!executionWindowAllows(new Date(due))) {
        return businessError(3008, "scheduled_for is outside the allowed execution window");
      }
      const schedule: FixtureSchedule = {
        id: uuid(),
        order_id: order.id,
        stage_id: stage.id,
        scheduled_for: new Date(due).toISOString().replace(/\.\d{3}Z$/, "Z"),
        state: "scheduled",
        version: 1,
      };
      world.schedules.set(schedule.id, schedule);
      const from = order.state;
      stage.state = "scheduled";
      order.state = "scheduled";
      order.version += 1;
      recordOrderEvent(order, "execution.scheduled", "user", currentIdentityId(), `阶段 ${String(stage.position)} 已预约 ${schedule.scheduled_for} 到点执行（到点未领取将标记错过，不自动补跑）`, stage.position, "scheduled");
      emitExecutionState(order, from, "executor_scheduled", stage.id, {
        kind: "user",
        user_id: currentIdentityId(),
      });
      return HttpResponse.json(
        successEnvelope({
          id: schedule.id,
          stage_id: schedule.stage_id,
          scheduled_for: schedule.scheduled_for,
          state: schedule.state,
        }),
      );
    }),

    http.post(apiRoute("/change-orders/:orderId/execution-schedules/cancellation"), ({ request, params }) => {
      seedExecutionScenarioOrder();
      const order = world.orders.get(String(params.orderId));
      if (order === undefined) return businessError(1002, "order not found");
      if (request.headers.get("If-Match") !== `"${String(order.version)}"`) {
        return businessError(1004, "order changed elsewhere");
      }
      const stage = order.stages.find((candidate) => candidate.state === "scheduled");
      if (stage !== undefined &&
          !stage.execution_actors.some((actor) => holdsExecutionSlot(actor.id, request))) {
        return businessError(3001, "current user is not a frozen executor of this stage");
      }
      if (stage === undefined) {
        return businessError(1010, `cancelling a schedule is not legal from state ${order.state}`);
      }
      const schedule = liveScheduleFor(order.id);
      if (schedule === undefined) {
        return businessError(1004, "no live schedule for this order");
      }
      const from = order.state;
      schedule.state = "cancelled";
      schedule.version += 1;
      stage.state = "execution_pending";
      order.state = "stage_execution_pending";
      order.version += 1;
      recordOrderEvent(order, "execution.schedule_cancelled", "user", currentIdentityId(), "执行人取消预约，工单回到待执行", stage.position, "stage_execution_pending");
      emitExecutionState(order, from, "executor_cancelled_schedule", stage.id, {
        kind: "user",
        user_id: currentIdentityId(),
      });
      return HttpResponse.json(successEnvelope(schedulePublic(schedule)));
    }),
  ];
}

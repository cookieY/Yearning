import { MOCK_IDENTITY_IDS } from "@/shared/mock/identity-store";
import { fixtureUuid as uuid, nowIsoSeconds as now } from "@/shared/mock/kit";
import { readStoredScenario } from "@/shared/mock/scenario-store";
import { emit, fixtureActor, fixtureUser, recordOrderEvent, world } from "./state";
import {
  FIXTURE_OWNER_ID,
  type FixtureExecutionAttempt,
  type FixtureOrder,
  type FixtureSchedule,
  type FixtureStatement,
} from "./types";

const ATTEMPT_CREATED_TO_PREFLIGHT_MS = 300;
const ATTEMPT_PREFLIGHT_TO_RUNNING_MS = 500;
const ATTEMPT_RUNNING_TO_TERMINAL_MS = 900;
export const CANCEL_RESOLUTION_MS = 600;

export function isAttemptTerminal(state: FixtureExecutionAttempt["state"]): boolean {
  return (
    state === "succeeded" ||
    state === "failed" ||
    state === "partial_failed" ||
    state === "cancelled" ||
    state === "partial_cancelled" ||
    state === "result_unknown"
  );
}

export function makeStatements(count: number, kind: "ddl" | "dml"): FixtureStatement[] {
  return Array.from({ length: count }, (_, index) => ({
    id: uuid(),
    ordinal: index + 1,
    statement_kind: kind,
    state: "not_started",
    affected_row_count: null,
    failure_name: null,
    started_at: null,
    finished_at: null,
  }));
}

export function emitExecutionState(
  order: FixtureOrder,
  from: string | null,
  reasonCode: string,
  stageId: string,
  actor: { kind: "user" | "system" | "worker"; user_id?: string | null } = { kind: "worker" },
): void {
  emit(
    `change-orders/${order.id}`,
    "io.yearning.v4.execution.state_changed",
    {
      aggregate_id: order.id,
      from,
      to: order.state,
      reason_code: reasonCode,
      stage_id: stageId,
      aggregate_version: order.version,
    },
    actor,
  );
}

function applyAttemptTerminal(order: FixtureOrder, attempt: FixtureExecutionAttempt): void {
  const stage = order.stages.find((candidate) => candidate.id === attempt.stage_id);
  if (stage === undefined) return;
  const nowAt = now();
  switch (attempt.outcome) {
    case "succeeded": {
      attempt.send_boundary = "sent";
      for (const statement of attempt.statements) {
        if (statement.state !== "sent") continue;
        statement.state = "succeeded";
        statement.affected_row_count = statement.ordinal === 1 ? 1 : 42;
        statement.started_at = nowAt;
        statement.finished_at = nowAt;
      }
      attempt.state = "succeeded";
      attempt.version += 1;
      stage.state = "succeeded";
      order.state = "completed";
      order.terminal_at = nowAt;
      order.version += 1;
      recordOrderEvent(order, "stage.execution_succeeded", "worker", null, `阶段 ${String(stage.position)}（${stage.datasource_name}）执行成功`, stage.position, "succeeded");
      recordOrderEvent(order, "change_order.completed", "system", null, "全部阶段执行完成", stage.position, "completed");
      emitExecutionState(order, "running", "execution_succeeded", stage.id);
      break;
    }
    case "partial_failed": {
      attempt.send_boundary = "sent";
      for (const statement of attempt.statements) {
        if (statement.state !== "sent") continue;
        if (statement.ordinal === 2) {
          statement.state = "failed";
          statement.failure_name = "lock_wait_timeout";
          statement.started_at = nowAt;
          statement.finished_at = nowAt;
        } else if (statement.ordinal < 2) {
          statement.state = "succeeded";
          statement.affected_row_count = 0;
          statement.started_at = nowAt;
          statement.finished_at = nowAt;
        } else {
          statement.state = "skipped";
        }
      }
      attempt.state = "partial_failed";
      attempt.version += 1;
      stage.state = "partial_failed";
      order.state = "partial_failed";
      order.terminal_at = nowAt;
      order.version += 1;
      recordOrderEvent(order, "stage.execution_partial_failed", "worker", null, `阶段 ${String(stage.position)}（${stage.datasource_name}）DDL 第 2 条失败，后续语句未执行`, stage.position, "partial_failed");
      recordOrderEvent(order, "change_order.partial_failed", "system", null, "DDL 部分成功，工单终止；原工单禁止重试", stage.position, "partial_failed");
      emitExecutionState(order, "running", "ddl_partial_failed", stage.id);
      break;
    }
    case "result_unknown": {
      attempt.send_boundary = "sent";
      for (const statement of attempt.statements) {
        if (statement.state !== "sent") continue;
        if (statement.ordinal === 1) {
          statement.state = "succeeded";
          statement.affected_row_count = 5;
          statement.started_at = nowAt;
          statement.finished_at = nowAt;
        } else {
          statement.state = "unknown";
          statement.started_at = nowAt;
        }
      }
      attempt.state = "result_unknown";
      attempt.version += 1;
      stage.state = "result_unknown";
      order.state = "result_unknown";
      order.version += 1;
      recordOrderEvent(order, "stage.execution_result_unknown", "worker", null, `阶段 ${String(stage.position)}（${stage.datasource_name}）执行结果未知，等待人工核验`, stage.position, "result_unknown");
      emitExecutionState(order, "running", "result_became_unknown", stage.id);
      break;
    }
    case "ghost": {
      attempt.send_boundary = "sent";
      if (attempt.osc !== null) {
        attempt.osc.progress_basis_points = 6500;
        attempt.osc.rows_copied = 84210;
        attempt.osc.last_heartbeat_at = nowAt;
      }
      break;
    }
  }
}

export function advanceAttempt(order: FixtureOrder, attempt: FixtureExecutionAttempt): void {
  setTimeout(() => {
    attempt.state = "preflight";
    attempt.version += 1;
    setTimeout(() => {
      attempt.state = "running";
      attempt.send_boundary = "sending";
      attempt.version += 1;
      if (attempt.osc !== null) {
        attempt.osc.phase = "copying";
        attempt.osc.progress_basis_points = 2000;
      }
      for (const statement of attempt.statements) {
        if (statement.state === "not_started" && attempt.outcome !== "ghost") {
          statement.state = "sent";
        }
      }
      if (attempt.outcome === "ghost") {
        return;
      }
      setTimeout(() => {
        applyAttemptTerminal(order, attempt);
      }, ATTEMPT_RUNNING_TO_TERMINAL_MS);
    }, ATTEMPT_PREFLIGHT_TO_RUNNING_MS);
  }, ATTEMPT_CREATED_TO_PREFLIGHT_MS);
}

export function resolveCancellation(order: FixtureOrder, attempt: FixtureExecutionAttempt): void {
  const stage = order.stages.find((candidate) => candidate.id === attempt.stage_id);
  if (stage === undefined) return;
  const nowAt = now();
  const hadSuccess = attempt.statements.some((statement) => statement.state === "succeeded");
  for (const statement of attempt.statements) {
    if (statement.state === "sent") {
      statement.state = "cancelled";
      statement.finished_at = nowAt;
    } else if (statement.state === "not_started") {
      statement.state = "skipped";
    }
  }
  attempt.state = hadSuccess ? "partial_cancelled" : "cancelled";
  attempt.send_boundary = "sent";
  attempt.version += 1;
  if (attempt.osc !== null) {
    attempt.osc.phase = "cancelled";
    attempt.osc.residual_state = "cleanup_required";
    attempt.osc.last_heartbeat_at = nowAt;
  }
  stage.state = attempt.state;
  order.state = attempt.state;
  order.terminal_at = nowAt;
  order.version += 1;
}

export function seedExecutionScenarioOrder(): void {
  const scenario = readStoredScenario();
  if (
    scenario !== "execution-partial" &&
    scenario !== "execution-unknown" &&
    scenario !== "execution-ghost" &&
    scenario !== "execution-preflight" &&
    scenario !== "schedule-missed"
  ) {
    return;
  }
  if (world.orders.size > 0) return;
  const submittedAt = now();
  const sqlByScenario: Record<string, string> = {
    "execution-partial":
      "ALTER TABLE orders ADD COLUMN note varchar(255); ALTER TABLE orders MODIFY COLUMN note text; CREATE INDEX idx_note ON orders (note);",
    "execution-unknown":
      "UPDATE orders SET status = 1 WHERE created_at < '2026-01-01'; UPDATE orders SET priority = 2 WHERE status = 1;",
    "execution-ghost": "ALTER TABLE orders ADD COLUMN note varchar(255);",
    "execution-preflight":
      "UPDATE orders SET status = 1 WHERE user_id = 42; UPDATE orders SET status = 2 WHERE user_id = 43;",
    "schedule-missed":
      "UPDATE archive_rows SET archived = 1 WHERE created_at < '2025-06-01';",
  };
  const isMissed = scenario === "schedule-missed";
  const order: FixtureOrder = {
    id: "7e6f1a2b-0000-4000-8000-00000000f801",
    display_number: "YR-20260830-000101",
    submitter_user_id: FIXTURE_OWNER_ID,
    requested_scheduled_for: null,
    title: "执行域场景工单",
    state: isMissed ? "missed_schedule" : "stage_execution_pending",
    current_stage_position: 1,
    stages: [
      {
        id: "7e6f1a2b-0000-4000-8000-00000000f811",
        position: 1,
        datasource_name: "orders-mysql",
        state: isMissed ? "cancelled" : "execution_pending",
        approval_steps: [
          {
            id: "7e6f1a2b-0000-4000-8000-00000000f821",
            position: 1,
            state: "approved",
            decided_at: submittedAt,
            actors: [fixtureActor(MOCK_IDENTITY_IDS.reviewer), fixtureActor(MOCK_IDENTITY_IDS.owner)],
          },
        ],
        execution_actors: [fixtureUser(MOCK_IDENTITY_IDS.executor), fixtureUser(MOCK_IDENTITY_IDS.owner)],
      },
    ],
    has_sql: true,
    sql_hash: `hash-exec-${scenario}`,
    snapshot_hash: "snap-exec-1",
    manually_verified: false,
    version: isMissed ? 3 : 2,
    submitted_at: submittedAt,
    terminal_at: isMissed ? submittedAt : null,
    review_run_id: null,
    sql_text: sqlByScenario[scenario] ?? "",
  };
  world.orders.set(order.id, order);
  world.orderSequence = Math.max(world.orderSequence, 101);
  world.orderTimeline.set(order.id, []);
  world.orderComments.set(order.id, []);
  recordOrderEvent(order, "change_order.submitted", "user", order.submitter_user_id, "工单提交，审核快照已冻结（阶段 1）", 1, "submitted");
  recordOrderEvent(order, "change_order.stage_ready_for_execution", "system", null, "阶段 1 终步通过，工单等待执行", 1, "stage_execution_pending");
  if (isMissed) {
    const schedule: FixtureSchedule = {
      id: "7e6f1a2b-0000-4000-8000-00000000f831",
      order_id: order.id,
      stage_id: "7e6f1a2b-0000-4000-8000-00000000f811",
      scheduled_for: submittedAt,
      state: "missed",
      version: 2,
    };
    world.schedules.set(schedule.id, schedule);
    recordOrderEvent(order, "schedule.missed", "system", null, "预约到点未被领取，超过宽限已错过（不自动补跑）", 1, "missed_schedule");
  }
}

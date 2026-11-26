import { MOCK_IDENTITY_IDS } from "@/shared/mock/identity-store";
import { nowIsoSeconds as now } from "@/shared/mock/kit";
import { readStoredScenario } from "@/shared/mock/scenario-store";
import { fixtureActor, fixtureUser, recordOrderEvent, world } from "./state";
import { FIXTURE_OWNER_ID, type FixtureOrder, type FixtureStepState } from "./types";

/**
 * Scenario "order-partial-execution" (FE-F6 acceptance gate: withdrawal after partial execution
 * explicitly warns of non-rollback): seeds one running order so the E2E can drive the
 * withdraw_after_partial_fact transition end to end.
 */
export function seedPartialExecutionOrder(): void {
  if (readStoredScenario() !== "order-partial-execution" || world.orders.size > 0) return;
  const submittedAt = now();
  const order: FixtureOrder = {
    id: "7e6f1a2b-0000-4000-8000-00000000f601",
    display_number: "YR-20260901-000601",
    submitter_user_id: FIXTURE_OWNER_ID,
    requested_scheduled_for: null,
    title: "跨阶段存量数据订正（场景）",
    state: "running",
    current_stage_position: 2,
    stages: [
      {
        id: "7e6f1a2b-0000-4000-8000-00000000f611",
        position: 1,
        datasource_name: "staging-mysql",
        state: "succeeded",
        approval_steps: [
          {
            id: "7e6f1a2b-0000-4000-8000-00000000f621",
            position: 1,
            state: "approved",
            decided_at: submittedAt,
            actors: [fixtureActor(MOCK_IDENTITY_IDS.reviewer), fixtureActor(MOCK_IDENTITY_IDS.owner)],
          },
        ],
        execution_actors: [fixtureUser(MOCK_IDENTITY_IDS.executor), fixtureUser(MOCK_IDENTITY_IDS.owner)],
      },
      {
        id: "7e6f1a2b-0000-4000-8000-00000000f612",
        position: 2,
        datasource_name: "prod-mysql",
        state: "running",
        approval_steps: [
          {
            id: "7e6f1a2b-0000-4000-8000-00000000f622",
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
    sql_hash: "hash-seed-42",
    snapshot_hash: "snap-seed-42",
    manually_verified: false,
    version: 3,
    submitted_at: submittedAt,
    terminal_at: null,
    review_run_id: null,
    sql_text: "UPDATE prod_orders SET status = 1 WHERE created_at < '2026-01-01';",
  };
  world.orders.set(order.id, order);
  world.orderSequence = Math.max(world.orderSequence, 42);
  recordOrderEvent(order, "change_order.submitted", "user", order.submitter_user_id, "工单提交，审核快照已冻结（阶段 1）", 1, "submitted");
  recordOrderEvent(order, "stage.execution_succeeded", "worker", null, "阶段 1（staging-mysql）执行成功", 1, "succeeded");
  recordOrderEvent(order, "stage.execution_started", "worker", null, "阶段 2（prod-mysql）开始执行", 2, "running");
}

const GALLERY_STATES: Array<FixtureOrder["state"]> = [
  "submitted", "stage_approval_active", "stage_execution_pending", "scheduled", "running",
  "completed", "rejected", "withdrawn", "withdrawn_after_partial_execution", "voided",
  "failed", "partial_failed", "cancelled", "partial_cancelled", "result_unknown",
  "blocked_datasource_unavailable", "missed_schedule", "invalid",
];

const GALLERY_FIRST_ID = "7e6f1a2b-0000-4000-8000-900000000001";
const GALLERY_DATASOURCES = ["staging-mysql", "prod-mysql", "report-pg"];
export const GALLERY_TOTAL = 40;

export function galleryStageState(orderState: FixtureOrder["state"]): FixtureOrder["stages"][number]["state"] {
  switch (orderState) {
    case "submitted":
      return "pending";
    case "stage_approval_active":
    case "rejected":
      return "approval_active";
    case "stage_execution_pending":
      return "execution_pending";
    case "scheduled":
    case "missed_schedule":
      return "scheduled";
    case "running":
      return "running";
    case "completed":
      return "succeeded";
    case "failed":
      return "failed";
    case "partial_failed":
      return "partial_failed";
    case "cancelled":
      return "cancelled";
    case "partial_cancelled":
      return "partial_cancelled";
    case "result_unknown":
      return "result_unknown";
    case "withdrawn_after_partial_execution":
      return "cancelled";
    default:
      return "pending";
  }
}

export function galleryStepState(orderState: FixtureOrder["state"]): FixtureStepState {
  switch (orderState) {
    case "submitted":
    case "stage_approval_active":
      return "active";
    case "rejected":
      return "rejected";
    case "invalid":
      return "invalid";
    default:
      return "approved";
  }
}

function gallerySubmittedAt(index: number): string {
  return new Date(Date.parse("2026-09-01T08:00:00Z") - index * 86_400_000)
    .toISOString()
    .slice(0, 19)
    .concat("Z");
}

function isTerminalOrderState(state: FixtureOrder["state"]): boolean {
  return ![
    "submitted",
    "stage_approval_active",
    "stage_execution_pending",
    "scheduled",
    "running",
    "blocked_datasource_unavailable",
  ].includes(state);
}

export function derivedUuid(baseUuid: string, group2: number): string {
  return `${baseUuid.slice(0, 9)}${group2.toString(16).padStart(4, "0")}${baseUuid.slice(13)}`;
}

export function seedOrderGallery(): void {
  if (import.meta.env.MODE === "test") return;
  if (world.orders.has(GALLERY_FIRST_ID)) return;
  for (let index = 0; index < GALLERY_TOTAL; index++) {
    const state = GALLERY_STATES[index % GALLERY_STATES.length];
    if (state === undefined) continue;
    const round = Math.floor(index / GALLERY_STATES.length) + 1;
    const id = index === 0 ? GALLERY_FIRST_ID : `7e6f1a2b-0000-4000-8000-9000000000${String(index + 1).padStart(2, "0")}`;
    const stageId = derivedUuid(id, 0x1000 + round);
    const submittedAt = gallerySubmittedAt(index);
    const terminal = isTerminalOrderState(state);
    const stageState = galleryStageState(state);
    const datasource = GALLERY_DATASOURCES[index % GALLERY_DATASOURCES.length] ?? "staging-mysql";
    const order: FixtureOrder = {
      id,
      display_number: `YR-20260901-${String(300000 + index).padStart(6, "0")}`,
      submitter_user_id: FIXTURE_OWNER_ID,
      requested_scheduled_for: null,
      title: `演示工单 · ${state} · #${String(index + 1)}`,
      state,
      current_stage_position: 1,
      stages: [
        {
          id: stageId,
          position: 1,
          datasource_name: datasource,
          state: stageState,
          approval_steps: [
            {
              id: derivedUuid(id, 0x2000 + round),
              position: 1,
              state: galleryStepState(state),
              decided_at: terminal ? submittedAt : null,
              actors: [fixtureActor(MOCK_IDENTITY_IDS.reviewer), fixtureActor(MOCK_IDENTITY_IDS.owner)],
            },
          ],
          execution_actors: [fixtureUser(MOCK_IDENTITY_IDS.executor), fixtureUser(MOCK_IDENTITY_IDS.owner)],
        },
      ],
      has_sql: true,
      sql_hash: `hash-gallery-${String(index + 1)}`,
      snapshot_hash: `snap-gallery-${String(index + 1)}`,
      manually_verified: false,
      version: 2,
      submitted_at: submittedAt,
      terminal_at: terminal ? submittedAt : null,
      review_run_id: null,
      sql_text: `SELECT 1; -- gallery order #${String(index + 1)} (${state})`,
    };
    world.orders.set(order.id, order);
    world.orderSequence = Math.max(world.orderSequence, 100 + index);
    recordOrderEvent(order, "change_order.submitted", "user", order.submitter_user_id, "工单提交，审核快照已冻结（演示）", 1, "submitted");
    if (terminal) {
      recordOrderEvent(order, "change_order.state_changed", "system", null, `演示状态：${state}`, 2, state);
    }
  }
}

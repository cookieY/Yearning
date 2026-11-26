import { delay } from "msw";
import { publishSseEvent, resetSseEventBusForTest } from "@/shared/events/sse-event-bus";
import {
  currentIdentityId,
  identityUsernameOf,
  readDevViewAs,
} from "@/shared/mock/identity-store";
import {
  createMockResponder,
  fixtureUuid as uuid,
  nowIsoSeconds as now,
} from "@/shared/mock/kit";
import { readStoredScenario } from "@/shared/mock/scenario-store";
import type {
  FixtureDraft,
  FixtureExecutionAttempt,
  FixtureOrder,
  FixtureRun,
  FixtureSchedule,
  FixtureStatement,
  FixtureTimelineEntry,
  FixtureWorld,
  ReviewBehavior,
} from "./types";

const MOCK_REQUEST_ID = "33333333-3333-4333-8333-333333333333";
export const {
  successEnvelope,
  businessErrorResponse: businessError,
  problem,
  pageOf,
} = createMockResponder(MOCK_REQUEST_ID, null);

export const world: FixtureWorld = {
  drafts: new Map(),
  runs: new Map(),
  tasks: new Map(),
  findings: new Map(),
  evidence: new Map(),
  orders: new Map(),
  orderTimeline: new Map(),
  orderComments: new Map(),
  attempts: new Map(),
  schedules: new Map(),
  verifications: new Map(),
  orderSequence: 0,
  outbox: [],
  sequences: new Map(),
  flowVersion: 1,
  flowUpdated: false,
};

/** Review outcome requested by the active mock scenario (default: healthy). */
export function currentBehavior(): ReviewBehavior {
  const scenario = readStoredScenario();
  switch (scenario) {
    case "review-blocked":
      return "blocked";
    case "review-partial":
      return "partial";
    case "review-provider-failed":
      return "provider_failed";
    default:
      return "ready";
  }
}

/** Deterministic attempt shape the active scenario runs toward. */
export function executionBehavior(): {
  kind: FixtureExecutionAttempt["execution_kind"];
  outcome: FixtureExecutionAttempt["outcome"];
  statementCount: number;
} {
  switch (readStoredScenario()) {
    case "execution-partial":
      return { kind: "ddl", outcome: "partial_failed", statementCount: 3 };
    case "execution-unknown":
      return { kind: "dml", outcome: "result_unknown", statementCount: 2 };
    case "execution-ghost":
      return { kind: "gh_ost", outcome: "ghost", statementCount: 1 };
    default:
      return { kind: "dml", outcome: "succeeded", statementCount: 0 };
  }
}

export function sqlStatementCount(sqlText: string, fallback: number): number {
  const count = sqlText
    .split(";")
    .map((part) => part.trim())
    .filter((part) => part !== "").length;
  return count > 0 ? count : fallback;
}

/** §18.56 Option B: requested execution time validation (mirroring backend ParseRequestedSchedule). */
export function parseRequestedSchedule(value: unknown): string | null {
  if (typeof value !== "string") return null;
  const parsed = new Date(value);
  if (Number.isNaN(parsed.getTime()) || parsed.getTime() <= Date.now()) return null;
  return value;
}

/** Test seam: wipes the world between vitest cases. */
export function resetReviewFixture(): void {
  world.drafts.clear();
  world.runs.clear();
  world.tasks.clear();
  world.findings.clear();
  world.evidence.clear();
  world.orders.clear();
  world.orderTimeline.clear();
  world.orderComments.clear();
  world.attempts.clear();
  world.schedules.clear();
  world.verifications.clear();
  world.orderSequence = 0;
  world.outbox.length = 0;
  world.sequences.clear();
  world.flowVersion = 1;
  world.flowUpdated = false;
  resetSseEventBusForTest();
}

export function fixtureRunIds(): string[] {
  return [...world.runs.keys()];
}

function orderNumberSuffix(displayNumber: string): number {
  const suffix = displayNumber.split("-").at(-1) ?? "0";
  return Number(suffix);
}

export function seedFixtureOrder(order: FixtureOrder): void {
  world.orders.set(order.id, order);
  world.orderSequence = Math.max(world.orderSequence, orderNumberSuffix(order.display_number));
  world.orderTimeline.set(order.id, []);
  world.orderComments.set(order.id, []);
}

export function seedFixtureSchedule(
  orderId: string,
  stageId: string,
  scheduledFor: string,
): string {
  const id = uuid();
  world.schedules.set(id, {
    id,
    order_id: orderId,
    stage_id: stageId,
    scheduled_for: scheduledFor,
    state: "scheduled",
    version: 1,
  });
  return id;
}

export function emit(
  subject: string,
  type: string,
  data: Record<string, unknown>,
  actor: { kind: "user" | "system" | "worker"; user_id?: string | null } = { kind: "worker" },
): void {
  const sequence = (world.sequences.get(subject) ?? 0) + 1;
  world.sequences.set(subject, sequence);
  const id = uuid();
  world.outbox.push({
    subject,
    sequence,
    payload: {
      specversion: "1.0",
      id,
      type,
      source: "yearning://control-plane",
      subject,
      time: now(),
      datacontenttype: "application/json",
      sequence,
      causation_id: null,
      actor,
      data,
    },
  });
  publishSseEvent({ id, type, subject, data });
}

export function revisionConflict(request: Request, draft: FixtureDraft): boolean {
  const ifMatch = request.headers.get("If-Match");
  if (ifMatch === null) return false;
  return ifMatch !== `"${String(draft.version)}"`;
}

export function draftPublic(draft: FixtureDraft) {
  const out: Record<string, unknown> = {
    id: draft.id,
    owner_user_id: draft.owner_user_id,
    flow_id: draft.flow_id,
    title: draft.title,
    requested_scheduled_for: draft.requested_scheduled_for,
    revision: draft.revision,
    state: draft.state,
    has_sql: draft.has_sql,
    ...(draft.statement_count === null ? {} : { statement_count: draft.statement_count }),
    review_run_id: draft.review_run_id,
    version: draft.version,
    created_at: draft.created_at,
    updated_at: draft.updated_at,
  };
  if (draft.description !== null) out.description = draft.description;
  if (draft.target_database !== null) out.target_database = draft.target_database;
  if (draft.sql_size_bytes !== null) out.sql_size_bytes = draft.sql_size_bytes;
  if (draft.statement_count !== null) out.statement_count = draft.statement_count;
  return out;
}

export function runPublic(run: FixtureRun) {
  return {
    id: run.id,
    draft_id: run.draft_id,
    draft_revision: run.draft_revision,
    state: run.state,
    statement_count: run.statement_count,
    fingerprint_group_count: run.fingerprint_group_count,
    stage_results: run.stage_results,
    gate: run.gate,
    failure_code: run.failure_code,
    version: run.version,
    created_at: run.created_at,
    started_at: run.started_at,
    finished_at: run.finished_at,
  };
}

export function fixtureUser(userId: string) {
  const createdAt = "2026-08-01T00:00:00Z";
  const username = identityUsernameOf(userId);
  return {
    id: userId,
    username,
    display_name: username,
    email: null,
    is_builtin_admin: true,
    version: 1,
    created_at: createdAt,
    updated_at: createdAt,
  };
}

export function fixtureActor(userId: string) {
  const username = identityUsernameOf(userId);
  return { id: userId, username, display_name: username };
}

export function liveScheduleFor(orderId: string): FixtureSchedule | undefined {
  return [...world.schedules.values()].find(
    (schedule) => schedule.order_id === orderId && schedule.state === "scheduled",
  );
}

export function schedulePublic(schedule: FixtureSchedule | undefined) {
  if (schedule === undefined) return null;
  return {
    id: schedule.id,
    stage_id: schedule.stage_id,
    scheduled_for: schedule.scheduled_for,
    state: schedule.state,
  };
}

export function orderPublic(order: FixtureOrder, request?: Request) {
  const submitterName = identityUsernameOf(order.submitter_user_id);
  const attemptsForOrder = [...world.attempts.values()].filter(
    (attempt) => attempt.order_id === order.id,
  );
  const latestAttempt = attemptsForOrder[attemptsForOrder.length - 1];
  const viewAs = readDevViewAs(request);
  const hideApprovals = viewAs === "executor" || viewAs === "user";
  const hideExecutions = viewAs === "approver" || viewAs === "user";
  const me = currentIdentityId();
  const stages =
    hideApprovals || hideExecutions
      ? order.stages.map((stage) => ({
          ...stage,
          approval_steps: hideApprovals
            ? stage.approval_steps.map((step) => ({
                ...step,
                actors: step.actors.filter((actor) => actor.id !== me),
              }))
            : stage.approval_steps,
          execution_actors: hideExecutions
            ? stage.execution_actors.filter((actor) => actor.id !== me)
            : stage.execution_actors,
        }))
      : order.stages;
  return {
    id: order.id,
    display_number: order.display_number,
    submitter_user_id: order.submitter_user_id,
    submitter_username: submitterName,
    submitter_display_name: submitterName,
    title: order.title,
    state: order.state,
    current_stage_position: order.current_stage_position,
    requested_scheduled_for: order.requested_scheduled_for,
    stages,
    has_sql: order.has_sql,
    sql_hash: order.sql_hash,
    snapshot_hash: order.snapshot_hash,
    manually_verified: order.manually_verified,
    has_ddl:
      executionBehavior().kind === "ddl" ||
      executionBehavior().kind === "gh_ost" ||
      /^\s*(CREATE|ALTER|DROP|TRUNCATE|RENAME)\b/im.test(order.sql_text),
    osc_choice_available: executionBehavior().kind === "gh_ost",
    version: order.version,
    submitted_at: order.submitted_at,
    terminal_at: order.terminal_at,
    ...(order.state === "scheduled"
      ? { active_schedule: schedulePublic(liveScheduleFor(order.id)) }
      : {}),
    ...(latestAttempt !== undefined ? { latest_attempt_id: latestAttempt.id } : {}),
  };
}

export function attemptPublic(attempt: FixtureExecutionAttempt) {
  return {
    id: attempt.id,
    stage_id: attempt.stage_id,
    execution_kind: attempt.execution_kind,
    state: attempt.state,
    send_boundary: attempt.send_boundary,
    osc: attempt.osc,
    version: attempt.version,
    created_at: attempt.created_at,
  };
}

export function statementPublic(statement: FixtureStatement) {
  return {
    id: statement.id,
    ordinal: statement.ordinal,
    statement_kind: statement.statement_kind,
    state: statement.state,
    affected_row_count: statement.affected_row_count,
    failure_name: statement.failure_name,
    started_at: statement.started_at,
    finished_at: statement.finished_at,
  };
}

export function recordOrderEvent(
  order: FixtureOrder,
  eventType: string,
  actorKind: "user" | "system" | "worker",
  actorUserId: string | null,
  summary: string,
  stagePosition: number | null,
  state: string | null,
  comment: string | null = null,
): void {
  const entries = world.orderTimeline.get(order.id) ?? [];
  const entry: FixtureTimelineEntry = {
    id: uuid(),
    event_type: eventType,
    occurred_at: now(),
    actor_kind: actorKind,
    actor_user_id: actorUserId,
    actor_display_name: actorUserId === null ? null : identityUsernameOf(actorUserId),
    summary,
    stage_position: stagePosition,
    state,
  };
  if (comment !== null && comment !== "") {
    entry.comment = comment;
  }
  entries.push(entry);
  world.orderTimeline.set(order.id, entries);
}

export async function* createMockEventTransport(
  resume: Record<string, number>,
): AsyncIterable<unknown> {
  const cursors: Record<string, number> = { ...resume };
  for (const [subject, sequence] of Object.entries(cursors)) {
    world.sequences.set(subject, Math.max(world.sequences.get(subject) ?? 0, sequence));
  }
  let cursor = 0;
  while (cursor < Number.MAX_SAFE_INTEGER) {
    if (cursor > world.outbox.length) cursor = 0;
    while (cursor < world.outbox.length) {
      const entry = world.outbox[cursor];
      if (entry === undefined) break;
      cursor += 1;
      const last = cursors[entry.subject] ?? 0;
      cursors[entry.subject] = entry.sequence;
      if (entry.sequence > last) yield entry.payload;
    }
    await delay(120);
  }
}

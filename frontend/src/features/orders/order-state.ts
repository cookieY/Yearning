import { STATUS_TONE_CLASS } from "@/shared/lib/status-tone";
import type {
  ChangeOrder,
  ChangeOrderState,
  StageState,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { stageApprovalSteps } from "@/features/orders/approval-steps";

/**
 * Order state semantics mirrored from api/contracts/state-machines.json
 * (change_order machine) — the frontend presentation layer of the backend's
 * authoritative transitions. Withdraw/void affordances and the
 * partial-execution warning are derived here so the UI can never offer an
 * action the state machine forbids (work-package gate: non-Ready normal UI cannot submit
 * and withdrawal after partial execution explicitly warns of non-rollback).
 */

/**
 * All ChangeOrderState values in list display order — the single source for
 * enum-driven UI (state filter options; labels resolve via orders.state.*).
 */
export const ORDER_STATES: readonly ChangeOrderState[] = [
  "submitted",
  "stage_approval_active",
  "stage_execution_pending",
  "scheduled",
  "running",
  "completed",
  "rejected",
  "withdrawn",
  "withdrawn_after_partial_execution",
  "voided",
  "failed",
  "partial_failed",
  "cancelled",
  "partial_cancelled",
  "result_unknown",
  "blocked_datasource_unavailable",
  "missed_schedule",
  "invalid",
];

/** States from which the submitter's withdraw action is legal. */
const WITHDRAWABLE_STATES: readonly ChangeOrderState[] = [
  "submitted",
  "stage_approval_active",
  "stage_execution_pending",
  "scheduled",
  "blocked_datasource_unavailable",
];

/** Withdraw from a running/unknown-result order is a partial-execution
 * withdrawal: prior stage effects stay and nothing rolls back (W007). */
const WITHDRAW_PARTIAL_STATES: readonly ChangeOrderState[] = ["running", "result_unknown"];

/** States from which the submitter's void action is legal. */
const VOIDABLE_STATES: readonly ChangeOrderState[] = [
  "submitted",
  "stage_approval_active",
  "stage_execution_pending",
  "scheduled",
  "blocked_datasource_unavailable",
  "result_unknown",
  "invalid",
];

export type WithdrawOutcome = "withdrawn" | "withdrawn_after_partial_execution";

/** The withdrawal result the state machine would produce, or null when the
 * action is not offered for this state. */
export function withdrawOutcome(state: ChangeOrderState | null | undefined): WithdrawOutcome | null {
  if (state === null || state === undefined) return null;
  if (WITHDRAWABLE_STATES.includes(state)) return "withdrawn";
  if (WITHDRAW_PARTIAL_STATES.includes(state)) return "withdrawn_after_partial_execution";
  return null;
}

export function canVoid(state: ChangeOrderState | null | undefined): boolean {
  return state !== null && state !== undefined && VOIDABLE_STATES.includes(state);
}

/** One hue per outcome family (owner ruling 2026-10-23: "different states have different colors...
 * colors must match the meaning of the state outcome"). Eleven hue tokens instead of five generic ones, so a
 * reviewer can tell "waiting for approval" (violet) from "waiting for
 * execution" (waiting) from "scheduled" (teal) at a glance, while every tone
 * still maps to a real semantic group:
 *   emerald  finished well        teal    queued for a fixed slot
 *   sky      handed in            indigo  actively running
 *   violet   decision pending     waiting nothing started yet (#F59145,
 *                                 owner page feedback 2026-11-04: use this color for all waiting states
 *                                 — queued/waiting/waiting for execution)
 *   orange   outcome uncertain    rose    effects happened, ended badly
 *   red      hard failure         stone   paperwork that closed quietly
 * Each tone is declared per theme in global.css (light holds deeper inks on
 * paper, dark holds luminous ones on near-black) and the contrast gate keeps
 * the chip text at WCAG AA in both. */
export type OrderStateTone =
  | "emerald"
  | "teal"
  | "sky"
  | "indigo"
  | "violet"
  | "amber"
  | "orange"
  | "waiting"
  | "rose"
  | "red"
  | "stone";

export function orderStateTone(state: ChangeOrderState): OrderStateTone {
  switch (state) {
    case "completed":
      return "emerald";
    case "scheduled":
      return "teal";
    case "submitted":
      return "sky";
    case "running":
      return "indigo";
    case "stage_approval_active":
      return "violet";
    case "stage_execution_pending":
      return "waiting";
    case "result_unknown":
    case "missed_schedule":
    case "blocked_datasource_unavailable":
      return "orange";
    case "partial_failed":
    case "partial_cancelled":
    case "withdrawn_after_partial_execution":
      return "rose";
    case "rejected":
    case "failed":
    case "invalid":
      return "red";
    case "withdrawn":
    case "cancelled":
    case "voided":
      return "stone";
  }
}

/** Chip surface per tone: a 12% wash of the same hue plus a 30% hairline, so
 * the chip stays flat (no gradient, no shadow) and the hue does the talking. */
export const ORDER_STATE_TONE_CLASS: Record<OrderStateTone, string> = STATUS_TONE_CLASS;

/** Solid dot/fill variant of the same tones (status strips, legends). */
export const ORDER_STATE_TONE_DOT: Record<OrderStateTone, string> = {
  emerald: "bg-tone-emerald",
  teal: "bg-tone-teal",
  sky: "bg-tone-sky",
  indigo: "bg-tone-indigo",
  violet: "bg-tone-violet",
  amber: "bg-tone-amber",
  orange: "bg-tone-orange",
  waiting: "bg-tone-waiting",
  rose: "bg-tone-rose",
  red: "bg-tone-red",
  stone: "bg-muted-foreground/60",
};

export function stageStateTone(state: StageState): OrderStateTone {
  switch (state) {
    case "succeeded":
      return "emerald";
    case "failed":
      return "red";
    case "partial_failed":
      return "rose";
    case "result_unknown":
      return "orange";
    case "partial_cancelled":
      return "rose";
    case "pending":
      return "waiting";
    case "skipped":
    case "cancelled":
      return "stone";
    case "approval_active":
      return "violet";
    case "execution_pending":
      return "waiting";
    case "scheduled":
      return "teal";
    case "running":
      return "indigo";
  }
}

/** Timeline actor icon semantics: system/worker events are machine facts,
 * user events carry the acting user. */
export type TimelineActorKind = "user" | "system" | "worker";

/** Identity of the approval step currently awaiting a decision, when the
 * given user is one of its frozen reviewers (W003 any-peer approval at same step). Derived
 * presentation only — the backend re-checks frozen-actor membership on the
 * decision command (order_decision profile 3001/3002). */
export interface ActiveApprovalStep {
  stageId: string;
  stagePosition: number;
  datasourceName: string;
  stepId: string;
  stepPosition: number;
  actorCount: number;
}

export function activeApprovalStepFor(
  order: ChangeOrder,
  userId: string | undefined,
): ActiveApprovalStep | null {
  if (userId === undefined) return null;
  if (order.state !== "stage_approval_active") return null;
  for (const stage of order.stages) {
    if (stage.state !== "approval_active") continue;
    for (const step of stageApprovalSteps(stage)) {
      if (step.state !== "active") continue;
      if (step.actors.some((actor) => actor.id === userId)) {
        return {
          stageId: stage.id,
          stagePosition: stage.position,
          datasourceName: stage.datasource_name,
          stepId: step.id,
          stepPosition: step.position,
          actorCount: step.actors.length,
        };
      }
    }
  }
  return null;
}

/** §18.58: the current user's "own" approval step on this order — prefers an active step still awaiting
 * a decision; otherwise returns the first step containing the user (decided/pending/skipped),
 * so the approval queue can show the user's participation status on already-reviewed orders. Purely derived presentation;
 * authoritative determination stays in the backend decision command. */
export interface MyApprovalStep extends ActiveApprovalStep {
  stepState: string;
}

export function myApprovalStepFor(
  order: ChangeOrder,
  userId: string | undefined,
): MyApprovalStep | null {
  if (userId === undefined) return null;
  let fallback: MyApprovalStep | null = null;
  for (const stage of order.stages) {
    for (const step of stageApprovalSteps(stage)) {
      if (!step.actors.some((actor) => actor.id === userId)) continue;
      const found: MyApprovalStep = {
        stageId: stage.id,
        stagePosition: stage.position,
        datasourceName: stage.datasource_name,
        stepId: step.id,
        stepPosition: step.position,
        actorCount: step.actors.length,
        stepState: step.state,
      };
      if (step.state === "active") return found;
      if (fallback === null) fallback = found;
    }
  }
  return fallback;
}

/** Badge tone per approval-step state (order_approval_steps vocabulary,
 * including the W008 invalid state when frozen actors were removed).
 * owner page feedback 2026-11-04: waiting (pending turn) no longer uses gray, unified under the #F59145
 * waiting family color. */
export function approvalStepTone(state: string): OrderStateTone {
  switch (state) {
    case "approved":
      return "emerald";
    case "rejected":
      return "red";
    case "invalid":
      return "orange";
    case "active":
      return "violet";
    case "pending":
      return "waiting";
    default:
      return "stone";
  }
}

/** The stage whose execution is currently waiting for its frozen executor
 * (W006), when the given user is one of its frozen executors. Admin confers
 * no execution right — the backend re-checks membership on every command
 * (3001), the UI only mirrors the frozen snapshot. */
export function frozenExecutorStageFor(
  order: ChangeOrder,
  userId: string | undefined,
): (typeof order.stages)[number] | null {
  if (userId === undefined) return null;
  if (order.state !== "stage_execution_pending") return null;
  const stage = order.stages.find((candidate) => candidate.state === "execution_pending");
  if (stage === undefined) return null;
  return stage.execution_actors.some((actor) => actor.id === userId) ? stage : null;
}

/** The stage holding the live execution schedule while the order is
 * `scheduled` (owner ruling 2026-10-30), when the given user is one of its
 * frozen executors — any frozen executor may cancel, not just the booker
 * (the backend re-checks membership on the command, 3001). The stage stays
 * execution_pending in the backend read model while scheduled; the mock
 * world marks it scheduled, so both spellings match. */
export function scheduledStageFor(
  order: ChangeOrder,
  userId: string | undefined,
): (typeof order.stages)[number] | null {
  if (userId === undefined) return null;
  if (order.state !== "scheduled") return null;
  const stage = order.stages.find(
    (candidate) => candidate.state === "scheduled" || candidate.state === "execution_pending",
  );
  if (stage === undefined) return null;
  return stage.execution_actors.some((actor) => actor.id === userId) ? stage : null;
}

/** The active or unverified execution stage (`stage_execution_pending`, `scheduled`,
 * `running`, or `result_unknown`) when the given user is one of its frozen executors.
 * Used for both entry-route action splitting and execution-scope queue reachability. */
export function activeExecutorStageFor(
  order: ChangeOrder,
  userId: string | undefined,
): (typeof order.stages)[number] | null {
  if (userId === undefined) return null;
  const pendingStage = frozenExecutorStageFor(order, userId);
  if (pendingStage !== null) return pendingStage;
  const scheduledStage = scheduledStageFor(order, userId);
  if (scheduledStage !== null) return scheduledStage;
  if (order.state === "running" || order.state === "result_unknown") {
    const activeStage =
      order.stages.find((candidate) => candidate.state === order.state) ??
      (order.current_stage_position !== null
        ? order.stages.find((candidate) => candidate.position === order.current_stage_position)
        : undefined);
    if (activeStage === undefined) return null;
    return activeStage.execution_actors.some((actor) => actor.id === userId)
      ? activeStage
      : null;
  }
  return null;
}

/** Terminal execution-attempt states (backend domain.AttemptState.Terminal) —
 * the shared truth for "attempt is live" across the polling hooks and the
 * workspace cards. */
export const ATTEMPT_TERMINAL_STATES: ReadonlySet<string> = new Set([
  "succeeded",
  "failed",
  "partial_failed",
  "cancelled",
  "partial_cancelled",
  "result_unknown",
]);

export function isAttemptTerminalState(state: string): boolean {
  return ATTEMPT_TERMINAL_STATES.has(state);
}

/** Badge tone per execution-attempt state (ExecutionAttempt vocabulary). */
export function executionAttemptTone(state: string): OrderStateTone {
  switch (state) {
    case "succeeded":
      return "emerald";
    case "failed":
      return "red";
    case "partial_failed":
      return "rose";
    case "result_unknown":
      return "orange";
    case "cancelled":
      return "stone";
    case "partial_cancelled":
      return "rose";
    case "created":
    case "preflight":
    case "running":
    case "cancelling":
      return "indigo";
    default:
      return "stone";
  }
}

/** Badge tone per per-statement fact. `unknown` is deliberately warning-toned
 * and never rendered like `not_started` — a sent statement whose database
 * answer was lost is a high-risk state (E005, gate: Unknown is never displayed as not-executed). */
export function statementStateTone(state: string): OrderStateTone {
  switch (state) {
    case "succeeded":
      return "emerald";
    case "failed":
      return "red";
    case "unknown":
      return "orange";
    case "cancelled":
      return "stone";
    case "sent":
      return "sky";
    case "not_started":
    case "skipped":
      return "stone";
    default:
      return "stone";
  }
}

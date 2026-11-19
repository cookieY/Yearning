import type {
  DraftState,
  ReviewRunState,
  SubmissionGate,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";

/**
 * Presentation-side projection of the change-draft/review-run state machine
 * (api/contracts/state-machines.json `change_draft`, PRD F4). Pure functions
 * only — no fetching, no React. The backend stays the sole authority for
 * transitions; this module only decides what the workspace shows and which
 * client-side affordances are unlocked.
 */

export type RunPhase =
  | "idle"
  | "queued"
  | "running"
  | "ready"
  | "blocked"
  | "partial"
  | "failed"
  | "outdated";

const PHASE_RANK: Record<RunPhase, number> = {
  idle: 0,
  queued: 1,
  running: 2,
  ready: 3,
  blocked: 3,
  partial: 3,
  failed: 3,
  outdated: 4,
};

const TERMINAL_PHASES: readonly RunPhase[] = ["ready", "blocked", "partial", "failed"];

export function isTerminalPhase(phase: RunPhase): boolean {
  return TERMINAL_PHASES.includes(phase);
}

/**
 * Folds one observation of a run's state into the currently presented phase.
 * Domain events are strictly ordered per subject and deduplicated by the
 * event client, but HTTP snapshots can interleave stale responses with fresh
 * events, so the fold is monotonic: an out-of-order observation can never
 * walk the workspace backwards (work-package gate: out-of-order events do not regress state). A
 * terminal phase sticks until superseded by `outdated`, which voids every
 * prior result because the reviewed inputs changed.
 */
export function mergeRunPhase(current: RunPhase, incoming: ReviewRunState): RunPhase {
  if (incoming === "outdated") return "outdated";
  if (isTerminalPhase(current)) return current;
  return PHASE_RANK[incoming as RunPhase] > PHASE_RANK[current]
    ? (incoming)
    : current;
}

/** The six-value gate reason vocabulary from the backend domain contract. */
const KNOWN_GATE_REASONS: Record<string, string> = {
  high_severity_finding: "precheck.gate.high_severity_finding",
  critical_severity_finding: "precheck.gate.critical_severity_finding",
  stage_review_failed: "precheck.gate.stage_review_failed",
  stage_review_incomplete: "precheck.gate.stage_review_incomplete",
  stage_review_blocked: "precheck.gate.stage_review_blocked",
  stage_review_missing: "precheck.gate.stage_review_missing",
};

export interface GateBlocker {
  code: string;
  /** i18n key under which the blocker text resolves; unknown codes use the safe generic text. */
  messageKey: string;
}

export function gateBlockers(gate: SubmissionGate | null | undefined): GateBlocker[] {
  if (!gate || gate.passed) return [];
  return gate.reason_codes.map((code) => ({
    code,
    messageKey: KNOWN_GATE_REASONS[code] ?? "precheck.gate.unknown",
  }));
}

export interface SubmissionDecision {
  submitEnabled: boolean;
  /** i18n key explaining a disabled submit; null when enabled. */
  reasonKey: string | null;
  /** Flow template changed since the run froze — the strongest blocker. */
  flowUpdated: boolean;
}

export interface SubmissionInput {
  /** Draft envelope state; only "submitted" still gates here (RCP-20261101:
   * draft.state ∈ {draft, submitted} — review states moved onto the run). */
  draftState: DraftState | null;
  runPhase: RunPhase;
  gate: SubmissionGate | null | undefined;
  /**
   * The editor text differs from the SQL sent for the last AI precheck
   * (RCP-20261101-EPHEMERAL-PRECHECK). Submission carries the editor SQL
   * verbatim and the server compares it with the prechecked text, so any
   * local change must force a re-precheck first.
   */
  sqlChangedSincePrecheck: boolean;
  flowUpdated: boolean;
}

/**
 * Mirrors the backend submission gate (R002) for presentation only: every
 * blocked outcome, incomplete result, high/critical finding, changed SQL and
 * stale template keeps the submit action disabled with an explanation. The
 * backend re-validates all of it inside the submission transaction; the
 * enabled button is never the safety boundary.
 */
export function submissionDecision(input: SubmissionInput): SubmissionDecision {
  if (input.flowUpdated) {
    return { submitEnabled: false, reasonKey: "precheck.blocked.flowUpdated", flowUpdated: true };
  }
  if (input.draftState === "submitted") {
    return { submitEnabled: false, reasonKey: "precheck.blocked.submitted", flowUpdated: false };
  }
  if (input.sqlChangedSincePrecheck) {
    return { submitEnabled: false, reasonKey: "precheck.blocked.outdated", flowUpdated: false };
  }
  if (input.runPhase !== "ready") {
    return {
      submitEnabled: false,
      reasonKey: `precheck.phase.${input.runPhase}`,
      flowUpdated: false,
    };
  }
  if (input.gate?.passed !== true) {
    return {
      submitEnabled: false,
      reasonKey: "precheck.blocked.gate",
      flowUpdated: false,
    };
  }
  const blockers = gateBlockers(input.gate);
  if (blockers.length > 0) {
    return {
      submitEnabled: false,
      reasonKey: "precheck.blocked.gate",
      flowUpdated: false,
    };
  }
  return { submitEnabled: true, reasonKey: null, flowUpdated: false };
}

/**
 * The phase the workspace should present (RCP-20261101-EPHEMERAL-PRECHECK):
 * the run is ephemeral client state, so staleness is a pure local comparison
 * — the editor text versus the SQL sent for the last precheck. A mismatch
 * voids any finished result immediately (PRD F4: mark outdated locally as soon as
 * SQL changes); the draft envelope carries no review state anymore.
 */
export function presentPhase(
  runPhase: RunPhase,
  sqlChangedSincePrecheck: boolean,
  hasResult: boolean,
): RunPhase {
  // A run in flight always presents live progress, whatever changed locally.
  if (runPhase === "queued" || runPhase === "running") return runPhase;
  if (sqlChangedSincePrecheck && hasResult) return "outdated";
  return runPhase;
}

export interface PrecheckProgressInput {
  starting: boolean;
  phase: RunPhase;
  stageResults?: ReadonlyArray<{ state: string }>;
  findingsPending: boolean;
}

export interface PrecheckProgressState {
  percent: number;
  completedStages: number;
  totalStages: number;
  stage: "starting" | "queued" | "running" | "finalizing" | "complete";
}

export function computePrecheckProgress(input: PrecheckProgressInput): PrecheckProgressState {
  const stages = input.stageResults ?? [];
  const totalStages = stages.length;
  let completedStages = 0;
  let activeWeight = 0;
  for (const s of stages) {
    if (s.state === "passed" || s.state === "blocked" || s.state === "partial" || s.state === "failed") {
      completedStages += 1;
      activeWeight += 1;
    } else if (s.state === "running") {
      activeWeight += 0.5;
    }
  }

  if (input.starting) {
    return { percent: 15, completedStages, totalStages, stage: "starting" };
  }
  if (isTerminalPhase(input.phase)) {
    if (input.findingsPending) {
      return {
        percent: 95,
        completedStages: totalStages,
        totalStages,
        stage: "finalizing",
      };
    }
    return {
      percent: 100,
      completedStages: totalStages,
      totalStages,
      stage: "complete",
    };
  }
  if (input.phase === "queued" && activeWeight === 0) {
    return { percent: 25, completedStages, totalStages, stage: "queued" };
  }
  if (input.phase === "running" || input.phase === "queued") {
    const percent = totalStages > 0
      ? Math.min(90, Math.round(30 + (activeWeight / totalStages) * 60))
      : 55;
    return { percent, completedStages, totalStages, stage: "running" };
  }
  return { percent: 0, completedStages, totalStages, stage: "starting" };
}

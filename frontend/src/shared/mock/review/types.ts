import type { SqlDigest } from "@/features/review/bulk-import/sql-digest";

// Deterministic identity shared with the auth mock handlers: the fixture
// drafts are owned by the same user the session mock authenticates.
export const FIXTURE_OWNER_ID = "0198d9cc-e65d-7b9d-a8aa-3c81945f99ac";
export const FIXTURE_FLOW_ID = "4f6f1a2b-0000-4000-8000-000000000001";
export const FIXTURE_DATASOURCE_ID = "4f6f1a2b-0000-4000-8000-000000000002";

// Unique markers the E2E storage/telemetry scan can look for after a reveal.
export const FIXTURE_SQL_MARKER = "e2e-sql-plaintext-marker-DO-NOT-PERSIST";
export const FIXTURE_RAW_EVIDENCE_MARKER = "e2e-raw-evidence-marker-DO-NOT-PERSIST";

export type ReviewBehavior = "ready" | "blocked" | "partial" | "provider_failed";

export interface FixtureStageResult {
  stage_position: number;
  datasource_id: string;
  state: "pending" | "running" | "passed" | "blocked" | "partial" | "failed";
  highest_severity: "none" | "low" | "medium" | "high" | "critical";
  gate_passed: boolean;
  finding_count: number;
  evidence_count: number;
  snapshot_hash: string;
  summary: string | null;
  summary_locale: "zh-CN" | "en-US" | null;
}

export interface FixtureRun {
  id: string;
  draft_id: string;
  draft_revision: number;
  state: "queued" | "running" | "ready" | "blocked" | "partial" | "failed" | "outdated";
  statement_count: number;
  fingerprint_group_count: number;
  stage_results: FixtureStageResult[];
  gate: { passed: boolean; reason_codes: string[] };
  failure_code: string | null;
  version: number;
  created_at: string;
  started_at: string | null;
  finished_at: string | null;
  /** Fixture-internal local digest backing bulk runs; never serialized. */
  digest?: SqlDigest;
  /** RCP-20261101 ephemeral precheck: SQL sent verbatim for review (compared verbatim on submit); persistent runs
   * (frozen order surface) omit this — after materialization, SQL is governed by the draft/order. */
  sql?: string;
  target_database?: string | null;
}

export interface FixtureFinding {
  id: string;
  stage_position: number;
  fingerprint_group_id: string | null;
  category: string;
  severity: "low" | "medium" | "high" | "critical";
  title: string;
  message: string;
  suggestion: string | null;
  model_confidence: number | null;
  evidence_ids: string[];
}

export interface FixtureEvidence {
  id: string;
  source_kind: string;
  source_reference: string;
  fact_status: "known" | "unknown" | "unavailable" | "stale";
  normalized_fact: Record<string, unknown>;
  has_raw_payload: boolean;
  raw_payload_expires_at: string | null;
  collected_at: string;
}

export interface FixtureDraft {
  id: string;
  owner_user_id: string;
  flow_id: string;
  title: string;
  description: string | null;
  /** §18.56 Option B: submitter requested execution time (RFC3339 or null); frozen onto the order upon submission. */
  requested_scheduled_for: string | null;
  target_database: string | null;
  revision: number;
  state: "draft" | "reviewing" | "ready" | "blocked" | "partial" | "failed" | "outdated" | "submitted";
  has_sql: boolean;
  sql: string | null;
  sql_size_bytes: number | null;
  statement_count: number | null;
  review_run_id: string | null;
  version: number;
  created_at: string;
  updated_at: string;
}

export interface FixtureTask {
  id: string;
  kind: "sql_grouping" | "ai_review";
  state: "queued" | "running" | "succeeded" | "failed" | "cancelled";
  progress: { completed: number; total: number; unit: string };
  result_ref: string | null;
  error: null;
  created_at: string;
  updated_at: string;
}

export interface FixtureTimelineEntry {
  id: string;
  event_type: string;
  occurred_at: string;
  actor_kind: "user" | "system" | "worker";
  actor_user_id: string | null;
  actor_display_name: string | null;
  summary: string;
  /** BUG-004 read face: approval/rejection comment, projected from the
   * decision row like the backend timeline; absent on non-decision events. */
  comment?: string;
  stage_position: number | null;
  state: string | null;
}

/** StepState from the frozen order_approval_steps vocabulary
 * (changeorder domain: pending/active/approved/rejected/skipped/invalid). */
export type FixtureStepState = "pending" | "active" | "approved" | "rejected" | "skipped" | "invalid";

/** Mirrors the backend StepView JSON the OpenAPI leaves as a free object
 * (changeorder application/read.go): frozen actor snapshots with display
 * names, per-step decision state and decided_at. */
export interface FixtureApprovalStep {
  id: string;
  position: number;
  state: FixtureStepState;
  decided_at: string | null;
  actors: Array<{ id: string; username: string; display_name: string }>;
}

export interface FixtureOrder {
  id: string;
  display_number: string;
  submitter_user_id: string;
  title: string;
  state:
    | "submitted"
    | "stage_approval_active"
    | "stage_execution_pending"
    | "scheduled"
    | "running"
    | "completed"
    | "rejected"
    | "withdrawn"
    | "withdrawn_after_partial_execution"
    | "voided"
    | "failed"
    | "partial_failed"
    | "cancelled"
    | "partial_cancelled"
    | "result_unknown"
    | "blocked_datasource_unavailable"
    | "missed_schedule"
    | "invalid";
  current_stage_position: number | null;
  /** §18.56 Option B: requested execution time frozen at submission (RFC3339 or null); used for display and
   * executor schedule prefilling, does not constitute execution authorization. */
  requested_scheduled_for: string | null;
  stages: Array<{
    id: string;
    position: number;
    datasource_name: string;
    state:
      | "pending"
      | "approval_active"
      | "execution_pending"
      | "scheduled"
      | "running"
      | "succeeded"
      | "failed"
      | "partial_failed"
      | "cancelled"
      | "partial_cancelled"
      | "result_unknown"
      | "skipped";
    approval_steps: FixtureApprovalStep[];
    execution_actors: Array<{
      id: string;
      username: string;
      display_name: string;
      email: string | null;
      is_builtin_admin: boolean;
      version: number;
      created_at: string;
      updated_at: string;
    }>;
  }>;
  has_sql: true;
  sql_hash: string;
  snapshot_hash: string;
  manually_verified: boolean;
  version: number;
  submitted_at: string;
  terminal_at: string | null;
  /** Fixture-internal link to the frozen submission review run; never
   * serialized (feeds the order-side frozen findings endpoint). */
  review_run_id: string | null;
  /** Fixture-internal plaintext for the sql-reveals handler (mirrors the
   * backend order_sql_payloads envelope); never serialized — the public
   * surface carries sql_hash only. */
  sql_text: string;
}

export interface FixtureComment {
  id: string;
  order_id: string;
  author_user_id: string;
  author_display_name: string;
  content: string;
  occurred_at: string;
}

/** Per-statement sanitized execution fact (OpenAPI ExecutionStatement). */
export interface FixtureStatement {
  id: string;
  ordinal: number;
  statement_kind: "ddl" | "dml";
  state: "not_started" | "sent" | "succeeded" | "failed" | "cancelled" | "unknown" | "skipped";
  affected_row_count: number | null;
  failure_name: string | null;
  started_at: string | null;
  finished_at: string | null;
}

/** Frozen per-tool OSC details (OpenAPI OscExecutionDetails); residual_state
 * is the gh-ost cleanup surface E006 requires the UI to show. */
export interface FixtureOscDetails {
  tool: "gh-ost" | "pt-osc";
  tool_version: string;
  binary_sha256: string;
  plan_hash: string;
  phase:
    | "planned"
    | "preflight"
    | "copying"
    | "postponed_cutover"
    | "cutover"
    | "cleanup"
    | "succeeded"
    | "failed"
    | "cancelled"
    | "result_unknown";
  progress_basis_points: number;
  rows_copied: number;
  residual_state: "not_checked" | "none" | "detected" | "cleanup_required" | "cleaned";
  last_heartbeat_at: string | null;
}

/** Mock execution attempt (OpenAPI ExecutionAttempt). */
export interface FixtureExecutionAttempt {
  id: string;
  order_id: string;
  stage_id: string;
  execution_kind: "ddl" | "dml" | "gh_ost";
  state:
    | "created"
    | "preflight"
    | "running"
    | "cancelling"
    | "succeeded"
    | "failed"
    | "partial_failed"
    | "cancelled"
    | "partial_cancelled"
    | "result_unknown";
  send_boundary: "not_started" | "sending" | "sent";
  osc: FixtureOscDetails | null;
  version: number;
  created_at: string;
  statements: FixtureStatement[];
  outcome: "succeeded" | "partial_failed" | "result_unknown" | "ghost";
}

/** Deferred-execution reservation (OpenAPI ExecutionSchedule). */
export interface FixtureSchedule {
  id: string;
  order_id: string;
  stage_id: string;
  scheduled_for: string;
  state: "scheduled" | "running" | "cancelled" | "missed" | "blocked";
  version: number;
}

/** Manual unknown-result verification record. */
export interface FixtureVerification {
  id: string;
  attempt_id: string;
  result: "confirmed_succeeded" | "confirmed_failed" | "confirmed_partial" | "still_unknown";
  reason: string;
  evidence: Array<{ kind: "text" | "database_fact" | "external_reference"; content: string }>;
  verified_by_user_id: string;
  occurred_at: string;
}

export interface FixtureEvent {
  subject: string;
  sequence: number;
  payload: Record<string, unknown>;
}

export interface FixtureWorld {
  drafts: Map<string, FixtureDraft>;
  runs: Map<string, FixtureRun>;
  tasks: Map<string, FixtureTask>;
  findings: Map<string, FixtureFinding[]>;
  evidence: Map<string, FixtureEvidence>;
  orders: Map<string, FixtureOrder>;
  orderTimeline: Map<string, FixtureTimelineEntry[]>;
  orderComments: Map<string, FixtureComment[]>;
  attempts: Map<string, FixtureExecutionAttempt>;
  schedules: Map<string, FixtureSchedule>;
  verifications: Map<string, FixtureVerification[]>;
  orderSequence: number;
  outbox: FixtureEvent[];
  sequences: Map<string, number>;
  flowVersion: number;
  flowUpdated: boolean;
}

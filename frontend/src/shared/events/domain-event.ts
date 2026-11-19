import { z } from "zod";
import { strictObject } from "zod";

/**
 * Runtime validation for the Yearning v4 domain event envelope, mirroring
 * api/events/domain-event.schema.json (CloudEvents 1.0 compatible) and — for
 * every per-type `data` branch — its `#/$defs` payloads plus the external
 * api/events/review-event-data.schema.json. This file is hand-written
 * frontend infrastructure: the event schemas are not part of the OpenAPI
 * generation pipeline, so each mirror below records its source and must be
 * updated together with the contract (enforced by the fixture tests that
 * replay the schema's own examples).
 *
 * Per api/events/README.md: delivery is at-least-once (dedup by event.id),
 * `sequence` is strictly increasing per subject, and events are notifications
 * — consumers re-read the HTTP resource, never treat events as commands.
 */

export const YEARNING_EVENT_TYPES = [
  "io.yearning.v4.review.completed",
  "io.yearning.v4.review.blocked",
  "io.yearning.v4.change_draft.state_changed",
  "io.yearning.v4.change_order.submitted",
  "io.yearning.v4.change_order.state_changed",
  "io.yearning.v4.approval.decided",
  "io.yearning.v4.execution.state_changed",
  "io.yearning.v4.execution.verification_recorded",
  "io.yearning.v4.flow.updated",
  "io.yearning.v4.user.deleted",
  "io.yearning.v4.query_access.state_changed",
  "io.yearning.v4.query_grant.activated",
  "io.yearning.v4.query_grant.revoked",
  "io.yearning.v4.query_execution.state_changed",
  "io.yearning.v4.query_session.state_changed",
  "io.yearning.v4.query_session.closed",
] as const;

export type YearningEventType = (typeof YEARNING_EVENT_TYPES)[number];

export const REVIEW_EVENT_TYPES: readonly YearningEventType[] = [
  "io.yearning.v4.review.completed",
  "io.yearning.v4.review.blocked",
];

const ORDER_SUBMITTED_TYPES: readonly YearningEventType[] = [
  "io.yearning.v4.change_order.submitted",
];

const STATE_CHANGED_TYPES: readonly YearningEventType[] = [
  "io.yearning.v4.change_draft.state_changed",
  "io.yearning.v4.change_order.state_changed",
  "io.yearning.v4.execution.state_changed",
  "io.yearning.v4.query_access.state_changed",
  "io.yearning.v4.query_execution.state_changed",
  "io.yearning.v4.query_session.state_changed",
  "io.yearning.v4.query_session.closed",
];

const RESOURCE_CHANGED_TYPES: readonly YearningEventType[] = [
  "io.yearning.v4.flow.updated",
  "io.yearning.v4.user.deleted",
];

const QUERY_GRANT_TYPES: readonly YearningEventType[] = [
  "io.yearning.v4.query_grant.activated",
  "io.yearning.v4.query_grant.revoked",
];

const SUBJECT_PATTERN =
  /^(review-runs|change-drafts|change-orders|execution-attempts|flows|users|query-access-requests|query-grants|query-executions|query-sessions)\/[0-9a-fA-F-]{36}$/;

const eventActorSchema = strictObject({
  kind: z.enum(["user", "system", "worker"]),
  user_id: z.uuid().nullable().optional(),
});

// Per-type `data` payloads mirroring domain-event.schema.json #/$defs.
export const orderSubmittedDataSchema = strictObject({
  order_id: z.uuid(),
  display_number: z.string(),
  submitter_user_id: z.uuid(),
  stage_count: z.number().int().min(1),
  snapshot_hash: z.string(),
  aggregate_version: z.number().int().min(1),
});

export const stateChangedDataSchema = strictObject({
  aggregate_id: z.uuid(),
  from: z.string().nullable(),
  to: z.string().min(1),
  reason_code: z.string().min(1),
  stage_id: z.uuid().nullable().optional(),
  aggregate_version: z.number().int().min(1),
  manual_verification: z.boolean().optional(),
});

export const resourceChangedDataSchema = strictObject({
  resource_id: z.uuid(),
  action: z.enum(["updated", "deleted"]),
  reason_code: z.string().nullable().optional(),
  aggregate_version: z.number().int().min(1),
});

export const approvalDecidedDataSchema = strictObject({
  order_id: z.uuid(),
  stage_id: z.uuid(),
  step_id: z.uuid(),
  reviewer_user_id: z.uuid(),
  decision: z.enum(["approve", "reject"]),
  aggregate_version: z.number().int().min(1),
});

export const executionVerificationDataSchema = strictObject({
  order_id: z.uuid(),
  attempt_id: z.uuid(),
  verified_by_user_id: z.uuid(),
  result: z.enum(["confirmed_succeeded", "confirmed_failed", "confirmed_partial", "still_unknown"]),
  evidence_count: z.number().int().min(1),
  aggregate_version: z.number().int().min(1),
});

export const queryGrantDataSchema = strictObject({
  grant_id: z.uuid(),
  requester_user_id: z.uuid(),
  state: z.enum(["active", "revoked"]),
  datasource_ids: z.uuid().array().min(1),
  reason: z.string().nullable().optional(),
  aggregate_version: z.number().int().min(1),
});

export const domainEventEnvelopeSchema = strictObject({
  specversion: z.literal("1.0"),
  id: z.uuid(),
  type: z.enum(YEARNING_EVENT_TYPES),
  source: z.literal("yearning://control-plane"),
  subject: z.string().regex(SUBJECT_PATTERN),
  time: z.iso.datetime({ offset: true }),
  sequence: z.number().int().min(1),
  correlation_id: z.uuid().optional(),
  causation_id: z.uuid().nullable().optional(),
  datacontenttype: z.literal("application/json").optional(),
  actor: eventActorSchema.optional(),
  // Replaced per-type by the data schemas below during parseDomainEvent.
  data: z.record(z.string(), z.unknown()),
});

export const reviewEventDataSchema = strictObject({
  review_run_id: z.uuid(),
  draft_id: z.uuid(),
  draft_revision: z.number().int().min(1),
  state: z.enum(["ready", "blocked", "partial", "failed"]),
  stage_results: strictObject({
    stage_position: z.number().int().min(1),
    state: z.enum(["passed", "blocked", "partial", "failed"]),
    highest_severity: z.enum(["none", "low", "medium", "high", "critical"]),
    finding_count: z.number().int().min(0),
    evidence_count: z.number().int().min(0),
    snapshot_hash: z.string(),
  })
    .array()
    .min(1),
  gate: strictObject({
    passed: z.boolean(),
    reason_codes: z
      .string()
      .array()
      .refine((values) => new Set(values).size === values.length, {
        message: "reason_codes must be unique (uniqueItems: true)",
      }),
  }),
  statement_count: z.number().int().min(0).max(100_000).optional(),
  fingerprint_group_count: z.number().int().min(0).max(1_000).optional(),
  aggregate_version: z.number().int().min(1),
});

export type YearningDomainEvent = z.infer<typeof domainEventEnvelopeSchema>;

export type ReviewEventData = z.infer<typeof reviewEventDataSchema>;

/** The per-type data schema for an event type, or null when envelope-only. */
function dataSchemaFor(eventType: YearningEventType): { safeParse(data: unknown): { success: boolean } } | null {
  if (REVIEW_EVENT_TYPES.includes(eventType)) {
    return reviewEventDataSchema;
  }
  if (ORDER_SUBMITTED_TYPES.includes(eventType)) {
    return orderSubmittedDataSchema;
  }
  if (STATE_CHANGED_TYPES.includes(eventType)) {
    return stateChangedDataSchema;
  }
  if (RESOURCE_CHANGED_TYPES.includes(eventType)) {
    return resourceChangedDataSchema;
  }
  if (eventType === "io.yearning.v4.approval.decided") {
    return approvalDecidedDataSchema;
  }
  if (eventType === "io.yearning.v4.execution.verification_recorded") {
    return executionVerificationDataSchema;
  }
  if (QUERY_GRANT_TYPES.includes(eventType)) {
    return queryGrantDataSchema;
  }
  return null;
}

/**
 * Validates the full event: envelope always, plus the per-type `data` schema
 * declared by the contract's allOf branches. Returns the typed event or null
 * when the payload violates the contract — a violating event must never reach
 * consumers, and a contract-legal event must never be rejected either.
 */
export function parseDomainEvent(raw: unknown): YearningDomainEvent | null {
  const envelope = domainEventEnvelopeSchema.safeParse(raw);
  if (!envelope.success) return null;
  const event = envelope.data;
  const dataSchema = dataSchemaFor(event.type);
  if (dataSchema !== null && !dataSchema.safeParse(event.data).success) {
    return null;
  }
  return event;
}

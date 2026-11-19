import { describe, expect, it } from "vitest";
import { parseDomainEvent, reviewEventDataSchema } from "@/shared/events/domain-event";

const REVIEW_RUN_ID = "0198d9cc-e65d-7b9d-a8aa-3c81945f99ac";
const DRAFT_ID = "0198d9cc-e65d-7b9d-a8aa-3c81945f99ad";
const EVENT_ID = "0198d9d0-0ec8-749d-b5d1-0cb384c603a0";

function reviewEvent(overrides: Record<string, unknown> = {}) {
  return {
    specversion: "1.0",
    id: EVENT_ID,
    type: "io.yearning.v4.review.completed",
    source: "yearning://control-plane",
    subject: `review-runs/${REVIEW_RUN_ID}`,
    time: "2026-08-28T08:30:00Z",
    sequence: 1,
    data: {
      review_run_id: REVIEW_RUN_ID,
      draft_id: DRAFT_ID,
      draft_revision: 3,
      state: "ready",
      stage_results: [
        {
          stage_position: 1,
          state: "passed",
          highest_severity: "none",
          finding_count: 0,
          evidence_count: 2,
          snapshot_hash: "sha256:abc",
        },
      ],
      gate: { passed: true, reason_codes: [] },
      aggregate_version: 4,
    },
    ...overrides,
  };
}

function stateChangedEvent(overrides: Record<string, unknown> = {}) {
  return {
    specversion: "1.0",
    id: EVENT_ID,
    type: "io.yearning.v4.change_draft.state_changed",
    source: "yearning://control-plane",
    subject: `change-drafts/${DRAFT_ID}`,
    time: "2026-08-28T08:30:00Z",
    sequence: 1,
    data: {
      aggregate_id: DRAFT_ID,
      from: "draft",
      to: "submitted",
      reason_code: "submitted_by_user",
      aggregate_version: 2,
    },
    ...overrides,
  };
}

describe("parseDomainEvent", () => {
  it("accepts a well-formed review event", () => {
    const parsed = parseDomainEvent(reviewEvent());
    expect(parsed).not.toBeNull();
    expect(parsed?.type).toBe("io.yearning.v4.review.completed");
    expect(parsed?.sequence).toBe(1);
  });

  it("accepts a non-review event whose data is a plain object", () => {
    const parsed = parseDomainEvent(stateChangedEvent());
    expect(parsed).not.toBeNull();
  });

  it("rejects an envelope with a foreign source", () => {
    expect(parseDomainEvent(stateChangedEvent({ source: "https://evil.example" }))).toBeNull();
  });

  it("rejects a subject outside the declared aggregates", () => {
    expect(parseDomainEvent(stateChangedEvent({ subject: `secrets/${DRAFT_ID}` }))).toBeNull();
  });

  it("rejects a review event whose data misses the gate object", () => {
    const event = reviewEvent();
    delete (event.data as Record<string, unknown>).gate;
    expect(parseDomainEvent(event)).toBeNull();
  });

  it("rejects a review event with an extra data field (additionalProperties=false)", () => {
    const event = reviewEvent();
    (event.data as Record<string, unknown>).extra = true;
    expect(parseDomainEvent(event)).toBeNull();
  });

  it("rejects a zero sequence", () => {
    expect(parseDomainEvent(stateChangedEvent({ sequence: 0 }))).toBeNull();
  });

  it("rejects a state_changed event whose data misses the required payload", () => {
    const event = stateChangedEvent();
    (event as { data: unknown }).data = { state: "draft" };
    expect(parseDomainEvent(event)).toBeNull();
  });

  it("accepts contract-legal variants: offset timestamps, null causation_id, datacontenttype", () => {
    const event = stateChangedEvent({
      time: "2026-08-28T16:30:00+08:00",
      causation_id: null,
      datacontenttype: "application/json",
    });
    expect(parseDomainEvent(event)).not.toBeNull();
  });

  it("rejects duplicated gate reason_codes (uniqueItems)", () => {
    const event = reviewEvent();
    (event.data as { gate: { reason_codes: string[] } }).gate.reason_codes = ["risk", "risk"];
    expect(parseDomainEvent(event)).toBeNull();
  });
});

describe("reviewEventDataSchema", () => {
  it("keeps stage severity inside the declared enum", () => {
    const event = reviewEvent();
    const data = event.data as { stage_results: { highest_severity: string }[] };
    const firstStage = data.stage_results[0];
    if (firstStage === undefined) throw new Error("fixture must contain a stage result");
    firstStage.highest_severity = "blocker";
    expect(reviewEventDataSchema.safeParse(event.data).success).toBe(false);
  });
});

describe("parseDomainEvent negative branches", () => {
  it("rejects a structurally valid envelope with an unknown event type", () => {
    const raw = {
      specversion: "1.0",
      id: "0198d9cc-e65d-7b9d-a8aa-3c81945f9901",
      type: "io.yearning.v4.unknown.thing",
      source: "yearning://control-plane",
      subject: "review-runs/4f6f1a2b-0000-4000-8000-00000000aa01",
      time: "2026-08-30T10:00:00Z",
      sequence: 1,
      data: {},
    };
    expect(parseDomainEvent(raw)).toBeNull();
  });

  it("rejects a wrong source even with a known type", () => {
    const raw = {
      specversion: "1.0",
      id: "0198d9cc-e65d-7b9d-a8aa-3c81945f9901",
      type: "io.yearning.v4.flow.updated",
      source: "yearning://elsewhere",
      subject: "flows/4f6f1a2b-0000-4000-8000-000000000001",
      time: "2026-08-30T10:00:00Z",
      sequence: 1,
      data: { resource_id: "4f6f1a2b-0000-4000-8000-000000000001", action: "updated", aggregate_version: 2 },
    };
    expect(parseDomainEvent(raw)).toBeNull();
  });

  it("rejects a resource_changed event whose action is outside the enum", () => {
    const raw = {
      specversion: "1.0",
      id: "0198d9cc-e65d-7b9d-a8aa-3c81945f9901",
      type: "io.yearning.v4.flow.updated",
      source: "yearning://control-plane",
      subject: "flows/4f6f1a2b-0000-4000-8000-000000000001",
      time: "2026-08-30T10:00:00Z",
      sequence: 1,
      data: { resource_id: "4f6f1a2b-0000-4000-8000-000000000001", action: "mutated", aggregate_version: 2 },
    };
    expect(parseDomainEvent(raw)).toBeNull();
  });
});

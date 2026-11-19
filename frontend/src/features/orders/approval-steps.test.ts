import { describe, expect, it } from "vitest";
import type { OrderStageApprovalStepsItem } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { parseApprovalStep, stageApprovalSteps } from "./approval-steps";

/**
 * The OpenAPI types approval_steps as free objects; these tests pin the
 * defensive parse against the backend StepView shape (changeorder
 * application/read.go) — well-formed steps surface structured actors, and
 * anything outside the frozen shape is dropped rather than guessed.
 */

describe("approval step parsing", () => {
  it("parses a well-formed StepView with actor snapshots", () => {
    const step = parseApprovalStep({
      id: "step-1",
      position: 2,
      state: "approved",
      decided_at: "2026-08-30T10:00:00Z",
      actors: [{ id: "u1", username: "henry", display_name: "Henry" }],
    });
    expect(step).toEqual({
      id: "step-1",
      position: 2,
      state: "approved",
      decidedAt: "2026-08-30T10:00:00Z",
      actors: [{ id: "u1", username: "henry", displayName: "Henry" }],
    });
  });

  it("falls back to the username when display_name is missing", () => {
    const step = parseApprovalStep({
      id: "s",
      position: 1,
      state: "active",
      actors: [{ id: "u1", username: "henry" }],
    });
    expect(step?.actors[0]?.displayName).toBe("henry");
    expect(step?.decidedAt).toBeNull();
  });

  it("drops steps outside the frozen shape instead of guessing", () => {
    expect(parseApprovalStep(null as unknown as OrderStageApprovalStepsItem)).toBeNull();
    expect(parseApprovalStep("step" as unknown as OrderStageApprovalStepsItem)).toBeNull();
    expect(parseApprovalStep({ id: "s", state: "active", actors: [] })).toBeNull(); // no position
    expect(parseApprovalStep({ id: "s", position: 0, state: "active", actors: [] })).toBeNull();
    expect(
      parseApprovalStep({ id: "s", position: 1, state: "active", actors: [{ id: "u" }] }),
    ).toBeNull(); // actor without username
    expect(
      parseApprovalStep({ id: "s", position: 1, state: "active", actors: "henry" }),
    ).toBeNull(); // actors not an array
  });

  it("collects the parseable steps of a stage in order", () => {
    const steps = stageApprovalSteps({
      approval_steps: [
        { id: "s1", position: 1, state: "approved", decided_at: null, actors: [] },
        { position: 2, state: "active" }, // malformed — dropped
        { id: "s3", position: 3, state: "pending", decided_at: null, actors: [] },
      ],
    });
    expect(steps.map((step) => step.id)).toEqual(["s1", "s3"]);
  });
});

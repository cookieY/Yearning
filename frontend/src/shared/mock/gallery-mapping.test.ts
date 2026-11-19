import { describe, expect, it } from "vitest";
import {
  galleryStageState,
  galleryStepState,
  type FixtureOrder,
  type FixtureStepState,
} from "./review-fixture";

/**
 * Pure state→stage/step mappings behind the browser-only order gallery.
 * The gallery seeding itself never runs under vitest (MODE guard), so the
 * 18-state mapping is exercised directly here — one assertion per state
 * keeps every switch arm covered.
 */

const STAGE_BY_STATE: Array<[FixtureOrder["state"], FixtureOrder["stages"][number]["state"]]> = [
  ["submitted", "pending"],
  ["stage_approval_active", "approval_active"],
  ["rejected", "approval_active"],
  ["stage_execution_pending", "execution_pending"],
  ["scheduled", "scheduled"],
  ["missed_schedule", "scheduled"],
  ["running", "running"],
  ["completed", "succeeded"],
  ["failed", "failed"],
  ["partial_failed", "partial_failed"],
  ["cancelled", "cancelled"],
  ["partial_cancelled", "partial_cancelled"],
  ["result_unknown", "result_unknown"],
  ["withdrawn_after_partial_execution", "cancelled"],
];

const STEP_BY_STATE: Array<[FixtureOrder["state"], FixtureStepState]> = [
  ["submitted", "active"],
  ["stage_approval_active", "active"],
  ["rejected", "rejected"],
  ["invalid", "invalid"],
  ["completed", "approved"],
];

describe("gallery state mappings", () => {
  it("maps every order state to its gallery stage state", () => {
    for (const [state, stage] of STAGE_BY_STATE) {
      expect(galleryStageState(state)).toBe(stage);
    }
  });

  it("maps the approval-relevant states to their gallery step state", () => {
    for (const [state, step] of STEP_BY_STATE) {
      expect(galleryStepState(state)).toBe(step);
    }
  });

  it("falls back to pending for states outside the stage mapping", () => {
    // The seeded invalid state has no dedicated stage arm.
    expect(galleryStageState("invalid")).toBe("pending");
  });
});

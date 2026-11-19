import { describe, expect, it } from "vitest";
import {
  CATALOG_DATASOURCES,
  CATALOG_FLOWS,
  reviewCatalogFlowViews,
} from "./flow-catalog";

/**
 * The owner test catalog's invariants: deterministic ids, every stage
 * datasource resolves through the D() lookup (a typo throws), and the
 * review-fixture view carries the resolved datasource_name plus the
 * enabled/disabled status split.
 */

describe("flow catalog", () => {
  it("holds 20 change_review flows with 1-4 stages and unique ids", () => {
    expect(CATALOG_FLOWS).toHaveLength(20);
    const ids = new Set(CATALOG_FLOWS.map((entry) => entry.id));
    expect(ids.size).toBe(20);
    for (const flow of CATALOG_FLOWS) {
      expect(flow.stages.length).toBeGreaterThanOrEqual(1);
      expect(flow.stages.length).toBeLessThanOrEqual(4);
    }
  });

  it("resolves every stage datasource and reflects enabled status in the view", () => {
    const views = reviewCatalogFlowViews("reviewer-id", "executor-id");
    expect(views).toHaveLength(20);
    const knownNames = new Set(CATALOG_DATASOURCES.map((entry) => entry.name));
    for (const view of views) {
      expect(["enabled", "disabled"]).toContain(view.status);
      expect(view.enabled).toBe(view.status === "enabled");
      for (const stage of view.stages ?? []) {
        expect(knownNames.has(stage.datasource_name)).toBe(true);
        expect(stage.approval_steps.length).toBeGreaterThanOrEqual(1);
        expect(stage.execution_actors.length).toBeGreaterThanOrEqual(1);
        // Distinct role slots (§18.44): approval actors are the reviewer
        // identity, execution actors the executor — never collapsed.
        for (const step of stage.approval_steps) {
          expect(step.actors.map((actor) => actor.user_id)).toEqual(["reviewer-id"]);
        }
        expect(stage.execution_actors.map((actor) => actor.user_id)).toEqual(
          Array.from({ length: stage.execution_actors.length }, () => "executor-id"),
        );
      }
    }
  });
});

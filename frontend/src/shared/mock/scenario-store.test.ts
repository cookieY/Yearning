import { beforeEach, describe, expect, it } from "vitest";
import {
  MOCK_SCENARIOS,
  readStoredScenario,
  useMockScenario,
} from "@/shared/mock/scenario-store";

describe("mock scenario store", () => {
  beforeEach(() => {
    localStorage.clear();
    useMockScenario.setState({ scenario: "ready" });
  });

  it("falls back to ready when nothing or an invalid value is stored", () => {
    expect(readStoredScenario()).toBe("ready");
    localStorage.setItem("yearning-mock-scenario", "chaos");
    expect(readStoredScenario()).toBe("ready");
  });

  it("reads back a stored valid scenario", () => {
    localStorage.setItem("yearning-mock-scenario", "blocked");
    expect(readStoredScenario()).toBe("blocked");
  });

  it("setScenario persists the choice, updates state and notifies workers", () => {
    const seen: string[] = [];
    window.addEventListener("yearning:mock-scenario", () => seen.push("event"));
    useMockScenario.getState().setScenario("error");
    expect(useMockScenario.getState().scenario).toBe("error");
    expect(localStorage.getItem("yearning-mock-scenario")).toBe("error");
    expect(seen).toEqual(["event"]);
  });

  it("declares exactly the contracted scenario set", () => {
    // Four FE-F1 demo scenarios for the generated client outcome surface,
    // the FE-F4 review lifecycle outcomes (Ready/Blocked/Partial/
    // Provider failure acceptance gates) served by the stateful fixture, the
    // FE-F6 order-partial-execution scenario for the partial-withdraw gate,
    // the FE-F8 execution outcomes (DDL partial / unknown / gh-ost residual /
    // preflight failure / missed schedule) and the FE-F10 query worlds.
    expect(MOCK_SCENARIOS).toEqual([
      "ready",
      "blocked",
      "running",
      "error",
      "review-ready",
      "review-blocked",
      "review-partial",
      "review-provider-failed",
      "order-partial-execution",
      "execution-partial",
      "execution-unknown",
      "execution-ghost",
      "execution-preflight",
      "schedule-missed",
      // FE-F10 query-domain worlds (entry branches, session, revocation,
      // approval queue).
      "query-flow",
      "query-flow-direct",
      "query-session",
      "query-revoked",
    "query-huge",
      "query-approval",
    ]);
  });
});

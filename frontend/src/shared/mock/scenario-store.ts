import { create } from "zustand";
import { getServerDimensions } from "@/shared/mock/server-dimensions";

/**
 * Mock scenario switch shared by vitest (node MSW server) and Playwright
 * (browser MSW worker), per code-generation-policy.json mock_layer. Scenarios
 * prove the generated client's full outcome surface before the real backend
 * exists: ready (err_code 0), blocked (typed business error), running
 * (long-running acceptance), error (transport failure). The review-* values
 * select the stateful precheck fixture's run outcome for FE-F4 acceptance
 * gates (Ready/Blocked/Partial/Provider failure E2E).
 */
export type MockScenario =
  | "ready"
  | "blocked"
  | "running"
  | "error"
  | "review-ready"
  | "review-blocked"
  | "review-partial"
  | "review-provider-failed"
  | "order-partial-execution"
  | "execution-partial"
  | "execution-unknown"
  | "execution-ghost"
  | "execution-preflight"
  | "schedule-missed"
  | "query-flow"
  | "query-flow-direct"
  | "query-session"
  | "query-revoked"
  | "query-huge"
  | "query-approval";
export const MOCK_SCENARIOS: MockScenario[] = [
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
  "query-flow",
  "query-flow-direct",
  "query-session",
  "query-revoked",
  "query-huge",
  "query-approval",
];

interface MockScenarioStore {
  scenario: MockScenario;
  setScenario: (scenario: MockScenario) => void;
}

export const SCENARIO_STORAGE_KEY = "yearning-mock-scenario";

export function readStoredScenario(): MockScenario {
  // Node transport (vite dev middleware, 2026-11-04): shared server dims.
  // Guard on `window` — Node ≥22 ships a global localStorage.
  if (typeof window === "undefined") {
    const server = getServerDimensions().scenario;
    return MOCK_SCENARIOS.includes(server as MockScenario) ? (server as MockScenario) : "ready";
  }
  const value = localStorage.getItem(SCENARIO_STORAGE_KEY);
  return MOCK_SCENARIOS.includes(value as MockScenario) ? (value as MockScenario) : "ready";
}

export const useMockScenario = create<MockScenarioStore>((set) => ({
  scenario: "ready",
  setScenario: (scenario) => {
    localStorage.setItem(SCENARIO_STORAGE_KEY, scenario);
    set({ scenario });
    window.dispatchEvent(new CustomEvent("yearning:mock-scenario"));
  },
}));

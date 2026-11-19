import { describe, expect, it } from "vitest";
import { STATE_DISPLAY_PROJECTION } from "@/api/generated/projections/state-display-projection";
import { OPERATION_ERROR_PROFILES } from "@/api/generated/projections/operation-error-profiles";
import { BUSINESS_ERROR_CATALOG, SUCCESS_ERR_CODE } from "@/api/generated/projections/business-error-catalog";

// The projections are generated outputs; these tests assert the contract facts
// the frontend relies on so a silent contract change cannot pass unnoticed.
describe("generated contract projections", () => {
  it("exposes the success err_code from error-codes.json", () => {
    expect(SUCCESS_ERR_CODE).toBe(0);
  });

  it("covers every declared business error with stable identifiers", () => {
    const entries = Object.values(BUSINESS_ERROR_CATALOG.errors);
    expect(entries.length).toBeGreaterThanOrEqual(60);
    for (const entry of entries) {
      expect(entry.name).toMatch(/^[A-Z][A-Z0-9_]*$/);
      expect(["never", "safe", "contextual"]).toContain(entry.retryability);
    }
  });

  it("treats unlisted business errors as contract violations", () => {
    expect(OPERATION_ERROR_PROFILES.semantics.unlistedBusinessErrorIsContractViolation).toBe(true);
  });

  it("binds every error profile to at least one operation with numeric codes", () => {
    const profiles = Object.values(OPERATION_ERROR_PROFILES.profiles);
    expect(profiles.length).toBeGreaterThan(0);
    for (const profile of profiles) {
      for (const code of profile.err_codes) {
        expect(BUSINESS_ERROR_CATALOG.errors[String(code)]).toBeTruthy();
      }
    }
  });

  it("projects every state machine with initial, states and terminal subsets", () => {
    const machines = Object.entries(STATE_DISPLAY_PROJECTION.machines);
    expect(machines.length).toBeGreaterThanOrEqual(6);
    for (const [name, machine] of machines) {
      expect(machine.states).toContain(machine.initial);
      for (const terminal of machine.terminal) {
        expect(machine.states).toContain(terminal);
      }
      expect(name.length).toBeGreaterThan(0);
    }
  });
});

// §18.75: Provider write surfaces (create/replace/connection-test) must declare probe failure
// (2011) and unique conflict (1005) — previously 2011 fell outside shared admin_create so probe failures were
// masked by the frontend contract-violation fallback, and unique conflicts leaked as bare 500s.
describe("provider error profiles (§18.75)", () => {
  it("declares ai provider write surfaces with 1005 and 2011", () => {
    const { profiles } = OPERATION_ERROR_PROFILES;
    const codes = (profile: string): number[] => profiles[profile]?.err_codes ?? [];
    expect(codes("ai_provider_create")).toEqual(expect.arrayContaining([1001, 1005, 2011]));
    expect(codes("ai_provider_replace")).toEqual(expect.arrayContaining([1002, 1003, 1004, 1005, 2011]));
    expect(codes("ai_provider_connection_test")).toEqual(expect.arrayContaining([1002, 2011]));
    expect(profiles.admin_create?.err_codes ?? []).not.toContain(2011);
    expect(codes("draft_sql_replace")).toEqual(expect.arrayContaining([2002, 2003]));
  });
});

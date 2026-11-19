import { afterEach, describe, expect, it } from "vitest";
import {
  getServerDimensions,
  setServerDimensions,
  type MockServerDimensions,
} from "./server-dimensions";

/**
 * The node-transport dimension store lives on globalThis (Symbol.for), so the
 * tests read and patch the real process-level store and restore it between
 * cases — exactly the surface the dev-server /__mock/dimensions endpoint
 * consumes (owner ruling 2026-11-04, MSW multi-tab Option 1).
 */

const KEY = Symbol.for("yearning.mock.server-dimensions");

function rawStore(): MockServerDimensions | undefined {
  return (globalThis as Record<symbol, MockServerDimensions | undefined>)[KEY];
}

const DEFAULTS: MockServerDimensions = {
  identity: "henry",
  superMode: true,
  scenario: "ready",
  authBehavior: "default",
};

afterEach(() => {
  // Reset to a fresh default store so test order can never leak dimensions.
  (globalThis as Record<symbol, unknown>)[KEY] = undefined;
  expect(getServerDimensions()).toEqual(DEFAULTS);
});

describe("server dimensions store", () => {
  it("seeds the documented defaults on first read", () => {
    expect(rawStore()).toBeUndefined();
    expect(getServerDimensions()).toEqual(DEFAULTS);
    // Second read reuses the same store (create-once semantics).
    expect(getServerDimensions()).toEqual(DEFAULTS);
  });

  it("patches only the provided fields and keeps the rest", () => {
    setServerDimensions({ identity: "reviewer-a" });
    expect(getServerDimensions()).toEqual({ ...DEFAULTS, identity: "reviewer-a" });
    setServerDimensions({ superMode: false, scenario: "orders-busy" });
    expect(getServerDimensions()).toEqual({
      ...DEFAULTS,
      identity: "reviewer-a",
      superMode: false,
      scenario: "orders-busy",
    });
    setServerDimensions({ authBehavior: "admin_locked" });
    expect(getServerDimensions().authBehavior).toBe("admin_locked");
  });

  it("treats explicit falsy values as real patches, not skips", () => {
    setServerDimensions({ superMode: false, identity: "executor-a" });
    const dimensions = getServerDimensions();
    expect(dimensions.superMode).toBe(false);
    expect(dimensions.identity).toBe("executor-a");
  });

  it("returns copies: callers cannot mutate the shared store", () => {
    const view = getServerDimensions();
    view.identity = "executor-a";
    view.superMode = false;
    expect(getServerDimensions()).toEqual(DEFAULTS);
    const patched = setServerDimensions({ scenario: "review-busy" });
    patched.scenario = "tampered";
    expect(getServerDimensions().scenario).toBe("review-busy");
  });
});

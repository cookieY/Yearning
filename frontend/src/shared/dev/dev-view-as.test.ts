import { afterEach, describe, expect, it } from "vitest";
import {
  DEV_VIEW_AS_HEADER,
  getDevViewAs,
  normalizeDevViewAsRole,
  resetDevViewAs,
  setDevViewAs,
} from "./dev-view-as";

afterEach(() => {
  resetDevViewAs();
  window.localStorage.clear();
  window.sessionStorage.clear();
});

describe("dev-view-as (D18 in-memory downgrade preview)", () => {
  it("accepts only downgrade presets and refuses privilege escalation", () => {
    expect(normalizeDevViewAsRole("approver")).toBe("approver");
    expect(normalizeDevViewAsRole("EXECUTOR")).toBe("executor");
    expect(normalizeDevViewAsRole("user")).toBe("user");
    expect(normalizeDevViewAsRole("admin")).toBe("none");
    expect(normalizeDevViewAsRole("super")).toBe("none");
    expect(normalizeDevViewAsRole(null)).toBe("none");
  });

  it("updates in-memory store and global header bridge without touching web storage", () => {
    expect(getDevViewAs()).toBe("none");
    expect(DEV_VIEW_AS_HEADER).toBe("X-Yearning-Dev-View-As");

    setDevViewAs("approver");
    expect(getDevViewAs()).toBe("approver");
    expect((globalThis as Record<string, unknown>).__YEARNING_DEV_VIEW_AS__).toBe("approver");
    expect(window.localStorage.length).toBe(0);
    expect(window.sessionStorage.length).toBe(0);

    setDevViewAs("admin");
    expect(getDevViewAs()).toBe("none");
    expect((globalThis as Record<string, unknown>).__YEARNING_DEV_VIEW_AS__).toBeUndefined();
  });
});

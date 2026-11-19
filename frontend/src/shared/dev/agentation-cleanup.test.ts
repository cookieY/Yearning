import { afterEach, describe, expect, it, vi } from "vitest";
import { clearAgentationStorage } from "./agentation-cleanup";

/**
 * Dev-only Agentation storage cleanup (RCP-20261103-FE-AGENTATION-DEV-TOOL):
 * annotation and settings keys are dropped at boot — they can capture page
 * content (SQL, evidence) and a stale webhook URL must never fire — while the
 * pure UI preference keys survive.
 */

function seed(): void {
  const store = window.localStorage;
  store.setItem("feedback-annotations-2026-11-04", JSON.stringify({ text: "SELECT ..." }));
  store.setItem("feedback-toolbar-settings", JSON.stringify({ webhookUrl: "https://example.test/hook" }));
  store.setItem("agentation-design-abc", "1");
  store.setItem("agentation-rearrange-abc", "1");
  store.setItem("agentation-wireframe-abc", "1");
  store.setItem("agentation-session-token", "1");
  store.setItem("feedback-toolbar-theme", "dark");
  store.setItem("feedback-toolbar-position", "right");
  store.setItem("unrelated-key", "keep");
}

afterEach(() => {
  window.localStorage.clear();
  vi.restoreAllMocks();
});

describe("clearAgentationStorage", () => {
  it("removes annotation and settings keys but keeps pure preferences", () => {
    seed();
    clearAgentationStorage();
    const store = window.localStorage;
    expect(store.getItem("feedback-annotations-2026-11-04")).toBeNull();
    expect(store.getItem("feedback-toolbar-settings")).toBeNull();
    expect(store.getItem("agentation-design-abc")).toBeNull();
    expect(store.getItem("agentation-rearrange-abc")).toBeNull();
    expect(store.getItem("agentation-wireframe-abc")).toBeNull();
    expect(store.getItem("agentation-session-token")).toBeNull();
    expect(store.getItem("feedback-toolbar-theme")).toBe("dark");
    expect(store.getItem("feedback-toolbar-position")).toBe("right");
    expect(store.getItem("unrelated-key")).toBe("keep");
  });

  it("tolerates an unavailable storage (private mode)", () => {
    // The cleanup must not throw when localStorage access itself fails —
    // the toolbar keeps working in memory.
    const descriptor = Object.getOwnPropertyDescriptor(window, "localStorage");
    Object.defineProperty(window, "localStorage", {
      configurable: true,
      get() {
        throw new DOMException("denied", "SecurityError");
      },
    });
    try {
      expect(() => {
        clearAgentationStorage();
      }).not.toThrow();
    } finally {
      if (descriptor !== undefined) {
        Object.defineProperty(window, "localStorage", descriptor);
      }
    }
  });

  it("is a no-op on an already-clean storage", () => {
    expect(() => {
      clearAgentationStorage();
    }).not.toThrow();
    expect(window.localStorage.length).toBe(0);
  });
});

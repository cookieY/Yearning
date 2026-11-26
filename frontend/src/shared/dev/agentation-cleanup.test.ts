import { afterEach, describe, expect, it, vi } from "vitest";
import {
  clearAgentationStorage,
  formatAllAgentationMarkdown,
  loadAllAgentationAnnotations,
} from "./agentation-cleanup";

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

describe("loadAllAgentationAnnotations", () => {
  it("loads and sorts non-expired annotations across pages", () => {
    const now = Date.now();
    const expired = now - 8 * 24 * 60 * 60 * 1000;
    window.localStorage.setItem(
      "feedback-annotations-/orders",
      JSON.stringify([
        {
          id: "1",
          x: 10,
          y: 20,
          comment: "Button spacing too small",
          element: "button.submit",
          elementPath: "form > button.submit",
          timestamp: now,
        },
        {
          id: "2",
          x: 0,
          y: 0,
          comment: "Expired note",
          element: "div",
          elementPath: "div",
          timestamp: expired,
        },
      ]),
    );
    window.localStorage.setItem(
      "feedback-annotations-/admin/users",
      JSON.stringify([
        {
          id: "3",
          x: 5,
          y: 5,
          comment: "Table alignment issue",
          element: "table",
          elementPath: "main > table",
          sourceFile: "src/features/admin/users.tsx:42:5",
          timestamp: now,
        },
      ]),
    );
    window.localStorage.setItem("feedback-annotations-/empty", JSON.stringify([]));
    window.localStorage.setItem("feedback-annotations-/broken", "{bad json");

    const pages = loadAllAgentationAnnotations();
    expect(pages.map((p) => p.pathname)).toEqual(["/admin/users", "/orders"]);
    expect(pages[0]?.annotations).toHaveLength(1);
    expect(pages[1]?.annotations).toHaveLength(1);
    expect(pages[1]?.annotations[0]?.comment).toBe("Button spacing too small");
  });
});

describe("formatAllAgentationMarkdown", () => {
  it("returns empty string when there are no pages", () => {
    expect(formatAllAgentationMarkdown([])).toBe("");
  });

  it("formats multi-page annotations into structured markdown", () => {
    const md = formatAllAgentationMarkdown([
      {
        pathname: "/orders",
        annotations: [
          {
            id: "1",
            x: 10,
            y: 20,
            comment: "Copy needs adjustment",
            element: "button.primary",
            elementPath: "div.actions > button.primary",
            sourceFile: "src/pages/orders.tsx:18:3",
            reactComponents: "<OrdersPage> <Button>",
            selectedText: "Submit",
            timestamp: Date.now(),
          },
        ],
      },
    ]);
    expect(md).toContain("## Page Feedback: /orders");
    expect(md).toContain("**Viewport:**");
    expect(md).toContain("### 1. button.primary");
    expect(md).toContain("**Location:** div.actions > button.primary");
    expect(md).toContain("**Source:** src/pages/orders.tsx:18:3");
    expect(md).toContain("**React:** <OrdersPage> <Button>");
    expect(md).toContain('**Selected text:** "Submit"');
    expect(md).toContain("**Feedback:** Copy needs adjustment");
  });
});

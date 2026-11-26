import { render, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { AgentationDevToolbar, installAltClickPassthrough } from "./agentation-dev-toolbar";

describe("installAltClickPassthrough", () => {
  afterEach(() => {
    document.body.innerHTML = "";
  });

  it("forwards Alt+Click as a clean non-Alt click while #root is marked", () => {
    const root = document.createElement("div");
    root.id = "root";
    const button = document.createElement("button");
    root.appendChild(button);
    document.body.appendChild(root);

    const uninstall = installAltClickPassthrough();
    let sawAttrInDocumentCapture = false;
    let receivedAltKey: boolean | null = null;
    const onDocClick = () => {
      sawAttrInDocumentCapture = root.getAttribute("data-feedback-toolbar") === "alt-passthrough";
    };
    const onButtonClick = (event: MouseEvent) => {
      receivedAltKey = event.altKey;
    };
    document.addEventListener("click", onDocClick, true);
    button.addEventListener("click", onButtonClick);

    try {
      const original = new MouseEvent("click", {
        bubbles: true,
        cancelable: true,
        altKey: true,
      });
      button.dispatchEvent(original);
      expect(original.defaultPrevented).toBe(true);
      expect(sawAttrInDocumentCapture).toBe(true);
      expect(receivedAltKey).toBe(false);
      expect(root.hasAttribute("data-feedback-toolbar")).toBe(false);
    } finally {
      document.removeEventListener("click", onDocClick, true);
      button.removeEventListener("click", onButtonClick);
      uninstall();
    }
  });

  it("does not mark #root on normal clicks without Alt", () => {
    const root = document.createElement("div");
    root.id = "root";
    const button = document.createElement("button");
    root.appendChild(button);
    document.body.appendChild(root);

    const uninstall = installAltClickPassthrough();
    let sawAttrInDocumentCapture = false;
    const onDocClick = () => {
      sawAttrInDocumentCapture = root.hasAttribute("data-feedback-toolbar");
    };
    document.addEventListener("click", onDocClick, true);

    try {
      button.dispatchEvent(new MouseEvent("click", { bubbles: true, altKey: false }));
      expect(sawAttrInDocumentCapture).toBe(false);
      expect(root.hasAttribute("data-feedback-toolbar")).toBe(false);
    } finally {
      document.removeEventListener("click", onDocClick, true);
      uninstall();
    }
  });
});

describe("AgentationDevToolbar", () => {
  afterEach(() => {
    document.body.innerHTML = "";
    document.head.querySelector("#agentation-copy-all-style")?.remove();
  });

  it("injects the Copy All Pages button into Agentation's native toolbar", async () => {
    render(<AgentationDevToolbar />);

    await waitFor(() => {
      const shadow = document.querySelector("agentation-toolbar")?.shadowRoot;
      const btn = shadow?.querySelector('button[aria-label="Copy all pages feedback"]') ?? null;
      expect(btn).not.toBeNull();
    });
  });
});

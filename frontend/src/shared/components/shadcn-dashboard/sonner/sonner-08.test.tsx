import { describe, expect, it, vi } from "vitest";
import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import SonnerDemo, { appToast, appToastManager } from "./sonner-08";
import { DEFAULT_TOAST_TIMEOUT_MS, PositionedToaster } from "./toaster";

/**
 * Smoke test for the vendored sonner-08 block: the demo mounts and its local
 * toast manager actually surfaces toasts through the vendored ui/toast
 * primitives + PositionedToaster (owner ruling 2026-11-04: represent order status
 * using the Sonner 08 component — the block must be functional, not decorative).
 */
describe("sonner-08 block", () => {
  it("fires all demo tone toasts through the positioned toaster", async () => {
    const user = userEvent.setup();
    render(<SonnerDemo />);
    for (const label of ["Default", "Success", "Info", "Warning", "Error"]) {
      await user.click(screen.getByRole("button", { name: label }));
    }
    expect(await screen.findByText("Changes saved")).toBeInTheDocument();
    expect(screen.getByText("Your profile is up to date.")).toBeInTheDocument();
  });

  it("shows the close button only for toasts that opt in", async () => {
    const user = userEvent.setup();
    render(<SonnerDemo />);
    await user.click(screen.getByRole("button", { name: "With close button" }));
    expect(await screen.findByText("Session expires soon")).toBeInTheDocument();
    const close = document.querySelectorAll('[data-slot="toast-close"]');
    expect(close.length).toBeGreaterThanOrEqual(1);
  });

  it("supports appToast helpers with both string and object options and bottom positions", async () => {
    render(
      <PositionedToaster manager={appToastManager} position="bottom-right">
        <div>App host</div>
      </PositionedToaster>,
    );

    act(() => {
      appToast.default("Default title", "Default string desc");
      appToast.default("Default obj", { description: "Obj desc", closeButton: false, timeout: 5000 });
      appToast.info("Info title", "Info desc");
      appToast.info("Info obj", { closeButton: false });
      appToast.success("Success title", "Success desc");
      appToast.success("Success obj", { description: "Saved", closeButton: true });
      appToast.warning("Warning title", "Warning desc");
      appToast.warning("Warning obj", { closeButton: false });
      appToast.error("Error title", "Error desc");
      appToast.error("Error obj", { closeButton: false });
      appToast.add({ type: "loading", title: "Loading fallback icon" });
      appToast.add({ title: "No type fallback" });
      appToast.add({
        title: "Custom content toast",
        data: { content: <span data-testid="custom-toast-body">Custom Body</span> },
      });
    });

    expect(await screen.findByTestId("custom-toast-body")).toBeInTheDocument();
  });

  it("auto-dismisses toasts after DEFAULT_TOAST_TIMEOUT_MS without requiring a mouse click", () => {
    vi.useFakeTimers();
    try {
      render(
        <PositionedToaster manager={appToastManager} position="top-right">
          <div>App host</div>
        </PositionedToaster>,
      );

      act(() => {
        appToast.success("Auto dismiss toast", "Should disappear automatically");
        window.dispatchEvent(new FocusEvent("blur"));
      });

      expect(screen.getByText("Auto dismiss toast")).toBeInTheDocument();

      act(() => {
        vi.advanceTimersByTime(DEFAULT_TOAST_TIMEOUT_MS + 100);
      });

      expect(screen.queryByText("Auto dismiss toast")).not.toBeInTheDocument();
    } finally {
      vi.useRealTimers();
    }
  });
});

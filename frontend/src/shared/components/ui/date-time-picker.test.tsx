import "@/shared/i18n";

import { fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { DateTimePicker } from "./date-time-picker";

describe("DateTimePicker portal positioning", () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("portals into document.body outside overflow-hidden ancestors and commits picked date/time", () => {
    const onChange = vi.fn();
    render(
      <div className="overflow-hidden" data-testid="overflow-clipper">
        <DateTimePicker
          value=""
          onChange={onChange}
          enforceFuture={false}
          testId="sched"
        />
      </div>,
    );

    fireEvent.click(screen.getByTestId("sched"));
    const panel = screen.getByTestId("sched-panel");
    const clipper = screen.getByTestId("overflow-clipper");

    expect(panel.parentElement).toBe(document.body);
    expect(clipper.contains(panel)).toBe(false);
    expect(panel.className).toContain("fixed");
    expect(panel.className).toContain("z-50");

    // Pointer interaction inside the portaled panel must not trigger outside-click dismissal.
    fireEvent(panel, new Event("pointerdown", { bubbles: true }));
    expect(screen.getByTestId("sched-panel")).toBeInTheDocument();

    const now = new Date();
    const targetDay = `${String(now.getFullYear())}-${String(now.getMonth() + 1).padStart(2, "0")}-15`;
    fireEvent.click(screen.getByRole("button", { name: targetDay }));
    fireEvent.click(screen.getByTestId("sched-time-toggle"));
    fireEvent.click(within(screen.getByTestId("dtp-hours")).getByRole("button", { name: "10" }));
    fireEvent.click(within(screen.getByTestId("dtp-minutes")).getByRole("button", { name: "30" }));
    fireEvent.click(screen.getByTestId("sched-confirm"));

    expect(onChange).toHaveBeenCalledWith(`${targetDay}T10:30`);
    expect(screen.queryByTestId("sched-panel")).toBeNull();
  });

  it("closes on Escape key and outside pointerdown", () => {
    render(
      <DateTimePicker
        value=""
        onChange={vi.fn()}
        enforceFuture={false}
        testId="sched"
      />,
    );

    fireEvent.click(screen.getByTestId("sched"));
    expect(screen.getByTestId("sched-panel")).toBeInTheDocument();

    fireEvent(document.body, new Event("pointerdown", { bubbles: true }));
    expect(screen.queryByTestId("sched-panel")).toBeNull();

    fireEvent.click(screen.getByTestId("sched"));
    expect(screen.getByTestId("sched-panel")).toBeInTheDocument();

    fireEvent.keyDown(document, { key: "Escape" });
    expect(screen.queryByTestId("sched-panel")).toBeNull();
  });

  it("flips upward when triggered near the bottom of the viewport", () => {
    const originalInnerHeight = window.innerHeight;
    Object.defineProperty(window, "innerHeight", { value: 600, configurable: true });
    vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockReturnValue({
      top: 520,
      bottom: 552,
      left: 40,
      right: 264,
      width: 224,
      height: 32,
      x: 40,
      y: 520,
      toJSON: () => ({}),
    });

    try {
      render(
        <DateTimePicker
          value=""
          onChange={vi.fn()}
          enforceFuture={false}
          testId="sched"
        />,
      );

      fireEvent.click(screen.getByTestId("sched"));
      const panel = screen.getByTestId("sched-panel");
      expect(Number.parseFloat(panel.style.top)).toBeLessThan(520);
      expect(Number.parseFloat(panel.style.left)).toBe(40);
    } finally {
      Object.defineProperty(window, "innerHeight", { value: originalInnerHeight, configurable: true });
    }
  });
});

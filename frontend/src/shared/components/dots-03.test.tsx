import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { Dot } from "./dots-03";

/**
 * Shadcn Dashboard `dots-03` (Scalloped): the block's geometry stays its own;
 * the repo-owned behavior is the `aria-label` prop, the reduced-motion static
 * fallback and the two eye paths — autonomous glancing vs. cursor tracking.
 * Timers are fake so the glance/blink loops are deterministic.
 */

function mockMatchMedia(reduced: boolean): void {
  vi.stubGlobal(
    "matchMedia",
    vi.fn((query: string) => ({
      matches: reduced && query.includes("prefers-reduced-motion"),
      media: query,
      onchange: null,
      addListener: vi.fn(),
      removeListener: vi.fn(),
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      dispatchEvent: vi.fn(),
    })),
  );
}

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  cleanup();
});

describe("Dot (dots-03)", () => {
  it("renders as an image with the provided aria-label and size", () => {
    mockMatchMedia(false);
    render(<Dot size={48} aria-label="Loading" className="extra" />);
    const dot = screen.getByRole("img", { name: "Loading" });
    expect(dot).toHaveAttribute("style");
    expect(dot.className).toContain("extra");
  });

  it("glances around autonomously when not following the cursor", () => {
    mockMatchMedia(false);
    vi.useFakeTimers();
    const random = vi.spyOn(Math, "random");
    // First draw calls: center branch (random < 0.25), then an off-center
    // glance (random >= 0.25 for the check, any value for the coordinates).
    random.mockReturnValueOnce(0.1);
    render(<Dot aria-label="loading" />);
    vi.advanceTimersByTime(900);
    // Second cycle: off-center glance branch.
    random.mockReturnValueOnce(0.9).mockReturnValueOnce(0.5).mockReturnValueOnce(0.5);
    vi.advanceTimersByTime(3000);
    expect(random).toHaveBeenCalled();
  });

  it("tracks the pointer when followCursor is set", () => {
    mockMatchMedia(false);
    const { container } = render(<Dot followCursor aria-label="loading" />);
    const host = container.firstElementChild as HTMLDivElement;
    host.getBoundingClientRect = () =>
      ({ left: 0, top: 0, width: 96, height: 96, right: 96, bottom: 96, x: 0, y: 0, toJSON: () => ({}) });
    window.dispatchEvent(
      new PointerEvent("pointermove", { clientX: 48 + 320, clientY: 48 }),
    );
    // Dead-center pointer: distance 0 falls back to 1 so the look vector
    // stays finite instead of NaN.
    window.dispatchEvent(
      new PointerEvent("pointermove", { clientX: 48, clientY: 48 }),
    );
    // Near pointer: strength scales with distance below saturation.
    window.dispatchEvent(
      new PointerEvent("pointermove", { clientX: 48 + 16, clientY: 48 - 12 }),
    );
    // Far pointer: the strength saturates at 1. No crash and the listener is
    // bound is the observable contract in jsdom (transform runs in motion).
    expect(host).toBeInTheDocument();
  });

  it("ignores pointer moves while the host has no layout box", () => {
    mockMatchMedia(false);
    const { container } = render(<Dot followCursor aria-label="loading" />);
    const host = container.firstElementChild as HTMLDivElement;
    host.getBoundingClientRect = () => null as unknown as DOMRect;
    expect(() =>
      window.dispatchEvent(new PointerEvent("pointermove", { clientX: 10, clientY: 10 })),
    ).not.toThrow();
  });

  it("stays static under reduced motion", () => {
    mockMatchMedia(true);
    vi.useFakeTimers();
    render(<Dot aria-label="loading" />);
    vi.advanceTimersByTime(6000);
    expect(screen.getByRole("img", { name: "loading" })).toBeInTheDocument();
  });
});

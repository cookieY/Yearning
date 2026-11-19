import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import Dots02, { Dot } from "./dots-02";

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

describe("Dot (dots-02)", () => {
  it("renders with custom aria-label, size, and default export", () => {
    mockMatchMedia(false);
    render(<Dot size={44} aria-label="Database AI Assistant" className="trigger-dot" />);
    const dot = screen.getByRole("img", { name: "Database AI Assistant" });
    expect(dot).toHaveAttribute("style");
    expect(dot.className).toContain("trigger-dot");
    cleanup();
    render(<Dots02 />);
    expect(screen.getByRole("img", { name: "Animated dot" })).toBeInTheDocument();
  });

  it("glances autonomously and blinks on schedule", () => {
    mockMatchMedia(false);
    vi.useFakeTimers();
    const random = vi.spyOn(Math, "random");
    random.mockReturnValueOnce(0.1);
    render(<Dot aria-label="assistant" />);
    vi.advanceTimersByTime(900);
    random.mockReturnValueOnce(0.8).mockReturnValueOnce(0.4).mockReturnValueOnce(0.6);
    vi.advanceTimersByTime(3500);
    expect(random).toHaveBeenCalled();
  });

  it("tracks pointer movement when followCursor is true", () => {
    mockMatchMedia(false);
    const { container } = render(<Dot followCursor aria-label="assistant" />);
    const host = container.firstElementChild as HTMLDivElement;
    host.getBoundingClientRect = () =>
      ({ left: 0, top: 0, width: 96, height: 96, right: 96, bottom: 96, x: 0, y: 0, toJSON: () => ({}) });
    window.dispatchEvent(new PointerEvent("pointermove", { clientX: 200, clientY: 48 }));
    window.dispatchEvent(new PointerEvent("pointermove", { clientX: 48, clientY: 48 }));
    expect(host).toBeInTheDocument();
  });

  it("stays static under prefers-reduced-motion", () => {
    mockMatchMedia(true);
    vi.useFakeTimers();
    render(<Dot aria-label="assistant-static" />);
    vi.advanceTimersByTime(5000);
    expect(screen.getByRole("img", { name: "assistant-static" })).toBeInTheDocument();
  });
});

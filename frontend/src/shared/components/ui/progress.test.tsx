import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { Progress } from "./progress";

// The progress fill is scaled with a compositor-only transform instead of an
// animated width: the indicator always occupies the full track and a CSS
// custom property carries the fraction. These assertions pin the *mapping* so
// a later "cleanup" cannot quietly reintroduce a layout animation; the
// browser-level contract (computed `transition-property`, indicator width
// against the track) is asserted in tests/e2e/motion-frames.spec.ts.

function scaleOf(): string | undefined {
  return screen
    .getByRole("progressbar")
    .style.getPropertyValue("--progress-scale");
}

describe("Progress", () => {
  it("exposes the fraction through --progress-scale and keeps the fill full width", () => {
    render(<Progress value={40} />);

    expect(scaleOf()).toBe("0.4");
    expect(screen.getByRole("progressbar").querySelector("[data-slot='progress-indicator']")).toHaveStyle({
      width: "100%",
    });
  });

  it("maps the endpoints exactly", () => {
    const { rerender } = render(<Progress value={0} />);
    expect(scaleOf()).toBe("0");

    rerender(<Progress value={100} />);
    expect(scaleOf()).toBe("1");
  });

  it("normalises a custom min/max range the way Base UI does", () => {
    render(<Progress value={50} min={0} max={200} />);
    expect(scaleOf()).toBe("0.25");
  });

  it("clamps values outside the range instead of emitting an invalid scale", () => {
    const { rerender } = render(<Progress value={150} />);
    expect(scaleOf()).toBe("1");

    rerender(<Progress value={-20} />);
    expect(scaleOf()).toBe("0");
  });

  it("treats a non-finite value as zero rather than an invalid transform", () => {
    render(<Progress value={Number.NaN} />);
    expect(scaleOf()).toBe("0");
  });

  it("treats an inverted or non-finite range as indeterminate instead of emitting NaN", () => {
    const { rerender } = render(<Progress value={50} min={100} max={0} />);
    expect(scaleOf()).toBe("0");

    rerender(<Progress value={50} min={Number.NEGATIVE_INFINITY} max={100} />);
    expect(scaleOf()).toBe("0");
  });

  it("keeps Base UI's ARIA value semantics", () => {
    const { rerender } = render(<Progress value={40} min={0} max={200} />);
    const bar = screen.getByRole("progressbar");
    expect(bar).toHaveAttribute("aria-valuenow", "40");
    expect(bar).toHaveAttribute("aria-valuemin", "0");
    expect(bar).toHaveAttribute("aria-valuemax", "200");

    // An invalid range becomes indeterminate: no aria-valuenow at all, so a
    // screen reader cannot announce a value the bar is not showing.
    rerender(<Progress value={40} min={100} max={0} />);
    expect(screen.getByRole("progressbar")).not.toHaveAttribute("aria-valuenow");
  });
});

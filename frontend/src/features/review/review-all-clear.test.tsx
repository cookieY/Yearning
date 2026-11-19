import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { ReviewAllClear } from "./review-all-clear";

describe("ReviewAllClear", () => {
  it("announces the clean verdict as text and keeps the animated face decorative", () => {
    render(<ReviewAllClear />);
    const status = screen.getByTestId("findings-empty-all");
    expect(status).toHaveAttribute("role", "status");
    expect(status.querySelectorAll("p")).toHaveLength(2);
    expect(screen.getByTestId("review-all-clear-face")).toHaveAttribute("aria-hidden", "true");
  });

  it("draws the scalloped dot: one body circle plus twelve lobes, and two caret eyes", () => {
    const { container } = render(<ReviewAllClear />);
    expect(container.querySelectorAll("circle")).toHaveLength(13);
    expect(container.querySelectorAll("path")).toHaveLength(2);
  });
});

import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { ReviewFinding } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { SeverityDistribution } from "./severity-distribution";

const finding = (severity: ReviewFinding["severity"], id: string): ReviewFinding =>
  ({ id, severity }) as unknown as ReviewFinding;

/** owner page feedback 2026-11-04: numeric distribution — all five levels stay permanently in a row from most to least severe,
 * each item showing dot + label + exact count (including 0), no longer using a chart. */
describe("SeverityDistribution", () => {
  it("lists all five severities, most severe first, with exact counts", () => {
    render(
      <SeverityDistribution
        findings={[finding("low", "a"), finding("critical", "b"), finding("low", "c"), finding("low", "d")]}
      />,
    );
    const counts = screen.getByTestId("severity-counts");
    const items = counts.querySelectorAll("li");
    expect(items).toHaveLength(5);
    expect(screen.getByTestId("severity-count-critical")).toHaveTextContent("1");
    expect(screen.getByTestId("severity-count-high")).toHaveTextContent("0");
    expect(screen.getByTestId("severity-count-medium")).toHaveTextContent("0");
    expect(screen.getByTestId("severity-count-low")).toHaveTextContent("3");
    expect(screen.getByTestId("severity-count-info")).toHaveTextContent("0");
    // Most severe first.
    expect(items[0]).toContainElement(screen.getByTestId("severity-count-critical"));
    expect(items[4]).toContainElement(screen.getByTestId("severity-count-info"));
  });
});

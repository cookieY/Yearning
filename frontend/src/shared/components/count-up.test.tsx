import { render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { CountUp } from "@/shared/components/count-up";

/** The setup file reports prefers-reduced-motion as matching, so most component
 * tests see the settled value immediately. The tick itself is exercised by
 * stubbing matchMedia to report "no preference" for one case below. */
describe("CountUp", () => {
  it("renders the final value immediately under reduced motion", () => {
    render(<CountUp value={47} testId="n" />);
    expect(screen.getByTestId("n")).toHaveTextContent("47");
  });

  it("renders an em dash when the value is missing", () => {
    render(<CountUp value={undefined} testId="n" />);
    expect(screen.getByTestId("n")).toHaveTextContent("—");
  });

  it("applies the formatter", () => {
    render(<CountUp value={23981} format={(v) => `${String(v)}K`} testId="n" />);
    expect(screen.getByTestId("n")).toHaveTextContent("23981K");
  });

  it("ticks up to the value when motion is allowed", async () => {
    vi.stubGlobal(
      "matchMedia",
      vi.fn().mockImplementation((query: string) => ({
        matches: false,
        media: query,
        onchange: null,
        addListener: vi.fn(),
        removeListener: vi.fn(),
        addEventListener: vi.fn(),
        removeEventListener: vi.fn(),
        dispatchEvent: vi.fn(),
      })),
    );
    render(<CountUp value={90} testId="n" />);
    // The first frame starts at zero; the tween then lands on the target.
    expect(Number(screen.getByTestId("n").textContent)).toBeLessThan(90);
    // The tick runs for ~900ms, so the default 1000ms waitFor budget would be
    // one scheduling hiccup away from a false failure.
    await waitFor(
      () => {
        expect(screen.getByTestId("n")).toHaveTextContent("90");
      },
      { timeout: 3000 },
    );
    vi.unstubAllGlobals();
  });
});

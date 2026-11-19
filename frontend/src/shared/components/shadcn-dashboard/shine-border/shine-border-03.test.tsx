import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import ShineBorderDemo, { ShineBorder } from "./shine-border-03";

describe("shine-border-03 block", () => {
  it("renders the ShineBorderDemo feature highlight card", () => {
    render(<ShineBorderDemo />);

    expect(screen.getByText("Platform Features")).toBeInTheDocument();
    expect(screen.getByText("Blazing Fast")).toBeInTheDocument();
    expect(screen.getByText("Real-time Analytics")).toBeInTheDocument();
    expect(screen.getByText("Edge Deployed")).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /explore all features/i }),
    ).toBeInTheDocument();
  });

  it("tracks mouse coordinates and toggles spotlight opacity on hover", () => {
    const { container } = render(
      <ShineBorder borderWidth={3} duration={250}>
        <div>Content</div>
      </ShineBorder>,
    );

    const wrapper = container.querySelector<HTMLElement>(
      '[data-slot="shine-border"]',
    );
    const spotlight = container.querySelector<HTMLElement>(
      '[data-slot="shine-border-spotlight"]',
    );
    expect(wrapper).not.toBeNull();
    expect(spotlight).not.toBeNull();
    if (!wrapper || !spotlight) return;

    expect(wrapper.style.getPropertyValue("--bw")).toBe("3px");
    expect(spotlight.style.opacity).toBe("0");
    expect(spotlight.style.transitionDuration).toBe("250ms");

    vi.spyOn(wrapper, "getBoundingClientRect").mockReturnValue({
      x: 100,
      y: 50,
      top: 50,
      left: 100,
      right: 484,
      bottom: 470,
      width: 384,
      height: 420,
      toJSON: () => ({}),
    });

    fireEvent.mouseEnter(wrapper);
    expect(spotlight.style.opacity).toBe("1");

    fireEvent.mouseMove(wrapper, { clientX: 220, clientY: 190 });
    expect(spotlight.style.maskImage).toContain("120px 140px");

    fireEvent.mouseLeave(wrapper);
    expect(spotlight.style.opacity).toBe("0");
  });
});

import { useRef } from "react";
import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { usePortalAnchor, type PortalAnchorOptions } from "@/shared/lib/use-portal-anchor";

function AnchorHarness({
  open,
  triggerRect,
  options,
}: {
  open: boolean;
  triggerRect: Partial<DOMRect>;
  options?: PortalAnchorOptions;
}) {
  const triggerRef = useRef<HTMLButtonElement>(null);
  const popupRef = useRef<HTMLDivElement>(null);
  usePortalAnchor(open, triggerRef, popupRef, options);

  return (
    <div>
      <button
        ref={(node) => {
          triggerRef.current = node;
          if (node !== null) {
            node.getBoundingClientRect = () =>
              ({
                top: 0,
                left: 0,
                right: 0,
                bottom: 0,
                width: 0,
                height: 0,
                x: 0,
                y: 0,
                toJSON: () => ({}),
                ...triggerRect,
              });
          }
        }}
        data-testid="anchor-trigger"
        type="button"
      >
        trigger
      </button>
      {open ? <div ref={popupRef} data-testid="anchor-popup">menu</div> : null}
    </div>
  );
}

describe("usePortalAnchor", () => {
  it("anchors below trigger and matches trigger width by default", () => {
    Object.defineProperty(window, "innerWidth", { value: 1440, configurable: true });
    Object.defineProperty(window, "innerHeight", { value: 900, configurable: true });
    render(
      <AnchorHarness
        open
        triggerRect={{ top: 100, bottom: 132, left: 40, right: 240, width: 200, height: 32 }}
      />,
    );
    const popup = screen.getByTestId("anchor-popup");
    expect(popup.style.position).toBe("fixed");
    expect(popup.style.top).toBe("136px");
    expect(popup.style.left).toBe("40px");
    expect(popup.style.width).toBe("200px");
  });

  it("flips upward and clamps horizontally near viewport edges", () => {
    Object.defineProperty(window, "innerWidth", { value: 1024, configurable: true });
    Object.defineProperty(window, "innerHeight", { value: 768, configurable: true });
    render(
      <AnchorHarness
        open
        triggerRect={{ top: 680, bottom: 712, left: 950, right: 1010, width: 60, height: 32 }}
        options={{ matchWidth: false, fallbackWidth: 280, preferredHeight: 300, gap: 6 }}
      />,
    );
    const popup = screen.getByTestId("anchor-popup");
    expect(popup.style.bottom).toBe("94px");
    expect(popup.style.top).toBe("");
    expect(popup.style.left).toBe("736px");
    expect(popup.style.width).toBe("");
  });

  it("updates on resize and scroll events", () => {
    Object.defineProperty(window, "innerWidth", { value: 1440, configurable: true });
    Object.defineProperty(window, "innerHeight", { value: 900, configurable: true });
    render(
      <AnchorHarness
        open
        triggerRect={{ top: 120, bottom: 152, left: 900, right: 1100, width: 200, height: 32 }}
        options={{ matchWidth: false, fallbackWidth: 280 }}
      />,
    );
    Object.defineProperty(window, "innerWidth", { value: 1024, configurable: true });
    fireEvent(window, new Event("resize"));
    expect(screen.getByTestId("anchor-popup").style.left).toBe("736px");
  });
});

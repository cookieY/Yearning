import { act, render } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { BrandTypewriter } from "./brand-typewriter";

// The login headline highlight is a typewriter loop. Two behaviours are
// pinned here: the loop itself (type → hold → delete → retype) and the
// determinism contract — under prefers-reduced-motion the component mounts
// with the full phrase and never schedules a timer, which is what the
// screenshot harness (reducedMotion: reduce) relies on for stable baselines.

const PHRASE = "\u5b89\u5168\u53ef\u63a7";

function allowMotion() {
  // setup.ts overrides matchMedia to report reduced motion as matching; the
  // loop only runs when the query does not match.
  vi.stubGlobal(
    "matchMedia",
    vi.fn().mockReturnValue({
      matches: false,
      media: "",
      onchange: null,
      addListener: vi.fn(),
      removeListener: vi.fn(),
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      dispatchEvent: vi.fn(),
    }),
  );
}

function typedText(container: HTMLElement): string {
  return container.querySelector("span")?.textContent ?? "";
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

describe("BrandTypewriter", () => {
  it("mounts with the full phrase and stays settled under reduced motion", () => {
    vi.useFakeTimers();
    const { container } = render(<BrandTypewriter phrase={PHRASE} />);

    expect(typedText(container)).toBe(PHRASE);

    act(() => {
      vi.advanceTimersByTime(10_000);
    });
    expect(typedText(container)).toBe(PHRASE);
  });

  it("types the phrase out, holds, deletes it and starts over", () => {
    allowMotion();
    vi.useFakeTimers();
    const { container } = render(<BrandTypewriter phrase={PHRASE} />);

    expect(typedText(container)).toBe("");

    for (let i = 1; i <= PHRASE.length; i += 1) {
      act(() => {
        vi.advanceTimersByTime(100);
      });
      expect(typedText(container)).toBe(PHRASE.slice(0, i));
    }

    // Full phrase holds; advancing into the hold must not delete anything.
    act(() => {
      vi.advanceTimersByTime(1_000);
    });
    expect(typedText(container)).toBe(PHRASE);

    // React only re-renders (and the effect only schedules the next tick)
    // when the act scope exits, so each advance consumes exactly one timer:
    // drive the remaining hold and the per-character deletes one tick at a
    // time.
    act(() => {
      vi.advanceTimersByTime(500);
    });
    for (let i = PHRASE.length - 1; i >= 0; i -= 1) {
      act(() => {
        vi.advanceTimersByTime(60);
      });
      expect(typedText(container)).toBe(PHRASE.slice(0, i));
    }
    expect(typedText(container)).toBe("");

    // After the retype pause the loop starts typing again.
    act(() => {
      vi.advanceTimersByTime(400);
    });
    act(() => {
      vi.advanceTimersByTime(100);
    });
    expect(typedText(container)).toBe("\u5b89");
  });
});

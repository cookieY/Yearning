import * as React from "react";

import { usePrefersReducedMotion } from "@/shared/lib/use-prefers-reduced-motion";

/**
 * One-shot count-up for stat displays (owner 2026-10-23: "numeric displays on the home page
 * should have animation"). The number ticks 0 → value once when the data lands, then
 * settles; a later refresh ticks from whatever is on screen to the new value,
 * so the motion always means "this number just changed" instead of decorating
 * a static page.
 *
 * Deliberate choices:
 * - 1000ms, longer than the 500ms UI-*transition* ceiling in AGENTS.md on
 *   purpose. That ceiling governs transitions (hover, popovers, sheets) where
 *   latency reads as sluggishness. A count-up is a data animation whose whole
 *   job is to be legible: the first pass at 500ms with an expo curve dumped 59%
 *   of the distance into the first 100ms and the owner's verdict was "it finished before
 *   the eye could react" (2026-10-23). Nothing loops: the spec forbids continuous
 *   animation without business meaning.
 * - `prefers-reduced-motion` renders the final value immediately — the same
 *   preference the CSS layer already honours, read here because this animation
 *   is driven from JavaScript and cannot be neutralised by the stylesheet.
 * - The span owns its text node and React never renders children into it. A
 *   parent re-render during the tick (a sibling query resolving, a theme
 *   toggle) would otherwise re-render the settled value for one frame before
 *   the next rAF frame overwrites it — a visible flicker. With no children,
 *   React's diff has nothing to write, and the tick stays authoritative.
 * - `useLayoutEffect` paints the first frame before the browser paints, so the
 *   number never flashes its target and then jumps back to zero.
 * - `tabular-nums` belongs to the caller's class list: ticking digits must not
 *   reflow the label beside them.
 */
const DURATION_MS = 1000;

/** Quadratic ease-out, deliberately flatter than the system `--ease-out`
 * (which is an expo curve and dumps ~60% of the distance into the first 100ms).
 * At 1000ms an expo curve still reads as "done in 400ms"; this one keeps the
 * digits moving through most of the second, which is the point of a count-up —
 * the number has to be legible while it travels. */
function easeOut(progress: number): number {
  return 1 - Math.pow(1 - progress, 2);
}

export interface CountUpProps {
  /** `undefined` renders the em dash the dashboard uses for missing data. */
  value: number | undefined;
  /** Must be referentially stable (a module-level function): it participates
   * in the effect's dependencies, so an inline arrow would restart the tick on
   * every parent render. */
  format?: (value: number) => string;
  className?: string;
  testId?: string;
}

export function CountUp({
  value,
  format = String,
  className,
  testId,
}: CountUpProps): React.JSX.Element {
  const ref = React.useRef<HTMLSpanElement | null>(null);
  const reduced = usePrefersReducedMotion();
  /** Last number actually painted, so an interrupted tick resumes from it
   * instead of snapping back to zero. */
  const painted = React.useRef<number | null>(null);

  React.useLayoutEffect(() => {
    const node = ref.current;
    if (node === null) {
      return;
    }
    if (value === undefined) {
      node.textContent = "—";
      painted.current = null;
      return;
    }
    const from = painted.current ?? 0;
    if (reduced || from === value) {
      node.textContent = format(value);
      painted.current = value;
      return;
    }
    node.textContent = format(from);
    painted.current = from;
    const started = performance.now();
    let frame = 0;
    const tick = (now: number): void => {
      // `now` is the frame's start timestamp, which can predate `started` by a
      // frame. Unclamped, a negative progress would make easeOut negative and
      // the first painted value could read "-1s".
      const progress = Math.min(1, Math.max(0, (now - started) / DURATION_MS));
      const next = Math.round(from + (value - from) * easeOut(progress));
      node.textContent = format(next);
      painted.current = next;
      if (progress < 1) {
        frame = requestAnimationFrame(tick);
      }
    };
    frame = requestAnimationFrame(tick);
    return () => {
      cancelAnimationFrame(frame);
    };
  }, [value, format, reduced]);

  // No children on purpose — see the header comment. The text is written by the
  // layout effect above, which runs before the first paint.
  return <span ref={ref} className={className} data-testid={testId} />;
}
